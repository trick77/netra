package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/trick77/netra/internal/hub/systemdstate"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// This file holds the per-entity families the Group 1-4 collectors write.
//
// Two rules run through all of it.
//
// ON CONFLICT DO NOTHING everywhere, for the reason InsertHostSamples has it:
// a replayed batch re-sends rows the hub already stored, and failing the
// INSERT would 503 the flush and pin the agent's ring buffer on a batch it can
// never land.
//
// Agents send NATURAL keys -- container_key, chip+label, device, mountpoint --
// and the hub resolves them to the surrogate ids the hypertables reference.
// An agent cannot know an id the hub assigns, and making it ask would add a
// round trip the protocol deliberately does not have.

// batchExecer is the pool as this file uses it: batches for the bulk path,
// single statements for the quarantine path below.
type batchExecer interface {
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// poisonRow reports whether err is a row the hub can never store -- one that
// will fail identically on every retry -- rather than a database that is
// merely unavailable.
//
// This distinction is the whole point. A 503 tells the agent to re-send, and
// the agent re-sends the IDENTICAL batch: for a transient failure that is
// exactly right, and for an unstorable row it is a permanent wedge, because
// the ring buffer only drops a prefix the hub acknowledged. A NUL byte in a
// process comm (22021) or an address INET will not parse (22P02) would
// otherwise stall every later scrape on that host forever.
//
// Class 22 (data exception) and class 23 (integrity constraint violation) are
// the only two treated this way, ON PURPOSE. Class 42 -- undefined table,
// syntax error -- is equally permanent but is the HUB's bug rather than the
// agent's, and quarantining it would drop every row of a family and answer
// 200, turning a schema mistake into silent fleet-wide data loss. Everything
// else (class 08 connection, 53 resources, 57 operator intervention, a
// context deadline) is transient and must reach the agent as a retry.
func poisonRow(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23")
}

// execBatch sends a batch and totals the rows affected, quarantining the rows
// Postgres refuses. Every family below inserts the same way, so the loop lives
// here once -- and so does the quarantine, which a family must not be able to
// opt out of by accident.
func execBatch(ctx context.Context, db batchExecer, batch *pgx.Batch, what string) (int64, error) {
	inserted, err := sendBatch(ctx, db, batch)
	if err == nil {
		return inserted, nil
	}
	if !poisonRow(err) {
		return 0, fmt.Errorf("insert %s: %w", what, err)
	}
	return quarantine(ctx, db, batch, what, err)
}

// sendBatch is the fast path: one round trip for the whole family.
func sendBatch(ctx context.Context, db batchExecer, batch *pgx.Batch) (int64, error) {
	results := db.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()

	var inserted int64
	for range batch.Len() {
		tag, err := results.Exec()
		if err != nil {
			return 0, err
		}
		inserted += tag.RowsAffected()
	}
	return inserted, nil
}

// quarantine re-runs a poisoned batch one statement at a time, keeping every
// row Postgres accepts and dropping the ones it does not.
//
// The WHOLE batch is replayed, not just the rows from the failure onward. pgx
// sends a batch with a single trailing Sync, which makes it one implicit
// transaction: a failure anywhere rolls back the rows that had already
// reported success, so after a poisoned batch nothing at all was stored.
// Replaying is safe because every statement in this file is ON CONFLICT DO
// NOTHING or DO UPDATE, so a row that did land is absorbed rather than
// duplicated -- the same property that makes a post-outage replay safe.
//
// One round trip per row is the cost, paid only on a path that is rare by
// construction: the agent generates these rows from the kernel, so a value
// Postgres rejects means a genuinely malformed name or address.
func quarantine(ctx context.Context, db batchExecer, batch *pgx.Batch, what string, cause error) (int64, error) {
	var inserted, dropped int64

	for _, q := range batch.QueuedQueries {
		tag, err := db.Exec(ctx, q.SQL, q.Arguments...)
		if err == nil {
			inserted += tag.RowsAffected()
			continue
		}
		if !poisonRow(err) {
			// The database went away mid-quarantine. That is a retry, not a
			// drop.
			return 0, fmt.Errorf("insert %s: %w", what, err)
		}
		dropped++
	}

	if dropped == 0 {
		// Every statement succeeded on its own, so the batch failed for a
		// reason that did not survive being taken apart. Saying "dropped
		// rows" here would send an operator looking for data loss that did
		// not happen.
		slog.Warn("a batch failed but every row stored individually",
			"family", what, "kept", inserted, "err", cause)
		return inserted, nil
	}

	slog.Warn("dropped rows Postgres refused to store",
		"family", what, "dropped", dropped, "kept", inserted, "err", cause)
	return inserted, nil
}

// resolveOne runs one dimension upsert, distinguishing a natural key the hub
// can never store from a database that is merely unavailable.
//
// ok=false means the key was poison -- a NUL byte in a container name, a label
// Postgres will not accept. The caller leaves it out of the id map, and the
// `id, ok := ids[...]` guard every family already has drops the samples that
// referenced it. Without this the resolvers would 503 the request BEFORE the
// batch quarantine ever ran, which is the same permanent wedge by an earlier
// door.
func (s *Store) resolveOne(ctx context.Context, dimension, key, stmt string, args ...any) (int32, bool, error) {
	var id int32
	if err := s.pool.QueryRow(ctx, stmt, args...).Scan(&id); err != nil {
		if poisonRow(err) {
			slog.Warn("dropped a dimension row Postgres refused to store",
				"dimension", dimension, "key", key, "err", err)
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("resolve %s %s: %w", dimension, key, err)
	}
	return id, true, nil
}

func tsOf(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// --------------------------------------------------------------- disk I/O

// InsertDiskIoSamples writes one row per block device.
func (s *Store) InsertDiskIoSamples(ctx context.Context, hostID int32, rows []*netrav1.DiskIoSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO disk_io_samples (
			host_id, ts, device, read_bytes, write_bytes, read_ops, write_ops,
			io_util_pct, r_await_ms, w_await_ms, weighted_io_pct
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (host_id, ts, device) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetDevice(),
			r.ReadBytes, r.WriteBytes, r.ReadOps, r.WriteOps,
			r.IoUtilPct, r.RAwaitMs, r.WAwaitMs, r.WeightedIoPct)
	}
	return execBatch(ctx, s.pool, batch, "disk io sample")
}

// ----------------------------------------------------------------- network

// InsertNetSamples writes one row per interface.
func (s *Store) InsertNetSamples(ctx context.Context, hostID int32, rows []*netrav1.NetSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO net_samples (host_id, ts, iface, rx_bytes, tx_bytes, rx_errs, tx_errs)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (host_id, ts, iface) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetIface(),
			r.RxBytes, r.TxBytes, r.RxErrs, r.TxErrs)
	}
	return execBatch(ctx, s.pool, batch, "net sample")
}

// ---------------------------------------------------- collector telemetry

// InsertCollectorSamples writes each collector's own health for one scrape.
func (s *Store) InsertCollectorSamples(ctx context.Context, hostID int32, rows []*netrav1.CollectorSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO collector_samples (host_id, ts, collector, duration_ms, ok, error_code)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id, ts, collector) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetCollector(),
			r.DurationMs, r.GetOk(), r.ErrorCode)
	}
	return execBatch(ctx, s.pool, batch, "collector sample")
}

// ------------------------------------------------------------------ events

// InsertEvents writes discrete state changes.
func (s *Store) InsertEvents(ctx context.Context, hostID int32, rows []*netrav1.Event) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	rows, err := s.dropUnchangedStates(ctx, hostID, rows)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO events (host_id, ts, type, subject, detail, severity)
		VALUES ($1, $2, $3, $4, COALESCE($5::jsonb, '{}'::jsonb), $6)
		ON CONFLICT (host_id, ts, type, subject) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		var subject *string
		if r.GetSubject() != "" {
			v := r.GetSubject()
			subject = &v
		}
		var detail *string
		if r.GetDetailJson() != "" {
			v := r.GetDetailJson()
			detail = &v
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetType(), subject, detail,
			eventSeverity(r))
	}
	return execBatch(ctx, s.pool, batch, "event")
}

// dropUnchangedStates removes the rows that restate a state the hub already
// has, leaving every other row untouched and in order.
//
// Only types eventStateKey recognises are considered; an occurrence type takes
// no query and no comparison, so a scrape carrying nothing but kernel errors
// costs exactly what it did before.
//
// The comparison is against the state as it stood BEFORE this batch, which is
// the same choice InsertSystemdUnitEvents documents: the rows are then walked
// oldest-first per subject against a running key, so a replayed batch holding
// clean -> degraded -> clean records all three, while three identical cleans
// record one.
//
// A failure reading the stored state fails the insert, EXCEPT for a row
// Postgres can never accept: see the quarantine note inside.
func (s *Store) dropUnchangedStates(ctx context.Context, hostID int32, rows []*netrav1.Event) ([]*netrav1.Event, error) {
	byKey := map[eventSubject][]stateRow{}
	for i, r := range rows {
		key, ok := eventStateKey(r.GetType(), r.GetDetailJson())
		if !ok {
			continue
		}
		id := eventSubject{typ: r.GetType(), subject: r.GetSubject()}
		byKey[id] = append(byKey[id], stateRow{idx: i, key: key})
	}
	if len(byKey) == 0 {
		return rows, nil
	}

	stored, err := s.latestEventStates(ctx, hostID, byKey)
	if err != nil {
		if !poisonRow(err) {
			return nil, err
		}
		// The batch carries a subject Postgres will not accept -- a NUL byte in
		// an array name -- and it poisoned the LOOKUP before the insert could
		// quarantine it. Failing here would 503 the flush, and the agent
		// re-sends the identical batch forever: the exact wedge the quarantine
		// exists to prevent, reached through a door it cannot see.
		//
		// So the pass continues knowing nothing about what is stored. Every key
		// looks new, the in-batch comparison below still runs, and the poison
		// row is rejected where it always was. The cost is at most one
		// redundant row per subject in one batch.
		stored = nil
	}

	drop := make(map[int]bool)
	for id, group := range byKey {
		// Oldest first, so the running comparison walks the batch the way time
		// did. Stable, so two events sharing a timestamp keep the order the
		// agent sent them in.
		sorted := slices.Clone(group)
		slices.SortStableFunc(sorted, func(a, b stateRow) int {
			return cmp.Compare(rows[a.idx].GetTsMs(), rows[b.idx].GetTsMs())
		})

		last, seen := stored[id]
		for _, sr := range sorted {
			if seen && sr.key == last {
				drop[sr.idx] = true
				continue
			}
			last, seen = sr.key, true
		}
	}
	if len(drop) == 0 {
		return rows, nil
	}

	kept := make([]*netrav1.Event, 0, len(rows)-len(drop))
	for i, r := range rows {
		if !drop[i] {
			kept = append(kept, r)
		}
	}
	return kept, nil
}

// latestEventStates reads the most recent stored state for each (type,
// subject) the batch touches.
//
// Rides events_host_type_subject_ts_idx (0017) on its (host_id, type) prefix,
// and takes the index's ts ordering rather than sorting: it reads one type's
// rows for one host instead of the host's whole event history. The COALESCE
// below is not sargable, so the subject is a filter and not a seek -- which is
// the right trade, since the guard is what keeps a type's rows per host down
// to the transitions in the first place.
//
// Subjects are compared through COALESCE on both sides: a host-wide event
// stores NULL, and an equality join would silently match nothing for it --
// which would not error, it would just never dedup those rows.
func (s *Store) latestEventStates(ctx context.Context, hostID int32, want map[eventSubject][]stateRow) (map[eventSubject]string, error) {
	types := make([]string, 0, len(want))
	subjects := make([]string, 0, len(want))
	for id := range want {
		types = append(types, id.typ)
		subjects = append(subjects, id.subject)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (e.type, e.subject) e.type, COALESCE(e.subject, ''), e.detail::text
		  FROM events e
		  JOIN unnest($2::text[], $3::text[]) AS k(type, subject)
		    ON e.type = k.type AND COALESCE(e.subject, '') = k.subject
		 WHERE e.host_id = $1
		 ORDER BY e.type, e.subject, e.ts DESC`, hostID, types, subjects)
	if err != nil {
		return nil, fmt.Errorf("read latest event states: %w", err)
	}
	defer rows.Close()

	out := make(map[eventSubject]string, len(want))
	for rows.Next() {
		var typ, subject, detail string
		if err := rows.Scan(&typ, &subject, &detail); err != nil {
			return nil, fmt.Errorf("scan latest event state: %w", err)
		}
		// A stored row whose detail no longer parses -- written by a producer
		// since changed -- has no comparable state, so the incoming row is
		// stored and becomes the new baseline.
		if key, ok := eventStateKey(typ, detail); ok {
			out[eventSubject{typ: typ, subject: subject}] = key
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read latest event states: %w", err)
	}
	return out, nil
}

// eventSeverity is what lands in events.severity: the wire field, and nothing
// else. It rode detail_json's `severity` key before the field existed, and the
// hub read both while agents older than the field were still in the fleet.
// Every producer states it in the field now, and 0015_event_severity.sql
// backfilled the column from the detail key, so history says the same thing.
//
// An unrecognised word falls through to "info" rather than failing the row.
// That is NOT the old fallback wearing a different hat: the column has a CHECK
// on it, so passing a value through unexamined would turn one malformed event
// into a rejected BATCH, taking every other family in it down with a host's
// whole scrape.
func eventSeverity(r *netrav1.Event) string {
	if s := r.GetSeverity(); validEventSeverity(s) {
		return s
	}
	return "info"
}

func validEventSeverity(s string) bool {
	return s == "info" || s == "warning" || s == "critical"
}

// -------------------------------------------------------------- dimensions

// resolveSensorIDs upserts the sensors named in rows and returns
// "chip\x00label" -> id.
//
// ON CONFLICT DO UPDATE rather than DO NOTHING: DO NOTHING makes RETURNING
// yield no row for an existing sensor, so the second scrape would resolve
// nothing and drop every sample. Updating a column to itself is the standard
// way to force the row back.
func (s *Store) resolveSensorIDs(ctx context.Context, hostID int32, rows []*netrav1.SensorSample) (map[string]int32, error) {
	out := make(map[string]int32)
	if len(rows) == 0 {
		return out, nil
	}

	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		key := sensorKey(r)
		if seen[key] {
			continue
		}
		seen[key] = true

		// COALESCE on the limits, not a plain assignment, and the difference
		// decides whether this feature survives an agent rollout. A chip's
		// published limit is constant for the life of the drive, so a row that
		// arrives without one is almost never a drive whose limit was
		// withdrawn -- it is an agent that predates the field, or a sysfs read
		// that timed out and is wedged for the next thousand scrapes. Writing
		// NULL over a good limit on the strength of that would quietly drop
		// the whole fleet back to the fallback ceilings, which is exactly the
		// permanently-red-NVMe failure the limits exist to prevent.
		//
		// The cost is that a genuine limit change can only be raised, never
		// cleared, until the sensor row is deleted with its host. That is the
		// right way round: a stale 80 C ceiling still judges better than none.
		//
		// KNOWN AND NOT FIXED HERE: a disk swapped into the same slot inherits
		// the previous drive's judgement. Sensor identity is chip/label/
		// instance and the instance is the block device name, so a new disk at
		// sda reuses this row -- keeping the old drive's limits through the
		// COALESCE above, and its metric_baselines row too, since the
		// baseline cleanup only removes subjects whose whole HOST has gone
		// quiet. The new drive is judged against another device's week until
		// the next recompute moves the percentiles.
		//
		// Fixing it needs identity to carry something the slot does not --
		// the serial, which `devices` already holds and `sensors` does not --
		// and that is a migration of every sensor's history, not a line here.
		// The exposure is bounded: limits differ little between drives of a
		// type, and the baseline is rewritten daily.
		id, ok, err := s.resolveOne(ctx, "sensor", sensorName(r), `
			INSERT INTO sensors (host_id, chip, label, kind, instance, limit_high, limit_high_crit)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (host_id, chip, label, instance) DO UPDATE
			   SET kind            = EXCLUDED.kind,
			       limit_high      = COALESCE(EXCLUDED.limit_high, sensors.limit_high),
			       limit_high_crit = COALESCE(EXCLUDED.limit_high_crit, sensors.limit_high_crit)
			RETURNING id`, hostID, r.GetChip(), r.GetLabel(), r.GetKind(), r.GetInstance(),
			r.LimitHigh, r.LimitHighCrit)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out[key] = id
	}
	return out, nil
}

// sensorKey is one sensor's identity, and the ONE place it is built.
//
// Instance is part of it because chip + label is not unique: every drivetemp
// chip is named "drivetemp" and publishes no tempN_label, so four SATA disks
// on one host arrive as four rows all calling themselves drivetemp/temp1, and
// two NVMe drives both arrive as nvme/Composite. Keyed without the instance,
// resolveSensorIDs mapped all four onto one id and InsertSensorSamples' ON
// CONFLICT DO NOTHING then discarded three of every four readings.
//
// A function rather than the expression written out at each call site: the two
// callers below build this key independently, and teaching only the resolver
// about the instance leaves the sample loop looking sensors up by the OLD key.
// That version compiles, creates the four sensor rows, and still funnels every
// sample into whichever one the map happens to hold -- the same data loss with
// a more convincing schema behind it.
func sensorKey(r *netrav1.SensorSample) string {
	return r.GetChip() + "\x00" + r.GetLabel() + "\x00" + r.GetInstance()
}

// sensorName is what a resolve failure is reported under -- an operator has to
// recognise the sensor in a log line, and "drivetemp/temp1" names four of them
// on a four-disk host.
func sensorName(r *netrav1.SensorSample) string {
	name := r.GetChip() + "/" + r.GetLabel()
	if r.GetInstance() == "" {
		return name
	}
	return name + " (" + r.GetInstance() + ")"
}

// InsertSensorSamples resolves each sensor's identity to an id and writes the
// rows.
func (s *Store) InsertSensorSamples(ctx context.Context, hostID int32, rows []*netrav1.SensorSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids, err := s.resolveSensorIDs(ctx, hostID, rows)
	if err != nil {
		return 0, err
	}

	const stmt = `
		INSERT INTO sensor_samples (host_id, ts, sensor_id, temp, value)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (host_id, ts, sensor_id) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		id, ok := ids[sensorKey(r)]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id, r.Temp, r.Value)
	}
	return execBatch(ctx, s.pool, batch, "sensor sample")
}

// resolveContainerIDs upserts the containers named in rows and returns
// container_key -> id.
//
// The image is updated on conflict because a service that was redeployed has a
// new one, and leaving the old value would describe a container that no longer
// exists.
//
// last_seen is stamped from the SAMPLE's own ts rather than from now(), for
// the reason resolveDeviceIDs states at length: the agent's ring buffer hands
// over hours-old scrapes after a hub outage, and now() would date every one of
// them to the moment they landed -- reporting a container as seen "just now"
// on samples taken overnight, which is the exact opposite of the staleness cue
// the Containers table reads this column for.
func (s *Store) resolveContainerIDs(ctx context.Context, hostID int32, rows []*netrav1.ContainerSample) (map[string]int32, error) {
	out := make(map[string]int32)
	if len(rows) == 0 {
		return out, nil
	}

	// The NEWEST row per container, not the first one seen: a batch can span
	// several scrapes, and name, image and last_seen all come from that same
	// newest row rather than from whichever arrived first.
	newest := make(map[string]*netrav1.ContainerSample, len(rows))
	for _, r := range rows {
		key := r.GetContainerKey()
		if key == "" {
			continue
		}
		if prev, ok := newest[key]; !ok || r.GetTsMs() > prev.GetTsMs() {
			newest[key] = r
		}
	}

	// tried records every key this batch has already attempted, INCLUDING the
	// ones that failed. Keying the skip on the output map alone would re-issue
	// a failed query for every row carrying the same poison key -- a host with
	// two hundred samples for one unstorable name would make two hundred round
	// trips and log two hundred warnings, on every ingest, forever, since the
	// agent keeps re-sending it.
	tried := make(map[string]bool, len(rows))

	for _, row := range rows {
		key := row.GetContainerKey()
		if key == "" || tried[key] {
			continue
		}
		tried[key] = true
		r := newest[key]

		// GREATEST, so an out-of-order replay cannot walk last_seen backwards
		// and mark a container gone in the UI while its newest sample is
		// current.
		// What Docker said about this container, from the same newest row the
		// name and image come from.
		//
		// All five OVERWRITE, including with NULL. An agent whose socket went
		// away is no longer in a position to assert that a container is
		// healthy, and keeping the last "healthy" the hub happened to hear is
		// the worst failure available here -- a green badge on a container
		// nobody can see. This is the same bargain name and image already make.
		//
		// restart_count is included on purpose, and it is only safe to include
		// because the AGENT holds the cache: it reports its last known count on
		// every scrape rather than only on the ones that called inspect, and it
		// drops that cached count the moment an inspect is refused. So an unset
		// restart_count here means the agent cannot answer, not that this
		// particular scrape did not ask. Coalescing instead would pin a number
		// nobody is asserting any more -- "Restarts: 12" above a State and a
		// Health that both correctly read "not reported".
		//
		// started_at overwrites for a stronger reason than the convention: it
		// shares the agent's inspect cache entry with restart_count, so an
		// agent that has lost inspect drops BOTH. Coalescing one while
		// overwriting the other would leave a start time from a generation
		// whose count is gone -- an uptime asserted for an incarnation nobody
		// can still see.
		//
		// It is also the one column here that is not derived from a
		// transition: Docker states it outright, which is exactly why it can
		// answer what state_ts cannot.
		//
		// state_ts is when the state was ENTERED, advanced only on an actual
		// change -- the rule read.Unit.Since documents for systemd. IS DISTINCT
		// FROM rather than <>, so the first transition out of NULL counts.
		const stmt = `
			INSERT INTO containers (host_id, container_key, name, image, is_agent, last_seen,
			                        docker_state, health, labels, restart_count, state_ts,
			                        started_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $6, $11)
			ON CONFLICT (host_id, container_key) DO UPDATE
			   SET name = EXCLUDED.name, image = EXCLUDED.image, is_agent = EXCLUDED.is_agent,
			       last_seen = GREATEST(containers.last_seen, EXCLUDED.last_seen),
			       docker_state = EXCLUDED.docker_state,
			       health = EXCLUDED.health,
			       labels = EXCLUDED.labels,
			       restart_count = EXCLUDED.restart_count,
			       started_at = EXCLUDED.started_at,
			       state_ts = CASE
			           WHEN containers.docker_state IS DISTINCT FROM EXCLUDED.docker_state
			           THEN EXCLUDED.last_seen
			           ELSE containers.state_ts
			       END
			RETURNING id`
		args := []any{
			hostID, key, r.GetName(), r.GetImage(), r.GetIsAgent(), tsOf(r.GetTsMs()),
			r.DockerState, r.Health, labelsJSON(r.GetLabels()), int64OrNil(r.RestartCount),
			tsPtrOf(r.StartedAtMs),
		}

		var (
			id  int32
			ok  bool
			err error
		)
		if carriesRestartData(rows, key) {
			id, ok, err = s.upsertContainerWithRestarts(ctx, hostID, key, stmt, args, rows, &r.Image)
		} else {
			// Nothing inspect-derived for this container, so no restart event
			// is derivable however the counter moved -- see
			// carriesRestartData. It keeps the single statement it has always
			// had rather than paying for a transaction that could not produce
			// anything.
			id, ok, err = s.resolveOne(ctx, "container", key, stmt, args...)
		}
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out[key] = id
	}
	return out, nil
}

// upsertContainerWithRestarts moves the container row forward and records the
// restarts that move implies, in ONE transaction.
//
// One transaction PER CONTAINER, and the two halves of that choice pull against
// each other. The row and the event that explains it are two halves of one
// fact: the stored counter advances past a restart, so a crash between them
// loses that restart forever, with nothing left to re-derive it from. But one
// transaction for the whole BATCH would let a single row Postgres refuses abort
// every other container in it -- exactly the wedge resolveOne's poison-row
// quarantine exists to prevent. Per container is the only shape that keeps
// both.
//
// SELECT ... FOR UPDATE rather than a plain read: two flushes for the same host
// can otherwise both read the same prior count and both emit the same restart.
func (s *Store) upsertContainerWithRestarts(
	ctx context.Context,
	hostID int32,
	key, stmt string,
	args []any,
	rows []*netrav1.ContainerSample,
	image *string,
) (int32, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("begin container %s: %w", key, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var prev containerState
	err = tx.QueryRow(ctx, `
		SELECT restart_count, started_at, last_seen, image
		  FROM containers
		 WHERE host_id = $1 AND container_key = $2
		 FOR UPDATE`, hostID, key).
		Scan(&prev.Count, &prev.StartedAt, &prev.LastSeen, &prev.Image)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// The quarantine has to be here too, not only on the upsert below.
		// This read runs FIRST and takes the same key, so an unstorable one --
		// a NUL byte in a container name, the classic case -- fails here and
		// would take the whole batch down before the upsert ever got the
		// chance to refuse it politely. Same treatment, same reason: one bad
		// container must not cost every other container on the host.
		if poisonRow(err) {
			slog.Warn("dropped a dimension row Postgres refused to store",
				"dimension", "container", "key", key, "err", err)
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read container %s: %w", key, err)
	}
	// ErrNoRows is a first sighting: prev stays zero, and a walk with no
	// previous count emits nothing. A container netra has just met has not
	// restarted as far as anyone here knows.

	var id int32
	if err := tx.QueryRow(ctx, stmt, args...).Scan(&id); err != nil {
		if poisonRow(err) {
			slog.Warn("dropped a dimension row Postgres refused to store",
				"dimension", "container", "key", key, "err", err)
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("resolve container %s: %w", key, err)
	}

	for _, ev := range restartEvents(prev, observationsOf(rows, key), image) {
		body, err := detailBody(ev.Detail)
		if err != nil {
			return 0, false, err
		}
		// A SAVEPOINT, not a bare Exec, and only for the quarantine below.
		// Postgres aborts the whole transaction on a failed statement, so
		// `continue` past a poison row would leave every later Exec failing
		// with 25P02 and the commit returning ErrTxCommitRollback -- the
		// container would error, and error again on every replay, which is
		// the exact wedge the quarantine exists to prevent. Rolling back to
		// the savepoint drops the one row and leaves the transaction usable.
		//
		// DO NOTHING is what makes a replayed ring buffer idempotent: the same
		// batch delivered twice re-derives the same rows at the same instants,
		// and the natural key refuses the duplicates. It is the same clause
		// InsertEvents uses, for the same reason.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return 0, false, fmt.Errorf("savepoint for restart event %s: %w", key, err)
		}
		if _, err := sp.Exec(ctx, `
			INSERT INTO events (host_id, ts, type, subject, detail, severity)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6)
			ON CONFLICT (host_id, ts, type, subject) DO NOTHING`,
			hostID, ev.TS, ev.Type, key, body, ev.Severity); err != nil {
			// Rolled back either way: the savepoint is dead the moment the
			// statement inside it failed, and leaving it open would poison
			// the commit as surely as the row would have.
			if rbErr := sp.Rollback(ctx); rbErr != nil {
				return 0, false, fmt.Errorf("roll back restart event for %s: %w", key, rbErr)
			}
			if poisonRow(err) {
				slog.Warn("dropped a restart event Postgres refused to store",
					"key", key, "type", ev.Type, "err", err)
				continue
			}
			return 0, false, fmt.Errorf("insert restart event for %s: %w", key, err)
		}
		if err := sp.Commit(ctx); err != nil {
			return 0, false, fmt.Errorf("release savepoint for restart event %s: %w", key, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("commit container %s: %w", key, err)
	}
	return id, true, nil
}

// InsertContainerSamples resolves container keys to ids and writes the rows.
func (s *Store) InsertContainerSamples(ctx context.Context, hostID int32, rows []*netrav1.ContainerSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids, err := s.resolveContainerIDs(ctx, hostID, rows)
	if err != nil {
		return 0, err
	}

	const stmt = `
		INSERT INTO container_samples (
			host_id, ts, container_id, cpu_pct, mem_used, mem_limit,
			net_rx, net_tx, io_read, io_write,
			cpu_user, cpu_system, mem_anon, mem_file, mem_shmem, mem_kernel,
			restart_count
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		          $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (host_id, ts, container_id) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		id, ok := ids[r.GetContainerKey()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id,
			r.CpuPct, int64OrNil(r.MemUsed), int64OrNil(r.MemLimit),
			r.NetRx, r.NetTx, r.IoRead, r.IoWrite,
			r.CpuUser, r.CpuSystem,
			int64OrNil(r.MemAnon), int64OrNil(r.MemFile),
			int64OrNil(r.MemShmem), int64OrNil(r.MemKernel),
			// Present on every scrape from an agent that can inspect at all --
			// it reports its cached count rather than only the freshly read
			// one, so this is a dense series rather than one point in ten.
			// NULL means the agent has no count for this container, never
			// "restarted zero times".
			int64OrNil(r.RestartCount))
	}
	return execBatch(ctx, s.pool, batch, "container sample")
}

// markerPrefix is where setup-agent.sh bind-mounts the .netra marker files
// inside the agent container. It is the path the agent MEASURES through, never
// a name a host answers to, and it must not reach this table: an operator with
// no netra anywhere on the box was shown "/netra/fs/ark is 94 % full".
//
// The same string is markerPrefix in internal/agent/collector/filesystems.go,
// where the agent stopped sending it. Restated rather than shared: the hub
// cannot import agent internals, and a package for one constant would couple
// two binaries that otherwise only meet over the wire.
//
// This is the LAST defence rather than a redundant one. A one-time repair
// migration used to strip the prefix from rows already carrying it; that is
// gone, because the schema was squashed and the database recreated, so no such
// row survives. What does survive is the agent that has not been upgraded yet
// -- the half an operator upgrades LAST -- and it re-sends the prefixed label
// on every scrape. Ingest is the only thing standing between that and a fleet
// page warning about "/netra/fs/ark is 94 % full" on a host with no netra
// anywhere on it.
const markerPrefix = "/netra/fs/"

// hostSideLabel is the label a host answers to, given what the agent sent.
//
// Anchored with TrimPrefix rather than sliced by length: /netra/fs/ is ten
// characters, and an off-by-one turns `ark` into `rk`, which is wrong in a way
// that still looks like a plausible filesystem name on the page.
func hostSideLabel(label string) string {
	return strings.TrimPrefix(label, markerPrefix)
}

// hostSideMountpoint is the mount point, or nothing if the agent only knew its
// own bind target.
//
// Dropped rather than stripped, which is where this differs from the label
// above. Stripping would turn /netra/fs/ark into `ark`, and a bare label does
// beat a container path -- but `ark` is a LABEL, and a mount point is the path
// an operator would type into df. An agent that sends only the bind target does
// not know that path, and saying so lets the COALESCE below keep whichever real
// one the hub already has -- from an earlier agent, or from the .env of the one
// that follows.
func hostSideMountpoint(mountpoint string) string {
	if strings.HasPrefix(mountpoint, markerPrefix) {
		return ""
	}
	return mountpoint
}

// resolveFilesystemIDs upserts the filesystems named in rows and returns
// label -> id, keyed on the label AS SENT so InsertFilesystemSamples looks its
// rows up unchanged: what is normalised is what gets stored, not what the
// caller passes back in.
func (s *Store) resolveFilesystemIDs(ctx context.Context, hostID int32, rows []*netrav1.FilesystemSample) (map[string]int32, error) {
	out := make(map[string]int32)
	if len(rows) == 0 {
		return out, nil
	}

	// One upsert per FILESYSTEM, not per label the batch happens to spell.
	//
	// Two spellings can arrive together: a replayed ring buffer written either
	// side of an agent upgrade carries both /netra/fs/ark and ark, and both
	// name one disk. Resolving as the rows come would let whichever appeared
	// first decide the mount point, so the marker path -- the one spelling
	// that knows no host path at all -- could silently outrank /mnt/ark on
	// batch order. Deciding what to store before storing it removes the
	// ordering question rather than answering it.
	//
	// filesystem_samples is PRIMARY KEY (host_id, ts, fs_id), so the two
	// spellings landing on one fs_id dedupe there exactly as a replayed batch
	// already does.
	type upsert struct {
		mountpoint string
		deviceID   *int64
	}
	order := make([]string, 0, len(rows))
	want := make(map[string]*upsert, len(rows))

	for _, r := range rows {
		sent := r.GetLabel()
		// The nameless are skipped AFTER normalising, not before: a bare
		// "/netra/fs/" strips to nothing, and inserting that would put a
		// filesystem with no name at all in the table -- a row the page can
		// only render as blank, carrying samples nothing can attribute.
		label := hostSideLabel(sent)
		if label == "" {
			continue
		}
		u, seen := want[label]
		if !seen {
			u = &upsert{}
			want[label] = u
			order = append(order, label)
		}
		// Last one that actually knows wins, for both columns. An agent that
		// does not know a value sends the zero one, and a row is never
		// demoted by a row that knows less than it does.
		if mp := hostSideMountpoint(r.GetMountpoint()); mp != "" {
			u.mountpoint = mp
		}
		if d := int64OrNil(r.DeviceId); d != nil {
			u.deviceID = d
		}
	}

	// See resolveContainerIDs on why the skip is keyed on attempts rather than
	// on successful resolutions: `order` holds each label once, so a label
	// Postgres refuses simply gets no entry in `out` and its samples are
	// dropped with it.
	ids := make(map[string]int32, len(order))
	for _, label := range order {
		u := want[label]

		// COALESCE(NULLIF(...)) rather than a bare EXCLUDED: an agent with no
		// AGENT_FS_MOUNTS mapping yet sends no mount point, and letting that
		// win would blank the /mnt/ark a better-informed agent established --
		// once per scrape, for as long as the two overlap. A name the hub has
		// is never replaced by no name, and device_id is guarded the same way
		// for the same reason -- st_dev is reassigned across reboots, so the
		// newest reading is the one to keep, but an absent one is not a
		// reading.
		id, ok, err := s.resolveOne(ctx, "filesystem", label, `
			INSERT INTO filesystems (host_id, label, mountpoint, device_id)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (host_id, label) DO UPDATE
			   SET mountpoint = COALESCE(NULLIF(EXCLUDED.mountpoint, ''), filesystems.mountpoint),
			       device_id  = COALESCE(EXCLUDED.device_id, filesystems.device_id)
			RETURNING id`,
			hostID, label, u.mountpoint, u.deviceID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ids[label] = id
	}

	// Keyed on the label AS SENT, so InsertFilesystemSamples looks its rows up
	// unchanged.
	for _, r := range rows {
		sent := r.GetLabel()
		if sent == "" {
			continue
		}
		if id, ok := ids[hostSideLabel(sent)]; ok {
			out[sent] = id
		}
	}
	return out, nil
}

// InsertFilesystemSamples resolves labels to filesystem ids and writes rows.
func (s *Store) InsertFilesystemSamples(ctx context.Context, hostID int32, rows []*netrav1.FilesystemSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids, err := s.resolveFilesystemIDs(ctx, hostID, rows)
	if err != nil {
		return 0, err
	}

	const stmt = `
		INSERT INTO filesystem_samples (
			host_id, ts, fs_id, total, used, free,
			inodes_total, inodes_used, read_bytes, write_bytes
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (host_id, ts, fs_id) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		id, ok := ids[r.GetLabel()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id,
			int64OrNil(r.Total), int64OrNil(r.Used), int64OrNil(r.Free),
			int64OrNil(r.InodesTotal), int64OrNil(r.InodesUsed),
			r.ReadBytes, r.WriteBytes)
	}
	n, err := execBatch(ctx, s.pool, batch, "filesystem sample")
	if err != nil {
		return n, err
	}
	if err := s.upsertFilesystemCurrent(ctx, hostID, rows, ids); err != nil {
		return n, err
	}
	return n, nil
}

// upsertFilesystemCurrent carries the newest row per filesystem into the gauge
// the fleet cell reads when the host is not talking.
//
// Its own statement rather than a column on filesystem_samples, and the whole
// argument is in 0013_filesystem_current.sql: fullness is a gauge, and a gauge
// read off a windowed grid goes blank the moment the window holds nothing --
// which for a machine that is switched off overnight is most of the time.
//
// Keyed on the RESOLVED fs_id, not on the label as sent. resolveFilesystemIDs
// collapses the two spellings of one mount -- the marker-prefixed
// /netra/fs/ark and a bare ark -- onto a single id, so a newest-map keyed on
// the label picks one spelling and can hand the gauge the older of the two
// readings for the same disk.
func (s *Store) upsertFilesystemCurrent(ctx context.Context, hostID int32, rows []*netrav1.FilesystemSample, ids map[string]int32) error {
	newest := make(map[int32]*netrav1.FilesystemSample, len(ids))
	for _, r := range rows {
		id, ok := ids[r.GetLabel()]
		if !ok {
			continue
		}
		if cur, seen := newest[id]; seen && cur.GetTsMs() >= r.GetTsMs() {
			continue
		}
		newest[id] = r
	}
	if len(newest) == 0 {
		return nil
	}

	// The WHERE guard is UpsertHostCurrent's, and it is load-bearing for the
	// same reason: an agent buffers scrapes while the hub is down and replays
	// them afterwards, in whatever order the batches land. Without it the
	// gauge takes whichever row arrived last rather than the newest one, and
	// walks backwards.
	//
	// No COALESCE on the three byte columns, unlike host_current's traffic
	// pair. The collector reads total, used and free from ONE statfs and skips
	// the filesystem entirely when it cannot (collector/filesystems.go), so a
	// NULL here means the agent genuinely could not measure -- and carrying
	// the previous bytes forward under a fresh ts is exactly the frozen
	// reading this feature is scoped away from.
	const stmt = `
		INSERT INTO filesystem_current (host_id, fs_id, ts, total, used, free)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id, fs_id) DO UPDATE
		   SET ts    = EXCLUDED.ts,
		       total = EXCLUDED.total,
		       used  = EXCLUDED.used,
		       free  = EXCLUDED.free
		 WHERE filesystem_current.ts <= EXCLUDED.ts`

	batch := &pgx.Batch{}
	for id, r := range newest {
		batch.Queue(stmt, hostID, id, tsOf(r.GetTsMs()),
			int64OrNil(r.Total), int64OrNil(r.Used), int64OrNil(r.Free))
	}
	// The error is returned rather than logged, unlike UpsertHostCurrent's.
	// That one is upserted AHEAD of the 503-capable path and so must not 503;
	// this runs inside a family whose contract is already "503 and the agent
	// replays an identical batch", and the replay is deduped by the ts guard
	// above.
	_, err := execBatch(ctx, s.pool, batch, "filesystem current")
	return err
}

// resolveDeviceIDs upserts the drives named in rows and returns device -> id.
func (s *Store) resolveDeviceIDs(ctx context.Context, hostID int32, rows []*netrav1.SmartAttribute) (map[string]int32, error) {
	out := make(map[string]int32)
	if len(rows) == 0 {
		return out, nil
	}

	// The NEWEST row per device, not the first one seen.
	//
	// last_seen is stamped from the reading's own ts rather than from now(),
	// so it survives a replay honestly: the agent's ring buffer hands over
	// hours-old scrapes after a hub outage, and now() would date every one of
	// them to the moment they happened to land -- reporting a drive as read
	// "just now" on readings taken overnight, which is the exact opposite of
	// the staleness cue the Drives table reads this column for.
	//
	// A batch can span several scrapes, so the newest wins; model and serial
	// come from that same row rather than from whichever arrived first.
	newest := make(map[string]*netrav1.SmartAttribute, len(rows))
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		name := r.GetDevice()
		if name == "" {
			continue
		}
		if prev, ok := newest[name]; !ok {
			newest[name] = r
			order = append(order, name)
		} else if r.GetTsMs() > prev.GetTsMs() {
			newest[name] = r
		}
	}

	// Iterated in first-seen order rather than over the map, so a failure
	// reports the same device twice in a row. See resolveContainerIDs on why
	// the skip is keyed on attempts rather than on successful resolutions.
	for _, name := range order {
		r := newest[name]

		// GREATEST, so an out-of-order replay cannot walk last_seen backwards
		// and hand the prune a drive that looks stale while its newest reading
		// is current.
		id, ok, err := s.resolveOne(ctx, "device", name, `
			INSERT INTO devices (host_id, device, model, serial, last_seen)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (host_id, device) DO UPDATE
			   SET model = EXCLUDED.model, serial = EXCLUDED.serial,
			       last_seen = GREATEST(devices.last_seen, EXCLUDED.last_seen)
			RETURNING id`,
			hostID, name, r.GetModel(), r.GetSerial(), tsOf(r.GetTsMs()))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out[name] = id
	}
	return out, nil
}

// InsertSmartAttributes resolves device names to ids and writes the rows.
func (s *Store) InsertSmartAttributes(ctx context.Context, hostID int32, rows []*netrav1.SmartAttribute) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids, err := s.resolveDeviceIDs(ctx, hostID, rows)
	if err != nil {
		return 0, err
	}

	const stmt = `
		INSERT INTO smart_attributes (host_id, ts, device_id, attr_id, raw, normalized)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id, ts, device_id, attr_id) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		id, ok := ids[r.GetDevice()]
		if !ok {
			continue
		}
		var normalized *int16
		if r.Normalized != nil {
			v := int16(r.GetNormalized())
			normalized = &v
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id, int16(r.GetAttrId()), r.Raw, normalized)
	}
	return execBatch(ctx, s.pool, batch, "smart attribute")
}

// --------------------------------------------------------------- inventory

// UpsertHostAddresses replaces the host's address set.
//
// scope is derived HERE, not by the agent (spec §5.2): the agent reports raw
// addresses, so the loopback/private/public rules are one implementation that
// can be corrected without redeploying every agent in the fleet.
//
// Addresses that vanished are deleted rather than left behind: an address the
// host no longer has is not inventory, it is a stale row that a subnet query
// would still match.
func (s *Store) UpsertHostAddresses(ctx context.Context, hostID int32, rows []*netrav1.HostAddress) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	// NULLIF on vrf and description, matching the metadata UPDATE in
	// ingest.go: proto3 has no absent string, so an interface with no alias
	// and one whose alias the agent could not read both arrive as "". Stored
	// verbatim, that '' is a measured empty description rather than an
	// absent one, and every `?? ABSENT` downstream is dead code -- the host
	// page rendered a column of blank cells where it meant to say "not
	// reported". vrf gets the same treatment for the same reason and is
	// currently '' for every row: sysfs cannot identify a VRF master, so the
	// addresses collector writes its documented vrfUnknown.
	const stmt = `
		INSERT INTO host_addresses (host_id, iface, if_index, address, family, scope, vrf, description)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''))
		ON CONFLICT (host_id, iface, address) DO UPDATE
		   SET if_index = EXCLUDED.if_index, scope = EXCLUDED.scope,
		       vrf = EXCLUDED.vrf, description = EXCLUDED.description,
		       last_seen = now()`

	batch := &pgx.Batch{}
	for _, r := range rows {
		var ifIndex *int32
		if r.IfIndex != nil {
			v := int32(r.GetIfIndex())
			ifIndex = &v
		}
		batch.Queue(stmt, hostID, r.GetIface(), ifIndex, r.GetAddress(),
			int16(r.GetFamily()), AddressScope(r.GetAddress()), r.GetVrf(), r.GetDescription())
	}

	n, err := execBatch(ctx, s.pool, batch, "host address")
	if err != nil {
		return 0, err
	}

	// Drop what the host no longer reports. The agent sends the whole set
	// whenever anything changes, so anything untouched by this batch is gone.
	keep := make([]string, 0, len(rows))
	for _, r := range rows {
		keep = append(keep, r.GetIface()+" "+r.GetAddress())
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM host_addresses
		 WHERE host_id = $1 AND (iface || ' ' || host(address)) <> ALL($2)`,
		hostID, keep); err != nil {
		if !poisonRow(err) {
			return 0, fmt.Errorf("prune host addresses: %w", err)
		}
		// One unstorable address in the keep set poisons the comparison. A
		// stale row outliving its address is a wrong answer to a subnet
		// query; a 503 here is a permanent wedge. Skip this round's prune and
		// let the next scrape -- which will carry the same set minus whatever
		// the quarantine dropped -- do it.
		slog.Warn("skipped the address prune: the keep set carries a value Postgres refuses",
			"host_id", hostID, "err", err)
	}

	return n, nil
}

// UpsertHostInterfaces replaces the host's interface set.
//
// The sibling of UpsertHostAddresses, and it prunes for the same reason: an
// interface the host no longer has is a stale row, not inventory. Nothing is
// derived here -- unlike scope, oper_state is the kernel's own word and the
// agent passes it through.
//
// NULLIF on every text column, matching UpsertHostAddresses: proto3 has no
// absent string, so a device with no MAC and one whose MAC could not be read
// both arrive as "". Stored verbatim that ” is a measured empty value, and
// the `?? ABSENT` in the UI becomes dead code -- a column of blank cells where
// it meant to say "not reported".
func (s *Store) UpsertHostInterfaces(ctx context.Context, hostID int32, rows []*netrav1.HostInterface) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO host_interfaces (
			host_id, iface, if_index, oper_state, speed_mbps, duplex, mtu, mac, description)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7, NULLIF($8, ''), NULLIF($9, ''))
		ON CONFLICT (host_id, iface) DO UPDATE
		   SET if_index = EXCLUDED.if_index, oper_state = EXCLUDED.oper_state,
		       speed_mbps = EXCLUDED.speed_mbps, duplex = EXCLUDED.duplex,
		       mtu = EXCLUDED.mtu, mac = EXCLUDED.mac,
		       description = EXCLUDED.description, last_seen = now()`

	batch := &pgx.Batch{}
	for _, r := range rows {
		var ifIndex *int32
		if r.IfIndex != nil {
			v := int32(r.GetIfIndex())
			ifIndex = &v
		}
		var speed *int64
		if r.SpeedMbps != nil {
			v := int64(r.GetSpeedMbps())
			speed = &v
		}
		var mtu *int32
		if r.Mtu != nil {
			v := int32(r.GetMtu())
			mtu = &v
		}
		batch.Queue(stmt, hostID, r.GetIface(), ifIndex, r.GetOperState(),
			speed, r.GetDuplex(), mtu, r.GetMac(), r.GetDescription())
	}

	n, err := execBatch(ctx, s.pool, batch, "host interface")
	if err != nil {
		return 0, err
	}

	// The agent sends the whole set whenever anything changes, so anything
	// untouched by this batch is an interface the host no longer has.
	//
	// No poisonRow escape hatch here, unlike the address prune: that one
	// exists because an INET the agent sent can be one Postgres refuses to
	// compare, and iface is plain TEXT with no such failure mode.
	keep := make([]string, 0, len(rows))
	for _, r := range rows {
		keep = append(keep, r.GetIface())
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM host_interfaces WHERE host_id = $1 AND iface <> ALL($2)`,
		hostID, keep); err != nil {
		return 0, fmt.Errorf("prune host interfaces: %w", err)
	}

	return n, nil
}

// UpsertHostPackages replaces the host's package inventory.
//
// Packages that vanished are deleted, for the reason UpsertHostAddresses
// prunes: `apt remove nginx` writes a remove row to package_events, and an
// inventory that still lists nginx contradicts it. The two halves of the same
// answer must not disagree.
//
// An EMPTY set is "the agent did not re-parse", never "this host has no
// packages", and the early return above is what keeps those apart. The
// packages collector parses only when the database mtime moved or the daily
// floor elapsed, and sends nothing otherwise; a parse error returns before
// Packages is populated, so a partial set cannot arise. The client makes the
// same guarantee on the wire: it carries the newest NON-EMPTY set of a batch
// whole rather than concatenating sets, and maxBatchRows drops entire scrapes
// rather than truncating a family. So a non-empty set is always a complete
// inventory, which is exactly what makes deleting the difference safe.
//
// The residual gap is a host that legitimately loses its last package. It
// keeps a stale row, because that is indistinguishable here from a scrape
// that did not re-parse. Closing it needs an explicit "empty" signal on the
// wire, which the protocol does not have; a host with zero packages is not a
// state a running Linux system reaches.
func (s *Store) UpsertHostPackages(ctx context.Context, hostID int32, rows []*netrav1.HostPackage) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	// version_changed_at is left out of the INSERT: a row this statement
	// creates takes the column's DEFAULT now(), and on the conflict path it
	// moves only when the version actually differs. A daily re-emit of an
	// unchanged inventory must not make every package look upgraded today.
	const stmt = `
		INSERT INTO host_packages (host_id, name, version, arch, format, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id, name, arch) DO UPDATE
		   SET version = EXCLUDED.version, format = EXCLUDED.format,
		       size_bytes = EXCLUDED.size_bytes, last_seen = now(),
		       version_changed_at = CASE
		           WHEN host_packages.version IS DISTINCT FROM EXCLUDED.version
		           THEN now() ELSE host_packages.version_changed_at END`

	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(stmt, hostID, r.GetName(), r.GetVersion(), r.GetArch(),
			r.GetFormat(), int64OrNil(r.SizeBytes))
	}

	n, err := execBatch(ctx, s.pool, batch, "host package")
	if err != nil {
		return 0, err
	}

	// Drop what the host no longer has installed. Keyed on name AND arch,
	// matching the primary key: a multiarch Debian carries the same package
	// for amd64 and i386 as two separate installations, and pruning on name
	// alone would delete one of them every time the other was reported.
	keep := make([]string, 0, len(rows))
	for _, r := range rows {
		keep = append(keep, r.GetName()+" "+r.GetArch())
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM host_packages
		 WHERE host_id = $1 AND (name || ' ' || arch) <> ALL($2)`,
		hostID, keep); err != nil {
		if !poisonRow(err) {
			return 0, fmt.Errorf("prune host packages: %w", err)
		}
		// Same trade as the address prune: a stale row beats a wedged agent.
		slog.Warn("skipped the package prune: the keep set carries a value Postgres refuses",
			"host_id", hostID, "err", err)
	}

	return n, nil
}

// InsertPackageEvents writes install, upgrade and remove events.
func (s *Store) InsertPackageEvents(ctx context.Context, hostID int32, rows []*netrav1.PackageEvent) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO package_events (host_id, ts, name, action, from_version, to_version)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id, ts, name) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetName(), r.GetAction(),
			emptyToNil(r.GetFromVersion()), emptyToNil(r.GetToVersion()))
	}
	return execBatch(ctx, s.pool, batch, "package event")
}

// InsertSystemdUnitEvents resolves unit names to ids and writes the events.
func (s *Store) InsertSystemdUnitEvents(ctx context.Context, hostID int32, rows []*netrav1.SystemdUnitEvent) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids := make(map[string]int32, len(rows))
	// See resolveContainerIDs on why the skip is keyed on attempts rather than
	// on successful resolutions.
	tried := make(map[string]bool, len(rows))
	for _, r := range rows {
		name := r.GetUnitName()
		if name == "" || tried[name] {
			continue
		}
		tried[name] = true
		id, ok, err := s.resolveOne(ctx, "systemd unit", name, `
			INSERT INTO systemd_units (host_id, unit_name) VALUES ($1, $2)
			ON CONFLICT (host_id, unit_name) DO UPDATE SET unit_name = EXCLUDED.unit_name
			RETURNING id`, hostID, name)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		ids[name] = id
	}

	// A repeat of the state already recorded is NOT a transition, and must not
	// be stored as one.
	//
	// The agent emits on change, but two paths legitimately re-send a state
	// the hub already has: the failed-only baseline goes out on every agent
	// start, and a ring replay resends whatever it was holding. Stored
	// verbatim, a crash-looping agent restarting every minute would write sixty
	// "exim4 went failed" rows an hour for a unit that has simply been failed
	// the whole time -- and read.Units counts rows in this table to decide
	// whether a unit is restarting repeatedly, so it would report a permanent
	// failure as a flap.
	//
	// Guarded per row rather than in one statement so the batch keeps its
	// per-row poison quarantine: one unstorable event must not cost the rest
	// their insert. The comparison is against the state as it stood BEFORE
	// this batch, since the columns are advanced below -- so a unit that
	// flapped A->B->A inside a single replayed batch records the B and skips
	// the trailing A. Undercounting is the safe direction for something that
	// raises a warning.
	const stmt = `
		INSERT INTO systemd_unit_events (host_id, unit_id, ts, state, substate)
		SELECT $1, $2, $3, $4, $5
		 WHERE NOT EXISTS (
		       SELECT 1 FROM systemd_units u
		        WHERE u.id = $2 AND u.host_id = $1
		          AND u.state IS NOT DISTINCT FROM $4
		          AND u.substate IS NOT DISTINCT FROM $5)
		ON CONFLICT (host_id, unit_id, ts) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		id, ok := ids[r.GetUnitName()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, id, tsOf(r.GetTsMs()), r.GetState(), emptyToNil(r.GetSubstate()))
	}
	n, err := execBatch(ctx, s.pool, batch, "systemd unit event")
	if err != nil {
		return n, err
	}

	// Advance the current-state columns from the same events.
	//
	// Without this the delta path writes only history, and a unit's state on
	// the host page would move only when the five-minute snapshot caught up --
	// turning a 60s time-to-alert into a 5-minute one, which is a regression
	// against the behaviour this whole change is meant to preserve. The
	// snapshot repairs divergence; the events are what make it fast.
	//
	// DISTINCT ON picks the newest event per unit in the batch, so a unit that
	// flapped twice inside one replay lands on where it ended up rather than
	// on whichever row the batch happened to queue last. The state_ts guard is
	// the same one ApplySystemdSnapshot uses, so a replayed batch cannot drag
	// a unit backwards past a state a later snapshot already confirmed.
	names := make([]string, 0, len(rows))
	sts := make([]string, 0, len(rows))
	subs := make([]string, 0, len(rows))
	when := make([]time.Time, 0, len(rows))
	for _, r := range rows {
		if _, ok := ids[r.GetUnitName()]; !ok {
			continue
		}
		names = append(names, r.GetUnitName())
		sts = append(sts, r.GetState())
		subs = append(subs, r.GetSubstate())
		when = append(when, tsOf(r.GetTsMs()))
	}
	if len(names) == 0 {
		return n, nil
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE systemd_units u
		   SET state = i.state, substate = NULLIF(i.substate, ''), state_ts = i.ts
		  FROM (SELECT DISTINCT ON (name) name, state, substate, ts
		          FROM unnest($2::text[], $3::text[], $4::text[], $5::timestamptz[])
		               AS t(name, state, substate, ts)
		         ORDER BY name, ts DESC) i
		 WHERE u.host_id = $1 AND u.unit_name = i.name
		   AND (u.state IS DISTINCT FROM i.state
		     OR u.substate IS DISTINCT FROM NULLIF(i.substate, ''))
		   AND (u.state_ts IS NULL OR i.ts > u.state_ts)`,
		hostID, names, sts, subs, when); err != nil {
		return n, fmt.Errorf("advance systemd unit state: %w", err)
	}
	return n, nil
}

// int64OrNil converts an optional unsigned protobuf field to the signed type
// Postgres stores, preserving "unset" as NULL.
//
// The schema uses BIGINT rather than an unsigned type because Postgres has
// none. Values this large do not occur for byte counts on real hosts.
func int64OrNil(v *uint64) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

// tsPtrOf renders an optional epoch-milliseconds field as a nullable instant.
//
// Non-positive is treated as absent rather than converted, which is a guard
// and not a nicety: zero is what an agent sends for a timestamp it does not
// have if it ever forgets to check, and stored as an instant that is 1970 --
// a container reported as having been up for fifty-odd years. The agent
// already omits the field for an unusable start time; this makes the hub
// refuse it too, because only one of the two has to be wrong.
func tsPtrOf(ms *int64) *time.Time {
	if ms == nil || *ms <= 0 {
		return nil
	}
	t := tsOf(*ms)
	return &t
}

// labelsJSON renders a container's labels for the JSONB column, keeping the
// difference between "the agent looked and found none" and "the agent never
// looked".
//
// That difference is why ContainerSample.labels is a wrapper message rather
// than a bare proto3 map: a map has no presence, so an agent too old to send
// labels and a container that genuinely carries none would arrive identically.
// A present-but-empty wrapper stores as `{}` and a missing one stores as NULL,
// and the UI words the two differently -- "none" against "not reported".
//
// A map that will not marshal is stored as NULL rather than failing the whole
// batch: labels are enrichment, and losing a host's CPU history over one
// unencodable label would be the wrong trade.
func labelsJSON(labels *netrav1.ContainerLabels) any {
	if labels == nil {
		return nil
	}
	values := labels.GetValues()
	if values == nil {
		values = map[string]string{}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		slog.Warn("container labels could not be encoded; storing none", "err", err)
		return nil
	}
	return encoded
}

// emptyToNil maps proto3's zero-value empty string to SQL NULL, so "no
// previous version" is stored as the absence of one rather than as "".
func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ApplySystemdSnapshot reconciles a host's systemd units against the complete
// set the agent just observed.
//
// This is the level-triggered half of the systemd path, and it exists because
// the event-triggered half cannot converge on its own. Events say what
// CHANGED; if the change that would correct the hub is never sent, the last
// event stands forever. Three routine things suppress it -- a unit recovering
// while the agent is down, a unit vanishing from the bus (`apt purge`), and a
// scrape lost to the ring buffer -- and each one pinned a unit at "failed"
// with no way back. A snapshot says what IS, so a divergence cannot outlive
// one.
//
// Only units that NEED ATTENTION get a row. A host runs 300-400 loaded
// services and a healthy one is almost entirely active/running daemons and
// inactive/dead oneshots; storing those would bury the row an operator is
// looking for under several hundred that say nothing. See package systemdstate
// for the rule and why the transient states are excluded from it.
//
// The whole thing is written so that an UNCHANGED snapshot performs zero
// writes -- not merely inserts no event rows, but updates no tuples, so there
// is no WAL and no dead tuple to vacuum. That is what makes sending one every
// five minutes per host affordable, and it is the property to protect if this
// function is ever edited.
func (s *Store) ApplySystemdSnapshot(ctx context.Context, hostID int32, snap *netrav1.SystemdSnapshot) (int64, error) {
	units := snap.GetUnits()
	if len(units) == 0 {
		return 0, nil
	}

	names := make([]string, 0, len(units))
	states := make([]string, 0, len(units))
	substates := make([]string, 0, len(units))
	for _, u := range units {
		if u.GetUnitName() == "" {
			continue
		}
		names = append(names, u.GetUnitName())
		states = append(states, u.GetState())
		substates = append(substates, u.GetSubstate())
	}
	if len(names) == 0 {
		return 0, nil
	}
	ts := tsOf(snap.GetTsMs())

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin systemd snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. The log, FIRST, while the old state is still readable.
	//
	// A snapshot that disagrees with the stored state means a transition
	// happened that the hub never heard about, so this writes the event that
	// went missing. Doing it after step 2 would compare the new state against
	// itself and record nothing, leaving the log claiming a unit never moved.
	//
	// Joined to systemd_units, so a unit with no row yet contributes nothing:
	// its history starts when it first becomes worth tracking.
	if _, err := tx.Exec(ctx, `
		INSERT INTO systemd_unit_events (host_id, unit_id, ts, state, substate)
		SELECT $1, u.id, $5, i.state, NULLIF(i.substate, '')
		  FROM unnest($2::text[], $3::text[], $4::text[]) AS i(name, state, substate)
		  JOIN systemd_units u ON u.host_id = $1 AND u.unit_name = i.name
		 WHERE (u.state IS DISTINCT FROM i.state
		     OR u.substate IS DISTINCT FROM NULLIF(i.substate, ''))
		   AND (u.state_ts IS NULL OR $5 > u.state_ts)
		ON CONFLICT (host_id, unit_id, ts) DO NOTHING`,
		hostID, names, states, substates, ts); err != nil {
		return 0, fmt.Errorf("insert systemd snapshot events: %w", err)
	}

	// 2. Correct the units already tracked.
	//
	// The IS DISTINCT FROM guard is what makes an unchanged snapshot free:
	// Postgres skips the row entirely rather than rewriting an identical
	// tuple. The state_ts guard makes the statement order-independent, so a
	// replayed or out-of-order batch cannot drag a unit backwards in time.
	tag, err := tx.Exec(ctx, `
		UPDATE systemd_units u
		   SET state = i.state, substate = NULLIF(i.substate, ''), state_ts = $5
		  FROM unnest($2::text[], $3::text[], $4::text[]) AS i(name, state, substate)
		 WHERE u.host_id = $1 AND u.unit_name = i.name
		   AND (u.state IS DISTINCT FROM i.state
		     OR u.substate IS DISTINCT FROM NULLIF(i.substate, ''))
		   AND (u.state_ts IS NULL OR $5 > u.state_ts)`,
		hostID, names, states, substates, ts)
	if err != nil {
		return 0, fmt.Errorf("update systemd unit state: %w", err)
	}
	n := tag.RowsAffected()

	// 3. Start tracking units that have become worth showing.
	//
	// Belt and braces: a unit that fails while the agent is running already
	// got its row from the event path. This covers the one that started
	// failing while the agent was down or while its scrapes were being
	// dropped -- the same gap the whole snapshot exists for.
	//
	// Only the state half of the rule applies here. A unit is also worth
	// showing when it is restarting repeatedly, but that is a rate measured
	// from the event log, and a unit with enough transitions to qualify has by
	// definition already been given a row by the event path that recorded
	// them.
	tag, err = tx.Exec(ctx, `
		INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
		SELECT $1, i.name, i.state, NULLIF(i.substate, ''), $5
		  FROM unnest($2::text[], $3::text[], $4::text[]) AS i(name, state, substate)
		 WHERE `+systemdstate.NotableSQL("i")+`
		ON CONFLICT (host_id, unit_name) DO NOTHING`,
		hostID, names, states, substates, ts)
	if err != nil {
		return 0, fmt.Errorf("insert notable systemd units: %w", err)
	}
	n += tag.RowsAffected()

	// 4. Drop what the host no longer has.
	//
	// This is the only thing that clears a unit which was PURGED rather than
	// repaired: `apt purge exim4` removes the unit file, systemd stops listing
	// it, and the collector -- which can only iterate units that still exist
	// -- never emits another event about it. Without this the row sits at
	// "failed" forever with nothing on the host it refers to.
	//
	// Gated on `complete`, and never on the list merely being non-empty. A
	// wedged D-Bus call sends NO snapshot at all rather than an empty one
	// (collector/systemd.go), and treating absence as emptiness here would
	// delete every tracked unit on the host on one bad scrape.
	//
	// Deleting takes the unit's events with it through the composite foreign
	// key's ON DELETE CASCADE, and read.Units COUNTS those events to decide
	// whether a unit is restarting repeatedly -- so a delete does not merely
	// drop history, it destroys the only evidence that would list the unit at
	// all. IF A SYSTEMD HISTORY VIEW IS EVER ADDED, change this to keep the
	// row with a NULL state and add a reaper to the existing
	// netra_prune_discrete_events job rather than a second job.
	//
	// Carries the same monotonic guard as steps 1 and 2, and for a reason that
	// is routine rather than theoretical. The agent holds one pending snapshot
	// and only replaces it a snapshotFloor later, so any POST made while
	// flushes are failing carries a snapshot taken at T0 alongside events from
	// T0..T5. The event path above has just created a row for a unit installed
	// and started at T3 -- a unit that did not exist when the snapshot was
	// taken and is legitimately absent from its name list. Without the guard
	// this deletes it, along with the transitions that had just been recorded
	// for it.
	if snap.GetComplete() {
		// Nested, so the poison recovery below can actually recover. A failed
		// statement leaves the WHOLE transaction aborted, and the Commit that
		// follows would come back as ErrTxCommitRollback -- discarding steps
		// 1-3 and 503ing the host into re-sending the identical batch, which
		// is precisely the wedge this branch exists to avoid. pgx turns a
		// nested Begin into a SAVEPOINT, so rolling this one back leaves the
		// work above committable.
		prune, err := tx.Begin(ctx)
		if err != nil {
			return 0, fmt.Errorf("begin systemd unit prune: %w", err)
		}
		if _, err := prune.Exec(ctx, `
			DELETE FROM systemd_units
			 WHERE host_id = $1 AND unit_name <> ALL($2)
			   AND (state_ts IS NULL OR state_ts <= $3)`,
			hostID, names, ts); err != nil {
			_ = prune.Rollback(ctx)
			if !poisonRow(err) {
				return 0, fmt.Errorf("prune systemd units: %w", err)
			}
			// One unstorable unit name poisons the comparison, exactly as it
			// does for addresses. A unit outliving its unit file is a wrong
			// answer on a page; a 503 here is a permanent wedge for this host.
			// Skip this round and let the next snapshot -- five minutes away
			// -- do it.
			slog.Warn("skipped the systemd unit prune: the keep set carries a value Postgres refuses",
				"host_id", hostID, "err", err)
		} else if err := prune.Commit(ctx); err != nil {
			return 0, fmt.Errorf("commit systemd unit prune: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit systemd snapshot: %w", err)
	}
	return n, nil
}
