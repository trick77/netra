package collector_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// collectEvents runs the collector and returns the events themselves, where
// collectOnce returns only their subjects.
func collectEvents(t *testing.T, testee *collector.Mdraid) []*netrav1.Event {
	t.Helper()
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return res.Events
}

// severityOfEvent returns the event's severity field, and whether its detail
// still carries a `severity` key. The key is not a channel any more -- the
// field is -- so the second return exists to assert its absence.
func severityOfEvent(t *testing.T, ev *netrav1.Event) (field string, inDetail bool) {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal([]byte(ev.GetDetailJson()), &d); err != nil {
		t.Fatalf("detail_json is not an object: %v", err)
	}
	_, inDetail = d["severity"]
	return ev.GetSeverity(), inDetail
}

// The rule this moved out of the browser.
//
// mdraidCondition in ui/src/features/events/message.ts was the only thing that
// knew a degraded array is critical, which was survivable while a person
// reading a page was the only consumer. It is not once anything else has to ask
// "what is critical" -- an alerting engine cannot call into the UI, and a
// degraded array is the single thing this collector exists to report.
func TestMdraidStatesCriticalForADegradedArray(t *testing.T) {
	root := t.TempDir()
	// array_state is `clean`, and that is the whole subtlety: the kernel calls
	// a raid1 with one disk left clean, because clean is about consistency and
	// not about how many disks are still there. testdata/mdraid/degraded says
	// the same thing.
	writeArray(t, root, "md0", map[string]string{
		"array_state": "clean",
		"level":       "raid1",
		"raid_disks":  "2",
		"degraded":    "1",
		"sync_action": "idle",
	})

	testee := collector.NewMdraid(root)

	events := collectEvents(t, testee)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	field, inDetail := severityOfEvent(t, events[0])
	if field != "critical" {
		t.Errorf("severity field = %q, want critical: nothing is rebuilding it", field)
	}
	// The field only. It rode the detail as well while the hub still read it
	// from there; the hub reads the field now, so a second copy could only
	// disagree with the first.
	if inDetail {
		t.Error("detail carries a severity key; the field is the only channel")
	}
}

// Degraded but rebuilding is a warning, not a critical: it is degraded now, and
// the kernel is already doing the thing an operator would otherwise be woken up
// to start.
func TestMdraidStatesWarningWhileRebuilding(t *testing.T) {
	root := t.TempDir()
	writeArray(t, root, "md0", map[string]string{
		"array_state": "clean",
		"level":       "raid1",
		"raid_disks":  "2",
		"degraded":    "1",
		"sync_action": "recover",
	})

	testee := collector.NewMdraid(root)

	events := collectEvents(t, testee)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if field, _ := severityOfEvent(t, events[0]); field != "warning" {
		t.Errorf("severity = %q, want warning: the array is being rebuilt", field)
	}
}

// A healthy array's baseline event is info. It still gets emitted -- the hub
// needs to know the array exists and what state it started in -- but it is not
// something anybody should be paged about.
func TestMdraidStatesInfoForAHealthyArray(t *testing.T) {
	root := t.TempDir()
	writeArray(t, root, "md0", healthyArray("clean", "idle"))

	testee := collector.NewMdraid(root)

	events := collectEvents(t, testee)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if field, _ := severityOfEvent(t, events[0]); field != "info" {
		t.Errorf("severity = %q, want info for a healthy array", field)
	}
}

// A scrub is not damage. `check` walks the array verifying parity on a healthy
// set of disks, so degraded stays 0 and the severity must not follow
// sync_action on its own.
func TestMdraidDoesNotCallAScrubDamage(t *testing.T) {
	root := t.TempDir()
	writeArray(t, root, "md0", healthyArray("clean", "check"))

	testee := collector.NewMdraid(root)

	events := collectEvents(t, testee)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if field, _ := severityOfEvent(t, events[0]); field != "info" {
		t.Errorf("severity = %q, want info: a scrub on a full array is routine", field)
	}
}

// The detail keeps the flat shape it has always had. A nested object would
// break every reader of the old shape, including the migration that backfills
// historical rows from `degraded` and `sync_action`.
func TestMdraidDetailKeepsItsFlatShape(t *testing.T) {
	root := t.TempDir()
	writeArray(t, root, "md0", healthyArray("clean", "idle"))

	testee := collector.NewMdraid(root)

	events := collectEvents(t, testee)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	var d map[string]any
	if err := json.Unmarshal([]byte(events[0].GetDetailJson()), &d); err != nil {
		t.Fatalf("detail_json: %v", err)
	}
	for _, key := range []string{"state", "level", "raid_disks", "degraded", "sync_action"} {
		if _, ok := d[key]; !ok {
			t.Errorf("detail is missing %q: %v", key, d)
		}
	}
	// severity is NOT among them: it is the array's state that lands here, and
	// the judgement about it travels in the event's own field.
	if _, ok := d["severity"]; ok {
		t.Errorf("detail carries a severity key: %v", d)
	}
}
