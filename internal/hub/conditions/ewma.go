package conditions

import (
	"math"
	"time"
)

// What a subject normally does AT THIS HOUR, kept as a moving average rather
// than recomputed from a window.
//
// THE HISTORY OF THIS FILE IS THE ARGUMENT FOR ITS SHAPE, so it is worth
// setting down. "Normal" was first p01/p99 over seven days of raw samples,
// rebuilt nightly by a procedure that sorted every reading every host had taken
// in a week; that cost a scheduled fleet-wide percentile sort, a table of
// percentiles, a SQL join so a fault could not enter its own baseline, a bound
// on that join so a permanent shift could not freeze it forever, and two gates
// written as sample counts that were really a scrape cadence times a calendar.
// A single moving average replaced all of it -- and then fired every morning on
// every host that does more work by day than by night.
//
// THE BUG THAT PUT THE HOURS IN, measured on the single-average version over
// thirty simulated days, with no fault anywhere in the data:
//
//	coretemp 40/70 C, busy 16h   slow stuck at 40.05, sd stuck at the floor
//	                             27,840 firing minutes
//	load5 0.2/3.0, busy 12h      slow stuck at 0.203
//	                             16,560 firing minutes
//
// The cause is not the estimator, and that took a second wrong answer to learn.
// A decaying q99 -- which handles a two-humped distribution by construction, as
// the percentile window had -- measured 27,927 firing minutes, WORSE. Because
// the freeze is what fails: at cold start the estimate sits at the idle level,
// so the band sits just above idle, the busy hours are outside it, learning
// freezes, and the estimate can never rise to contain them. ANY estimator that
// stops learning while outside its own band is unable to learn a mode above
// that band. The freeze cannot simply be removed either -- without it a
// sustained fault walks the variance up until the band overtakes the reading and
// the hub reports a recovery for a drive that never cooled.
//
// Twenty-four buckets dissolve the conflict instead of trading it off. Each
// bucket only ever sees ONE mode, because a host that is busy at noon is busy at
// noon every day: the 23:00 bucket sees idle, the 12:00 bucket sees busy, and
// neither is ever outside its own band, so neither ever freezes. Measured the
// same way, 29 and 23 firing minutes over thirty days -- three orders of
// magnitude, and what is left is the minute either side of a mode change while
// `fast` catches up, which OpenFor swallows.
//
// WHAT STILL DOES NOT GO AWAY, because it would be easy to imply otherwise:
//
//   - The warm-up. A seven-day time constant needs about seven days of
//     observation whatever the mechanism. FamilyRule.MinSpan is that
//     requirement as a duration rather than as a sample count.
//   - The fault-versus-new-normal decision. "The drive is cooking" and "this
//     host is busier now" are the same signal, so something has to rule on when
//     a persistent excursion becomes the normal. That is ExcursionSince and one
//     comparison.
//   - The WEEKLY cycle. Monday at 10:00 and Sunday at 10:00 share a bucket, so a
//     weekday-only rhythm is still averaged across both. Capturing it needs 168
//     buckets keyed on weekday as well, which is the same change again and is
//     not made here: 24 removes the dominant cycle, and the residue is a wider
//     band rather than a false condition.

// Time constants.
const (
	// TauFast is the smoothing on "what it is doing now".
	//
	// Two minutes rather than five, and the difference is detection latency. A
	// reading that lands just over the line is approached asymptotically, so
	// fast needs several multiples of Tau to cross it: at five minutes that is
	// fifteen to twenty-five, against the flat three minutes the tick counter
	// this replaced used to take. At two it is six to ten, and a large
	// excursion is faster than that counter ever was.
	TauFast = 2 * time.Minute

	// TauSlow is the smoothing on "what it normally does at this hour", and
	// therefore how long a persistent excursion takes to be accepted as the new
	// normal.
	//
	// Seven days, so each bucket carries about a week of its own hour. Note
	// that this is a week of WALL CLOCK either way: a bucket is advanced once
	// per scrape like everything else, and the decay is computed from the gap
	// since that bucket last saw a reading -- about a minute within an hour,
	// about a day across the gap between one day's hour and the next.
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
// because "how big" and "how long" are not the same question.
//
// So `fast` smooths sensor jitter, and this decides when an excursion has lasted
// long enough to mean something. Three minutes, matching the three consecutive
// passes the in-memory counter this replaced required -- and better than it,
// because a duration measured from ExcursionSince survives a hub restart where
// the counter began again at zero.
//
// It also absorbs the one cost of bucketing: for the minute or two after a mode
// change, `fast` still carries the previous hour's level while the new bucket
// judges against its own, so a subject can read momentarily outside a band it
// belongs well inside.
const OpenFor = 3 * time.Minute

// Buckets is how many hour-of-day buckets each subject carries.
const Buckets = 24

// Bucket is what one hour of the day normally looks like for one subject.
//
// UpdatedTS is per bucket and not a copy of the subject's, because the decay
// has to be measured from the last reading THIS bucket saw. Within an hour that
// is a minute ago; across the day boundary it is twenty-three hours ago, and
// using the subject's own high-water mark would weight the first reading of each
// hour as though no time had passed.
type Bucket struct {
	Slow      float64
	Var       float64
	UpdatedTS time.Time
}

// Seeded reports whether this bucket has any reading in it.
func (b Bucket) Seeded() bool { return !b.UpdatedTS.IsZero() }

// Scale is the size of a meaningful departure, in the metric's own unit.
//
// The floor is not a fudge. A drive in a climate-controlled rack holds 38.0 to
// 38.4 C for a week, so its true sd is 0.1 and a 4-sigma band is 0.4 C: it
// would alarm on the first warm afternoon, correctly by the arithmetic and
// uselessly in fact. The floor is the smallest departure worth a sentence.
func (b Bucket) Scale(floor float64) float64 {
	return math.Max(math.Sqrt(b.Var), floor)
}

// Band is the pair a reading in this hour is judged against, before any
// hardware limit caps it.
//
// Named Band rather than Bounds because Bounds is the struct
// DeviationThresholds returns after the limits have capped this pair, and having
// the two spellings one letter apart invited exactly the mistake of judging
// against the uncapped numbers.
func (b Bucket) Band(floor float64) (warn, crit float64) {
	s := b.Scale(floor)
	return b.Slow + KWarn*s, b.Slow + KCrit*s
}

// EWMA is one subject's moving state: what it is doing now, and what each hour
// of its day normally looks like.
type EWMA struct {
	// Fast is what the subject is doing now, and is NOT bucketed. It is the
	// present tense, not a seasonal expectation -- there is one current reading
	// whatever hour it arrives in.
	Fast float64

	// FirstTS is the oldest reading folded in, which is what the warm-up is
	// measured against. UpdatedTS is the newest.
	FirstTS   time.Time
	UpdatedTS time.Time

	// ExcursionSince is when Fast last left its hour's band, or zero while it
	// is inside. Per subject rather than per bucket: it drives OpenFor, which
	// is a fact about the subject being in trouble and not about an hour.
	ExcursionSince time.Time

	// Hour is the seasonal part, indexed by hour of day.
	//
	// UTC, and that is not a compromise. The cycle repeats every twenty-four
	// hours whichever offset it is labelled in, so a host in CET lands its busy
	// hours in a consistent set of UTC buckets and each still sees one mode.
	// Using the host's local time would mean reading a timezone the hub does not
	// collect, to relabel buckets whose behaviour would not change.
	Hour [Buckets]Bucket
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

// HourOf is the bucket index for an instant.
func HourOf(ts time.Time) int { return ts.UTC().Hour() }

// Bucket returns the state for one hour.
func (e EWMA) Bucket(hour int) Bucket {
	if hour < 0 || hour >= Buckets {
		return Bucket{}
	}
	return e.Hour[hour]
}

// Update folds one reading in, and reports the state after it.
//
// A METHOD ON A VALUE RETURNING A VALUE, so a caller folding sixty buffered
// samples forward is a loop over a pure function and the whole thing is testable
// without a database -- the same shape DiskSeverity and DriveFindings are in.
//
// Decay is computed from the ELAPSED TIME, not per call, and that is the reason
// this can be folded forward at all. An agent buffers an hour by default
// (AGENT_BUFFER_WINDOW) and posts sixty samples at once when it reconnects. A
// per-call decay would weight those sixty as one tick, and the average would
// then track how often the hub looked rather than what the host did.
func (e EWMA) Update(x float64, ts time.Time, rule FamilyRule, lim Limits) EWMA {
	hour := HourOf(ts)

	if !e.Seeded() {
		// The first reading seeds the subject and its own hour, and no other:
		// nothing is yet known about the twenty-three hours not seen. Var stays
		// zero, so Scale falls back to the floor, and the warm-up is what keeps
		// anything from being judged on it.
		next := EWMA{Fast: x, FirstTS: ts, UpdatedTS: ts}
		next.Hour[hour] = Bucket{Slow: x, UpdatedTS: ts}
		return next
	}

	// Out-of-order or duplicate readings are dropped rather than folded
	// backwards. Replay re-delivers samples the state has already seen -- the
	// natural key on the sample tables makes that harmless there, and it must be
	// harmless here too, or a reconnecting agent would fold its whole buffer in
	// twice and halve its own variance.
	if !ts.After(e.UpdatedTS) {
		return e
	}

	next := e
	next.UpdatedTS = ts
	next.Fast += alpha(ts.Sub(e.UpdatedTS), TauFast) * (x - e.Fast)

	b := e.Hour[hour]
	if !b.Seeded() {
		// An hour this subject has not been awake for yet. Seeded from the
		// reading rather than from a neighbouring bucket: borrowing would import
		// the wrong mode exactly at the boundary where the modes differ, which
		// is the whole thing the buckets exist to separate.
		next.Hour[hour] = Bucket{Slow: x, UpdatedTS: ts}
		return next
	}

	// THE FREEZE, and it is load-bearing rather than a refinement.
	//
	// Without it the variance channel recreates the bug the window design's SQL
	// exclusion existed to prevent. A drive sits 14 sigma out and stays there;
	// var chases the squared deviation toward 196, reaching 26 within a day, so
	// sd becomes 5.1, the band becomes slow + 20 against a reading of slow + 14,
	// and the reading is inside its own band. The condition clears and the hub
	// has reported a recovery for a drive that never cooled.
	//
	// Winsorising the update instead was measured and is worse in both
	// directions: clamping the deviation at 4 x scale pins sd near 4 x floor,
	// widening the band to 16 x floor so it clears early anyway, while slowing a
	// legitimate shift to 0.57 units a day -- a 14-unit step would take
	// twenty-five days to be accepted.
	//
	// The band is the CAPPED one, computed the way judgeDeviation computes it,
	// from the bucket as it stood before this reading. When the fold decided
	// against the uncapped band instead, the two disagreed wherever a cap bit:
	// a drivetemp subject at normal 50 with an sd of 4 has an uncapped warn of
	// 62 while the judge caps crit to the 60 ceiling and pulls warn to 58, so a
	// reading of 61 was critical to the judge and unremarkable here. No
	// excursion was recorded, and every condition the ceilings and the published
	// limits exist to raise could not be raised at all.
	w, c := b.Band(rule.Floor)
	if next.Fast > DeviationThresholds(w, c, lim, rule.Ceilings).Warn {
		if next.ExcursionSince.IsZero() {
			next.ExcursionSince = ts
		}
		// ...but an excursion that has outlasted the averaging window itself is
		// not an excursion any more, whatever anyone thinks of it. Seven days at
		// a level IS that level. Learning resumes, the band follows, and the
		// condition clears on its own -- the same ruling the window design spelt
		// as `opened_ts > cutoff`, in one comparison instead of a join.
		//
		// What keeps that safe for a subject genuinely in trouble is the other
		// tier: a drive whose normal has crept to 58 C still meets its published
		// limit, or its family ceiling, and stays critical on that.
		if ts.Sub(next.ExcursionSince) < TauSlow {
			return next
		}
	} else {
		next.ExcursionSince = time.Time{}
	}

	a := alpha(ts.Sub(b.UpdatedTS), TauSlow)
	d := x - b.Slow
	nb := Bucket{
		Slow:      b.Slow + a*d,
		Var:       b.Var + a*(d*d-b.Var),
		UpdatedTS: ts,
	}
	if nb.Var < 0 {
		// Arithmetic only, not a reachable state: a is in (0,1) so the update
		// cannot go negative mathematically. Floored anyway because Var feeds a
		// square root, and a NaN threshold would judge every reading as healthy
		// -- failing silent, which is the one direction this must not fail.
		nb.Var = 0
	}
	next.Hour[hour] = nb
	return next
}

// alpha is the weight one reading carries, given the gap since the last.
//
// 1 - exp(-dt/tau) rather than a fixed fraction, which is what makes the average
// a function of TIME rather than of how many times it was called. A gap of a
// whole tau carries 63% of the weight; a gap of one minute against a seven-day
// tau carries 0.0099%.
func alpha(dt, tau time.Duration) float64 {
	if dt <= 0 {
		return 0
	}
	return 1 - math.Exp(-dt.Seconds()/tau.Seconds())
}
