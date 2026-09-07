package read

import (
	"fmt"
	"time"
)

// Tier names, as they appear in a /metrics response.
const (
	TierRaw = "raw"
	Tier5m  = "5m"
	Tier1h  = "1h"
	Tier1d  = "1d"
)

// tierSpec is one storage resolution of one family.
//
// step, lag and retention all mirror 0001_init.sql, and none of them is
// inferable from the others. TestIntegrationTierSpecsMatchTheSchema pins every
// one of them against timescaledb_information, so editing the migration
// without editing this table fails the build rather than silently returning a
// window the hub cannot actually answer.
type tierSpec struct {
	// name is what the response reports as "tier".
	name string
	// suffix is appended to the family's raw table to name the view holding
	// this tier: "" for raw, "_5m", "_1h" and "_1d" for the continuous
	// aggregates.
	suffix string
	// tsColumn is the time column of that relation. The aggregates call it
	// bucket, not ts, because it is the left edge of a bucket rather than the
	// instant of a reading.
	tsColumn string
	// step is the distance between consecutive points.
	step time.Duration
	// lag is the end_offset of this tier's refresh policy: how far back a
	// refresh RUN reaches. Zero for raw tiers, which have no materialisation
	// step.
	//
	// It used to clamp the answered window. It no longer does, and the reason
	// is 0014_realtime_aggregates.sql: every aggregate is now a REAL-TIME
	// aggregate, so the view unions the materialised rows with a live query
	// over everything past the watermark and a question about the last five
	// minutes gets an answer instead of nothing. What lag and refreshEvery
	// still describe is how wide that live tail is -- how much the query
	// computes on the fly rather than reads -- which is why both are still
	// pinned against timescaledb_information by
	// TestIntegrationTierSpecsMatchTheSchema. A policy that quietly grew its
	// end_offset would make every query on this tier scan further back, and
	// nothing else in the system would say so.
	lag time.Duration
	// refreshEvery is the schedule_interval of that same policy: how often a
	// run happens, and therefore how far past end_offset the live tail can
	// reach between runs.
	//
	// It is a separate field rather than folded into lag because every number
	// in this struct mirrors exactly ONE value in 0001_init.sql, which is
	// what lets TestIntegrationTierSpecsMatchTheSchema pin each of them
	// against timescaledb_information. A single combined constant would drift
	// silently the next time either half of the policy changed.
	refreshEvery time.Duration
	// retention is the interval of this tier's retention policy: data older
	// than now - retention has been dropped.
	//
	// drop_chunks removes a chunk only once its NEWEST row is past the cutoff,
	// so slightly older data often survives. This is deliberately the
	// GUARANTEED window rather than the observed one -- promising the
	// overshoot would make the response's window depend on chunk boundaries.
	retention time.Duration
}

// The four resolutions every rolled-up family carries, fine to coarse.
//
// A tier is not a fixed global: the raw-only family below has its own raw spec
// with a different retention, which is the whole reason tier selection is
// per-family rather than a lookup on the range alone.
var (
	rawTier = tierSpec{
		name: TierRaw, suffix: "", tsColumn: "ts",
		step: time.Minute, lag: 0, retention: 7 * 24 * time.Hour,
	}
	fiveMinuteTier = tierSpec{
		name: Tier5m, suffix: "_5m", tsColumn: "bucket",
		step: 5 * time.Minute, lag: 10 * time.Minute,
		refreshEvery: 5 * time.Minute, retention: 30 * 24 * time.Hour,
	}
	hourlyTier = tierSpec{
		name: Tier1h, suffix: "_1h", tsColumn: "bucket",
		step: time.Hour, lag: time.Hour,
		refreshEvery: 30 * time.Minute, retention: 90 * 24 * time.Hour,
	}
	// The tier a question about a YEAR resolves to.
	//
	// It exists because the ladder used to stop at 90 days, so the widest
	// window anything could answer was three months -- and the 1h tier
	// answers three months with 2160 points, which is a chart nobody can read
	// and a response nobody wants to send. A day is the bucket that question
	// wants: twelve months is 365 points, the same order as the 30d chart
	// draws today.
	//
	// 400 days rather than a round 365: the widest window offered is 12
	// months, and a retention equal to the window puts the far edge of that
	// window on the chunk being dropped. The overshoot is five weeks of daily
	// rows -- 1/24 of what an hour costs -- beside the 90 days of hourly the
	// tier above already keeps.
	//
	// Today is deliberately left outside the answered window. The tier is
	// real-time now, so the day the clock is inside CAN be computed -- and a
	// day that is four hours old is not a day-shaped reading, while the LAST
	// point on a chart is what every headline value reads. planQuery's open-
	// bucket floor is what excludes it, at every tier and for the same
	// reason.
	dailyTier = tierSpec{
		name: Tier1d, suffix: "_1d", tsColumn: "bucket",
		step: 24 * time.Hour, lag: 2 * time.Hour,
		refreshEvery: time.Hour, retention: 400 * 24 * time.Hour,
	}
)

// rolledUpTiers is the ladder shared by every family that has continuous
// aggregates. Ordered fine to coarse; selection depends on that order.
var rolledUpTiers = []tierSpec{rawTier, fiveMinuteTier, hourlyTier, dailyTier}

// smartTiers is the one raw-only family (spec 5.3).
//
// Not an omission: SMART is read hourly, so a 5-minute bucket would hold at
// most one reading and restate the raw table at triple the storage. Pinned by
// TestIntegrationRawOnlyTablesHaveNoContinuousAggregates.
var smartTiers = []tierSpec{{
	name: TierRaw, suffix: "", tsColumn: "ts",
	step: time.Hour, lag: 0, retention: 90 * 24 * time.Hour,
}}

// Window is a half-open-in-spirit time range carried in a /metrics response.
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Plan is the outcome of tier selection: which relation answers the query,
// over which window, and every way that window differs from what was asked.
type Plan struct {
	// Family is the requested family, echoed so a response is
	// self-describing.
	Family string
	// Tier is which resolution answered. A client that ignores it cannot
	// silently mix resolutions anyway -- the column names differ per tier by
	// construction, so busy at raw becomes busy_avg and busy_max at 5m -- but
	// it is what a chart legend should say.
	Tier string
	// Step is the distance between points at the chosen tier. It is always
	// the tier's real step, never the step the caller asked for.
	Step time.Duration
	// Window is what the response ACTUALLY covers, clamped by retention on
	// the leading edge and by the open bucket on the trailing one. The SQL is
	// bounded by this and not by Requested, which is what makes a gap inside
	// it mean "the host reported nothing" rather than "the hub lagged".
	Window Window
	// Requested is the window as asked, so a caller can see every clamp
	// rather than infer it.
	Requested Window
	// Warnings carries what the caller cannot see for themselves: columns
	// asked for that this tier does not have, and a truncated result.
	//
	// NOT the window clamps. Those used to state themselves here and no
	// longer do: Window and Requested above are the machine-readable answer
	// to "what did you actually give me", and the sentences on top of them
	// named storage tiers at a reader who had only ever picked a range.
	Warnings []string
	// Empty reports that the clamps left no window at all -- asking for the
	// last five minutes with step=1h, where the only closed hourly bucket is
	// already behind `from`. The response is a valid 200 with no points; it is
	// a real answer, not an error.
	Empty bool

	spec tierSpec
	fam  *family
}

// defaultSpan is the window used when neither from nor to is given. An hour of
// raw samples is what a "how is this host right now" view wants and is cheap
// at every tier.
const defaultSpan = time.Hour

// planQuery selects the tier and computes the window. It is a pure function of
// its arguments -- no database, no clock of its own -- so every boundary in
// the table below is an ordinary unit test rather than something that needs
// data at a particular age to exist.
//
// The rule is RETENTION ON from, not span. Span alone gets the raw-only family
// wrong: family=smart over sixty days must answer raw, because there is no 5m
// tier to fall back to.
func planQuery(fam *family, req Window, step time.Duration, stepSet bool, now time.Time) (Plan, error) {
	to, from := req.To, req.From
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-defaultSpan)
	}
	requested := Window{From: from, To: to}

	if !from.Before(to) {
		return Plan{}, fmt.Errorf("%w: from must be before to", ErrInvalid)
	}

	p := Plan{Family: fam.name, Requested: requested, fam: fam}

	// A to in the future is not an error -- a dashboard asking for "now"
	// against a hub whose clock is a second behind should not 400 -- but it
	// cannot be answered past the present either.
	if to.After(now) {
		to = now
		if !from.Before(to) {
			return Plan{}, fmt.Errorf("%w: the requested window lies entirely in the future", ErrInvalid)
		}
	}

	p.spec = selectTier(fam.tiers, from, step, stepSet, now)
	p.Tier = p.spec.name
	p.Step = p.spec.step

	// Leading edge: everything before this has been dropped by the retention
	// policy. Returning the part that survives beats both erroring and
	// silently starting the series late.
	//
	// Unannounced, deliberately. This used to append a warning naming the
	// tier and its retention, and the chart already says it better than the
	// sentence did: the line starts where the data starts and the time axis
	// underneath gives the date. A sentence restating a visible fact in
	// storage vocabulary the reader has never seen -- they picked "30d", not
	// a tier -- is noise on top of a picture that was already clear.
	horizon := now.Add(-p.spec.retention)
	if from.Before(horizon) {
		from = horizon
	}

	// There is no trailing clamp any more, and that is the whole point of
	// 0014_realtime_aggregates.sql.
	//
	// There was one. Every aggregate was materialized_only, so a query past
	// the refresh policy's end_offset returned nothing at all rather than
	// falling through to the rows behind it, and this clamped `to` back to
	// now - (end_offset + schedule_interval): fifteen minutes at the 5m tier,
	// ninety at the 1h tier, three hours at 1d. Only the 1h range, which reads
	// the raw table, was ever live. A saturation on an interface was therefore
	// absent from the 24h chart for a quarter of an hour and then appeared --
	// which is exactly what an operator reported, and is the difference
	// against rrdtool, whose file is written at poll time and read on the next
	// page load.
	//
	// The aggregates are real-time now: the view unions its materialised rows
	// with a live query over everything past the watermark, all the way down
	// the hierarchy, so every tier answers to the present and the clamp has
	// nothing left to protect against.
	//
	// One clamp survives, and it is much smaller than the one it replaces:
	// the OPEN bucket is still excluded.
	//
	// A real-time aggregate will happily compute the bucket the clock is
	// currently inside, from however few samples have landed in it. Drawing
	// that is not freshness, it is a last point built from a fifth of the
	// readings its neighbours were built from -- and every headline figure on
	// a page reads the last point. rrdtool does not draw its unfinished CDP
	// either, so this is also what the reference does.
	//
	// The cost is bounded by ONE bucket rather than by a policy: at the 5m
	// tier the newest reading now covers a window that ended between zero and
	// five minutes ago, against fifteen to twenty before. The 1h tier gains an
	// hour and the 1d tier gains two.
	//
	// Written as a floor on `to` rather than as an unconditional step back,
	// because a request for a window that ENDED hours ago is asking about
	// closed buckets already and must not lose its last one.
	if p.spec.tsColumn == "bucket" {
		if lastClosed := now.Truncate(p.spec.step).Add(-p.spec.step); to.After(lastClosed) {
			to = lastClosed
		}
	}

	// Both edges onto bucket boundaries, for a relation that stores whole
	// buckets.
	//
	// It is not enough to align the trailing edge. The client lays the answer
	// on a grid anchored at `from` (seriesOnGrid), so an unaligned `from`
	// offsets every slot from the boundaries time_bucket() actually used, and
	// the span stops being a whole number of buckets -- which puts the
	// unfillable trailing slot straight back. Aligning both edges makes one
	// slot mean exactly one bucket.
	//
	// `from` moves EARLIER, never later, so this cannot hide a bucket the
	// caller asked for. It can reach at most one bucket back past the
	// retention horizon, where there are simply no rows -- a gap at the far
	// left edge, which is true.
	//
	// Raw is excluded by construction: it stores samples at their own
	// timestamps rather than in buckets, so there are no boundaries to align
	// to and the caller's window is already exactly what it asked for.
	if p.spec.tsColumn == "bucket" {
		from = from.Truncate(p.spec.step)
		to = to.Truncate(p.spec.step)
	}

	if !from.Before(to) {
		// Every clamp fired and nothing is left -- the last five minutes at a
		// tier that lags an hour. A 200 with no points and a warning already
		// present is the honest answer.
		p.Empty = true
		to = from
	}

	p.Window = Window{From: from, To: to}
	return p, nil
}

// selectTier picks the relation that answers the query.
//
// tiers is ordered fine to coarse and is never empty.
func selectTier(tiers []tierSpec, from time.Time, step time.Duration, stepSet bool, now time.Time) tierSpec {
	if stepSet {
		// The coarsest tier at or below the requested step, so step=10m
		// resolves to the 5m tier rather than 400ing on a value that is not
		// itself a tier. Below the finest tier's step there is nothing
		// finer to give, so the finest is the answer -- clamping beats an
		// error for a caller who simply asked for more resolution than
		// exists.
		chosen := tiers[0]
		for _, t := range tiers {
			if t.step <= step {
				chosen = t
			}
		}
		return chosen
	}

	// The finest tier whose retention still covers the START of the range.
	// from, not the span: a six-day range that begins sixty days ago fits in
	// no raw table however short it is.
	for _, t := range tiers {
		if now.Sub(from) <= t.retention {
			return t
		}
	}

	// Older than every tier. The coarsest is all there is, and the response's
	// own window says how much of the request it covers.
	return tiers[len(tiers)-1]
}
