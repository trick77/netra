package conditions

import (
	"math"
	"testing"
	"time"
)

var epoch = time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)

// steady folds n readings of x at the scrape cadence and returns the state.
func steady(x float64, n int, floor float64) EWMA {
	e := EWMA{}
	for i := range n {
		e = e.Update(x, epoch.Add(time.Duration(i)*ScrapeInterval), FamilyRule{Floor: floor}, Limits{})
	}
	return e
}

// A flat series must not alarm on its own noise. Its true spread is zero, so a
// 4-sigma band is zero wide, and the floor is the only thing standing between
// that and a condition on every reading.
func TestAFlatSeriesStaysQuiet(t *testing.T) {
	const floor = 4.0
	e := steady(44, 3000, floor)

	warn, crit := e.Band(floor)
	if warn <= 44 || crit <= warn {
		t.Fatalf("band = %v/%v, want both above the normal 44 and ordered", warn, crit)
	}
	if got := crit - 44; math.Abs(got-KCrit*floor) > 1e-9 {
		t.Errorf("crit sits %v above normal, want %v (K x floor)", got, KCrit*floor)
	}
}

// The first reading is the normal, with no spread. Nothing may be judged on it,
// which is the span gate's job rather than this one's.
func TestTheFirstReadingSeedsTheState(t *testing.T) {
	e := EWMA{}.Update(44, epoch, FamilyRule{Floor: 4}, Limits{})

	if e.Slow != 44 || e.Fast != 44 || e.Var != 0 {
		t.Errorf("seed = %+v, want slow=fast=44 var=0", e)
	}
	if e.Span() != 0 {
		t.Errorf("span = %v, want 0", e.Span())
	}
	if !e.Seeded() {
		t.Error("a seeded state reported unseeded")
	}
}

// Decay is a function of elapsed time, not of how many times Update was called.
// That is what lets a reconnecting agent's buffered hour fold in as the hour it
// was rather than as one tick.
func TestDecayFollowsElapsedTimeNotCallCount(t *testing.T) {
	const floor = 0.5

	// One reading an hour later.
	oneJump := EWMA{}.Update(1.0, epoch, FamilyRule{Floor: floor}, Limits{})
	oneJump = oneJump.Update(5.0, epoch.Add(time.Hour), FamilyRule{Floor: floor}, Limits{})

	// Sixty readings a minute apart, covering the same hour.
	buffered := EWMA{}.Update(1.0, epoch, FamilyRule{Floor: floor}, Limits{})
	for i := 1; i <= 60; i++ {
		buffered = buffered.Update(5.0, epoch.Add(time.Duration(i)*time.Minute), FamilyRule{Floor: floor}, Limits{})
	}

	// Slow has the same total elapsed weight either way, so the two land in the
	// same place: the average measures the SERIES, not the polling.
	//
	// This is the whole reason the fold reads forward from its own high-water
	// mark rather than taking the newest sample. A tick-driven update would
	// absorb one of those sixty readings and the average would track how often
	// the hub looked.
	if math.Abs(oneJump.Slow-buffered.Slow) > 1e-6 {
		t.Errorf("slow diverged: one jump %v, sixty readings %v", oneJump.Slow, buffered.Slow)
	}

	// Over an hour -- thirty TauFast -- fast has saturated either way, so it
	// cannot distinguish them here. Its sensitivity to spacing is covered by
	// TestAShortBurstIsSmoothedButStillCrossesTheBand, over minutes.
	if math.Abs(oneJump.Fast-buffered.Fast) > 1e-6 {
		t.Errorf("fast: %v vs %v, both should have saturated over an hour",
			oneJump.Fast, buffered.Fast)
	}
}

// A replayed sample must not be folded in twice. The agent's ring re-delivers
// what the hub already has, and a second fold would halve the subject's own
// variance.
func TestAReplayedSampleIsIgnored(t *testing.T) {
	const floor = 4.0
	e := steady(44, 100, floor)
	last := e.UpdatedTS

	again := e.Update(44, last, FamilyRule{Floor: floor}, Limits{})
	if again != e {
		t.Error("a sample at the high-water mark changed the state")
	}
	backwards := e.Update(99, last.Add(-time.Hour), FamilyRule{Floor: floor}, Limits{})
	if backwards != e {
		t.Error("an out-of-order sample changed the state")
	}
}

// THE FOLD AND THE JUDGE MUST AGREE ON WHERE THE BAND IS, and for a while they
// did not: the fold decided excursions against the UNCAPPED band while
// judgeDeviation judges against the capped one.
//
// Wherever a cap bites, the judge sees a severity the fold recorded no excursion
// for, ExcursionSince stays zero, and the subject is filed unjudged for as long
// as it lasts -- so every condition the family ceiling and the published limits
// exist to raise could never be raised. The freeze failed with it, so the
// subject went on folding the excursion into its own normal.
func TestTheExcursionIsDecidedAgainstTheCappedBand(t *testing.T) {
	rule := RuleFor(KindTemperature, ChipDriveTemp)

	// A drive whose normal has crept to 50 with a 4-degree spread. The uncapped
	// band is 62/66; the 60 C family ceiling pulls crit to 60 and warn beneath
	// it, so a reading of 61 is critical to the judge and inside the uncapped
	// band to anything that forgot the cap.
	e := EWMA{Slow: 50, Fast: 50, Var: 16, FirstTS: epoch, UpdatedTS: epoch}

	uncappedWarn, uncappedCrit := e.Band(rule.Floor)
	capped := DeviationThresholds(uncappedWarn, uncappedCrit, Limits{}, rule.Ceilings)
	if 61 <= capped.Warn || 61 >= uncappedWarn {
		t.Fatalf("precondition: 61 must be over the capped warn %v and under the "+
			"uncapped warn %v", capped.Warn, uncappedWarn)
	}
	if DeviationSeverity(61, capped) == "" {
		t.Fatal("precondition: 61 must be a severity to the judge")
	}

	// Five minutes at 61 carries `fast` to about 60: over the capped warn of 58,
	// and still under the uncapped 62. So this is exactly the window in which
	// the two disagreed.
	next := e
	for k := 1; k <= 5; k++ {
		next = next.Update(61, epoch.Add(time.Duration(k)*time.Minute), rule, Limits{})
	}
	if next.Fast <= capped.Warn || next.Fast >= uncappedWarn {
		t.Fatalf("precondition: fast %v must sit between the capped warn %v and "+
			"the uncapped %v", next.Fast, capped.Warn, uncappedWarn)
	}
	if next.ExcursionSince.IsZero() {
		t.Error("no excursion recorded for a reading the judge calls critical: " +
			"the ceiling and the published limits can never raise anything")
	}
}

// THE REGRESSION THAT MADE THE FREEZE MANDATORY.
//
// Without it the variance channel recreates the bug the window design's
// exclusion existed to prevent. A drive sits far outside its band and stays
// there; var chases the squared deviation, sd grows, the band widens past the
// reading, and the hub reports a recovery for a drive that never cooled.
func TestASustainedFaultDoesNotWidenItsOwnBandPastItself(t *testing.T) {
	const floor = 4.0
	e := steady(44, 5000, floor)

	warn, _ := e.Band(floor)
	if 58 <= warn {
		t.Fatalf("precondition: 58 must start outside the band at %v", warn)
	}

	// Three days stuck at 58.
	ts := e.UpdatedTS
	for range 3 * 24 * 60 {
		ts = ts.Add(ScrapeInterval)
		e = e.Update(58, ts, FamilyRule{Floor: floor}, Limits{})
	}

	w, c := e.Band(floor)
	if DeviationSeverity(e.Fast, Bounds{Warn: w, Crit: c}) == "" {
		t.Errorf("after three days at 58 the band is %v/%v and fast is %v: "+
			"the fault widened its own band and would be reported as recovered",
			w, c, e.Fast)
	}
	if e.ExcursionSince.IsZero() {
		t.Error("no excursion was recorded")
	}
}

// ...and the other half of the same decision: an excursion that has outlasted
// the averaging window is not an excursion. Seven days at a level IS that level,
// so learning resumes and the condition can finally clear.
//
// This is what stops a permanent legitimate shift -- a deploy that doubles a
// host's load for good -- from producing a row that can never be cleared.
func TestAnExcursionOutlastingTauSlowBecomesTheNewNormal(t *testing.T) {
	const floor = 0.5
	e := steady(2.0, 5000, floor)
	before := e.Slow

	// Ten days at the new level, comfortably past TauSlow.
	ts := e.UpdatedTS
	for range 10 * 24 * 60 {
		ts = ts.Add(ScrapeInterval)
		e = e.Update(6.0, ts, FamilyRule{Floor: floor}, Limits{})
	}

	if e.Slow <= before+0.5 {
		t.Errorf("slow = %v, barely moved from %v: a shift that outlasted TauSlow "+
			"was never accepted, so the condition could never clear", e.Slow, before)
	}
	w, c := e.Band(floor)
	if DeviationSeverity(e.Fast, Bounds{Warn: w, Crit: c}) != "" {
		t.Errorf("after ten days the band is %v/%v against fast %v, still firing",
			w, c, e.Fast)
	}
}

// A brief spike costs nothing. Fast crosses, learning pauses for the minutes it
// takes to come back, and the normal is where it was.
func TestABriefSpikeDoesNotMoveTheNormal(t *testing.T) {
	const floor = 4.0
	e := steady(44, 5000, floor)
	before := e.Slow

	ts := e.UpdatedTS
	for range 3 {
		ts = ts.Add(ScrapeInterval)
		e = e.Update(80, ts, FamilyRule{Floor: floor}, Limits{})
	}
	for range 30 {
		ts = ts.Add(ScrapeInterval)
		e = e.Update(44, ts, FamilyRule{Floor: floor}, Limits{})
	}

	if math.Abs(e.Slow-before) > 0.05 {
		t.Errorf("slow moved from %v to %v on a three-minute spike", before, e.Slow)
	}
	if !e.ExcursionSince.IsZero() {
		t.Error("the excursion was not cleared once the reading came back")
	}
}

// THE SMOOTHER IS MAGNITUDE-BLIND, and this pins why OpenFor has to exist
// separately from it.
//
// A large short burst crosses the band easily: two minutes at twenty-eight
// sigma carries fast 63% of the way there. Only the DURATION of the excursion
// separates that from a real one, which is why judgeDeviation files an
// excursion younger than OpenFor as unjudged instead of leaning on TauFast.
// Lengthening TauFast until the burst were suppressed would make every genuine
// excursion an hour late.
func TestAShortBurstIsSmoothedButStillCrossesTheBand(t *testing.T) {
	const floor = 0.5
	e := steady(2.0, 5000, floor)
	warn, _ := e.Band(floor)

	ts := e.UpdatedTS
	for range 2 {
		ts = ts.Add(ScrapeInterval)
		e = e.Update(30.0, ts, FamilyRule{Floor: floor}, Limits{})
	}

	if e.Fast <= warn {
		t.Fatalf("fast = %v, did not cross %v: this test no longer demonstrates "+
			"why OpenFor is needed", e.Fast, warn)
	}
	// Smoothed, though: nowhere near the raw 30.
	if e.Fast > 25 {
		t.Errorf("fast = %v, want well under the raw reading of 30", e.Fast)
	}
	// And the clock the suppression runs on has started.
	if ts.Sub(e.ExcursionSince) >= OpenFor {
		t.Errorf("excursion already %v old, want under OpenFor", ts.Sub(e.ExcursionSince))
	}
}

// Var feeds a square root, so it must never go negative: a NaN threshold would
// judge every reading as healthy, which is the one direction this must not fail.
func TestVarianceNeverGoesNegative(t *testing.T) {
	const floor = 4.0
	e := steady(44, 200, floor)

	ts := e.UpdatedTS
	for i := range 500 {
		ts = ts.Add(ScrapeInterval)
		e = e.Update(44+float64(i%7)-3, ts, FamilyRule{Floor: floor}, Limits{})
		if e.Var < 0 {
			t.Fatalf("var = %v", e.Var)
		}
		if math.IsNaN(e.Scale(floor)) {
			t.Fatal("scale is NaN")
		}
	}
}
