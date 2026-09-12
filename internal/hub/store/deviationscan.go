package store

import (
	"context"
	"fmt"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
)

// The deviation scans: temperature, process count and load average, judged
// against each subject's own baseline rather than a constant.
//
// WHY THESE READ A SAMPLE TABLE WHEN EVERY OTHER SCAN READS A GAUGE.
// scanHosts has host_current, scanFilesystems has filesystem_current, and both
// are one row per subject that ingest overwrites. Sensors have no such table,
// and host_current carries neither processes_total nor load5 -- it holds ten
// columns chosen for the fleet page, and those are not among them. So the
// current reading comes from a DISTINCT ON over the last few minutes of the
// hypertable instead. Both tables are chunked by day, so the range predicate
// touches exactly one chunk, and the alternative was three more gauge tables
// and the ingest writes to keep them true.
//
// currentWindow is how far back that look goes. Five scrapes: wide enough that
// a host whose ingest arrived late still has a reading, narrow enough that the
// scan never mistakes a stale sample for a current one -- and the staleness
// check below does not rely on it anyway, because a window is not a judgement
// about whether a subject is still there.
const currentWindow = 5 * conditions.ScrapeInterval

// scanSensors raises the temperature condition.
//
// EVERY KNOWN SENSOR IS ENUMERATED, not only the ones with a current reading,
// and that is the same decision scanFilesystems documents at length. A sensor
// absent from Seen while its host is reporting resolves as VANISHED -- at once,
// with no hysteresis, destroying the onset. A sensors collector that failed for
// one pass, or an agent whose hwmon read wedged, looks exactly like a drive
// that was pulled. So a sensor with no current reading is UNJUDGED and its
// condition is left alone, which takes the same trade scanFilesystems takes:
// a genuinely removed sensor leaves a stale condition open until someone
// dismisses it, rather than a wedged one being silently called recovered. The
// first is visible and wrong; the second is invisible and is a lie.
func (s *Store) scanSensors(ctx context.Context, scan *conditions.Scan,
	open map[conditions.Key]bool) error {
	rows, err := s.pool.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (host_id, sensor_id)
			       host_id, sensor_id, temp, ts
			  FROM sensor_samples
			 WHERE ts >= now() - $1::interval
			   AND temp IS NOT NULL
			 ORDER BY host_id, sensor_id, ts DESC
		)
		SELECT sen.host_id,
		       netra_sensor_subject(sen.chip, sen.label, sen.instance),
		       sen.chip,
		       sen.limit_high, sen.limit_high_crit,
		       l.temp, l.ts,
		       eh.slow, e.fast, eh.var, e.first_ts, e.updated_ts,
		       eh.updated_ts, e.excursion_since
		  FROM sensors sen
		  LEFT JOIN latest l
		         ON l.sensor_id = sen.id AND l.host_id = sen.host_id
		  LEFT JOIN metric_ewma e
		         ON e.host_id = sen.host_id
		        AND e.kind    = $2
		        AND e.subject = netra_sensor_subject(sen.chip, sen.label, sen.instance)
		  -- Only the bucket for the hour the current reading was taken in,
		  -- which is the only one this pass judges against. AT TIME ZONE 'UTC'
		  -- rather than a bare EXTRACT, because EXTRACT on a timestamptz reads
		  -- the session's TimeZone and the fold stamps the hour in UTC -- a
		  -- server running in CET would otherwise look up a bucket two hours
		  -- from the one that was written.
		  LEFT JOIN metric_ewma_hour eh
		         ON eh.host_id = e.host_id
		        AND eh.kind    = e.kind
		        AND eh.subject = e.subject
		        AND eh.hour    = EXTRACT(HOUR FROM l.ts AT TIME ZONE 'UTC')::smallint
		 WHERE sen.kind = 'temperature'`,
		currentWindow, conditions.KindTemperature)
	if err != nil {
		return fmt.Errorf("query sensors: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int32
		var subject, chip string
		var limitHigh, limitCrit, temp, slow, fast, variance *float64
		var readingTS, firstTS, updatedTS, bucketTS, excursion *time.Time

		if err := rows.Scan(&hostID, &subject, &chip, &limitHigh, &limitCrit,
			&temp, &readingTS, &slow, &fast, &variance, &firstTS, &updatedTS,
			&bucketTS, &excursion); err != nil {
			return fmt.Errorf("scan sensor: %w", err)
		}

		key := conditions.Key{HostID: hostID, Kind: conditions.KindTemperature, Subject: subject}

		judgeDeviation(scan, key, deviationInput{
			value:     temp,
			readingTS: readingTS,
			state:     ewmaOf(hourOf(readingTS), slow, fast, variance, firstTS, updatedTS, bucketTS, excursion),
			isOpen:    open[key],
			rule:      conditions.RuleFor(conditions.KindTemperature, chip),
			limits:    conditions.Limits{High: limitHigh, HighCrit: limitCrit},
			detail:    map[string]any{"chip": chip},
		})
	}
	return rows.Err()
}

// scanHostGauges raises the processes and load conditions.
//
// One query for both, because they come from the same row and the same
// staleness question answers for each. They are still two independent
// judgements: a kernel that reports a process count and no load average must
// have the first judged and the second left alone, never both suppressed
// because one column was NULL.
func (s *Store) scanHostGauges(ctx context.Context, scan *conditions.Scan,
	open map[conditions.Key]bool) error {
	rows, err := s.pool.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (host_id)
			       host_id, processes_total, load5, ts
			  FROM host_samples
			 WHERE ts >= now() - $1::interval
			 ORDER BY host_id, ts DESC
		)
		SELECT h.id,
		       l.processes_total, l.load5, l.ts,
		       ehp.slow, ep.fast, ehp.var, ep.first_ts, ep.updated_ts,
		       ehp.updated_ts, ep.excursion_since,
		       ehl.slow, el.fast, ehl.var, el.first_ts, el.updated_ts,
		       ehl.updated_ts, el.excursion_since
		  FROM hosts h
		  LEFT JOIN latest l ON l.host_id = h.id
		  LEFT JOIN metric_ewma ep
		         ON ep.host_id = h.id AND ep.kind = $2 AND ep.subject = ''
		  LEFT JOIN metric_ewma el
		         ON el.host_id = h.id AND el.kind = $3 AND el.subject = ''
		  -- See the note in scanSensors on AT TIME ZONE 'UTC'.
		  LEFT JOIN metric_ewma_hour ehp
		         ON ehp.host_id = h.id AND ehp.kind = $2 AND ehp.subject = ''
		        AND ehp.hour = EXTRACT(HOUR FROM l.ts AT TIME ZONE 'UTC')::smallint
		  LEFT JOIN metric_ewma_hour ehl
		         ON ehl.host_id = h.id AND ehl.kind = $3 AND ehl.subject = ''
		        AND ehl.hour = EXTRACT(HOUR FROM l.ts AT TIME ZONE 'UTC')::smallint`,
		currentWindow, conditions.KindProcesses, conditions.KindLoad)
	if err != nil {
		return fmt.Errorf("query host gauges: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int32
		var procs *int
		var load *float64
		var readingTS *time.Time
		var pSlow, pFast, pVar, lSlow, lFast, lVar *float64
		var pFirst, pUpdated, pBucket, pExcursion *time.Time
		var lFirst, lUpdated, lBucket, lExcursion *time.Time

		if err := rows.Scan(&hostID, &procs, &load, &readingTS,
			&pSlow, &pFast, &pVar, &pFirst, &pUpdated, &pBucket, &pExcursion,
			&lSlow, &lFast, &lVar, &lFirst, &lUpdated, &lBucket, &lExcursion); err != nil {
			return fmt.Errorf("scan host gauge: %w", err)
		}

		var procValue *float64
		if procs != nil {
			v := float64(*procs)
			procValue = &v
		}

		procKey := conditions.Key{HostID: hostID, Kind: conditions.KindProcesses}
		judgeDeviation(scan, procKey, deviationInput{
			value:     procValue,
			readingTS: readingTS,
			state:     ewmaOf(hourOf(readingTS), pSlow, pFast, pVar, pFirst, pUpdated, pBucket, pExcursion),
			isOpen:    open[procKey],
			rule:      conditions.RuleFor(conditions.KindProcesses, ""),
		})

		loadKey := conditions.Key{HostID: hostID, Kind: conditions.KindLoad}
		judgeDeviation(scan, loadKey, deviationInput{
			value:     load,
			readingTS: readingTS,
			state:     ewmaOf(hourOf(readingTS), lSlow, lFast, lVar, lFirst, lUpdated, lBucket, lExcursion),
			isOpen:    open[loadKey],
			rule:      conditions.RuleFor(conditions.KindLoad, ""),
		})
	}
	return rows.Err()
}

// deviationInput is one subject's reading and everything needed to judge it.
//
// Pointers where absence is a fact, because each absence means something
// different: no reading is a subject that did not report, an unseeded state is
// a subject not yet watched, and a NULL column is a kernel that does not
// publish the metric. Collapsing any of them to a zero would judge a host
// against a number nobody measured.
type deviationInput struct {
	value     *float64
	readingTS *time.Time
	state     conditions.EWMA
	// isOpen is whether this subject already has a condition open.
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
	isOpen bool
	rule   conditions.FamilyRule
	limits conditions.Limits
	detail map[string]any
}

// ewmaOf rebuilds one subject's state from its LEFT JOINed columns.
//
// The first five are NULL together or none are -- they come from one row -- so a
// single nil check covers them, and a subject with no row comes back as the zero
// EWMA, which reports Seeded() false and is handled as "not watched yet".
//
// excursion is the exception: it is nullable IN the row, because a subject
// sitting inside its band has no excursion to record. It also has to be read
// and passed here, which is easy to leave out and silent when you do -- the
// suppression in judgeDeviation is measured from it, so a zero value makes
// every departure look either brand new or infinitely old depending on which
// way the comparison falls. The CI failure that found this had both.
func ewmaOf(hour int, slow, fast, variance *float64,
	firstTS, updatedTS, bucketTS, excursion *time.Time) conditions.EWMA {
	if fast == nil || firstTS == nil || updatedTS == nil {
		return conditions.EWMA{}
	}
	if slow == nil || variance == nil || bucketTS == nil {
		// The subject is watched but this HOUR is not: a state seeded less than
		// a day ago has most of its buckets empty. Returned with the
		// per-subject half intact so the span gate still sees the real history,
		// and with the bucket unseeded so judgeDeviation files it unjudged --
		// not healthy, because nothing is known about this hour yet.
		return conditions.EWMA{Fast: *fast, FirstTS: *firstTS, UpdatedTS: *updatedTS}
	}
	e := conditions.EWMA{
		Fast: *fast, FirstTS: *firstTS, UpdatedTS: *updatedTS,
	}
	// Only the bucket for the reading's own hour is read back, because only
	// that one is judged against. Its index is not stored on the bucket, so the
	// caller passes it and the slot is filled directly.
	if hour >= 0 && hour < conditions.Buckets {
		e.Hour[hour] = conditions.Bucket{Slow: *slow, Var: *variance, UpdatedTS: *bucketTS}
	}
	if excursion != nil {
		e.ExcursionSince = *excursion
	}
	return e
}

// hourOf is the bucket index for a reading, or -1 when there is no reading.
//
// -1 rather than 0, because 0 is midnight: a subject that did not report would
// otherwise be given midnight's bucket and judged against it. judgeDeviation
// files a subject with no reading as unjudged before the bucket is consulted, so
// this only has to be a value that cannot be mistaken for an hour.
func hourOf(ts *time.Time) int {
	if ts == nil {
		return -1
	}
	return conditions.HourOf(*ts)
}

// judgeDeviation applies the rule to one subject and files it under Seen,
// Unjudged or Bad.
//
// THE THREE-STATE DISCIPLINE IS THE WHOLE FUNCTION. "Not over the line" and
// "cannot say" are different answers, and only the first may clear a
// condition. A subject with no reading this pass, or with no baseline yet, has
// not been found healthy -- it has not been looked at, and Scan.Unjudged is
// the state that says so. Writing either into Seen would let a sensor that
// stopped reporting, or one whose baseline was rebuilt, close a live condition
// as a recovery that never happened.
func judgeDeviation(scan *conditions.Scan, key conditions.Key, in deviationInput) {
	// No reading, or a metric this kernel does not publish.
	if in.value == nil || in.readingTS == nil {
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
	if !in.state.Seeded() || in.state.Span() < in.rule.MinSpan {
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
	if in.readingTS.Sub(in.state.UpdatedTS) > conditions.OpenFor {
		scan.Unjudged[key] = true
		return
	}

	// The bucket for the hour this reading was taken in. An hour the subject
	// has not been observed through yet is UNJUDGED: a state seeded yesterday
	// afternoon knows nothing about this morning, and judging against an empty
	// bucket would compare the reading against zero.
	bucket := in.state.Bucket(conditions.HourOf(*in.readingTS))
	if !bucket.Seeded() {
		scan.Unjudged[key] = true
		return
	}

	scan.Seen[key] = true

	warn, crit := bucket.Band(in.rule.Floor)
	bounds := conditions.DeviationThresholds(warn, crit, in.limits, in.rule.Ceilings)

	// Fast, not the raw reading: the smoothing keeps sensor jitter and a single
	// odd sample out of the judgement.
	severity := conditions.DeviationSeverity(in.state.Fast, bounds)
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
	// whether to keep looking. See deviationInput.isOpen for what applying it
	// to an open condition costs.
	//
	// The IsZero check is not redundant with the comparison beside it, and
	// leaving it out is a silent failure rather than a loud one: Sub against a
	// zero timestamp is an enormous duration, which passes OpenFor and opens
	// the condition on its very first reading with no suppression whatever.
	// Fail closed, so a state the fold has not marked cannot be raised.
	if !in.isOpen && (in.state.ExcursionSince.IsZero() ||
		in.readingTS.Sub(in.state.ExcursionSince) < conditions.OpenFor) {
		scan.Unjudged[key] = true
		delete(scan.Seen, key)
		return
	}

	detail := map[string]any{
		// The raw reading, because that is the number an operator will check
		// against `uptime` or `sensors` and has to recognise. `fast` is what
		// was judged, and saying so separately keeps the row honest without
		// making the headline a figure that appears nowhere else.
		"value":     *in.value,
		"smoothed":  in.state.Fast,
		"warn":      bounds.Warn,
		"crit":      bounds.Crit,
		"normal":    bucket.Slow,
		"hour":      conditions.HourOf(*in.readingTS),
		"source":    bounds.Source,
		"span_days": int(in.state.Span() / (24 * time.Hour)),
		// When this reading was taken, which is what the API serves as
		// measured_ts for a temperature condition.
		//
		// It rides the detail because the detail is refreshed on every pass
		// that finds the subject still bad and FROZEN on a pass that could not
		// judge it -- so it answers "when was this last actually measured"
		// exactly, and without the API joining a hypertable once per open row
		// on every fleet page load.
		"measured_ts": in.readingTS.UTC().Format(time.RFC3339),
	}
	if in.rule.Unit != "" {
		detail["unit"] = in.rule.Unit
	}
	for k, v := range in.detail {
		detail[k] = v
	}

	onset, atLeast := *in.readingTS, true
	if e := in.state.ExcursionSince; !e.IsZero() && e.Before(onset) {
		onset, atLeast = e, false
	}

	scan.Bad[key] = conditions.Finding{
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
