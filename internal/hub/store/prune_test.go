package store_test

import (
	"context"
	"testing"

	"github.com/trick77/netra/internal/hub/store"
)

// events, systemd_unit_events and package_events are the three tables in
// 0001_init.sql with no retention policy, because they are plain Postgres
// tables and add_retention_policy only takes hypertables.
//
// They were sized on "a unit changes state a handful of times a month", which
// held until the agent began emitting a failed-unit baseline on restart: the
// baseline is bounded, but a crash-looping agent re-emits it on every restart
// into a table nothing pruned. This is the job that prunes them.
func TestIntegrationDiscreteEventPruneJobIsRegistered(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var jobs int
	var schedule string
	if err := s.Pool().QueryRow(ctx, `
		SELECT count(*), coalesce(max(schedule_interval)::text, '')
		  FROM timescaledb_information.jobs
		 WHERE proc_name = 'netra_prune_discrete_events'`).Scan(&jobs, &schedule); err != nil {
		t.Fatalf("count prune jobs: %v", err)
	}

	// EXACTLY one. add_job has no if_not_exists, and this migration re-runs
	// from the top whenever it failed part-way, so an unguarded call would
	// register a fresh copy every time -- each one deleting the same rows.
	if jobs != 1 {
		t.Fatalf("prune jobs = %d, want exactly 1; the add_job guard is missing or wrong", jobs)
	}
	if schedule != "1 day" {
		t.Errorf("schedule_interval = %q, want %q", schedule, "1 day")
	}
}

// The migration re-runs from the top on an existing schema (see the file
// header), so applying it twice must not leave two prune jobs behind.
func TestIntegrationDiscreteEventPruneJobSurvivesAReRun(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM schema_migrations`); err != nil {
		t.Fatalf("forget the applied migration: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var jobs int
	if err := s.Pool().QueryRow(ctx, `
		SELECT count(*) FROM timescaledb_information.jobs
		 WHERE proc_name = 'netra_prune_discrete_events'`).Scan(&jobs); err != nil {
		t.Fatalf("count prune jobs: %v", err)
	}
	if jobs != 1 {
		t.Errorf("prune jobs after a re-run = %d, want 1", jobs)
	}
}

// What the job actually does: drop what is past the horizon from all three
// tables, and keep everything inside it.
func TestIntegrationDiscreteEventPruneDropsOnlyWhatIsPastTheHorizon(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('prune-me') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	var unitID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO systemd_units (host_id, unit_name) VALUES ($1, 'ssh.service') RETURNING id`,
		hostID).Scan(&unitID); err != nil {
		t.Fatalf("insert unit: %v", err)
	}

	// One row either side of a 90-day horizon in each table. 91 and 89 days
	// rather than 90 exactly, so the test does not turn on whether the
	// comparison is strict.
	seed := []string{
		`INSERT INTO systemd_unit_events (host_id, unit_id, ts, state)
		 VALUES ($1, $2, now() - INTERVAL '91 days', 'failed'),
		        ($1, $2, now() - INTERVAL '89 days', 'active')`,
	}
	if _, err := s.Pool().Exec(ctx, seed[0], hostID, unitID); err != nil {
		t.Fatalf("seed systemd_unit_events: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO package_events (host_id, ts, name, action)
		VALUES ($1, now() - INTERVAL '91 days', 'bash', 'upgrade'),
		       ($1, now() - INTERVAL '89 days', 'bash', 'upgrade')`, hostID); err != nil {
		t.Fatalf("seed package_events: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO events (host_id, ts, type, subject)
		VALUES ($1, now() - INTERVAL '91 days', 'mdraid_degraded', 'md0'),
		       ($1, now() - INTERVAL '89 days', 'mdraid_degraded', 'md0')`, hostID); err != nil {
		t.Fatalf("seed events: %v", err)
	}

	// Called directly rather than waiting for the scheduler: OpenTest
	// unschedules every policy job, including this one, precisely so nothing
	// deletes rows out from under a test.
	if _, err := s.Pool().Exec(ctx,
		`CALL netra_prune_discrete_events(0, '{"retention": "90 days"}'::jsonb)`); err != nil {
		t.Fatalf("call the prune procedure: %v", err)
	}

	for _, tc := range []struct{ table, where string }{
		{"systemd_unit_events", "host_id = $1"},
		{"package_events", "host_id = $1"},
		{"events", "host_id = $1"},
	} {
		var remaining int
		if err := s.Pool().QueryRow(ctx,
			`SELECT count(*) FROM `+tc.table+` WHERE `+tc.where, hostID).Scan(&remaining); err != nil {
			t.Fatalf("count %s: %v", tc.table, err)
		}
		if remaining != 1 {
			t.Errorf("%s has %d rows, want 1 -- the 91-day row should be gone and the 89-day one kept",
				tc.table, remaining)
		}
	}
}

// The horizon comes from the job's config so it can be changed with alter_job
// on a running hub rather than by editing the schema. A config that does not
// carry one must still prune rather than doing nothing silently.
func TestIntegrationDiscreteEventPruneDefaultsItsHorizon(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('prune-default') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO events (host_id, ts, type) VALUES ($1, now() - INTERVAL '200 days', 'agent_upgrade')`,
		hostID); err != nil {
		t.Fatalf("seed events: %v", err)
	}

	if _, err := s.Pool().Exec(ctx, `CALL netra_prune_discrete_events(0, NULL)`); err != nil {
		t.Fatalf("call the prune procedure: %v", err)
	}

	var remaining int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1`, hostID).Scan(&remaining); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if remaining != 0 {
		t.Errorf("events has %d rows, want 0 -- a null config must fall back to the 90-day default", remaining)
	}
}
