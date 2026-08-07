package store_test

import (
	"context"
	"testing"

	"github.com/trick77/netra/internal/hub/store"
)

func TestIntegrationHostSamplesIsHypertable(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.hypertables
		 WHERE hypertable_name = 'host_samples'`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("host_samples hypertable count = %d, want 1", n)
	}
}

// start_offset must exceed the agent ring-buffer window, or data replayed
// after an outage is recorded as invalid but never re-materialised, leaving
// the rollup permanently wrong. Buffer is 1h, so the floor is 1h.
func TestIntegrationRefreshPolicyStartOffsetExceedsBufferWindow(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rows, err := s.Pool().Query(ctx,
		`SELECT config ->> 'start_offset'
		   FROM timescaledb_information.jobs
		  WHERE proc_name = 'policy_refresh_continuous_aggregate'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var startOffset string
		if err := rows.Scan(&startOffset); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++

		var greater bool
		if err := s.Pool().QueryRow(ctx,
			`SELECT $1::interval > interval '1 hour'`, startOffset).Scan(&greater); err != nil {
			t.Fatalf("compare: %v", err)
		}
		if !greater {
			t.Fatalf("start_offset = %s, want greater than the 1h buffer window", startOffset)
		}
	}
	if seen != 2 {
		t.Fatalf("refresh policies found = %d, want 2 (5m and 1h aggregates)", seen)
	}
}

// TestIntegrationBackfillOlderThanStartOffsetIsExcludedFromRollup is the
// regression spec §10 and §14 both call for: a sample older than the
// continuous aggregate's start_offset must not appear in the rollup after a
// refresh, while a sample within the window must. Asserting only that the
// migration's start_offset config says "6 hours" (as the tests above do)
// restates the constraint; this exercises it against real TimescaleDB
// refresh behaviour, which is the part that can silently regress.
func TestIntegrationBackfillOlderThanStartOffsetIsExcludedFromRollup(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('rollup-host') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	// Mirrors host_samples_5m's policy: start_offset 6h, end_offset 10m.
	// tooOld sits outside the refreshed range and must never be
	// materialised; withinWindow sits inside it and must be.
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_samples (host_id, ts, cpu_total)
		 VALUES ($1, now() - interval '8 hours', $2)`, hostID, 11.0); err != nil {
		t.Fatalf("insert too-old sample: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_samples (host_id, ts, cpu_total)
		 VALUES ($1, now() - interval '1 hour', $2)`, hostID, 22.0); err != nil {
		t.Fatalf("insert within-window sample: %v", err)
	}

	if _, err := s.Pool().Exec(ctx,
		`CALL refresh_continuous_aggregate('host_samples_5m',
			(now() - interval '6 hours')::timestamptz,
			(now() - interval '10 minutes')::timestamptz)`); err != nil {
		t.Fatalf("refresh_continuous_aggregate: %v", err)
	}

	var tooOldCount int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples_5m
		  WHERE host_id = $1 AND bucket < now() - interval '6 hours'`, hostID).Scan(&tooOldCount); err != nil {
		t.Fatalf("query too-old bucket: %v", err)
	}
	if tooOldCount != 0 {
		t.Fatalf("rollup rows older than start_offset = %d, want 0 (silently excluded forever)", tooOldCount)
	}

	var withinWindowCount int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples_5m
		  WHERE host_id = $1 AND bucket >= now() - interval '6 hours'
		    AND bucket <= now() - interval '10 minutes'`, hostID).Scan(&withinWindowCount); err != nil {
		t.Fatalf("query within-window bucket: %v", err)
	}
	if withinWindowCount == 0 {
		t.Fatal("rollup rows within the refreshed window = 0, want at least 1")
	}
}

func TestIntegrationRawRetentionExceedsRefreshLag(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var dropAfter string
	if err := s.Pool().QueryRow(ctx,
		`SELECT config ->> 'drop_after'
		   FROM timescaledb_information.jobs
		  WHERE proc_name = 'policy_retention'
		    AND hypertable_name = 'host_samples'`).Scan(&dropAfter); err != nil {
		t.Fatalf("query: %v", err)
	}

	var ok bool
	if err := s.Pool().QueryRow(ctx,
		`SELECT $1::interval > interval '6 hours'`, dropAfter).Scan(&ok); err != nil {
		t.Fatalf("compare: %v", err)
	}
	if !ok {
		t.Fatalf("raw drop_after = %s, want greater than the 6h start_offset", dropAfter)
	}
}
