package store

import (
	"context"
	"strings"
	"testing"
)

// A failure to unschedule the policy jobs must surface, not be swallowed.
//
// Swallowing it would put the suite back where it started: the jobs would keep
// running, and the only symptom would be an occasional unexplained 40P01 in
// some unrelated test -- which is precisely the failure mode this mechanism
// exists to remove. Better for the test that asked for a database to fail
// immediately and say why.
func TestIntegrationUnschedulePolicyJobsReportsAFailure(t *testing.T) {
	ctx := context.Background()

	// Given a store whose pool is closed, so every statement fails.
	s := OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s.pool.Close()

	// When the jobs are unscheduled.
	err := s.unschedulePolicyJobs(ctx)

	// Then the caller is told, with the reason attached.
	if err == nil {
		t.Fatal("err = nil against a closed pool, want a failure -- a silent one leaves the " +
			"scheduler running and the deadlock comes back as an unexplained flake")
	}
	if !strings.Contains(err.Error(), "unschedule policy jobs") {
		t.Errorf("err = %v, want it to name what failed", err)
	}
}
