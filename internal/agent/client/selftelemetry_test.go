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
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// capabilityCollector reports a capability that the test controls, so the
// metadata-hash plumbing can be exercised without depending on whether the
// machine running the suite happens to be in a PID namespace.
type capabilityCollector struct {
	key   string
	value string
}

func (capabilityCollector) Name() string            { return "capability" }
func (capabilityCollector) Interval() time.Duration { return time.Minute }
func (capabilityCollector) Collect(context.Context) (*collector.Result, error) {
	return &collector.Result{}, nil
}
func (c *capabilityCollector) Capabilities() map[string]string {
	return map[string]string{c.key: c.value}
}

// Every scrape reports how long its collectors took. Without it there is no
// way to tell a slow host from a slow collector.
func TestScrapeOnceStampsScrapeDuration(t *testing.T) {
	c := newClient(t, "http://127.0.0.1:1")

	sample := c.ScrapeOnce(context.Background())

	if sample.GetAgent() == nil {
		t.Fatal("Agent = nil, want self-telemetry on every sample")
	}
	if sample.GetAgent().ScrapeDurationMs == nil {
		t.Error("ScrapeDurationMs = nil, want it set on every scrape")
	}
	if sample.GetAgent().BufferDepth == nil {
		t.Error("BufferDepth = nil, want it set on every scrape")
	}
}

// The RTT of a post is only known after the post, which is after the sample
// was built. The first sample therefore cannot carry one, and inventing a
// zero would report an impossibly fast hub.
func TestPostLatencyUnsetOnFirstScrape(t *testing.T) {
	c := newClient(t, "http://127.0.0.1:1")

	sample := c.ScrapeOnce(context.Background())

	if got := sample.GetAgent().PostLatencyMs; got != nil {
		t.Errorf("PostLatencyMs = %v, want nil before any post has happened", *got)
	}
}

// Once a post has succeeded, the next scrape carries its round-trip time.
func TestPostLatencyReflectsThePreviousSuccessfulPost(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	defer srv.Close()

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	sample := c.ScrapeOnce(ctx)

	if sample.GetAgent().PostLatencyMs == nil {
		t.Fatal("PostLatencyMs = nil, want the previous post's RTT")
	}
}

// During an outage the field must be NULL, not 0 and not the last good value.
// A stale reading would show a healthy hub throughout an outage, which is
// precisely when someone is looking at it.
func TestPostLatencyUnsetNotZeroAfterFailedFlush(t *testing.T) {
	var fail bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			// Close without responding: a transport-level failure.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("ResponseWriter is not a Hijacker")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		rec := &recorder{}
		rec.handler(t).ServeHTTP(w, r)
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	ctx := context.Background()

	// One good round trip, so there is a value that could go stale.
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("first Flush: %v", err)
	}
	if got := c.ScrapeOnce(ctx).GetAgent().PostLatencyMs; got == nil {
		t.Fatal("PostLatencyMs = nil after a successful post, want a value")
	}

	fail = true
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush() = nil, want an error once the hub stops responding")
	}

	sample := c.ScrapeOnce(ctx)
	if got := sample.GetAgent().PostLatencyMs; got != nil {
		t.Errorf("PostLatencyMs = %v, want nil after a failed flush", *got)
	}
}

// A 503 is a completed HTTP exchange but a failed flush. The batch was not
// stored, so reporting a healthy RTT for it would be misleading.
func TestPostLatencyUnsetAfterServiceUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush() = nil, want an error on 503")
	}

	sample := c.ScrapeOnce(ctx)
	if got := sample.GetAgent().PostLatencyMs; got != nil {
		t.Errorf("PostLatencyMs = %v, want nil after a 503", *got)
	}
}

// A collector that starts or stops being able to run is a change to the
// static facts, so it must flip the hash and make the hub ask for metadata --
// otherwise the hub keeps serving a capability map that is no longer true.
func TestCapabilityChangeFlipsMetadataHashAndRequestsResend(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	defer srv.Close()

	cap := &capabilityCollector{key: "processes", value: "ok"}
	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	c := client.New(cfg, []collector.Collector{
		collector.NewLoad(cfg.ProcRoot, config.ScrapeInterval),
		cap,
	})
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	first := rec.last().GetMetadataHash()

	// The collector loses access -- a bind mount removed on redeploy.
	cap.value = "namespaced"

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	second := rec.last().GetMetadataHash()

	if string(first) == string(second) {
		t.Error("metadata hash unchanged after a capability changed, want it to differ")
	}
}

// The capability map has to survive the wire, or the hub has nothing to store
// in hosts.capabilities.
func TestCapabilitiesReachTheHubInMetadata(t *testing.T) {
	rec := &recorder{
		// Ask for metadata on the first request, as a hub with no stored
		// hash would.
		respond: func(req *netrav1.IngestRequest) *netrav1.IngestResponse {
			return &netrav1.IngestResponse{AckSeq: req.GetSeq(), RequestMetadata: true}
		},
	}
	srv := httptest.NewServer(rec.handler(t))
	defer srv.Close()

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	c := client.New(cfg, []collector.Collector{
		&capabilityCollector{key: "users", value: "unavailable"},
	})
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	// The first response asked for metadata; the second request carries it.
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}

	md := rec.last().GetMetadata()
	if md == nil {
		t.Fatal("Metadata = nil, want it sent once the hub asked")
	}
	if got := md.GetCapabilities()["users"]; got != "unavailable" {
		t.Errorf("capabilities[users] = %q, want %q", got, "unavailable")
	}
}
