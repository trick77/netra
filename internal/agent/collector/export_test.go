package collector

import (
	"context"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// SetSysClassNetForTest points the per-interface sysfs reads at a fixture
// tree for the duration of one test.
//
// SystemIfaces is the one production path that cannot take its root as a
// parameter -- it is an IfaceLister, and the signature is the injection seam
// for every other test in this package. Without this hook the VRF and alias
// reads would only ever be exercised against whatever the machine running the
// suite happens to have configured, which is not a test.
func SetSysClassNetForTest(t *testing.T, root string) {
	t.Helper()

	prev := sysClassNet
	sysClassNet = root
	t.Cleanup(func() { sysClassNet = prev })
}

// IfaceAliasForTest exposes the alias lookup.
func IfaceAliasForTest(name string) string { return ifaceAlias(name) }

// SetStatfsTimeoutForTest shortens the per-mountpoint statfs deadline, so the
// wedged-mount path can be exercised without spending the production two
// seconds per blocked call.
func (f *Filesystems) SetStatfsTimeoutForTest(d time.Duration) { f.statfsTimeout = d }

// CapContainerRowsForTest and MaxContainerRowsForTest expose the container row
// backstop, so a test can assert the bound without restating the literal --
// which would then agree with a wrong value as readily as a right one.
func CapContainerRowsForTest(rows []*netrav1.ContainerSample) []*netrav1.ContainerSample {
	return capContainerRows(rows)
}

const MaxContainerRowsForTest = maxContainerRows

// SessionForTest is one logind session as this collector reads it: the class
// it belongs to, and whether it is still alive.
type SessionForTest struct {
	Class string
	State string
}

// CountHumanSessionsForTest exposes both judgements LogindSessions makes --
// the class allowlist and the closing-state denylist -- which are the things a
// machine without logind can still check.
func CountHumanSessionsForTest(sessions []SessionForTest) int {
	out := make([]session, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, session{class: s.Class, state: s.State})
	}
	return countHumanSessions(out)
}

// SessionSourceForTest is the bus seam LogindSessions sits on, so a test can
// stand in for logind entirely: which sessions exist, and what each one is.
type SessionSourceForTest interface {
	Paths(ctx context.Context) ([]dbus.ObjectPath, error)
	Info(ctx context.Context, path dbus.ObjectPath) (SessionForTest, error)
}

// CountLogindSessionsForTest runs the real counting path against a fake bus.
func CountLogindSessionsForTest(ctx context.Context, src SessionSourceForTest) (int, error) {
	return countLogindSessions(ctx, testSource{src})
}

type testSource struct{ inner SessionSourceForTest }

func (t testSource) sessionPaths(ctx context.Context) ([]dbus.ObjectPath, error) {
	return t.inner.Paths(ctx)
}

func (t testSource) sessionInfo(ctx context.Context, p dbus.ObjectPath) (session, error) {
	s, err := t.inner.Info(ctx, p)
	if err != nil {
		return session{}, err
	}
	return session{class: s.Class, state: s.State}, nil
}

// SessionPathsOfForTest exposes the ListSessions decoding helper on the bus
// side. The property-map decode is reached through BusSessionsForTest.
func SessionPathsOfForTest(ids []string) []dbus.ObjectPath {
	sessions := make([]logindSession, 0, len(ids))
	for _, id := range ids {
		sessions = append(sessions, logindSession{
			ID:   id,
			User: "someone",
			Seat: "seat0",
			Path: dbus.ObjectPath("/org/freedesktop/login1/session/" + id),
		})
	}
	return sessionPathsOf(sessions)
}

// BusSessionsForTest builds the D-Bus half against a stand-in object, so the
// two decodes it performs -- the ListSessions reply, and the property map --
// are exercised without a bus. objects is called with the object path the
// production code would have asked the connection for.
func BusSessionsForTest(objects func(path dbus.ObjectPath) dbus.BusObject) SessionSourceForTest {
	return busAdapter{busSessions{object: objects}}
}

type busAdapter struct{ inner busSessions }

func (b busAdapter) Paths(ctx context.Context) ([]dbus.ObjectPath, error) {
	return b.inner.sessionPaths(ctx)
}

func (b busAdapter) Info(ctx context.Context, p dbus.ObjectPath) (SessionForTest, error) {
	s, err := b.inner.sessionInfo(ctx, p)
	if err != nil {
		return SessionForTest{}, err
	}
	return SessionForTest{Class: s.class, State: s.state}, nil
}
