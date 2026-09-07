package conditions_test

import (
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
)

func gb(n int64) *int64 {
	v := n * 1024 * 1024 * 1024
	return &v
}

// Sporadic is a RATE, and every guard on it exists because of a host that was
// fine.
func TestSporadicNeedsEnoughHistoryAndMoreThanOneMiss(t *testing.T) {
	for name, tc := range map[string]struct {
		present, span int
		want          string
	}{
		// Two buckets cannot tell a gap from a host that started reporting
		// mid-window.
		"too little history to judge": {2, 4, ""},
		// At the shortest span judged, ONE miss is exactly the ratio -- so a
		// host that dropped a single scrape while its agent settled would be
		// badged on the strength of that one bucket.
		"a single miss is an event, not a pattern": {4, 5, ""},
		"two misses in five is a pattern":          {3, 5, conditions.SeverityWarning},
		"a fifth of a long window":                 {24, 30, conditions.SeverityWarning},
		"under a fifth of a long window":           {28, 30, ""},
		"reporting cleanly":                        {30, 30, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := conditions.SporadicSeverity(tc.present, tc.span); got != tc.want {
				t.Errorf("SporadicSeverity(%d, %d) = %q, want %q",
					tc.present, tc.span, got, tc.want)
			}
		})
	}
}

// The compound rule, and the case that made it necessary.
//
// netra used to say "/mnt/ark is 90% full -- 674.4 GB free" in one breath and
// expect someone to act on it. Both halves have to agree, or a percentage on a
// large array is a permanent alarm nobody can clear.
func TestDiskNeedsBothAHighPercentageAndLittleRoom(t *testing.T) {
	for name, tc := range map[string]struct {
		pct  float64
		free *int64
		want string
	}{
		"91% of a small root":       {91, gb(9), conditions.SeverityWarning},
		"97% of a small root":       {97, gb(3), conditions.SeverityCritical},
		"91% of a 6.7 TB array":     {91, gb(674), ""},
		"97% with 287 GB left":      {97, gb(287), ""},
		"89% with almost nothing":   {89, gb(1), ""},
		"exactly at the warn floor": {conditions.DiskWarnPct, gb(50), conditions.SeverityWarning},
		"exactly at the crit floor": {conditions.DiskCritPct, gb(10), conditions.SeverityCritical},
	} {
		t.Run(name, func(t *testing.T) {
			if got := conditions.DiskSeverity(tc.pct, tc.free); got != tc.want {
				t.Errorf("DiskSeverity(%v, %v) = %q, want %q", tc.pct, *tc.free, got, tc.want)
			}
		})
	}
}

// A 512 GB SSD at 96% has 20.5 GB free, which is a warning rather than a
// critical -- and stays one until roughly 96.1%. That is the intended reading
// of critical: twenty gigabytes is where filling up is hours away, and five
// per cent of a half-terabyte disk is not.
func TestAHalfTerabyteDiskAt96PercentIsAWarning(t *testing.T) {
	free := int64(20.5 * 1024 * 1024 * 1024)
	if got := conditions.DiskSeverity(96, &free); got != conditions.SeverityWarning {
		t.Errorf("got %q, want warning", got)
	}
}

// Unknown bytes fall back to the percentage alone. A row that has lost track
// of how much room is left must not go silent about a disk at 97%.
func TestUnknownFreeBytesFallBackToThePercentage(t *testing.T) {
	if got := conditions.DiskSeverity(97, nil); got != conditions.SeverityCritical {
		t.Errorf("got %q, want critical", got)
	}
	if got := conditions.DiskSeverity(91, nil); got != conditions.SeverityWarning {
		t.Errorf("got %q, want warning", got)
	}
	if got := conditions.DiskSeverity(50, nil); got != "" {
		t.Errorf("got %q, want nothing", got)
	}
}

// used / (used + free), never used / total: total includes the root reserve,
// so dividing by it reports a disk as less full than df does.
func TestUsePctExcludesTheRootReserve(t *testing.T) {
	used, free := int64(90), int64(10)
	got, ok := conditions.UsePct(&used, &free)
	if !ok || got != 90 {
		t.Errorf("UsePct = %v, %v; want 90, true", got, ok)
	}
}

func TestUsePctRefusesWhatItCannotCompute(t *testing.T) {
	zero := int64(0)
	used := int64(5)
	for name, tc := range map[string]struct{ used, free *int64 }{
		"no used":      {nil, &zero},
		"no free":      {&used, nil},
		"empty device": {&zero, &zero},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := conditions.UsePct(tc.used, tc.free); ok {
				t.Error("computed a percentage from nothing")
			}
		})
	}
}

func TestReportingIsThreeScrapesWide(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-30 * time.Second)
	edge := now.Add(-conditions.StaleAfter)
	stale := now.Add(-conditions.StaleAfter - time.Second)

	if !conditions.Reporting(&fresh, now) {
		t.Error("a host seen 30s ago is not reporting")
	}
	// Inclusive at the boundary: exactly three scrapes is the last moment a
	// host is still considered present, not the first moment it is not.
	if !conditions.Reporting(&edge, now) {
		t.Error("a host at exactly the threshold was called silent")
	}
	if conditions.Reporting(&stale, now) {
		t.Error("a host past the threshold was called present")
	}
}

// Never seen is not reporting, and that matters beyond the silent condition
// itself: a host netra has no observation of cannot have subjects that
// "vanished" from it.
func TestAHostNeverSeenIsNotReporting(t *testing.T) {
	now := time.Now()
	if conditions.Reporting(nil, now) {
		t.Error("a host never seen was called reporting")
	}
	if got := conditions.SilentSeverity(nil, now); got != conditions.SeverityCritical {
		t.Errorf("SilentSeverity = %q, want critical", got)
	}
}

func TestATalkingHostIsNotSilent(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-10 * time.Second)
	if got := conditions.SilentSeverity(&fresh, now); got != "" {
		t.Errorf("SilentSeverity = %q, want nothing", got)
	}
}
