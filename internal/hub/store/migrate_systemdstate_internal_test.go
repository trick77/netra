package store

import (
	"context"
	"testing"
	"time"
)

// applySystemdStateMigration re-runs 0003 against rows seeded after Migrate
// has already recorded it, the way applyMarkerPrefixMigration re-runs 0002.
//
// The statements are idempotent by construction: ADD COLUMN IF NOT EXISTS, and
// a backfill that sets each row to the newest event it already has. Running it
// a second time is the only way to test it against the data an existing
// install actually carries, since OpenTest hands out an already-migrated
// database.
func applySystemdStateMigration(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	sql, err := migrationFS.ReadFile("migrations/0003_systemd_unit_state.sql")
	if err != nil {
		t.Fatalf("read 0003: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply 0003: %v", err)
	}
}

// Every existing host must come through the upgrade with its unit states
// intact.
//
// Before 0003 a unit's state was whatever its newest event said. The backfill
// has to reproduce exactly that, because without it every unit on every
// existing install reads as absent until its next transition -- and for a unit
// that is currently failed and staying failed, the next transition is never.
// The upgrade would silently clear precisely the warnings this change exists
// to make clearable.
func TestIntegrationSystemdStateBackfillMatchesTheNewestEvent(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Given: a host as an old hub would have left it -- units carrying history
	// in systemd_unit_events and nothing in the new columns.
	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('systemd-backfill') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	var flappy, broken, quiet int32
	for _, u := range []struct {
		name string
		into *int32
	}{
		{"flappy.service", &flappy},
		{"exim4.service", &broken},
		{"quiet.service", &quiet},
	} {
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO systemd_units (host_id, unit_name) VALUES ($1, $2) RETURNING id`,
			hostID, u.name).Scan(u.into); err != nil {
			t.Fatalf("insert %s: %v", u.name, err)
		}
	}

	now := time.Now().Truncate(time.Millisecond)
	// flappy went down and came back: the NEWEST event wins, not the first.
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO systemd_unit_events (host_id, unit_id, ts, state, substate)
		VALUES ($1, $2, $4, 'failed', 'failed'),
		       ($1, $2, $5, 'active', 'running'),
		       ($1, $3, $4, 'failed', 'failed')`,
		hostID, flappy, broken, now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	// Blank the columns Migrate already filled, so the backfill is what puts
	// them back rather than the run above.
	if _, err := s.Pool().Exec(ctx,
		`UPDATE systemd_units SET state = NULL, substate = NULL, state_ts = NULL WHERE host_id = $1`,
		hostID); err != nil {
		t.Fatalf("blank columns: %v", err)
	}

	// When.
	applySystemdStateMigration(t, ctx, s)

	// Then: each row holds what the LATERAL over systemd_unit_events would
	// have returned, and a unit with no events holds nothing rather than a
	// guess at "active".
	type row struct {
		state, substate *string
		ts              *time.Time
	}
	read := func(id int32) row {
		var r row
		if err := s.Pool().QueryRow(ctx,
			`SELECT state, substate, state_ts FROM systemd_units WHERE id = $1`, id).
			Scan(&r.state, &r.substate, &r.ts); err != nil {
			t.Fatalf("read unit %d: %v", id, err)
		}
		return r
	}

	if got := read(flappy); got.state == nil || *got.state != "active" {
		t.Errorf("flappy.service state = %v, want active -- the NEWEST event, not the first", got.state)
	}
	if got := read(broken); got.state == nil || *got.state != "failed" {
		t.Errorf("exim4.service state = %v, want failed; an upgrade that drops this "+
			"clears the very warning the release is meant to fix", got.state)
	} else if got.ts == nil || !got.ts.Equal(now.Add(-2*time.Hour).UTC()) {
		t.Errorf("exim4.service state_ts = %v, want the event's timestamp %v -- "+
			"since must stay the onset, not become upgrade time", got.ts, now.Add(-2*time.Hour).UTC())
	}
	if got := read(quiet); got.state != nil {
		t.Errorf("quiet.service state = %v, want null -- it has no events, and any "+
			"state here would be a guess", got.state)
	}
}
