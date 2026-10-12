package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/trick77/ship/kit/migrate"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// noTxMarker opts a migration out of the surrounding transaction.
//
// TimescaleDB refuses to create a continuous aggregate inside a transaction
// block, so those migrations carry this marker on their first line. netra's
// own spelling, kept so no applied file is rewritten.
const noTxMarker = "-- netra:no-transaction"

// migrateLockID is the advisory-lock key held for the whole of Migrate.
//
// An arbitrary but FIXED constant: pg_advisory_lock namespaces by value alone,
// so every hub replica must use the same one. Chosen from "netra" so it does
// not collide with an application lock added later.
const migrateLockID int64 = 0x6E65747261 // "netra"

// migrateLockTimeout bounds how long one replica waits for another's migration.
//
// Generous rather than tight: a first deploy applies the whole schema including
// hypertables and continuous aggregates, and timing out mid-way through a
// legitimate slow migration would be its own outage.
//
// A var, not a const, only so the timeout branch is reachable from a test
// without making one wait five minutes for it. Nothing in production writes it.
var migrateLockTimeout = 5 * time.Minute

// Migrate applies every pending migration in filename order, exactly once,
// serialised across replicas by a session advisory lock (kit/migrate).
func (s *Store) Migrate(ctx context.Context) error {
	sub, err := fs.Sub(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	return s.migrateFS(ctx, sub)
}

// migrateFS is Migrate over any set of files, so a test can drive a body of its
// own through the same path.
func (s *Store) migrateFS(ctx context.Context, fsys fs.FS) error {
	// Closing it leaves s.pool open: the sql.DB only borrows its connections.
	db := stdlib.OpenDBFromPool(s.pool)
	defer func() { _ = db.Close() }()

	applied, err := migrate.Up(ctx, db, fsys, migrate.Options{
		Dialect:     migrate.Postgres(migrateLockID),
		NoTxMarker:  noTxMarker,
		LockTimeout: migrateLockTimeout,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			// Where to look, which the generic message cannot know.
			return fmt.Errorf("%w (look for an advisory lock with key %d in pg_locks, joined to "+
				"pg_stat_activity to find the session)", err, migrateLockID)
		}
		return err
	}

	if s.unscheduleJobs && len(applied) > 0 {
		// Test databases only; the flag is unreachable from Open. Done HERE
		// rather than in OpenTest because the jobs do not exist until the
		// migrations that register them have run, and OpenTest returns before
		// its caller migrates.
		//
		// Only when this call actually APPLIED something. Every test calls
		// Migrate, but since OpenTest began handing out clones of an
		// already-migrated template, almost every one of those calls applies
		// nothing -- and the jobs it would be unscheduling were unscheduled
		// once, in the template, and inherited.
		return s.unschedulePolicyJobs(ctx)
	}
	return nil
}
