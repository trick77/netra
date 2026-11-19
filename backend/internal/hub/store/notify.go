package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trick77/netra/internal/hub/notify"
)

// mutedSQL is true when a live mute covers the row's host, kind and subject.
// $1 is now; the host, kind, subject and condition id expressions are
// formatted in (an event passes NULL for the id). A mute bound to a condition
// covers that row only, not a reopened one with the same subject that a pass
// reads before the mute is pruned.
const mutedSQL = `EXISTS (
	SELECT 1 FROM alert_mutes m
	 WHERE m.host_id = %[1]s
	   AND (m.kind = '' OR m.kind = %[2]s)
	   AND (m.subject IS NULL OR m.subject = %[3]s)
	   AND (m.condition_id IS NULL OR m.condition_id = %[4]s)
	   AND (m.until_ts IS NULL OR m.until_ts > $1))`

// PendingConditions returns every condition the notifier owes a message.
// See notify.Store.
func (s *Store) PendingConditions(ctx context.Context, now time.Time, repeat time.Duration) ([]notify.Condition, error) {
	// A nil cutoff turns reminders off.
	var remindBefore *time.Time
	if repeat > 0 {
		t := now.Add(-repeat)
		remindBefore = &t
	}

	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.host_id, coalesce(h.hostname, 'host ' || h.id), c.kind, c.subject,
		       c.severity, c.opened_ts, c.opened_at_least, c.resolved_ts,
		       coalesce(c.resolved_reason, ''),
		       c.detail,
		       CASE
		         WHEN c.resolved_ts IS NOT NULL THEN 'cleared'
		         WHEN c.notified_ts IS NULL THEN 'opened'
		         WHEN c.notified_severity = 'warning' AND c.severity = 'critical' THEN 'escalated'
		         ELSE 'reminder'
		       END,
		       `+fmt.Sprintf(mutedSQL, "c.host_id", "c.kind", "c.subject", "c.id")+`
		  FROM host_conditions c
		  JOIN hosts h ON h.id = c.host_id
		 WHERE (c.resolved_ts IS NULL AND (
		          c.notified_ts IS NULL
		          OR (c.notified_severity = 'warning' AND c.severity = 'critical')
		          OR ($2::timestamptz IS NOT NULL AND c.notified_ts <= $2)))
		    OR (c.resolved_ts IS NOT NULL
		        AND c.notified_ts IS NOT NULL
		        AND c.cleared_notified_ts IS NULL)
		 ORDER BY c.host_id, c.opened_ts, c.id`, now, remindBefore)
	if err != nil {
		return nil, fmt.Errorf("query pending conditions: %w", err)
	}
	defer rows.Close()

	var out []notify.Condition
	for rows.Next() {
		var (
			c      notify.Condition
			detail []byte
			what   string
		)
		if err := rows.Scan(&c.ID, &c.HostID, &c.Hostname, &c.Kind, &c.Subject,
			&c.Severity, &c.OpenedTS, &c.OpenedAtLeast, &c.ResolvedTS, &c.ResolvedReason,
			&detail, &what, &c.Muted); err != nil {
			return nil, fmt.Errorf("scan pending condition: %w", err)
		}
		c.What = notify.What(what)
		c.Detail = decodeDetail(detail)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pending conditions: %w", err)
	}
	return out, nil
}

// MarkNotified records that these conditions have had their message (or,
// for a muted all-clear, have been dropped). See notify.Store.
func (s *Store) MarkNotified(ctx context.Context, items []notify.Condition, now time.Time) error {
	if len(items) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, c := range items {
		if c.What == notify.Cleared {
			batch.Queue(`
				UPDATE host_conditions SET cleared_notified_ts = $2
				 WHERE id = $1`, c.ID, now)
			continue
		}
		// The severity the message carried, not whatever the row holds by
		// now: a rise between the read and this write is then still owed.
		batch.Queue(`
			UPDATE host_conditions SET notified_ts = $2, notified_severity = $3
			 WHERE id = $1`, c.ID, now, c.Severity)
	}
	_, err := execBatch(ctx, s.pool, batch, "notified condition")
	return err
}

// CriticalEventsSince returns the critical events newer than since, condition
// transitions excluded. See notify.Store.
func (s *Store) CriticalEventsSince(ctx context.Context, now, since time.Time) ([]notify.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.host_id, coalesce(h.hostname, 'host ' || h.id), e.ts, e.type,
		       coalesce(e.subject, ''), e.severity, e.detail,
		       `+fmt.Sprintf(mutedSQL, "e.host_id", "e.type", "coalesce(e.subject, '')", "NULL")+`
		  FROM events e
		  JOIN hosts h ON h.id = e.host_id
		 WHERE e.severity = 'critical'
		   AND e.ts > $2
		   AND e.detail->>'transition' IS NULL
		   AND e.type <> $3
		 ORDER BY e.ts, e.id`, now, since, notify.EventType)
	if err != nil {
		return nil, fmt.Errorf("query critical events: %w", err)
	}
	defer rows.Close()

	var out []notify.Event
	for rows.Next() {
		var (
			e      notify.Event
			detail []byte
		)
		if err := rows.Scan(&e.ID, &e.HostID, &e.Hostname, &e.TS, &e.Type,
			&e.Subject, &e.Severity, &detail, &e.Muted); err != nil {
			return nil, fmt.Errorf("scan critical event: %w", err)
		}
		e.Detail = decodeDetail(detail)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read critical events: %w", err)
	}
	return out, nil
}

// PruneMutes deletes mutes that have run out: past their time, or bound to a
// condition that has resolved. See notify.Store.
//
// A resolved condition whose all-clear is still owed keeps its mute for one
// more pass, which reads that all-clear as muted and drops it. Pruned first,
// the all-clear would go out -- the one message the mute was asked to hold.
func (s *Store) PruneMutes(ctx context.Context, now time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM alert_mutes m
		 WHERE (m.until_ts IS NOT NULL AND m.until_ts <= $1)
		    OR EXISTS (
		       SELECT 1 FROM host_conditions c
		        WHERE c.id = m.condition_id
		          AND c.resolved_ts IS NOT NULL
		          AND (c.notified_ts IS NULL OR c.cleared_notified_ts IS NOT NULL))`, now); err != nil {
		return fmt.Errorf("prune mutes: %w", err)
	}
	return nil
}

// RecordNotification writes one message to the events log. See notify.Store.
//
// No subject: a message is about the host, and the alerts it carried are in
// the detail. Two messages for one host in the same microsecond cannot
// happen (one per host per pass), so the natural key's ON CONFLICT never
// drops one.
func (s *Store) RecordNotification(ctx context.Context, hostID int32, ts time.Time,
	severity string, n notify.Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO events (host_id, ts, type, subject, detail, severity)
		VALUES ($1, $2, $3, NULL, $4::jsonb, $5)
		ON CONFLICT (host_id, ts, type, subject) DO NOTHING`,
		hostID, ts, notify.EventType, string(body), severity); err != nil {
		return fmt.Errorf("record notification: %w", err)
	}
	return nil
}

// decodeDetail reads a jsonb detail. Not scanned straight into a map: a
// detail that is not an object would fail the scan and with it the whole
// pass, every host's message held back by one row. It only feeds a note in
// a message, so it renders as none instead.
func decodeDetail(raw []byte) map[string]any {
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	return d
}
