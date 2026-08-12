package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func (capabilityCollector) Name() string { return "capability" }
func (capabilityCollector) Collect(context.Context) (*collector.Result, error) {
	return &collector.Result{}, nil
}

// An empty value means "nothing to report", the way a recovered collector
// behaves: every reporter in the tree returns nil once it is healthy again.
func (c *capabilityCollector) Capabilities() map[string]string {
	if c.value == "" {
		return nil
	}
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

// collector_samples, both its continuous aggregates and the host page's
// device-availability panel all existed while nothing in internal/agent ever
// populated IngestRequest.collectors -- the tables were fed only by the
// simulator, so the panel was blank on every real host.
//
// A collector cannot time itself, so the scrape loop measures it: the health
// row is built outside the collector, which is why this family has no
// counterpart on collector.Result.
func TestScrapeReportsEveryCollectorsOwnHealth(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	defer srv.Close()

	c := client.NewWithInterval(
		config.Config{HubURL: srv.URL, Token: "nta_test", BufferWindow: time.Hour},
		[]collector.Collector{&capabilityCollector{key: "k"}, failingCollector{}},
		time.Minute,
	)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(rec.requests))
	}
	rows := rec.requests[0].GetCollectors()
	if len(rows) != 2 {
		t.Fatalf("collector rows = %d, want one per registered collector", len(rows))
	}

	byName := map[string]*netrav1.CollectorSample{}
	for _, r := range rows {
		byName[r.GetCollector()] = r
	}

	ok, found := byName["capability"]
	if !found {
		t.Fatal("no row for the healthy collector")
	}
	if !ok.GetOk() {
		t.Error("healthy collector reported ok = false")
	}
	if ok.ErrorCode != nil {
		t.Errorf("healthy collector carries error_code %q; it must be unset", ok.GetErrorCode())
	}
	if ok.DurationMs == nil {
		t.Error("healthy collector carries no duration")
	}

	bad, found := byName["failing"]
	if !found {
		t.Fatal("no row for the failing collector -- a collector that fails must still report health")
	}
	if bad.GetOk() {
		t.Error("failing collector reported ok = true")
	}
	if bad.GetErrorCode() == "" {
		t.Error("failing collector carries no error_code")
	}
}

// The raw error text never reaches the wire: it carries per-host paths and
// errno strings, and the column is rolled up with last() into both aggregate
// tiers, so free text would fill the aggregates with one-off values.
func TestCollectorErrorCodeIsAShortTokenNotTheMessage(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	defer srv.Close()

	c := client.NewWithInterval(
		config.Config{HubURL: srv.URL, Token: "nta_test", BufferWindow: time.Hour},
		[]collector.Collector{failingCollector{}},
		time.Minute,
	)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	got := rec.requests[0].GetCollectors()[0].GetErrorCode()
	if strings.Contains(got, "sensor unreadable") {
		t.Errorf("error_code = %q; the raw message must not reach the wire", got)
	}
	if got != "error" {
		t.Errorf("error_code = %q, want the generic token \"error\"", got)
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
		collector.NewLoad(cfg.ProcRoot),
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

// Recovery is a capability change too, including when it empties the map.
//
// refreshCapabilities used to return early on an empty merged set, so the LAST
// capability could never be cleared: the metadata kept its stale entry, the
// hash never moved, and the hub went on reporting a degraded subsystem for the
// life of the process. Recovery has to propagate as readily as failure.
func TestRecoveringTheLastCapabilityClearsItFromMetadata(t *testing.T) {
	rec := &recorder{
		// Ask for metadata every time, so each request carries the current map
		// rather than only its hash.
		respond: func(req *netrav1.IngestRequest) *netrav1.IngestResponse {
			return &netrav1.IngestResponse{AckSeq: req.GetSeq(), RequestMetadata: true}
		},
	}
	srv := httptest.NewServer(rec.handler(t))
	defer srv.Close()

	// Given: the only capability reporter on the host is degraded.
	cap := &capabilityCollector{key: "sensors", value: "degraded"}
	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	c := client.New(cfg, []collector.Collector{cap})
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if got := rec.last().GetMetadata().GetCapabilities()["sensors"]; got != "degraded" {
		t.Fatalf("capabilities[sensors] = %q, want %q before recovery", got, "degraded")
	}
	degraded := rec.last().GetMetadataHash()

	// When: the sensor comes back and the collector reports nothing.
	cap.value = ""

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("third Flush: %v", err)
	}

	// Then: the hash moved, and the capability is gone from the metadata.
	if string(rec.last().GetMetadataHash()) == string(degraded) {
		t.Error("metadata hash unchanged after the last capability cleared, want it to differ")
	}
	if got, ok := rec.last().GetMetadata().GetCapabilities()["sensors"]; ok {
		t.Errorf("capabilities[sensors] = %q, want it absent once the sensor recovered", got)
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
