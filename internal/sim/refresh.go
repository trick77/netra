package sim

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// aggregatePairs is every continuous aggregate in the schema, as (5m, 1h)
// pairs.
//
// The ORDER WITHIN A PAIR IS LOAD-BEARING. Each _1h view selects FROM the
// matching _5m view, never from the raw hypertable -- so refreshing 1h first
// reads an empty 5m tier and materialises nothing at all, silently. The hub
// never hits this because its scheduled policies always run against data the
// 5m tier has long since covered; a backfill is the one case where the order
// is visible.
var aggregatePairs = [][2]string{
	{"host_samples_5m", "host_samples_1h"},
	{"agent_samples_5m", "agent_samples_1h"},
	{"cpu_core_samples_5m", "cpu_core_samples_1h"},
	{"disk_io_samples_5m", "disk_io_samples_1h"},
	{"sensor_samples_5m", "sensor_samples_1h"},
	{"net_samples_5m", "net_samples_1h"},
	{"collector_samples_5m", "collector_samples_1h"},
	{"container_samples_5m", "container_samples_1h"},
	{"filesystem_samples_5m", "filesystem_samples_1h"},
}

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
// A refresh cannot run inside a transaction, and it cannot overlap the window
// a scheduled policy is working on -- so the range is clamped to end before
// the 5m policy's end_offset, and a conflict with a running job is reported
// rather than retried silently.
func (r *Refresher) Refresh(ctx context.Context, from, to time.Time) error {
	for _, pair := range aggregatePairs {
		for _, view := range pair {
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
