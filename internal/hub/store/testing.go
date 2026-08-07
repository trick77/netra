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
	var extensionInstalled bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb')`,
	).Scan(&extensionInstalled); err != nil {
		return fmt.Errorf("check timescaledb extension: %w", err)
	}
	if extensionInstalled {
		if _, err := s.pool.Exec(ctx, `SELECT timescaledb_pre_restore()`); err != nil {
			return fmt.Errorf("pause timescaledb background jobs: %w", err)
		}
	}

	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= resetSchemaRetries; attempt++ {
		attempts = attempt
		_, err := s.pool.Exec(ctx,
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
	if _, err := s.pool.Exec(ctx, `SET SESSION timescaledb.restoring = 'off'`); err != nil && lastErr == nil {
		return fmt.Errorf("resume timescaledb background jobs (session): %w", err)
	}
	var dbName string
	if err := s.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName); err == nil {
		_, _ = s.pool.Exec(ctx, fmt.Sprintf(
			`ALTER DATABASE %s SET timescaledb.restoring = 'off'`, quoteIdentifier(dbName)))
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
