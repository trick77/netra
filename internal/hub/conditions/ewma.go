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
// Twenty-four buckets dissolve the conflict instead of trading it off. A host
// that is busy at noon is busy at noon every day, so the 23:00 bucket sees idle,
// the 12:00 bucket sees busy, and neither is ever outside its own band. Measured
// the same way, 29 and 23 firing minutes over thirty days -- three orders of
// magnitude.
//
// That first cut only held for a day that begins on the hour, and review caught
// it: a busy period starting at 08:15 put 1,305 minutes back, because the 08:00
// bucket seeded on its idle quarter, froze the moment `fast` crossed into the
// busy three quarters, and never learned them. Two more pieces make a MIXED hour
// work -- a per-bucket warm-up during which nothing freezes (WarmWeight), and a
// running fraction of visits spent outside the band that tells a recurring mode
// from a fault (ModeFraction). Measured across mode changes at :00, :15, :30 and
// :45: 29, 29, 42 and 89 firing minutes. What is left is the minute either side
// of a mode change while `fast` catches up, which OpenFor swallows.
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
	// Seven days, so each bucket carries about a week of its own hour. Within
	// a bucket that week is counted in observations rather than in wall clock
	// -- see BucketAlpha for why -- but a full daily visit still weighs what a
	// day of wall clock would, so the constant means the same thing in both.
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

// BucketAlpha is the weight ONE READING carries in its hour's bucket, and it is
// a constant rather than a function of elapsed time.
//
// This is the one place the moving average deliberately does not decay by wall
// clock, and the reason was measured. A bucket sees its hour once a day: sixty
// readings a minute apart, then a twenty-three-hour gap. Time-based decay gave
// the first reading after the gap alpha(23h, 7d) = 0.128 and the other fifty-nine
// together about 0.006 -- so the :00 sample carried twenty-two times the rest of
// the hour combined, the bucket was an average of one reading per day rather
// than of the hour, and its variance was the day-to-day spread of that single
// reading. A glitchy 64 C at 12:00:00 on a 44 C drive moved that hour's normal
// by 2.6 C and its sd to 7 for the following week; a subject that swings within
// the hour but is steady at :00 kept sd at the floor and fired.
//
// Every reading of an hour is instead one observation of that hour, weighted as
// a sixtieth of a day against TauSlow. A full visit still totals about 0.13 --
// the same daily decay as before, spread across the hour rather than dropped on
// its first minute. A reconnecting agent's buffered hour folds in as sixty
// observations exactly as a live hour does, because the fold advances from its
// high-water mark and every reading reaches here.
var BucketAlpha = alpha(24*ScrapeInterval, TauSlow)

// WarmWeight is how much observation a bucket needs before its band is trusted,
// either to judge against or to freeze on.
//
// About two and a half days of visits. Below it the bucket learns everything
// unconditionally, because a band drawn from a day's worth of one hour is not a
// basis for calling anything a fault -- and a mixed hour, one where the busy
// period starts at 08:30, would otherwise seed on its idle half, freeze the
// moment `fast` crossed into its busy half, and never learn the busy half at
// all. Measured: 1,305 firing minutes over thirty days for a mode change at
// 08:15, against 29 for one at 08:00 -- the every-morning condition returning
// for any host whose day does not begin on the hour.
const WarmWeight = 0.30

// ModeFraction is the share of a bucket's observations that may fall outside
// its band before the excursion is taken to be a MODE of that hour rather than
// a fault, and learning resumes.
//
// This is the recurring-pattern half of the fault-versus-new-normal decision.
// The per-subject rule -- an excursion that lasts longer than TauSlow is the
// new normal -- only sees CONTINUOUS excursions, and a recurring one never is:
// a cron job at 03:15 is outside the 03:00 band for thirty minutes and back
// inside for the rest of the day, so the subject's clock resets every morning
// and the bucket freezes on it forever. Counted as a fraction of the bucket's
// own visits it is 50%, a fault is 100%, and an ordinary hour is 0%.
//
// A tenth, because a mode has to be allowed to be small. A busy period starting
// at 08:45 occupies a quarter of the 08:00 bucket; a threshold of a quarter had
// that bucket converging to exactly the line and never crossing it, measured at
// 378 firing minutes. At a tenth, 89. The cost is that a genuine fault stops
// being frozen out once it has been present for about a tenth of the window --
// three quarters of a day -- after which the mean absorbs it over TauSlow as it
// would have anyway, and the hardware limit and family ceiling hold a subject
// that is absolutely too hot regardless.
const ModeFraction = 0.10

// ModeMaxLength is how long a single continuous excursion may run and still be
// taken for a recurring mode rather than a fault.
//
// THE DISCRIMINATOR THE FRACTION ALONE DOES NOT HAVE. A fault crosses
// ModeFraction after about three quarters of a day exactly as a mode does, and
// once it did the bucket learned it: measured, a drive that went from 44 C to
// 58 C and stayed had its band overtake the reading inside three days -- the
// self-erasing failure again, at a slower rate. But a mode and a fault differ in
// SHAPE, not only in share. A mode is bucket-local: the busy quarter of the
// 08:00 hour lasts thirty minutes and then the 09:00 bucket, whose whole visit
// is busy, takes over and reads the same level as inside its own band. A fault
// is subject-global: it runs through every bucket for hours or days. So the
// subject's continuous excursion clock -- which resets the moment any bucket
// reads the level as normal -- is short for a mode and long for a fault, and a
// bucket may only learn its recurring excursion while that clock is short.
//
// Ninety minutes: longer than any excursion a mixed hour can produce, since the
// next bucket resolves it at the hour boundary, and far shorter than the day it
// takes a fault to cross ModeFraction. A fault is then frozen out until the
// subject-level TauSlow escape, which is the seven days the design promises.
const ModeMaxLength = 90 * time.Minute

// Bucket is what one hour of the day normally looks like for one subject.
type Bucket struct {
	Slow float64
	Var  float64

	// Weight is how much observation this bucket rests on, approaching one.
	// It is the bucket's own warm-up, and also its seed flag: zero is a bucket
	// no reading has reached.
	Weight float64

	// Exc is the fraction of this bucket's observations that found `fast`
	// outside the bucket's band, decayed at the same rate as everything else.
	// See ModeFraction.
	Exc float64
}

// Seeded reports whether this bucket has any reading in it.
func (b Bucket) Seeded() bool { return b.Weight > 0 }

// Ready reports whether this bucket has enough history for its band to mean
// anything, which gates both judging against it and freezing on it.
func (b Bucket) Ready() bool { return b.Weight >= WarmWeight }

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
		// zero, so Scale falls back to the floor, and the bucket's own warm-up
		// is what keeps anything from being judged on it.
		next := EWMA{Fast: x, FirstTS: ts, UpdatedTS: ts}
		next.Hour[hour] = Bucket{Slow: x, Weight: BucketAlpha}
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
		//
		// The subject-level excursion is NOT cleared here, and that is the fix
		// for a real bug: a drive at 61 C with a condition open, on a host
		// normally off overnight, gets booted at 03:00. The 03:00 bucket seeds
		// at 61, and if the excursion were reset the next scan would find `fast`
		// inside a band centred on the fault and file the subject healthy --
		// a miss against the open condition, and a second such hour closes it
		// as a recovery. Left as it was, the judge sees an unready bucket and
		// files the subject unjudged, which leaves the condition alone.
		next.Hour[hour] = Bucket{Slow: x, Weight: BucketAlpha}
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
	outside := next.Fast > DeviationThresholds(w, c, lim, rule.Ceilings).Warn
	if outside {
		if next.ExcursionSince.IsZero() {
			next.ExcursionSince = ts
		}
	} else {
		next.ExcursionSince = time.Time{}
	}

	// WHEN THE BUCKET LEARNS, and it is four conditions rather than one
	// because four different things have to be allowed through the freeze.
	//
	//  1. A bucket that is not yet Ready learns everything. Its band is drawn
	//     from too little to call anything a fault, and a bucket seeded on the
	//     idle half of a mixed hour would otherwise freeze the moment `fast`
	//     crossed into the busy half, and never learn it. See WarmWeight.
	//
	//  2. A RECURRING excursion is a mode of that hour, not a fault, and the
	//     bucket learns it. Recurring means two things at once: outside its
	//     band for more than ModeFraction of its visits -- a cron job at 03:15
	//     is outside for half of every 03:00 visit, forever -- AND the
	//     subject's current continuous excursion is shorter than
	//     ModeMaxLength, because a mode is resolved by the next bucket at the
	//     hour boundary while a fault runs on for days. Either alone is wrong:
	//     the fraction alone let a sustained fault through after three quarters
	//     of a day, and the subject-level clock alone resets every morning and
	//     never sees a recurring pattern at all. Measured: a new 03:15 job is
	//     learned in three days; a drive that went to 58 C and stayed is held.
	//
	//  3. An excursion that has outlasted TauSlow is the new normal, whatever
	//     anyone thinks of it. Seven days at a level IS that level, and the
	//     condition clears on its own -- the same ruling the window design spelt
	//     as `opened_ts > cutoff`, in one comparison instead of a join. What
	//     keeps that safe for a subject genuinely in trouble is the other tier:
	//     a drive whose normal has crept to 58 C still meets its published limit
	//     or its family ceiling, and stays critical on that.
	//
	//  4. Inside the band, it simply learns.
	//
	// A continuous fault fails all four for as long as it has to: it is
	// outside, the bucket is ready, its excursion is hours old rather than
	// minutes, and it is younger than a week. It is frozen out for that week.
	nb := b
	excursion := ts.Sub(next.ExcursionSince)
	recurring := b.Exc > ModeFraction && excursion < ModeMaxLength
	accepted := excursion >= TauSlow
	if !b.Ready() || recurring || accepted || !outside {
		d := x - b.Slow
		nb.Slow += BucketAlpha * d
		nb.Var += BucketAlpha * (d*d - b.Var)
		if nb.Var < 0 {
			// Arithmetic only, not a reachable state: alpha is in (0,1) so the
			// update cannot go negative mathematically. Floored anyway because
			// Var feeds a square root, and a NaN threshold would judge every
			// reading as healthy -- failing silent, the one direction this
			// must not fail.
			nb.Var = 0
		}
	}

	// The fraction and the weight advance on EVERY observation, learned or
	// frozen. The fraction has to keep counting while frozen or it could never
	// reach the threshold that unfreezes it; the weight is how much the bucket
	// has seen, which a frozen reading still is.
	seen := 0.0
	if outside {
		seen = 1
	}
	nb.Exc += BucketAlpha * (seen - b.Exc)
	nb.Weight += BucketAlpha * (1 - b.Weight)
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
