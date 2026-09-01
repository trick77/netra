package collector

import (
	"context"
	"errors"
	"testing"
)

// fakeConn stands in for a D-Bus connection. The real ones are concrete types
// from two different libraries with no interface between them, which is why
// callBus is generic over the connection rather than over a seam of its own.
type fakeConn struct {
	id     int
	closed bool
}

// busHarness scripts what each call on a connection returns, and records what
// callBus did with the connections it was given.
type busHarness struct {
	dials    int
	dialErr  error
	closed   []int
	results  []error
	attempts []int // connection id per call, in order
}

func (h *busHarness) dial(context.Context) (*fakeConn, error) {
	if h.dialErr != nil {
		return nil, h.dialErr
	}
	h.dials++
	return &fakeConn{id: h.dials}, nil
}

func (h *busHarness) close(c *fakeConn) {
	c.closed = true
	h.closed = append(h.closed, c.id)
}

func (h *busHarness) call(c *fakeConn) (int, error) {
	h.attempts = append(h.attempts, c.id)
	if len(h.results) == 0 {
		return c.id, nil
	}
	err := h.results[0]
	h.results = h.results[1:]
	if err != nil {
		return 0, err
	}
	return c.id, nil
}

func run(t *testing.T, ctx context.Context, b *heldBus[fakeConn], h *busHarness) (int, error) {
	t.Helper()
	return callBus(ctx, b, h.dial, h.close, h.call)
}

// The point of holding the connection at all: a second scrape must not dial.
func TestCallBusReusesTheHeldConnection(t *testing.T) {
	// Given a bus whose calls all succeed.
	var b heldBus[fakeConn]
	h := &busHarness{}

	// When it is called twice, as two scrapes would.
	if _, err := run(t, t.Context(), &b, h); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := run(t, t.Context(), &b, h); err != nil {
		t.Fatalf("second call: %v", err)
	}

	// Then one connection served both.
	if h.dials != 1 {
		t.Errorf("dials = %d, want 1 connection reused across both calls", h.dials)
	}
	if len(h.closed) != 0 {
		t.Errorf("closed = %v, want the connection still held", h.closed)
	}
}

// dbus or systemd restarting under the agent breaks the held connection. The
// scrape it breaks must still produce a reading -- that is what makes holding
// the connection strictly better than dialling a fresh one every time, rather
// than a trade.
func TestCallBusRedialsOnceWhenTheHeldConnectionWentStale(t *testing.T) {
	// Given a held connection whose next call fails.
	var b heldBus[fakeConn]
	h := &busHarness{}
	if _, err := run(t, t.Context(), &b, h); err != nil {
		t.Fatalf("priming call: %v", err)
	}
	h.results = []error{errors.New("connection closed by peer")}

	// When the collector calls again.
	got, err := run(t, t.Context(), &b, h)

	// Then it redialled and answered within the same call.
	if err != nil {
		t.Fatalf("call after a stale connection: %v, want a redial and a reading", err)
	}
	if got != 2 {
		t.Errorf("answer came from connection %d, want 2 (the replacement)", got)
	}
	if h.dials != 2 {
		t.Errorf("dials = %d, want 2", h.dials)
	}
	if len(h.closed) != 1 || h.closed[0] != 1 {
		t.Errorf("closed = %v, want the stale connection 1 closed exactly once", h.closed)
	}
}

// Two passes, not a retry loop. A bus that is genuinely gone is the caller's
// "unavailable" capability or its utmp fallback, and it must not be reached by
// way of an unbounded redial.
func TestCallBusGivesUpAfterTwoFailuresAndHoldsNothing(t *testing.T) {
	// Given every call failing.
	var b heldBus[fakeConn]
	h := &busHarness{results: []error{errors.New("first"), errors.New("second")}}

	// When called.
	_, err := run(t, t.Context(), &b, h)

	// Then it tried exactly twice and reported the last failure.
	if err == nil || err.Error() != "second" {
		t.Errorf("err = %v, want the second failure", err)
	}
	if len(h.attempts) != 2 {
		t.Errorf("attempts = %v, want exactly 2", h.attempts)
	}
	// And it is holding no dead socket open for the next scrape to find.
	if b.conn != nil {
		t.Error("a connection is still held after both passes failed")
	}
	if len(h.closed) != 2 {
		t.Errorf("closed = %v, want both connections closed", h.closed)
	}
}

// A scrape that ran out of budget says nothing about the connection. Dropping
// it here would make every timed-out scrape cost a redial on the next one, on
// exactly the host already too slow to afford it.
func TestCallBusKeepsTheConnectionWhenTheContextExpired(t *testing.T) {
	// Given a held connection and a cancelled scrape.
	var b heldBus[fakeConn]
	h := &busHarness{}
	if _, err := run(t, t.Context(), &b, h); err != nil {
		t.Fatalf("priming call: %v", err)
	}
	h.results = []error{context.DeadlineExceeded}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// When the call fails under it.
	if _, err := run(t, ctx, &b, h); err == nil {
		t.Fatal("err = nil, want the cancelled call to fail")
	}

	// Then the connection is still there for the next scrape.
	if len(h.closed) != 0 {
		t.Errorf("closed = %v, want the connection kept", h.closed)
	}
	if h.dials != 1 {
		t.Errorf("dials = %d, want no redial after a deadline", h.dials)
	}
}

// A dial that fails has no stale connection to blame, so retrying it would
// only dial the same absent socket twice.
func TestCallBusDoesNotRetryAFailedDial(t *testing.T) {
	// Given a bus socket that is not there.
	var b heldBus[fakeConn]
	h := &busHarness{dialErr: errors.New("no such file or directory")}

	// When called.
	_, err := run(t, t.Context(), &b, h)

	// Then it reports the dial failure without calling anything.
	if err == nil {
		t.Fatal("err = nil, want the dial failure")
	}
	if len(h.attempts) != 0 {
		t.Errorf("attempts = %v, want none: there was no connection to call on", h.attempts)
	}
}
