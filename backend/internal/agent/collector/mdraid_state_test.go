package collector_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
)

// writeArray lays down one md array's sysfs attributes under root, creating the
// tree on first use and overwriting on every call after -- which is what lets a
// test walk an array through a sequence of states.
func writeArray(t *testing.T, root, name string, attrs map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "block", name, "md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for k, v := range attrs {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", dir, k, err)
		}
	}
}

func healthyArray(state, sync string) map[string]string {
	return map[string]string{
		"array_state": state,
		"level":       "raid1",
		"raid_disks":  "2",
		"degraded":    "0",
		"sync_action": sync,
	}
}

// collectOnce runs the collector and returns the events it produced.
func collectOnce(t *testing.T, testee *collector.Mdraid) []string {
	t.Helper()
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := make([]string, 0, len(res.Events))
	for _, ev := range res.Events {
		out = append(out, ev.GetSubject())
	}
	return out
}

// The dirty bit is not a state change.
//
// md flips array_state between `active` and `clean` as the superblock dirty bit
// moves: `active` while a write is outstanding, `clean` once the metadata is
// flushed. During a scheduled check it does that for days. Before compareKey
// this was the ONLY thing the fleet's event log contained -- one row every few
// minutes, per array, saying the array was fine in two different words.
func TestMdraidIgnoresTheDirtyBitFlappingDuringACheck(t *testing.T) {
	root := t.TempDir()
	writeArray(t, root, "md3", healthyArray("clean", "check"))

	testee := collector.NewMdraid(root)
	if got := collectOnce(t, testee); len(got) != 1 {
		t.Fatalf("baseline events = %v, want exactly one", got)
	}

	// The exact sequence a scrubbing array produces.
	for _, state := range []string{"active", "clean", "active", "clean", "active"} {
		writeArray(t, root, "md3", healthyArray(state, "check"))
		if got := collectOnce(t, testee); len(got) != 0 {
			t.Fatalf("array_state %q emitted %v, want nothing: the array did not change", state, got)
		}
	}
}

// The scrub itself still produces exactly two events.
//
// This is the line compareKey has to walk: normalizing array_state must not
// take the scrub with it. sync_action is compared exactly, so leaving idle and
// returning to it are both changes.
func TestMdraidStillReportsAScrubStartingAndFinishing(t *testing.T) {
	root := t.TempDir()
	writeArray(t, root, "md3", healthyArray("clean", "idle"))

	testee := collector.NewMdraid(root)
	collectOnce(t, testee) // baseline

	writeArray(t, root, "md3", healthyArray("active", "check"))
	if got := collectOnce(t, testee); len(got) != 1 {
		t.Fatalf("idle -> check emitted %v, want one event (the scrub started)", got)
	}

	writeArray(t, root, "md3", healthyArray("clean", "idle"))
	if got := collectOnce(t, testee); len(got) != 1 {
		t.Fatalf("check -> idle emitted %v, want one event (the scrub finished)", got)
	}
}

// A state OUTSIDE the healthy set is a real condition and keeps its voice.
//
// The normalization collapses the words that all mean "fine". `inactive` does
// not mean fine, and an array that stops must not be silenced by the same
// change that silenced the dirty bit.
func TestMdraidStillReportsAnArrayLeavingTheHealthyStates(t *testing.T) {
	root := t.TempDir()
	writeArray(t, root, "md3", healthyArray("active", "idle"))

	testee := collector.NewMdraid(root)
	collectOnce(t, testee) // baseline

	writeArray(t, root, "md3", healthyArray("inactive", "idle"))
	if got := collectOnce(t, testee); len(got) != 1 {
		t.Fatalf("active -> inactive emitted %v, want one event", got)
	}
}

// Degrading is still reported, whatever array_state says about it.
//
// The kernel calls a raid1 with one disk left `clean`, so degraded is the only
// honest source -- and it is compared exactly.
func TestMdraidStillReportsDegradingWhileTheStateStaysClean(t *testing.T) {
	root := t.TempDir()
	writeArray(t, root, "md3", healthyArray("clean", "idle"))

	testee := collector.NewMdraid(root)
	collectOnce(t, testee) // baseline

	writeArray(t, root, "md3", map[string]string{
		"array_state": "clean",
		"level":       "raid1",
		"raid_disks":  "2",
		"degraded":    "1",
		"sync_action": "recover",
	})
	if got := collectOnce(t, testee); len(got) != 1 {
		t.Fatalf("degrading emitted %v, want one event", got)
	}
}

// The RAW state survives into the detail.
//
// Normalizing what is compared must not normalize what is stored: the exact
// word the kernel used is visible nowhere else, and an operator reading a row
// about an array that went inactive wants the kernel's own vocabulary.
func TestMdraidStoresTheRawStateItWasGiven(t *testing.T) {
	root := t.TempDir()
	writeArray(t, root, "md3", healthyArray("write-pending", "idle"))

	testee := collector.NewMdraid(root)
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(res.Events))
	}

	var detail map[string]any
	if err := json.Unmarshal([]byte(res.Events[0].GetDetailJson()), &detail); err != nil {
		t.Fatalf("detail is not valid JSON: %v", err)
	}
	if detail["state"] != "write-pending" {
		t.Errorf("detail state = %v, want the raw write-pending", detail["state"])
	}
}
