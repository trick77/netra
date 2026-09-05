package collector

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// The policy is the whole feature. RestartCount is the one field the list
// endpoint does not carry, and the reason it took this long to collect is that
// the obvious way to get it -- inspect every container, every scrape -- is
// per-container daemon work once a minute forever, which is exactly what this
// package refuses to do for /containers/{id}/stats.
//
// So these tests are about what is NOT called.

// spyInspector records every id it was asked about and answers from a table.
type spyInspector struct {
	calls  []string
	counts map[string]uint64
	err    error
}

func (s *spyInspector) inspect(_ context.Context, id string) (uint64, error) {
	s.calls = append(s.calls, id)
	if s.err != nil {
		return 0, s.err
	}
	return s.counts[id], nil
}

func (s *spyInspector) reset() { s.calls = nil }

func metaOf(ids ...string) map[string]ContainerMeta {
	out := make(map[string]ContainerMeta, len(ids))
	for _, id := range ids {
		out[id] = ContainerMeta{ID: id}
	}
	return out
}

func TestRefreshRestarts(t *testing.T) {
	ctx := context.Background()

	t.Run("inspects a container it has never seen", func(t *testing.T) {
		// Given a collector with an inspector and one unseen container.
		spy := &spyInspector{counts: map[string]uint64{"a": 3}}
		testee := &Containers{inspector: spy.inspect}

		// When the scrape refreshes.
		testee.refreshRestarts(ctx, metaOf("a"), nil)

		// Then it asked, and the answer is available to the row build.
		if len(spy.calls) != 1 {
			t.Fatalf("inspected %d times, want 1", len(spy.calls))
		}
		got, ok := testee.readRestart("a")
		if !ok || got != 3 {
			t.Errorf("readRestart = (%d, %v), want (3, true)", got, ok)
		}
	})

	// The point of the cache. A steady container is not worth a request, and a
	// fleet of two hundred steady containers is not worth two hundred.
	t.Run("asks nothing more about a container that stayed put", func(t *testing.T) {
		// Given a container already inspected once.
		spy := &spyInspector{counts: map[string]uint64{"a": 3}}
		testee := &Containers{inspector: spy.inspect}
		testee.refreshRestarts(ctx, metaOf("a"), nil)
		spy.reset()

		// When several more scrapes pass with nothing happening.
		for range restartRefreshEvery - 2 {
			testee.refreshRestarts(ctx, metaOf("a"), nil)
		}

		// Then the daemon was not asked again.
		if len(spy.calls) != 0 {
			t.Errorf("inspected %d times on a steady container, want 0", len(spy.calls))
		}
	})

	// The free detector: Collect already computes "this cgroup's counters went
	// backwards" so it can refuse to rate a negative delta, and that condition
	// IS a restart. Reusing it is what makes a restart visible on the next
	// scrape rather than up to ten scrapes later.
	t.Run("inspects again when the cgroup was recreated", func(t *testing.T) {
		// Given a container already inspected.
		spy := &spyInspector{counts: map[string]uint64{"a": 3}}
		testee := &Containers{inspector: spy.inspect}
		testee.refreshRestarts(ctx, metaOf("a"), nil)
		spy.reset()
		spy.counts["a"] = 4

		// When the next scrape reports its counters went backwards.
		testee.refreshRestarts(ctx, metaOf("a"), map[string]bool{"a": true})

		// Then it was asked, and the new count is what the row build sees.
		if len(spy.calls) != 1 {
			t.Fatalf("inspected %d times after a recreate, want 1", len(spy.calls))
		}
		if got, _ := testee.readRestart("a"); got != 4 {
			t.Errorf("readRestart = %d, want 4", got)
		}
	})

	// A wedged daemon answers each request in the client's 5s timeout, and the
	// scrape interval is 60s. Twenty is already over budget; unbounded is a
	// collector that never returns.
	t.Run("bounds how many it inspects in one scrape", func(t *testing.T) {
		// Given far more unseen containers than the per-scrape cap.
		spy := &spyInspector{counts: map[string]uint64{}}
		testee := &Containers{inspector: spy.inspect}
		ids := make([]string, 0, maxInspectsPerScrape*3)
		for i := range maxInspectsPerScrape * 3 {
			ids = append(ids, fmt.Sprintf("c%03d", i))
		}

		// When one scrape refreshes.
		testee.refreshRestarts(ctx, metaOf(ids...), nil)

		// Then it stopped at the cap.
		if len(spy.calls) != maxInspectsPerScrape {
			t.Errorf("inspected %d times, want the cap of %d", len(spy.calls), maxInspectsPerScrape)
		}

		// And the ones it skipped are still uncached, so the next scrape takes
		// them -- deferred, not dropped.
		spy.reset()
		testee.refreshRestarts(ctx, metaOf(ids...), nil)
		if len(spy.calls) != maxInspectsPerScrape {
			t.Errorf("second scrape inspected %d times, want %d", len(spy.calls), maxInspectsPerScrape)
		}
	})

	// A failed inspect must leave NO entry. Writing a zero would report "this
	// container has never restarted", which is the reading an operator most
	// wants to be able to trust.
	t.Run("reports nothing rather than zero when the daemon refuses", func(t *testing.T) {
		// Given an inspector that fails.
		spy := &spyInspector{err: errors.New("permission denied")}
		testee := &Containers{inspector: spy.inspect}

		// When the scrape refreshes.
		testee.refreshRestarts(ctx, metaOf("a"), nil)

		// Then the container carries no restart count at all.
		if _, ok := testee.readRestart("a"); ok {
			t.Error("readRestart returned a value after a failed inspect")
		}
	})

	// An agent that has just been refused is no longer in a position to assert
	// a restart count. Holding the last one it read would put "Restarts: 12" on
	// a page whose State and Health both correctly say "not reported" -- the
	// stale-assertion failure the overwrite rule for those two exists to stop.
	t.Run("forgets a count it can no longer read", func(t *testing.T) {
		// Given a container whose count was read once.
		spy := &spyInspector{counts: map[string]uint64{"a": 12}}
		testee := &Containers{inspector: spy.inspect}
		testee.refreshRestarts(ctx, metaOf("a"), nil)
		if _, ok := testee.readRestart("a"); !ok {
			t.Fatal("no count cached after a successful inspect")
		}

		// When the daemon starts refusing and the container is asked again.
		spy.err = errors.New("403 Forbidden")
		testee.refreshRestarts(ctx, metaOf("a"), map[string]bool{"a": true})

		// Then the stale number is gone rather than kept.
		if got, ok := testee.readRestart("a"); ok {
			t.Errorf("readRestart = %d after the daemon refused; the agent must stop asserting it", got)
		}
	})

	// Between inspects the CACHED value is what every scrape reports. A series
	// carrying a point one time in ten is not a series, and the whole reason
	// restart_count is per-sample is so a hole in the charts can be attributed.
	t.Run("keeps answering between inspects", func(t *testing.T) {
		// Given a container inspected once.
		spy := &spyInspector{counts: map[string]uint64{"a": 5}}
		testee := &Containers{inspector: spy.inspect}
		testee.refreshRestarts(ctx, metaOf("a"), nil)

		// When later scrapes do not inspect it.
		for range 3 {
			testee.refreshRestarts(ctx, metaOf("a"), nil)
		}

		// Then the count is still available to the row build.
		if got, ok := testee.readRestart("a"); !ok || got != 5 {
			t.Errorf("readRestart = (%d, %v), want (5, true) between inspects", got, ok)
		}
	})

	// Every attempt failing is a socket proxied read-only past /containers/json
	// -- a real deployment. Saying so is what stops the page rendering an empty
	// Restarts field with no explanation.
	t.Run("says why when the daemon refuses every inspect", func(t *testing.T) {
		// Given an inspector that always fails.
		spy := &spyInspector{err: errors.New("403 Forbidden")}
		testee := &Containers{inspector: spy.inspect}

		// When enough consecutive scrapes fail to rule out a transient.
		for range noInspectAfterScrapes {
			testee.refreshRestarts(ctx, metaOf("a", "b"), nil)
		}

		// Then the capability names the reason.
		if got := testee.Capabilities()["container_restarts"]; got != capRestartsNoInspect {
			t.Errorf("container_restarts = %q, want %q", got, capRestartsNoInspect)
		}
	})

	// A container removed between the list and the inspect answers 404, and in
	// steady state a scrape attempts one or two inspects -- so "every attempt
	// failed" is reached by an ordinary Tuesday. Reporting on the first one
	// made the capability badge flap on and off.
	t.Run("does not blame the daemon for one failed inspect", func(t *testing.T) {
		// Given an inspector that fails once and then works.
		spy := &spyInspector{err: errors.New("404 No such container")}
		testee := &Containers{inspector: spy.inspect}

		// When a single scrape hits it.
		testee.refreshRestarts(ctx, metaOf("a"), nil)

		// Then nothing is claimed about the daemon.
		if got, ok := testee.Capabilities()["container_restarts"]; ok {
			t.Errorf("container_restarts = %q after ONE failure; a removed container is not a broken daemon", got)
		}
	})

	// A failed inspect writes no cache entry, so without a back-off every
	// container stays uncached and is retried every scrape: 20 rejected
	// requests a minute, forever, against a socket that will never answer.
	t.Run("backs off a daemon that will not answer", func(t *testing.T) {
		// Given a daemon refusing everything.
		spy := &spyInspector{err: errors.New("403 Forbidden")}
		testee := &Containers{inspector: spy.inspect}
		meta := metaOf("a", "b", "c")
		for range noInspectAfterScrapes {
			testee.refreshRestarts(ctx, meta, nil)
		}
		spy.reset()

		// When many more scrapes pass.
		for range backoffScrapes * 2 {
			testee.refreshRestarts(ctx, meta, nil)
		}

		// Then it is probed occasionally rather than on every scrape.
		if len(spy.calls) == 0 {
			t.Error("never probed again; an operator who fixes the proxy would need to restart the agent")
		}
		if len(spy.calls) >= len(meta)*backoffScrapes {
			t.Errorf("made %d requests over %d scrapes; the back-off is not holding",
				len(spy.calls), backoffScrapes*2)
		}
	})

	// The refusal is a proxy configuration, not a property of the container, so
	// it must not latch: fixing the proxy brings the counts back on its own.
	t.Run("recovers when the daemon starts answering again", func(t *testing.T) {
		// Given a backed-off collector.
		spy := &spyInspector{counts: map[string]uint64{"a": 2}, err: errors.New("403 Forbidden")}
		testee := &Containers{inspector: spy.inspect}
		for range noInspectAfterScrapes {
			testee.refreshRestarts(ctx, metaOf("a"), nil)
		}

		// When the daemon starts answering and the back-off next probes.
		spy.err = nil
		for range backoffScrapes + 1 {
			testee.refreshRestarts(ctx, metaOf("a"), nil)
		}

		// Then the count is back and the capability is cleared.
		if got, ok := testee.readRestart("a"); !ok || got != 2 {
			t.Errorf("readRestart = (%d, %v), want (2, true) once the daemon answered again", got, ok)
		}
		if got, ok := testee.Capabilities()["container_restarts"]; ok {
			t.Errorf("container_restarts = %q, want cleared once inspect worked again", got)
		}
	})

	// An agent built without an inspector at all -- the same supported
	// configuration, reached a different way.
	t.Run("says why when there is no inspector", func(t *testing.T) {
		// Given a collector with no inspector.
		testee := &Containers{}

		// When the scrape refreshes.
		testee.refreshRestarts(ctx, metaOf("a"), nil)

		// Then it says so rather than staying silent.
		if got := testee.Capabilities()["container_restarts"]; got != capRestartsNoInspect {
			t.Errorf("container_restarts = %q, want %q", got, capRestartsNoInspect)
		}
	})

	// "containers: no-docker-socket" already says nothing Docker knows is
	// reachable. A second key repeating it as a restart-specific failure would
	// have the page print two explanations for one cause.
	t.Run("stays quiet about restarts when the socket itself is gone", func(t *testing.T) {
		// Given a collector with no inspector on a host with no socket.
		testee := &Containers{}
		testee.observe(true, false, 0, 0)

		// When the scrape refreshes.
		testee.refreshRestarts(ctx, metaOf("a"), nil)

		// Then only the socket is reported.
		caps := testee.Capabilities()
		if _, ok := caps["container_restarts"]; ok {
			t.Errorf("container_restarts reported alongside no-docker-socket: %v", caps)
		}
		if caps["containers"] != "no-docker-socket" {
			t.Errorf("containers = %q, want no-docker-socket", caps["containers"])
		}
	})

	// A container that is gone will not be asked about again. One that comes
	// back arrives with a NEW id and so a fresh read, which is right: Docker
	// resets RestartCount when a container is recreated.
	t.Run("forgets a container that stopped being listed", func(t *testing.T) {
		// Given two containers, both inspected.
		spy := &spyInspector{counts: map[string]uint64{"a": 1, "b": 2}}
		testee := &Containers{inspector: spy.inspect}
		testee.refreshRestarts(ctx, metaOf("a", "b"), nil)

		// When one of them stops appearing.
		testee.refreshRestarts(ctx, metaOf("a"), nil)

		// Then its cached count is gone rather than reported forever.
		if _, ok := testee.readRestart("b"); ok {
			t.Error("readRestart still answers for a container that is no longer listed")
		}
	})

	// The hole the recreate detector cannot see: a container that restarted and
	// then burned MORE CPU than its previous life did, inside one interval,
	// leaves usage_usec higher than it was. The slow refresh is what closes it.
	t.Run("re-reads a quiet container eventually", func(t *testing.T) {
		// Given a container inspected once and never flagged again.
		spy := &spyInspector{counts: map[string]uint64{"a": 1}}
		testee := &Containers{inspector: spy.inspect}
		testee.refreshRestarts(ctx, metaOf("a"), nil)
		spy.reset()

		// When enough scrapes pass to cover a full refresh cycle.
		for range restartRefreshEvery * 2 {
			testee.refreshRestarts(ctx, metaOf("a"), nil)
		}

		// Then it was asked again -- and not on every scrape.
		if len(spy.calls) == 0 {
			t.Error("a quiet container was never re-read")
		}
		if len(spy.calls) >= restartRefreshEvery*2 {
			t.Errorf("inspected %d times over %d scrapes; the refresh is not rationed",
				len(spy.calls), restartRefreshEvery*2)
		}
	})
}

// The row cap bounds how many containers a scrape reports, which bounded its
// SIZE only while every field on a row was a name or a number. Labels are the
// first thing on a container row whose length an operator controls.
func TestCapLabels(t *testing.T) {
	t.Run("passes an ordinary label set through untouched", func(t *testing.T) {
		// Given the two labels compose writes.
		labels := map[string]string{
			"com.docker.compose.project": "shop",
			"com.docker.compose.service": "web",
		}

		// When it is capped.
		got, _ := capLabels(labels)

		// Then nothing was dropped.
		if len(got) != 2 {
			t.Errorf("kept %d of 2 labels", len(got))
		}
	})

	// Non-nil, because the caller has already established that the socket
	// answered: a nil map reaches the hub as "never looked", which is the one
	// distinction the wrapper message on the wire exists to preserve.
	t.Run("returns an empty map, not nil, for a container with no labels", func(t *testing.T) {
		if got, _ := capLabels(map[string]string{}); got == nil {
			t.Error("capLabels returned nil for a container that has no labels")
		}
	})

	t.Run("drops whole pairs once the budget is spent", func(t *testing.T) {
		// Given one container carrying far more label text than the budget.
		labels := map[string]string{}
		value := make([]byte, 512)
		for i := range value {
			value[i] = 'x'
		}
		for i := range 40 {
			labels[fmt.Sprintf("com.example.k%02d", i)] = string(value)
		}

		// When it is capped.
		got, _ := capLabels(labels)

		// Then it fits the budget.
		total := 0
		for k, v := range got {
			total += len(k) + len(v)
		}
		if total > maxLabelBytes {
			t.Errorf("kept %d bytes, over the %d budget", total, maxLabelBytes)
		}
		if len(got) == 0 {
			t.Error("kept nothing; the budget should keep what fits")
		}

		// And every value it kept is WHOLE. A truncated value is worse than an
		// absent one: half a commit hash still reads as a commit hash.
		for k, v := range got {
			if v != string(value) {
				t.Errorf("label %q was truncated rather than dropped", k)
			}
		}
	})

	// Sorted keys, so the survivors are the same ones scrape after scrape --
	// the same argument capContainerRows makes for truncating a stable order.
	t.Run("keeps the same subset on every scrape", func(t *testing.T) {
		labels := map[string]string{}
		value := make([]byte, 512)
		for i := range 40 {
			labels[fmt.Sprintf("com.example.k%02d", i)] = string(value)
		}

		first, _ := capLabels(labels)
		for range 5 {
			next, _ := capLabels(labels)
			if len(next) != len(first) {
				t.Fatalf("kept %d labels, then %d", len(first), len(next))
			}
			for k := range first {
				if _, ok := next[k]; !ok {
					t.Errorf("label %q survived one scrape and not the next", k)
				}
			}
		}
	})
}
