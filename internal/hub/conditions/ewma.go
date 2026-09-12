package conditions

import (
	"math"
	"time"
)

// What a subject normally does, kept as a moving average rather than recomputed
// from a window.
//
// THE WINDOW THIS REPLACED, AND WHY. Until now "normal" was p01/p99 over seven
// days of raw samples, rebuilt nightly by a procedure that sorted every reading
// every host had taken in a week. That worked, and it cost: a fleet-wide
// percentile sort as a scheduled job; a table of percentiles to keep in step
// with it; a SQL exclusion joining host_conditions so a fault could not enter
// its own baseline; a bound on that exclusion so a permanent shift could not
// freeze the baseline forever; and two gates expressed as sample counts --
// 2000, 8000 -- which were a scrape cadence multiplied by a calendar
// requirement and readable as neither. Four of those five are arithmetic here.
//
// WHAT IT DOES NOT REPLACE, because the numbers say otherwise and it would be
// easy to claim:
//
//   - The warm-up. With TauSlow at seven days, var has accumulated 3.5% of its
//     steady-state weight after six hours, so sd reads 0.19 of true and a
//     4-sigma band is really 0.75 sigma: everything fires. Bias-correcting only
//     substitutes the six-hour sample variance, which does not contain the
//     weekly cycle, so the first Monday fires anyway. A seven-day time constant
//     needs about seven days of observation, whatever the mechanism. What goes
//     away is a sample count standing in for a calendar, not the calendar.
//
//   - The fault-versus-new-normal decision. "The drive is cooking" and "this
//     host is busier now" are the same signal, and no amount of looking at it
//     tells them apart. Something has to decide when a persistent excursion
//     becomes the normal. That was a predicate in SQL; here it is one timestamp
//     and one comparison. Simpler, not absent.

// Time constants.
const (
	// TauFast is the smoothing on "what it is doing now".
	//
	// Two minutes rather than five, and the difference is detection latency. A
	// reading that lands just over the line is approached asymptotically, so
	// fast needs several multiples of Tau to cross it: at five minutes that is
	// fifteen to twenty-five, against the flat three minutes the tick counter
	// this replaces used to take. At two it is six to ten, and a large
	// excursion is faster than the counter ever was.
	TauFast = 2 * time.Minute

	// TauSlow is the smoothing on "what it normally does", and therefore how
	// long a persistent excursion takes to be accepted as the new normal.
	//
	// Seven days, matching what the window it replaces measured over -- and now
	// the only place that number appears, rather than being restated as a
	// retention interval, a sample count and a config default.
	TauSlow = 7 * 24 * time.Hour
)

// How many standard deviations out is worth saying something about.
const (
	KWarn = 3.0
	KCrit = 4.0
)

// OpenFor is how long a subject must be outside its band before it is raised.
//
// THE FAST AVERAGE DOES NOT DO THIS JOB, and assuming it did was wrong. A
// smoother is magnitude-blind: with TauFast at two minutes, a ninety-second
// burst to twenty-eight sigma carries `fast` 63% of the way there and straight
// through the band, while a sustained four-sigma excursion takes far longer to
// cross. Suppressing the burst by lengthening TauFast to the half-hour it would
// need makes every real excursion an hour late. One constant cannot do both,
// because they are not the same question: "how big" and "how long" have to be
// asked separately.
//
// So `fast` smooths sensor jitter, and this decides when an excursion has
// lasted long enough to mean something. Three minutes, matching the three
// consecutive passes the in-memory counter this replaces used to require --
// and better than it, because a duration measured from ExcursionSince survives
// a hub restart, where the counter started again from zero.
const OpenFor = 3 * time.Minute

// EWMA is one subject's moving state.
//
// Four numbers and three timestamps, updated as samples arrive, against a row
// per subject in a table nothing has to sort.
type EWMA struct {
	// Slow is the subject's normal, Fast is what it is doing now, Var is the
	// exponentially weighted variance of the deviation from Slow.
	Slow float64
	Fast float64
	Var  float64

	// FirstTS is the oldest reading folded in, which is what the warm-up is
	// measured against. UpdatedTS is the newest, and the reference the decay is
	// computed from.
	FirstTS   time.Time
	UpdatedTS time.Time

	// ExcursionSince is when Fast last left the band, or zero while it is
	// inside. See Update: it is what stops a fault being learned, and what lets
	// a genuine new normal eventually be.
	ExcursionSince time.Time
}

// Seeded reports whether this state has any reading in it at all.
func (e EWMA) Seeded() bool { return !e.UpdatedTS.IsZero() }

// Span is how much history this state rests on.
func (e EWMA) Span() time.Duration {
	if !e.Seeded() {
		return 0
	}
	return e.UpdatedTS.Sub(e.FirstTS)
}

// Scale is the size of a meaningful departure, in the metric's own unit.
//
// The floor is not a fudge. A drive in a climate-controlled rack holds 38.0 to
// 38.4 C for a week, so its true sd is 0.1 and a 4-sigma band is 0.4 C: it
// would alarm on the first warm afternoon, correctly by the arithmetic and
// uselessly in fact. The floor is the smallest departure worth a sentence.
func (e EWMA) Scale(floor float64) float64 {
	return math.Max(math.Sqrt(e.Var), floor)
}

// Band is the pair Fast is judged against.
//
// Named Band rather than Bounds because Bounds is the struct
// DeviationThresholds returns after the hardware limits have capped this pair,
// and having the two spellings one letter apart invited exactly the mistake of
// judging against the uncapped numbers.
func (e EWMA) Band(floor float64) (warn, crit float64) {
	scale := e.Scale(floor)
	return e.Slow + KWarn*scale, e.Slow + KCrit*scale
}

// Update folds one reading in, and reports the state after it.
//
// A METHOD ON A VALUE RETURNING A VALUE, so a caller folding sixty buffered
// samples forward is a loop over a pure function and the whole thing is
// testable without a database -- the same shape DiskSeverity and DriveFindings
// are in.
//
// Decay is computed from the ELAPSED TIME, not per call, and that is the reason
// this can be folded forward at all. An agent buffers an hour by default
// (AGENT_BUFFER_WINDOW) and posts sixty samples at once when it reconnects. A
// per-call decay would weight those sixty as sixty consecutive minutes of
// wall-clock, which they are, or as one tick, which is what a naive
// tick-driven update does -- and then the average tracks how often the hub
// looked rather than what the host did.
func (e EWMA) Update(x float64, ts time.Time, floor float64) EWMA {
	if !e.Seeded() {
		// The first reading IS the normal, with no spread yet. Var stays zero,
		// so Scale falls back to the floor and the warm-up below is what keeps
		// anything from being judged on it.
		return EWMA{
			Slow: x, Fast: x, Var: 0,
			FirstTS: ts, UpdatedTS: ts,
		}
	}

	// Out-of-order or duplicate readings are dropped rather than folded
	// backwards. Replay re-delivers samples the state has already seen -- the
	// natural key on the sample tables makes that harmless there, and it must
	// be harmless here too, or a reconnecting agent would fold its whole buffer
	// in twice and halve its own variance.
	dt := ts.Sub(e.UpdatedTS)
	if dt <= 0 {
		return e
	}

	next := e
	next.UpdatedTS = ts
	next.Fast += alpha(dt, TauFast) * (x - e.Fast)

	// THE FREEZE, and it is load-bearing rather than a refinement.
	//
	// Without it the variance channel recreates the exact bug the exclusion in
	// the window design existed to prevent. Walk it: a drive sits 14 sigma out
	// and stays there. var chases (x-slow)^2 = 196, reaching 26 within a day,
	// so sd becomes 5.1 and the band becomes slow + 20 against a reading of
	// slow + 14. The reading is now INSIDE its own band, the condition clears,
	// and the hub has reported a recovery for a drive that never cooled.
	//
	// Winsorising the update was the alternative and is worse in both
	// directions: clamping the deviation at 4 x scale pins sd near 4 x floor,
	// which widens the band to 16 x floor and clears early anyway, while
	// slowing a legitimate shift to 0.57 units a day -- a 14-unit step would
	// take twenty-five days to be accepted.
	//
	// Freezing costs nothing on a spike. Fast crosses, learning pauses for the
	// minutes it takes to come back, and nothing is lost: a single sample moves
	// Slow by alpha x deviation, which for a 20-unit spike at this Tau is
	// 0.002.
	warn, _ := e.Band(floor)
	if next.Fast > warn {
		if next.ExcursionSince.IsZero() {
			next.ExcursionSince = ts
		}
		// ...but an excursion that has outlasted the averaging window itself is
		// not an excursion any more, whatever anyone thinks of it. Seven days
		// at a level IS that level. Learning resumes, the band follows, and the
		// condition clears on its own -- which is the same decision the window
		// design spelled as `opened_ts > cutoff`, in one comparison instead of
		// a join.
		//
		// What keeps that safe for a subject that is genuinely in trouble is
		// the other tier: a drive whose normal has crept to 58 C still meets
		// its published limit, or its family ceiling, and stays critical on
		// that. See DeviationThresholds.
		if ts.Sub(next.ExcursionSince) < TauSlow {
			return next
		}
	} else {
		next.ExcursionSince = time.Time{}
	}

	a := alpha(dt, TauSlow)
	d := x - e.Slow
	next.Slow += a * d
	next.Var += a * (d*d - e.Var)
	if next.Var < 0 {
		// Arithmetic only, not a real state: a is in (0,1) so the update cannot
		// go negative mathematically. Floored anyway because Var feeds a square
		// root, and a NaN threshold would judge every reading as healthy --
		// failing silent, which is the one direction this must not fail.
		next.Var = 0
	}
	return next
}

// alpha is the weight one reading carries, given the gap since the last.
//
// 1 - exp(-dt/tau) rather than a fixed fraction, which is what makes the
// average a function of TIME rather than of how many times it was called. A gap
// of a whole tau carries 63% of the weight; a gap of one minute against a
// seven-day tau carries 0.0099%.
func alpha(dt, tau time.Duration) float64 {
	if dt <= 0 {
		return 0
	}
	return 1 - math.Exp(-dt.Seconds()/tau.Seconds())
}
