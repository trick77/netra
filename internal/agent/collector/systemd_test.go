package collector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
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

// The first scrape emits a BASELINE: one event per unit, saying what the agent
// found on arrival. Same as mdraid, and for the same reason.
//
// A unit that was ALREADY failed when the agent started is the case that
// forces it. Emitting nothing until the next transition means a host whose
// database has been down for a week produces no event for it at all, and only
// the services_failed counter reveals anything is wrong -- with no unit name
// and no "since when". That is precisely the question this table exists to
// answer.
func TestSystemdEmitsABaselineOnTheFirstScrape(t *testing.T) {
	testee := collector.NewSystemd(time.Minute, fakeUnits(
		collector.Unit{Name: "ssh.service", Active: "active", SubState: "running"},
		collector.Unit{Name: "nginx.service", Active: "failed", SubState: "failed"},
	))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.SystemdEvents) != 2 {
		t.Fatalf("events on the first scrape = %d, want 2 (one per unit)", len(res.SystemdEvents))
	}

	var failed *netrav1.SystemdUnitEvent
	for _, e := range res.SystemdEvents {
		if e.GetUnitName() == "nginx.service" {
			failed = e
		}
	}
	if failed == nil {
		t.Fatal("no event for nginx.service; a unit already failed at agent start must be reported")
	}
	if got := failed.GetState(); got != "failed" {
		t.Errorf("nginx.service state = %q, want failed", got)
	}
	if failed.GetTsMs() == 0 {
		t.Error("baseline event carries no ts_ms")
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
