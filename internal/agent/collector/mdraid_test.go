package collector_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
)

// mdraid writes events, not samples.
//
// An array is "clean" for weeks, so a 60s series saying so is the same
// near-constant-series waste that keeps systemd out of spec §5.3. Only the
// transition is worth storing (§5.1 rule 4), which is why mdraid appears in
// §5.2's list and in no §5.3 row.
func TestMdraidEmitsAnEventOnFirstSighting(t *testing.T) {
	testee := collector.NewMdraid("testdata/mdraid/clean/sys")

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(res.Events) != 1 {
		t.Fatalf("events on first sighting = %d, want 1 (the baseline)", len(res.Events))
	}
	ev := res.Events[0]
	if ev.GetType() != "mdraid" {
		t.Errorf("type = %q, want mdraid", ev.GetType())
	}
	if ev.GetSubject() != "md0" {
		t.Errorf("subject = %q, want md0", ev.GetSubject())
	}
	if ev.GetTsMs() == 0 {
		t.Error("event carries no ts_ms")
	}

	var detail map[string]any
	if err := json.Unmarshal([]byte(ev.GetDetailJson()), &detail); err != nil {
		t.Fatalf("detail is not valid JSON: %v (%q)", err, ev.GetDetailJson())
	}
	if detail["state"] != "clean" {
		t.Errorf("detail state = %v, want clean", detail["state"])
	}
	if detail["level"] != "raid1" {
		t.Errorf("detail level = %v, want raid1", detail["level"])
	}
	// degraded is the number that matters operationally, and it must survive
	// the JSON round trip as a number rather than a string.
	if detail["degraded"] != float64(0) {
		t.Errorf("detail degraded = %v, want 0", detail["degraded"])
	}
}

// An unchanged array emits nothing.
//
// Emitting on every scrape would turn `events` into the 60s series the design
// deliberately avoided, and bury the transitions that matter under weeks of
// identical rows.
func TestMdraidEmitsNothingWhileTheArrayIsUnchanged(t *testing.T) {
	testee := collector.NewMdraid("testdata/mdraid/clean/sys")

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	for i := range 3 {
		res, err := testee.Collect(context.Background())
		if err != nil {
			t.Fatalf("Collect %d: %v", i, err)
		}
		if len(res.Events) != 0 {
			t.Fatalf("scrape %d emitted %d events for an unchanged array, want 0", i+2, len(res.Events))
		}
	}
}

// The transition IS the data. A degradation must produce an event the moment
// it appears, carrying enough detail to act on without querying the host.
func TestMdraidEmitsAnEventWhenTheArrayDegrades(t *testing.T) {
	testee := collector.NewMdraid("testdata/mdraid/clean/sys")
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	testee.SetSysRootForTest("testdata/mdraid/degraded/sys")
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(res.Events) != 1 {
		t.Fatalf("events on degradation = %d, want 1", len(res.Events))
	}

	var detail map[string]any
	if err := json.Unmarshal([]byte(res.Events[0].GetDetailJson()), &detail); err != nil {
		t.Fatalf("detail is not valid JSON: %v", err)
	}
	if detail["degraded"] != float64(1) {
		t.Errorf("detail degraded = %v, want 1", detail["degraded"])
	}
	if detail["sync_action"] != "recover" {
		t.Errorf("detail sync_action = %v, want recover", detail["sync_action"])
	}

	// And the new state becomes the baseline: a degradation is reported once,
	// not once a minute for as long as it lasts.
	res, err = testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if len(res.Events) != 0 {
		t.Errorf("events while still degraded = %d, want 0", len(res.Events))
	}
}

// Recovery is a transition too. An array that comes back must say so, or the
// last thing the operator ever heard was that it broke.
func TestMdraidEmitsAnEventWhenTheArrayRecovers(t *testing.T) {
	testee := collector.NewMdraid("testdata/mdraid/degraded/sys")
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	testee.SetSysRootForTest("testdata/mdraid/clean/sys")
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("events on recovery = %d, want 1", len(res.Events))
	}
}

// A host with no md arrays is the common case. Absent, not broken.
func TestMdraidReportsNothingWithNoArrays(t *testing.T) {
	testee := collector.NewMdraid(t.TempDir())

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v, want no error when there are no arrays", err)
	}
	if len(res.Events) != 0 {
		t.Errorf("events = %d, want 0", len(res.Events))
	}
}

// The same loss the inventory collectors re-arm for. An array's transition
// lives in one scrape, and a dropped scrape leaves the hub serving the array's
// previous state permanently, because from this collector's point of view
// nothing changed afterwards.
func TestMdraidResendInventoryReEmitsEveryArray(t *testing.T) {
	// Given: a collector that has already reported what it found.
	testee := collector.NewMdraid("testdata/mdraid/clean/sys")
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("baseline Collect: %v", err)
	}
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if len(res.Events) != 0 {
		t.Fatalf("events on an unchanged scrape = %d, want 0", len(res.Events))
	}

	// When: the agent tells it a buffered scrape was lost.
	var resender collector.InventoryResender = testee
	resender.ResendInventory()

	res, err = testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect after re-arm: %v", err)
	}

	// Then: every array's state is reported again.
	if len(res.Events) != 1 {
		t.Fatalf("events after re-arm = %d, want 1", len(res.Events))
	}
	if got := res.Events[0].GetSubject(); got != "md0" {
		t.Errorf("subject = %q, want md0", got)
	}
}
