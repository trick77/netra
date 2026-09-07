package conditions_test

import (
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
)

var now = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

func key(host int32, kind, subject string) conditions.Key {
	return conditions.Key{HostID: host, Kind: kind, Subject: subject}
}

// scan builds a Scan where every listed subject was seen and the host is
// reporting, which is the ordinary case.
func scan(seen []conditions.Key, bad map[conditions.Key]conditions.Finding) conditions.Scan {
	s := conditions.Scan{
		Evaluated: map[string]bool{},
		Seen:      map[conditions.Key]bool{},
		Bad:       bad,
		Reporting: map[int32]bool{},
	}
	for _, k := range seen {
		s.Seen[k] = true
		s.Reporting[k.HostID] = true
		s.Evaluated[k.Kind] = true
	}
	for k := range bad {
		s.Seen[k] = true
		s.Reporting[k.HostID] = true
		s.Evaluated[k.Kind] = true
	}
	return s
}

func only(t *testing.T, actions []conditions.Action) conditions.Action {
	t.Helper()
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want exactly 1: %+v", len(actions), actions)
	}
	return actions[0]
}

func TestOpensOnTheFirstObservation(t *testing.T) {
	k := key(1, conditions.KindDisk, "/var")
	got := only(t, conditions.Diff(nil, scan(nil, map[conditions.Key]conditions.Finding{
		k: {Key: k, Severity: conditions.SeverityWarning},
	}), now))

	if got.Open == nil {
		t.Fatalf("want an open action, got %+v", got)
	}
	if got.Open.Key != k {
		t.Errorf("key = %+v, want %+v", got.Open.Key, k)
	}
	// A finding that states no onset is dated now, rather than left zero and
	// written as a NOT NULL violation.
	if !got.Open.OpenedTS.Equal(now) {
		t.Errorf("opened_ts = %v, want %v", got.Open.OpenedTS, now)
	}
}

// A finding that knows its own onset keeps it: a silent host has last_seen, a
// failed unit has systemd's state_ts. Those are the moment, not an estimate.
func TestKeepsAnOnsetTheFindingKnows(t *testing.T) {
	k := key(1, conditions.KindFailedUnits, "exim4.service")
	onset := now.Add(-6 * time.Hour)

	got := only(t, conditions.Diff(nil, scan(nil, map[conditions.Key]conditions.Finding{
		k: {Key: k, Severity: conditions.SeverityWarning, OpenedTS: onset},
	}), now))

	if !got.Open.OpenedTS.Equal(onset) {
		t.Errorf("opened_ts = %v, want the finding's own %v", got.Open.OpenedTS, onset)
	}
}

// Still bad is not a transition. It refreshes the row -- a disk that opened at
// 91% and is now at 97% is the same condition, and the row must not keep
// printing the number it opened with -- and writes no event.
func TestAStillBadConditionUpdatesRatherThanReopens(t *testing.T) {
	k := key(1, conditions.KindDisk, "/var")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning}}

	got := only(t, conditions.Diff(open, scan(nil, map[conditions.Key]conditions.Finding{
		k: {
			Key:      k,
			Severity: conditions.SeverityCritical,
			Detail:   map[string]any{"pct": 97.0},
		},
	}), now))

	if got.Update == nil {
		t.Fatalf("want an update, got %+v", got)
	}
	if got.Update.ID != 7 {
		t.Errorf("id = %d, want 7", got.Update.ID)
	}
	if got.Update.Severity != conditions.SeverityCritical {
		t.Errorf("severity = %q, want the new one", got.Update.Severity)
	}
	if got.Update.Detail["pct"] != 97.0 {
		t.Errorf("detail = %+v, want the current numbers", got.Update.Detail)
	}
}

// The hysteresis, and the reason it exists: a disk oscillating either side of
// 90% must not write an opened/cleared pair per tick.
func TestOneMissDoesNotClear(t *testing.T) {
	k := key(1, conditions.KindDisk, "/var")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning}}

	got := only(t, conditions.Diff(open, scan([]conditions.Key{k}, nil), now))

	if got.Resolve != nil {
		t.Fatalf("a single miss closed the condition: %+v", got.Resolve)
	}
	if got.Update == nil || got.Update.MissingTicks != 1 {
		t.Fatalf("want the miss counted, got %+v", got.Update)
	}
}

func TestTwoConsecutiveMissesClear(t *testing.T) {
	k := key(1, conditions.KindDisk, "/var")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning, MissingTicks: 1}}

	got := only(t, conditions.Diff(open, scan([]conditions.Key{k}, nil), now))

	if got.Resolve == nil {
		t.Fatalf("want a resolve, got %+v", got)
	}
	if got.Resolve.Reason != conditions.ReasonCleared {
		t.Errorf("reason = %q, want cleared", got.Resolve.Reason)
	}
}

// Two misses that are not consecutive are not two misses. Without the reset a
// disk hovering at the threshold would close on its second dip whenever that
// happened, however long it had been over in between.
func TestAMissCounterResetsWhenTheConditionReturns(t *testing.T) {
	k := key(1, conditions.KindDisk, "/var")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning, MissingTicks: 1}}

	got := only(t, conditions.Diff(open, scan(nil, map[conditions.Key]conditions.Finding{
		k: {Key: k, Severity: conditions.SeverityWarning},
	}), now))

	if got.Update == nil || got.Update.MissingTicks != 0 {
		t.Fatalf("want the counter reset, got %+v", got.Update)
	}
}

// The subject went away while the host kept talking: a mount unmounted, a
// drive pulled, a unit purged. It did not recover, and recording it as a
// recovery is how a fleet goes green because nobody is looking at it.
func TestAVanishedSubjectResolvesAsVanished(t *testing.T) {
	k := key(1, conditions.KindDisk, "/mnt/backup")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning}}

	// The host is reporting, and this mount is not among what it reported.
	s := conditions.Scan{
		Evaluated: map[string]bool{conditions.KindDisk: true},
		Seen:      map[conditions.Key]bool{key(1, conditions.KindDisk, "/"): true},
		Bad:       nil,
		Reporting: map[int32]bool{1: true},
	}

	got := only(t, conditions.Diff(open, s, now))

	if got.Resolve == nil {
		t.Fatalf("want a resolve, got %+v", got)
	}
	if got.Resolve.Reason != conditions.ReasonVanished {
		t.Errorf("reason = %q, want vanished", got.Resolve.Reason)
	}
}

// No hysteresis for a vanished subject. The wait exists to stop a predicate
// flapping across a threshold, and a mount that is gone is not flapping.
func TestAVanishedSubjectDoesNotWaitOutTheHysteresis(t *testing.T) {
	k := key(1, conditions.KindDisk, "/mnt/backup")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning, MissingTicks: 0}}

	s := conditions.Scan{
		Evaluated: map[string]bool{conditions.KindDisk: true},
		Seen:      map[conditions.Key]bool{},
		Bad:       nil,
		Reporting: map[int32]bool{1: true},
	}

	got := only(t, conditions.Diff(open, s, now))
	if got.Resolve == nil {
		t.Fatalf("a vanished subject was made to wait: %+v", got)
	}
}

// The case that would otherwise clear the fleet during an outage.
//
// A host that stopped reporting names no mounts, so every condition on it
// looks absent. Resolving those would record a recovery for a machine nobody
// can see -- and netra's existing position is the opposite: a 96% disk on a
// machine that is off is still a 96% disk.
func TestASilentHostsConditionsAreLeftAlone(t *testing.T) {
	k := key(1, conditions.KindDisk, "/var")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning}}

	s := conditions.Scan{
		Evaluated: map[string]bool{conditions.KindDisk: true},
		Seen:      map[conditions.Key]bool{},
		Bad:       nil,
		Reporting: map[int32]bool{1: false},
	}

	if actions := conditions.Diff(open, s, now); len(actions) != 0 {
		t.Fatalf("a silent host's conditions were touched: %+v", actions)
	}
}

// A miss must not be counted either, or a long enough outage clears every
// condition on the host it made unobservable -- the same bug one step slower.
func TestASilentHostAccruesNoMisses(t *testing.T) {
	k := key(1, conditions.KindDisk, "/var")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning, MissingTicks: 1}}

	s := conditions.Scan{
		Evaluated: map[string]bool{conditions.KindDisk: true},
		Seen:      map[conditions.Key]bool{},
		Bad:       nil,
		Reporting: map[int32]bool{1: false},
	}

	if actions := conditions.Diff(open, s, now); len(actions) != 0 {
		t.Fatalf("a silent host accrued a miss: %+v", actions)
	}
}

// Subjects are independent: one mount clearing says nothing about another.
func TestSubjectsAreTrackedSeparately(t *testing.T) {
	varLog := key(1, conditions.KindDisk, "/var/log")
	root := key(1, conditions.KindDisk, "/")
	open := []conditions.Open{
		{ID: 1, Key: varLog, Severity: conditions.SeverityWarning, MissingTicks: 1},
		{ID: 2, Key: root, Severity: conditions.SeverityCritical},
	}

	actions := conditions.Diff(open, scan([]conditions.Key{varLog, root},
		map[conditions.Key]conditions.Finding{
			root: {Key: root, Severity: conditions.SeverityCritical},
		}), now)

	var resolved, updated int
	for _, a := range actions {
		switch {
		case a.Resolve != nil:
			resolved++
			if a.Resolve.Key != varLog {
				t.Errorf("resolved the wrong subject: %+v", a.Resolve.Key)
			}
		case a.Update != nil:
			updated++
		}
	}
	if resolved != 1 || updated != 1 {
		t.Fatalf("got %d resolved and %d updated, want 1 and 1: %+v", resolved, updated, actions)
	}
}

// Detail is copied out of the finding rather than aliased. The observer reuses
// its maps across passes, and a row holding a reference to one would change
// underneath the caller between the diff and the write.
func TestDetailIsCopiedNotAliased(t *testing.T) {
	k := key(1, conditions.KindFailedUnits, "")
	units := []string{"exim4.service"}
	detail := map[string]any{"pct": 91.0, "units": units}

	got := only(t, conditions.Diff(nil, scan(nil, map[conditions.Key]conditions.Finding{
		k: {Key: k, Severity: conditions.SeverityWarning, Detail: detail},
	}), now))

	detail["pct"] = 42.0
	if got.Open.Detail["pct"] != 91.0 {
		t.Errorf("detail aliased the observer's map: %+v", got.Open.Detail)
	}

	// The half a shallow copy misses, and the one that actually occurs: the
	// failed-units detail carries a slice, which maps.Clone leaves pointing at
	// whatever the observer reuses between passes.
	units[0] = "nginx.service"
	if got.Open.Detail["units"].([]string)[0] != "exim4.service" {
		t.Errorf("detail's slice aliased the observer's: %+v", got.Open.Detail)
	}
}

// Nothing wrong and nothing open is no work at all, which is the state a
// healthy fleet spends all its time in.
func TestAHealthyFleetProducesNoActions(t *testing.T) {
	k := key(1, conditions.KindDisk, "/")
	if actions := conditions.Diff(nil, scan([]conditions.Key{k}, nil), now); len(actions) != 0 {
		t.Fatalf("a healthy fleet produced work: %+v", actions)
	}
}

// The failure that would quietly destroy every onset netra holds.
//
// A kind whose observer errored contributes nothing to Seen, so every subject
// of that kind looks absent. Treated as vanished -- which skips the hysteresis
// -- they would all resolve on that one tick and reopen on the next with
// opened_ts = now. The onset 0016 says can only be walked once, at open, would
// be gone, and one failed query would have rewritten the fleet's history.
func TestAKindNobodyEvaluatedIsLeftAlone(t *testing.T) {
	k := key(1, conditions.KindDisk, "/var")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning}}

	// The host is reporting and the pass looked at units, but the filesystem
	// query failed, so `disk` is absent from Evaluated entirely.
	s := conditions.Scan{
		Evaluated: map[string]bool{conditions.KindFailedUnits: true},
		Seen:      map[conditions.Key]bool{},
		Bad:       nil,
		Reporting: map[int32]bool{1: true},
	}

	if actions := conditions.Diff(open, s, now); len(actions) != 0 {
		t.Fatalf("an unevaluated kind was resolved: %+v", actions)
	}
}

// The same guard must not stop a kind that WAS evaluated from resolving, or it
// is a mute button rather than a safety catch.
func TestAnEvaluatedKindStillResolves(t *testing.T) {
	k := key(1, conditions.KindDisk, "/var")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning}}

	s := conditions.Scan{
		Evaluated: map[string]bool{conditions.KindDisk: true},
		Seen:      map[conditions.Key]bool{},
		Bad:       nil,
		Reporting: map[int32]bool{1: true},
	}

	got := only(t, conditions.Diff(open, s, now))
	if got.Resolve == nil || got.Resolve.Reason != conditions.ReasonVanished {
		t.Fatalf("want a vanished resolve, got %+v", got)
	}
}

// A miss-counting update carries no detail, and that must read as "leave the
// stored numbers alone" rather than as an empty object. The column is NOT
// NULL, and blanking it would strip the figures a still-open condition is
// displayed with for the tick before it clears or comes back.
func TestAMissCarriesNoDetailToWrite(t *testing.T) {
	k := key(1, conditions.KindDisk, "/var")
	open := []conditions.Open{{ID: 7, Key: k, Severity: conditions.SeverityWarning}}

	got := only(t, conditions.Diff(open, scan([]conditions.Key{k}, nil), now))
	if got.Update == nil {
		t.Fatalf("want an update, got %+v", got)
	}
	if got.Update.Detail != nil {
		t.Errorf("a miss proposed writing detail %+v; nil means leave it", got.Update.Detail)
	}
}

// The map key is authoritative, so an observer that fills the map but leaves
// the finding's own copy zero still opens against the right host rather than
// against host 0 -- a foreign key violation nowhere near where it surfaces.
func TestTheMapKeyWinsOverTheFindingsOwnCopy(t *testing.T) {
	k := key(42, conditions.KindDisk, "/var")
	got := only(t, conditions.Diff(nil, scan(nil, map[conditions.Key]conditions.Finding{
		k: {Severity: conditions.SeverityWarning},
	}), now))

	if got.Open.Key != k {
		t.Errorf("key = %+v, want %+v", got.Open.Key, k)
	}
}
