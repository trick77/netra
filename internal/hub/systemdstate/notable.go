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

// Notable reports whether a unit's state deserves an operator's attention.
//
// The rule is deliberately narrow. A host runs 300-400 loaded services and a
// healthy one is overwhelmingly `active/running` (a daemon) or `inactive/dead`
// (a oneshot that finished, or a service that is simply not enabled). Listing
// those buries the one row that matters, so they are not notable.
//
// The transient states -- `activating`, `deactivating`, and `reloading` -- are
// NOT notable either, and that exclusion is load-bearing rather than an
// oversight. A unit passes through them on every normal start; a snapshot that
// happened to land mid-boot would mint a permanent row for a unit that was
// never unhealthy. The rule has to describe units that are PERSISTENTLY out of
// the ordinary, not units caught mid-transition.
//
// `auto-restart` is a substate rather than a state, and a unit sitting in it
// is looping: systemd has given up on the current attempt and is waiting to
// try again. ActiveState alone reports that as `activating`, so matching on
// state would miss a restart loop entirely -- which is why this takes both.
func Notable(state, substate string) bool {
	return state == "failed" || substate == "auto-restart"
}

// NotableSQL is Notable as a SQL predicate, for the callers that must apply it
// to a set of rows rather than one value. alias is the table alias the columns
// hang off ("u" or the bare table name); pass "" when the columns are
// unqualified.
//
// Kept beside Notable rather than inlined at its two call sites so that the
// Go and SQL forms are read together and cannot quietly diverge.
func NotableSQL(alias string) string {
	if alias != "" {
		alias += "."
	}
	return "(" + alias + "state = 'failed' OR " + alias + "substate = 'auto-restart')"
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
