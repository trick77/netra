// Package read answers the hub's read API: the host inventory, the dimension
// listings behind each host, the discrete-event log, and the metric series
// themselves.
//
// It is a sibling of package admin rather than part of it. admin WRITES the
// inventory -- creating hosts, minting tokens, editing sites -- and every one
// of its operations is an operator action. Everything here is a projection of
// what the agents reported, and the two have no shared state beyond the pool.
//
// The logic lives here rather than in the HTTP handlers for the reason
// admin's does: it can be tested against a real database without an HTTP
// server, and the handlers stay thin enough to be obviously correct.
package read

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a request names a host that does not exist, so
// a handler can answer 404 rather than an empty 200. An empty body and a
// missing host are different facts: a host with no containers legitimately
// returns [], and conflating the two would make "is this host registered?"
// unanswerable through the API.
var ErrNotFound = errors.New("not found")

// ErrInvalid is returned for a request the caller can fix -- an unknown
// family, a from after a to -- so a handler can answer 400 rather than 500.
// Its message reaches the client, so it must name what is wrong and never
// quote SQL.
var ErrInvalid = errors.New("invalid")

// Service answers read queries against the hub database.
type Service struct {
	pool *pgxpool.Pool

	// columns caches the value columns of each metric table and continuous
	// aggregate, discovered from information_schema on first use.
	//
	// Discovered rather than declared: host_samples_5m alone has 66 columns
	// and the three host tiers have 190 between them. Transcribing those into
	// Go would be a second copy of the schema that drifts the first time
	// someone adds a column to 0001_init.sql -- and drifts SILENTLY, because
	// a missing column reads as a metric that was never collected rather than
	// as an error. Reading them from the database cannot drift.
	mu      sync.Mutex
	columns map[string][]string
}

// NewService builds a Service over the given pool.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, columns: map[string][]string{}}
}

// hostExists reports whether a host row exists, so every per-host endpoint can
// tell "no rows" from "no host".
func (s *Service) hostExists(ctx context.Context, hostID int32) error {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM hosts WHERE id = $1)`, hostID).Scan(&exists); err != nil {
		return fmt.Errorf("check host: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}
