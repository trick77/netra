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

// bimodal is a subject that is idle at night and busy by day, every day, with
// the busy period starting `offset` minutes past 08:00.
//
// THE OFFSET IS THE POINT. The first cut of this test only used schedules that
// switched on the hour, and passed, while a switch at 08:15 measured 1,305
// firing minutes: the 08:00 bucket seeded on its idle quarter, froze the moment
// the reading crossed into the busy three quarters, and never learned them.
// Real hosts do not begin their day on the hour.
func bimodal(idle, busy float64, offset, busyHours int) func(int) float64 {
	return func(min int) float64 {
		d := min % (24 * 60)
		if start := 8*60 + offset; d >= start && d < start+busyHours*60 {
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
		if !b.Ready() {
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
// and must not read as one -- whatever minute its day begins on.
func TestASeasonalSubjectDoesNotFireEveryMorning(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind, chip string
		at         func(int) float64
		// What an earlier design measured on this shape, for the record.
		wasFiring int
	}{
		{"cpu 40/70 C busy 16h @08:00", KindTemperature, "coretemp", bimodal(40, 70, 0, 16), 27840},
		{"cpu 40/70 C busy 16h @08:15", KindTemperature, "coretemp", bimodal(40, 70, 15, 16), 1305},
		{"cpu 40/70 C busy 16h @08:30", KindTemperature, "coretemp", bimodal(40, 70, 30, 16), 870},
		{"cpu 40/70 C busy 16h @08:45", KindTemperature, "coretemp", bimodal(40, 70, 45, 16), 435},
		{"load5 0.2/3.0 busy 12h @08:00", KindLoad, "", bimodal(0.2, 3.0, 0, 12), 16560},
		{"load5 0.2/3.0 busy 12h @08:30", KindLoad, "", bimodal(0.2, 3.0, 30, 12), 0},
		{"procs 300/600 busy 12h @08:20", KindProcesses, "", bimodal(300, 600, 20, 12), 0},
	} {
		firing, e := firingMinutes(t, tc.kind, tc.chip, tc.at, 30)

		// Generous against the measured 23-89, and still well under the worst
		// case any earlier design produced. A regression to one average per
		// subject fails this by two orders of magnitude; a regression to
		// hour-aligned-only fails the offset rows by a factor of two to six.
		if firing > 200 {
			t.Errorf("%s: %d firing minutes over 30 days (an earlier design measured %d) -- "+
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
		fmt.Printf("%-30s firing=%4d  midday normal=%.2f  night normal=%.2f\n",
			tc.name, firing, busy.Slow, idle.Slow)
	}
}

// A recurring pattern that appears AFTER the subject has warmed up -- a new
// cron job -- must be learned within days, not fired on forever.
//
// This is the case the subject-level TauSlow escape cannot see: the excursion
// lasts thirty minutes and then the reading is back inside its band, so the
// continuous clock resets every morning. Only the bucket's own running fraction
// of visits spent outside can notice that the 03:00 hour has changed.
func TestANewRecurringPatternIsLearnedWithinDays(t *testing.T) {
	rule := RuleFor(KindTemperature, "coretemp")
	base := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	newJob := func(min int) float64 {
		day, d := min/(24*60), min%(24*60)
		if day >= 12 && d >= 3*60+15 && d < 3*60+45 {
			return 70
		}
		return 40
	}

	e := EWMA{}
	perDay := map[int]int{}
	for min := range 30 * 24 * 60 {
		ts := base.Add(time.Duration(min) * time.Minute)
		e = e.Update(newJob(min), ts, rule, Limits{})
		if e.Span() < rule.MinSpan {
			continue
		}
		b := e.Bucket(HourOf(ts))
		if !b.Ready() {
			continue
		}
		w, c := b.Band(rule.Floor)
		if DeviationSeverity(e.Fast, DeviationThresholds(w, c, Limits{}, rule.Ceilings)) != "" {
			perDay[min/(24*60)]++
		}
	}

	// It fires on the first days, which is right: nobody told netra about the
	// job, and thirty minutes at 70 C on a 40 C host IS a departure until it
	// has recurred. It must then stop.
	if perDay[12] == 0 {
		t.Error("the new job was not noticed on its first day")
	}
	if perDay[19] != 0 || perDay[25] != 0 {
		t.Errorf("still firing a week later: day 19 = %d, day 25 = %d minutes -- "+
			"the recurring pattern was never learned", perDay[19], perDay[25])
	}
	fmt.Printf("new 03:15 job from day 12, firing minutes per day:")
	for d := 12; d < 20; d++ {
		fmt.Printf(" d%d=%d", d, perDay[d])
	}
	fmt.Println()
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
// hot is caught in the hour it happens and stays caught for the week the design
// promises, because a fault is subject-global -- it runs through every bucket
// for hours -- and ModeMaxLength tells that from a recurring mode.
func TestASustainedFaultIsStillCaughtAcrossEveryHour(t *testing.T) {
	rule := RuleFor(KindTemperature, ChipDriveTemp)
	base := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	e := EWMA{}
	// Ten days flat, so every bucket knows 44 C.
	for min := range 10 * 24 * 60 {
		e = e.Update(44, base.Add(time.Duration(min)*time.Minute), rule, Limits{})
	}

	// Then five days at 58 -- under the 60 C ceiling, so only the baseline tier
	// can hold it -- spanning every hour of the day.
	start := 10 * 24 * 60
	caught, total := 0, 0
	for min := start; min < start+5*24*60; min++ {
		ts := base.Add(time.Duration(min) * time.Minute)
		e = e.Update(58, ts, rule, Limits{})
		b := e.Bucket(HourOf(ts))
		w, c := b.Band(rule.Floor)
		total++
		if DeviationSeverity(e.Fast, DeviationThresholds(w, c, Limits{}, rule.Ceilings)) != "" {
			caught++
		}
	}

	// Everything but the couple of minutes `fast` takes to climb.
	if caught < total-5 {
		t.Errorf("caught %d of %d minutes: the baseline tier let a sustained fault "+
			"through before its week was up", caught, total)
	}
}
