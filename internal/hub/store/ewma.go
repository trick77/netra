package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trick77/netra/internal/hub/conditions"
)

// FoldSamples brings every subject's moving average up to date.
//
// ON THE EVALUATOR TICK, NOT ON INGEST, and that was a real choice. Updating
// inside InsertHostSamples / InsertSensorSamples would be a read-modify-write
// per subject per scrape inside the ingest transaction, against inserts that
// are batched -- the expensive shape, on the one path that has to stay cheap.
// Reading forward on the tick costs one range scan per family over the current
// chunk, once a minute, and touches no hot path at all.
//
// What made the tick viable is that the fold reads forward from each row's OWN
// updated_ts rather than taking the latest sample. A tick-driven update that
// looked only at the newest row would absorb one reading per minute, so an
// agent reconnecting with a buffered hour would have fifty-nine of its sixty
// samples ignored and the average would track how often the hub looked rather
// than what the host did. Folding from the high-water mark makes the two
// identical.
//
// foldLimit bounds one pass. A host restored from a long outage, or a sensor
// whose row was just created, can have days of samples waiting; folding all of
// them in one statement would hold a transaction open across chunks while the
// tick that needs the result waits. The remainder is folded on the next tick,
// and the arithmetic is identical either way because the decay is computed from
// each reading's own timestamp.
const foldLimit = 20000

// foldSource is one metric family the fold knows how to read.
//
// A table rather than three near-identical methods: the three differ only in
// which rows to read and how to name the subject, and that is data. Adding
// r_await_ms or io_util_pct later is a row here, which is the whole point of
// moving to a primitive any metric can share.
type foldSource struct {
	kind string
	// query returns (host_id, subject, value, ts) for every sample newer than
	// the subject's own high-water mark, oldest first. $1 is the row limit.
	query string
}

func foldSources() []foldSource {
	return []foldSource{
		{
			kind: conditions.KindTemperature,
			// Joined to sensors for the identity, and LEFT JOINed to the state
			// so a sensor with no row yet still yields its samples -- that is
			// how a new subject gets seeded.
			query: `
				SELECT s.host_id,
				       netra_sensor_subject(sen.chip, sen.label, sen.instance) AS subject,
				       sen.chip, sen.limit_high, sen.limit_high_crit, s.temp, s.ts
				  FROM sensor_samples s
				  JOIN sensors sen
				    ON sen.id = s.sensor_id AND sen.host_id = s.host_id
				  LEFT JOIN metric_ewma e
				    ON e.host_id = s.host_id
				   AND e.kind    = 'temperature'
				   AND e.subject = netra_sensor_subject(sen.chip, sen.label, sen.instance)
				 WHERE sen.kind = 'temperature'
				   AND s.temp IS NOT NULL
				   AND s.ts > $2
				   AND (e.updated_ts IS NULL OR s.ts > e.updated_ts)
				 ORDER BY s.ts
				 LIMIT $1`,
		},
		{
			kind: conditions.KindProcesses,
			query: `
				SELECT h.host_id, '', '', NULL::double precision, NULL::double precision,
				       h.processes_total::double precision, h.ts
				  FROM host_samples h
				  LEFT JOIN metric_ewma e
				    ON e.host_id = h.host_id AND e.kind = 'processes' AND e.subject = ''
				 WHERE h.processes_total IS NOT NULL
				   AND h.ts > $2
				   AND (e.updated_ts IS NULL OR h.ts > e.updated_ts)
				 ORDER BY h.ts
				 LIMIT $1`,
		},
		{
			kind: conditions.KindLoad,
			query: `
				SELECT h.host_id, '', '', NULL::double precision, NULL::double precision,
				       h.load5, h.ts
				  FROM host_samples h
				  LEFT JOIN metric_ewma e
				    ON e.host_id = h.host_id AND e.kind = 'load' AND e.subject = ''
				 WHERE h.load5 IS NOT NULL
				   AND h.ts > $2
				   AND (e.updated_ts IS NULL OR h.ts > e.updated_ts)
				 ORDER BY h.ts
				 LIMIT $1`,
		},
	}
}

// FoldSamples advances every subject's state over the samples that have landed.
func (s *Store) FoldSamples(ctx context.Context) error {
	// EVERY FAMILY IS ATTEMPTED, and the errors are joined rather than returned
	// at the first one.
	//
	// Returning early made one family's failure silently stop the other two:
	// a persistent error in the temperature fold left processes and load never
	// advancing, and three minutes later the freshness gate in judgeDeviation
	// filed every host-kind subject as unjudged -- with a log line naming only
	// `temperature`, so the two kinds that had actually gone blind were the two
	// nothing mentioned. It skipped the prune as well, so the state grew
	// without bound for as long as the failure lasted.
	//
	// This is the same rule ScanConditions already follows for its five kinds:
	// one failing costs its own kind and nothing else.
	var errs []error
	for _, src := range foldSources() {
		if err := s.foldFamily(ctx, src); err != nil {
			// Named, so a failure says which family stopped advancing rather
			// than that "the fold" did.
			errs = append(errs, fmt.Errorf("fold %s: %w", src.kind, err))
		}
	}
	if err := s.pruneEWMA(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ewmaKey is one subject's identity within a family.
type ewmaKey struct {
	hostID  int32
	subject string
}

func (s *Store) foldFamily(ctx context.Context, src foldSource) error {
	// Current state for every subject this family knows about, read once. The
	// alternative -- a SELECT per subject as its samples arrive -- is a query
	// per sensor per tick, which is the shape this whole change is getting rid
	// of.
	state, err := s.loadEWMA(ctx, src.kind)
	if err != nil {
		return err
	}

	// The oldest high-water mark, which bounds the sample scan.
	//
	// WITHOUT IT NOTHING BOUNDS THE TIME RANGE. `s.ts > e.updated_ts` is
	// correlated through the LEFT JOIN, so the planner cannot exclude a chunk
	// from it, and with ORDER BY ts ASC the rows that qualify in steady state
	// are the NEWEST ones -- so every pass walked all seven days of chunks to
	// find the last minute's worth, three times a minute. The design this
	// replaced paid a full-week scan once a night; doing it 4,320 times a day
	// instead would not have been an improvement.
	//
	// A subject with no row yet has no mark, so the floor falls back to raw
	// retention: there is nothing older than that to seed from anyway.
	since, err := s.foldFloor(ctx, src.kind)
	if err != nil {
		return err
	}

	rows, err := s.pool.Query(ctx, src.query, foldLimit, since)
	if err != nil {
		return fmt.Errorf("query samples: %w", err)
	}
	defer rows.Close()

	touched := make(map[ewmaKey]map[int]bool)
	for rows.Next() {
		var key ewmaKey
		var chip string
		var limitHigh, limitCrit, value *float64
		var ts time.Time
		if err := rows.Scan(&key.hostID, &key.subject, &chip,
			&limitHigh, &limitCrit, &value, &ts); err != nil {
			return fmt.Errorf("scan sample: %w", err)
		}
		if value == nil {
			continue
		}

		// THE CHIP AND THE PUBLISHED LIMITS ARE CARRIED THROUGH SO THIS BAND
		// MATCHES THE JUDGEMENT'S, EXACTLY.
		//
		// The band decides whether `fast` counts as outside it, and
		// excursion_since is stamped from that -- so a band any wider here than
		// judgeDeviation's leaves a departure the judge can see with no
		// excursion recorded against it, and the subject is filed unjudged for
		// as long as it lasts. Getting the floor right is not enough: the caps
		// move the band much further, and a drivetemp subject capped to its 60 C
		// ceiling is judged against a warn of 59 where the uncapped band says
		// 62. The host kinds pass no chip and no limits because they have
		// neither.
		state[key] = state[key].Update(*value, ts,
			conditions.RuleFor(src.kind, chip),
			conditions.Limits{High: limitHigh, HighCrit: limitCrit})
		if touched[key] == nil {
			touched[key] = make(map[int]bool)
		}
		touched[key][conditions.HourOf(ts)] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(touched) == 0 {
		return nil
	}

	return s.saveEWMA(ctx, src.kind, state, touched)
}

// foldFloor is the oldest instant any subject of this kind still needs samples
// from, and therefore how far back the scan has to reach.
//
// Clamped to raw retention. A subject whose mark is older than that -- a host
// off for a fortnight -- has no samples left to fold from the missing stretch,
// so reaching further back only widens the scan.
func (s *Store) foldFloor(ctx context.Context, kind string) (time.Time, error) {
	retention := time.Now().UTC().Add(-conditions.TauSlow)

	var oldest *time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT min(updated_ts) FROM metric_ewma WHERE kind = $1`, kind).Scan(&oldest); err != nil {
		return time.Time{}, fmt.Errorf("fold floor: %w", err)
	}
	if oldest == nil || oldest.Before(retention) {
		return retention, nil
	}
	return *oldest, nil
}

// pruneEWMA drops subjects that have stopped reporting entirely.
//
// 0020's recompute carried the same cleanup and nothing replaced it, so a
// removed sensor left its row behind for good -- and loadEWMA reads every row
// of the kind on every tick, so the cost is paid forever.
//
// Keyed on updated_ts against raw retention, which means the subject has had no
// sample at all for a week. Deleting it cannot strand an open condition: with no
// current reading, scanSensors files that subject Unjudged either way, which
// leaves its condition exactly as it is. If it comes back, it re-warms.
func (s *Store) pruneEWMA(ctx context.Context) error {
	// The subject row first, then its orphaned buckets. Two statements rather
	// than a cascade, because metric_ewma_hour is keyed on the same triple
	// rather than on a surrogate the subject row owns -- there is no foreign key
	// between them to cascade along, and adding one would buy a constraint check
	// on every bucket write to save one DELETE a minute.
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM metric_ewma WHERE updated_ts < now() - $1::interval`,
		conditions.TauSlow); err != nil {
		return fmt.Errorf("prune state: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM metric_ewma_hour h
		 WHERE NOT EXISTS (
		     SELECT 1 FROM metric_ewma e
		      WHERE e.host_id = h.host_id AND e.kind = h.kind AND e.subject = h.subject
		 )`); err != nil {
		return fmt.Errorf("prune buckets: %w", err)
	}
	return nil
}

// loadEWMA reads every subject's state for one kind: the per-subject row and
// its hour buckets.
//
// Two queries rather than a join, because a join would repeat the per-subject
// columns across up to twenty-four rows and then need de-duplicating in Go. Two
// full reads of a table sized at twenty-four rows per judged subject is the
// cheaper and plainer shape.
func (s *Store) loadEWMA(ctx context.Context, kind string) (map[ewmaKey]conditions.EWMA, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT host_id, subject, fast, first_ts, updated_ts, excursion_since
		  FROM metric_ewma WHERE kind = $1`, kind)
	if err != nil {
		return nil, fmt.Errorf("query state: %w", err)
	}
	defer rows.Close()

	out := make(map[ewmaKey]conditions.EWMA)
	for rows.Next() {
		var key ewmaKey
		var e conditions.EWMA
		var excursion *time.Time
		if err := rows.Scan(&key.hostID, &key.subject, &e.Fast,
			&e.FirstTS, &e.UpdatedTS, &excursion); err != nil {
			return nil, fmt.Errorf("scan state: %w", err)
		}
		if excursion != nil {
			e.ExcursionSince = *excursion
		}
		out[key] = e
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	buckets, err := s.pool.Query(ctx, `
		SELECT host_id, subject, hour, slow, var, updated_ts
		  FROM metric_ewma_hour WHERE kind = $1`, kind)
	if err != nil {
		return nil, fmt.Errorf("query buckets: %w", err)
	}
	defer buckets.Close()

	for buckets.Next() {
		var key ewmaKey
		var hour int16
		var b conditions.Bucket
		if err := buckets.Scan(&key.hostID, &key.subject, &hour,
			&b.Slow, &b.Var, &b.UpdatedTS); err != nil {
			return nil, fmt.Errorf("scan bucket: %w", err)
		}
		// A bucket whose subject row is missing is skipped rather than
		// resurrecting the subject from it: the per-subject row carries the
		// span the warm-up is measured against, and a state with buckets but no
		// span would be judged against a MinSpan of zero.
		e, ok := out[key]
		if !ok || hour < 0 || hour >= conditions.Buckets {
			continue
		}
		e.Hour[hour] = b
		out[key] = e
	}
	return out, buckets.Err()
}

// saveEWMA writes back the subjects this pass advanced, and only the hour
// buckets it actually touched.
//
// `touched` carries the hours as well as the subject, because one reading
// changes exactly one bucket. Writing all twenty-four would be twenty-four times
// the rows for no new information, and a pass that only ever sees the current
// hour would keep rewriting twenty-three unchanged rows every minute.
func (s *Store) saveEWMA(ctx context.Context, kind string,
	state map[ewmaKey]conditions.EWMA, touched map[ewmaKey]map[int]bool) error {
	batch := &pgx.Batch{}
	queued := 0
	for key, hours := range touched {
		e := state[key]
		var excursion *time.Time
		if !e.ExcursionSince.IsZero() {
			ex := e.ExcursionSince
			excursion = &ex
		}

		// first_ts is deliberately NOT in either DO UPDATE list: it is the
		// oldest reading ever folded in, which is what the warm-up is measured
		// against, and rewriting it on every pass would keep the span at zero
		// forever and leave every subject permanently unjudged.
		batch.Queue(`
			INSERT INTO metric_ewma
			    (host_id, kind, subject, fast, first_ts, updated_ts, excursion_since)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (host_id, kind, subject) DO UPDATE
			   SET fast = excluded.fast,
			       updated_ts = excluded.updated_ts,
			       excursion_since = excluded.excursion_since`,
			key.hostID, kind, key.subject, e.Fast, e.FirstTS, e.UpdatedTS, excursion)
		queued++

		for hour := range hours {
			b := e.Hour[hour]
			batch.Queue(`
				INSERT INTO metric_ewma_hour
				    (host_id, kind, subject, hour, slow, var, updated_ts)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (host_id, kind, subject, hour) DO UPDATE
				   SET slow = excluded.slow,
				       var  = excluded.var,
				       updated_ts = excluded.updated_ts`,
				key.hostID, kind, key.subject, hour, b.Slow, b.Var, b.UpdatedTS)
			queued++
		}
	}

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()
	for range queued {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("upsert state: %w", err)
		}
	}
	return nil
}
