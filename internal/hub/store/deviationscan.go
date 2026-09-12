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
func (s *Store) scanSensors(ctx context.Context, scan *conditions.Scan) error {
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
		       e.slow, e.fast, e.var, e.first_ts, e.updated_ts
		  FROM sensors sen
		  LEFT JOIN latest l
		         ON l.sensor_id = sen.id AND l.host_id = sen.host_id
		  LEFT JOIN metric_ewma e
		         ON e.host_id = sen.host_id
		        AND e.kind    = $2
		        AND e.subject = netra_sensor_subject(sen.chip, sen.label, sen.instance)
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
		var readingTS, firstTS, updatedTS *time.Time

		if err := rows.Scan(&hostID, &subject, &chip, &limitHigh, &limitCrit,
			&temp, &readingTS, &slow, &fast, &variance, &firstTS, &updatedTS); err != nil {
			return fmt.Errorf("scan sensor: %w", err)
		}

		key := conditions.Key{HostID: hostID, Kind: conditions.KindTemperature, Subject: subject}

		judgeDeviation(scan, key, deviationInput{
			value:     temp,
			readingTS: readingTS,
			state:     ewmaOf(slow, fast, variance, firstTS, updatedTS),
			// The chip's own rule, which the fold could not know: it sees a
			// subject as a string, so it uses the kind's default floor. The
			// judgement is where the real floor, the family ceiling and the
			// published limits all enter, so no threshold a reader sees rests
			// on the fold's approximation.
			rule:   conditions.RuleFor(conditions.KindTemperature, chip),
			limits: conditions.Limits{High: limitHigh, HighCrit: limitCrit},
			detail: map[string]any{"chip": chip},
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
func (s *Store) scanHostGauges(ctx context.Context, scan *conditions.Scan) error {
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
		       ep.slow, ep.fast, ep.var, ep.first_ts, ep.updated_ts,
		       el.slow, el.fast, el.var, el.first_ts, el.updated_ts
		  FROM hosts h
		  LEFT JOIN latest l ON l.host_id = h.id
		  LEFT JOIN metric_ewma ep
		         ON ep.host_id = h.id AND ep.kind = $2 AND ep.subject = ''
		  LEFT JOIN metric_ewma el
		         ON el.host_id = h.id AND el.kind = $3 AND el.subject = ''`,
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
		var pFirst, pUpdated, lFirst, lUpdated *time.Time

		if err := rows.Scan(&hostID, &procs, &load, &readingTS,
			&pSlow, &pFast, &pVar, &pFirst, &pUpdated,
			&lSlow, &lFast, &lVar, &lFirst, &lUpdated); err != nil {
			return fmt.Errorf("scan host gauge: %w", err)
		}

		var procValue *float64
		if procs != nil {
			v := float64(*procs)
			procValue = &v
		}

		judgeDeviation(scan, conditions.Key{
			HostID: hostID, Kind: conditions.KindProcesses,
		}, deviationInput{
			value:     procValue,
			readingTS: readingTS,
			state:     ewmaOf(pSlow, pFast, pVar, pFirst, pUpdated),
			rule:      conditions.RuleFor(conditions.KindProcesses, ""),
		})

		judgeDeviation(scan, conditions.Key{
			HostID: hostID, Kind: conditions.KindLoad,
		}, deviationInput{
			value:     load,
			readingTS: readingTS,
			state:     ewmaOf(lSlow, lFast, lVar, lFirst, lUpdated),
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
	rule      conditions.FamilyRule
	limits    conditions.Limits
	detail    map[string]any
}

// ewmaOf rebuilds one subject's state from its LEFT JOINed columns.
//
// All five are NULL together or none are -- they come from one row -- so a
// single nil check is enough, and a subject with no row comes back as the zero
// EWMA, which reports Seeded() false and is handled as "not watched yet".
func ewmaOf(slow, fast, variance *float64, firstTS, updatedTS *time.Time) conditions.EWMA {
	if slow == nil || fast == nil || variance == nil || firstTS == nil || updatedTS == nil {
		return conditions.EWMA{}
	}
	return conditions.EWMA{
		Slow: *slow, Fast: *fast, Var: *variance,
		FirstTS: *firstTS, UpdatedTS: *updatedTS,
	}
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

	scan.Seen[key] = true

	warn, crit := in.state.Band(in.rule.Floor)
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
	// The IsZero check is not redundant with the comparison beside it, and
	// leaving it out is a silent failure rather than a loud one: Sub against a
	// zero timestamp is an enormous duration, which passes OpenFor and opens
	// the condition on its very first reading with no suppression whatever.
	// Fail closed, so a state the fold has not marked cannot be raised.
	if in.state.ExcursionSince.IsZero() ||
		in.readingTS.Sub(in.state.ExcursionSince) < conditions.OpenFor {
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
		"normal":    in.state.Slow,
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

	scan.Bad[key] = conditions.Finding{
		Key:      key,
		Severity: severity,
		Detail:   detail,
		// The reading's own timestamp as a floor, not now(). A pass that finds
		// a host three minutes behind on ingest must not record the onset as
		// the moment the hub noticed.
		//
		// No walk back through the series, unlike the disk onset, and now for a
		// stronger reason than before: the threshold is a moving average, so it
		// was a different number at every past instant. "When did this first
		// cross" has no answer without also asking which minute's threshold to
		// ask it against, and reconstructing that would mean replaying the
		// average backwards. A floor that is honest beats a number that looks
		// precise.
		OpenedTS:      *in.readingTS,
		OpenedAtLeast: true,
	}
}
