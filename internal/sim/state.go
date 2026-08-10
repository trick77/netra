package sim

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// timedEvent is one discrete state change, waiting for the grid to reach it.
// Exactly one of the three payloads is set.
//
// These exist because events, systemd_unit_events and package_events only
// ever receive a row on a TRANSITION. A simulator that emits samples and
// nothing else leaves all three tables -- and every index and retention
// policy on them -- permanently empty, which is precisely the state in which
// a query against them looks like it works.
type timedEvent struct {
	ts    time.Time
	unit  *netrav1.SystemdUnitEvent
	pkg   *netrav1.PackageEvent
	event *netrav1.Event
}

// schedule is a host's discrete events over the whole simulated window,
// sorted by time and consumed in order as the grid advances.
type schedule struct {
	events []timedEvent
	cursor int
}

// newSchedule lays out every transition for one host across [from,to).
//
// The first instant carries a baseline for the change-driven collectors: the
// real agent's systemd and mdraid collectors report their whole state on
// first sight, because a hub that has never heard of a unit cannot tell
// "active and unchanged for a year" from "never existed".
func newSchedule(p *Profile, s signal, from, to time.Time) *schedule {
	var evs []timedEvent

	for _, unit := range p.Units {
		evs = append(evs, timedEvent{ts: from, unit: &netrav1.SystemdUnitEvent{
			UnitName: unit, State: "active", Substate: "running",
		}})
	}
	if p.Mdraid != "" {
		evs = append(evs, timedEvent{ts: from, event: &netrav1.Event{
			Type:       "mdraid",
			Subject:    p.Mdraid,
			DetailJson: `{"level":"raid10","state":"clean","degraded":0,"disks":4}`,
		}})
	}

	evs = append(evs, unitFailures(p, s, from, to)...)
	evs = append(evs, packageChanges(p, s, from, to)...)
	evs = append(evs, mdraidTrouble(p, s, from, to)...)

	sort.SliceStable(evs, func(i, j int) bool { return evs[i].ts.Before(evs[j].ts) })
	return &schedule{events: evs}
}

// due returns every event that has come due at or before ts, advancing the
// cursor. The grid is walked oldest-first, so a linear cursor is enough and
// no event can be visited twice.
func (sc *schedule) due(ts time.Time) []timedEvent {
	start := sc.cursor
	for sc.cursor < len(sc.events) && !sc.events[sc.cursor].ts.After(ts) {
		sc.cursor++
	}
	return sc.events[start:sc.cursor]
}

// failedUnits reports how many of this host's units are in the failed state
// at ts, which is what host_samples.services_failed carries. It re-derives
// the answer from the schedule rather than tracking it as mutable state, so
// it stays correct whatever order the grid is walked in.
func (sc *schedule) failedUnits(ts time.Time) uint32 {
	state := map[string]string{}
	for _, e := range sc.events {
		if e.ts.After(ts) {
			break
		}
		if e.unit != nil {
			state[e.unit.GetUnitName()] = e.unit.GetState()
		}
	}
	var n uint32
	for _, st := range state {
		if st == "failed" {
			n++
		}
	}
	return n
}

// unitFailureInterval is roughly how often one of a host's units falls over.
// Long enough that a failure is an event rather than the norm, short enough
// that a 90-day window contains several.
const unitFailureInterval = 9 * 24 * time.Hour

// unitFailures makes one unit fail and recover, repeatedly, across the
// window. A failure that never recovers would be indistinguishable from a
// collector that stopped reporting.
func unitFailures(p *Profile, s signal, from, to time.Time) []timedEvent {
	if len(p.Units) == 0 {
		return nil
	}

	var evs []timedEvent
	for i, at := 0, from.Add(unitFailureInterval); at.Before(to); i, at = i+1, at.Add(unitFailureInterval) {
		key := fmt.Sprintf("%s/unitfail/%d", p.Hostname, i)
		unit := p.Units[int(s.unit(key, at)*float64(len(p.Units)))%len(p.Units)]
		// Somewhere between 20 minutes and just over three hours down.
		down := time.Duration(20+int(s.unit(key+"/dur", at)*160)) * time.Minute

		evs = append(evs,
			timedEvent{ts: at, unit: &netrav1.SystemdUnitEvent{
				UnitName: unit, State: "failed", Substate: "failed",
			}},
			timedEvent{ts: at.Add(down), unit: &netrav1.SystemdUnitEvent{
				UnitName: unit, State: "active", Substate: "running",
			}},
		)
	}
	return evs
}

// packageUpgradeInterval is how often the host's package set changes. A
// weekly unattended-upgrades run is the shape being imitated.
const packageUpgradeInterval = 7 * 24 * time.Hour

// packageChanges upgrades a few packages every week and installs one new one
// partway through, so package_events has both actions in it.
func packageChanges(p *Profile, s signal, from, to time.Time) []timedEvent {
	if len(p.Packages) == 0 {
		return nil
	}

	var evs []timedEvent
	for i, at := 0, from.Add(packageUpgradeInterval); at.Before(to); i, at = i+1, at.Add(packageUpgradeInterval) {
		// Upgrade runs land in the small hours, like the timer that drives
		// them.
		at = at.Truncate(24 * time.Hour).Add(6*time.Hour + 17*time.Minute)
		for j := range 3 {
			key := fmt.Sprintf("%s/pkg/%d/%d", p.Hostname, i, j)
			pkg := p.Packages[int(s.unit(key, at)*float64(len(p.Packages)))%len(p.Packages)]
			// Spaced by more than the coarse grid step: three upgrades 40
			// seconds apart all land in one slot, share a timestamp, and any
			// two that picked the same package collapse into one row.
			evs = append(evs, timedEvent{ts: at.Add(time.Duration(j) * 7 * time.Minute), pkg: &netrav1.PackageEvent{
				Name:        pkg.Name,
				Action:      "upgrade",
				FromVersion: pkg.Version,
				ToVersion:   bumpVersion(pkg.Version),
			}})
		}
	}

	// One install, halfway through, so the table is not all upgrades.
	mid := from.Add(to.Sub(from) / 2)
	evs = append(evs, timedEvent{ts: mid, pkg: &netrav1.PackageEvent{
		Name: "ncdu", Action: "install", ToVersion: "1.19-1",
	}})
	return evs
}

// mdraidTrouble degrades and rebuilds the array once in the window. The
// rebuild follows rather than being left open: a permanently degraded array
// is a fixture, not a history.
func mdraidTrouble(p *Profile, s signal, from, to time.Time) []timedEvent {
	if p.Mdraid == "" {
		return nil
	}

	span := to.Sub(from)
	if span < 48*time.Hour {
		return nil
	}
	at := from.Add(time.Duration(s.unit(p.Hostname+"/mdraid", from) * float64(span) * 0.7)).Add(span / 6)
	rebuilt := at.Add(11 * time.Hour)
	if !rebuilt.Before(to) {
		return nil
	}

	return []timedEvent{
		{ts: at, event: &netrav1.Event{
			Type:       "mdraid",
			Subject:    p.Mdraid,
			DetailJson: `{"level":"raid10","state":"degraded","degraded":1,"disks":4,"failed":"sdc"}`,
		}},
		// Well clear of the degrade rather than 90 seconds after it: on the
		// coarse grid both would come due in the same slot, get the same
		// timestamp, and the second would vanish into the unique index on
		// (host_id, ts, type, subject).
		{ts: at.Add(25 * time.Minute), event: &netrav1.Event{
			Type:       "mdraid",
			Subject:    p.Mdraid,
			DetailJson: `{"level":"raid10","state":"recovering","degraded":1,"disks":4,"resync_pct":0.4}`,
		}},
		{ts: rebuilt, event: &netrav1.Event{
			Type:       "mdraid",
			Subject:    p.Mdraid,
			DetailJson: `{"level":"raid10","state":"clean","degraded":0,"disks":4}`,
		}},
	}
}

// bumpVersion increments the trailing revision of a version string. It is
// crude on purpose: the value only has to differ from the one before it and
// look like a Debian version, and a real version comparator here would be a
// second implementation of something nothing reads back.
func bumpVersion(v string) string {
	return v + "+deb12u1"
}

// fingerprint derives a stable synthetic machine-id hash for a hostname. The
// hub warns when a host's fingerprint changes, on the theory that a token was
// copied to another machine -- so a simulator that generated a fresh one per
// run would fill the log with a warning about itself.
func fingerprint(hostname string) string {
	sum := sha256.Sum256([]byte("netra-sim/" + hostname))
	return hex.EncodeToString(sum[:])
}
