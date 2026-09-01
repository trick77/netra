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
// A `held` flag rather than a nil check, because C is an interface at both use
// sites (see unitConn and sessionConn) and its zero value is only incidentally
// comparable to nil.
type heldBus[C any] struct {
	mu   sync.Mutex
	conn C
	held bool
}

// callBus runs fn on the held connection, dialling one if none is held and
// redialling ONCE if fn fails on a connection that was ALREADY OPEN when the
// call started.
//
// That last qualifier is the whole shape of the retry. A connection this call
// dialled a moment ago cannot have gone stale, so a failure on it is the
// call's own -- dbus present but logind absent, a GetAll denied by policy, the
// exact hosts the utmp fallback exists for -- and retrying would buy a second
// dial and a second failure. Only a connection carried over from an earlier
// scrape is worth suspecting.
//
// This is where staleness is handled, instead of being paid for in advance by
// dialling every scrape: dbus or logind or systemd restarting under the agent
// breaks the held connection, the next call on it fails, and this drops it and
// retries on a fresh one WITHIN THE SAME SCRAPE. So a restart costs nothing --
// which is strictly better than the fresh-connection version it replaces, not
// merely as good as it.
//
// The connection is dropped on any failure, so a host whose bus has gone is
// not holding a dead socket open for the next scrape to find.
//
// Serialised on the mutex rather than trusted to the scrape loop being single
// threaded. The loop is that today; a package-level connection that silently
// depends on it is a trap for whoever changes that.
func callBus[C, T any](
	ctx context.Context,
	b *heldBus[C],
	dial func(context.Context) (C, error),
	closeConn func(C),
	fn func(C) (T, error),
) (T, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var zero T
	var lastErr error
	for {
		// Read BEFORE the dial below can set it: this is "was there a
		// connection when we got here", which is what decides whether a
		// failure is worth retrying.
		carriedOver := b.held

		if !b.held {
			conn, err := dial(ctx)
			if err != nil {
				// Reported as itself rather than retried: there is no stale
				// connection to blame, so a second pass would dial the same
				// absent socket again.
				return zero, fmt.Errorf("connect to the system bus: %w", err)
			}
			b.conn, b.held = conn, true
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
		var zeroC C
		b.conn, b.held = zeroC, false

		if !carriedOver {
			break
		}
	}
	return zero, lastErr
}
