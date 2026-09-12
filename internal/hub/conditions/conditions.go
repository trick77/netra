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
	"slices"
	"time"
)

// Kinds, as they appear in host_conditions.kind and in the UI's filter URLs.
const (
	KindSilent      = "silent"
	KindSporadic    = "sporadic"
	KindDisk        = "disk"
	KindFailedUnits = "failed-units"
	KindDrive       = "drive"

	// The deviation kinds, judged against a baseline measured from each
	// subject's own history rather than against a constant. See deviation.go.
	KindTemperature = "temperature"
	KindProcesses   = "processes"
	KindLoad        = "load"
)

// DeviationKinds are the kinds judged against a moving average of their own
// history (metric_ewma) rather than against a constant.
//
// Named as a set because three separate places need the same answer: the
// evaluator applies the open delay only to these, the catalogue orders them
// together, and the scan reads baselines only for them. A list is cheaper to
// keep honest than three switch statements that must agree.
var DeviationKinds = map[string]bool{
	KindTemperature: true,
	KindProcesses:   true,
	KindLoad:        true,
}

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
	// Evaluated is the kinds this pass actually looked at.
	//
	// The most important field here, and the least obvious. Absence in Seen
	// means "this subject was not reported"; absence of the whole KIND means
	// "nobody looked", and conflating them is destructive: if the filesystem
	// query fails, every disk subject is missing from Seen, every disk
	// condition reads as vanished, and -- because a vanished subject skips the
	// hysteresis -- they all resolve on that single tick and reopen on the
	// next with opened_ts = now. The onset 0016 says can only be walked once,
	// at open, is then gone for good, destroyed by one failed query.
	//
	// So a kind that is not in here is left entirely alone, exactly as a
	// silent host's conditions are.
	Evaluated map[string]bool
	// Seen is every (host, kind, subject) the pass actually JUDGED, including
	// the ones it judged healthy. Membership here is what entitles Diff to
	// clear a condition.
	Seen map[Key]bool
	// Unjudged is present-but-unmeasurable: the subject still exists, and this
	// pass could not say anything about its state.
	//
	// The third state, and it has to exist. Without it "the reading is too old
	// to trust" collapses into "judged healthy", and a condition clears on a
	// subject nobody actually looked at. The case is routine rather than
	// exotic: the agent's statfs backoff skips a wedged mountpoint for
	// 2^min(failures-1,10) scrapes -- up to about seventeen hours -- while the
	// host keeps posting, so a genuinely full wedged disk would have its
	// critical condition closed as a recovery two ticks in.
	//
	// Treated exactly as a silent host's conditions are: left alone, and
	// accruing no misses.
	Unjudged map[Key]bool
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
	// PrevSeverity is what the row said before this pass.
	//
	// Carried so the store can tell an ESCALATION from a condition merely
	// staying as it is. A disk that opened at 91% and reached 97% is the same
	// condition, and it used to change in place with nothing written -- silent
	// in the log an alerting reader consumes, which is the one place it had to
	// be audible.
	//
	// The miss-counting update below sets this equal to Severity, because a
	// pass that found nothing to describe has not changed the severity either.
	// So no event fires there, which is what keeps a flapping predicate out of
	// the log.
	PrevSeverity string
	// Detail refreshes the stored numbers: a disk that opened at 91% and is
	// now at 97% is the same condition, and the row must not keep printing the
	// number it opened with.
	//
	// NIL MEANS LEAVE IT ALONE, and the distinction matters because the column
	// is NOT NULL. The miss-counting update below carries no detail -- the
	// pass found nothing to describe -- and writing that as an empty object
	// would blank the numbers a still-open condition is displayed with, for
	// the one tick before it either clears or comes back.
	Detail       map[string]any
	MissingTicks int
}

// cloneDetail copies a finding's detail deeply enough that the caller cannot
// change it afterwards.
//
// maps.Clone alone is not enough and the difference is not theoretical: the
// failed-units detail carries a SLICE of unit names, and a shallow copy leaves
// that slice aliased to whatever the observer reuses between passes. One level
// of slice and map values is copied, which covers every shape a detail
// actually has; anything deeper stays shared, and a detail that needs it wants
// a type rather than a deeper clone here.
func cloneDetail(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case []string:
			out[k] = slices.Clone(t)
		case []any:
			out[k] = slices.Clone(t)
		case map[string]any:
			out[k] = maps.Clone(t)
		default:
			out[k] = v
		}
	}
	return out
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
			// The MAP key wins over the copy inside the finding. They are the
			// same thing said twice, and an observer that fills the map but
			// leaves the struct's copy zero would otherwise open a row against
			// host 0 -- a foreign key violation whose cause is nowhere near
			// where it surfaces.
			f.Key = key
			if f.OpenedTS.IsZero() {
				f.OpenedTS = now
			}
			f.Detail = cloneDetail(f.Detail)
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
			PrevSeverity: existing.Severity,
			Detail:       cloneDetail(finding.Detail),
			MissingTicks: 0,
		}})
	}

	// Open, and no longer bad.
	for key, o := range byKey {
		if _, stillBad := scan.Bad[key]; stillBad {
			continue
		}

		// Nobody looked at this kind, so its subjects being absent from Seen
		// says nothing about them. See Scan.Evaluated: without this a failed
		// query resolves every condition of the kind and destroys its onset.
		if !scan.Evaluated[key.Kind] {
			continue
		}

		// The subject is there and could not be judged. Same treatment as a
		// silent host: untouched, and no miss counted -- see Scan.Unjudged.
		if scan.Unjudged[key] {
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
			PrevSeverity: o.Severity,
			MissingTicks: o.MissingTicks + 1,
		}})
	}

	return actions
}
