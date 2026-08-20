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
	// this tier: "" for raw, "_5m" and "_1h" for the continuous aggregates.
	suffix string
	// tsColumn is the time column of that relation. The aggregates call it
	// bucket, not ts, because it is the left edge of a bucket rather than the
	// instant of a reading.
	tsColumn string
	// step is the distance between consecutive points.
	step time.Duration
	// lag is the end_offset of this tier's refresh policy: how far back a
	// refresh RUN reaches. Every continuous aggregate in 0001_init.sql is
	// materialized_only = true (the TimescaleDB default since 2.13), so a
	// query past the materialised horizon returns nothing rather than falling
	// through to the raw table. Zero for raw tiers, which have no
	// materialisation step.
	lag time.Duration
	// refreshEvery is the schedule_interval of that same policy: how often a
	// run happens, and therefore how stale end_offset's reach can be.
	//
	// It is a separate field rather than folded into lag because every number
	// in this struct mirrors exactly ONE value in 0001_init.sql, which is
	// what lets TestIntegrationTierSpecsMatchTheSchema pin each of them
	// against timescaledb_information. A single combined constant would drift
	// silently the next time either half of the policy changed.
	//
	// The two are added at the point of use -- see materialisedThrough. Zero
	// for raw tiers, which have no policy.
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

// The three resolutions every rolled-up family carries, fine to coarse.
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
)

// rolledUpTiers is the trio shared by every family that has continuous
// aggregates. Ordered fine to coarse; selection depends on that order.
var rolledUpTiers = []tierSpec{rawTier, fiveMinuteTier, hourlyTier}

// smartTiers is the one raw-only family (spec 5.3).
//
// Not an omission: SMART is read hourly, so a 5-minute bucket would hold at
// most one reading and restate the raw table at triple the storage. Pinned by
// TestIntegrationRawOnlyTablesHaveNoContinuousAggregates.
var smartTiers = []tierSpec{{
	name: TierRaw, suffix: "", tsColumn: "ts",
	step: time.Hour, lag: 0, retention: 90 * 24 * time.Hour,
}}

// materialisedThrough is the newest bucket boundary this tier can be relied on
// to have written, given the clock.
//
// end_offset is how far back a refresh run reaches; schedule_interval is how
// long it can be since the last run. Between runs, everything newer than the
// last one is missing, so the horizon is the SUM -- reading end_offset alone
// left every 5m and 1h window ending on buckets no run had written yet.
//
// Truncated to the step because an aggregate materialises whole buckets. The
// client lays the answer on a grid of step-wide slots (seriesOnGrid), so a
// horizon in the middle of a bucket becomes a slot nothing can fill -- and
// every headline value on a page reads the LAST slot, which is the rule that
// stops a dead host reporting its final rate as current. Truncate aligns to
// the epoch, which is where time_bucket() puts its boundaries too.
func (s tierSpec) materialisedThrough(now time.Time) time.Time {
	return now.Add(-(s.lag + s.refreshEvery)).Truncate(s.step)
}

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
	// the leading edge and by materialisation lag on the trailing one. The
	// SQL is bounded by this and not by Requested, which is what makes a gap
	// inside it mean "the host reported nothing" rather than "the hub lagged".
	Window Window
	// Requested is the window as asked, so a caller can see every clamp
	// rather than infer it.
	Requested Window
	// Warnings names each clamp in words. Empty when nothing moved.
	Warnings []string
	// Empty reports that the clamps left no window at all -- asking for the
	// last five minutes at the 1h tier, which materialises an hour behind.
	// The response is a valid 200 with no points; it is a real answer, not an
	// error.
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
// The rule is RETENTION ON from, not span. Span alone gets the raw-only
// families wrong in both directions: family=smart over sixty days must answer
// raw, because there is no 5m tier to fall back to, and family=process can
// never cover more than forty-eight hours however long a range is asked for.
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
		p.Warnings = append(p.Warnings, "to was in the future and was clamped to now")
		if !from.Before(to) {
			return Plan{}, fmt.Errorf("%w: the requested window lies entirely in the future", ErrInvalid)
		}
	}

	p.spec = selectTier(fam.tiers, from, step, stepSet, now)
	p.Tier = p.spec.name
	p.Step = p.spec.step

	// Leading edge: everything before this has been dropped by the retention
	// policy. Returning the part that survives, and saying so, beats both
	// erroring and silently starting the series late.
	horizon := now.Add(-p.spec.retention)
	if from.Before(horizon) {
		from = horizon
		p.Warnings = append(p.Warnings, fmt.Sprintf(
			"from predates the %s tier's %s retention; the window starts at the oldest data that still exists",
			p.spec.name, humanDuration(p.spec.retention)))
	}

	// Trailing edge: the aggregates are materialized_only, so a query past
	// the refresh policy's end_offset returns nothing at all rather than
	// falling through to the raw rows behind it.
	if p.spec.lag > 0 {
		// Truncated to a whole bucket, not left at the horizon itself.
		//
		// An aggregate materialises whole buckets. now-lag almost always
		// lands in the middle of one, and a window ending mid-bucket asks
		// for a bucket that does not exist yet: the client puts the answer
		// on a grid of step-wide slots (seriesOnGrid), so that half-bucket
		// becomes a whole trailing slot nothing can ever fill.
		//
		// Every headline value on the page reads the LAST slot -- that is
		// the rule that stops a dead host reporting its final rate as
		// current -- so an unfillable slot meant every 5m and 1h chart
		// showed its trend correctly and its number as absent. The fleet's
		// traffic, the fleet-traffic tile and the host page's limits meters
		// all read "—" together on a host that was reporting perfectly.
		//
		// Truncate aligns to the epoch, which is where time_bucket() puts
		// its boundaries too, so this lands on a real bucket edge rather
		// than one derived from the request's own timing.
		fresh := p.spec.materialisedThrough(now)
		if to.After(fresh) {
			to = fresh
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"the %s tier materialises %s behind now; the window ends at the last whole bucket before that",
				p.spec.name, humanDuration(p.spec.lag+p.spec.refreshEvery)))
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

	// Older than every tier. The coarsest is all there is, and the leading
	// clamp above will say how much of the request it covers.
	return tiers[len(tiers)-1]
}

// humanDuration renders the handful of intervals that appear in warnings the
// way the migration writes them -- "7 days", "48 hours", "10 minutes" --
// rather than the way Go does, which would put "168h0m0s" in a message meant
// for a person.
//
// The one-week floor on the day unit is what keeps a retention the migrations
// declare in hours reading back as "48 hours" rather than "2 days": a warning
// that renames the interval makes it harder to find the line that set it.
func humanDuration(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0 && d >= 7*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/time.Minute), "minute")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
