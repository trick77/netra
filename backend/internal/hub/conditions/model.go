package conditions

import (
	"math"
	"time"
)

// Key identifies one condition: a kind, about one subject, on one host.
//
// Subject is finer than the UI displays. The fleet page collapses a host's
// drives into a single row, and that is a RENDERING decision -- if the state
// machine collapsed too, the condition would open and close every time the
// fullest mount changed from /var to /mnt.
//
// Table: host_conditions
type Key struct {
	HostID  int32
	Kind    string
	Subject string
}

// Open is a condition currently recorded as open, as read back from the store.
//
// Table: host_conditions
type Open struct {
	ID           int64
	Key          Key
	Severity     string
	MissingTicks int
}

// Bucket is what one hour of the day normally looks like for one subject.
//
// Table: metric_ewma_hour
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
//
// Table: metric_ewma
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

// Bucket returns the state for one hour.
func (e EWMA) Bucket(hour int) Bucket {
	if hour < 0 || hour >= Buckets {
		return Bucket{}
	}
	return e.Hour[hour]
}
