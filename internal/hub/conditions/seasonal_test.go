package conditions

import (
	"fmt"
	"testing"
	"time"
)

// THE TEST THAT PUT THE HOURS IN, kept because the number it asserts is the
// whole justification for twenty-four buckets instead of one average.
//
// A single slow/var pair measured 27,840 firing minutes over thirty days on a
// CPU busy sixteen hours a day, with no fault anywhere in the data: `slow` stuck
// at the idle level, `sd` stuck at the floor, a condition opening every morning
// and clearing every night forever. A decaying q99 in its place measured 27,927
// -- worse -- which is what established that the freeze was the cause rather
// than the estimator.
//
// Simulated rather than driven through the database on purpose. Thirty days at
// one sample a minute is 43,200 readings per subject, and the property under
// test is arithmetic: whether the band converges to contain ordinary behaviour.
// The store tests cover the fold and the scan actually carrying it.

// bimodal is a subject that is idle at night and busy by day, every day.
func bimodal(idle, busy float64, busyHours int) func(int) float64 {
	return func(min int) float64 {
		if h := (min / 60) % 24; h >= 8 && h < 8+busyHours {
			return busy
		}
		return idle
	}
}

func flat(v float64) func(int) float64 { return func(int) float64 { return v } }

// firingMinutes folds a whole series and counts how many minutes the subject
// would have read as departing from its own normal.
//
// Counted WITHOUT the OpenFor gate, so it is the raw crossing count rather than
// the number of conditions that would open. That makes it a stricter measure
// than production behaviour: the residual minutes below are single crossings
// either side of a mode change, which OpenFor swallows entirely.
func firingMinutes(t *testing.T, kind, chip string, at func(int) float64, days int) (int, EWMA) {
	t.Helper()
	rule := RuleFor(kind, chip)
	// A Monday, so the simulated week starts where a real one does.
	base := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	e := EWMA{}
	firing := 0
	for min := range days * 24 * 60 {
		ts := base.Add(time.Duration(min) * time.Minute)
		e = e.Update(at(min), ts, rule, Limits{})
		if e.Span() < rule.MinSpan {
			continue
		}
		b := e.Bucket(HourOf(ts))
		if !b.Seeded() {
			continue
		}
		w, c := b.Band(rule.Floor)
		if DeviationSeverity(e.Fast, DeviationThresholds(w, c, Limits{}, rule.Ceilings)) != "" {
			firing++
		}
	}
	return firing, e
}

// A host that does more work by day than by night is not a host with a problem,
// and must not read as one.
func TestASeasonalSubjectDoesNotFireEveryMorning(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind, chip string
		at         func(int) float64
		// The single-average design's measured count, for the record.
		wasFiring int
	}{
		{"cpu 40/70 C busy 16h", KindTemperature, "coretemp", bimodal(40, 70, 16), 27840},
		{"load5 0.2/3.0 busy 12h", KindLoad, "", bimodal(0.2, 3.0, 12), 16560},
		{"procs 300/600 busy 12h", KindProcesses, "", bimodal(300, 600, 12), 16560},
	} {
		firing, e := firingMinutes(t, tc.kind, tc.chip, tc.at, 30)

		// Generous against the measured 23-29, and still three orders of
		// magnitude under the single-average design. A regression that
		// reintroduced one average per subject fails this by a factor of 200.
		if firing > 200 {
			t.Errorf("%s: %d firing minutes over 30 days (one average measured %d) -- "+
				"a seasonal subject is being reported as abnormal",
				tc.name, firing, tc.wasFiring)
		}

		// And the reason it does not fire: each bucket learned its own mode,
		// rather than one average sitting between them.
		busy := e.Bucket(12)
		idle := e.Bucket(2)
		if busy.Slow <= idle.Slow {
			t.Errorf("%s: midday normal %.2f is not above the small-hours normal %.2f",
				tc.name, busy.Slow, idle.Slow)
		}
		fmt.Printf("%-26s firing=%4d  midday normal=%.2f  night normal=%.2f\n",
			tc.name, firing, busy.Slow, idle.Slow)
	}
}

// A subject with no daily rhythm must not be made worse by the bucketing: every
// bucket converges on the same value and the band is where it always was.
func TestAFlatSubjectIsUnaffectedByBucketing(t *testing.T) {
	firing, e := firingMinutes(t, KindTemperature, ChipDriveTemp, flat(44), 30)
	if firing != 0 {
		t.Errorf("%d firing minutes on a drive that held 44 C for a month", firing)
	}
	for hour := range Buckets {
		b := e.Bucket(hour)
		if !b.Seeded() {
			t.Errorf("hour %d was never seeded over 30 days", hour)
			continue
		}
		if b.Slow < 43.9 || b.Slow > 44.1 {
			t.Errorf("hour %d normal = %v, want 44", hour, b.Slow)
		}
	}
}

// The buckets must not cost fault detection. A drive that goes hot and stays
// hot is caught in the hour it happens and stays caught, because every bucket
// it then passes through sees the excursion.
func TestASustainedFaultIsStillCaughtAcrossEveryHour(t *testing.T) {
	rule := RuleFor(KindTemperature, ChipDriveTemp)
	base := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	e := EWMA{}
	// Ten days flat, so every bucket knows 44 C.
	for min := range 10 * 24 * 60 {
		e = e.Update(44, base.Add(time.Duration(min)*time.Minute), rule, Limits{})
	}

	// Then three days at 61, spanning every hour of the day.
	start := 10 * 24 * 60
	caught, total := 0, 0
	for min := start; min < start+3*24*60; min++ {
		ts := base.Add(time.Duration(min) * time.Minute)
		e = e.Update(61, ts, rule, Limits{})
		b := e.Bucket(HourOf(ts))
		w, c := b.Band(rule.Floor)
		total++
		if DeviationSeverity(e.Fast, DeviationThresholds(w, c, Limits{}, rule.Ceilings)) != "" {
			caught++
		}
	}

	// Everything but the couple of minutes `fast` takes to climb.
	if caught < total-5 {
		t.Errorf("caught %d of %d minutes: the fault is being lost at hour boundaries",
			caught, total)
	}
}
