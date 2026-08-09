package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
	"github.com/trick77/netra/internal/agent/config"
)

// agent_samples.uptime_s and host_samples.uptime_s are DIFFERENT FACTS.
//
// The agent's is how long the process has run; the host's is how long the
// machine has. An agent uptime reset with host uptime unchanged means the agent
// restarted on its own, which also means its ring buffer was lost. Conflating
// them hides crash-looping behind a healthy-looking host (spec §5.3).
//
// The fixture host has been up far longer than this process, so reporting the
// host's value in the agent's field would show as a wildly larger number.
func TestAgentUptimeIsTheProcessNotTheHost(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	s := rec.last().GetHostSamples()[0]
	agentUptime := s.GetAgent().GetUptimeS()
	hostUptime := s.GetUptimeS()

	if s.GetAgent().UptimeS == nil {
		t.Fatal("agent uptime_s is unset; the agent always knows how long it has run")
	}
	if hostUptime == 0 {
		t.Fatal("host uptime_s is 0; the fixture should provide one")
	}
	if agentUptime == hostUptime {
		t.Errorf("agent uptime_s = host uptime_s = %d; they are different facts and must not be the same value",
			agentUptime)
	}
	// The process started moments ago.
	if agentUptime > 60 {
		t.Errorf("agent uptime_s = %d, want a freshly started process", agentUptime)
	}
}

// rss_bytes and goroutines are the agent's own footprint, and the whole point
// of collecting them is to notice the agent misbehaving on a host it is meant
// to be a negligible tenant of.
func TestAgentReportsItsOwnFootprint(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	agent := rec.last().GetHostSamples()[0].GetAgent()
	if agent.RssBytes == nil || agent.GetRssBytes() == 0 {
		t.Error("rss_bytes is unset or zero; the process always occupies memory")
	}
	if agent.Goroutines == nil || agent.GetGoroutines() == 0 {
		t.Error("goroutines is unset or zero; the process always has at least one")
	}
}

// post_failures_total is cumulative across the agent's life, so the hub can
// tell "one blip" from "failing every minute for an hour".
//
// It is NOT reset by a success: an agent that fails ten times and then works
// must still report ten, or the history of the outage disappears the moment it
// ends.
func TestPostFailuresTotalAccumulatesAndSurvivesRecovery(t *testing.T) {
	var down = true
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rec.handler(t).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	c := client.NewWithInterval(cfg,
		[]collector.Collector{collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval)},
		time.Millisecond)
	ctx := context.Background()

	// Two failed flushes.
	for range 2 {
		c.ScrapeOnce(ctx)
		if err := c.Flush(ctx); err == nil {
			t.Fatal("Flush succeeded against a 503, want an error")
		}
	}

	down = false
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush after recovery: %v", err)
	}

	// The replayed batch carries every buffered sample; the newest one was
	// built after both failures.
	samples := rec.last().GetHostSamples()
	newest := samples[len(samples)-1]
	if got := newest.GetAgent().GetPostFailuresTotal(); got != 2 {
		t.Errorf("post_failures_total = %d, want 2 -- the count must survive recovery", got)
	}
}

// A 401 is a post failure too. It is the one an operator most needs counted:
// a revoked token stops all data, and without this the agent goes quiet with
// no number anywhere explaining why.
func TestPostFailuresTotalCountsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush succeeded against a 401, want an error")
	}

	// The next scrape carries the count, even though the buffer was dropped.
	s := c.ScrapeOnce(ctx)
	if got := s.GetAgent().GetPostFailuresTotal(); got != 1 {
		t.Errorf("post_failures_total = %d, want 1 after a 401", got)
	}
}
