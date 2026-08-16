// Package systemdstate holds the one definition of which systemd unit states
// are worth showing an operator.
//
// It exists as its own package because the definition is needed in two places
// that have no business importing each other: package store decides which
// units earn a row, and package read decides which rows the units endpoint
// returns. A copy in each would be two copies of a product rule that has to
// agree, and nothing would catch them drifting apart.
//
// There is a THIRD copy, in TypeScript, at ui/src/features/host/tabs/
// Overview.tsx (see notableUnit there). It cannot import this one, and no
// compiler or linter in this repo can see across that boundary -- so a change
// here is only half a change. The comment on the other side says the same.
package systemdstate

import "time"

// A unit needs an operator's attention when it is FAILED, or when it is
// RESTARTING REPEATEDLY. Those are the two halves of one rule, and they are
// answered differently:
//
//   - failed is a property of the unit's current state, which is what Notable
//     below tests.
//   - restarting repeatedly is a RATE, so no amount of looking at the current
//     state can answer it. It is counted from the event log, against
//     FlapThreshold, in read.Units.
//
// Both halves must be applied together everywhere, or the host page's warning
// band and its Units tab disagree about whether anything is wrong -- and a
// panel that lists a unit the band calls fine is worse than either answer on
// its own.

// Notable reports whether a unit's state, on its own, deserves attention.
//
// Deliberately narrow: a host runs 300-400 loaded services, and a healthy one
// is overwhelmingly `active/running` (a daemon) or `inactive/dead` (a oneshot
// that finished, or a service simply not enabled). Listing those buries the
// one row that matters.
//
// The transient states -- `activating`, `deactivating`, `reloading` -- are
// excluded, and so is the `auto-restart` substate, which is less obvious. A
// unit in auto-restart is inside systemd's backoff between two start attempts,
// which sounds like exactly what we want, but it is a single sighting rather
// than a rate: at the default RestartSec=100ms a 60-second scrape essentially
// never lands in that window, so catching one is closer to a coin toss than to
// evidence. A unit looping fast enough to be caught reliably trips
// StartLimitBurst and becomes `failed`, which this already matches; a unit
// looping slowly enough to miss the start limit is caught by the transition
// count. Listing on the sighting would put a red badge on a service that is
// most likely fine, on the one panel that exists to show only what is not.
func Notable(state, _ string) bool {
	return state == "failed"
}

// NotableSQL is Notable as a SQL predicate, for the callers that must apply it
// to a set of rows rather than one value. alias is the table alias the columns
// hang off ("u" or the bare table name); pass "" when the columns are
// unqualified.
//
// Kept beside Notable rather than inlined at its call sites so the Go and SQL
// forms are read together and cannot quietly diverge.
func NotableSQL(alias string) string {
	if alias != "" {
		alias += "."
	}
	return "(" + alias + "state = 'failed')"
}

// FlapWindow and FlapThreshold define "restarting repeatedly": at least this
// many recorded state changes inside this window.
//
// Counting transitions is the only honest way to measure repetition, and the
// obvious alternatives do not work:
//
//   - `substate = 'auto-restart'` alone is a single sighting, not a rate. It
//     is also the gap BETWEEN restart attempts, which at the default
//     RestartSec=100ms a 60-second scrape will essentially never land in.
//   - "how long has it been in auto-restart" is worse than useless: state_ts
//     advances on every state CHANGE, and a flapping unit is changing state
//     constantly, so its age in the current state is near zero exactly when it
//     is flapping hardest. A debounce built on it can never fire.
//
// A healthy unit transitions zero times in an hour; even a oneshot on a timer
// manages two or three. Four is comfortably clear of both, and well under what
// a service dying and restarting every few minutes produces.
//
// This does NOT cover a unit crash-looping in milliseconds -- systemd's
// StartLimitBurst escalates that to `failed` within seconds, and Notable
// already catches it. What it covers is the slow loop that never trips the
// start limit: a service that runs for a few minutes, dies, and comes back,
// which today shows up as a healthy unit at every single scrape.
//
// The UI has its own copy of the threshold, in needsAttention in
// ui/src/features/host/tabs/Overview.tsx. Change both.
const (
	FlapWindow    = time.Hour
	FlapThreshold = 4
)
