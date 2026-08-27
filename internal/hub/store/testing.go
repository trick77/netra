package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// deadlockSQLState is Postgres's SQLSTATE for "deadlock detected".
const deadlockSQLState = "40P01"

// lockNotAvailableSQLState is Postgres's SQLSTATE for a statement that hit
// lock_timeout waiting for a lock, rather than being killed by deadlock
// detection. Both are the same underlying problem — something else is
// holding a lock we need — so both are retried the same way.
const lockNotAvailableSQLState = "55P03"

// internalErrorSQLState is Postgres's catch-all XX000, which is what
// TimescaleDB raises when one of its own catalog scans loses a race with a
// background worker. It is far too broad to retry on its own -- see
// isSchedulerRaceError, which pairs it with the message.
const internalErrorSQLState = "XX000"

// OpenTest gives the test its own database, cloned from a template that was
// migrated once per run, and drops it again on cleanup.
//
// Integration tests run against real TimescaleDB rather than a mock: the
// continuous aggregates and their start_offset behaviour are the risky part
// of this schema and a fake would verify nothing about them. That is not the
// slow part, though -- re-running the DDL is. This schema builds 11
// hypertables, 18 continuous aggregates and 50 policy jobs, which costs
// roughly half a second every time, and it used to be paid once per test:
// each OpenTest dropped the public schema and each test's Migrate rebuilt it
// from nothing.
//
// `CREATE DATABASE ... TEMPLATE` copies files instead of executing DDL and
// costs ~15ms, so the migration is paid once per run rather than 70+ times.
// The database handed back is already migrated, which every caller's
// subsequent Migrate() sees in schema_migrations and skips -- so callers need
// no change, and the ones that test Migrate itself still work because Migrate
// takes its advisory lock before it looks at what has been applied.
//
// A database per test also means tests stop sharing one, which was its own
// running cost: a red run in this package usually meant another checkout was
// pointed at the same BACKEND_TEST_DSN, not a fault in the branch under test.
func OpenTest(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set; skipping integration test")
	}

	s, err := openTestClone(t, dsn)
	if err == nil {
		return s
	}
	if isInsufficientPrivilege(err) {
		// Deliberately NOT a fallback to the old shared-database path. That
		// path needs `timescaledb.restoring`, which is superuser-only, so a
		// role that cannot CREATE DATABASE cannot run these tests either way
		// -- a fallback here would only turn one clear error into a second,
		// more confusing one further down. Say what is actually required.
		t.Fatalf("the BACKEND_TEST_DSN role cannot create databases, which these "+
			"tests need in order to clone a migrated template per test. It also "+
			"needs to install the timescaledb extension, so in practice it has "+
			"to be a superuser -- the role compose.yaml and CI both use: %v", err)
	}
	t.Fatalf("%v", err)
	return nil
}

// openTestClone builds the template if needed and clones it for this test.
func openTestClone(t *testing.T, dsn string) (*Store, error) {
	t.Helper()
	ctx := context.Background()

	template, err := ensureTemplateDB(ctx, dsn)
	if err != nil {
		return nil, err
	}

	clone := fmt.Sprintf("netra_t_%d_%s_%d", os.Getpid(), cloneRun, cloneSeq.Add(1))
	if err := adminExec(ctx, dsn, fmt.Sprintf(
		`CREATE DATABASE %s TEMPLATE %s`,
		quoteIdentifier(clone), quoteIdentifier(template))); err != nil {
		return nil, fmt.Errorf("clone template database: %w", err)
	}

	// Registered BEFORE the pool below so it runs AFTER it -- t.Cleanup is
	// LIFO, and DROP DATABASE fails while a connection is open. WITH (FORCE)
	// covers the rest: TimescaleDB attaches a background worker to every
	// database it lives in, on its own schedule, so "no connections" is never
	// something this test can guarantee by closing its own.
	t.Cleanup(func() {
		if err := adminExec(context.Background(), dsn, fmt.Sprintf(
			`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdentifier(clone))); err != nil {
			// Not a test failure -- the test already passed or failed on its
			// own terms and nothing it asserted depends on the drop. But a
			// drop that fails silently leaks a database nothing ever sweeps,
			// so name the one that was left behind.
			t.Logf("dropping the test database %s failed, so it is leaked: %v", clone, err)
		}
	})

	cloneDSN, err := dsnForDatabase(dsn, clone)
	if err != nil {
		return nil, err
	}
	s, err := Open(ctx, cloneDSN)
	if err != nil {
		return nil, fmt.Errorf("open cloned test database: %w", err)
	}

	// Still set, still for the same reason: the clone inherits the template's
	// already-unscheduled policy jobs, but a caller's Migrate that applies a
	// migration the template predates would register live ones. Cheap when
	// there is nothing to do, and a test that forgets does not fail -- it
	// flakes, later, somewhere else. See unschedulePolicyJobs.
	s.unscheduleJobs = true

	t.Cleanup(s.Close)
	return s, nil
}

// insufficientPrivilegeSQLState is Postgres's SQLSTATE for a refused
// permission, which is what a role without CREATEDB gets from CREATE
// DATABASE.
const insufficientPrivilegeSQLState = "42501"

// isInsufficientPrivilege reports whether err is Postgres refusing on
// permissions, as opposed to anything else that can go wrong here.
func isInsufficientPrivilege(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == insufficientPrivilegeSQLState
}

// OpenTestSibling opens a SECOND pool on the same database as s, closed on
// cleanup.
//
// Tests that need one are testing what happens when the store's own pool is
// gone -- ingest failing while the request still authenticates -- so they
// cannot share s.pool. Re-reading BACKEND_TEST_DSN used to do the job, back
// when every test shared that one database. It silently stopped being the
// same database when OpenTest began handing out clones: the second pool
// connected to an unmigrated database, authentication failed on a missing
// `tokens` table, and the test saw a 500 where it wanted the 503 its own
// simulated storage failure should have produced.
func OpenTestSibling(t *testing.T, s *Store) *Store {
	t.Helper()

	sibling, err := Open(context.Background(), s.dsn)
	if err != nil {
		t.Fatalf("open sibling pool on the test database: %v", err)
	}
	t.Cleanup(sibling.Close)
	return sibling
}

// cloneSeq makes clone names unique within a process; the pid makes them
// unique across the several test binaries a `go test ./...` runs at once.
var cloneSeq atomic.Int64

// cloneRun separates this process's clones from any an EARLIER run left
// behind. The pid does not: a run killed before its cleanups ran (^C, a
// package timeout) leaks its netra_t_ databases, Linux reuses pids, and the
// next process handed that pid would fail its first CREATE DATABASE with a
// bare 42P04 that names nothing explaining it.
var cloneRun = func() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("read random bytes for the test database name: %v", err))
	}
	return hex.EncodeToString(b)
}()

// templateCache remembers a template this process has already confirmed, so
// only the FIRST OpenTest pays for the check.
//
// Without it every OpenTest opens a connection to `postgres` and takes the
// build lock just to read one row of pg_database -- 70+ times a run and,
// more to the point, an exclusive lock every test in every binary would queue
// behind the moment `-p 1` is dropped.
//
// Successes only. A failure has to stay a failure for the next caller rather
// than be replayed from a cache, and this harness's own tests hand
// ensureTemplateDB deliberately unusable DSNs in the same process that later
// runs real ones.
var templateCache sync.Map // dsn string -> template database name

// templateBuildLockID is a fixed advisory-lock key, taken on the `postgres`
// database while the template is checked for and built. Every test binary in
// a run is a separate process with no shared setup hook, so without it they
// race to create the same database and all but one fail.
const templateBuildLockID = 0x6e657472616d6967

// ensureTemplateDB returns the name of a migrated template database, building
// it if this is the first caller to need it.
//
// The name carries a hash of the migration files, so editing the schema
// yields a different template rather than silently reusing a stale one. That
// matters more than it looks: Migrate matches by filename with no checksum,
// so a template built from an older 0001 would be skipped by the caller's
// Migrate and the tests would quietly run against the wrong schema.
//
// Old templates are left behind rather than swept. They are small, and a
// sweep would drop the template another checkout's run is cloning from right
// now.
func ensureTemplateDB(ctx context.Context, dsn string) (string, error) {
	if cached, ok := templateCache.Load(dsn); ok {
		return cached.(string), nil
	}

	name := templateDBName()

	admin, err := adminConnect(ctx, dsn)
	if err != nil {
		return "", err
	}
	defer admin.Close(ctx)

	if _, err := admin.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(templateBuildLockID)); err != nil {
		return "", fmt.Errorf("take template build lock: %w", err)
	}
	defer func() {
		_, _ = admin.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(templateBuildLockID))
	}()

	var exists bool
	if err := admin.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return "", fmt.Errorf("look for template database: %w", err)
	}
	if exists {
		templateCache.Store(dsn, name)
		return name, nil
	}

	// Built under a scratch name and renamed at the end, so a build that dies
	// half way leaves no database under the real name for the next run to
	// mistake for a finished template.
	building := name + "_building"

	// Everything before the migration, then everything after it, as two lists
	// of (what, statement) rather than eight near-identical error wrappings.
	// The ORDER inside each list is the load-bearing part, so it is what the
	// code shows.
	prepare := []struct{ what, sql string }{
		{"clear partial template", fmt.Sprintf(
			`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdentifier(building))},
		{"create template database", fmt.Sprintf(
			`CREATE DATABASE %s`, quoteIdentifier(building))},
	}
	// Barring connections BEFORE evicting them, not after: TimescaleDB's
	// scheduler reconnects on its own, and CREATE DATABASE ... TEMPLATE fails
	// outright while any session is attached to the source. Reversing these
	// two leaves a window the worker reliably wins. The rename is last, so the
	// template only appears under its real name once it is complete.
	publish := []struct{ what, sql string }{
		{"bar connections to template", fmt.Sprintf(
			`ALTER DATABASE %s ALLOW_CONNECTIONS false`, quoteIdentifier(building))},
		{"evict template connections", fmt.Sprintf(
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = %s`,
			quoteLiteral(building))},
		{"publish template database", fmt.Sprintf(
			`ALTER DATABASE %s RENAME TO %s`,
			quoteIdentifier(building), quoteIdentifier(name))},
	}

	for _, step := range prepare {
		if _, err := admin.Exec(ctx, step.sql); err != nil {
			return "", fmt.Errorf("%s: %w", step.what, err)
		}
	}

	if err := migrateTemplate(ctx, dsn, building); err != nil {
		return "", err
	}

	for _, step := range publish {
		if _, err := admin.Exec(ctx, step.sql); err != nil {
			return "", fmt.Errorf("%s: %w", step.what, err)
		}
	}
	templateCache.Store(dsn, name)
	return name, nil
}

// quoteLiteral single-quotes a string for a statement that cannot take a bind
// parameter. Only used for database names this file generated itself, but
// quoted properly regardless -- an escaping helper that is right only for its
// current caller is the kind that breaks when the next one arrives.
func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

// migrateTemplate runs the migrations once, into the freshly created template.
func migrateTemplate(ctx context.Context, dsn, database string) error {
	tmplDSN, err := dsnForDatabase(dsn, database)
	if err != nil {
		return err
	}
	s, err := Open(ctx, tmplDSN)
	if err != nil {
		return fmt.Errorf("open template database: %w", err)
	}
	defer s.Close()

	// The whole point of the flag, and the one place it now does real work:
	// the clones inherit whatever scheduling state this leaves behind, so the
	// policy jobs are unscheduled once here instead of once per test.
	s.unscheduleJobs = true
	if err := s.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate template database: %w", err)
	}
	return nil
}

// templateDBName is netra_tmpl_ followed by a hash of every migration file's
// name and contents, so a schema edit produces a different template.
func templateDBName() string {
	h := sha256.New()
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		// Cannot happen: the directory is embedded at compile time.
		panic(fmt.Sprintf("read embedded migrations: %v", err))
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		body, err := migrationFS.ReadFile("migrations/" + n)
		if err != nil {
			panic(fmt.Sprintf("read embedded migration %s: %v", n, err))
		}
		h.Write([]byte(n))
		h.Write(body)
	}
	return "netra_tmpl_" + hex.EncodeToString(h.Sum(nil))[:12]
}

// adminConnect opens a single connection to the `postgres` database, which is
// where CREATE/DROP DATABASE have to be issued from -- neither can run while
// connected to the database it names.
func adminConnect(ctx context.Context, dsn string) (*pgx.Conn, error) {
	adminDSN, err := dsnForDatabase(dsn, "postgres")
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return nil, fmt.Errorf("connect to the postgres database: %w", err)
	}
	return conn, nil
}

// adminExec runs one statement on the `postgres` database and closes the
// connection again.
func adminExec(ctx context.Context, dsn, sql string) error {
	conn, err := adminConnect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("%s: %w", sql, err)
	}
	return nil
}

// dsnForDatabase rewrites the database name in a URL-style DSN.
//
// BACKEND_TEST_DSN is a URL everywhere it is set (compose, CI, the README), and
// a keyword/value DSN would fail here rather than silently connect to the
// wrong database.
func dsnForDatabase(dsn, database string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse BACKEND_TEST_DSN: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("BACKEND_TEST_DSN is not a postgres:// URL: %q", u.Scheme)
	}
	u.Path = "/" + database
	return u.String(), nil
}

// quoteIdentifier double-quotes a Postgres identifier, doubling any
// embedded double quotes, so it is safe to interpolate into DDL that has no
// parameter placeholder for identifiers (ALTER DATABASE names its target
// positionally, not as a bind parameter).
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
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
// The template is built with the workers already stopped, and a clone inherits
// that, but the protection ends the moment a caller's Migrate applies a
// migration the template predates and creates fresh jobs. This closes the
// remaining window: the test body itself.
//
// netra_prune_discrete_events is in the list for the same reason: it DELETEs
// from events, systemd_unit_events and package_events, which a test that
// deletes a host is simultaneously cascading through. It is netra's own job
// rather than one of Timescale's, but the scheduler treats it identically.
//
// netra_prune_stale_containers is the same shape: it removes container rows,
// which cascade into container_samples, another hypertable a host-delete test
// is walking from the other end.
//
// netra_prune_stale_devices joins it, and is the worse of the two: it DELETEs
// from devices, which cascades into smart_attributes -- a hypertable, so the
// delete reaches its chunks -- while a host-delete test is cascading through
// the same pair from the other end. The loser of that deadlock fails with
// SQLSTATE 40P01 and nothing in the test to explain it.
//
// A job left off this list does not fail anything: no assertion covers it, the
// suite stays green, and the deadlock surfaces later as a flake. Any job a
// migration adds belongs here the same day.
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
// The scheduler must be STOPPED BEFORE the jobs are altered, and the ordering
// is the whole point of the two statements below.
//
// Migrate has just reinstalled the extension and created 48 jobs, and
// TimescaleDB dispatches new ones within seconds. Every job the scheduler
// picks up makes it UPDATE that job's row in
// _timescaledb_internal.bgw_job_stat -- last_start, next_start, total_runs.
// alter_job reads and writes the same row through TimescaleDB's own catalog
// scan rather than an ordinary MVCC read, so a scan landing mid-update sees
// two versions of a row whose primary key guarantees one:
//
//	ERROR: more than one bgw job stat found (SQLSTATE XX000)
//
// and its sibling on the write side, "tuple concurrently updated". Both are
// races against the scheduler, not corruption -- the PK on bgw_job_stat.job_id
// makes genuinely duplicated rows impossible.
//
// stop_background_workers() is the part of timescaledb_pre_restore() that
// actually stops an in-flight worker -- established empirically against the
// retired shared-database reset path, where forcing a job to run while only
// the timescaledb.restoring GUC was set still let it execute, and doing the
// same with the workers stopped left total_runs unchanged for 40s. It is
// called directly here rather than through the wrapper because the wrapper
// also sets timescaledb.restoring, which is session state on a pooled
// connection and would have to be unset on the same connection to avoid
// wedging the next test.
//
// With the workers stopped the jobs cannot run at all, so the alter_job pass
// that follows is really about the `scheduled` FLAG, which
// TestIntegrationTestDatabaseHasItsPolicyJobsUnscheduled asserts on and which
// survives into any session that inspects this database.
const stopWorkers = `SELECT _timescaledb_functions.stop_background_workers()`

// unscheduleAttempts is how many times the alter_job pass runs in TOTAL.
//
// A worker already dispatched when stop_background_workers() lands can still
// be mid-update. Three,
// for the reason DeleteHost uses three: the contending party is gone by the
// time the retry runs.
const unscheduleAttempts = 3

func (s *Store) unschedulePolicyJobs(ctx context.Context) error {
	// Both failure paths carry the same "unschedule policy jobs" prefix: it is
	// one operation from a caller's point of view, and
	// TestIntegrationUnschedulePolicyJobsReportsAFailure asserts on that
	// prefix precisely so a refactor here cannot quietly change what a
	// failing test database says about itself.
	if _, err := s.pool.Exec(ctx, stopWorkers); err != nil {
		return fmt.Errorf("unschedule policy jobs: stop background workers: %w", err)
	}

	var err error
	for attempt := 1; attempt <= unscheduleAttempts; attempt++ {
		// The job ids are not stable across runs, so they are selected rather
		// than hardcoded. alter_job returns a record, hence the SELECT rather
		// than CALL.
		_, err = s.pool.Exec(ctx, `
			SELECT alter_job(job_id, scheduled => false)
			  FROM timescaledb_information.jobs
			 WHERE proc_name IN ('policy_retention', 'policy_refresh_continuous_aggregate',
			                     'netra_prune_discrete_events',
			                     'netra_prune_stale_devices',
			                     'netra_prune_stale_containers')`)
		if err == nil {
			return nil
		}
		// Anything that is not the scheduler race will not change on a retry,
		// and a closed pool must fail now rather than after three backoffs.
		if !isSchedulerRaceError(err) || attempt == unscheduleAttempts {
			break
		}
		time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
	}
	return fmt.Errorf("unschedule policy jobs: %w", err)
}

// isSchedulerRaceError reports whether err is alter_job losing a race with a
// TimescaleDB background worker writing the same bgw_job_stat row.
//
// Retrying alter_job is always safe: setting scheduled => false twice is the
// same as setting it once, and the losing statement changed nothing.
//
// Matched on SQLSTATE and message rather than SQLSTATE alone, because XX000 is
// Postgres's catch-all internal error -- widening this to every XX000 would
// turn a genuine TimescaleDB bug into three silent retries and a delay.
func isSchedulerRaceError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code == deadlockSQLState || pgErr.Code == lockNotAvailableSQLState {
		return true
	}
	return pgErr.Code == internalErrorSQLState &&
		(strings.Contains(pgErr.Message, "more than one bgw job stat") ||
			strings.Contains(pgErr.Message, "tuple concurrently updated"))
}
