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
// conditions.CurrentWindow is how far back that look goes. The staleness check
// below does not rely on it, because a window is not a judgement about whether
// a subject is still there.

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
		       eh.weight, eh.exc, e.excursion_since
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
		conditions.CurrentWindow, conditions.KindTemperature)
	if err != nil {
		return fmt.Errorf("query sensors: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int32
		var subject, chip string
		var limitHigh, limitCrit, temp, slow, fast, variance, weight, exc *float64
		var readingTS, firstTS, updatedTS, excursion *time.Time

		if err := rows.Scan(&hostID, &subject, &chip, &limitHigh, &limitCrit,
			&temp, &readingTS, &slow, &fast, &variance, &firstTS, &updatedTS,
			&weight, &exc, &excursion); err != nil {
			return fmt.Errorf("scan sensor: %w", err)
		}

		key := conditions.Key{HostID: hostID, Kind: conditions.KindTemperature, Subject: subject}

		conditions.JudgeDeviation(scan, key, conditions.DeviationInput{
			Value:     temp,
			ReadingTS: readingTS,
			State:     ewmaOf(hourOf(readingTS), slow, fast, variance, weight, exc, firstTS, updatedTS, excursion),
			IsOpen:    open[key],
			Rule:      conditions.RuleFor(conditions.KindTemperature, chip),
			Limits:    conditions.Limits{High: limitHigh, HighCrit: limitCrit},
			Detail:    map[string]any{"chip": chip},
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
		       ehp.weight, ehp.exc, ep.excursion_since,
		       ehl.slow, el.fast, ehl.var, el.first_ts, el.updated_ts,
		       ehl.weight, ehl.exc, el.excursion_since
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
		conditions.CurrentWindow, conditions.KindProcesses, conditions.KindLoad)
	if err != nil {
		return fmt.Errorf("query host gauges: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int32
		var procs *int
		var load *float64
		var readingTS *time.Time
		var pSlow, pFast, pVar, pWeight, pExc, lSlow, lFast, lVar, lWeight, lExc *float64
		var pFirst, pUpdated, pExcursion *time.Time
		var lFirst, lUpdated, lExcursion *time.Time

		if err := rows.Scan(&hostID, &procs, &load, &readingTS,
			&pSlow, &pFast, &pVar, &pFirst, &pUpdated, &pWeight, &pExc, &pExcursion,
			&lSlow, &lFast, &lVar, &lFirst, &lUpdated, &lWeight, &lExc, &lExcursion); err != nil {
			return fmt.Errorf("scan host gauge: %w", err)
		}

		var procValue *float64
		if procs != nil {
			v := float64(*procs)
			procValue = &v
		}

		procKey := conditions.Key{HostID: hostID, Kind: conditions.KindProcesses}
		conditions.JudgeDeviation(scan, procKey, conditions.DeviationInput{
			Value:     procValue,
			ReadingTS: readingTS,
			State:     ewmaOf(hourOf(readingTS), pSlow, pFast, pVar, pWeight, pExc, pFirst, pUpdated, pExcursion),
			IsOpen:    open[procKey],
			Rule:      conditions.RuleFor(conditions.KindProcesses, ""),
		})

		loadKey := conditions.Key{HostID: hostID, Kind: conditions.KindLoad}
		conditions.JudgeDeviation(scan, loadKey, conditions.DeviationInput{
			Value:     load,
			ReadingTS: readingTS,
			State:     ewmaOf(hourOf(readingTS), lSlow, lFast, lVar, lWeight, lExc, lFirst, lUpdated, lExcursion),
			IsOpen:    open[loadKey],
			Rule:      conditions.RuleFor(conditions.KindLoad, ""),
		})
	}
	return rows.Err()
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
// suppression in conditions.JudgeDeviation is measured from it, so a zero value makes
// every departure look either brand new or infinitely old depending on which
// way the comparison falls. The CI failure that found this had both.
func ewmaOf(hour int, slow, fast, variance, weight, exc *float64,
	firstTS, updatedTS, excursion *time.Time) conditions.EWMA {
	if fast == nil || firstTS == nil || updatedTS == nil {
		return conditions.EWMA{}
	}
	if slow == nil || variance == nil || weight == nil || exc == nil {
		// The subject is watched but this HOUR is not: a state seeded less than
		// a day ago has most of its buckets empty. Returned with the
		// per-subject half intact so the span gate still sees the real history,
		// and with the bucket unseeded so conditions.JudgeDeviation files it unjudged --
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
		e.Hour[hour] = conditions.Bucket{Slow: *slow, Var: *variance, Weight: *weight, Exc: *exc}
	}
	if excursion != nil {
		e.ExcursionSince = *excursion
	}
	return e
}

// hourOf is the bucket index for a reading, or -1 when there is no reading.
//
// -1 rather than 0, because 0 is midnight: a subject that did not report would
// otherwise be given midnight's bucket and judged against it. conditions.JudgeDeviation
// files a subject with no reading as unjudged before the bucket is consulted, so
// this only has to be a value that cannot be mistaken for an hour.
func hourOf(ts *time.Time) int {
	if ts == nil {
		return -1
	}
	return conditions.HourOf(*ts)
}
