package sim

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
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
			DetailJson: `{"state":"clean","level":"raid10","raid_disks":4,"degraded":0,"sync_action":"idle"}`,
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
	var n uint32
	for _, st := range sc.unitStates(ts) {
		if st.state == "failed" {
			n++
		}
	}
	return n
}

// unitState is one unit's systemd state, as the snapshot carries it.
type unitState struct{ state, substate string }

// unitStates replays the schedule up to ts and reports where each unit that
// has ever moved ended up.
//
// Only units with events appear: a unit that never failed has no event, and
// the caller fills it in as healthy from the profile's unit list. Derived from
// the schedule on every call rather than tracked as mutable state, for the
// reason failedUnits was: the grid is not guaranteed to be walked forwards.
func (sc *schedule) unitStates(ts time.Time) map[string]unitState {
	state := map[string]unitState{}
	for _, e := range sc.events {
		if e.ts.After(ts) {
			break
		}
		if e.unit != nil {
			state[e.unit.GetUnitName()] = unitState{
				state:    e.unit.GetState(),
				substate: e.unit.GetSubstate(),
			}
		}
	}
	return state
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

	// The running version of anything already upgraded. Without it a package
	// upgraded twice reports the same from_version both times, and the second
	// event claims to start from a version the first one already replaced.
	current := map[string]string{}

	var evs []timedEvent
	for i, at := 0, from.Add(packageUpgradeInterval); at.Before(to); i, at = i+1, at.Add(packageUpgradeInterval) {
		// Upgrade runs land in the small hours, like the timer that drives
		// them.
		at = at.Truncate(24 * time.Hour).Add(6*time.Hour + 17*time.Minute)
		for j := range 3 {
			key := fmt.Sprintf("%s/pkg/%d/%d", p.Hostname, i, j)
			pkg := p.Packages[int(s.unit(key, at)*float64(len(p.Packages)))%len(p.Packages)]

			from := pkg.Version
			if v, ok := current[pkg.Name]; ok {
				from = v
			}
			to := bumpVersion(from)
			current[pkg.Name] = to

			// Spaced by more than the coarse grid step: three upgrades 40
			// seconds apart all land in one slot, share a timestamp, and any
			// two that picked the same package collapse into one row.
			evs = append(evs, timedEvent{ts: at.Add(time.Duration(j) * 7 * time.Minute), pkg: &netrav1.PackageEvent{
				Name:        pkg.Name,
				Action:      "upgrade",
				FromVersion: from,
				ToVersion:   to,
			}})
		}
	}

	// Installs too, so package_events is not all upgrades. Periodic rather
	// than one at the window's midpoint: the schedule now runs past the
	// backfill into the live horizon, and a midpoint of that span would put
	// the only install somewhere nobody ever sees.
	for i, at := 0, from.Add(packageInstallInterval); at.Before(to); i, at = i+1, at.Add(packageInstallInterval) {
		pkg := installablePackages[i%len(installablePackages)]
		evs = append(evs, timedEvent{ts: at.Truncate(24 * time.Hour).Add(21*time.Hour + 3*time.Minute), pkg: &netrav1.PackageEvent{
			Name: pkg.Name, Action: "install", ToVersion: pkg.Version,
		}})
	}
	return evs
}

// packageInstallInterval is how often somebody installs something new.
const packageInstallInterval = 26 * 24 * time.Hour

// installablePackages are the packages that get installed during a run. They
// are deliberately NOT in packageNames: the point is that they appear in the
// inventory only after their install event, so the two halves agree.
var installablePackages = []PackageSpec{
	{Name: "ncdu", Version: "1.19-1", Size: 61440},
	{Name: "ripgrep", Version: "13.0.0-4", Size: 1892352},
	{Name: "tmux", Version: "3.3a-3", Size: 495616},
	{Name: "ncftp", Version: "3.2.6-1", Size: 372736},
}

// mdraidInterval is how often the array has a bad day. Rare enough to be an
// incident, frequent enough that a 90-day window contains one or two.
const mdraidInterval = 47 * 24 * time.Hour

// mdraidTrouble degrades and rebuilds the array, repeatedly, across the
// window. The rebuild always follows: a permanently degraded array is a
// fixture, not a history.
//
// Repeating rather than placing one incident somewhere in the span, because
// the span now includes the live horizon -- a single incident positioned as a
// fraction of it could fall entirely outside the backfill and never be seen.
func mdraidTrouble(p *Profile, s signal, from, to time.Time) []timedEvent {
	if p.Mdraid == "" {
		return nil
	}

	var evs []timedEvent
	for i, at := 0, from.Add(mdraidInterval/3); at.Before(to); i, at = i+1, at.Add(mdraidInterval) {
		at := at.Add(time.Duration(s.unit(fmt.Sprintf("%s/mdraid/%d", p.Hostname, i), from) * float64(mdraidInterval) * 0.4))
		rebuilt := at.Add(11 * time.Hour)
		if !rebuilt.Before(to) {
			break
		}
		evs = append(evs, mdraidIncident(p.Mdraid, at, rebuilt)...)
	}
	return evs
}

// mdraidIncident is one degrade, resync and recovery.
//
// The detail JSON below, and the baseline in newSchedule, are arrayState from
// agent/collector/mdraid.go marshalled by hand: state, level, raid_disks,
// degraded, sync_action, and nothing else. Keep it identical to the struct --
// both the KEYS and the VALUES, which is the part that is easy to get wrong.
//
// `state` is sysfs array_state, and its vocabulary is about CONSISTENCY, not
// about how many disks are left: clear, inactive, suspended, readonly,
// read-auto, clean, active, write-pending, active-idle. There is no
// "degraded" and no "recovering" in it. The kernel reports a raid10 that has
// lost a member as `clean`, and says so in this repo's own fixture --
// collector/testdata/mdraid/degraded holds array_state=clean, degraded=1,
// sync_action=recover. So the incident below is spelled the way a real array
// spells it: state stays clean throughout, and `degraded` and `sync_action`
// carry the whole story.
//
// Two rounds of drift have been fixed here, neither caught by a test because
// nothing asserts on these strings: first `disks` for `raid_disks` plus
// `failed` and `resync_pct` keys that do not exist, then state values no
// agent could ever send. Both had the same cost -- a simulated array rendered
// differently from a real one, so the dev environment could not be used to
// check the events log at all, which is exactly what it is for.
func mdraidIncident(array string, at, rebuilt time.Time) []timedEvent {
	return []timedEvent{
		{ts: at, event: &netrav1.Event{
			Type:       "mdraid",
			Subject:    array,
			DetailJson: `{"state":"clean","level":"raid10","raid_disks":4,"degraded":1,"sync_action":"idle"}`,
		}},
		// Well clear of the degrade rather than 90 seconds after it: on the
		// coarse grid both would come due in the same slot, get the same
		// timestamp, and the second would vanish into the unique index on
		// (host_id, ts, type, subject).
		{ts: at.Add(25 * time.Minute), event: &netrav1.Event{
			Type:       "mdraid",
			Subject:    array,
			DetailJson: `{"state":"clean","level":"raid10","raid_disks":4,"degraded":1,"sync_action":"recover"}`,
		}},
		{ts: rebuilt, event: &netrav1.Event{
			Type:       "mdraid",
			Subject:    array,
			DetailJson: `{"state":"clean","level":"raid10","raid_disks":4,"degraded":0,"sync_action":"idle"}`,
		}},
	}
}

// bumpVersion increments the trailing revision of a version string. It is
// crude on purpose: the value only has to differ from the one before it and
// look like a Debian version, and a real version comparator here would be a
// second implementation of something nothing reads back.
//
// It increments an existing suffix rather than appending another, so a
// package upgraded three times reads 1.2.3+deb12u3 and not
// 1.2.3+deb12u1+deb12u1+deb12u1.
func bumpVersion(v string) string {
	const suffix = "+deb12u"
	if i := strings.LastIndex(v, suffix); i >= 0 {
		if n, err := strconv.Atoi(v[i+len(suffix):]); err == nil {
			return v[:i] + suffix + strconv.Itoa(n+1)
		}
	}
	return v + suffix + "1"
}

// packageStateAt replays the package events up to ts into the inventory they
// imply: the current version of anything upgraded, and everything installed
// since the window opened.
//
// The two have to agree. store.UpsertHostPackages exists to keep the
// inventory and the event log consistent, so a fixture whose events say
// "installed ncdu 45 days ago" while its inventory has never heard of ncdu
// is a state no real hub can produce -- and it is the state anyone developing
// the inventory UI would be developing against.
func (sc *schedule) packageStateAt(ts time.Time) (versions map[string]string, installed []PackageSpec) {
	versions = map[string]string{}
	seen := map[string]bool{}

	for _, e := range sc.events {
		if e.ts.After(ts) {
			break
		}
		if e.pkg == nil {
			continue
		}
		name := e.pkg.GetName()
		versions[name] = e.pkg.GetToVersion()
		if e.pkg.GetAction() == "install" && !seen[name] {
			seen[name] = true
			installed = append(installed, PackageSpec{
				Name:    name,
				Version: e.pkg.GetToVersion(),
				Size:    sizeOfInstallable(name),
			})
		}
	}
	return versions, installed
}

func sizeOfInstallable(name string) uint64 {
	for _, p := range installablePackages {
		if p.Name == name {
			return p.Size
		}
	}
	return 0
}

// fingerprint derives a stable synthetic machine-id hash for a hostname. The
// hub warns when a host's fingerprint changes, on the theory that a token was
// copied to another machine -- so a simulator that generated a fresh one per
// run would fill the log with a warning about itself.
func fingerprint(hostname string) string {
	sum := sha256.Sum256([]byte("netra-sim/" + hostname))
	return hex.EncodeToString(sum[:])
}
