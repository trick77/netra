package conditions

import (
	"testing"
	"time"
)

func ptr(f float64) *float64 { return &f }

// band is the pair a subject with this normal and this spread is judged against,
// before any hardware limit caps it.
func band(normal, sd, floor float64) (warn, crit float64) {
	return Bucket{Slow: normal, Var: sd * sd}.Band(floor)
}

// THE BUG THIS WHOLE TIER EXISTS FOR. A busy NVMe whose composite temperature
// normally sits at 65 C must not be permanently critical against a ceiling
// picked for spinning disks.
func TestBusyNVMeIsNotCriticalAgainstItsOwnNormal(t *testing.T) {
	rule := RuleFor(KindTemperature, ChipNVMe)
	warn, crit := band(65, 2, rule.Floor)

	bounds := DeviationThresholds(warn, crit, Limits{}, rule.Ceilings)
	if got := DeviationSeverity(65, bounds); got != "" {
		t.Errorf("a drive sitting at its own normal is %q, want healthy", got)
	}
	if bounds.Crit <= 65 {
		t.Errorf("crit = %v, must sit above the drive's normal 65", bounds.Crit)
	}

	// Against the drivetemp ceiling the same drive would be permanently red,
	// which is the regression this pins.
	spinning := RuleFor(KindTemperature, ChipDriveTemp)
	sWarn, sCrit := band(65, 2, spinning.Floor)
	wrong := DeviationThresholds(sWarn, sCrit, Limits{}, spinning.Ceilings)
	if DeviationSeverity(65, wrong) != SeverityCritical {
		t.Fatal("precondition failed: the drivetemp ceiling should red-line a 65 C drive")
	}
}

// The hardware's own limit caps the calibrated one, so a subject whose average
// has crept upward cannot drift past the point the drive calls critical.
func TestDeviceLimitCapsTheBand(t *testing.T) {
	rule := RuleFor(KindTemperature, ChipNVMe)
	warn, crit := band(78, 3, rule.Floor)
	lim := Limits{High: ptr(80), HighCrit: ptr(85)}

	bounds := DeviationThresholds(warn, crit, lim, rule.Ceilings)

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
// runs at 44 C is worth noticing well before 80.
func TestTheBandStillWarnsBeneathAGenerousDeviceLimit(t *testing.T) {
	rule := RuleFor(KindTemperature, ChipDriveTemp)
	warn, crit := band(44, 1, rule.Floor)
	lim := Limits{High: ptr(70), HighCrit: ptr(80)}

	bounds := DeviationThresholds(warn, crit, lim, rule.Ceilings)

	if bounds.Warn >= 60 {
		t.Errorf("warn = %v, want the calibrated value well under the 70 C limit", bounds.Warn)
	}
	// The device limit did not bind, so the log must not claim it decided.
	if bounds.Source != SourceBaseline {
		t.Errorf("source = %q, want %q", bounds.Source, SourceBaseline)
	}
	if got := DeviationSeverity(62, bounds); got == "" {
		t.Error("62 C on a drive that normally runs at 44 should be notable")
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

		warn, crit := band(1200, 50, rule.Floor)
		bounds := DeviationThresholds(warn, crit, Limits{}, rule.Ceilings)
		if bounds.Crit <= 1200 {
			t.Errorf("%s: crit %v must exceed the normal 1200", kind, bounds.Crit)
		}
		if bounds.Source != SourceBaseline {
			t.Errorf("%s: source = %q, want %q", kind, bounds.Source, SourceBaseline)
		}
	}
}

// A chip that publishes an equal max and crit -- coretemp does on some parts --
// must still produce two distinguishable severities.
func TestEqualDeviceLimitsKeepWarnUnderCrit(t *testing.T) {
	rule := RuleFor(KindTemperature, "coretemp")
	warn, crit := band(88, 1, rule.Floor)
	lim := Limits{High: ptr(90), HighCrit: ptr(90)}

	bounds := DeviationThresholds(warn, crit, lim, rule.Ceilings)

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
// what it has always been.
func TestTheFallbackCeilingDoesNotCapTheWarning(t *testing.T) {
	rule := RuleFor(KindTemperature, ChipDriveTemp)

	warn, crit := band(57, 0.5, rule.Floor)
	bounds := DeviationThresholds(warn, crit, Limits{}, rule.Ceilings)
	if got := DeviationSeverity(57, bounds); got != "" {
		t.Errorf("a drive sitting at its own normal is %q, want healthy", got)
	}

	// The ceiling still does its own job: a drive whose normal is ALREADY past
	// it is critical, which is the case it exists for.
	hWarn, hCrit := band(65, 0.5, rule.Floor)
	hot := DeviationThresholds(hWarn, hCrit, Limits{}, rule.Ceilings)
	if got := DeviationSeverity(65, hot); got != SeverityCritical {
		t.Errorf("a drive past its family ceiling all week is %q, want critical", got)
	}
}

// The source names whichever authority set the CRITICAL threshold, because that
// is the number both renderers print beside the word "limit".
func TestSourceIsBaselineWhenOnlyTheWarnCapCameFromTheDevice(t *testing.T) {
	rule := RuleFor(KindTemperature, ChipNVMe)
	warn, crit := band(68, 1, rule.Floor)
	lim := Limits{High: ptr(70), HighCrit: ptr(85)}

	bounds := DeviationThresholds(warn, crit, lim, rule.Ceilings)

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

// THE FIRST MONDAY, as a property of the rule table.
//
// A netra installed on a Saturday would, with a one-day span, have calibrated
// entirely against a quiet weekend, and Monday morning is then a departure from
// normal on every host at once. The two kinds driven by what people ask of a
// machine wait for a weekly cycle; temperature does not, because a drive has no
// weekday and has the chip's published limit as a second tier.
func TestHostKindsWaitForAWeeklyCycleAndTemperatureDoesNot(t *testing.T) {
	for _, kind := range []string{KindProcesses, KindLoad} {
		if got := RuleFor(kind, "").MinSpan; got != TauSlow {
			t.Errorf("%s span = %v, want %v", kind, got, TauSlow)
		}
	}
	for _, chip := range []string{ChipNVMe, ChipDriveTemp, "coretemp", "k10temp"} {
		got := RuleFor(KindTemperature, chip).MinSpan
		if got != 24*time.Hour {
			t.Errorf("temperature/%s span = %v, want 24h", chip, got)
		}
		if got >= TauSlow {
			t.Errorf("temperature/%s waited for a weekly cycle it has no use for", chip)
		}
	}
}

// A span tolerates gaps completely, which is the whole reason it replaced a
// sample count: the count had to sit under a perfect week's worth of samples or
// a host that ever missed a scrape could never be judged at all.
func TestTheSpanGateIgnoresMissedScrapes(t *testing.T) {
	rule := RuleFor(KindLoad, "")
	start := time.Now().UTC().Add(-8 * 24 * time.Hour)

	// Eight days of history carrying only a handful of readings, which no
	// sample-count gate would ever have passed.
	state := EWMA{}
	for i := range 40 {
		state = state.Update(1.5, start.Add(time.Duration(i)*5*time.Hour), rule, Limits{})
	}

	if state.Span() < rule.MinSpan {
		t.Fatalf("span = %v, want at least %v", state.Span(), rule.MinSpan)
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
