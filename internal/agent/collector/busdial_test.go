package collector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"

	"github.com/trick77/netra/internal/agent/collector"
)

// These drive the two production listers through their dial seams, which is
// the only way to see what a SECOND scrape does. The connection they hold now
// outlives a scrape by design, and the way to get that wrong is silent: godbus
// closes a connection as soon as the context it was handed is done, so a
// connection dialled on the scrape's own context dies with the scrape and the
// redial path quietly pays for a dial, a failed call and a second dial every
// minute -- worse than the fresh-connection version this replaced, while
// looking identical from the outside.

// fakeUnitConn is a systemd bus connection that answers ListUnits from a
// script.
type fakeUnitConn struct {
	units  []dbus.UnitStatus
	errs   []error
	calls  int
	closed int
}

func (c *fakeUnitConn) ListUnitsContext(context.Context) ([]dbus.UnitStatus, error) {
	c.calls++
	if len(c.errs) > 0 {
		err := c.errs[0]
		c.errs = c.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	return c.units, nil
}

func (c *fakeUnitConn) Close() { c.closed++ }

// A second scrape must not dial. This is the entire point of the change, and
// nothing below the dial seam can observe it.
func TestSystemUnitsHoldsItsConnectionAcrossScrapes(t *testing.T) {
	// Given a bus that answers with one service.
	conn := &fakeUnitConn{units: []dbus.UnitStatus{
		{Name: "nginx.service", ActiveState: "active", SubState: "running"},
		{Name: "run-docker.mount", ActiveState: "active", SubState: "mounted"},
	}}
	dials := 0
	restore := collector.SetSystemBusDialForTest(
		func(context.Context) (collector.UnitConnForTest, error) {
			dials++
			return conn, nil
		})
	defer restore()

	// When two scrapes ask for units.
	first, err := collector.SystemUnits(t.Context())
	if err != nil {
		t.Fatalf("first scrape: %v", err)
	}
	if _, err := collector.SystemUnits(t.Context()); err != nil {
		t.Fatalf("second scrape: %v", err)
	}

	// Then one connection served both, and it is still open.
	if dials != 1 {
		t.Errorf("dials = %d, want 1 connection held across both scrapes", dials)
	}
	if conn.closed != 0 {
		t.Errorf("closed = %d, want the connection still held", conn.closed)
	}
	// And the filtering it does is unchanged: services only.
	if len(first) != 1 || first[0].Name != "nginx.service" {
		t.Errorf("units = %v, want only nginx.service", first)
	}
}

// systemd restarting under the agent breaks the held connection. The scrape it
// breaks must still produce a reading.
func TestSystemUnitsRedialsWhenTheHeldConnectionDied(t *testing.T) {
	// Given a held connection that then refuses every call.
	dead := &fakeUnitConn{errs: []error{errors.New("use of closed connection")}}
	live := &fakeUnitConn{units: []dbus.UnitStatus{
		{Name: "sshd.service", ActiveState: "active", SubState: "running"},
	}}
	conns := []*fakeUnitConn{dead, live}
	restore := collector.SetSystemBusDialForTest(
		func(context.Context) (collector.UnitConnForTest, error) {
			c := conns[0]
			conns = conns[1:]
			return c, nil
		})
	defer restore()

	if _, err := collector.SystemUnits(t.Context()); err == nil {
		// The first scrape dials `dead` and its one scripted error is spent
		// there; a fresh connection is not retried, so this scrape fails.
		t.Fatal("first scrape succeeded, want the scripted failure")
	}

	// When the next scrape runs on the connection now held.
	units, err := collector.SystemUnits(t.Context())

	// Then it redialled and answered.
	if err != nil {
		t.Fatalf("scrape after the connection died: %v", err)
	}
	if len(units) != 1 || units[0].Name != "sshd.service" {
		t.Errorf("units = %v, want sshd.service from the replacement", units)
	}
}

// A dial that fails is the bus being absent, which is the collector's
// "unavailable" capability rather than something to retry.
func TestSystemUnitsReportsADialFailure(t *testing.T) {
	restore := collector.SetSystemBusDialForTest(
		func(context.Context) (collector.UnitConnForTest, error) {
			return nil, errors.New("no such file or directory")
		})
	defer restore()

	if _, err := collector.SystemUnits(t.Context()); err == nil {
		t.Fatal("err = nil, want the dial failure")
	}
}

// fakeSessionConn is a system bus connection whose objects answer a canned
// ListSessions reply.
type fakeSessionConn struct {
	body   []any
	closed int
}

func (c *fakeSessionConn) Object(string, godbus.ObjectPath) godbus.BusObject {
	return fakeObject{body: c.body}
}

func (c *fakeSessionConn) Close() error { c.closed++; return nil }

// The same held-connection contract on the logind side.
func TestLogindSessionsHoldsItsConnectionAcrossScrapes(t *testing.T) {
	// Given a bus that reports no sessions -- enough to reach the connection
	// handling, which is what this pins.
	conn := &fakeSessionConn{body: []any{[]struct {
		ID   string
		UID  uint32
		User string
		Seat string
		Path godbus.ObjectPath
	}{}}}
	dials := 0
	restore := collector.SetLogindBusDialForTest(
		func(context.Context) (collector.SessionConnForTest, error) {
			dials++
			return conn, nil
		})
	defer restore()

	// When two scrapes count sessions.
	if _, err := collector.LogindSessions(t.Context()); err != nil {
		t.Fatalf("first scrape: %v", err)
	}
	if _, err := collector.LogindSessions(t.Context()); err != nil {
		t.Fatalf("second scrape: %v", err)
	}

	// Then one connection served both, and it is still open.
	if dials != 1 {
		t.Errorf("dials = %d, want 1 connection held across both scrapes", dials)
	}
	if conn.closed != 0 {
		t.Errorf("closed = %d, want the connection still held", conn.closed)
	}
}

// A host with dbus but no logind must still fall through to utmp, and must not
// pay two dials a minute to find that out.
func TestLogindSessionsReportsAFailureWithoutRetryingAFreshConnection(t *testing.T) {
	conn := &fakeSessionConn{body: nil} // an undecodable reply
	dials := 0
	restore := collector.SetLogindBusDialForTest(
		func(context.Context) (collector.SessionConnForTest, error) {
			dials++
			return conn, nil
		})
	defer restore()

	if _, err := collector.LogindSessions(t.Context()); err == nil {
		t.Fatal("err = nil, want the listing to fail")
	}
	if dials != 1 {
		t.Errorf("dials = %d, want 1: a connection just dialled cannot be stale", dials)
	}
}
