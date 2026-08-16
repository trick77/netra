package read

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/trick77/netra/internal/hub/systemdstate"
)

// Event is one row of /api/v1/events.
type Event struct {
	// ID is a string because the log unions three tables with three different
	// keys: events has an integer identity, package_events is keyed
	// (host_id, ts, name) and systemd_unit_events (host_id, unit_id, ts).
	// Synthesising integers across those keyspaces invites a collision that
	// would silently drop a row from a React list; a prefixed string cannot.
	// Nothing reads it apart from the row key, so it costs nothing.
	ID       string    `json:"id"`
	HostID   int32     `json:"host_id"`
	Hostname string    `json:"hostname"`
	TS       time.Time `json:"ts"`
	Type     string    `json:"type"`
	// Subject is the thing the event is about -- an array name, a device, an
	// interface -- and is null for events about the host as a whole, such as
	// an agent upgrade.
	Subject *string `json:"subject"`
	// Detail is the event's payload, passed through as stored. Its shape is
	// the emitting collector's, not this API's.
	Detail json.RawMessage `json:"detail"`
}

// EventQuery is a parsed /api/v1/events request. A zero HostID means every
// host; a zero Since means the default window.
type EventQuery struct {
	HostID int32
	Since  time.Time
	Until  time.Time
	Type   string
	Limit  int
}

// Event query bounds. The default window is a day because the endpoint
// answers "what happened recently"; the limit exists because a fleet-wide
// query with no host filter reads a table that spans every host, and a
// crash-looping agent can put a lot in it.
const (
	defaultEventWindow = 24 * time.Hour
	defaultEventLimit  = 500
	maxEventLimit      = 5000
)

// Events returns the discrete-state log, newest first.
//
// The log is a UNION of the three tables that hold discrete state, not one:
//
//   - events, whose only producer is the mdraid collector;
//   - package_events, every install, upgrade and remove the agent diffed;
//   - systemd_unit_events, gated to the transitions that matter (below).
//
// They are separate tables because package and unit events carry structured,
// queryable columns that would be buried in jsonb (0001_init.sql), and that
// remains the right shape for storing them. It was never the right shape for
// READING them: the log answers "what happened", and a reader does not care
// which table a fact was filed in. Before this united them the log showed
// mdraid and nothing else, and package_events was read by nothing at all.
//
// The host filter is optional on purpose: mdraid degradation and a fleet-wide
// package rollout are as often read across the fleet as per host. Each branch
// rides its table's (host_id, ts DESC) index.
func (s *Service) Events(ctx context.Context, q EventQuery, now time.Time) ([]Event, error) {
	if q.HostID != 0 {
		if err := s.hostExists(ctx, q.HostID); err != nil {
			return nil, err
		}
	}

	since, until := q.Since, q.Until
	if until.IsZero() {
		until = now
	}
	if since.IsZero() {
		since = until.Add(-defaultEventWindow)
	}
	if !since.Before(until) {
		return nil, fmt.Errorf("%w: since must be before until", ErrInvalid)
	}

	limit := q.Limit
	switch {
	case limit <= 0:
		limit = defaultEventLimit
	case limit > maxEventLimit:
		// Clamped rather than rejected: a caller asking for more than the
		// cap wants as much as they can have, and newest-first ordering
		// means the rows they get are the ones they care about.
		limit = maxEventLimit
	}

	// A zero host id and an empty type mean "no filter". Encoding that in SQL
	// with a NULL parameter rather than assembling the WHERE clause in Go
	// keeps this one prepared statement whatever the request looks like.
	var hostFilter *int32
	if q.HostID != 0 {
		hostFilter = &q.HostID
	}
	var typeFilter *string
	if q.Type != "" {
		typeFilter = &q.Type
	}

	rows, err := s.pool.Query(ctx, eventsSQL, hostFilter, typeFilter, since, until, limit)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.HostID, &e.Hostname, &e.TS, &e.Type, &e.Subject, &e.Detail); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rowsErr(rows.Err(), "events")
}

// unitLookback is how far before the requested window the unit branch reads.
//
// A transition is only worth showing next to the one before it -- "entered
// failed" and "recovered to active" are the same fact seen from two sides --
// so the branch needs the row PRECEDING the window, which by definition sits
// outside it. Seven days rather than a tighter number because a unit that
// failed last Tuesday and recovers this morning must still render as a
// recovery; short of that the recovery reads as a unit appearing from nowhere
// in an unremarkable state, and gets filtered out entirely.
const unitLookback = "7 days"

// packageRunRows is how many packages of ONE apt run reach the log.
//
// The agent takes a single clock read per scrape and stamps the whole package
// diff with it (collector/packages.go), so every package touched by one
// `apt upgrade` lands with a byte-identical timestamp. (host_id, ts) is
// therefore not an approximation of "one run" -- it is exactly one run.
//
// Without a cap those rows are eligible for the whole page. A dist-upgrade
// writing 400 of them takes 400 of the 500 slots and pushes a degraded array
// off the log, which is the one thing the log exists to show. Nothing about
// the limit is unfair -- the page really does hold the newest N events -- but
// "the newest N" is the wrong question when one transaction supplies them all.
//
// Three, matching MAX_CONDITION_ROWS in ui/src/features/fleet/AttentionBand.tsx,
// which folds a host's conditions the same way and for the same reason. The
// rest are counted, not dropped: see `more` below.
const packageRunRows = 3

// eventsSQL is the log: three tables, one ordering, one limit.
//
// Each branch is parenthesised with its OWN ORDER BY and LIMIT. Both halves
// matter, but only for WORK, not for fairness: a branch limit equal to the
// outer limit means no branch can contribute more rows than the whole page
// holds, so the union never materialises more than three pages before the
// final sort. The ORDER BY is what makes that bound mean "the newest N of
// this family" rather than "whichever N the planner reached first".
//
// What it does NOT do is stop one family crowding out the others. The result
// is exactly what a single ORDER BY ts DESC LIMIT N over all three tables
// would return. For events and units that is fine -- neither can produce a
// burst. Packages can, so that branch caps its own runs; see packageRunRows.
//
// The package branch counts a run before it truncates it, which is the whole
// point and dictates the shape: the window functions run in a subquery with no
// limit of its own, and rn/run_size are filtered in the query that reads it.
// A `rn <= N` predicate pushed up beside the window would be applied before
// count(*) finished and run_size would report the truncated run -- the same
// trap the unit branch's CTE has, in a different disguise.
//
// `more` sits on rn = 1, the row of the run that renders FIRST, and the
// obvious alternative is wrong. Every kept row of a run shares one ts, so the
// outer LIMIT can cut through the middle of a run: put the marker on the last
// row and a run straddling the page boundary loses it, leaving two rows of a
// 400-package upgrade with nothing saying the other 398 happened. rn = 1
// survives whenever any row of the run does. It also means nothing here
// depends on the outer ORDER BY's direction.
//
// The unit branch is the one with a shape to get wrong. Its lag() must run
// over the WHOLE partition, so the CTE takes no limit and is bounded only by
// the widened lookback; the filter and the limit are applied afterwards, in
// the branch that reads it. A limit pushed inside the CTE would truncate the
// partition and hand the boundary row a previous_state belonging to some other
// transition -- silently, and only for the oldest row on the page, which is
// the one nobody checks.
//
// Type filtering is pushed into every branch rather than wrapped around the
// union so that asking for one family does not pay for the other two, and in
// particular so a `package` filter never evaluates the window function.
//
// No new index was needed for any of this, which is not obvious: the unit
// branch is the only one reading a table without a (host_id, ts) index, and
// adding one looked necessary until it was measured. It is not, for two
// separate reasons. Per host, systemd_unit_events_pkey already LEADS with
// host_id, so the filter rides it and skips through unit_id -- a purpose-built
// (host_id, ts DESC) index measured 3.9ms against the primary key's 4.3ms on a
// million rows, which is noise. Fleet-wide the planner will not take a ts index
// at all: the window is PARTITION BY (host_id, unit_id) ORDER BY ts, which is
// the primary key's own order, so scanning it hands the WindowAgg its input
// already sorted and any other path would have to sort a million rows first.
var eventsSQL = fmt.Sprintf(`
	WITH unit_transitions AS (
		SELECT ev.host_id, ev.unit_id, ev.ts, ev.state, ev.substate,
		       %[1]s AS notable,
		       lag(ev.state) OVER w AS prev_state,
		       lag(%[1]s)    OVER w AS prev_notable
		  FROM systemd_unit_events ev
		 WHERE ($1::integer IS NULL OR ev.host_id = $1)
		   AND ($2::text IS NULL OR $2 = 'unit')
		   AND ev.ts >= $3::timestamptz - interval '%[2]s'
		   AND ev.ts <= $4
		WINDOW w AS (PARTITION BY ev.host_id, ev.unit_id ORDER BY ev.ts)
	)
	(
		SELECT 'e:' || e.id AS id, e.host_id, coalesce(h.hostname, '') AS hostname,
		       e.ts, e.type, e.subject, e.detail
		  FROM events e
		  JOIN hosts h ON h.id = e.host_id
		 WHERE ($1::integer IS NULL OR e.host_id = $1)
		   AND ($2::text IS NULL OR e.type = $2)
		   AND e.ts >= $3 AND e.ts <= $4
		 ORDER BY e.ts DESC, e.id DESC
		 LIMIT $5
	)
	UNION ALL
	(
		SELECT 'p:' || r.host_id || ':' || r.ts || ':' || r.name AS id,
		       r.host_id, coalesce(h.hostname, '') AS hostname,
		       r.ts, 'package' AS type, r.name AS subject,
		       jsonb_strip_nulls(jsonb_build_object(
		           'action',       r.action,
		           'from_version', r.from_version,
		           'to_version',   r.to_version,
		           'run_size',     CASE WHEN r.run_size > %[3]d THEN r.run_size END,
		           'more',         CASE WHEN r.rn = 1 AND r.run_size > %[3]d
		                                THEN r.run_size - %[3]d END
		       )) AS detail
		  FROM (
		      SELECT p.host_id, p.ts, p.name, p.action,
		             p.from_version, p.to_version,
		             row_number() OVER ordered AS rn,
		             count(*)     OVER whole   AS run_size
		        FROM package_events p
		       WHERE ($1::integer IS NULL OR p.host_id = $1)
		         AND ($2::text IS NULL OR $2 = 'package')
		         AND p.ts >= $3 AND p.ts <= $4
		      -- Two windows over the same partition, and they cannot be one.
		      -- A window carrying ORDER BY gets the default frame, RANGE
		      -- BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW, so count(*) over
		      -- it is a RUNNING count -- 1, 2, 3 -- rather than the size of
		      -- the run. Every row of a 400-package upgrade would then report
		      -- run_size <= its own position, no row would exceed the cap,
		      -- and the fold would silently never appear. row_number needs
		      -- the ordering; count must not have it.
		      WINDOW ordered AS (PARTITION BY p.host_id, p.ts ORDER BY p.name DESC),
		             whole   AS (PARTITION BY p.host_id, p.ts)
		  ) r
		  JOIN hosts h ON h.id = r.host_id
		 WHERE r.rn <= %[3]d
		 ORDER BY r.ts DESC, r.name DESC
		 LIMIT $5
	)
	UNION ALL
	(
		SELECT 'u:' || t.host_id || ':' || t.unit_id || ':' || t.ts AS id,
		       t.host_id, coalesce(h.hostname, '') AS hostname,
		       t.ts, 'unit' AS type, u.unit_name AS subject,
		       jsonb_strip_nulls(jsonb_build_object(
		           'state',          t.state,
		           'substate',       t.substate,
		           'previous_state', t.prev_state,
		           'severity',       CASE WHEN t.notable THEN 'critical' END
		       )) AS detail
		  FROM unit_transitions t
		  JOIN systemd_units u ON u.id = t.unit_id AND u.host_id = t.host_id
		  JOIN hosts h ON h.id = t.host_id
		 WHERE (t.notable OR t.prev_notable)
		   AND t.ts >= $3 AND t.ts <= $4
		 ORDER BY t.ts DESC, u.unit_name
		 LIMIT $5
	)
	ORDER BY ts DESC, id DESC
	LIMIT $5`, systemdstate.NotableSQL("ev"), unitLookback, packageRunRows)
