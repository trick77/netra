package read

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A fixed clock. Tier selection is a pure function of (family, from, to, step,
// now), which is the whole reason these boundaries are ordinary unit tests
// rather than something needing data at a particular age to exist.
var testNow = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

func mustPlan(t *testing.T, fam string, from, to time.Time, step time.Duration, stepSet bool) Plan {
	t.Helper()
	f, err := lookupFamily(fam)
	if err != nil {
		t.Fatalf("lookupFamily(%q): %v", fam, err)
	}
	p, err := planQuery(f, Window{From: from, To: to}, step, stepSet, testNow)
	if err != nil {
		t.Fatalf("planQuery: %v", err)
	}
	return p
}

// The boundary the plan document calls load-bearing: a range that straddles
// the raw tier's retention must fall to 5m on the FROM edge, not on its
// midpoint and not on its span.
func TestTierSelectionAtTheRawRetentionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		age  time.Duration
		want string
	}{
		{"a minute inside raw retention", 7*day - time.Minute, TierRaw},
		{"exactly at raw retention", 7 * day, TierRaw},
		{"a second past raw retention", 7*day + time.Second, Tier5m},
		{"a minute past raw retention", 7*day + time.Minute, Tier5m},
		{"exactly at 5m retention", 30 * day, Tier5m},
		{"a second past 5m retention", 30*day + time.Second, Tier1h},
		{"exactly at 1h retention", 90 * day, Tier1h},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mustPlan(t, "host", testNow.Add(-tc.age), testNow, 0, false)
			if p.Tier != tc.want {
				t.Errorf("tier = %q, want %q", p.Tier, tc.want)
			}
		})
	}
}

// A from older than every tier's retention is not an error. The hub returns
// the part that still exists at the coarsest tier and says what it dropped --
// erroring would make "show me everything you have" unanswerable.
func TestTierSelectionOlderThanEveryRetention(t *testing.T) {
	p := mustPlan(t, "host", testNow.Add(-100*day), testNow, 0, false)

	if p.Tier != Tier1h {
		t.Errorf("tier = %q, want %q", p.Tier, Tier1h)
	}
	if want := testNow.Add(-90 * day); !p.Window.From.Equal(want) {
		t.Errorf("window.from = %v, want %v", p.Window.From, want)
	}
	if want := testNow.Add(-100 * day); !p.Requested.From.Equal(want) {
		t.Errorf("requested.from = %v, want %v", p.Requested.From, want)
	}
	if !hasWarning(p, "predates") {
		t.Errorf("warnings = %q, want one naming the retention clamp", p.Warnings)
	}
}

// The trailing edge. Every continuous aggregate in 0001_init.sql is
// materialized_only, so a query past the refresh policy's end_offset returns
// nothing rather than falling through to the raw rows -- and a client that
// was handed to = now would read that emptiness as a host that stopped
// reporting.
func TestTierSelectionClampsToTheMaterialisationHorizon(t *testing.T) {
	for _, tc := range []struct {
		name string
		age  time.Duration
		lag  time.Duration
	}{
		{"5m tier lags ten minutes", 10 * day, 10 * time.Minute},
		{"1h tier lags an hour", 60 * day, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mustPlan(t, "host", testNow.Add(-tc.age), testNow, 0, false)

			if want := testNow.Add(-tc.lag); !p.Window.To.Equal(want) {
				t.Errorf("window.to = %v, want %v", p.Window.To, want)
			}
			if !p.Requested.To.Equal(testNow) {
				t.Errorf("requested.to = %v, want %v", p.Requested.To, testNow)
			}
			if !hasWarning(p, "materialises") {
				t.Errorf("warnings = %q, want one naming the lag", p.Warnings)
			}
		})
	}
}

// Raw has no materialisation step, so nothing is clamped off its trailing
// edge. If this ever starts clamping, every live view silently loses its most
// recent minutes.
func TestTierSelectionRawIsFreshToNow(t *testing.T) {
	p := mustPlan(t, "host", testNow.Add(-time.Hour), testNow, 0, false)

	if p.Tier != TierRaw {
		t.Fatalf("tier = %q, want %q", p.Tier, TierRaw)
	}
	if !p.Window.To.Equal(testNow) {
		t.Errorf("window.to = %v, want %v", p.Window.To, testNow)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("warnings = %q, want none", p.Warnings)
	}
}

// The two raw-only families are why selection is per-family rather than a
// lookup on the range: a sixty-day SMART range has no 5m tier to fall to, and
// process can never cover more than its 48 hours however long a range is
// asked for.
func TestTierSelectionForRawOnlyFamilies(t *testing.T) {
	t.Run("smart over sixty days stays raw and is not clamped", func(t *testing.T) {
		p := mustPlan(t, "smart", testNow.Add(-60*day), testNow, 0, false)

		if p.Tier != TierRaw {
			t.Errorf("tier = %q, want %q", p.Tier, TierRaw)
		}
		if want := testNow.Add(-60 * day); !p.Window.From.Equal(want) {
			t.Errorf("window.from = %v, want %v", p.Window.From, want)
		}
		if len(p.Warnings) != 0 {
			t.Errorf("warnings = %q, want none -- SMART retention is 90 days", p.Warnings)
		}
	})

	t.Run("smart past its own ninety days is clamped, not promoted", func(t *testing.T) {
		p := mustPlan(t, "smart", testNow.Add(-100*day), testNow, 0, false)

		if p.Tier != TierRaw {
			t.Errorf("tier = %q, want %q", p.Tier, TierRaw)
		}
		if want := testNow.Add(-90 * day); !p.Window.From.Equal(want) {
			t.Errorf("window.from = %v, want %v", p.Window.From, want)
		}
	})

	t.Run("process is bounded at forty-eight hours", func(t *testing.T) {
		p := mustPlan(t, "process", testNow.Add(-7*day), testNow, 0, false)

		if p.Tier != TierRaw {
			t.Errorf("tier = %q, want %q", p.Tier, TierRaw)
		}
		if want := testNow.Add(-48 * time.Hour); !p.Window.From.Equal(want) {
			t.Errorf("window.from = %v, want %v", p.Window.From, want)
		}
		if !hasWarning(p, "48 hours") {
			t.Errorf("warnings = %q, want one naming the 48-hour retention", p.Warnings)
		}
	})
}

// An explicit step is total: every duration resolves to a tier rather than
// 400ing on a value that is not itself one.
func TestTierSelectionWithAnExplicitStep(t *testing.T) {
	for _, tc := range []struct {
		name   string
		family string
		step   time.Duration
		want   string
		wantS  int
	}{
		{"finer than raw clamps to raw", "host", time.Second, TierRaw, 60},
		{"exactly raw", "host", time.Minute, TierRaw, 60},
		{"between raw and 5m", "host", 2 * time.Minute, TierRaw, 60},
		{"exactly 5m", "host", 5 * time.Minute, Tier5m, 300},
		{"between 5m and 1h", "host", 10 * time.Minute, Tier5m, 300},
		{"exactly 1h", "host", time.Hour, Tier1h, 3600},
		{"coarser than 1h clamps to 1h", "host", 24 * time.Hour, Tier1h, 3600},
		{"a raw-only family ignores the step", "process", time.Hour, TierRaw, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mustPlan(t, tc.family, testNow.Add(-2*time.Hour), testNow, tc.step, true)

			if p.Tier != tc.want {
				t.Errorf("tier = %q, want %q", p.Tier, tc.want)
			}
			if got := int(p.Step / time.Second); got != tc.wantS {
				t.Errorf("step_s = %d, want %d -- the tier's step, never the request's", got, tc.wantS)
			}
		})
	}
}

// An explicit step overrides the range-based choice but NOT the clamps: asking
// for raw over sixty days returns the seven days that exist and says so,
// rather than an empty series that reads as a host with no history.
func TestTierSelectionExplicitStepStillObeysRetention(t *testing.T) {
	p := mustPlan(t, "host", testNow.Add(-60*day), testNow, time.Minute, true)

	if p.Tier != TierRaw {
		t.Fatalf("tier = %q, want %q", p.Tier, TierRaw)
	}
	if want := testNow.Add(-7 * day); !p.Window.From.Equal(want) {
		t.Errorf("window.from = %v, want %v", p.Window.From, want)
	}
	if !hasWarning(p, "predates") {
		t.Errorf("warnings = %q, want one naming the retention clamp", p.Warnings)
	}
}

// Both clamps firing can leave nothing at all: the last five minutes at a tier
// that materialises an hour behind. That is a real answer -- an empty 200 with
// the warning that explains it -- not an error, because nothing about the
// request was wrong.
func TestTierSelectionCanLeaveAnEmptyWindow(t *testing.T) {
	p := mustPlan(t, "host", testNow.Add(-5*time.Minute), testNow, time.Hour, true)

	if !p.Empty {
		t.Errorf("Empty = false, want true")
	}
	if !p.Window.From.Equal(p.Window.To) {
		t.Errorf("window = %v..%v, want an empty range", p.Window.From, p.Window.To)
	}
	if !hasWarning(p, "materialises") {
		t.Errorf("warnings = %q, want one explaining the empty window", p.Warnings)
	}
}

func TestTierSelectionRejectsAnInvertedRange(t *testing.T) {
	f, _ := lookupFamily("host")
	_, err := planQuery(f, Window{From: testNow, To: testNow.Add(-time.Hour)}, 0, false, testNow)

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// A dashboard asking for "now" against a hub whose clock is a second behind
// must not 400. A window entirely in the future must.
func TestTierSelectionAtTheFutureEdge(t *testing.T) {
	t.Run("a to in the future is clamped", func(t *testing.T) {
		p := mustPlan(t, "host", testNow.Add(-time.Hour), testNow.Add(time.Minute), 0, false)

		if !p.Window.To.Equal(testNow) {
			t.Errorf("window.to = %v, want %v", p.Window.To, testNow)
		}
		if !hasWarning(p, "future") {
			t.Errorf("warnings = %q, want one naming the future clamp", p.Warnings)
		}
	})

	t.Run("a window wholly in the future is rejected", func(t *testing.T) {
		f, _ := lookupFamily("host")
		_, err := planQuery(f,
			Window{From: testNow.Add(time.Hour), To: testNow.Add(2 * time.Hour)}, 0, false, testNow)

		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v, want ErrInvalid", err)
		}
	})
}

func TestTierSelectionDefaultsToTheLastHourOfRaw(t *testing.T) {
	p := mustPlan(t, "host", time.Time{}, time.Time{}, 0, false)

	if p.Tier != TierRaw {
		t.Errorf("tier = %q, want %q", p.Tier, TierRaw)
	}
	if want := testNow.Add(-time.Hour); !p.Window.From.Equal(want) {
		t.Errorf("window.from = %v, want %v", p.Window.From, want)
	}
	if !p.Window.To.Equal(testNow) {
		t.Errorf("window.to = %v, want %v", p.Window.To, testNow)
	}
}

// The error names the families rather than saying "invalid": a caller who
// mistyped one wants the list.
func TestLookupFamilyRejectsAnUnknownName(t *testing.T) {
	_, err := lookupFamily("cpu")

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "cpu_core") {
		t.Errorf("err = %q, want it to list the valid families", err)
	}
}

func hasWarning(p Plan, substr string) bool {
	for _, w := range p.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
