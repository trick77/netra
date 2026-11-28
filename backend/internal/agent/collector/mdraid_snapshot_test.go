package collector_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
)

// writeMember lays down one md/dev-<device> directory.
func writeMember(t *testing.T, root, array, device, state, slot string) {
	t.Helper()
	dir := filepath.Join(root, "block", array, "md", "dev-"+device)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for k, v := range map[string]string{"state": state, "slot": slot} {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", dir, k, err)
		}
	}
}

func collectResult(t *testing.T, testee *collector.Mdraid) *collector.Result {
	t.Helper()
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return res
}

// The snapshot is what /proc/mdstat says: the members, and while a check runs,
// how far along it is and how fast.
func TestMdraidSnapshotCarriesProgressAndMembers(t *testing.T) {
	// Given: md2 halfway through a check, sdc1 and sdd1 in sync.
	root := t.TempDir()
	attrs := healthyArray("clean", "check")
	attrs["sync_completed"] = "5251286464 / 11720777728"
	attrs["sync_speed"] = "96384"
	attrs["mismatch_cnt"] = "0"
	writeArray(t, root, "md2", attrs)
	writeMember(t, root, "md2", "sdd1", "in_sync", "1")
	writeMember(t, root, "md2", "sdc1", "in_sync", "0")

	// When
	res := collectResult(t, collector.NewMdraid(root))

	// Then
	arrays := res.MdSnapshot.GetArrays()
	if len(arrays) != 1 {
		t.Fatalf("snapshot arrays = %d, want 1", len(arrays))
	}
	a := arrays[0]
	if a.GetName() != "md2" || a.GetSyncAction() != "check" || a.GetRaidDisks() != 2 {
		t.Errorf("array = %v", a)
	}
	if a.GetSyncDoneSectors() != 5251286464 || a.GetSyncTotalSectors() != 11720777728 {
		t.Errorf("progress = %d / %d", a.GetSyncDoneSectors(), a.GetSyncTotalSectors())
	}
	if a.GetSyncSpeedKibs() != 96384 || a.MismatchCnt == nil {
		t.Errorf("speed = %d, mismatches = %v", a.GetSyncSpeedKibs(), a.MismatchCnt)
	}
	members := a.GetMembers()
	if len(members) != 2 || members[0].GetDevice() != "sdc1" || members[1].GetSlot() != 1 {
		t.Errorf("members = %v, want sdc1 then sdd1 in slot 1", members)
	}
}

// "none" is the kernel saying no sync runs: no figures, not zeros.
func TestMdraidSnapshotLeavesAnIdleArraysProgressUnset(t *testing.T) {
	root := t.TempDir()
	attrs := healthyArray("clean", "idle")
	attrs["sync_completed"] = "none"
	attrs["sync_speed"] = "none"
	writeArray(t, root, "md0", attrs)
	writeMember(t, root, "md0", "sdb1", "spare", "none")

	a := collectResult(t, collector.NewMdraid(root)).MdSnapshot.GetArrays()[0]

	if a.SyncDoneSectors != nil || a.SyncTotalSectors != nil || a.SyncSpeedKibs != nil {
		t.Errorf("idle array reports progress: %v", a)
	}
	if a.GetMembers()[0].Slot != nil {
		t.Errorf("a spare has slot %d, want none", a.GetMembers()[0].GetSlot())
	}
}

// A sync_completed the collector cannot read is no reading, not a guess.
func TestMdraidSnapshotIgnoresAnUnreadableSyncCompleted(t *testing.T) {
	for _, raw := range []string{"12 of 40", "x / 40", "12 / 0", "delayed"} {
		root := t.TempDir()
		attrs := healthyArray("clean", "resync")
		attrs["sync_completed"] = raw
		writeArray(t, root, "md0", attrs)

		a := collectResult(t, collector.NewMdraid(root)).MdSnapshot.GetArrays()[0]
		if a.SyncDoneSectors != nil || a.SyncTotalSectors != nil {
			t.Errorf("sync_completed %q read as %d / %d", raw, a.GetSyncDoneSectors(), a.GetSyncTotalSectors())
		}
	}
}

// No arrays: one empty snapshot at start, in case the hub still holds arrays
// lost while the agent was down, then none. The last array gone: one empty
// snapshot so the hub drops the rows, then none again.
func TestMdraidSnapshotSaysOnceThatTheLastArrayIsGone(t *testing.T) {
	root := t.TempDir()
	testee := collector.NewMdraid(root)
	if res := collectResult(t, testee); res.MdSnapshot == nil || len(res.MdSnapshot.GetArrays()) != 0 {
		t.Fatalf("first scrape without arrays: snapshot = %v, want an empty one", res.MdSnapshot)
	}
	if res := collectResult(t, testee); res.MdSnapshot != nil {
		t.Fatalf("host without arrays kept sending a snapshot: %v", res.MdSnapshot)
	}

	writeArray(t, root, "md0", healthyArray("clean", "idle"))
	if res := collectResult(t, testee); len(res.MdSnapshot.GetArrays()) != 1 {
		t.Fatalf("snapshot = %v, want md0", res.MdSnapshot)
	}

	if err := os.RemoveAll(filepath.Join(root, "block", "md0")); err != nil {
		t.Fatal(err)
	}
	res := collectResult(t, testee)
	if res.MdSnapshot == nil || len(res.MdSnapshot.GetArrays()) != 0 {
		t.Errorf("after the last array went: snapshot = %v, want an empty one", res.MdSnapshot)
	}
	if res := collectResult(t, testee); res.MdSnapshot != nil {
		t.Errorf("snapshot after the empty one = %v, want none", res.MdSnapshot)
	}
}

// Progress moving is not an event: a check would otherwise log a row a minute.
func TestMdraidMovingProgressRaisesNoEvent(t *testing.T) {
	root := t.TempDir()
	attrs := healthyArray("clean", "check")
	attrs["sync_completed"] = "100 / 1000"
	writeArray(t, root, "md0", attrs)
	testee := collector.NewMdraid(root)
	collectResult(t, testee)

	attrs["sync_completed"] = "500 / 1000"
	attrs["sync_speed"] = "120000"
	writeArray(t, root, "md0", attrs)

	if evs := collectResult(t, testee).Events; len(evs) != 0 {
		t.Errorf("progress raised %d events", len(evs))
	}
}

// Only a check or repair ending has a mismatch count to report: a rebuild
// leaves the last check's figure in place, and a frozen check counted half.
func TestMdraidMismatchesRideOnlyTheEndOfAScrub(t *testing.T) {
	for _, tc := range []struct{ from, to string }{
		{"recover", "idle"},
		{"check", "frozen"},
	} {
		root := t.TempDir()
		attrs := healthyArray("clean", tc.from)
		attrs["mismatch_cnt"] = "128"
		writeArray(t, root, "md0", attrs)
		testee := collector.NewMdraid(root)
		collectResult(t, testee)

		attrs["sync_action"] = tc.to
		writeArray(t, root, "md0", attrs)
		d := detailOf(t, collectResult(t, testee).Events[0])
		if _, ok := d["mismatch_cnt"]; ok {
			t.Errorf("%s -> %s carries mismatch_cnt: %v", tc.from, tc.to, d)
		}
	}
}

// A transition carries what the array was doing before it, and the end of a
// check carries what the check found.
func TestMdraidEventsSayWhatTheArrayWasDoing(t *testing.T) {
	root := t.TempDir()
	attrs := healthyArray("clean", "idle")
	writeArray(t, root, "md0", attrs)
	testee := collector.NewMdraid(root)

	first := detailOf(t, collectResult(t, testee).Events[0])
	if _, ok := first["prev_sync_action"]; ok {
		t.Errorf("first sighting carries prev_sync_action: %v", first)
	}

	attrs["sync_action"] = "check"
	attrs["mismatch_cnt"] = "0"
	writeArray(t, root, "md0", attrs)
	started := detailOf(t, collectResult(t, testee).Events[0])
	if started["prev_sync_action"] != "idle" {
		t.Errorf("check start: prev_sync_action = %v, want idle", started["prev_sync_action"])
	}
	if _, ok := started["mismatch_cnt"]; ok {
		t.Errorf("check start carries mismatch_cnt: %v", started)
	}

	attrs["sync_action"] = "idle"
	attrs["mismatch_cnt"] = "128"
	writeArray(t, root, "md0", attrs)
	finished := detailOf(t, collectResult(t, testee).Events[0])
	if finished["prev_sync_action"] != "check" || finished["mismatch_cnt"] != float64(128) {
		t.Errorf("check end = %v, want prev check and 128 mismatches", finished)
	}
}
