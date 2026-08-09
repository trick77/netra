package collector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
)

func fakeUnits(units ...collector.Unit) collector.UnitLister {
	return func(context.Context) ([]collector.Unit, error) { return units, nil }
}

// The two numbers an operator dashboards ride host_samples, where two integers
// cost nothing -- rather than forcing every dashboard to count rows in an
// event table.
func TestSystemdReportsTheSummaryOnTheHostRow(t *testing.T) {
	testee := collector.NewSystemd(time.Minute, fakeUnits(
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

// A unit's state is constant for days, so only the transition is stored. The
// first scrape establishes the baseline and emits nothing -- otherwise an
// agent start would deliver one event per unit on the host, hundreds of rows
// saying nothing happened.
func TestSystemdEmitsNoEventsOnTheFirstScrape(t *testing.T) {
	testee := collector.NewSystemd(time.Minute, fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
	))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.SystemdEvents) != 0 {
		t.Errorf("events on the first scrape = %d, want 0", len(res.SystemdEvents))
	}
}

func TestSystemdEmitsNothingWhileUnitsAreUnchanged(t *testing.T) {
	lister := fakeUnits(collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"})
	testee := collector.NewSystemd(time.Minute, lister)

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
	testee := collector.NewSystemd(time.Minute, fakeUnits(
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
	testee := collector.NewSystemd(time.Minute, func(context.Context) ([]collector.Unit, error) {
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

// The columns systemctl prints are UNIT LOAD ACTIVE SUB DESCRIPTION, and a
// failed unit is bulleted in some versions.
func TestSystemdParsesSystemctlOutput(t *testing.T) {
	out := []byte(`ssh.service          loaded active   running OpenBSD Secure Shell server
● broken.service     loaded failed   failed  A unit that failed
docker.socket        loaded active   running Docker Socket
`)
	units := collector.ParseSystemctlForTest(out)

	if len(units) != 2 {
		t.Fatalf("units = %d, want 2 (.socket is not a service)", len(units))
	}
	if units[0].Name != "ssh.service" || units[0].Active != "active" {
		t.Errorf("unit 0 = %+v", units[0])
	}
	if units[1].Name != "broken.service" {
		t.Errorf("unit 1 name = %q; the bullet must be stripped", units[1].Name)
	}
	if units[1].Active != "failed" {
		t.Errorf("unit 1 active = %q, want failed", units[1].Active)
	}
}
