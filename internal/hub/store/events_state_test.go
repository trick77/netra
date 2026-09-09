package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

func openEvents(t *testing.T) (*store.Store, int32) {
	t.Helper()
	s := store.OpenTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s, seedHost(t, s)
}

func mdraidEvent(ts time.Time, subject, detail string) *netrav1.Event {
	sev := "info"
	return &netrav1.Event{
		TsMs: ts.UnixMilli(), Type: "mdraid", Subject: subject,
		Severity: &sev, DetailJson: detail,
	}
}

func mdraidDetail(state string, degraded int, sync string) string {
	return fmt.Sprintf(
		`{"state":%q,"level":"raid1","raid_disks":2,"degraded":%d,"sync_action":%q}`,
		state, degraded, sync)
}

// eventDetails reads the host's mdraid log oldest-first.
func eventDetails(t *testing.T, s *store.Store, hostID int32) []string {
	t.Helper()
	rows, err := s.Pool().Query(context.Background(),
		`SELECT detail ->> 'state' || '/' || (detail ->> 'degraded') || '/' || (detail ->> 'sync_action')
		   FROM events WHERE host_id = $1 AND type = 'mdraid' ORDER BY ts`, hostID)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read events: %v", err)
	}
	return out
}

// A state restated is not an event.
//
// The agent emits mdraid on change, but re-reports every array on a first
// sighting -- which happens on every agent start and after a dropped-scrape
// re-arm -- and an agent older than #214 re-reports the kernel's dirty bit
// flapping as well. Stored verbatim, that filled the fleet log with the same
// `md3 clean - raid1, 2 devices` line minutes apart for an array that had not
// moved.
func TestIntegrationRepeatedEventStatesAreNotStored(t *testing.T) {
	ctx := context.Background()
	s, id := openEvents(t)
	start := time.Now().Add(-time.Hour)

	for i := range 10 {
		at := start.Add(time.Duration(i) * time.Minute)
		if _, err := s.InsertEvents(ctx, id, []*netrav1.Event{
			mdraidEvent(at, "md3", mdraidDetail("clean", 0, "idle")),
		}); err != nil {
			t.Fatalf("baseline %d: %v", i, err)
		}
	}

	got := eventDetails(t, s, id)
	if len(got) != 1 {
		t.Errorf("stored %d events (%v), want 1 -- the array became clean once and "+
			"has not moved since", len(got), got)
	}
}

// The dirty bit is not a state change.
//
// The kernel says `active` while a write is outstanding and `clean` once the
// superblock is flushed, so on an array taking writes the raw word flaps every
// few minutes -- and the agent SENDS that raw word. It normalizes only inside
// compareKey, a copy used for its own change detection, so its dedup lasts as
// long as its process: every restart and every ResendInventory re-arm reports
// each array again with whatever word is current.
func TestIntegrationMdraidDirtyBitFlapIsNotAnEvent(t *testing.T) {
	ctx := context.Background()
	s, id := openEvents(t)
	start := time.Now().Add(-time.Hour)

	for i, state := range []string{"clean", "active", "clean", "active-idle", "write-pending"} {
		at := start.Add(time.Duration(i) * time.Minute)
		if _, err := s.InsertEvents(ctx, id, []*netrav1.Event{
			mdraidEvent(at, "md3", mdraidDetail(state, 0, "idle")),
		}); err != nil {
			t.Fatalf("flap %d: %v", i, err)
		}
	}

	if got := eventDetails(t, s, id); len(got) != 1 {
		t.Errorf("stored %d events (%v), want 1 -- every one of those words means "+
			"the same healthy array", len(got), got)
	}
}

// Real transitions still record, in both directions and per array.
func TestIntegrationRealEventTransitionsAreStillRecorded(t *testing.T) {
	ctx := context.Background()
	s, id := openEvents(t)
	start := time.Now().Add(-time.Hour)

	steps := []struct {
		state    string
		degraded int
		sync     string
	}{
		{"clean", 0, "idle"},
		{"clean", 0, "idle"},    // repeat, dropped
		{"clean", 1, "recover"}, // degraded and rebuilding
		{"clean", 1, "recover"}, // repeat, dropped
		{"clean", 0, "idle"},    // recovered
		{"clean", 0, "check"},   // a scrub starting: the collector's own account of it
		{"clean", 0, "idle"},    // and its finish
	}
	for i, st := range steps {
		at := start.Add(time.Duration(i) * time.Minute)
		if _, err := s.InsertEvents(ctx, id, []*netrav1.Event{
			mdraidEvent(at, "md3", mdraidDetail(st.state, st.degraded, st.sync)),
			// A second array on the same host keeps its own history.
			mdraidEvent(at, "md0", mdraidDetail("clean", 0, "idle")),
		}); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}

	want := []string{"clean/0/idle", "clean/1/recover", "clean/0/idle", "clean/0/check", "clean/0/idle"}
	got := eventDetails(t, s, id)
	// md0 contributes exactly one row, its first sighting.
	if len(got) != len(want)+1 {
		t.Fatalf("stored %d events (%v), want %d", len(got), got, len(want)+1)
	}
}

// A replayed batch collapses inside itself, without losing what moved in it.
func TestIntegrationRepeatsWithinOneBatchCollapse(t *testing.T) {
	ctx := context.Background()
	s, id := openEvents(t)
	start := time.Now().Add(-time.Hour)

	at := func(i int) time.Time { return start.Add(time.Duration(i) * time.Minute) }
	if _, err := s.InsertEvents(ctx, id, []*netrav1.Event{
		mdraidEvent(at(0), "md3", mdraidDetail("clean", 0, "idle")),
		mdraidEvent(at(1), "md3", mdraidDetail("clean", 0, "idle")),
		mdraidEvent(at(2), "md3", mdraidDetail("clean", 1, "idle")),
		mdraidEvent(at(3), "md3", mdraidDetail("clean", 1, "idle")),
		mdraidEvent(at(4), "md3", mdraidDetail("clean", 0, "idle")),
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	want := []string{"clean/0/idle", "clean/1/idle", "clean/0/idle"}
	got := eventDetails(t, s, id)
	if len(got) != len(want) {
		t.Fatalf("stored %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Occurrences are not states, and must not be collapsed.
//
// Two identical `ata1: hard resetting link` lines are two resets, and how
// often a drive does that is the whole reading.
func TestIntegrationRepeatedOccurrenceEventsAreAllStored(t *testing.T) {
	ctx := context.Background()
	s, id := openEvents(t)
	start := time.Now().Add(-time.Hour)

	sev := "warning"
	for i := range 5 {
		at := start.Add(time.Duration(i) * time.Minute)
		if _, err := s.InsertEvents(ctx, id, []*netrav1.Event{{
			TsMs: at.UnixMilli(), Type: "ata_error", Subject: "ata1", Severity: &sev,
			DetailJson: `{"message":"ata1: hard resetting link"}`,
		}}); err != nil {
			t.Fatalf("occurrence %d: %v", i, err)
		}
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1 AND type = 'ata_error'`, id).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 5 {
		t.Errorf("stored %d ata_error rows, want 5 -- a repeat there is a second reset", n)
	}
}

// An event the hub cannot interpret is still stored.
func TestIntegrationUnreadableEventDetailIsStored(t *testing.T) {
	ctx := context.Background()
	s, id := openEvents(t)
	start := time.Now().Add(-time.Hour)

	for i := range 2 {
		at := start.Add(time.Duration(i) * time.Minute)
		if _, err := s.InsertEvents(ctx, id, []*netrav1.Event{
			mdraidEvent(at, "md3", `{"state":["not","a","string"]}`),
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1 AND type = 'mdraid'`, id).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 2 {
		t.Errorf("stored %d rows, want 2 -- a detail the guard cannot read must not be dropped", n)
	}
}
