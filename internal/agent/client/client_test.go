package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
	"github.com/trick77/netra/internal/agent/config"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

type recorder struct {
	mu       sync.Mutex
	requests []*netrav1.IngestRequest
	respond  func(*netrav1.IngestRequest) *netrav1.IngestResponse
}

func (rec *recorder) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var req netrav1.IngestRequest
		if err := proto.Unmarshal(raw, &req); err != nil {
			t.Errorf("unmarshal: %v", err)
			return
		}

		rec.mu.Lock()
		rec.requests = append(rec.requests, &req)
		rec.mu.Unlock()

		resp := &netrav1.IngestResponse{AckSeq: req.GetSeq()}
		if rec.respond != nil {
			resp = rec.respond(&req)
		}
		out, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(out)
	})
}

func (rec *recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

func (rec *recorder) last() *netrav1.IngestRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		return nil
	}
	return rec.requests[len(rec.requests)-1]
}

// failingCollector always errors, simulating an unreadable sensor.
type failingCollector struct{}

func (failingCollector) Name() string            { return "failing" }
func (failingCollector) Interval() time.Duration { return time.Minute }
func (failingCollector) Collect(context.Context, *netrav1.HostSample) error {
	return errors.New("sensor unreadable")
}

func newClient(t *testing.T, url string) *client.Client {
	t.Helper()
	cfg := config.Config{
		HubURL:       url,
		Token:        "nta_test",
		Interval:     time.Minute,
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{
		collector.NewMemory(cfg.ProcRoot, cfg.Interval),
		collector.NewLoad(cfg.ProcRoot, cfg.Interval),
	}
	return client.New(cfg, collectors)
}

func TestFlushSendsBufferedSamplesAndClearsOnAck(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if rec.count() != 1 {
		t.Fatalf("requests = %d, want 1", rec.count())
	}
	if got := len(rec.last().GetHostSamples()); got != 1 {
		t.Fatalf("host samples in request = %d, want 1", got)
	}
	if c.BufferDepth() != 0 {
		t.Fatalf("BufferDepth() = %d, want 0 after an ack", c.BufferDepth())
	}
}

// An unreachable hub must not lose samples: they stay buffered and go out on
// the next successful flush, flagged as backfill.
func TestFlushBuffersWhileHubIsDown(t *testing.T) {
	var down atomic.Bool
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rec.handler(t).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	down.Store(true)
	c.ScrapeOnce(ctx)
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush() succeeded against a 503, want an error")
	}
	if c.BufferDepth() != 2 {
		t.Fatalf("BufferDepth() = %d, want 2 while the hub is down", c.BufferDepth())
	}

	down.Store(false)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush after recovery: %v", err)
	}
	if c.BufferDepth() != 0 {
		t.Fatalf("BufferDepth() = %d, want 0 after recovery", c.BufferDepth())
	}
	if !rec.last().GetBackfill() {
		t.Fatal("Backfill = false, want true for replayed samples")
	}
}

func TestFlushSendsMetadataWhenRequested(t *testing.T) {
	rec := &recorder{
		respond: func(req *netrav1.IngestRequest) *netrav1.IngestResponse {
			// Ask for metadata on the first request only.
			return &netrav1.IngestResponse{
				AckSeq:          req.GetSeq(),
				RequestMetadata: req.GetMetadata() == nil,
			}
		},
	}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("first Flush: %v", err)
	}
	if rec.last().GetMetadata() != nil {
		t.Fatal("first request carried metadata, want none until asked")
	}

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if rec.last().GetMetadata() == nil {
		t.Fatal("second request carried no metadata, want it after RequestMetadata")
	}
	if rec.last().GetMetadata().GetHostname() == "" {
		t.Fatal("metadata hostname is empty")
	}
}

func TestFlushSendsMetadataHashOnEveryRequest(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(rec.last().GetMetadataHash()) != 8 {
		t.Fatalf("len(MetadataHash) = %d, want 8", len(rec.last().GetMetadataHash()))
	}
}

func TestFlushRejectsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	err := c.Flush(ctx)
	if err == nil {
		t.Fatal("Flush() succeeded against a 401, want an error")
	}
	// A revoked host must stop hammering the hub, so the buffer is cleared
	// rather than replayed forever.
	if c.BufferDepth() != 0 {
		t.Fatalf("BufferDepth() = %d, want 0 after a 401", c.BufferDepth())
	}
}

// A zero-value or malformed response must not be mistaken for success:
// sequence numbers start at 1, so ack_seq == 0 can never be a genuine ack.
// Treating it as one would drain nothing from the buffer while the agent
// believes the flush succeeded — no error, no backoff, no backfill flag on
// the next attempt.
func TestFlushTreatsZeroAckSeqAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, _ := proto.Marshal(&netrav1.IngestResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush() succeeded with ack_seq=0, want an error")
	}
	if c.BufferDepth() != 1 {
		t.Fatalf("BufferDepth() = %d, want 1 — a zero ack must not drain the buffer", c.BufferDepth())
	}
}

// A collector that fails must not stop the scrape: it is logged and skipped,
// but the rest of the sample is still worth sending. An agent that dies
// because one sensor is unreadable is worse than one reporting partial data.
func TestScrapeOnceSkipsFailingCollector(t *testing.T) {
	cfg := config.Config{
		HubURL:       "http://unused.invalid",
		Token:        "nta_test",
		Interval:     time.Minute,
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{
		failingCollector{},
		collector.NewMemory(cfg.ProcRoot, cfg.Interval),
	}
	c := client.New(cfg, collectors)

	sample := c.ScrapeOnce(context.Background())

	if sample == nil {
		t.Fatal("ScrapeOnce() returned nil, want a sample despite the failing collector")
	}
	if sample.MemTotal == nil {
		t.Fatal("MemTotal is unset — the working collector's fields were lost alongside the failing one's")
	}
	if c.BufferDepth() != 1 {
		t.Fatalf("BufferDepth() = %d, want 1 — the partial sample must still be buffered", c.BufferDepth())
	}
}
