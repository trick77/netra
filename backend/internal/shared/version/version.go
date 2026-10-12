// Package version compares netra's X.Y.Z release versions.
//
// It exists for one caller -- the hub's minimum-agent check -- but lives in
// shared rather than hub so the agent can consult the same rule without
// importing the hub.
package version

import (
	"strconv"
	"strings"
)

// MinAgent is the oldest agent release the hub accepts ingest from.
//
// 0.0.205 is the first release whose agent states an event's severity in the
// wire field (#218). The hub no longer falls back to detail_json's `severity`
// key, so an agent below this would have every notable event stored as "info"
// -- silently, and exactly for the rows that matter. It is also above the
// releases that added the sensor `kind` (0.0.39) and moved the mdraid
// array_state normalization into the collector (0.0.199), which the hub has
// likewise stopped compensating for.
//
// Raising this is a deliberate act: it silences every host below it until an
// operator pulls a newer image.
const MinAgent = "0.0.205"

// AtLeast reports whether reported is at or above min.
//
// A version that does not parse is ACCEPTED, and that is deliberate rather
// than lax. buildinfo.Version() is "dev" for every local and CI build and the
// simulator reports "sim" (devtools/sim/run.go), so rejecting the unparseable
// would lock development and netra-sim out of their own hub. A parser that
// went wrong would also brick a whole fleet, and failing open is the cheaper
// mistake: the versions this gate exists to catch are real, stamped releases.
//
// Any suffix after the patch number is ignored, so 0.0.205-rc1 counts as
// 0.0.205 and passes a 0.0.205 minimum. netra tags bare X.Y.Z
// (.github/workflows/release.yaml), so this is a courtesy rather than a
// prerelease ordering anyone relies on.
func AtLeast(reported, min string) bool {
	got, ok := parse(reported)
	if !ok {
		return true
	}
	want, ok := parse(min)
	if !ok {
		return true
	}
	for i := range got {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	return true
}

// parse reads a leading "v", then major.minor.patch as decimal integers,
// discarding anything after the patch number.
//
// The three parts are compared as INTEGERS, never as text. Releases are past
// 0.0.200, and lexically "0.0.216" sorts below "0.0.99" -- a string compare
// here would reject current agents and accept old ones.
func parse(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return out, false
	}
	// Cut the suffix before splitting so "0.0.205-rc1" yields a numeric patch.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
