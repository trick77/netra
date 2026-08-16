package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// openSystemd gives a test a migrated database with one host.
func openSystemd(t *testing.T) (*store.Store, int32) {
	t.Helper()
	s := store.OpenTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s, seedHost(t, s)
}

func unitState(name, state, substate string) *netrav1.SystemdUnitState {
	return &netrav1.SystemdUnitState{UnitName: name, State: state, Substate: substate}
}

func snapshot(ts time.Time, complete bool, units ...*netrav1.SystemdUnitState) *netrav1.SystemdSnapshot {
	return &netrav1.SystemdSnapshot{TsMs: ts.UnixMilli(), Complete: complete, Units: units}
}

// unitRows reads back what the host currently tracks.
func unitRows(t *testing.T, s *store.Store, hostID int32) map[string]string {
	t.Helper()
	rows, err := s.Pool().Query(context.Background(),
		`SELECT unit_name, coalesce(state, '') FROM systemd_units WHERE host_id = $1`, hostID)
	if err != nil {
		t.Fatalf("read units: %v", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var name, state string
		if err := rows.Scan(&name, &state); err != nil {
			t.Fatalf("scan unit: %v", err)
		}
		out[name] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate units: %v", err)
	}
	return out
}

// A snapshot that never arrived must not be read as a snapshot saying nothing
// is wrong.
//
// This is the single most dangerous path in the whole feature. The prune
// deletes every unit missing from a complete snapshot, so if an absent or
// incomplete one were treated as "this host has no units", one wedged D-Bus
// call would clear every real warning on every host in the fleet -- silently,
// and looking exactly like a fix. The collector holds up its end by sending no
// snapshot at all when the lister fails (26a42a5: a wedged collector costs one
// scrape, not the agent); this is the other end.
func TestIntegrationSystemdSnapshotAbsentOrIncompleteNeverPrunes(t *testing.T) {
	ctx := context.Background()
	ts := time.Now().Truncate(time.Millisecond)

	for _, tc := range []struct {
		name string
		snap *netrav1.SystemdSnapshot
	}{
		{"no snapshot at all", nil},
		{"empty unit list", snapshot(ts, true)},
		{"explicitly not complete", snapshot(ts, false, unitState("ssh.service", "active", "running"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id := openSystemd(t)
			exec := `INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
			         VALUES ($1, 'exim4.service', 'failed', 'failed', $2)`
			if _, err := s.Pool().Exec(ctx, exec, id, ts.Add(-time.Hour)); err != nil {
				t.Fatalf("seed unit: %v", err)
			}

			if _, err := s.ApplySystemdSnapshot(ctx, id, tc.snap); err != nil {
				t.Fatalf("ApplySystemdSnapshot: %v", err)
			}

			got := unitRows(t, s, id)
			if _, ok := got["exim4.service"]; !ok {
				t.Fatal("exim4.service was deleted; absence of a snapshot is not an " +
					"empty snapshot, and one wedged scrape must not clear the fleet")
			}
			if got["exim4.service"] != "failed" {
				t.Errorf("exim4.service state = %q, want it left alone at failed", got["exim4.service"])
			}
		})
	}
}

// The three ways a unit used to get stuck at "failed" forever, each fixed by
// the snapshot. These are the bug this feature exists for.
func TestIntegrationSystemdSnapshotClearsStuckUnits(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)

	t.Run("a unit that recovered while the agent was down", func(t *testing.T) {
		// The agent restarts, its in-memory prev map is empty, and its
		// failed-only baseline says nothing about a unit that is now healthy.
		// Before the snapshot this state was permanent.
		s, id := openSystemd(t)
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
			 VALUES ($1, 'exim4.service', 'failed', 'failed', $2)`, id, now.Add(-time.Hour)); err != nil {
			t.Fatalf("seed unit: %v", err)
		}

		if _, err := s.ApplySystemdSnapshot(ctx, id,
			snapshot(now, true, unitState("exim4.service", "active", "running"))); err != nil {
			t.Fatalf("ApplySystemdSnapshot: %v", err)
		}

		if got := unitRows(t, s, id)["exim4.service"]; got != "active" {
			t.Errorf("exim4.service state = %q, want active -- the host says it recovered", got)
		}
	})

	t.Run("a unit purged from the host", func(t *testing.T) {
		// `apt purge exim4` removes the unit file, so systemd stops listing it
		// and the collector -- which can only iterate units that still exist
		// -- never emits another event about it.
		s, id := openSystemd(t)
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
			 VALUES ($1, 'exim4.service', 'failed', 'failed', $2)`, id, now.Add(-time.Hour)); err != nil {
			t.Fatalf("seed unit: %v", err)
		}

		if _, err := s.ApplySystemdSnapshot(ctx, id,
			snapshot(now, true, unitState("ssh.service", "active", "running"))); err != nil {
			t.Fatalf("ApplySystemdSnapshot: %v", err)
		}

		if _, ok := unitRows(t, s, id)["exim4.service"]; ok {
			t.Error("exim4.service survived a complete snapshot that does not list it; " +
				"nothing on the host answers to that name any more")
		}
	})

	t.Run("a unit whose failure the agent never managed to report", func(t *testing.T) {
		// The scrape carrying "exim4 went failed" was dropped by the ring
		// buffer. The snapshot has to start tracking it from scratch.
		s, id := openSystemd(t)
		if _, err := s.ApplySystemdSnapshot(ctx, id,
			snapshot(now, true, unitState("exim4.service", "failed", "failed"))); err != nil {
			t.Fatalf("ApplySystemdSnapshot: %v", err)
		}

		if got := unitRows(t, s, id)["exim4.service"]; got != "failed" {
			t.Errorf("exim4.service state = %q, want failed -- no event ever carried it", got)
		}
	})
}

// Only units worth acting on earn a row.
//
// The write side of the same rule read.Units applies. A host runs 300-400
// loaded services; storing a row for each would make the units table grow by
// two orders of magnitude per host to hold rows nobody will ever look at.
func TestIntegrationSystemdSnapshotTracksOnlyNotableUnits(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	s, id := openSystemd(t)

	if _, err := s.ApplySystemdSnapshot(ctx, id, snapshot(now, true,
		unitState("ssh.service", "active", "running"),
		unitState("oneshot.service", "inactive", "dead"),
		// Transient by definition: every service passes through activating on
		// a normal start, and minting a permanent row for one caught mid-boot
		// is what the notable rule exists to avoid.
		unitState("starting.service", "activating", "start-pre"),
		unitState("exim4.service", "failed", "failed"),
		// systemd reports a unit in its restart backoff as activating, so this
		// is only visible in the substate.
		unitState("backup.service", "activating", "auto-restart"),
	)); err != nil {
		t.Fatalf("ApplySystemdSnapshot: %v", err)
	}

	got := unitRows(t, s, id)
	if len(got) != 2 {
		t.Fatalf("tracked %d units (%v), want only exim4.service and backup.service", len(got), got)
	}
	for _, name := range []string{"exim4.service", "backup.service"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s is not tracked, and it is exactly what an operator needs to see", name)
		}
	}
}

// An unchanged snapshot must cost nothing.
//
// Not merely "writes no event rows" -- writes no TUPLES. Every five minutes,
// for every host, forever: if a no-op snapshot rewrote its rows it would
// produce WAL and dead tuples proportional to the fleet for no information at
// all, and the cadence would have to be dialled back until the repair stopped
// being useful. Asserting on xmin rather than on row counts is the point,
// because a row count is unchanged by a rewrite that touches every tuple.
func TestIntegrationSystemdSnapshotUnchangedWritesNothing(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	s, id := openSystemd(t)

	units := []*netrav1.SystemdUnitState{
		unitState("exim4.service", "failed", "failed"),
		unitState("ssh.service", "active", "running"),
	}
	if _, err := s.ApplySystemdSnapshot(ctx, id, snapshot(now, true, units...)); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}

	xmin := func() string {
		var v string
		if err := s.Pool().QueryRow(ctx,
			`SELECT xmin::text FROM systemd_units WHERE host_id = $1 AND unit_name = 'exim4.service'`,
			id).Scan(&v); err != nil {
			t.Fatalf("read xmin: %v", err)
		}
		return v
	}
	events := func() int {
		var n int
		if err := s.Pool().QueryRow(ctx,
			`SELECT count(*) FROM systemd_unit_events WHERE host_id = $1`, id).Scan(&n); err != nil {
			t.Fatalf("count events: %v", err)
		}
		return n
	}

	before, beforeEvents := xmin(), events()

	// The same truth, five minutes later, exactly as a real agent resends it.
	if _, err := s.ApplySystemdSnapshot(ctx, id,
		snapshot(now.Add(5*time.Minute), true, units...)); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}

	if after := xmin(); after != before {
		t.Errorf("the row was rewritten (xmin %s -> %s); an unchanged snapshot must not "+
			"touch a tuple, or a 5-minute cadence costs the fleet a rewrite per host", before, after)
	}
	if after := events(); after != beforeEvents {
		t.Errorf("events %d -> %d; an unchanged snapshot recorded a transition that did not happen",
			beforeEvents, after)
	}
}

// A snapshot older than what the row already knows is ignored.
//
// Both write paths carry the same monotonic guard, which is what makes them
// order-independent: a replayed batch from the agent's ring buffer can arrive
// after a snapshot that already superseded it, and without this it would drag
// the unit back to a state the host has since left.
func TestIntegrationSystemdSnapshotStaleIsIgnored(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	s, id := openSystemd(t)

	if _, err := s.ApplySystemdSnapshot(ctx, id,
		snapshot(now, true, unitState("exim4.service", "failed", "failed"))); err != nil {
		t.Fatalf("current snapshot: %v", err)
	}

	// An hour-old snapshot, arriving late, claiming the unit was fine.
	if _, err := s.ApplySystemdSnapshot(ctx, id,
		snapshot(now.Add(-time.Hour), true, unitState("exim4.service", "active", "running"))); err != nil {
		t.Fatalf("stale snapshot: %v", err)
	}

	if got := unitRows(t, s, id)["exim4.service"]; got != "failed" {
		t.Errorf("exim4.service state = %q, want failed -- a stale snapshot must not "+
			"undo a newer one", got)
	}
}

// A transition the hub never received still reaches the event log.
//
// The snapshot's job is to correct the current state, but a state that changed
// without an event would leave the log claiming the unit never moved. Writing
// the missing event is what keeps "what is it now" and "what has it done"
// telling the same story.
func TestIntegrationSystemdSnapshotRecordsTheMissedTransition(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	s, id := openSystemd(t)

	if _, err := s.ApplySystemdSnapshot(ctx, id,
		snapshot(now.Add(-time.Hour), true, unitState("exim4.service", "failed", "failed"))); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if _, err := s.ApplySystemdSnapshot(ctx, id,
		snapshot(now, true, unitState("exim4.service", "active", "running"))); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}

	var states []string
	rows, err := s.Pool().Query(ctx,
		`SELECT state FROM systemd_unit_events WHERE host_id = $1 ORDER BY ts`, id)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		states = append(states, st)
	}

	// The first snapshot found no row to compare against, so it recorded
	// nothing; the second saw failed -> active and wrote the recovery.
	if len(states) != 1 || states[0] != "active" {
		t.Errorf("events = %v, want one 'active' -- the recovery the agent never sent", states)
	}
}
