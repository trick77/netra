package conditions

import (
	"math"
	"testing"
)

func ptr(f float64) *float64 { return &f }

// A drive that has held a steady temperature for a week gets a margin from its
// magnitude, not from its range, or a sensor that never moves would alarm on
// the first warm afternoon.
func TestMarginFloorsOnAStableSubject(t *testing.T) {
	steady := Baseline{P01: 38.0, P99: 38.4, Samples: 10080}

	// Range margin is 0.5 * 0.4 = 0.2, magnitude is 0.05 * 38.4 = 1.92, floor
	// for a drive is 4. The floor is the largest, so it wins.
	if got, want := steady.Margin(4), 4.0; got != want {
		t.Errorf("margin = %v, want %v", got, want)
	}

	// With no floor the magnitude margin still rescues it from 0.2.
	if got := steady.Margin(0); math.Abs(got-1.92) > 1e-9 {
		t.Errorf("margin without floor = %v, want 1.92", got)
	}
}

// A subject that genuinely swings takes its margin from the range, because
// half its own operating span is the meaningful step beyond it.
func TestMarginFollowsRangeOnASwingingSubject(t *testing.T) {
	swings := Baseline{P01: 35, P99: 75, Samples: 10080}

	// Range 0.5 * 40 = 20, magnitude 0.05 * 75 = 3.75, floor 6.
	if got, want := swings.Margin(6), 20.0; got != want {
		t.Errorf("margin = %v, want %v", got, want)
	}
}

// THE BUG THIS WHOLE TIER EXISTS FOR. A busy NVMe whose composite temperature
// normally sits at 65 C must not be permanently critical against a ceiling
// picked for spinning disks.
func TestBusyNVMeIsNotCriticalAgainstItsOwnNormal(t *testing.T) {
	busy := Baseline{P01: 55, P99: 65, Samples: 10080}
	rule := RuleFor(KindTemperature, ChipNVMe)

	// No published limits: the nvme ceiling, not the drivetemp one.
	bounds := DeviationThresholds(busy, rule.Floor, Limits{}, rule.Ceilings)

	if got := DeviationSeverity(65, bounds); got != "" {
		t.Errorf("a drive sitting at its own p99 is %q, want healthy", got)
	}
	if bounds.Crit <= 65 {
		t.Errorf("crit = %v, must sit above the drive's normal 65", bounds.Crit)
	}

	// Against the drivetemp ceilings the same drive would be permanently red,
	// which is the regression this test pins.
	spinning := RuleFor(KindTemperature, ChipDriveTemp)
	wrong := DeviationThresholds(busy, spinning.Floor, Limits{}, spinning.Ceilings)
	if DeviationSeverity(65, wrong) != SeverityCritical {
		t.Fatal("precondition failed: the drivetemp ceiling should red-line a 65 C drive")
	}
}

// The hardware's own limit caps the calibrated one, so a subject that has been
// too hot all week cannot calibrate its way past the point the drive calls
// critical.
func TestDeviceLimitCapsTheBaseline(t *testing.T) {
	hot := Baseline{P01: 60, P99: 78, Samples: 10080}
	rule := RuleFor(KindTemperature, ChipNVMe)
	lim := Limits{High: ptr(80), HighCrit: ptr(85)}

	bounds := DeviationThresholds(hot, rule.Floor, lim, rule.Ceilings)

	if bounds.Crit != 85 {
		t.Errorf("crit = %v, want the device's 85", bounds.Crit)
	}
	if bounds.Source != SourceDevice {
		t.Errorf("source = %q, want %q", bounds.Source, SourceDevice)
	}
	if got := DeviationSeverity(86, bounds); got != SeverityCritical {
		t.Errorf("86 C against an 85 C device limit = %q, want critical", got)
	}
}

// The early warning survives a generous device limit: a drive that normally
// runs at 44 C is worth noticing at 52, not held silent until 80.
func TestBaselineStillWarnsBeneathAGenerousDeviceLimit(t *testing.T) {
	cool := Baseline{P01: 40, P99: 44, Samples: 10080}
	rule := RuleFor(KindTemperature, ChipDriveTemp)
	lim := Limits{High: ptr(70), HighCrit: ptr(80)}

	bounds := DeviationThresholds(cool, rule.Floor, lim, rule.Ceilings)

	if bounds.Warn >= 60 {
		t.Errorf("warn = %v, want the calibrated value well under the 70 C device limit", bounds.Warn)
	}
	// The device limit did not bind, so the log must not claim it decided.
	if bounds.Source != SourceBaseline {
		t.Errorf("source = %q, want %q", bounds.Source, SourceBaseline)
	}
	if got := DeviationSeverity(54, bounds); got == "" {
		t.Error("54 C on a drive that normally runs at 44 should be notable")
	}
}

// processes and load have no absolute number that is wrong everywhere, so no
// ceiling may be applied to them.
func TestHostKindsHaveNoCeiling(t *testing.T) {
	for _, kind := range []string{KindProcesses, KindLoad} {
		rule := RuleFor(kind, "")
		if rule.Ceilings.Crit != 0 {
			t.Errorf("%s carries a ceiling %v, want none", kind, rule.Ceilings)
		}

		b := Baseline{P01: 100, P99: 1200, Samples: 10080}
		bounds := DeviationThresholds(b, rule.Floor, Limits{}, rule.Ceilings)
		if bounds.Crit <= b.P99 {
			t.Errorf("%s: crit %v must exceed p99 %v", kind, bounds.Crit, b.P99)
		}
		if bounds.Source != SourceBaseline {
			t.Errorf("%s: source = %q, want %q", kind, bounds.Source, SourceBaseline)
		}
	}
}

// A chip that publishes an equal max and crit -- coretemp does on some parts --
// must still produce two distinguishable severities.
//
// The baseline is set so BOTH device limits actually bind, or the step-back
// this pins is never reached and the test passes without exercising anything.
func TestEqualDeviceLimitsKeepWarnUnderCrit(t *testing.T) {
	b := Baseline{P01: 86, P99: 88, Samples: 10080}
	rule := RuleFor(KindTemperature, "coretemp")
	lim := Limits{High: ptr(90), HighCrit: ptr(90)}

	bounds := DeviationThresholds(b, rule.Floor, lim, rule.Ceilings)

	if bounds.Crit != 90 {
		t.Fatalf("crit = %v, want the device's 90 -- the test no longer binds", bounds.Crit)
	}
	if bounds.Warn >= bounds.Crit {
		t.Errorf("warn %v must stay under crit %v", bounds.Warn, bounds.Crit)
	}
}

// THE FALLBACK CEILING MUST NOT CAP THE WARNING.
//
// drivetemp registers tempN_max only when the drive reports SCT limits, so a
// healthy 7200 rpm disk that has held 56-58 C all week publishes nothing. A
// fallback that capped warn at 55 would put it permanently at warning for being
// what it has always been -- the fleet-wide-constant failure this whole file
// exists to end, one severity down from where it was first found.
func TestTheFallbackCeilingDoesNotCapTheWarning(t *testing.T) {
	warm := Baseline{P01: 56, P99: 58, Samples: 10080}
	rule := RuleFor(KindTemperature, ChipDriveTemp)

	bounds := DeviationThresholds(warm, rule.Floor, Limits{}, rule.Ceilings)

	if got := DeviationSeverity(57, bounds); got != "" {
		t.Errorf("a drive sitting inside its own normal is %q, want healthy", got)
	}

	// The ceiling still does its own job: a drive whose normal is ALREADY past
	// it is critical, which is the case it exists for.
	cooking := Baseline{P01: 64, P99: 65, Samples: 10080}
	hot := DeviationThresholds(cooking, rule.Floor, Limits{}, rule.Ceilings)
	if got := DeviationSeverity(65, hot); got != SeverityCritical {
		t.Errorf("a drive that has run past its family ceiling all week is %q, want critical", got)
	}
}

// The source names whichever authority set the CRITICAL threshold, because that
// is the number both renderers print beside the word "limit".
//
// A chip publishing max 70 and crit 85, on a sensor calibrated to 71/74, binds
// only the warning. Calling that "device" would have the row claim 74 C was the
// drive's own limit and that 70.5 C was past it.
func TestSourceIsBaselineWhenOnlyTheWarnCapCameFromTheDevice(t *testing.T) {
	b := Baseline{P01: 60, P99: 68, Samples: 10080}
	rule := RuleFor(KindTemperature, ChipNVMe)
	lim := Limits{High: ptr(70), HighCrit: ptr(85)}

	bounds := DeviationThresholds(b, rule.Floor, lim, rule.Ceilings)

	if bounds.Warn != 70 {
		t.Fatalf("warn = %v, want the device's 70 -- the test no longer binds", bounds.Warn)
	}
	if bounds.Crit == 85 {
		t.Fatalf("crit = 85, want a calibrated value -- the test no longer binds")
	}
	if bounds.Source != SourceBaseline {
		t.Errorf("source = %q, want %q: the device limit did not decide crit",
			bounds.Source, SourceBaseline)
	}
}

// Too little history is not a clean bill of health.
func TestBaselineIsNotReadyBelowTheSampleGate(t *testing.T) {
	if (Baseline{P01: 1, P99: 2, Samples: BaselineMinSamples - 1}).Ready(BaselineMinSamples) {
		t.Error("a baseline below the gate reported ready")
	}
	if !(Baseline{P01: 1, P99: 2, Samples: BaselineMinSamples}).Ready(BaselineMinSamples) {
		t.Error("a baseline at the gate reported not ready")
	}
}

// THE FIRST MONDAY.
//
// A netra installed on a Saturday clears a 34-hour gate by Sunday evening
// against a baseline built entirely from a quiet weekend, and Monday morning is
// then a departure from normal on every host at once. Worse, the recompute
// excludes samples taken while a condition is open, so Monday's legitimate load
// is not counted as evidence and the row stands for about a week.
//
// So the two kinds driven by what people ask of a machine wait for a weekly
// cycle. Temperature does not: a drive has no weekday, and it has the chip's
// own published limit as a second tier that owes nothing to history.
func TestHostKindsWaitForAWeeklyCycleAndTemperatureDoesNot(t *testing.T) {
	weekendOnly := Baseline{P01: 1, P99: 2, Samples: BaselineMinSamples + 100}

	for _, kind := range []string{KindProcesses, KindLoad} {
		rule := RuleFor(kind, "")
		if rule.MinSamples != BaselineWeeklySamples {
			t.Errorf("%s gate = %d, want %d", kind, rule.MinSamples, BaselineWeeklySamples)
		}
		if weekendOnly.Ready(rule.MinSamples) {
			t.Errorf("%s judged on a weekend's worth of history", kind)
		}
	}

	for _, chip := range []string{ChipNVMe, ChipDriveTemp, "coretemp", "k10temp"} {
		rule := RuleFor(KindTemperature, chip)
		if rule.MinSamples != BaselineMinSamples {
			t.Errorf("temperature/%s gate = %d, want %d",
				chip, rule.MinSamples, BaselineMinSamples)
		}
		if !weekendOnly.Ready(rule.MinSamples) {
			t.Errorf("temperature/%s waited for a weekly cycle it has no use for", chip)
		}
	}
}

// The weekly gate has to be REACHABLE. Raw retention is 7 days, so the window
// can never hold more than a perfect week -- a gate at the theoretical maximum
// would need a host that has never missed a scrape, and any host that dropped
// one would never be judged again.
func TestTheWeeklyGateIsReachableByAHostThatMissesScrapes(t *testing.T) {
	perfectWeek := int(BaselineWindow / ScrapeInterval)
	if BaselineWeeklySamples >= perfectWeek {
		t.Fatalf("weekly gate %d is at or above a perfect week of %d samples, "+
			"so a host that misses one scrape can never be judged",
			BaselineWeeklySamples, perfectWeek)
	}

	// A host losing the most the sporadic rule tolerates before flagging it
	// must still clear the gate, or netra would consider it healthy and refuse
	// to judge it at the same time.
	worstTolerated := int(float64(perfectWeek) * (1 - SporadicMissRatio))
	if worstTolerated < BaselineWeeklySamples {
		t.Errorf("a host at the sporadic tolerance reaches %d samples, under the %d gate",
			worstTolerated, BaselineWeeklySamples)
	}
}

func TestDeviationSeverityBoundaries(t *testing.T) {
	bounds := Bounds{Warn: 50, Crit: 60}

	for _, tc := range []struct {
		value float64
		want  string
	}{
		{49.9, ""},
		{50, SeverityWarning},
		{59.9, SeverityWarning},
		{60, SeverityCritical},
		{61, SeverityCritical},
	} {
		if got := DeviationSeverity(tc.value, bounds); got != tc.want {
			t.Errorf("severity(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}
