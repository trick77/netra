package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trick77/netra/internal/hub/notify"
)

// MaxMuteNoteBytes bounds the free-text note on a mute.
const MaxMuteNoteBytes = 500

// MaxMuteDuration is the longest timed mute. Longer is "until lifted", which
// says what it means instead of hiding a forever behind a large number.
const MaxMuteDuration = 366 * 24 * time.Hour

// MaxMuteSubjectBytes bounds a mute's subject: a mount path or a device name.
const MaxMuteSubjectBytes = 1024

// NewMute is what an operator asks for.
type NewMute struct {
	HostID int32
	// Kind is a condition kind or an event type; empty mutes the whole host.
	Kind string
	// Subject narrows the mute to one subject of the kind, "" being the one a
	// host-wide condition such as silent has. Nil covers every subject. Not
	// trimmed: it is matched exactly against what the hub stored.
	Subject *string
	// UntilResolved binds the mute to the condition open now on this host,
	// kind and subject, and lifts it when that condition resolves.
	UntilResolved bool
	// Minutes is how long the mute lasts; zero means until lifted. Minutes
	// rather than a Duration so an absurd value from the wire is rejected
	// here instead of overflowing on the way in.
	Minutes int64
	Note    string
}

// CreateMute stores a mute and returns it.
func (s *Service) CreateMute(ctx context.Context, m NewMute, now time.Time) (Mute, error) {
	m.Kind = strings.TrimSpace(m.Kind)
	m.Note = strings.TrimSpace(m.Note)
	switch {
	case !notify.MutableKind(m.Kind):
		return Mute{}, fmt.Errorf("%w: %q is not a condition kind or a notifying event type", ErrInvalid, m.Kind)
	case m.Subject != nil && m.Kind == "":
		return Mute{}, fmt.Errorf("%w: a subject needs a kind", ErrInvalid)
	case m.Subject != nil && len(*m.Subject) > MaxMuteSubjectBytes:
		return Mute{}, fmt.Errorf("%w: subject is longer than %d bytes", ErrInvalid, MaxMuteSubjectBytes)
	case len(m.Note) > MaxMuteNoteBytes:
		return Mute{}, fmt.Errorf("%w: note is longer than %d bytes", ErrInvalid, MaxMuteNoteBytes)
	case m.Minutes < 0 || m.Minutes > int64(MaxMuteDuration/time.Minute):
		return Mute{}, fmt.Errorf("%w: minutes must be between 0 (until lifted) and %d",
			ErrInvalid, int64(MaxMuteDuration/time.Minute))
	case m.UntilResolved && (!notify.ConditionKind(m.Kind) || m.Subject == nil):
		return Mute{}, fmt.Errorf("%w: until_resolved needs a condition kind and a subject", ErrInvalid)
	case m.UntilResolved && m.Minutes != 0:
		return Mute{}, fmt.Errorf("%w: until_resolved and minutes exclude each other", ErrInvalid)
	}

	var until *time.Time
	if m.Minutes > 0 {
		t := now.Add(time.Duration(m.Minutes) * time.Minute)
		until = &t
	}

	var conditionID *int32
	if m.UntilResolved {
		var id int32
		err := s.pool.QueryRow(ctx, `
			SELECT id FROM host_conditions
			 WHERE host_id = $1 AND kind = $2 AND subject = $3 AND resolved_ts IS NULL`,
			m.HostID, m.Kind, *m.Subject).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return Mute{}, fmt.Errorf("%w: nothing of that kind is open on that host to mute until resolved", ErrInvalid)
		}
		if err != nil {
			return Mute{}, fmt.Errorf("find condition: %w", err)
		}
		conditionID = &id
	}

	var out Mute
	err := s.pool.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO alert_mutes (host_id, kind, subject, condition_id, until_ts, note, created_ts)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id, host_id, kind, subject, condition_id, until_ts, note, created_ts)
		SELECT ins.id, ins.host_id, coalesce(h.hostname, 'host ' || h.id), ins.kind, ins.subject,
		       ins.condition_id, ins.until_ts, ins.note, ins.created_ts
		  FROM ins JOIN hosts h ON h.id = ins.host_id`,
		m.HostID, m.Kind, m.Subject, conditionID, until, m.Note, now).
		Scan(&out.ID, &out.HostID, &out.Hostname, &out.Kind, &out.Subject, &out.ConditionID,
			&out.Until, &out.Note, &out.Created)
	if err != nil {
		return Mute{}, fmt.Errorf("insert mute: %w", classify(err))
	}
	return out, nil
}

// ListMutes returns the mutes in force at now, newest first: every host's,
// or one host's when hostID is set (the host page polls only its own). A mute
// whose condition has resolved is over, even while it waits a pass to drop
// that condition's all-clear.
func (s *Service) ListMutes(ctx context.Context, now time.Time, hostID *int32) ([]Mute, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.host_id, coalesce(h.hostname, 'host ' || h.id), m.kind, m.subject,
		       m.condition_id, m.until_ts, m.note, m.created_ts
		  FROM alert_mutes m
		  JOIN hosts h ON h.id = m.host_id
		  LEFT JOIN host_conditions c ON c.id = m.condition_id
		 WHERE (m.until_ts IS NULL OR m.until_ts > $1)
		   AND c.resolved_ts IS NULL
		   AND ($2::integer IS NULL OR m.host_id = $2)
		 ORDER BY m.created_ts DESC, m.id DESC`, now, hostID)
	if err != nil {
		return nil, fmt.Errorf("query mutes: %w", err)
	}
	defer rows.Close()

	out := []Mute{}
	for rows.Next() {
		var m Mute
		if err := rows.Scan(&m.ID, &m.HostID, &m.Hostname, &m.Kind, &m.Subject, &m.ConditionID,
			&m.Until, &m.Note, &m.Created); err != nil {
			return nil, fmt.Errorf("scan mute: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read mutes: %w", err)
	}
	return out, nil
}

// DeleteMute lifts a mute.
func (s *Service) DeleteMute(ctx context.Context, id int32) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM alert_mutes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete mute: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
