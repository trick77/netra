package collector

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
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
	interval time.Duration
	lister   UnitLister

	prev        map[string]Unit
	unavailable bool
}

// NewSystemd builds a Systemd collector.
func NewSystemd(interval time.Duration, lister UnitLister) *Systemd {
	return &Systemd{interval: interval, lister: lister}
}

// SetListerForTest swaps the unit source, so a test can change what systemd
// reports between two scrapes without rebuilding the collector and losing the
// previous state the transition detection depends on.
func (s *Systemd) SetListerForTest(l UnitLister) { s.lister = l }

// EmitsBaseline implements BaselineEmitter, keeping this collector out of the
// agent's startup priming. Its first Collect reports every unit's state, and
// priming would discard exactly that.
func (s *Systemd) EmitsBaseline() bool { return true }

// Name implements Collector.
func (s *Systemd) Name() string { return "systemd" }

// Interval implements Collector.
func (s *Systemd) Interval() time.Duration { return s.interval }

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

	// The first scrape emits a BASELINE: prev is nil, nothing matches, so every
	// unit produces one event. Same as mdraid, and for the same reason -- a
	// unit that was already failed when the agent started would otherwise
	// produce no event at all, and only the services_failed counter would
	// reveal it. "Which units are failed, and since when" is the question this
	// table exists to answer, and it cannot answer it about a failure that
	// predates the agent unless the agent says what it found on arrival.
	//
	// The volume is the one real difference from mdraid, which baselines a
	// handful of arrays while this baselines every .service on the host --
	// a few hundred rows, once per agent start. That is a bounded one-off, not
	// a per-scrape cost: the loop below still emits nothing for an unchanged
	// unit, so the steady state is unaffected.
	for _, name := range names {
		u := cur[name]
		p, seen := prev[name]
		if seen && p.Active == u.Active && p.SubState == u.SubState {
			// Unchanged. Emitting anyway would turn the event table into
			// the 60s series this collector exists to avoid.
			continue
		}
		events = append(events, &netrav1.SystemdUnitEvent{
			TsMs:     ts,
			UnitName: name,
			State:    u.Active,
			Substate: u.SubState,
		})
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
