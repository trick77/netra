package read

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
)

// Condition is one open condition, as the fleet page reads it.
//
// The row is the truth and the events are the history -- 0016 is explicit about
// which way round that is, because the log is pruned at 90 days and a condition
// open longer than that still has to be able to say when it began. So `since`
// comes from here and is never looked up from the event that opened it.
type Condition struct {
	ID       int64  `json:"id"`
	HostID   int32  `json:"host_id"`
	Hostname string `json:"hostname"`
	Kind     string `json:"kind"`
	// Subject is a mount label for disk, a device for drive, and empty for a
	// condition about the host as a whole. Finer than the page displays: the
	// collapse to one row per host is the renderer's job.
	Subject  string `json:"subject"`
	Severity string `json:"severity"`
	// OpenedTS is the onset, walked once when the condition opened.
	OpenedTS time.Time `json:"opened_ts"`
	// OpenedAtLeast marks an onset that is a FLOOR rather than a moment,
	// because the walk hit the end of what is retained. The page says "over 7 d"
	// rather than naming a bucket where nothing happened.
	OpenedAtLeast bool `json:"opened_at_least"`
	// Detail is the condition's own numbers, shaped by the kind, for the
	// sentence the page writes.
	Detail json.RawMessage `json:"detail"`

	// MeasuredTS is when this condition's SUBJECT was last actually measured,
	// for the kinds that have a subject to measure. Null for the host-wide ones,
	// whose subject is the host and whose measurement is last_seen.
	MeasuredTS *time.Time `json:"measured_ts"`
	// Stale says the subject is present and unmeasurable: still listed, and not
	// re-read for longer than its kind allows.
	//
	// It exists because the hub deliberately never resolves such a condition.
	// A mount the agent cannot stat produces no sample at all and a hung NFS
	// export freezes its reading indefinitely, which from here is byte for byte
	// what an unmounted volume looks like -- so declaring one gone would
	// eventually declare a still-full wedged disk recovered.
	//
	// The browser used to make that go away by dropping the mount after three
	// minutes, which is the same lie told quietly. So the row is served, marked,
	// and the page says the reading is old instead of pretending the problem
	// ended.
	Stale bool `json:"stale"`
}

// ConditionsResponse is the conditions endpoint's whole body: what is wrong
// now, and the vocabulary for talking about it.
type ConditionsResponse struct {
	Conditions []Condition           `json:"conditions"`
	Kinds      []conditions.KindInfo `json:"kinds"`
}

// Conditions is every condition currently open, fleet-wide.
//
// EVERY one, with no server-side filter, and that is a decision rather than an
// omission. The counts line above the host list states how many hosts carry
// each kind, so it needs the whole set in order to count -- a ?attn= subset
// cannot produce its own counts. It is cheap because open rows are bounded by
// what is actually wrong rather than by fleet size, and the query rides
// host_conditions_open_key, which covers exactly the unresolved rows.
func (s *Service) Conditions(ctx context.Context, now time.Time) (ConditionsResponse, error) {
	// The subject's own last reading, joined per kind, because staleness is not
	// one question. A mount is measured on every scrape tick; a drive is
	// measured on whatever AGENT_SMART_INTERVAL the operator set. Kinds whose
	// subject IS the host have no join and no measured_ts: their measurement is
	// last_seen, and the silent condition already states it.
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.host_id, h.hostname, c.kind, c.subject, c.severity,
		       c.opened_ts, c.opened_at_least, c.detail,
		       CASE c.kind
		         WHEN 'disk'  THEN fc.ts
		         WHEN 'drive' THEN d.last_seen
		         -- From the condition's OWN detail, not from a join. The
		         -- reading's timestamp is refreshed on every pass that finds
		         -- the subject still bad, and frozen on a pass that could not
		         -- judge it -- which is exactly "when was this last actually
		         -- measured". A join to the newest sensor_sample would answer
		         -- the same question by scanning a hypertable once per open
		         -- row, on every fleet page load.
		         WHEN 'temperature' THEN (c.detail ->> 'measured_ts')::timestamptz
		       END AS measured_ts,
		       hc.last_seen
		  FROM host_conditions c
		  JOIN hosts h ON h.id = c.host_id
		  LEFT JOIN host_current hc ON hc.host_id = c.host_id
		  LEFT JOIN filesystems f
		         ON c.kind = 'disk' AND f.host_id = c.host_id AND f.label = c.subject
		  LEFT JOIN filesystem_current fc
		         ON fc.host_id = f.host_id AND fc.fs_id = f.id
		  LEFT JOIN devices d
		         ON c.kind = 'drive' AND d.host_id = c.host_id AND d.device = c.subject
		 WHERE c.resolved_ts IS NULL
		 ORDER BY c.host_id, c.kind, c.subject`)
	if err != nil {
		return ConditionsResponse{}, fmt.Errorf("query conditions: %w", err)
	}
	defer rows.Close()

	out := ConditionsResponse{Conditions: []Condition{}, Kinds: conditions.Catalogue()}
	for rows.Next() {
		var c Condition
		var measured, lastSeen *time.Time
		if err := rows.Scan(&c.ID, &c.HostID, &c.Hostname, &c.Kind, &c.Subject,
			&c.Severity, &c.OpenedTS, &c.OpenedAtLeast, &c.Detail,
			&measured, &lastSeen); err != nil {
			return ConditionsResponse{}, fmt.Errorf("scan condition: %w", err)
		}
		c.MeasuredTS = measured
		c.Stale = subjectIsStale(c.Kind, measured, lastSeen)
		out.Conditions = append(out.Conditions, c)
	}
	if err := rows.Err(); err != nil {
		return ConditionsResponse{}, fmt.Errorf("read conditions: %w", err)
	}
	return out, nil
}

// subjectIsStale applies the same window the scan applies, against the host's
// OWN last_seen rather than the wall clock.
//
// The comparison has to match the scan's or the page and the engine disagree
// about which readings are current -- and the host's clock is the reference for
// the reason driveIsCurrent gives: an agent with a skewed clock would otherwise
// lose its whole inventory to a fact about its NTP config.
//
// A subject with no reading at all is NOT stale. It is a condition whose kind
// has no subject to measure, or one whose subject row has not landed yet;
// neither is "measured, long ago", and marking it would put "not measured
// since" on a row that never claimed a measurement.
func subjectIsStale(kind string, measured, lastSeen *time.Time) bool {
	if measured == nil || lastSeen == nil {
		return false
	}
	switch kind {
	case conditions.KindDisk:
		return lastSeen.Sub(*measured) > conditions.StaleAfter
	case conditions.KindDrive:
		return lastSeen.Sub(*measured) > conditions.DriveStaleAfter
	default:
		// Temperature, on the same rule the disk uses: it is read from a
		// 60-second series, so a reading older than three scrapes is the same
		// "measured, long ago" a stale mount is.
		//
		// Without this the row keeps quoting a temperature as though it were
		// current. scanSensors deliberately marks a sensor that stopped
		// reporting UNJUDGED rather than vanished -- a collector that failed
		// for one pass looks exactly like a drive that was pulled -- so the
		// condition stays open and the fleet page goes on printing the last
		// number anybody saw, with nothing saying how old it is.
		//
		// `processes` and `load` are NOT here, for the reason the comment on
		// the query gives: their subject IS the host, so their staleness is
		// the host's own silence and the `silent` condition already says it in
		// full. A second "not measured since" on the load row underneath would
		// be the same fact twice.
		if kind == conditions.KindTemperature {
			return lastSeen.Sub(*measured) > conditions.StaleAfter
		}
		return false
	}
}
