package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// deadlockErr is what Postgres returns to the transaction it killed to break a
// lock cycle.
func deadlockErr() error {
	return &pgconn.PgError{Code: pgDeadlockDetected, Message: "deadlock detected"}
}

// A deadlocked statement must be retried, because the database has already
// rolled it back and says so.
//
// The real cycle is a cascading DELETE FROM hosts against TimescaleDB's
// retention jobs, which cannot be provoked on demand -- so the retry logic is
// exercised directly rather than through a race the test would have to win.
func TestRetryOnDeadlockRetriesUntilItSucceeds(t *testing.T) {
	// Given a statement that deadlocks once and then succeeds.
	calls := 0
	fn := func() error {
		calls++
		if calls == 1 {
			return deadlockErr()
		}
		return nil
	}

	// When it is run under the retry.
	err := retryOnDeadlock(context.Background(), fn)

	// Then the caller sees the success, not the deadlock.
	if err != nil {
		t.Errorf("err = %v, want nil -- a deadlock is transient and the retry succeeded", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

// Anything that is NOT a deadlock must surface immediately.
//
// Widening the retry would turn real failures into silent delays: a constraint
// violation will fail identically however many times it is tried, and a
// dropped connection is something the caller needs to be told about.
func TestRetryOnDeadlockDoesNotRetryOtherErrors(t *testing.T) {
	// Given a statement failing with a uniqueness violation.
	want := &pgconn.PgError{Code: pgUniqueViolation}
	calls := 0
	fn := func() error {
		calls++
		return want
	}

	// When it is run under the retry.
	err := retryOnDeadlock(context.Background(), fn)

	// Then it ran once and the error came back untouched.
	if !errors.Is(err, error(want)) {
		t.Errorf("err = %v, want the original %v", err, want)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 -- only a deadlock may be retried", calls)
	}
}

// A statement that deadlocks every time must give up rather than retry
// forever, and must report the deadlock rather than swallow it.
func TestRetryOnDeadlockGivesUpAndReportsTheDeadlock(t *testing.T) {
	// Given a statement that always deadlocks.
	calls := 0
	fn := func() error {
		calls++
		return deadlockErr()
	}

	// When it is run under the retry.
	err := retryOnDeadlock(context.Background(), fn)

	// Then it stopped at the bound and the caller learns why it failed.
	if !isDeadlock(err) {
		t.Errorf("err = %v, want the deadlock -- a persistent failure must not be reported as success", err)
	}
	if calls != deleteRetries {
		t.Errorf("calls = %d, want %d", calls, deleteRetries)
	}
}

// A cancelled request must abandon the retry rather than sleep out its budget.
// An operator who gave up waiting is not served by the hub retrying anyway.
func TestRetryOnDeadlockStopsWhenTheContextIsCancelled(t *testing.T) {
	// Given a request that is cancelled while the statement is deadlocking.
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	fn := func() error {
		calls++
		cancel()
		return deadlockErr()
	}

	// When it is run under the retry.
	start := time.Now()
	err := retryOnDeadlock(ctx, fn)

	// Then it returned the cancellation without waiting out the backoff.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if elapsed := time.Since(start); elapsed >= deleteRetryBackoff {
		t.Errorf("waited %v; a cancelled request must not sleep out the backoff", elapsed)
	}
}
