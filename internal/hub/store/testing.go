package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// deadlockSQLState is Postgres's SQLSTATE for "deadlock detected".
const deadlockSQLState = "40P01"

// lockNotAvailableSQLState is Postgres's SQLSTATE for a statement that hit
// lock_timeout waiting for a lock, rather than being killed by deadlock
// detection. Both are the same underlying problem — something else is
// holding a lock resetSchema needs — so both are retried the same way.
const lockNotAvailableSQLState = "55P03"

// resetSchemaRetries is the number of times resetSchema retries the schema
// reset after a retryable lock error before giving up.
const resetSchemaRetries = 5

// OpenTest connects to the database named by NETRA_TEST_DSN and drops the
// public schema so each test starts from nothing.
//
// Integration tests run against real TimescaleDB rather than a mock: the
// continuous aggregates and their start_offset behaviour are the risky part
// of this schema and a fake would verify nothing about them.
func OpenTest(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("NETRA_TEST_DSN")
	if dsn == "" {
		t.Skip("NETRA_TEST_DSN not set; skipping integration test")
	}

	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}

	// Every test's Migrate leaves TimescaleDB's policy jobs unscheduled, so the
	// background scheduler cannot deadlock against the test's own statements.
	// Set here rather than by each test because a test that forgets does not
	// fail -- it flakes, later, somewhere else. See unschedulePolicyJobs.
	s.unscheduleJobs = true

	if err := resetSchema(ctx, s); err != nil {
		s.Close()
		t.Fatalf("reset schema: %v", err)
	}

	t.Cleanup(s.Close)
	return s
}

// resetSchema drops and recreates the public schema, giving the test a
// database with nothing in it.
//
// Our migrations register TimescaleDB policy jobs (continuous-aggregate
// refresh and retention policies), which TimescaleDB's background scheduler
// runs on its own timetable — newly created jobs are observed to fire within
// seconds, not at their nominal schedule_interval. A job that fires mid-reset
// takes locks on the very hypertables and catalog rows DROP SCHEMA CASCADE is
// removing, deadlocking against it (SQLSTATE 40P01). This is why -p 1 does
// not help: the contention is with Timescale's background workers, not with
// other test binaries.
//
// timescaledb_pre_restore() is TimescaleDB's supported switch for exactly
// this situation (it exists so pg_restore can load a dump without racing the
// scheduler). Verified empirically that it is load-bearing, not just the GUC
// it also sets: forcing a policy job to run immediately (`alter_job(id,
// next_start => now())`) while only the timescaledb.restoring GUC was set
// via a plain ALTER DATABASE still let the job execute — the GUC alone does
// not stop an already-dispatched worker. timescaledb_pre_restore() does stop
// it, because it additionally calls
// _timescaledb_functions.stop_background_workers() directly, which is the
// part that actually matters; the same forced-job-run test with
// timescaledb_pre_restore() left total_runs unchanged for 40s.
//
// timescaledb_pre_restore()/post_restore() live in the timescaledb extension
// itself, and DROP SCHEMA CASCADE always drops the extension along with
// everything else it owns in public — confirmed with \dx and \dn before and
// after the drop, extension and all four of its _timescaledb_* schemas gone.
// So the wrapper functions are only callable when the extension is currently
// installed, which is why pre_restore is skipped on a brand-new database or
// one straight out of a previous reset (no extension yet — Migrate() installs
// it before the next continuous aggregate exists to endanger). Symmetrically,
// there is no matching post_restore() call after the drop, because by then
// the extension the function lives in is already gone. What has to be
// cleaned up instead is the state pre_restore() left directly on this
// session and on this database: SET SESSION timescaledb.restoring and ALTER
// DATABASE ... SET timescaledb.restoring both remain valid statements without
// the extension installed (the GUC class is registered once for the whole
// cluster via shared_preload_libraries), so both are set back to 'off' before
// returning. This matters because Migrate() runs immediately after and needs
// a working scheduler to create the continuous aggregates in the first place
// — TimescaleDB refuses "WITH (timescaledb.continuous)" while a session has
// restoring on. Freshly created jobs pick up the scheduler normally once
// Migrate() reinstalls the extension; nothing needs an explicit restart call
// for jobs that never existed to begin with.
//
// Pausing the scheduler removes the routine cause of the deadlock but not
// every possible one (a job already dispatched before stop_background_workers
// takes effect can still be mid-flight), so the drop itself also gets a
// short lock_timeout and a few retries on 40P01/55P03: both are transient —
// Postgres resolves a deadlock by killing one side, and a lock_timeout just
// means something else briefly held the lock — so the loser should just try
// again.
func resetSchema(ctx context.Context, s *Store) error {
	// ONE connection for the whole reset.
	//
	// timescaledb_pre_restore() sets timescaledb.restoring on the SESSION that
	// runs it, and the matching `SET SESSION ... = 'off'` below only clears it
	// on the session that runs THAT. Both used to go through s.pool, which
	// offers no connection affinity — so the two could land on different
	// pooled connections, leaving the flag stuck on for the first one. The
	// Migrate() that runs immediately afterwards then failed with
	// TimescaleDB's refusal to create a continuous aggregate while restoring:
	// an intermittent, ordering-dependent test failure with no obvious cause.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire reset connection: %w", err)
	}
	defer conn.Release()

	var extensionInstalled bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb')`,
	).Scan(&extensionInstalled); err != nil {
		return fmt.Errorf("check timescaledb extension: %w", err)
	}
	if extensionInstalled {
		if _, err := conn.Exec(ctx, `SELECT timescaledb_pre_restore()`); err != nil {
			return fmt.Errorf("pause timescaledb background jobs: %w", err)
		}
	}

	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= resetSchemaRetries; attempt++ {
		attempts = attempt
		_, err := conn.Exec(ctx,
			`SET lock_timeout = '5s'; DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
		lastErr = err
		if err == nil {
			break
		}
		if !isRetryableLockError(err) {
			break
		}
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
	}

	// Always try to clear the session- and database-level restoring flag,
	// even if the drop itself failed, so a failed reset does not wedge every
	// later test sharing this database or this connection.
	if _, err := conn.Exec(ctx, `SET SESSION timescaledb.restoring = 'off'`); err != nil && lastErr == nil {
		return fmt.Errorf("resume timescaledb background jobs (session): %w", err)
	}

	var dbName string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName); err == nil {
		_, _ = conn.Exec(ctx, fmt.Sprintf(
			`ALTER DATABASE %s SET timescaledb.restoring = 'off'`, quoteIdentifier(dbName)))
	}

	// lock_timeout is a session GUC set above on a POOLED connection, so it
	// outlives this reset and every later query drawn on the same connection
	// inherits it -- a test doing legitimate slow work would fail with 55P03
	// for no reason it could see. Reset for the same reason restoring is.
	//
	// LAST, after the ALTER DATABASE above. That statement takes a lock on
	// pg_database, its error is deliberately discarded, and the 5s bound set
	// at the top of the retry loop is the only thing keeping it from waiting
	// forever when another test binary holds that lock. Resetting the GUC
	// before it runs would hand an ignored statement an unbounded wait.
	if _, err := conn.Exec(ctx, `SET SESSION lock_timeout = DEFAULT`); err != nil && lastErr == nil {
		return fmt.Errorf("reset lock_timeout: %w", err)
	}

	if lastErr == nil {
		return nil
	}
	if isRetryableLockError(lastErr) {
		var pgErr *pgconn.PgError
		errors.As(lastErr, &pgErr)
		return fmt.Errorf("gave up after %d attempts, still hitting %s: %w", attempts, pgErr.Code, lastErr)
	}
	return lastErr
}

// quoteIdentifier double-quotes a Postgres identifier, doubling any
// embedded double quotes, so it is safe to interpolate into DDL that has no
// parameter placeholder for identifiers (ALTER DATABASE names its target
// positionally, not as a bind parameter).
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// isRetryableLockError reports whether err is a Postgres error resetSchema's
// retry loop should retry: a deadlock, or a lock_timeout expiring while
// waiting for a lock that something else (most likely a
// still-in-flight TimescaleDB background job) is holding.
func isRetryableLockError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == deadlockSQLState || pgErr.Code == lockNotAvailableSQLState
}

// unschedulePolicyJobs leaves TimescaleDB's policy jobs registered but stops
// the scheduler from ever running them. Test databases only.
//
// 0001_init.sql registers 49 retention and continuous-aggregate policies, and
// TimescaleDB's background scheduler fires newly created jobs within seconds
// rather than at their nominal schedule_interval. policy_retention drops
// chunks, which takes AccessExclusiveLock on them; a test that deletes a host
// takes RowExclusiveLock across the same hypertables through the ON DELETE
// CASCADE. The two deadlock, Postgres kills one side, and if the loser is the
// test it fails with SQLSTATE 40P01 -- intermittently, and with nothing in the
// test's own code to explain it. TestIntegrationGroup2TablesCascadeOnHostDelete
// and the admin/UI host-delete tests all hit this.
//
// resetSchema already stops the workers for the DROP SCHEMA it performs, but
// that protection ends when Migrate reinstalls the extension and creates these
// jobs. This closes the remaining window: the test body itself.
//
// netra_prune_discrete_events is in the list for the same reason: it DELETEs
// from events, systemd_unit_events and package_events, which a test that
// deletes a host is simultaneously cascading through. It is netra's own job
// rather than one of Timescale's, but the scheduler treats it identically.
//
// UNSCHEDULED, not deleted. Several tests count the policies to prove the
// migration registered them (rollup_test.go, group1rollup_test.go), and
// deleting the jobs would make those tests pass for the wrong reason -- or
// fail. An unscheduled job still appears in timescaledb_information.jobs; it
// simply never runs. Two of those tests already unschedule jobs themselves for
// this exact reason; doing it here means every test gets it rather than each
// one rediscovering the problem.
//
// Nothing is lost: no test depends on a policy running on its own. The ones
// that need aggregate data call refresh_continuous_aggregate directly, which
// is unaffected.
func (s *Store) unschedulePolicyJobs(ctx context.Context) error {
	// The job ids are not stable across runs, so they are selected rather than
	// hardcoded. alter_job returns a record, hence the SELECT rather than CALL.
	if _, err := s.pool.Exec(ctx, `
		SELECT alter_job(job_id, scheduled => false)
		  FROM timescaledb_information.jobs
		 WHERE proc_name IN ('policy_retention', 'policy_refresh_continuous_aggregate',
		                     'netra_prune_discrete_events')`,
	); err != nil {
		return fmt.Errorf("unschedule policy jobs: %w", err)
	}
	return nil
}
