package version_test

import (
	"testing"

	"github.com/trick77/netra/internal/shared/buildinfo"
	"github.com/trick77/netra/internal/shared/version"
)

// The constant must PARSE, and this is the test that matters most here.
//
// AtLeast fails open on anything it cannot read, so a typo in MinAgent -- a
// stray space, a fourth part, a "v0.0.205-" -- would not fail loudly. It would
// silently accept every agent in the fleet, and the gate would look like it
// was working right up until an ancient agent quietly wrote misclassified
// rows.
func TestMinAgentIsAParsableVersion(t *testing.T) {
	// Given a version one patch below the minimum...
	if version.AtLeast("0.0.0", version.MinAgent) {
		t.Fatalf("AtLeast(0.0.0, %q) = true: MinAgent does not parse, so the gate accepts everything",
			version.MinAgent)
	}
	// ...and the minimum itself, which is the boundary an operator reads as
	// "this release is supported".
	if !version.AtLeast(version.MinAgent, version.MinAgent) {
		t.Errorf("AtLeast(%[1]q, %[1]q) = false: the minimum must be its own floor", version.MinAgent)
	}
}

// An unstamped build reports "dev" (buildinfo), which must reach the hub.
// Otherwise nobody can run an agent they just built against a hub they just
// built, which is the whole local loop.
func TestAnUnstampedBuildIsNotLockedOut(t *testing.T) {
	if !version.AtLeast(buildinfo.Version(), version.MinAgent) {
		t.Errorf("AtLeast(%q, %q) = false: a dev build must not be refused",
			buildinfo.Version(), version.MinAgent)
	}
}

func TestAtLeast(t *testing.T) {
	tests := []struct {
		name     string
		reported string
		min      string
		want     bool
	}{
		{"equal", "0.0.205", "0.0.205", true},
		{"one patch above", "0.0.206", "0.0.205", true},
		{"one patch below", "0.0.204", "0.0.205", false},

		// The reason this compares integers rather than strings: netra is past
		// 0.0.200, and lexically "0.0.216" sorts BELOW "0.0.99". A string
		// compare would refuse current agents and admit old ones -- backwards
		// in both directions at once.
		{"three digits beats two", "0.0.216", "0.0.99", true},
		{"two digits loses to three", "0.0.99", "0.0.216", false},

		{"minor outranks patch", "0.1.0", "0.0.205", true},
		{"lower minor loses whatever the patch", "0.0.999", "0.1.0", false},
		{"major outranks everything", "1.0.0", "0.9.9", true},

		{"a v prefix is tolerated", "v0.0.206", "0.0.205", true},
		{"surrounding space is tolerated", " 0.0.206 ", "0.0.205", true},

		// A prerelease counts as its release. netra tags bare X.Y.Z, so this
		// is a courtesy rather than an ordering anything depends on.
		{"a prerelease counts as the release", "0.0.205-rc1", "0.0.205", true},
		{"build metadata is ignored", "0.0.205+f00", "0.0.205", true},

		// Everything unreadable is ACCEPTED. "sim" is what netra-sim reports
		// (devtools/sim/run.go) and "dev" is any local build; refusing either
		// would lock the simulator and development out of their own hub.
		{"sim", "sim", "0.0.205", true},
		{"dev", "dev", "0.0.205", true},
		{"empty", "", "0.0.205", true},
		{"too few parts", "0.0", "0.0.205", true},
		{"too many parts", "0.0.205.1", "0.0.205", true},
		{"not numbers", "a.b.c", "0.0.205", true},
		{"negative", "0.0.-1", "0.0.205", true},

		// An unreadable MINIMUM also fails open, which is what makes the
		// MinAgent parse test above the one that guards this package.
		{"an unparsable minimum accepts anything", "0.0.1", "nonsense", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := version.AtLeast(tc.reported, tc.min); got != tc.want {
				t.Errorf("AtLeast(%q, %q) = %v, want %v", tc.reported, tc.min, got, tc.want)
			}
		})
	}
}
