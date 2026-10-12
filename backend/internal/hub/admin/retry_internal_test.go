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
	if calls != deleteAttempts {
		t.Errorf("calls = %d, want %d", calls, deleteAttempts)
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

// A request cancelled after the LAST attempt must still report the deadlock.
//
// The loop used to back off unconditionally, including after the final
// attempt, where there was nothing left to wait for. A request cancelled
// during that pointless pause returned context.Canceled instead of the
// PgError -- so the cause vanished from both the response and the logs, on
// exactly the failure this retry exists to make diagnosable.
func TestRetryOnDeadlockReportsTheDeadlockEvenIfCancelledAfterTheLastAttempt(t *testing.T) {
	// Given a statement that always deadlocks, and a request cancelled as the
	// final attempt fails.
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	fn := func() error {
		calls++
		if calls == deleteAttempts {
			cancel()
		}
		return deadlockErr()
	}

	// When it is run under the retry.
	err := retryOnDeadlock(ctx, fn)

	// Then the deadlock is what surfaces, not the cancellation.
	if !isDeadlock(err) {
		t.Errorf("err = %v, want the deadlock -- the cause must not be masked by a late cancellation", err)
	}
	if calls != deleteAttempts {
		t.Errorf("calls = %d, want %d", calls, deleteAttempts)
	}
}

// The bound must not cost a backoff nobody waits on: after the last attempt
// there is nothing left to retry, so the call returns immediately.
func TestRetryOnDeadlockDoesNotBackOffAfterTheLastAttempt(t *testing.T) {
	// Given a statement that always deadlocks.
	fn := func() error { return deadlockErr() }

	// When it is run under the retry.
	start := time.Now()
	_ = retryOnDeadlock(context.Background(), fn)

	// Then it slept between attempts, but not after the final one.
	elapsed := time.Since(start)
	if max := time.Duration(deleteAttempts-1) * deleteRetryBackoff; elapsed >= max+deleteRetryBackoff {
		t.Errorf("took %v, want under %v -- the last attempt must not be followed by a backoff",
			elapsed, max+deleteRetryBackoff)
	}
}
