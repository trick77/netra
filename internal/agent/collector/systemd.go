package collector

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// Unit is one systemd unit's state.
type Unit struct {
	Name     string
	Active   string // active, failed, inactive
	SubState string // running, exited, dead
}

// UnitLister enumerates systemd units.
//
// Injected so the collector is testable without systemd, which the machines
// running these tests do not have.
type UnitLister func(ctx context.Context) ([]Unit, error)

// SystemUnits is the production UnitLister. It calls
// org.freedesktop.systemd1.Manager.ListUnits on the system bus.
//
// D-Bus rather than shelling out to `systemctl list-units`, which is what this
// collector did until it was found never to run at all. systemctl is itself
// only a formatter around this same method call, and it is NOT present in the
// agent image: netra-agent ships on Alpine, and systemd is not packaged for
// musl at any version. Bind-mounting the host's binary does not help either --
// it is linked against glibc and will not execute against musl. So every host
// reported a "systemd: unavailable" capability and no unit data whatsoever,
// while deploy/agent/compose.yaml.example already documented the bus socket
// mount this function actually needs.
//
// Talking to the bus directly also removes the column parsing that stood
// between systemd and the three fields netra wants, including the leading
// bullet systemd puts on a failed unit -- the units that matter most were the
// ones most likely to be misparsed.
//
// A fresh connection per scrape, closed on the way out, rather than one held
// open: the call runs once a minute, so the cost is immaterial next to running
// smartctl, and a connection that is never reused cannot go stale when dbus or
// systemd is restarted under the agent.
func SystemUnits(ctx context.Context) ([]Unit, error) {
	conn, err := dbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to the system bus: %w", err)
	}
	defer conn.Close()

	statuses, err := conn.ListUnitsContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list units: %w", err)
	}

	units := make([]Unit, 0, len(statuses))
	for _, s := range statuses {
		// ListUnits returns every LOADED unit of every type, which is what
		// `list-units --all` printed. Services are the only type with a
		// failure an operator acts on, and the summary counters on the host
		// row are defined as service counts.
		if !strings.HasSuffix(s.Name, ".service") {
			continue
		}
		units = append(units, Unit{
			Name:     s.Name,
			Active:   s.ActiveState,
			SubState: s.SubState,
		})
	}
	return units, nil
}

// Systemd reports unit state changes as EVENTS, plus a numeric summary on the
// host row.
//
// Events rather than samples for the same reason as mdraid: a unit's state is
// constant for days, so a 60s series saying "running" is the near-constant
// waste that keeps systemd out of spec §5.3. The two numbers an operator
// dashboards -- how many services exist and how many are failed -- ride
// host_samples, where they are cheap.
type Systemd struct {
	lister UnitLister

	prev        map[string]Unit
	unavailable bool
}

// NewSystemd builds a Systemd collector.
func NewSystemd(lister UnitLister) *Systemd {
	return &Systemd{lister: lister}
}

// SetListerForTest swaps the unit source, so a test can change what systemd
// reports between two scrapes without rebuilding the collector and losing the
// previous state the transition detection depends on.
func (s *Systemd) SetListerForTest(l UnitLister) { s.lister = l }

// EmitsBaseline implements BaselineEmitter, keeping this collector out of the
// agent's startup priming. Its first Collect reports the units that are
// already failed, and priming would discard exactly that.
func (s *Systemd) EmitsBaseline() bool { return true }

// Name implements Collector.
func (s *Systemd) Name() string { return "systemd" }

// ResendInventory implements InventoryResender.
//
// Forgetting the last seen units re-arms the baseline path in Collect, which
// re-reports the units that are FAILED -- the same bounded set an agent
// restart emits, not every loaded unit. Without it, a scrape carrying
// "nginx.service went failed" that the ring dropped left the hub serving
// "active" permanently: this collector is event-based precisely so the last
// event is the state, and from its point of view nothing changed afterwards.
func (s *Systemd) ResendInventory() { s.prev = nil }

// Capabilities implements CapabilityReporter.
func (s *Systemd) Capabilities() map[string]string {
	if s.unavailable {
		// A host running OpenRC, or an agent started without the system bus
		// socket mounted, is not broken; it has no systemd to ask.
		// Distinguishing that from "zero services" is the whole point of
		// saying so.
		return map[string]string{"systemd": "unavailable"}
	}
	return nil
}

// Collect implements Collector.
func (s *Systemd) Collect(ctx context.Context) (*Result, error) {
	units, err := s.lister(ctx)
	if err != nil {
		s.unavailable = true
		return &Result{}, nil
	}
	s.unavailable = false

	cur := make(map[string]Unit, len(units))
	var failed uint32
	for _, u := range units {
		cur[u.Name] = u
		if u.Active == "failed" {
			failed++
		}
	}

	prev := s.prev
	s.prev = cur

	names := make([]string, 0, len(cur))
	for name := range cur {
		names = append(names, name)
	}
	slices.Sort(names)

	ts := time.Now().UnixMilli()
	var events []*netrav1.SystemdUnitEvent

	// The first scrape emits a BASELINE, but only of the units that are
	// FAILED.
	//
	// The baseline exists because a unit already failed when the agent started
	// would otherwise produce no event at all, and only the services_failed
	// counter would reveal it -- with no unit name and no "since when". That
	// is the question this table exists to answer, and it cannot answer it
	// about a failure predating the agent unless the agent says what it found
	// on arrival.
	//
	// Restricted to failed units because the volume is nothing like mdraid's.
	// mdraid baselines a handful of arrays; every loaded .service on a normal
	// host is 200-400, most of them inactive/dead oneshots that say nothing.
	// systemd_unit_events is deliberately a plain table with no retention
	// policy, sized on the premise that "a unit changes state a handful of
	// times a month" -- so an unrestricted baseline would write a few hundred
	// unprunable rows per host on EVERY agent restart, and a crash-looping
	// agent or a fleet redeploy would multiply that. A failed unit is the rare
	// case by construction, so this is normally zero rows and never more than
	// a handful.
	//
	// This is the one place this collector deliberately differs from mdraid.
	for _, name := range names {
		u := cur[name]
		p, seen := prev[name]

		if !seen && prev == nil {
			// First scrape. Report it only if it is already broken; a healthy
			// unit's state is not news, and saying so for every unit on the
			// host is what the restriction above avoids.
			if u.Active == "failed" {
				events = append(events, unitEvent(ts, name, u))
			}
			continue
		}

		if seen && p.Active == u.Active && p.SubState == u.SubState {
			// Unchanged. Emitting anyway would turn the event table into
			// the 60s series this collector exists to avoid.
			continue
		}

		// A transition, including a unit appearing for the first time after
		// the baseline scrape -- an installed-and-started service is a change
		// worth recording whatever state it landed in.
		events = append(events, unitEvent(ts, name, u))
	}

	// The summary rides the host row, where two integers cost nothing, rather
	// than forcing a dashboard to count rows in an event table.
	return &Result{
		Host: &netrav1.HostSample{
			ServicesTotal:  ptrTo(uint32(len(units))),
			ServicesFailed: ptrTo(failed),
		},
		SystemdEvents: events,
	}, nil
}

// unitEvent builds one unit-state event.
func unitEvent(ts int64, name string, u Unit) *netrav1.SystemdUnitEvent {
	return &netrav1.SystemdUnitEvent{
		TsMs:     ts,
		UnitName: name,
		State:    u.Active,
		Substate: u.SubState,
	}
}
