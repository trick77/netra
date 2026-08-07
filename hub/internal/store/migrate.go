package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// noTxMarker opts a migration out of the surrounding transaction.
//
// TimescaleDB refuses to create a continuous aggregate inside a transaction
// block, so those migrations carry this marker on their first line.
const noTxMarker = "-- netra:no-transaction"

// Migrate applies every pending migration in filename order, exactly once.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`,
			name).Scan(&applied); err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
		if applied {
			continue
		}

		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		if err := s.applyMigration(ctx, name, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}

	return nil
}

func (s *Store) applyMigration(ctx context.Context, name, body string) error {
	if strings.HasPrefix(strings.TrimSpace(body), noTxMarker) {
		// Outside a transaction: the statements are individually atomic and a
		// partial failure leaves the migration unrecorded, so it is retried.
		if _, err := s.pool.Exec(ctx, body); err != nil {
			return err
		}
		_, err := s.pool.Exec(ctx,
			`INSERT INTO schema_migrations (name) VALUES ($1)`, name)
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, body); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
