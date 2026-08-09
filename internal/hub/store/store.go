// Package store owns the Postgres/TimescaleDB connection and schema.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps the connection pool. It is the only place that knows a DSN.
type Store struct {
	pool *pgxpool.Pool

	// unscheduleJobs makes Migrate leave TimescaleDB's policy jobs registered
	// but not RUNNING. It is set only by OpenTest, never by Open, so it cannot
	// reach a hub: a production hub needs its retention and refresh policies to
	// fire, and they are the whole reason those policies exist.
	//
	// See unschedulePolicyJobs for why a test database needs this.
	unscheduleJobs bool
}

// Open connects and verifies the database is reachable.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Pool exposes the pool for queries in sibling packages.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases all connections.
func (s *Store) Close() { s.pool.Close() }
