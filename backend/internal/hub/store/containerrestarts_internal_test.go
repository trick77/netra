package store

import (
	"testing"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// The rules that decide whether a restart happened, and when. Pure, so every
// case is a table row rather than a database round trip -- and the cases that
// matter most are the ones that must emit NOTHING.

var base = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func at(min int) time.Time      { return base.Add(time.Duration(min) * time.Minute) }
func n(v int64) *int64          { return &v }
func tp(t time.Time) *time.Time { return &t }
func sp(s string) *string       { return &s }

func TestRestartEventsCountsEveryStep(t *testing.T) {
	// Given a stored count of 3 and a replayed batch that climbs to 6.
	prev := containerState{Count: n(3), LastSeen: tp(at(0))}
	obs := []observation{
		{TS: at(1), Count: n(4)},
		{TS: at(2), Count: n(4)},
		{TS: at(3), Count: n(6)},
	}

	// When the walk runs.
	got := restartEvents(prev, obs, sp("nginx:1.27"))

	// Then each STEP is one event carrying how far it moved -- not one event
	// per sample, and not one event for the whole batch.
	if len(got) != 2 {
		t.Fatalf("emitted %d events, want 2: %+v", len(got), got)
	}
	if got[0].TS != at(1) || got[0].Detail["delta"] != int64(1) {
		t.Errorf("first event = %v delta %v, want %v delta 1", got[0].TS, got[0].Detail["delta"], at(1))
	}
	// The corrective case for the whole design: 4 -> 6 is TWO restarts in one
	// event, and every window query must sum this rather than count rows.
	if got[1].TS != at(3) || got[1].Detail["delta"] != int64(2) {
		t.Errorf("second event = %v delta %v, want %v delta 2", got[1].TS, got[1].Detail["delta"], at(3))
	}
	for _, e := range got {
		if e.Type != EventContainerRestart {
			t.Errorf("type = %q, want %q", e.Type, EventContainerRestart)
		}
	}
}

func TestRestartEventsEmitsNothingWithoutBothSides(t *testing.T) {
	for _, tc := range []struct {
		name string
		prev containerState
		obs  []observation
	}{
		{
			// A container netra has just met has not restarted as far as
			// anyone here knows.
			"first sighting",
			containerState{},
			[]observation{{TS: at(1), Count: n(7)}},
		},
		{
			// The agent lost inspect. Its count going absent is not a restart.
			"count disappears",
			containerState{Count: n(3), LastSeen: tp(at(0))},
			[]observation{{TS: at(1), Count: nil}},
		},
		{
			// And coming back after an absence has no previous value to be
			// measured against, because the walk advanced through the nil.
			"count returns after an absence",
			containerState{Count: n(3), LastSeen: tp(at(0))},
			[]observation{{TS: at(1), Count: nil}, {TS: at(2), Count: n(9)}},
		},
		{
			"steady counter",
			containerState{Count: n(4), LastSeen: tp(at(0))},
			[]observation{{TS: at(1), Count: n(4)}, {TS: at(2), Count: n(4)}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := restartEvents(tc.prev, tc.obs, sp("nginx:1")); len(got) != 0 {
				t.Errorf("emitted %d events, want none: %+v", len(got), got)
			}
		})
	}
}

// Docker resets RestartCount for a new container, and container_key is the
// compose service, so a decrease is the same service on a new container.
func TestRestartEventsReadsADecreaseAsARedeploy(t *testing.T) {
	prev := containerState{Count: n(6), LastSeen: tp(at(0)), Image: sp("caddy:2.7-alpine")}
	obs := []observation{{TS: at(1), Count: n(0)}}

	got := restartEvents(prev, obs, sp("caddy:2-alpine"))

	if len(got) != 1 || got[0].Type != EventContainerRecreate {
		t.Fatalf("got %+v, want one %s", got, EventContainerRecreate)
	}
	if got[0].Severity != "info" {
		t.Errorf("severity = %q, want info: a deploy is an operator's own action", got[0].Severity)
	}
	if got[0].Detail["image_from"] != "caddy:2.7-alpine" || got[0].Detail["image_to"] != "caddy:2-alpine" {
		t.Errorf("detail = %v, want the image it replaced", got[0].Detail)
	}
}

// The counter can be reset to the same number it already held -- 0 replacing 0
// is the ordinary case for a service that never crashes. Without the start
// time that redeploy is invisible.
func TestRestartEventsReadsANewStartTimeAsARedeploy(t *testing.T) {
	prev := containerState{Count: n(0), StartedAt: tp(at(-60)), LastSeen: tp(at(0))}
	obs := []observation{{TS: at(1), Count: n(0), StartedAt: tp(at(1))}}

	got := restartEvents(prev, obs, sp("nginx:1"))

	if len(got) != 1 || got[0].Type != EventContainerRecreate {
		t.Fatalf("got %+v, want one %s", got, EventContainerRecreate)
	}
}

// The guard the whole design turns on. A delayed batch compared against a newer
// stored count reads as a decrease, which is the shape of a redeploy -- so an
// out-of-order delivery would fabricate one.
func TestRestartEventsIgnoresSamplesAlreadyAccountedFor(t *testing.T) {
	prev := containerState{Count: n(9), LastSeen: tp(at(10))}
	obs := []observation{
		{TS: at(3), Count: n(2)}, // stale: long since stored
		{TS: at(7), Count: n(5)}, // stale
	}

	if got := restartEvents(prev, obs, sp("nginx:1")); len(got) != 0 {
		t.Errorf("emitted %d events from samples at or before last_seen: %+v", len(got), got)
	}
}

// What collecting started_at buys: an exact instant even when the rationed
// inspect noticed the change minutes late.
func TestRestartEventsDatesFromDockerWhenItCan(t *testing.T) {
	// Given a restart Docker says happened at 12:01, noticed on the 12:09
	// sample -- the ten-scrape lag the inspect cache admits.
	prev := containerState{Count: n(1), StartedAt: tp(at(-30)), LastSeen: tp(at(0))}
	obs := []observation{{TS: at(9), Count: n(2), StartedAt: tp(at(1))}}

	got := restartEvents(prev, obs, sp("nginx:1"))

	if len(got) != 1 {
		t.Fatalf("emitted %d events, want 1", len(got))
	}
	if !got[0].TS.Equal(at(1)) {
		t.Errorf("ts = %v, want Docker's own %v rather than the sample that noticed", got[0].TS, at(1))
	}
	if got[0].Detail["ts_source"] != tsSourceStartedAt {
		t.Errorf("ts_source = %v, want %q", got[0].Detail["ts_source"], tsSourceStartedAt)
	}
	if got[0].Detail["observed_ts"] == nil {
		t.Error("observed_ts missing; a reader must be able to see the lag")
	}
}

// Without a start time the sample IS the only clock there is, and the event
// must say so: it is an upper bound, not a moment.
func TestRestartEventsFallsBackToTheSampleAndSaysSo(t *testing.T) {
	prev := containerState{Count: n(1), LastSeen: tp(at(0))}
	obs := []observation{{TS: at(5), Count: n(2)}}

	got := restartEvents(prev, obs, sp("nginx:1"))

	if len(got) != 1 || !got[0].TS.Equal(at(5)) {
		t.Fatalf("got %+v, want one event at %v", got, at(5))
	}
	if got[0].Detail["ts_source"] != tsSourceObserved {
		t.Errorf("ts_source = %v, want %q", got[0].Detail["ts_source"], tsSourceObserved)
	}
}

// Clock skew must not date an event into the future of the sample reporting it:
// a window query looking backwards would never find it.
func TestRestartEventsClampsAStartTimeAheadOfItsSample(t *testing.T) {
	prev := containerState{Count: n(1), StartedAt: tp(at(-30)), LastSeen: tp(at(0))}
	obs := []observation{{TS: at(5), Count: n(2), StartedAt: tp(at(9))}}

	got := restartEvents(prev, obs, sp("nginx:1"))

	if len(got) != 1 || !got[0].TS.Equal(at(5)) {
		t.Fatalf("got %+v, want the event clamped to the sample ts %v", got, at(5))
	}
	if got[0].Detail["ts_source"] != tsSourceObserved {
		t.Errorf("ts_source = %v, want %q once the start time is refused",
			got[0].Detail["ts_source"], tsSourceObserved)
	}
}

// The walk's meaning depends on order, and nothing in the ingest contract
// promises a batch arrives sorted.
func TestObservationsOfSortsOldestFirst(t *testing.T) {
	rows := containerSamplesFor("shop/web", at(3), at(1), at(2))
	got := observationsOf(rows, "shop/web")
	if len(got) != 3 {
		t.Fatalf("collected %d observations, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].TS.Before(got[i-1].TS) {
			t.Fatalf("observation %d (%v) precedes %d (%v)", i, got[i].TS, i-1, got[i-1].TS)
		}
	}
}

// The fast path: a container with nothing inspect-derived cannot produce a
// restart event, so it must not pay for a transaction.
func TestCarriesRestartDataOnlyWhenInspectAnswered(t *testing.T) {
	rows := containerSamplesFor("shop/web", at(1))
	if carriesRestartData(rows, "shop/web") {
		t.Error("a sample with neither field must not take the transactional path")
	}
	rows[0].RestartCount = n64(2)
	if !carriesRestartData(rows, "shop/web") {
		t.Error("a sample carrying a restart count must take the transactional path")
	}
}

// containerSamplesFor builds bare samples for one key at the given instants.
func containerSamplesFor(key string, at ...time.Time) []*netrav1.ContainerSample {
	out := make([]*netrav1.ContainerSample, 0, len(at))
	for _, t := range at {
		out = append(out, &netrav1.ContainerSample{
			ContainerKey: key,
			TsMs:         t.UnixMilli(),
		})
	}
	return out
}

func n64(v uint64) *uint64 { return &v }
