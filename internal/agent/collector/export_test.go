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

// IfacePhysicalForTest exposes the device-symlink check.
func IfacePhysicalForTest(name string) *bool { return ifacePhysical(name) }

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

// UnitConnForTest and SessionConnForTest are the connection seams the two
// bus-backed collectors dial through, so a test can drive SystemUnits and
// LogindSessions end to end -- including the held-connection reuse and the
// redial -- without a system bus.
type (
	UnitConnForTest    = unitConn
	SessionConnForTest = sessionConn
)

// SetSystemBusDialForTest replaces the systemd dial and drops any held
// connection, returning a func that restores both. The drop matters: these
// connections outlive a scrape by design, so one left over from an earlier
// test would be reused by the next and hide the dial entirely.
func SetSystemBusDialForTest(dial func(context.Context) (unitConn, error)) func() {
	prev := dialSystemBus
	dialSystemBus = dial
	systemBus = heldBus[unitConn]{}
	return func() {
		dialSystemBus = prev
		systemBus = heldBus[unitConn]{}
	}
}

// SetLogindBusDialForTest is SetSystemBusDialForTest for logind.
func SetLogindBusDialForTest(dial func(context.Context) (sessionConn, error)) func() {
	prev := dialLogindBus
	dialLogindBus = dial
	logindBus = heldBus[sessionConn]{}
	return func() {
		dialLogindBus = prev
		logindBus = heldBus[sessionConn]{}
	}
}

// ParseKmsgRecordForTest exposes the /dev/kmsg record parser. It is a pure
// function over bytes, which is why the wire format is tested directly rather
// than through a fake device: the framing is the part with edge cases, and the
// drain loop around it has almost none.
func ParseKmsgRecordForTest(raw []byte) (kmsgRecord, bool) { return parseKmsgRecord(raw) }

// KmsgSuppressWindowForTest and KmsgMaxEventsForTest expose the two folding
// bounds, so a test can assert against them without restating the literal --
// which would then agree with a wrong value as readily as a right one.
const (
	KmsgSuppressWindowForTest = kmsgSuppressWindow
	KmsgMaxEventsForTest      = kmsgMaxEvents
)

// ErrKmsgGapForTest is the EPIPE sentinel, so a stand-in source can report a
// ring that wrapped past the reader.
var ErrKmsgGapForTest = errKmsgGap

// SetCgroupRootForTest repoints the collector at a different fixture tree.
func (c *Containers) SetCgroupRootForTest(root string) { c.cgroupRoot = root }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (c *Containers) SetProcRootForTest(root string) { c.procRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (c *Containers) SetClockForTest(fn func() time.Time) { c.now = fn }

// SetReadlinkForTest replaces the readlink used to resolve namespace links.
func (c *Containers) SetReadlinkForTest(fn func(string) (string, error)) { c.readlink = fn }

// SetProcRootForTest repoints the collector at a different fixture tree so a
// test can simulate the passage of time between two scrapes.
func (c *CPU) SetProcRootForTest(root string) { c.procRoot = root }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (d *DiskIO) SetProcRootForTest(root string) { d.procRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (d *DiskIO) SetClockForTest(fn func() time.Time) { d.now = fn }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (f *Filesystems) SetProcRootForTest(root string) { f.procRoot = root }

// SetProcRootForTest repoints the collector at a different fixture tree so a
// test can simulate the passage of time between two scrapes.
func (k *KernelStat) SetProcRootForTest(root string) { k.procRoot = root }

// SetClockForTest replaces the clock used to measure the interval between two
// scrapes, so rate arithmetic is exact rather than timing-dependent.
func (k *KernelStat) SetClockForTest(fn func() time.Time) { k.now = fn }

// SetSourceForTest replaces the device, the uptime anchor and the clock.
func (k *Kmsg) SetSourceForTest(
	open func() (KmsgSource, error),
	uptime func() (time.Duration, error),
	now func() time.Time,
) {
	k.newSource, k.uptime, k.now = open, uptime, now
	k.src, k.openErr = nil, nil
}

// SetProcRootForTest repoints the collector at a different fixture tree.
func (l *Limits) SetProcRootForTest(root string) { l.procRoot = root }

// SetSysRootForTest repoints the collector at a different fixture tree.
func (m *Mdraid) SetSysRootForTest(root string) { m.sysRoot = root }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (n *Netstat) SetProcRootForTest(root string) { n.procRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (n *Netstat) SetClockForTest(fn func() time.Time) { n.now = fn }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (n *Network) SetProcRootForTest(root string) { n.procRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (n *Network) SetClockForTest(fn func() time.Time) { n.now = fn }

// SetClockForTest replaces the clock used for the daily floor.
func (p *Packages) SetClockForTest(fn func() time.Time) { p.now = fn }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (p *Procs) SetProcRootForTest(root string) { p.procRoot = root }

// SetClockForTest replaces the clock used for the interval gate.
func (s *Smart) SetClockForTest(fn func() time.Time) { s.now = fn }

// SetClockForTest replaces the clock used for the snapshot floor.
func (s *Systemd) SetClockForTest(fn func() time.Time) { s.now = fn }

// SetListerForTest swaps the unit source, so a test can change what systemd
// reports between two scrapes without rebuilding the collector and losing the
// previous state the transition detection depends on.
func (s *Systemd) SetListerForTest(l UnitLister) { s.lister = l }

// SetPathForTest repoints the collector at a fixture file.
func (u *Users) SetPathForTest(path string) { u.path = path }

// SetRecordSizesForTest pins the candidate sizes, so a test can prove a
// fixture parses under one specific layout rather than relying on detection
// happening to pick the right one.
func (u *Users) SetRecordSizesForTest(sizes ...int) { u.recordSizes = sizes }

// SetProcRootForTest repoints the collector at a different fixture tree so a
// test can simulate the passage of time between two scrapes.
func (v *VMStat) SetProcRootForTest(root string) { v.procRoot = root }

// SetClockForTest replaces the clock used to measure the interval.
func (v *VMStat) SetClockForTest(fn func() time.Time) { v.now = fn }
