package admin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/trick77/netra/internal/hub/notify"
)

// MaxMuteNoteBytes bounds the free-text note on a mute.
const MaxMuteNoteBytes = 500

// MaxMuteDuration is the longest timed mute. Longer is "until lifted", which
// says what it means instead of hiding a forever behind a large number.
const MaxMuteDuration = 366 * 24 * time.Hour

// NewMute is what an operator asks for.
type NewMute struct {
	HostID int32
	// Kind is a condition kind or an event type; empty mutes the whole host.
	Kind string
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
	case len(m.Note) > MaxMuteNoteBytes:
		return Mute{}, fmt.Errorf("%w: note is longer than %d bytes", ErrInvalid, MaxMuteNoteBytes)
	case m.Minutes < 0 || m.Minutes > int64(MaxMuteDuration/time.Minute):
		return Mute{}, fmt.Errorf("%w: minutes must be between 0 (until lifted) and %d",
			ErrInvalid, int64(MaxMuteDuration/time.Minute))
	}

	var until *time.Time
	if m.Minutes > 0 {
		t := now.Add(time.Duration(m.Minutes) * time.Minute)
		until = &t
	}

	var out Mute
	err := s.pool.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO alert_mutes (host_id, kind, until_ts, note, created_ts)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id, host_id, kind, until_ts, note, created_ts)
		SELECT ins.id, ins.host_id, coalesce(h.hostname, 'host ' || h.id), ins.kind, ins.until_ts,
		       ins.note, ins.created_ts
		  FROM ins JOIN hosts h ON h.id = ins.host_id`,
		m.HostID, m.Kind, until, m.Note, now).
		Scan(&out.ID, &out.HostID, &out.Hostname, &out.Kind, &out.Until, &out.Note, &out.Created)
	if err != nil {
		return Mute{}, fmt.Errorf("insert mute: %w", classify(err))
	}
	return out, nil
}

// ListMutes returns the mutes in force at now, newest first: every host's,
// or one host's when hostID is set (the host page polls only its own).
func (s *Service) ListMutes(ctx context.Context, now time.Time, hostID *int32) ([]Mute, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.host_id, coalesce(h.hostname, 'host ' || h.id), m.kind, m.until_ts, m.note, m.created_ts
		  FROM alert_mutes m
		  JOIN hosts h ON h.id = m.host_id
		 WHERE (m.until_ts IS NULL OR m.until_ts > $1)
		   AND ($2::integer IS NULL OR m.host_id = $2)
		 ORDER BY m.created_ts DESC, m.id DESC`, now, hostID)
	if err != nil {
		return nil, fmt.Errorf("query mutes: %w", err)
	}
	defer rows.Close()

	out := []Mute{}
	for rows.Next() {
		var m Mute
		if err := rows.Scan(&m.ID, &m.HostID, &m.Hostname, &m.Kind, &m.Until, &m.Note, &m.Created); err != nil {
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
