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
// ONE connection, held for the life of the process and redialled only when a
// call on it fails.
//
// This took a fresh connection per scrape for a while, on the reasoning that
// the cost was immaterial next to running smartctl and that a connection never
// reused could not go stale. Both halves turned out to be wrong. smartctl
// self-gates to once an hour, so it is not what a scrape is spent on: measured
// across the fleet, `systemd` was 15ms of a ~100ms scrape, second only to
// `containers`. And the dial is not one connection --
// dbus.NewSystemConnectionContext calls NewConnection, which dials the bus
// TWICE (a call connection and a signal connection, each with its own SASL
// EXTERNAL handshake and Hello), adds a JobRemoved match rule, and starts a
// dispatch goroutine. netra subscribes to no signals and calls exactly one
// method, so all of that was set up and torn down every 60 seconds to ask one
// question.
//
// Staleness is handled where it actually occurs instead of being paid for in
// advance: a dbus or systemd restart under the agent breaks the held
// connection, listUnits sees the call fail, drops it, and redials ONCE within
// the same scrape. So the restart costs nothing rather than a scrape's worth
// of unit state -- which is better than the fresh-connection version managed,
// not merely as good.
func SystemUnits(ctx context.Context) ([]Unit, error) {
	statuses, err := listUnits(ctx)
	if err != nil {
		return nil, err
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

// unitConn is the part of a systemd bus connection SystemUnits uses.
//
// An interface rather than *dbus.Conn so the held-connection handling can be
// exercised without a system bus. That handling IS the change this belongs to,
// and pinning it to a concrete connection would have left it tested only in
// production.
type unitConn interface {
	ListUnitsContext(ctx context.Context) ([]dbus.UnitStatus, error)
	Close()
}

// systemBus is the connection SystemUnits holds between scrapes.
var systemBus heldBus[unitConn]

// dialSystemBus opens a connection whose lifetime is the PROCESS's, not the
// scrape's. A var so a test can substitute a connection.
//
// context.WithoutCancel is not decoration here, and getting it wrong is
// silent. go-systemd hands the context straight to godbus as WithContext, and
// godbus spawns a goroutine that CLOSES the connection as soon as that context
// is done (newConn, conn.go). The scrape context is cancelled by collect's own
// `defer cancel()` at the end of every scrape -- so a connection dialled with
// it dies the instant the scrape that dialled it finishes, and callBus's
// redial would hide that completely: every minute would quietly pay a dial, a
// failed call and a second dial, which is worse than the fresh-connection
// version this replaces rather than better.
//
// The scrape's deadline still bounds the CALL below, which is where the time
// actually goes.
var dialSystemBus = func(ctx context.Context) (unitConn, error) {
	return dbus.NewSystemConnectionContext(context.WithoutCancel(ctx))
}

// listUnits calls ListUnits on the held connection, redialling once if the
// call fails on a connection that has gone stale. See callBus.
func listUnits(ctx context.Context) ([]dbus.UnitStatus, error) {
	// Wrapped inside the call rather than around callBus, which already names
	// a dial failure as one: wrapping outside turned "could not reach the bus"
	// into "list units: connect to the system bus: ...", blaming the method
	// for a connection that was never made.
	return callBus(ctx, &systemBus,
		dialSystemBus,
		func(c unitConn) { c.Close() },
		func(c unitConn) ([]dbus.UnitStatus, error) {
			statuses, err := c.ListUnitsContext(ctx)
			if err != nil {
				return nil, fmt.Errorf("list units: %w", err)
			}
			return statuses, nil
		},
	)
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

	// now and lastSnapshot pace the level-triggered snapshot. See
	// snapshotFloor.
	now          func() time.Time
	lastSnapshot time.Time
}

// snapshotFloor is how often the full unit set is resent.
//
// The snapshot exists to repair divergence, not to report it: a failure still
// reaches the hub in one scrape through the event path, so this only bounds
// how long a unit can stay WRONG after the one transition that would have
// fixed it was never sent. Five minutes is short enough that an operator who
// ran `systemctl reset-failed` sees the warning go away while still looking at
// the page, and long enough that the cost is nothing -- the hub's upsert is
// written so that an unchanged snapshot performs zero writes, so the only
// recurring cost is ~24KB on the wire.
const snapshotFloor = 5 * time.Minute

// NewSystemd builds a Systemd collector.
func NewSystemd(lister UnitLister) *Systemd {
	return &Systemd{lister: lister, now: time.Now}
}

// SetClockForTest replaces the clock used for the snapshot floor.
func (s *Systemd) SetClockForTest(fn func() time.Time) { s.now = fn }

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
//
// Clearing lastSnapshot as well is what repairs the MIRROR case, which the
// baseline alone cannot: a dropped scrape carrying "nginx.service recovered"
// left the hub serving "failed" just as permanently, and a failed-only
// baseline says nothing about a unit that is now healthy. Without this line
// the next snapshot -- the only message that can state a recovery the hub
// missed -- waits out snapshotFloor while the page shows a warning that is no
// longer true.
func (s *Systemd) ResendInventory() {
	s.prev = nil
	s.lastSnapshot = time.Time{}
}

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
		// No units, and therefore NO SNAPSHOT -- which is the point, not an
		// omission. The hub prunes units missing from a complete snapshot, so
		// an empty-but-complete one sent from here would tell it this host has
		// no services at all and clear every warning on it. A scrape the bus
		// refused is a scrape with no information, and the hub must keep
		// serving what it already knows (see 26a42a5: a wedged collector costs
		// one scrape, not the agent).
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

	// Through the same clock the snapshot floor is paced by, so a test that
	// moves the clock moves the timestamps with it. Reading time.Now here
	// while the floor read s.now made SetClockForTest half a clock: the
	// snapshot's ts_ms -- the value every monotonic guard in the hub compares
	// against -- came from wall time no matter what the test set.
	ts := s.now().UnixMilli()
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
	// systemd_unit_events is a plain table pruned only at 90 days
	// (netra_prune_discrete_events), sized on the premise that "a unit changes
	// state a handful of times a month" -- so an unrestricted baseline would
	// write a few hundred rows per host on EVERY agent restart, and a
	// crash-looping agent or a fleet redeploy would multiply that. A failed
	// unit is the rare case by construction, so this is normally zero rows and
	// never more than a handful.
	//
	// This is the one place this collector deliberately differs from mdraid.
	//
	// The asymmetry -- it can announce a failure it found on arrival, but
	// never a RECOVERY that happened while the agent was down -- is what the
	// snapshot below now covers, so this branch is strictly redundant against
	// a current hub. It stays for one release because agents roll forward
	// ahead of hubs: deploy/agent/compose.yaml.tmpl pins netra-agent:latest,
	// so a host can be running an agent that speaks SystemdSnapshot to a hub
	// that ignores it, and dropping this would leave that pair reporting no
	// failures at all. DELETE THIS BRANCH once no supported hub predates the
	// snapshot.
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

	// The snapshot: what IS, rather than what changed.
	//
	// Sent on the first scrape (when prev is nil, so nothing above could have
	// produced a transition) and every snapshotFloor after. Reusing the events'
	// ts rather than reading the clock again keeps one scrape speaking with one
	// voice -- the hub's monotonic guard compares the two against the same
	// stored state_ts, and two timestamps a microsecond apart would make which
	// one wins depend on statement order.
	var snapshot *netrav1.SystemdSnapshot
	if now := s.now(); prev == nil || now.Sub(s.lastSnapshot) >= snapshotFloor {
		s.lastSnapshot = now
		states := make([]*netrav1.SystemdUnitState, 0, len(names))
		for _, name := range names {
			u := cur[name]
			states = append(states, &netrav1.SystemdUnitState{
				UnitName: name,
				State:    u.Active,
				Substate: u.SubState,
			})
		}
		snapshot = &netrav1.SystemdSnapshot{
			TsMs: ts,
			// Every loaded .service the bus returned. The lister error above
			// returns before this point, so a snapshot is only ever built from
			// a successful ListUnits -- there is no path that sets this on a
			// partial set.
			Complete: true,
			Units:    states,
		}
	}

	// The summary rides the host row, where two integers cost nothing, rather
	// than forcing a dashboard to count rows in an event table.
	return &Result{
		Host: &netrav1.HostSample{
			ServicesTotal:  ptrTo(uint32(len(units))),
			ServicesFailed: ptrTo(failed),
		},
		SystemdEvents:   events,
		SystemdSnapshot: snapshot,
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
