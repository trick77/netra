package collector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
)

// clock returns a controllable time source and a knob to advance it, so the
// snapshot floor can be tested without a test that sleeps for five minutes.
func clock(start time.Time) (func() time.Time, func(time.Duration)) {
	at := start
	return func() time.Time { return at }, func(d time.Duration) { at = at.Add(d) }
}

// The very first scrape must carry a snapshot.
//
// It is the scrape after an agent restart, which is precisely when the hub's
// stored state and the host's reality are most likely to disagree: anything
// that changed while the agent was down produced no event, and the failed-only
// baseline can announce a failure it found but never a recovery.
func TestSystemdSnapshotOnTheFirstScrape(t *testing.T) {
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
		collector.Unit{Name: "exim4.service", Active: "failed", SubState: "failed"},
	))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	snap := res.SystemdSnapshot
	if snap == nil {
		t.Fatal("no snapshot on the first scrape; the hub has nothing to reconcile against")
	}
	if !snap.GetComplete() {
		t.Error("complete = false; the hub prunes only on a complete snapshot, so this " +
			"one would never clear a unit that vanished")
	}
	if len(snap.GetUnits()) != 2 {
		t.Fatalf("snapshot carries %d units, want every loaded service, healthy ones "+
			"included -- a snapshot of only the failures cannot state a recovery",
			len(snap.GetUnits()))
	}

	// One scrape, one voice: the hub's monotonic guard compares the events and
	// the snapshot against the same stored state_ts, so two timestamps a
	// microsecond apart would make the winner depend on statement order.
	for _, e := range res.SystemdEvents {
		if e.GetTsMs() != snap.GetTsMs() {
			t.Errorf("event ts %d != snapshot ts %d", e.GetTsMs(), snap.GetTsMs())
		}
	}
}

// Between snapshots the collector stays quiet, and after the floor it speaks
// again.
func TestSystemdSnapshotIsPacedByTheFloor(t *testing.T) {
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
	))
	now, advance := clock(time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))
	testee.SetClockForTest(now)

	if res, _ := testee.Collect(context.Background()); res.SystemdSnapshot == nil {
		t.Fatal("no snapshot on the first scrape")
	}

	// A minute later: nothing to repair, so nothing to send. Sending one every
	// scrape would be 400 units a minute per host for a set that changes a
	// handful of times a month.
	advance(time.Minute)
	if res, _ := testee.Collect(context.Background()); res.SystemdSnapshot != nil {
		t.Error("a snapshot one minute in; the floor exists so this is the quiet case")
	}

	advance(5 * time.Minute)
	if res, _ := testee.Collect(context.Background()); res.SystemdSnapshot == nil {
		t.Error("no snapshot after the floor elapsed; divergence would go unrepaired")
	}
}

// A ring drop must not have to wait out the floor.
//
// ResendInventory runs when the agent has just lost scrapes. Those scrapes may
// have carried the recovery that would have cleared a warning, so the repair
// is wanted now rather than up to five minutes later, with a stale warning on
// the page in the meantime.
func TestSystemdResendInventoryForcesASnapshot(t *testing.T) {
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
	))
	now, advance := clock(time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))
	testee.SetClockForTest(now)

	_, _ = testee.Collect(context.Background())
	advance(time.Minute)
	if res, _ := testee.Collect(context.Background()); res.SystemdSnapshot != nil {
		t.Fatal("the floor is not holding; the rest of this test proves nothing")
	}

	testee.ResendInventory()
	if res, _ := testee.Collect(context.Background()); res.SystemdSnapshot == nil {
		t.Error("no snapshot after ResendInventory; a dropped recovery would stay " +
			"on the page until the floor elapsed")
	}
}

// A scrape that could not read the bus sends NO snapshot.
//
// The hub deletes every unit missing from a complete snapshot. An empty one
// sent from here would say "this host runs no services", and one wedged D-Bus
// call would clear every warning on the host -- looking exactly like a fix.
func TestSystemdWedgedListerSendsNoSnapshot(t *testing.T) {
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "exim4.service", Active: "failed", SubState: "failed"},
	))
	if res, _ := testee.Collect(context.Background()); res.SystemdSnapshot == nil {
		t.Fatal("no snapshot on the first scrape")
	}

	testee.SetListerForTest(func(context.Context) ([]collector.Unit, error) {
		return nil, errors.New("dial unix /run/dbus/system_bus_socket: no such file")
	})

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.SystemdSnapshot != nil {
		t.Fatalf("a wedged lister produced a snapshot of %d units; absence of data "+
			"must never be sent as evidence that nothing is wrong",
			len(res.SystemdSnapshot.GetUnits()))
	}
}

// The snapshot reports states as they are, including the substate, because
// systemd puts a unit in its restart backoff at activating/auto-restart -- the
// state alone would report a service that has been crashing for an hour as
// merely starting up.
func TestSystemdSnapshotCarriesSubstates(t *testing.T) {
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "backup.service", Active: "activating", SubState: "auto-restart"},
	))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	units := res.SystemdSnapshot.GetUnits()
	if len(units) != 1 {
		t.Fatalf("got %d units, want 1", len(units))
	}
	if got := units[0].GetSubstate(); got != "auto-restart" {
		t.Errorf("substate = %q, want auto-restart -- the only place a restart loop shows", got)
	}
}
