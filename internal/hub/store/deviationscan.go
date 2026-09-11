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
		       b.p01, b.p99, b.sample_count
		  FROM sensors sen
		  LEFT JOIN latest l
		         ON l.sensor_id = sen.id AND l.host_id = sen.host_id
		  LEFT JOIN metric_baselines b
		         ON b.host_id = sen.host_id
		        AND b.kind    = $2
		        AND b.subject = netra_sensor_subject(sen.chip, sen.label, sen.instance)
		 WHERE sen.kind = 'temperature'`,
		currentWindow, conditions.KindTemperature)
	if err != nil {
		return fmt.Errorf("query sensors: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int32
		var subject, chip string
		var limitHigh, limitCrit, temp, p01, p99 *float64
		var readingTS *time.Time
		var samples *int

		if err := rows.Scan(&hostID, &subject, &chip, &limitHigh, &limitCrit,
			&temp, &readingTS, &p01, &p99, &samples); err != nil {
			return fmt.Errorf("scan sensor: %w", err)
		}

		key := conditions.Key{HostID: hostID, Kind: conditions.KindTemperature, Subject: subject}
		rule := conditions.RuleFor(conditions.KindTemperature, chip)
		limits := conditions.Limits{High: limitHigh, HighCrit: limitCrit}

		judgeDeviation(scan, key, deviationInput{
			value:     temp,
			readingTS: readingTS,
			p01:       p01,
			p99:       p99,
			samples:   samples,
			rule:      rule,
			limits:    limits,
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
		       bp.p01, bp.p99, bp.sample_count,
		       bl.p01, bl.p99, bl.sample_count
		  FROM hosts h
		  LEFT JOIN latest l ON l.host_id = h.id
		  LEFT JOIN metric_baselines bp
		         ON bp.host_id = h.id AND bp.kind = $2 AND bp.subject = ''
		  LEFT JOIN metric_baselines bl
		         ON bl.host_id = h.id AND bl.kind = $3 AND bl.subject = ''`,
		currentWindow, conditions.KindProcesses, conditions.KindLoad)
	if err != nil {
		return fmt.Errorf("query host gauges: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int32
		var procs *int
		var load, pP01, pP99, lP01, lP99 *float64
		var readingTS *time.Time
		var pSamples, lSamples *int

		if err := rows.Scan(&hostID, &procs, &load, &readingTS,
			&pP01, &pP99, &pSamples, &lP01, &lP99, &lSamples); err != nil {
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
			p01:       pP01,
			p99:       pP99,
			samples:   pSamples,
			rule:      conditions.RuleFor(conditions.KindProcesses, ""),
		})

		judgeDeviation(scan, conditions.Key{
			HostID: hostID, Kind: conditions.KindLoad,
		}, deviationInput{
			value:     load,
			readingTS: readingTS,
			p01:       lP01,
			p99:       lP99,
			samples:   lSamples,
			rule:      conditions.RuleFor(conditions.KindLoad, ""),
		})
	}
	return rows.Err()
}

// deviationInput is one subject's reading and everything needed to judge it.
//
// Pointers throughout, because every one of them is a LEFT JOIN away from
// being absent and the three absences mean different things: no reading is a
// subject that did not report, no baseline is a subject not yet calibrated,
// and a NULL column is a kernel that does not publish the metric. Collapsing
// any of them to a zero would judge a host against a number nobody measured.
type deviationInput struct {
	value     *float64
	readingTS *time.Time
	p01, p99  *float64
	samples   *int
	rule      conditions.FamilyRule
	limits    conditions.Limits
	detail    map[string]any
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

	// No baseline, or one drawn from too little history. A subject netra has
	// not watched long enough is not a subject netra has found to be fine.
	if in.p99 == nil || in.p01 == nil || in.samples == nil {
		scan.Unjudged[key] = true
		return
	}
	baseline := conditions.Baseline{P01: *in.p01, P99: *in.p99, Samples: *in.samples}
	if !baseline.Ready(in.rule.MinSamples) {
		scan.Unjudged[key] = true
		return
	}

	scan.Seen[key] = true

	bounds := conditions.DeviationThresholds(baseline, in.rule.Floor, in.limits, in.rule.Ceilings)
	severity := conditions.DeviationSeverity(*in.value, bounds)
	if severity == "" {
		return
	}

	detail := map[string]any{
		"value":       *in.value,
		"warn":        bounds.Warn,
		"crit":        bounds.Crit,
		"p99":         baseline.P99,
		"source":      bounds.Source,
		"window_days": int(conditions.BaselineWindow / (24 * time.Hour)),
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
		// No walk back through the series, unlike the disk onset: the open
		// delay means a deviation has been true for at least OpenAfter passes
		// before it opens at all, and the baseline it is measured against is
		// rebuilt daily -- so "when did this first cross" cannot be answered
		// from history without asking which day's threshold to ask it about.
		// A floor that is honest beats a number that looks precise.
		OpenedTS:      *in.readingTS,
		OpenedAtLeast: true,
	}
}
