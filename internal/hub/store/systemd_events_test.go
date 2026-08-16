package store_test

import (
	"context"
	"testing"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// A re-sent state is not a restart.
//
// The agent emits on change, but two paths legitimately repeat a state the hub
// already holds: the failed-only baseline goes out on every agent start, and a
// ring replay resends what it was holding. Stored verbatim, a crash-looping
// agent restarting every minute writes sixty "exim4 went failed" rows an hour
// for a unit that has been failed the whole time -- and read.Units counts those
// rows to decide whether a unit is restarting repeatedly, so it would report a
// permanent failure as a flap. The unit is broken either way; saying the wrong
// thing about HOW is what sends someone looking in the wrong place.
func TestIntegrationRepeatedSystemdEventsAreNotTransitions(t *testing.T) {
	ctx := context.Background()
	s, id := openSystemd(t)
	start := time.Now().Add(-time.Hour)

	// Ten agent restarts, each re-announcing the same failed unit.
	for i := range 10 {
		at := start.Add(time.Duration(i) * time.Minute)
		if _, err := s.InsertSystemdUnitEvents(ctx, id, []*netrav1.SystemdUnitEvent{
			{TsMs: at.UnixMilli(), UnitName: "exim4.service", State: "failed", Substate: "failed"},
		}); err != nil {
			t.Fatalf("baseline %d: %v", i, err)
		}
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM systemd_unit_events WHERE host_id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 1 {
		t.Errorf("stored %d events, want 1 -- the unit entered failed once and has not "+
			"moved since; the other nine are the agent repeating itself", n)
	}

	// The state is still right, and its onset is still the FIRST sighting
	// rather than the most recent restart of the agent.
	var state string
	var stateTs time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT state, state_ts FROM systemd_units WHERE host_id = $1 AND unit_name = 'exim4.service'`,
		id).Scan(&state, &stateTs); err != nil {
		t.Fatalf("read unit: %v", err)
	}
	if state != "failed" {
		t.Errorf("state = %q, want failed", state)
	}
	if stateTs.After(start.Add(30 * time.Second)) {
		t.Errorf("state_ts = %v, want the FIRST sighting ~%v -- 'since' must not creep "+
			"forward every time the agent restarts", stateTs, start)
	}
}

// A genuine flip still records both halves.
//
// The dedupe above must not be so eager that it swallows real transitions --
// that would trade a false positive for a blind spot, and the transition count
// is the only thing that can see a unit which is broken without ever looking
// broken.
func TestIntegrationRealSystemdTransitionsAreStillRecorded(t *testing.T) {
	ctx := context.Background()
	s, id := openSystemd(t)
	start := time.Now().Add(-time.Hour)

	states := []struct{ state, sub string }{
		{"failed", "failed"},
		{"active", "running"},
		{"failed", "failed"},
		{"active", "running"},
	}
	for i, st := range states {
		at := start.Add(time.Duration(i) * time.Minute)
		if _, err := s.InsertSystemdUnitEvents(ctx, id, []*netrav1.SystemdUnitEvent{
			{TsMs: at.UnixMilli(), UnitName: "backup.service", State: st.state, Substate: st.sub},
		}); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM systemd_unit_events WHERE host_id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != len(states) {
		t.Errorf("stored %d events, want %d -- every flip is a real transition, and the "+
			"count of them is what identifies a unit that keeps restarting", n, len(states))
	}
}
