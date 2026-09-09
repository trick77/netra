package collector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
)

func fakeUnits(units ...collector.Unit) collector.UnitLister {
	return func(context.Context) ([]collector.Unit, error) { return units, nil }
}

// The two numbers an operator dashboards ride host_samples, where two integers
// cost nothing -- rather than forcing every dashboard to count rows in an
// event table.
func TestSystemdReportsTheSummaryOnTheHostRow(t *testing.T) {
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
		collector.Unit{Name: "nginx.service", Active: "active", SubState: "running"},
		collector.Unit{Name: "broken.service", Active: "failed", SubState: "failed"},
	))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Host == nil {
		t.Fatal("no host contribution; the summary must ride host_samples")
	}
	if got := res.Host.GetServicesTotal(); got != 3 {
		t.Errorf("services_total = %d, want 3", got)
	}
	if got := res.Host.GetServicesFailed(); got != 1 {
		t.Errorf("services_failed = %d, want 1", got)
	}
}

// The first scrape emits NO events, and states everything in the SNAPSHOT.
//
// This collector used to baseline the already-failed units as events, so that
// a unit broken before the agent started said something more than the
// services_failed counter. The snapshot answers that and the case the event
// baseline never could -- a unit that RECOVERED while the agent was down --
// so the events are pure transitions now.
//
// Nothing is emitted per unit for the reason the baseline was always
// restricted to failures: every loaded .service on a normal host is 200-400,
// mostly inactive/dead oneshots, and systemd_unit_events is a plain table with
// no retention policy.
func TestSystemdFirstScrapeStatesUnitsInTheSnapshotNotAsEvents(t *testing.T) {
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
		collector.Unit{Name: "nginx.service", Active: "failed", SubState: "failed"},
		collector.Unit{Name: "cleanup.service", Active: "inactive", SubState: "dead"},
	))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(res.SystemdEvents) != 0 {
		t.Fatalf("events on the first scrape = %d, want 0 -- arrival is not a transition",
			len(res.SystemdEvents))
	}

	snap := res.SystemdSnapshot
	if snap == nil {
		t.Fatal("first scrape carried no snapshot; nothing would say the host has a failed unit")
	}
	if !snap.GetComplete() {
		t.Error("snapshot is not marked complete")
	}
	if snap.GetTsMs() == 0 {
		t.Error("snapshot carries no ts_ms")
	}

	states := map[string]string{}
	for _, u := range snap.GetUnits() {
		states[u.GetUnitName()] = u.GetState()
	}
	// Every unit, not just the broken one: the snapshot is what IS, and it is
	// what lets the hub retire a failure it never saw end.
	want := map[string]string{
		"ssh.service":     "active",
		"nginx.service":   "failed",
		"cleanup.service": "inactive",
	}
	for name, state := range want {
		if states[name] != state {
			t.Errorf("snapshot state for %s = %q, want %q", name, states[name], state)
		}
	}
}

// A healthy unit that fails LATER must still produce an event: the baseline
// restriction applies only to the first scrape, not to transitions.
func TestSystemdEmitsAnEventWhenAUnitFailsAfterAHealthyBaseline(t *testing.T) {
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
	))
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if len(res.SystemdEvents) != 0 {
		t.Fatalf("baseline emitted %d events for a healthy host, want 0", len(res.SystemdEvents))
	}

	testee.SetListerForTest(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "failed", SubState: "failed"},
	))
	res, err = testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.SystemdEvents) != 1 {
		t.Fatalf("events on failure = %d, want 1", len(res.SystemdEvents))
	}
}

// A unit that appears after the baseline scrape is a change worth recording
// whatever state it landed in -- an installed-and-started service is news even
// though a healthy unit present at startup is not.
func TestSystemdEmitsAnEventForAUnitThatAppearsLater(t *testing.T) {
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
	))
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	testee.SetListerForTest(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
		collector.Unit{Name: "nginx.service", Active: "active", SubState: "running"},
	))
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(res.SystemdEvents) != 1 {
		t.Fatalf("events = %d, want 1 for the newly installed unit", len(res.SystemdEvents))
	}
	if got := res.SystemdEvents[0].GetUnitName(); got != "nginx.service" {
		t.Errorf("unit_name = %q, want nginx.service", got)
	}
}

func TestSystemdEmitsNothingWhileUnitsAreUnchanged(t *testing.T) {
	lister := fakeUnits(collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"})
	testee := collector.NewSystemd(lister)

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	for i := range 3 {
		res, err := testee.Collect(context.Background())
		if err != nil {
			t.Fatalf("Collect %d: %v", i, err)
		}
		if len(res.SystemdEvents) != 0 {
			t.Fatalf("scrape %d emitted %d events for unchanged units, want 0", i+2, len(res.SystemdEvents))
		}
	}
}

// The transition IS the data: a service failing must produce an event the
// moment it does, and exactly once.
func TestSystemdEmitsAnEventWhenAUnitFails(t *testing.T) {
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
	))
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	testee.SetListerForTest(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "failed", SubState: "failed"},
	))
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(res.SystemdEvents) != 1 {
		t.Fatalf("events on failure = %d, want 1", len(res.SystemdEvents))
	}
	ev := res.SystemdEvents[0]
	if ev.GetUnitName() != "ssh.service" {
		t.Errorf("unit_name = %q", ev.GetUnitName())
	}
	if ev.GetState() != "failed" {
		t.Errorf("state = %q, want failed", ev.GetState())
	}
	if ev.GetTsMs() == 0 {
		t.Error("event carries no ts_ms")
	}

	// And the failure is reported once, not once a minute for as long as it
	// lasts -- the new state becomes the baseline.
	res, err = testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if len(res.SystemdEvents) != 0 {
		t.Errorf("events while still failed = %d, want 0", len(res.SystemdEvents))
	}
}

// A host running OpenRC, or an agent started without /run/systemd, has no
// systemd. That is not a failure, and it must be distinguishable from a host
// with zero services.
func TestSystemdReportsUnavailableAsACapability(t *testing.T) {
	testee := collector.NewSystemd(func(context.Context) ([]collector.Unit, error) {
		return nil, errors.New("exec: systemctl: executable file not found in $PATH")
	})

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v, want no error when systemd is absent", err)
	}
	if res.Host != nil {
		t.Error("host contribution present with no systemd; services_total must stay unset rather than 0")
	}
	if got := testee.Capabilities()["systemd"]; got != "unavailable" {
		t.Errorf("capability = %q, want unavailable", got)
	}
}

// A scrape carrying a unit's transition can be lost -- the ring overflowing
// during a hub outage, or the whole buffer being dumped after a 401. This
// collector only speaks on change and the last event IS the state, so without
// a re-arm the hub keeps serving "active" for a unit that failed, forever.
func TestSystemdResendInventoryReArmsTheFailedBaseline(t *testing.T) {
	// Given: a collector that has already reported a healthy host.
	testee := collector.NewSystemd(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
		collector.Unit{Name: "nginx.service", Active: "active", SubState: "running"},
	))
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("baseline Collect: %v", err)
	}

	// And: nginx fails, in a scrape the agent never delivered.
	testee.SetListerForTest(fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
		collector.Unit{Name: "nginx.service", Active: "failed", SubState: "failed"},
	))
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("lost Collect: %v", err)
	}

	// When: the agent tells it that scrape was lost, and it scrapes again.
	var resender collector.InventoryResender = testee
	resender.ResendInventory()

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect after re-arm: %v", err)
	}

	// Then: the hub is told the failure again -- through the snapshot, which
	// the re-arm re-triggers by forgetting the previous states. Events stay
	// transitions, and a re-arm has no transition to report.
	if len(res.SystemdEvents) != 0 {
		t.Fatalf("events after re-arm = %d, want 0 -- the snapshot carries the state", len(res.SystemdEvents))
	}

	snap := res.SystemdSnapshot
	if snap == nil {
		t.Fatal("no snapshot after re-arm; the hub would keep serving active for a failed unit")
	}
	states := map[string]string{}
	for _, u := range snap.GetUnits() {
		states[u.GetUnitName()] = u.GetState()
	}
	if states["nginx.service"] != "failed" {
		t.Errorf("nginx.service = %q after re-arm, want failed", states["nginx.service"])
	}
	if states["ssh.service"] != "active" {
		t.Errorf("ssh.service = %q after re-arm, want active", states["ssh.service"])
	}
}
