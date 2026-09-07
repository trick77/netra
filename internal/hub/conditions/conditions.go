// Package conditions decides what is currently wrong with each host.
//
// The rules lived in the browser: DISK_WARN_PCT and its free-bytes floor in
// ui/src/features/fleet/conditions.ts, STALE_THRESHOLD_MS and the sporadic
// ratio in lib/host.ts, driveAlarms in features/host/smart.ts. That was
// survivable while a person reading a page was the only consumer of them, and
// stops being survivable the moment anything else has to ask what is wrong --
// an alerting engine cannot call into a browser.
//
// It also meant nothing recorded when a condition BEGAN. Four of the eight
// kinds left their onset column empty because a counter delta cannot say, so a
// disk that filled at 03:00 and drained by 09:00 left no trace anywhere in
// netra. Here a condition is opened by an event and closed by an event, and
// the row carries the onset.
package conditions

import (
	"maps"
	"time"
)

// Kinds, as they appear in host_conditions.kind and in the UI's filter URLs.
const (
	KindSilent      = "silent"
	KindSporadic    = "sporadic"
	KindDisk        = "disk"
	KindFailedUnits = "failed-units"
	KindDrive       = "drive"
)

// Severities. `ok` and `neutral` are UI vocabulary for the absence of a
// condition and never reach a row: a condition is definitionally something
// wrong.
const (
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Why a condition stopped, which are two different facts.
const (
	// ReasonCleared is the predicate going false: the disk drained, the unit
	// came back.
	ReasonCleared = "cleared"
	// ReasonVanished is the SUBJECT going away while the host kept talking: a
	// mount unmounted, a drive pulled, a unit purged.
	//
	// Without this, such a condition stays open forever -- nothing evaluates
	// it again, so its predicate is never false, merely absent -- or it is
	// recorded as a recovery that never happened. A fleet that goes green
	// because nobody is looking at it is what this exists to prevent.
	ReasonVanished = "vanished"
)

// clearAfter is how many consecutive evaluations must find a condition's
// predicate false before it closes.
//
// Asymmetric on purpose: a condition opens on the FIRST observation and closes
// only after the second consecutive miss. You want to know quickly, and you do
// not need a fast all-clear -- a disk oscillating either side of 90% would
// otherwise write an opened/cleared pair per tick, filling the log with
// transitions that describe nothing.
const clearAfter = 2

// Key identifies one condition: a kind, about one subject, on one host.
//
// Subject is finer than the UI displays. The fleet page collapses a host's
// drives into a single row, and that is a RENDERING decision -- if the state
// machine collapsed too, the condition would open and close every time the
// fullest mount changed from /var to /mnt.
type Key struct {
	HostID  int32
	Kind    string
	Subject string
}

// Open is a condition currently recorded as open, as read back from the store.
type Open struct {
	ID           int64
	Key          Key
	Severity     string
	MissingTicks int
}

// Finding is one subject the evaluator judged to be in a bad state.
type Finding struct {
	Key      Key
	Severity string
	// Detail is the condition's own numbers, for the sentence the UI writes.
	// Shaped by the kind, like an event's detail.
	Detail map[string]any
	// OpenedTS is when this started, where that is honestly known: a silent
	// host has last_seen, a failed unit has systemd's own state_ts, a
	// filesystem is walked back through its series. Zero means "use now".
	OpenedTS time.Time
	// OpenedAtLeast marks an onset that is a FLOOR rather than a moment,
	// because the walk hit the end of what is retained. The UI says "over 7 d"
	// rather than naming a bucket where nothing happened.
	OpenedAtLeast bool
}

// Scan is one evaluation pass: everything looked at, and everything found
// wrong.
//
// Seen is the load-bearing half and the one an observer is likeliest to
// forget. A condition whose subject is absent from Seen did not RECOVER, it
// stopped being reported -- and telling those apart is the whole of
// ReasonVanished.
type Scan struct {
	// Seen is every (host, kind, subject) the pass actually evaluated,
	// including the healthy ones.
	Seen map[Key]bool
	// Bad is the subset judged to be in a bad state.
	Bad map[Key]Finding
	// Reporting is the hosts whose data is current enough to conclude anything
	// from. A subject missing on a host that is NOT reporting is stale data,
	// not a fixed problem: netra's existing position is that a 96% disk on a
	// machine that is off is still a 96% disk, and the row only stops claiming
	// to describe this minute.
	Reporting map[int32]bool
}

// Action is one change the evaluator wants made, in the order it was decided.
type Action struct {
	// Open is set when a condition should be opened.
	Open *Finding
	// Resolve is set when one should be closed.
	Resolve *Resolution
	// Update is set when an open condition stays open with a new severity, or
	// with its miss counter changed.
	Update *Update
}

// Resolution closes one open condition.
type Resolution struct {
	ID     int64
	Key    Key
	Reason string
}

// Update carries an open condition forward.
type Update struct {
	ID       int64
	Key      Key
	Severity string
	// Detail is refreshed on every pass: a disk that opened at 91% and is now
	// at 97% is the same condition, and the row must not keep printing the
	// number it opened with.
	Detail       map[string]any
	MissingTicks int
}

// Diff decides what changes a scan implies, given what is currently open.
//
// Pure, and that is deliberate: every rule about opening, clearing, hysteresis
// and vanished subjects is decided here against plain values, so the whole
// state machine is testable without a database.
func Diff(open []Open, scan Scan, now time.Time) []Action {
	byKey := make(map[Key]Open, len(open))
	for _, o := range open {
		byKey[o.Key] = o
	}

	var actions []Action

	// Newly bad, or still bad.
	for key, finding := range scan.Bad {
		existing, isOpen := byKey[key]
		if !isOpen {
			f := finding
			if f.OpenedTS.IsZero() {
				f.OpenedTS = now
			}
			f.Detail = maps.Clone(f.Detail)
			actions = append(actions, Action{Open: &f})
			continue
		}
		// Still bad. The miss counter resets even when nothing else changed:
		// two non-consecutive misses must not close a condition that was true
		// in between.
		actions = append(actions, Action{Update: &Update{
			ID:           existing.ID,
			Key:          key,
			Severity:     finding.Severity,
			Detail:       maps.Clone(finding.Detail),
			MissingTicks: 0,
		}})
	}

	// Open, and no longer bad.
	for key, o := range byKey {
		if _, stillBad := scan.Bad[key]; stillBad {
			continue
		}

		seen := scan.Seen[key]
		if !seen && !scan.Reporting[key.HostID] {
			// The host stopped talking, so this subject's absence says nothing
			// about the subject. Left exactly as it is -- not resolved, and
			// not counted as a miss either, or a long outage would silently
			// clear every condition on the host it made unobservable.
			continue
		}

		reason := ReasonCleared
		if !seen {
			// The host is reporting and no longer names this subject: the
			// mount was unmounted, the drive pulled, the unit purged.
			reason = ReasonVanished
		}

		// A vanished subject resolves at once. There is nothing to wait for --
		// the hysteresis below exists to stop a predicate flapping across its
		// threshold, and a subject that is gone is not flapping.
		if reason == ReasonVanished {
			actions = append(actions, Action{Resolve: &Resolution{
				ID: o.ID, Key: key, Reason: reason,
			}})
			continue
		}

		if o.MissingTicks+1 >= clearAfter {
			actions = append(actions, Action{Resolve: &Resolution{
				ID: o.ID, Key: key, Reason: reason,
			}})
			continue
		}
		actions = append(actions, Action{Update: &Update{
			ID:           o.ID,
			Key:          key,
			Severity:     o.Severity,
			MissingTicks: o.MissingTicks + 1,
		}})
	}

	return actions
}
