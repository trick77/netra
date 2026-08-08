package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/store"
)

// The 1h views are ~60 lines of near-identical avg(X_avg) / max(X_max) pairs
// -- disk_io_samples_1h alone has sixteen. A transposition there
// (avg(read_bytes_max) where avg(read_bytes_avg) was meant, or w_await
// rolling up r_await) produces a view that materialises cleanly, is queried
// without error, and is wrong forever. Asserting the aggregates merely EXIST
// cannot see it.
//
// This is newcolumns_test.go's argument applied to the rollups: every value
// is distinct, so a transposition lands on a named column instead of hiding
// behind two equal numbers. With one 5m bucket per family, avg and max of the
// same column still differ, which is what catches an avg/max swap in the 1h
// tier.
func TestIntegrationGroup1AggregatesComputeTheRightNumbers(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)
	ts := recentBucket()

	sensorID := seedSensor(t, s, hostID, "coretemp", "Package id 0")

	families := []struct {
		table string
		// dimension is the column that joins the two rows of one scrape;
		// both sample rows share its value here, since the arithmetic under
		// test is within a series, not across one.
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

	for _, f := range families {
		// Column i gets 10*(i+1) in the first row and 10*(i+1)+2 in the
		// second, so every avg (10i+11) and every max (10i+12) is unique
		// across the whole family. Two columns holding the same number would
		// let a swap through, which is the entire point.
		lo := make([]float64, len(f.columns))
		hi := make([]float64, len(f.columns))
		for i := range f.columns {
			lo[i] = float64(10 * (i + 1))
			hi[i] = lo[i] + 2
		}

		insertAggregateRow(t, s, f.table, f.dimension, f.dimValue, hostID, ts, f.columns, lo)
		insertAggregateRow(t, s, f.table, f.dimension, f.dimValue, hostID, ts.Add(time.Minute), f.columns, hi)

		refreshTiers(t, s, f.table)

		for _, tier := range []string{"5m", "1h"} {
			for i, col := range f.columns {
				wantAvg := lo[i] + 1
				wantMax := hi[i]
				gotAvg := queryFloat(t, s, f.table+"_"+tier, col+"_avg", f.dimension, f.dimValue, hostID)
				gotMax := queryFloat(t, s, f.table+"_"+tier, col+"_max", f.dimension, f.dimValue, hostID)
				if gotAvg != wantAvg {
					t.Errorf("%s_%s.%s_avg = %v, want %v", f.table, tier, col, gotAvg, wantAvg)
				}
				if gotMax != wantMax {
					t.Errorf("%s_%s.%s_max = %v, want %v", f.table, tier, col, gotMax, wantMax)
				}
			}
		}
	}
}

// collector_samples rolls up counts rather than avg/max pairs, and those are
// the aggregates least certain to survive a continuous aggregate: count(*)
// and sum((NOT ok)::INTEGER) in the 5m tier, summed again in the 1h tier,
// with last(error_code, bucket) carrying the most recent failure forward.
// Three scrapes, two of them failures, so a count and a failure count that
// were accidentally the same expression are distinguishable.
func TestIntegrationCollectorSamplesAggregateCountsFailures(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)
	ts := recentBucket()

	rows := []struct {
		offset    time.Duration
		duration  int
		ok        bool
		errorCode any
	}{
		{0, 4, true, nil},
		{time.Minute, 9, false, "permission_denied"},
		{2 * time.Minute, 6, false, "timeout"},
	}
	for _, r := range rows {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO collector_samples (host_id, ts, collector, duration_ms, ok, error_code)
			 VALUES ($1, $2, 'sensors', $3, $4, $5)`,
			hostID, ts.Add(r.offset), r.duration, r.ok, r.errorCode); err != nil {
			t.Fatalf("insert collector sample: %v", err)
		}
	}

	refreshTiers(t, s, "collector_samples")

	for _, tier := range []string{"5m", "1h"} {
		view := "collector_samples_" + tier
		var sampleCount, failureCount int64
		var durationAvg float64
		var durationMax int
		var errorCode string
		if err := s.Pool().QueryRow(ctx,
			`SELECT sample_count, failure_count, duration_ms_avg, duration_ms_max, error_code
			   FROM `+view+` WHERE host_id = $1 AND collector = 'sensors'`,
			hostID).Scan(&sampleCount, &failureCount, &durationAvg, &durationMax, &errorCode); err != nil {
			t.Fatalf("query %s: %v", view, err)
		}

		if sampleCount != 3 {
			t.Errorf("%s.sample_count = %d, want 3", view, sampleCount)
		}
		if failureCount != 2 {
			t.Errorf("%s.failure_count = %d, want 2", view, failureCount)
		}
		// (4+9+6)/3, and the slowest of the three.
		if durationAvg != 19.0/3.0 {
			t.Errorf("%s.duration_ms_avg = %v, want %v", view, durationAvg, 19.0/3.0)
		}
		if durationMax != 9 {
			t.Errorf("%s.duration_ms_max = %d, want 9", view, durationMax)
		}
		// The last failure in the bucket, not the first and not the NULL
		// belonging to the successful scrape.
		if errorCode != "timeout" {
			t.Errorf("%s.error_code = %q, want \"timeout\"", view, errorCode)
		}
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
func refreshTiers(t *testing.T, s *store.Store, table string) {
	t.Helper()

	for _, tier := range []string{"5m", "1h"} {
		if _, err := s.Pool().Exec(context.Background(),
			fmt.Sprintf(`CALL refresh_continuous_aggregate('%s_%s',
				(now() - interval '12 hours')::timestamptz, now()::timestamptz)`, table, tier)); err != nil {
			t.Fatalf("refresh %s_%s: %v", table, tier, err)
		}
	}
}

// queryFloat reads one aggregated column for one series.
func queryFloat(t *testing.T, s *store.Store, view, column, dimension string, dimValue any, hostID int32) float64 {
	t.Helper()

	var got float64
	sql := fmt.Sprintf(`SELECT %s FROM %s WHERE host_id = $1 AND %s = $2`, column, view, dimension)
	if err := s.Pool().QueryRow(context.Background(), sql, hostID, dimValue).Scan(&got); err != nil {
		t.Fatalf("query %s.%s: %v", view, column, err)
	}
	return got
}
