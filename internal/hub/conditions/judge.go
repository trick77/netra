package conditions

import "time"

// DeviationInput is one subject's reading and everything needed to judge it.
//
// Pointers where absence is a fact, because each absence means something
// different: no reading is a subject that did not report, an unseeded state is
// a subject not yet watched, and a NULL column is a kernel that does not
// publish the metric. Collapsing any of them to a zero would judge a host
// against a number nobody measured.
type DeviationInput struct {
	Value     *float64
	ReadingTS *time.Time
	State     EWMA
	// IsOpen is whether this subject already has a condition open.
	//
	// The OpenFor gate below must not apply to it. The in-memory counter this
	// replaced skipped open keys explicitly -- it decided when to start
	// looking, never whether to keep looking -- and without that a signal
	// flapping around its warn line resolves and reopens forever: a tick
	// inside the band counts one miss, the tick back outside restamps
	// excursion_since and files the subject unjudged for three minutes, and
	// Diff leaves the miss counter untouched through all of it. So the next dip
	// reaches clearAfter, the condition closes, and it reopens minutes later
	// with a fresh onset -- the transition-pair spam this whole engine exists
	// to avoid.
	IsOpen bool
	Rule   FamilyRule
	Limits Limits
	Detail map[string]any
}

// JudgeDeviation applies the rule to one subject and files it under Seen,
// Unjudged or Bad.
//
// THE THREE-STATE DISCIPLINE IS THE WHOLE FUNCTION. "Not over the line" and
// "cannot say" are different answers, and only the first may clear a
// condition. A subject with no reading this pass, or with no baseline yet, has
// not been found healthy -- it has not been looked at, and Scan.Unjudged is
// the state that says so. Writing either into Seen would let a sensor that
// stopped reporting, or one whose baseline was rebuilt, close a live condition
// as a recovery that never happened.
func JudgeDeviation(scan *Scan, key Key, in DeviationInput) {
	// No reading, or a metric this kernel does not publish.
	if in.Value == nil || in.ReadingTS == nil {
		scan.Unjudged[key] = true
		return
	}

	// Not watched long enough. A subject netra has only just started averaging
	// is not a subject netra has found to be fine.
	//
	// A SPAN, not a sample count, and the difference is the point of the
	// change: it tolerates gaps completely, so a host that drops scrapes is
	// judged on the same footing as one that never does, and the constant says
	// what it means. See FamilyRule.MinSpan.
	if !in.State.Seeded() || in.State.Span() < in.Rule.MinSpan {
		scan.Unjudged[key] = true
		return
	}

	// The average has to be as current as the reading being judged against it.
	//
	// foldLimit bounds one pass, so a fleet catching up after the migration --
	// or after a long outage -- has state that lags the newest sample by hours.
	// Judging then compares today's reading against last week's normal and
	// stamps the finding OpenedTS = now, so a week-old excursion is reported as
	// having just started, with a `value` from today and a `smoothed` from
	// whenever the fold last reached. Unjudged until the fold catches up, which
	// is the honest answer and self-clearing.
	if in.ReadingTS.Sub(in.State.UpdatedTS) > OpenFor {
		scan.Unjudged[key] = true
		return
	}

	// The bucket for the hour this reading was taken in. An hour the subject
	// has not been observed through yet is UNJUDGED: a state seeded yesterday
	// afternoon knows nothing about this morning, and judging against an empty
	// bucket would compare the reading against zero.
	bucket := in.State.Bucket(HourOf(*in.ReadingTS))
	if !bucket.Ready() {
		// Unseeded, or seeded on too little. An hour the subject has not been
		// observed through enough is not one it can be judged in -- and this is
		// also what keeps a bucket that was seeded ON a fault from clearing the
		// condition: a drive at 61 C on a host booted at 03:00 for the first
		// time seeds the 03:00 bucket at 61, and judging against that band
		// would file the subject healthy, which is a miss against its open
		// condition. Unjudged leaves the condition alone.
		scan.Unjudged[key] = true
		return
	}

	scan.Seen[key] = true

	warn, crit := bucket.Band(in.Rule.Floor)
	bounds := DeviationThresholds(warn, crit, in.Limits, in.Rule.Ceilings)

	// Fast, not the raw reading: the smoothing keeps sensor jitter and a single
	// odd sample out of the judgement.
	severity := DeviationSeverity(in.State.Fast, bounds)
	if severity == "" {
		return
	}

	// Outside the band, but not for long enough to mean anything yet.
	//
	// UNJUDGED RATHER THAN SEEN, and the distinction is the one the old
	// in-memory counter had to be told about explicitly. Filing a brief
	// excursion as healthy would count as a MISS against any condition already
	// open on this subject, and two misses close it -- so a subject genuinely
	// in trouble would be opened, closed and reopened forever, losing its onset
	// each time. "Not for long enough to say" is exactly the third state.
	//
	// A new subject simply does not open, which is the point: a nightly cron
	// burst writes nothing at all.
	//
	// NOT FOR A SUBJECT ALREADY OPEN: this decides when to start looking, never
	// whether to keep looking. See DeviationInput.IsOpen for what applying it
	// to an open condition costs.
	//
	// The IsZero check is not redundant with the comparison beside it, and
	// leaving it out is a silent failure rather than a loud one: Sub against a
	// zero timestamp is an enormous duration, which passes OpenFor and opens
	// the condition on its very first reading with no suppression whatever.
	// Fail closed, so a state the fold has not marked cannot be raised.
	if !in.IsOpen && (in.State.ExcursionSince.IsZero() ||
		in.ReadingTS.Sub(in.State.ExcursionSince) < OpenFor) {
		scan.Unjudged[key] = true
		delete(scan.Seen, key)
		return
	}

	detail := map[string]any{
		// The raw reading, because that is the number an operator will check
		// against `uptime` or `sensors` and has to recognise. `fast` is what
		// was judged, and saying so separately keeps the row honest without
		// making the headline a figure that appears nowhere else.
		"value":     *in.Value,
		"smoothed":  in.State.Fast,
		"warn":      bounds.Warn,
		"crit":      bounds.Crit,
		"normal":    bucket.Slow,
		"hour":      HourOf(*in.ReadingTS),
		"source":    bounds.Source,
		"span_days": int(in.State.Span() / (24 * time.Hour)),
		// When this reading was taken, which is what the API serves as
		// measured_ts for a temperature condition.
		//
		// It rides the detail because the detail is refreshed on every pass
		// that finds the subject still bad and FROZEN on a pass that could not
		// judge it -- so it answers "when was this last actually measured"
		// exactly, and without the API joining a hypertable once per open row
		// on every fleet page load.
		"measured_ts": in.ReadingTS.UTC().Format(time.RFC3339),
	}
	if in.Rule.Unit != "" {
		detail["unit"] = in.Rule.Unit
	}
	for k, v := range in.Detail {
		detail[k] = v
	}

	onset, atLeast := *in.ReadingTS, true
	if e := in.State.ExcursionSince; !e.IsZero() && e.Before(onset) {
		onset, atLeast = e, false
	}

	scan.Bad[key] = Finding{
		Key:      key,
		Severity: severity,
		Detail:   detail,
		// WHEN THE READING ACTUALLY LEFT THE BAND, which the state already
		// knows and no walk has to reconstruct.
		//
		// The previous comment here argued that the onset was unknowable
		// because the threshold is a moving average and was a different number
		// at every past instant -- true, and beside the point: excursion_since
		// is stamped at the moment fast crossed, by the fold, at the time it
		// happened. Using now() instead reported every open at least OpenFor
		// late, and arbitrarily late behind a fold catching up on a backlog:
		// an excursion stamped twenty hours ago would open claiming it had just
		// started.
		//
		// Exact rather than a floor when it comes from the state, so
		// OpenedAtLeast is false there and the UI prints the time instead of
		// "over". The reading's own timestamp stays the fallback for a subject
		// whose condition is already open and whose excursion has since been
		// restamped, and it is a floor because it is the oldest instant this
		// pass can actually vouch for.
		OpenedTS:      onset,
		OpenedAtLeast: atLeast,
	}
}
