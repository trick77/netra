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
