package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A replica that cannot get the migration lock must FAIL, loudly and by
// itself, rather than block forever.
//
// pg_advisory_lock blocks indefinitely and the context Migrate runs under
// carries no deadline of its own — cmd/netra passes the bare
// signal.NotifyContext. Without the bounded wait, a hub whose migration hangs
// rather than crashes (Postgres releases session locks on backend death, but
// not on a wedged backend) leaves every other replica stopped with no log line
// and no health signal. That is a worse failure than the duplicate-key crash
// the lock was added to prevent, because nothing reports it.
//
// The lock is held here by a SEPARATE connection, exactly as a second replica
// would hold it.
func TestIntegrationMigrateTimesOutWaitingForTheLock(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)

	holder, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire holder connection: %v", err)
	}
	defer holder.Release()

	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockID); err != nil {
		t.Fatalf("holder could not take the migration lock: %v", err)
	}
	defer func() {
		_, _ = holder.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockID)
	}()

	// Short enough to keep the suite fast; the production value is five
	// minutes and waiting it out would prove the same thing.
	saved := migrateLockTimeout
	migrateLockTimeout = 500 * time.Millisecond
	t.Cleanup(func() { migrateLockTimeout = saved })

	start := time.Now()
	err = s.Migrate(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Migrate succeeded while another session held the lock; it must wait, then give up")
	}
	// The message is the entire point of the branch: it has to tell an
	// operator where to look.
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error does not say it timed out: %v", err)
	}
	if !strings.Contains(err.Error(), "pg_locks") {
		t.Errorf("error does not name pg_locks, so it does not say where to look: %v", err)
	}
	// It must actually WAIT rather than fail instantly — a replica that gives
	// up immediately would turn every concurrent start into a crash.
	if elapsed < 400*time.Millisecond {
		t.Errorf("gave up after %s, want a wait of about %s", elapsed, migrateLockTimeout)
	}
	// And it must not block anywhere near the production timeout.
	if elapsed > 30*time.Second {
		t.Errorf("blocked for %s; the bounded wait did not apply", elapsed)
	}
}

// The mirror case: once the holder releases, a waiting replica proceeds
// normally. Without this, the test above would still pass if the lock were
// never actually acquired by Migrate at all.
func TestIntegrationMigrateProceedsOnceTheLockIsFree(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)

	holder, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire holder connection: %v", err)
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockID); err != nil {
		t.Fatalf("holder could not take the migration lock: %v", err)
	}

	saved := migrateLockTimeout
	migrateLockTimeout = 30 * time.Second
	t.Cleanup(func() { migrateLockTimeout = saved })

	released := make(chan struct{})
	go func() {
		time.Sleep(250 * time.Millisecond)
		_, _ = holder.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockID)
		holder.Release()
		close(released)
	}()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate after the lock was released: %v", err)
	}
	<-released

	// And the lock is not left held afterwards, or the next replica would wait
	// out the full timeout for nothing. Asked by TAKING it rather than by
	// reading pg_locks: the bigint form of pg_advisory_lock splits its key
	// across classid/objid, so a catalog query is both fiddlier and less
	// direct than simply proving the lock is available.
	probe, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire probe connection: %v", err)
	}
	defer probe.Release()

	var got bool
	if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, migrateLockID).Scan(&got); err != nil {
		t.Fatalf("probing the migration lock: %v", err)
	}
	if !got {
		t.Error("Migrate returned with the advisory lock still held")
	} else {
		_, _ = probe.Exec(ctx, `SELECT pg_advisory_unlock($1)`, migrateLockID)
	}
}

func TestSplitStatements(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "two plain statements",
			body: "CREATE TABLE a (id INT);\nCREATE TABLE b (id INT);",
			want: []string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"},
		},
		{
			name: "semicolon inside single-quoted literal does not split",
			body: "INSERT INTO a (name) VALUES ('foo;bar');\nCREATE TABLE b (id INT);",
			want: []string{"INSERT INTO a (name) VALUES ('foo;bar')", "CREATE TABLE b (id INT)"},
		},
		{
			name: "escaped quote inside literal does not confuse the parser",
			body: "INSERT INTO a (name) VALUES ('it''s; here');\nCREATE TABLE b (id INT);",
			want: []string{"INSERT INTO a (name) VALUES ('it''s; here')", "CREATE TABLE b (id INT)"},
		},
		{
			name: "semicolon inside line comment does not split",
			body: "CREATE TABLE a (id INT); -- comment; with semicolon\nCREATE TABLE b (id INT);",
			want: []string{"CREATE TABLE a (id INT)", "-- comment; with semicolon\nCREATE TABLE b (id INT)"},
		},
		{
			name: "semicolon inside block comment does not split",
			body: "CREATE TABLE a (id INT); /* comment; with semicolon */\nCREATE TABLE b (id INT);",
			want: []string{"CREATE TABLE a (id INT)", "/* comment; with semicolon */\nCREATE TABLE b (id INT)"},
		},
		{
			name: "semicolon inside dollar-quoted block does not split",
			body: "CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; END; $$ LANGUAGE plpgsql;\nCREATE TABLE b (id INT);",
			want: []string{
				"CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; END; $$ LANGUAGE plpgsql",
				"CREATE TABLE b (id INT)",
			},
		},
		{
			name: "semicolon inside tagged dollar-quoted block does not split",
			body: "CREATE FUNCTION f() RETURNS void AS $tag$ BEGIN PERFORM 1; END; $tag$ LANGUAGE plpgsql;\nCREATE TABLE b (id INT);",
			want: []string{
				"CREATE FUNCTION f() RETURNS void AS $tag$ BEGIN PERFORM 1; END; $tag$ LANGUAGE plpgsql",
				"CREATE TABLE b (id INT)",
			},
		},
		{
			name: "trailing semicolon produces no empty statement",
			body: "CREATE TABLE a (id INT);\n",
			want: []string{"CREATE TABLE a (id INT)"},
		},
		{
			name: "trailing comment produces no empty statement",
			body: "CREATE TABLE a (id INT);\n-- trailing comment, nothing after it\n",
			want: []string{"CREATE TABLE a (id INT)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitStatements(tt.body)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("splitStatements(%q) = %#v, want %#v", tt.body, got, tt.want)
			}
		})
	}
}

// TestIntegrationApplyMigrationNoTransactionMultiStatement proves the actual
// defect this file fixes: a no-transaction migration with more than one
// statement must apply each statement in its own implicit transaction, not
// as a single multi-statement Exec (which Postgres wraps in one implicit
// transaction block regardless).
//
// CREATE INDEX CONCURRENTLY is a faithful stand-in for Task 5's continuous
// aggregates: Postgres rejects it outright inside any transaction block
// ("CREATE INDEX CONCURRENTLY cannot run inside a transaction block"), so
// this test fails loudly without the fix instead of silently doing the
// wrong thing.
func TestIntegrationApplyMigrationNoTransactionMultiStatement(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (setup): %v", err)
	}

	body := noTxMarker + `
CREATE TABLE concurrent_index_target (id INTEGER PRIMARY KEY, name TEXT);
CREATE INDEX CONCURRENTLY idx_concurrent_index_target_name ON concurrent_index_target (name);
`

	if err := s.applyMigration(ctx, "9999_no_tx_multi_statement.sql", body); err != nil {
		t.Fatalf("applyMigration: %v", err)
	}

	var tableCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = 'concurrent_index_target'`).Scan(&tableCount); err != nil {
		t.Fatalf("query table: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("concurrent_index_target table count = %d, want 1", tableCount)
	}

	var indexCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes
		 WHERE schemaname = 'public' AND indexname = 'idx_concurrent_index_target_name'`).Scan(&indexCount); err != nil {
		t.Fatalf("query index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("idx_concurrent_index_target_name index count = %d, want 1", indexCount)
	}
}

// A no-transaction migration has no rollback, so applyMigration can leave the
// schema half-built with the migration unrecorded — its own comment says such
// a migration "is retried". Retrying only works if every statement in the file
// is individually re-runnable, and nothing else enforces that.
//
// The worst case is reproduced directly: apply the no-transaction file fully,
// then forget it in schema_migrations, which is exactly the state an
// interrupted run leaves behind (partial application is strictly easier to
// recover from). Migrate must then re-run the file from the top and succeed.
// Without IF NOT EXISTS on every statement this fails on the first one with
// 42P07, and because the hub migrates on every start it would refuse to boot
// from then on.
//
// 0003, not 0001, and the difference is the whole shape of this invariant.
// A migration re-runs against the schema its PREDECESSORS left, never against
// the final one: its row is missing only because it did not finish, and
// nothing after it can have run. Replaying 0001 over the finished schema is
// therefore not a state the hub can reach -- and since 0010 dropped the sites
// tables and hosts.site_id, it is not even coherent: 0001 would fail on its
// index over a dropped column, and its CREATE TABLE IF NOT EXISTS would
// resurrect the two tables 0010 removed. Every migration is re-runnable where
// it actually runs; none is re-runnable over a schema from its own future,
// and no file can be once anything is ever dropped.
func TestIntegrationMigrateRerunsUnrecordedNoTransactionMigration(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (setup): %v", err)
	}

	if _, err := s.pool.Exec(ctx,
		`DELETE FROM schema_migrations WHERE name = '0003_host_proto_samples.sql'`); err != nil {
		t.Fatalf("forget 0003_host_proto_samples.sql in schema_migrations: %v", err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("re-running an unrecorded no-transaction migration must succeed, got: %v", err)
	}

	// And it must still be recorded afterwards, so a third start is a no-op.
	var recorded bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = '0003_host_proto_samples.sql')`,
	).Scan(&recorded); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if !recorded {
		t.Fatal("0001_init.sql was not recorded after the re-run")
	}
}

// applyMigration's transactional branch (no noTxMarker) is the default path
// for every future migration and the only place a failed migration is rolled
// back. The embedded migrations directory currently holds a single
// no-transaction file, so nothing exercises that branch end to end unless a
// test drives applyMigration directly with an arbitrary body — which it is
// unexported but callable for, from this in-package test file.
//
// The happy path proves a transactional migration applies and is recorded.
func TestIntegrationApplyMigrationTransactionalCommits(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (setup): %v", err)
	}

	body := `CREATE TABLE tx_commit_target (id INTEGER PRIMARY KEY, name TEXT);`

	if err := s.applyMigration(ctx, "9999_tx_commit.sql", body); err != nil {
		t.Fatalf("applyMigration: %v", err)
	}

	var tableCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = 'tx_commit_target'`).Scan(&tableCount); err != nil {
		t.Fatalf("query table: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("tx_commit_target table count = %d, want 1", tableCount)
	}

	var recorded bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = '9999_tx_commit.sql')`,
	).Scan(&recorded); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if !recorded {
		t.Fatal("9999_tx_commit.sql was not recorded after a successful transactional apply")
	}
}

// The rollback path proves the reason the transactional branch exists at
// all: a transactional migration is applied as one multi-statement Exec
// inside a transaction, so a failure on a later statement must undo an
// earlier one's effects, not just abort short. Nothing before this test
// asserted that a failed transactional migration leaves no trace.
func TestIntegrationApplyMigrationTransactionalRollsBackOnFailure(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (setup): %v", err)
	}

	body := `
CREATE TABLE tx_rollback_target (id INTEGER PRIMARY KEY, name TEXT);
CREATE TABLE tx_rollback_target (id INTEGER PRIMARY KEY, name TEXT);
`

	err := s.applyMigration(ctx, "9999_tx_rollback.sql", body)
	if err == nil {
		t.Fatal("applyMigration: want error from the second, duplicate CREATE TABLE, got nil")
	}

	var tableCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = 'tx_rollback_target'`).Scan(&tableCount); err != nil {
		t.Fatalf("query table: %v", err)
	}
	if tableCount != 0 {
		t.Fatalf("tx_rollback_target table count = %d, want 0: the first statement's table "+
			"survived a failure later in the same transaction", tableCount)
	}

	var recorded bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = '9999_tx_rollback.sql')`,
	).Scan(&recorded); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if recorded {
		t.Fatal("9999_tx_rollback.sql was recorded despite the migration failing")
	}
}
