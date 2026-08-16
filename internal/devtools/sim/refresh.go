package sim

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// aggregate is one continuous aggregate and the width of its time bucket.
// The width is carried because a refresh window has to be expressed in that
// aggregate's buckets, not in wall-clock time -- see Refresh.
type aggregate struct {
	view   string
	bucket time.Duration
}

// aggregatePairs is every continuous aggregate in the schema, as (5m, 1h)
// pairs.
//
// The ORDER WITHIN A PAIR IS LOAD-BEARING. Each _1h view selects FROM the
// matching _5m view, never from the raw hypertable -- so refreshing 1h first
// reads an empty 5m tier and materialises nothing at all, silently. The hub
// never hits this because its scheduled policies always run against data the
// 5m tier has long since covered; a backfill is the one case where the order
// is visible.
var aggregatePairs = [][2]aggregate{
	{{"host_samples_5m", tier5m}, {"host_samples_1h", tier1h}},
	{{"host_snmp_samples_5m", tier5m}, {"host_snmp_samples_1h", tier1h}},
	{{"agent_samples_5m", tier5m}, {"agent_samples_1h", tier1h}},
	{{"cpu_core_samples_5m", tier5m}, {"cpu_core_samples_1h", tier1h}},
	{{"disk_io_samples_5m", tier5m}, {"disk_io_samples_1h", tier1h}},
	{{"sensor_samples_5m", tier5m}, {"sensor_samples_1h", tier1h}},
	{{"net_samples_5m", tier5m}, {"net_samples_1h", tier1h}},
	{{"collector_samples_5m", tier5m}, {"collector_samples_1h", tier1h}},
	{{"container_samples_5m", tier5m}, {"container_samples_1h", tier1h}},
	{{"filesystem_samples_5m", tier5m}, {"filesystem_samples_1h", tier1h}},
}

const (
	tier5m = 5 * time.Minute
	tier1h = time.Hour
)

// Refresher materialises the continuous aggregates over a backfilled range.
//
// This exists because the hub's own policies only look back six to twelve
// hours: data inserted three months into the past is invalidated correctly by
// TimescaleDB, but no scheduled job ever reaches back far enough to
// re-materialise it. Without this the raw tables would fill up, the 5m and 1h
// views would stay empty, and then the raw retention policy would delete the
// only copy.
type Refresher struct {
	pool *pgxpool.Pool
}

// NewRefresher connects to the hub's database. The DSN must point at the
// database the hub actually uses -- notably NOT the integration-test
// database, whose schema is dropped and recreated by other test runs.
func NewRefresher(ctx context.Context, dsn string) (*Refresher, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Refresher{pool: pool}, nil
}

// Close releases the pool.
func (r *Refresher) Close() {
	if r != nil && r.pool != nil {
		r.pool.Close()
	}
}

// Refresh materialises every aggregate over [from,to).
//
// The window is EXPANDED OUTWARD to whole buckets, per aggregate, and that is
// the whole point of this function rather than an implementation detail.
// refresh_continuous_aggregate inscribes the window it is given: it
// materialises only the buckets that fall ENTIRELY inside it. A caller that
// tiles a backfill in wall-clock time therefore leaves the bucket straddling
// every segment boundary materialised by nobody -- the segment before it ends
// mid-bucket and the segment after starts mid-bucket. A 90-day run begun at
// 08:31 lost eighteen hourly buckets that way, one per boundary, silently and
// permanently once raw retention expired.
//
// Expanding is safe where clamping is not: a refresh is idempotent, so the
// overlap between adjacent expanded windows costs a little work and changes
// nothing. It also removes the other half of the same bug -- a window shorter
// than one bucket, which a short --backfill produces and which
// refresh_continuous_aggregate rejects outright with "refresh window too
// small", aborting a run whose data had already landed.
//
// A refresh cannot run inside a transaction. The range is NOT clamped away
// from the window the scheduled policies work on: the last backfill segment
// runs right up to now, straight through the 5m policy's [now-6h, now-10m].
// The two can collide, and a collision is reported rather than retried
// silently -- re-running the simulator is idempotent, so the cheap answer to
// a losing race is to run it again.
func (r *Refresher) Refresh(ctx context.Context, from, to time.Time) error {
	for _, pair := range aggregatePairs {
		for _, agg := range pair {
			from, to := bucketWindow(from, to, agg.bucket)
			view := agg.view
			// CALL, not SELECT: refresh_continuous_aggregate is a procedure,
			// and it commits internally per chunk.
			//
			// The casts are required, not decorative. The procedure's window
			// arguments are declared ANYELEMENT, so an uncast parameter gives
			// Postgres nothing to infer from and the call fails with
			// "could not determine data type of parameter $1".
			sql := fmt.Sprintf(
				"CALL refresh_continuous_aggregate('%s', $1::timestamptz, $2::timestamptz)", view)
			if _, err := r.pool.Exec(ctx, sql, from, to); err != nil {
				return fmt.Errorf("refresh %s over [%s,%s): %w",
					view, from.Format(time.RFC3339), to.Format(time.RFC3339), err)
			}
		}
	}
	return nil
}

// bucketWindow expands [from,to) outward to whole buckets of the given width,
// so every bucket the caller's window touches falls entirely inside the
// result and is therefore materialised.
//
// Truncate works on absolute time since the epoch, which is what makes this
// agree with time_bucket: both align to the epoch rather than to midnight
// local, so a 5-minute bucket starts at :00, :05, :10 in UTC regardless of
// the process's timezone.
func bucketWindow(from, to time.Time, bucket time.Duration) (time.Time, time.Time) {
	start := from.Truncate(bucket)
	end := to.Truncate(bucket)
	if end.Before(to) {
		end = end.Add(bucket)
	}
	return start, end
}
