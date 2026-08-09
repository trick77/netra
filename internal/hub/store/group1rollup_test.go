package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/trick77/netra/internal/hub/store"
)

// rollupHour is a timestamp about an hour back, truncated to the hour, so a
// test can place rows in several distinct 5m buckets that all share ONE 1h
// bucket. recentBucket() truncates to 5 minutes instead, which is right for a
// single-scrape test but would let a "+7 minutes" row cross an hour boundary
// whenever the truncated time landed at :55.
//
// An hour back keeps it inside every retention policy and inside the window
// refreshTiers materialises, while sitting in buckets that have closed.
func rollupHour() time.Time {
	return time.Now().UTC().Add(-time.Hour).Truncate(time.Hour)
}

// The 1h views are ~60 lines of near-identical avg(X_avg) / max(X_max) pairs
// -- disk_io_samples_1h alone has sixteen. A transposition there
// (avg(read_bytes_max) where avg(read_bytes_avg) was meant, or w_await
// rolling up r_await) produces a view that materialises cleanly, is queried
// without error, and is wrong forever. Asserting the aggregates merely EXIST
// cannot see it.
//
// Two things are needed to make one visible, and they catch different
// mistakes. Distinct values per column catch a COLUMN transposition, which is
// newcolumns_test.go's argument applied to the rollups. Two 5m buckets per
// series catch a FUNCTION transposition: with only one input row per series
// the 1h tier's avg, max, min and sum are all the same number, so max-where-
// avg-was-meant passes silently.
func TestIntegrationGroup1AggregatesComputeTheRightNumbers(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)
	base := rollupHour()

	sensorID := seedSensor(t, s, hostID, "coretemp", "Package id 0")

	families := []struct {
		table string
		// dimension is the column that joins these rows into one series;
		// every row here shares its value, since the arithmetic under test is
		// within a series, not across one.
		dimension string
		dimValue  any
		columns   []string
	}{
		{"cpu_core_samples", "core", int32(3), []string{"busy"}},
		{"sensor_samples", "sensor_id", sensorID, []string{"temp"}},
		{"net_samples", "iface", "eth0", []string{"rx_bytes", "tx_bytes", "rx_errs", "tx_errs"}},
		{"disk_io_samples", "device", "nvme0n1", []string{
			"read_bytes", "write_bytes", "read_ops", "write_ops",
			"io_util_pct", "r_await_ms", "w_await_ms", "weighted_io_pct",
		}},
	}

	// Rows 0 and 1 land in the first 5m bucket, rows 2 and 3 in the second.
	offsets := []time.Duration{0, time.Minute, 6 * time.Minute, 7 * time.Minute}

	for _, f := range families {
		// Column i is centred on 100*(i+1), so no two columns anywhere in the
		// family share a value.
		values := make([][]float64, len(f.columns))
		for i := range f.columns {
			c := float64(100 * (i + 1))
			values[i] = []float64{c, c + 2, c + 6, c + 10}
		}

		for row, offset := range offsets {
			rowValues := make([]float64, len(f.columns))
			for i := range f.columns {
				rowValues[i] = values[i][row]
			}
			insertAggregateRow(t, s, f.table, f.dimension, f.dimValue, hostID, base.Add(offset), f.columns, rowValues)
		}

		refreshTiers(t, s, f.table)

		for i, col := range f.columns {
			c := values[i]
			wantBucketAvg := []float64{(c[0] + c[1]) / 2, (c[2] + c[3]) / 2}
			wantBucketMax := []float64{c[1], c[3]}

			assertTier(t, s, f.table+"_5m", col+"_avg", f.dimension, f.dimValue, hostID, wantBucketAvg)
			assertTier(t, s, f.table+"_5m", col+"_max", f.dimension, f.dimValue, hostID, wantBucketMax)

			// avg-of-avgs across the two buckets, and max-of-maxes. Chosen so
			// these two differ from each other AND from either bucket's own
			// avg and max, which is what a function transposition trips over.
			assertTier(t, s, f.table+"_1h", col+"_avg", f.dimension, f.dimValue, hostID,
				[]float64{(wantBucketAvg[0] + wantBucketAvg[1]) / 2})
			assertTier(t, s, f.table+"_1h", col+"_max", f.dimension, f.dimValue, hostID,
				[]float64{wantBucketMax[1]})
		}
	}
}

// collector_samples rolls up counts rather than avg/max pairs, and those are
// the aggregates least certain to survive a continuous aggregate: count(*)
// and sum((NOT ok)::INTEGER) in the 5m tier, summed again in the 1h tier,
// with last(error_code, bucket) carrying the most recent failure forward.
//
// Three 5m buckets, for the same reason as above, and with deliberately
// different row counts: three scrapes, then two, then two. sum (7) is then
// distinct from max (3) and from every bucket, so a sum silently written as a
// max fails.
//
// The third bucket is the one where the collector RECOVERS, and it is there
// for error_code specifically. last(error_code, ts) must carry the bucket's
// NULL forward, not the last non-NULL it saw: a collector that failed at
// 09:03 and has been fine since must not still be reported as broken. With
// only the first two buckets the failure was last in both, so the assertion
// held whether last() returned the NULL at the maximum ts or skipped NULLs
// entirely — and a rewrite to last(error_code, ts) FILTER (WHERE NOT ok)
// would have made error_code permanently sticky with every test still green.
func TestIntegrationCollectorSamplesAggregateCountsFailures(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)
	base := rollupHour()

	rows := []struct {
		offset    time.Duration
		duration  int
		ok        bool
		errorCode any
	}{
		// First 5m bucket: three scrapes, two of them failures. Durations
		// average exactly, so the assertions below need no epsilon.
		{0, 4, true, nil},
		{time.Minute, 8, false, "permission_denied"},
		{2 * time.Minute, 6, false, "timeout"},
		// Second 5m bucket: two scrapes, one failure.
		{6 * time.Minute, 20, true, nil},
		{7 * time.Minute, 30, false, "io_error"},
		// Third 5m bucket: the collector has recovered. error_code must be
		// NULL here and in the hour that contains it.
		{12 * time.Minute, 40, true, nil},
		{13 * time.Minute, 60, true, nil},
	}
	for _, r := range rows {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO collector_samples (host_id, ts, collector, duration_ms, ok, error_code)
			 VALUES ($1, $2, 'sensors', $3, $4, $5)`,
			hostID, base.Add(r.offset), r.duration, r.ok, r.errorCode); err != nil {
			t.Fatalf("insert collector sample: %v", err)
		}
	}

	refreshTiers(t, s, "collector_samples")

	// (4+8+6)/3, (20+30)/2, (40+60)/2.
	firstAvg := 6.0
	secondAvg := 25.0
	thirdAvg := 50.0

	assertTier(t, s, "collector_samples_5m", "sample_count", "collector", "sensors", hostID, []float64{3, 2, 2})
	assertTier(t, s, "collector_samples_5m", "failure_count", "collector", "sensors", hostID, []float64{2, 1, 0})
	assertTier(t, s, "collector_samples_5m", "duration_ms_avg", "collector", "sensors", hostID, []float64{firstAvg, secondAvg, thirdAvg})
	assertTier(t, s, "collector_samples_5m", "duration_ms_max", "collector", "sensors", hostID, []float64{8, 30, 60})

	assertTier(t, s, "collector_samples_1h", "sample_count", "collector", "sensors", hostID, []float64{7})
	assertTier(t, s, "collector_samples_1h", "failure_count", "collector", "sensors", hostID, []float64{3})
	assertTier(t, s, "collector_samples_1h", "duration_ms_avg", "collector", "sensors", hostID,
		[]float64{(firstAvg + secondAvg + thirdAvg) / 3})
	assertTier(t, s, "collector_samples_1h", "duration_ms_max", "collector", "sensors", hostID, []float64{60})

	// The last failure in each window, not the first — and NULL once the
	// collector recovers, rather than the last error still standing.
	assertTextTier(t, s, "collector_samples_5m", "error_code", hostID,
		[]*string{ptr("timeout"), ptr("io_error"), nil})
	assertTextTier(t, s, "collector_samples_1h", "error_code", hostID, []*string{nil})
}

// sum(bigint) returns numeric, so collector_samples_1h's counts would have a
// different column type from collector_samples_5m's without an explicit cast.
// pgx scans numeric into int64 transparently, which is exactly why no
// value-based assertion can see this: it surfaces in 1D, where tier selection
// reads both tiers through one query path and a shared scan target or JSON
// encoder gets a different type depending on the range requested.
func TestIntegrationCollectorSampleCountsAreBigintInBothTiers(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, view := range []string{"collector_samples_5m", "collector_samples_1h"} {
		for _, column := range []string{"sample_count", "failure_count"} {
			var dataType string
			if err := s.Pool().QueryRow(ctx,
				`SELECT data_type FROM information_schema.columns
				  WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`,
				view, column).Scan(&dataType); err != nil {
				t.Fatalf("query %s.%s type: %v", view, column, err)
			}
			if dataType != "bigint" {
				t.Errorf("%s.%s is %s, want bigint", view, column, dataType)
			}
		}
	}
}

// drop_chunks removes a chunk only once its NEWEST row is past the cutoff, so
// a tier really retains up to retention + chunk_interval. Timescale defaults
// to 7-day raw chunks and sizes continuous-aggregate chunks at 10x that, so
// the 5m tier was retaining ~100 days against a stated 30. Nothing reports
// it; the disk just fills.
//
// The bound is retention/4 rather than an exact interval per table, so this
// stays a statement about the property rather than a second copy of the
// migration's numbers.
func TestIntegrationChunkIntervalIsSmallEnoughForItsRetention(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// The interval comparison stays in SQL: time_interval is a Postgres
	// interval, which pgx does not scan into a time.Duration, and dividing
	// intervals is something Postgres does correctly for free.
	rows, err := s.Pool().Query(ctx,
		`SELECT j.hypertable_name,
		        j.config ->> 'drop_after'      AS drop_after,
		        d.time_interval::TEXT          AS chunk_interval,
		        d.time_interval <= (j.config ->> 'drop_after')::INTERVAL / 4 AS ok
		   FROM timescaledb_information.jobs j
		   JOIN timescaledb_information.dimensions d
		     ON d.hypertable_name = COALESCE(
		          (SELECT ca.materialization_hypertable_name
		             FROM timescaledb_information.continuous_aggregates ca
		            WHERE ca.view_name = j.hypertable_name),
		          j.hypertable_name)
		  WHERE j.proc_name = 'policy_retention'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var name, dropAfter, chunkInterval string
		var ok bool
		if err := rows.Scan(&name, &dropAfter, &chunkInterval, &ok); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++

		if !ok {
			t.Errorf("%s chunk interval %s against retention %s: a chunk is dropped only "+
				"when its newest row expires, so this retains far longer than stated",
				name, chunkInterval, dropAfter)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	// Seven raw hypertables and fourteen continuous aggregates.
	if seen != 21 {
		t.Fatalf("retention policies with a chunk interval = %d, want 21", seen)
	}
}

// Migrate() records 0001 in schema_migrations only after every statement has
// succeeded, and matches by filename with no checksum -- so calling Migrate
// twice (TestIntegrationMigrateIsIdempotent) proves the runner SKIPS a
// recorded migration and proves nothing about the file being re-runnable.
//
// The scenario the file header is written for is the other one: a failure
// part-way through leaves the schema half-built and the migration unrecorded,
// and the hub re-runs the file from the top on its next start. Deleting the
// schema_migrations row reproduces exactly that. Any statement missing an
// IF NOT EXISTS, or any add_*_policy that errors rather than warns on re-add,
// fails here.
func TestIntegrationMigrationIsRerunnableAgainstItsOwnSchema(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}

	if _, err := s.Pool().Exec(ctx, `DELETE FROM schema_migrations`); err != nil {
		t.Fatalf("clear schema_migrations: %v", err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("re-running 0001 against its own schema: %v", err)
	}

	// Re-running must not duplicate anything either: a second
	// create_hypertable or add_retention_policy that quietly registered a
	// twin would double the policy counts rollup_test.go pins.
	var hypertables, policies int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.hypertables`).Scan(&hypertables); err != nil {
		t.Fatalf("count hypertables: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.jobs
		  WHERE proc_name IN ('policy_retention', 'policy_refresh_continuous_aggregate')`).Scan(&policies); err != nil {
		t.Fatalf("count policies: %v", err)
	}

	// Seven. timescaledb_information.hypertables lists user hypertables only;
	// the fourteen internal materialisation hypertables backing the
	// continuous aggregates are not exposed there.
	if hypertables != 7 {
		t.Errorf("hypertables after re-run = %d, want 7", hypertables)
	}
	// 21 retention policies (7 raw + 14 aggregate) and 14 refresh policies.
	if policies != 35 {
		t.Errorf("policies after re-run = %d, want 35", policies)
	}
}

// insertAggregateRow writes one sample row with the given value per column.
func insertAggregateRow(t *testing.T, s *store.Store, table, dimension string, dimValue any,
	hostID int32, ts time.Time, columns []string, values []float64,
) {
	t.Helper()

	args := []any{hostID, ts, dimValue}
	placeholders := []string{"$1", "$2", "$3"}
	for i, v := range values {
		args = append(args, v)
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+4))
	}

	sql := fmt.Sprintf(`INSERT INTO %s (host_id, ts, %s, %s) VALUES (%s)`,
		table, dimension, strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	if _, err := s.Pool().Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("insert into %s: %v", table, err)
	}
}

// refreshTiers materialises the 5m tier and then the 1h tier that reads it.
// Order matters: the 1h aggregate is built on the 5m aggregate, so refreshing
// it first would roll up nothing.
//
// The refresh policy jobs are unscheduled first. TimescaleDB's scheduler runs
// a newly created policy within seconds rather than at its nominal
// schedule_interval -- the same behaviour resetSchema in testing.go documents
// at length -- and a manual refresh that collides with one fails outright
// with "concurrent refresh" (SQLSTATE 55P03) rather than waiting. With
// fourteen aggregates now in the schema that stopped being a rare race and
// started being most runs.
//
// Unscheduling is per-test: OpenTest drops the schema, so nothing leaks into
// another test, and the jobs still exist for the tests that count them.
func refreshTiers(t *testing.T, s *store.Store, table string) {
	t.Helper()

	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`SELECT alter_job(job_id, scheduled => false)
		   FROM timescaledb_information.jobs
		  WHERE proc_name = 'policy_refresh_continuous_aggregate'`); err != nil {
		t.Fatalf("unschedule refresh policies: %v", err)
	}

	for _, tier := range []string{"5m", "1h"} {
		view := fmt.Sprintf("%s_%s", table, tier)
		refreshAggregate(t, s, view)
	}
}

// concurrentRefreshSQLState is TimescaleDB's error for a refresh that
// overlaps another one already in progress.
const concurrentRefreshSQLState = "55P03"

// refreshAggregate refreshes one continuous aggregate, retrying while a
// background job dispatched before refreshTiers unscheduled it is still in
// flight. Unscheduling removes the routine cause of the collision but cannot
// recall a worker that has already started, which is the same residual race
// resetSchema retries for.
func refreshAggregate(t *testing.T, s *store.Store, view string) {
	t.Helper()
	refreshAggregateRange(t, s, view, "now() - interval '12 hours'", "now()")
}

// refreshAggregateRange is refreshAggregate over an explicit window, for a
// test that cares which buckets the refresh covers rather than wanting every
// bucket it wrote. from and to are SQL expressions, not values, so a caller
// can express the window relative to now() the way the policies do.
func refreshAggregateRange(t *testing.T, s *store.Store, view, from, to string) {
	t.Helper()

	sql := fmt.Sprintf(`CALL refresh_continuous_aggregate('%s',
		(%s)::timestamptz, (%s)::timestamptz)`, view, from, to)

	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		_, err := s.Pool().Exec(context.Background(), sql)
		if err == nil {
			return
		}
		lastErr = err

		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != concurrentRefreshSQLState {
			t.Fatalf("refresh %s: %v", view, err)
		}
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
	}
	t.Fatalf("refresh %s: still colliding after 5 attempts: %v", view, lastErr)
}

// assertTier reads one aggregated column for one series across every bucket
// in the view, oldest first, and compares it to want.
func assertTier(t *testing.T, s *store.Store, view, column, dimension string, dimValue any,
	hostID int32, want []float64,
) {
	t.Helper()

	// Cast in SQL: these columns are variously double precision, bigint and
	// numeric (avg of an integer column), and pgx will not scan the latter
	// two into a float64. The column types themselves are asserted by
	// TestIntegrationCollectorSampleCountsAreBigintInBothTiers, so casting
	// here hides nothing.
	sql := fmt.Sprintf(
		`SELECT (%s)::DOUBLE PRECISION FROM %s WHERE host_id = $1 AND %s = $2 ORDER BY bucket`,
		column, view, dimension)
	rows, err := s.Pool().Query(context.Background(), sql, hostID, dimValue)
	if err != nil {
		t.Fatalf("query %s.%s: %v", view, column, err)
	}
	defer rows.Close()

	var got []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan %s.%s: %v", view, column, err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s.%s: %v", view, column, err)
	}

	if !equalFloats(got, want) {
		t.Errorf("%s.%s = %v, want %v", view, column, got, want)
	}
}

// ptr returns a pointer to v, so a caller can spell a non-NULL expectation
// inline next to a nil one.
func ptr[T any](v T) *T { return &v }

// assertTextTier is assertTier for a nullable text column, hard-wired to the
// one collector series these tests write. It scans into *string rather than
// string on purpose: a NULL error_code is the value that says the collector
// recovered, and scanning into string would fail on it rather than compare
// it.
func assertTextTier(t *testing.T, s *store.Store, view, column string, hostID int32, want []*string) {
	t.Helper()

	sql := fmt.Sprintf(
		`SELECT %s FROM %s WHERE host_id = $1 AND collector = 'sensors' ORDER BY bucket`, column, view)
	rows, err := s.Pool().Query(context.Background(), sql, hostID)
	if err != nil {
		t.Fatalf("query %s.%s: %v", view, column, err)
	}
	defer rows.Close()

	var got []*string
	for rows.Next() {
		var v *string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan %s.%s: %v", view, column, err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s.%s: %v", view, column, err)
	}

	if !equalTextPtrs(got, want) {
		t.Errorf("%s.%s = %v, want %v", view, column, formatTextPtrs(got), formatTextPtrs(want))
	}
}

// equalTextPtrs compares two nullable-text slices, treating nil as NULL.
func equalTextPtrs(got, want []*string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		switch {
		case got[i] == nil && want[i] == nil:
		case got[i] == nil || want[i] == nil:
			return false
		case *got[i] != *want[i]:
			return false
		}
	}
	return true
}

// formatTextPtrs renders a nullable-text slice for a failure message, so a
// NULL reads as NULL rather than as a pointer address.
func formatTextPtrs(v []*string) []string {
	out := make([]string, len(v))
	for i, s := range v {
		if s == nil {
			out[i] = "NULL"
			continue
		}
		out[i] = *s
	}
	return out
}

// equalFloats compares two float slices exactly. The test values are chosen
// so every expected number is exactly representable, so an epsilon would only
// hide a real disagreement.
func equalFloats(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
