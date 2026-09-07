package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trick77/netra/internal/hub/conditions"
)

// eventTypeFor is the `events.type` a condition's transitions are logged under.
//
// The kind itself, so the log reads in the same vocabulary the fleet page
// filters by, and so a reader who followed a ?attn=disk link finds `disk` rows
// in the events log rather than something they have to translate.
func eventTypeFor(kind string) string { return kind }

// OpenConditions reads every condition currently open.
//
// This is the "what is firing now" query, and it rides
// host_conditions_open_key directly: the partial index covers exactly the rows
// WHERE resolved_ts IS NULL, so history costs nothing to skip however much of
// it accumulates.
func (s *Store) OpenConditions(ctx context.Context) ([]conditions.Open, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, host_id, kind, subject, severity, missing_ticks
		  FROM host_conditions
		 WHERE resolved_ts IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("query open conditions: %w", err)
	}
	defer rows.Close()

	var out []conditions.Open
	for rows.Next() {
		var o conditions.Open
		if err := rows.Scan(&o.ID, &o.Key.HostID, &o.Key.Kind, &o.Key.Subject,
			&o.Severity, &o.MissingTicks); err != nil {
			return nil, fmt.Errorf("scan open condition: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read open conditions: %w", err)
	}
	return out, nil
}

// ApplyConditions writes one evaluation pass.
//
// Everything in ONE transaction, and that is the point rather than an
// optimisation: a condition's row and the event that explains it are two
// halves of one fact. A crash between them leaves either a row nothing
// accounts for, or an "opened" event for a condition that is not open --
// and the second is worse, because the log is what alerting reads.
func (s *Store) ApplyConditions(ctx context.Context, actions []conditions.Action, now time.Time) error {
	if len(actions) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, a := range actions {
		switch {
		case a.Open != nil:
			if err := openCondition(ctx, tx, *a.Open, now); err != nil {
				return err
			}
		case a.Resolve != nil:
			if err := resolveCondition(ctx, tx, *a.Resolve, now); err != nil {
				return err
			}
		case a.Update != nil:
			if err := updateCondition(ctx, tx, *a.Update); err != nil {
				return err
			}
		}
	}

	return tx.Commit(ctx)
}

func openCondition(ctx context.Context, tx pgx.Tx, f conditions.Finding, recordedAt time.Time) error {
	detail, err := detailJSON(f.Detail)
	if err != nil {
		return err
	}

	// ON CONFLICT DO NOTHING against the partial unique index, so a second
	// evaluator -- or a retry of a transaction that committed -- cannot open
	// the same condition twice. The event below is skipped with it: writing
	// one for a row that already existed would report an onset that did not
	// happen.
	tag, err := tx.Exec(ctx, `
		INSERT INTO host_conditions
		    (host_id, kind, subject, severity, opened_ts, opened_at_least, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
		ON CONFLICT (host_id, kind, subject) WHERE resolved_ts IS NULL
		DO NOTHING`,
		f.Key.HostID, f.Key.Kind, f.Key.Subject, f.Severity,
		f.OpenedTS, f.OpenedAtLeast, detail)
	if err != nil {
		return fmt.Errorf("open condition %s/%s: %w", f.Key.Kind, f.Key.Subject, err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}

	// The event is stamped when the transition was RECORDED, and the onset
	// rides its detail.
	//
	// Back-dating it to f.OpenedTS is the obvious move and it is wrong twice.
	// A failed unit's onset is min(state_ts), which is never pruned and can be
	// years old, so the opening event would land outside
	// netra_prune_discrete_events' 90-day horizon and be deleted at the next
	// daily run -- while the condition it opened is still open. That is
	// precisely the scar 0016 cites as the reason resolved rows are kept.
	// It also hides the row from every windowed read: read/events.go bounds by
	// `e.ts >= $3 AND e.ts <= $4`, so a transition backdated a month never
	// appears in a day's log.
	//
	// The row's opened_ts is the onset and always was. This event answers
	// "when did netra conclude it", which is a different and also true fact.
	return insertConditionEvent(ctx, tx, f.Key, recordedAt, f.Severity, map[string]any{
		"transition": "opened",
		"severity":   f.Severity,
		"opened_ts":  f.OpenedTS.UTC().Format(time.RFC3339),
		// True when the onset is a floor rather than a moment, so a reader
		// knows "since" is the earliest netra can vouch for.
		"opened_at_least": f.OpenedAtLeast,
	})
}

func resolveCondition(ctx context.Context, tx pgx.Tx, r conditions.Resolution, now time.Time) error {
	var openedTS time.Time
	// RETURNING opened_ts so the cleared event can carry the moment it closes,
	// which is what lets a consumer pair the two without scanning the log for
	// a matching open. The guard on resolved_ts keeps this idempotent.
	err := tx.QueryRow(ctx, `
		UPDATE host_conditions
		   SET resolved_ts = $2, resolved_reason = $3
		 WHERE id = $1 AND resolved_ts IS NULL
		RETURNING opened_ts`, r.ID, now, r.Reason).Scan(&openedTS)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already resolved by someone else. Not an error, and emphatically not
		// a second event.
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve condition %d: %w", r.ID, err)
	}

	// A resolution is INFO whatever the condition's own severity was. The
	// thing that needs attention is the condition opening; its ending is the
	// good news, and paging on a recovery is how people learn to mute a feed.
	return insertConditionEvent(ctx, tx, r.Key, now, "info", map[string]any{
		"transition": "cleared",
		"reason":     r.Reason,
		"severity":   "info",
		// Milliseconds, matching how every other duration crosses this wire.
		"open_ms":   now.Sub(openedTS).Milliseconds(),
		"opened_ts": openedTS.UTC().Format(time.RFC3339),
	})
}

func updateCondition(ctx context.Context, tx pgx.Tx, u conditions.Update) error {
	// No event: this is a condition staying as it is. Only opening and
	// clearing are transitions, and writing a row per tick for an unchanged
	// condition is the near-constant-series waste the whole event model exists
	// to keep out of the log.
	//
	// A nil detail leaves the stored numbers alone -- see Update.Detail. The
	// COALESCE is what makes that true against a NOT NULL column.
	detail, err := detailJSON(u.Detail)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE host_conditions
		   SET severity = $2,
		       missing_ticks = $3,
		       detail = COALESCE($4::jsonb, detail)
		 WHERE id = $1 AND resolved_ts IS NULL`,
		u.ID, u.Severity, u.MissingTicks, detail); err != nil {
		return fmt.Errorf("update condition %d: %w", u.ID, err)
	}
	return nil
}

// insertConditionEvent writes a transition into the events table.
//
// Into `events` rather than a table of its own, because that table was
// designed for exactly this: 0001_init.sql calls it the general discrete-state
// table and names "SMART threshold crossings" among its contents. It also
// means the eventlog needs no new branch -- the `e:` branch already reads
// these -- and a condition's history sits in the same log as the mdraid and
// kernel events a reader is comparing it against.
func insertConditionEvent(ctx context.Context, tx pgx.Tx, key conditions.Key,
	ts time.Time, severity string, detail map[string]any) error {
	body, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("marshal condition event: %w", err)
	}

	// The natural key already carries ON CONFLICT DO NOTHING (0001_init.sql),
	// so a replayed transaction cannot double-write. Subject is NULL rather
	// than '' for a host-wide condition, matching what every other producer
	// sends and what the index's NULLS NOT DISTINCT expects.
	var subject *string
	if key.Subject != "" {
		s := key.Subject
		subject = &s
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO events (host_id, ts, type, subject, detail, severity)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		ON CONFLICT (host_id, ts, type, subject) DO NOTHING`,
		key.HostID, ts, eventTypeFor(key.Kind), subject, string(body), severity); err != nil {
		return fmt.Errorf("insert condition event: %w", err)
	}
	return nil
}

// detailJSON renders a detail map, or nil for "no detail to write".
func detailJSON(detail map[string]any) (*string, error) {
	if detail == nil {
		return nil, nil
	}
	body, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("marshal condition detail: %w", err)
	}
	s := string(body)
	return &s, nil
}
