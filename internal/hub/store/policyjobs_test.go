package store_test

import (
	"context"
	"testing"

	"github.com/trick77/netra/internal/hub/store"
)

// A test database must have TimescaleDB's policy jobs REGISTERED BUT NOT
// SCHEDULED.
//
// Registered, because several tests count them to prove the migration created
// them, and because a hub in production genuinely needs them.
//
// Not scheduled, because the scheduler fires newly created jobs within seconds
// and policy_retention drops chunks under AccessExclusiveLock. A test that
// deletes a host takes RowExclusiveLock across the same hypertables through
// the ON DELETE CASCADE, the two deadlock, and whichever side Postgres kills
// loses -- so the suite failed intermittently with SQLSTATE 40P01 and nothing
// in the failing test to explain it.
//
// This asserts the invariant rather than the absence of a flake, because a
// flake cannot be tested for directly: a regression here would come back as an
// occasional unexplained 40P01 in some unrelated test, exactly as before.
func TestIntegrationTestDatabaseHasItsPolicyJobsUnscheduled(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const policies = `proc_name IN ('policy_retention', 'policy_refresh_continuous_aggregate')`

	var registered int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.jobs WHERE `+policies).Scan(&registered); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if registered == 0 {
		t.Fatal("no policy jobs registered; the migration no longer creates them, " +
			"or they were deleted rather than unscheduled")
	}

	var stillScheduled int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.jobs
		  WHERE `+policies+` AND scheduled`).Scan(&stillScheduled); err != nil {
		t.Fatalf("count scheduled jobs: %v", err)
	}
	if stillScheduled != 0 {
		t.Errorf("%d of %d policy jobs are still scheduled; the background scheduler can "+
			"deadlock against a test's own statements", stillScheduled, registered)
	}
}
