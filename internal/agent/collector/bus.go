package collector

import (
	"context"
	"fmt"
	"sync"
)

// heldBus is a D-Bus connection kept ACROSS scrapes.
//
// Both bus-backed collectors used to dial a fresh connection every scrape and
// close it on the way out, on the reasoning that the cost was immaterial and
// that a connection never reused could not go stale. The first half did not
// survive measurement: across the fleet `systemd` was 15ms of a ~100ms scrape,
// second only to `containers`, and go-systemd's NewSystemConnectionContext
// dials the bus TWICE -- a call connection and a signal connection, each with
// its own SASL EXTERNAL handshake and Hello -- then adds a match rule and
// starts a dispatch goroutine, all to ask one question netra asks once a
// minute and to receive signals it never subscribes to.
//
// The second half is answered by callBus rather than by paying for it in
// advance. See the comment there.
//
// Package level at both use sites, because UnitLister and SessionLister are
// plain function types and their production implementations have nowhere else
// to keep a connection -- the same place dockerClient lives, for the same
// reason.
type heldBus[C any] struct {
	mu   sync.Mutex
	conn *C
}

// callBus runs fn on the held connection, dialling one if none is held and
// redialling ONCE if fn fails on a connection that was already open.
//
// This is where staleness is handled: dbus or logind or systemd restarting
// under the agent breaks the held connection, the next call on it fails, and
// this drops it and retries on a fresh one WITHIN THE SAME SCRAPE. So a
// restart costs nothing -- which is strictly better than the fresh-connection
// version it replaces, not merely as good as it.
//
// At most two passes. A second failure is the bus being genuinely unreachable,
// which is the caller's "unavailable" capability or its utmp fallback, and not
// something another retry would fix. The connection is left dropped either
// way, so a host whose bus is gone is not holding a dead socket open.
//
// Serialised on the mutex rather than trusted to the scrape loop being single
// threaded. The loop is that today; a package-level connection that silently
// depends on it is a trap for whoever changes that.
func callBus[C, T any](
	ctx context.Context,
	b *heldBus[C],
	dial func(context.Context) (*C, error),
	closeConn func(*C),
	fn func(*C) (T, error),
) (T, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var zero T
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if b.conn == nil {
			conn, err := dial(ctx)
			if err != nil {
				// A dial that fails is reported as itself rather than retried:
				// there is no stale connection to blame, so the second pass
				// would dial the same absent socket again.
				return zero, fmt.Errorf("connect to the system bus: %w", err)
			}
			b.conn = conn
		}

		v, err := fn(b.conn)
		if err == nil {
			return v, nil
		}
		lastErr = err

		// The scrape's own deadline is not evidence against the connection.
		// Dropping it here would make every timed-out scrape cost a redial on
		// the next one, on exactly the host already too slow to afford it.
		if ctx.Err() != nil {
			break
		}

		closeConn(b.conn)
		b.conn = nil
	}
	return zero, lastErr
}
