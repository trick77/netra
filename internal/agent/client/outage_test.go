package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// hubEvents returns every `hub` event across all recorded requests.
func hubEvents(reqs []*netrav1.IngestRequest) []*netrav1.Event {
	var out []*netrav1.Event
	for _, req := range reqs {
		for _, ev := range req.GetEvents() {
			if ev.GetType() == client.EventTypeHub {
				out = append(out, ev)
			}
		}
	}
	return out
}

func hubDetail(t *testing.T, ev *netrav1.Event) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(ev.GetDetailJson()), &m); err != nil {
		t.Fatalf("detail_json is not an object: %v", err)
	}
	return m
}

// The point of the whole mechanism, in one test.
//
// post_failures_total rides EVERY buffered scrape, so a hub that was away for
// twenty scrapes replays to the hub as 0, 1, 2, ..., 20. Anything deriving
// events from that staircase writes twenty rows for one incident, which is why
// the agent -- the only party that knows it was one incident -- says so itself.
func TestOneOutageProducesExactlyOneEvent(t *testing.T) {
	rec := &recorder{}
	down := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rec.handler(t).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{})

	// Five failed flushes: one outage, not five.
	for range 5 {
		c.ScrapeOnce(context.Background())
		if err := c.Flush(context.Background()); err == nil {
			t.Fatal("flush succeeded while the hub was down")
		}
	}

	down = false
	c.ScrapeOnce(context.Background())
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("flush after recovery: %v", err)
	}
	// A second scrape and flush, because the event is attached to the scrape
	// AFTER the one that recovered -- and because a second one must not
	// produce a second event.
	c.ScrapeOnce(context.Background())
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("second flush after recovery: %v", err)
	}

	events := hubEvents(rec.all())
	if len(events) != 1 {
		t.Fatalf("got %d hub events, want exactly 1", len(events))
	}

	ev := events[0]
	if got := ev.GetSeverity(); got != "info" {
		t.Errorf("severity = %q, want %q: the ring replayed everything, so nothing was lost", got, "info")
	}
	if ev.GetSubject() != "" {
		t.Errorf("subject = %q, want empty: an outage is about the host as a whole", ev.GetSubject())
	}

	d := hubDetail(t, ev)
	if d["severity"] != "info" {
		t.Errorf("detail severity = %v, want info: both event views read the detail key first", d["severity"])
	}
	if d["reason"] != "unreachable" {
		t.Errorf("reason = %v, want unreachable", d["reason"])
	}
	if got, ok := d["failures"].(float64); !ok || got != 5 {
		t.Errorf("failures = %v, want 5", d["failures"])
	}
	if _, present := d["lost"]; present {
		t.Errorf("lost was reported on an outage that lost nothing: %v", d["lost"])
	}
}

// A hub that was never away produces no event at all. The log is for things
// that happened.
func TestNoOutageProducesNoEvent(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{})
	for range 3 {
		c.ScrapeOnce(context.Background())
		if err := c.Flush(context.Background()); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}

	if events := hubEvents(rec.all()); len(events) != 0 {
		t.Fatalf("got %d hub events on a healthy agent, want 0", len(events))
	}
}

// A revoked token dumps the whole buffer, and there is no recovery to hang the
// report on: the token stays revoked until an operator acts. Reported at the
// moment of the dump instead, or the one incident that genuinely needs a human
// is the one incident that never gets reported.
func TestRejectedTokenReportsAtTheMomentOfTheDump(t *testing.T) {
	rec := &recorder{}
	reject := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reject {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rec.handler(t).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{})

	// Two buffered scrapes, then the 401 that throws them away.
	c.ScrapeOnce(context.Background())
	c.ScrapeOnce(context.Background())
	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("flush succeeded against a hub returning 401")
	}

	reject = false
	c.ScrapeOnce(context.Background())
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("flush after the token was fixed: %v", err)
	}

	events := hubEvents(rec.all())
	if len(events) != 1 {
		t.Fatalf("got %d hub events, want exactly 1", len(events))
	}

	ev := events[0]
	if got := ev.GetSeverity(); got != "critical" {
		t.Errorf("severity = %q, want critical: the buffer was discarded", got)
	}
	d := hubDetail(t, ev)
	if d["reason"] != "token-rejected" {
		t.Errorf("reason = %v, want token-rejected: a config error is not a hub that will be back", d["reason"])
	}
	if got, ok := d["lost"].(float64); !ok || got != 2 {
		t.Errorf("lost = %v, want 2 discarded scrapes", d["lost"])
	}
}

// The severity is decided by OUTCOME, not by the fact of having failed. This
// pins the half that matters: samples that will never arrive are critical,
// where a replayed outage is info.
func TestDiscardedScrapesMakeTheEventCritical(t *testing.T) {
	rec := &recorder{}
	reject := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reject {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rec.handler(t).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{})
	c.ScrapeOnce(context.Background())
	_ = c.Flush(context.Background())

	reject = false
	c.ScrapeOnce(context.Background())
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	events := hubEvents(rec.all())
	if len(events) != 1 {
		t.Fatalf("got %d hub events, want 1", len(events))
	}
	if got := events[0].GetSeverity(); got != "critical" {
		t.Errorf("severity = %q, want critical when scrapes were discarded", got)
	}
}

// The event has to reach the hub, which means riding the ordinary delivery
// path rather than being posted out of band. It is attached to a scrape, so it
// buffers, replays and acks exactly like a collector's event does.
func TestOutageEventRidesAScrape(t *testing.T) {
	rec := &recorder{}
	down := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rec.handler(t).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{})
	c.ScrapeOnce(context.Background())
	_ = c.Flush(context.Background())

	down = false
	c.ScrapeOnce(context.Background())
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// The recovering flush drains the backlog; the event is written during it
	// and attached to the NEXT scrape, so it needs one more round trip.
	c.ScrapeOnce(context.Background())
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if got := len(hubEvents(rec.all())); got != 1 {
		t.Fatalf("got %d hub events, want 1 -- the event must ride a scrape to the hub", got)
	}
}

// An outage that is still going has produced no event yet: it is not over, and
// its duration is not yet a fact.
func TestAnOngoingOutageReportsNothing(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{})
	for range 3 {
		c.ScrapeOnce(context.Background())
		_ = c.Flush(context.Background())
	}

	if got := len(hubEvents(rec.all())); got != 0 {
		t.Fatalf("got %d hub events during an ongoing outage, want 0", got)
	}
}
