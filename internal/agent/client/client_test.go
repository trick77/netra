package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// all returns a copy of every recorded request, oldest first.
func (rec *recorder) all() []*netrav1.IngestRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]*netrav1.IngestRequest, len(rec.requests))
	copy(out, rec.requests)
	return out
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
func (failingCollector) Collect(context.Context) (*collector.Result, error) {
	return nil, errors.New("sensor unreadable")
}

func newClient(t *testing.T, url string) *client.Client {
	t.Helper()
	cfg := config.Config{
		HubURL:       url,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{
		collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval),
		collector.NewLoad(cfg.ProcRoot, config.ScrapeInterval),
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

// wideCollector emits a fixed number of per-core rows per scrape, standing in
// for a many-core host. It is how a test reaches a multi-batch drain without
// buffering tens of thousands of scrapes: the batch bound counts rows, and a
// 64-core host carries 65 of them per scrape.
type wideCollector struct{ cores int }

func (wideCollector) Name() string            { return "wide" }
func (wideCollector) Interval() time.Duration { return time.Minute }
func (w wideCollector) Collect(context.Context) (*collector.Result, error) {
	rows := make([]*netrav1.CpuCoreSample, 0, w.cores)
	ts := time.Now().UnixMilli()
	for i := 0; i < w.cores; i++ {
		busy := float64(i)
		rows = append(rows, &netrav1.CpuCoreSample{TsMs: ts, Core: uint32(i), Busy: &busy})
	}
	return &collector.Result{Cores: rows}, nil
}

// newDeepBufferClient builds a client whose ring holds more than one
// maxBatchRows batch.
//
// At the production cadence it cannot: capacityFor caps the ring at
// window/interval, and the 6h maximum window over a fixed 60s interval is 360
// entries. On a 64-core host that is ~23,000 rows, just past one batch — so
// the multi-batch behaviours below (drop-the-batch vs drop-the-ring,
// first-batch backfill vs whole-replay backfill) are reachable in principle
// but awkward to provoke. A millisecond interval takes the ring to
// maxBufferSlots, and the wide collector makes each entry carry 65 rows, so
// the guards themselves can be tested without buffering 20,000 scrapes.
func newDeepBufferClient(t *testing.T, url string) *client.Client {
	t.Helper()
	cfg := config.Config{
		HubURL:       url,
		Token:        "nta_test",
		BufferWindow: 6 * time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{
		collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval),
		collector.NewLoad(cfg.ProcRoot, config.ScrapeInterval),
		wideCollector{cores: 64},
	}
	return client.NewWithInterval(cfg, collectors, time.Millisecond)
}

// The 401 path must drop the WHOLE ring, not just the batch it attempted.
//
// The single-sample case above passes either way: with one sample buffered,
// "drop the attempted batch" and "drop everything" are the same thing. With
// more rows than maxBatchRows the two diverge, and the old
// AckThrough(highest) left every sample beyond the first batch in the ring —
// so a revoked agent stayed pinned near capacity while ScrapeOnce kept adding.
func TestFlushOnUnauthorizedDropsTheEntireBufferNotJustTheBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := newDeepBufferClient(t, srv.URL)
	ctx := context.Background()

	// Comfortably more than one maxBatchRows worth: 400 scrapes from the wide
	// collector is 400 host rows plus 25,600 core rows.
	const buffered = 400
	for i := 0; i < buffered; i++ {
		c.ScrapeOnce(ctx)
	}
	if got := c.BufferDepth(); got != buffered {
		t.Fatalf("BufferDepth() = %d before the flush, want %d", got, buffered)
	}

	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush() succeeded against a 401, want an error")
	}
	if got := c.BufferDepth(); got != 0 {
		t.Fatalf("BufferDepth() = %d after a 401, want 0 — the whole buffer must go, not one batch", got)
	}
}

// Backfill must stay set for every batch of a replay, not just the first.
//
// It was derived from "the last flush failed", which the first successful
// partial drain cleared — so recovering a buffer larger than one batch flagged
// batch 1 as backfill and everything after it as live, even though all of it
// was replayed history the hub needs to invalidate aggregate ranges for.
func TestFlushKeepsBackfillSetForEveryBatchOfAReplay(t *testing.T) {
	var rec recorder
	var mu sync.Mutex
	down := true
	inner := rec.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		isDown := down
		mu.Unlock()
		if isDown {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := newDeepBufferClient(t, srv.URL)
	ctx := context.Background()

	// Build up more than two batches of history while the hub is down: 700
	// scrapes from the wide collector is ~45,500 rows, over two batches.
	const buffered = 700
	for i := 0; i < buffered; i++ {
		c.ScrapeOnce(ctx)
	}
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush() succeeded while the hub was down, want an error")
	}

	mu.Lock()
	down = false
	mu.Unlock()

	var flushes int
	for c.BufferDepth() > 0 {
		if err := c.Flush(ctx); err != nil {
			t.Fatalf("Flush() during replay: %v", err)
		}
		flushes++
		if flushes > 10 {
			t.Fatal("replay did not drain in a reasonable number of flushes")
		}
	}
	if flushes < 2 {
		t.Fatalf("replay took %d flushes, want at least 2 — the test needs a multi-batch drain", flushes)
	}

	reqs := rec.all()
	if len(reqs) < 2 {
		t.Fatalf("recorded %d requests, want at least 2", len(reqs))
	}
	// Every batch EXCEPT the last one is drained while history remains, so
	// every one of them is backfill. The final batch empties the ring.
	for i, req := range reqs {
		if !req.GetBackfill() {
			t.Errorf("request %d of %d had backfill=false; every batch of a replay is backfill", i+1, len(reqs))
		}
	}

	// And once drained, the next flush is live traffic again.
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush() after the replay drained: %v", err)
	}
	if last := rec.last(); last.GetBackfill() {
		t.Error("backfill stayed set after the buffer drained; it must clear on an empty ring")
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

	// And it counts. A hub bug or a proxy returning an empty 200 is "the agent
	// could not deliver", exactly like a network error or a 401, so it belongs
	// in the same number. Without this the agent buffers and backs off while
	// post_failures_total sits at zero — the one metric that would show the
	// outage insisting nothing is wrong.
	if got := c.ScrapeOnce(ctx).GetAgent().GetPostFailuresTotal(); got != 1 {
		t.Errorf("post_failures_total = %d, want 1 after a zero ack_seq", got)
	}
}

// Run must scrape and flush on every tick, and stop promptly once its context
// is cancelled — nothing in the ticker loop should outlive the caller.
func TestRunFlushesOnEveryTickAndStopsOnCancel(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval)}
	c := client.NewWithInterval(cfg, collectors, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	// Give the ticker a few chances to fire and successfully flush before
	// tearing the loop down.
	deadline := time.Now().Add(2 * time.Second)
	for rec.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.count() < 2 {
		t.Fatalf("requests = %d, want at least 2 before cancel", rec.count())
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return within 2s of context cancellation")
	}
}

// A transient flush failure must not stop the loop: Run backs off with
// jitter and keeps retrying, and still exits promptly once the context is
// cancelled mid-backoff rather than waiting out the full sleep.
func TestRunBacksOffOnTransientFailureAndStopsOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval)}
	c := client.NewWithInterval(cfg, collectors, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	// Let at least one tick fail and arm the flush backoff, which is far
	// longer than the tick interval, before cancelling. Run keeps ticking
	// through the backoff now rather than sleeping the loop, so what this
	// proves is that cancellation is observed promptly in that state.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return within 2s of context cancellation during backoff")
	}
}

// A 401 must switch Run into its slow-retry path rather than the normal
// exponential backoff, and still honor context cancellation while sleeping.
func TestRunRetriesSlowlyOnUnauthorizedAndStopsOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval)}
	c := client.NewWithInterval(cfg, collectors, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	// The unauthorized path holds the next flush off for 5 minutes, far
	// longer than this test can wait, so cancelling shortly after the first
	// tick is the only way to prove Run still returns promptly in that
	// branch rather than waiting the hold-off out.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return within 2s of context cancellation during the slow retry sleep")
	}
}

// A malformed response body (not a valid IngestResponse) must surface as an
// error rather than a zero-value success.
func TestFlushFailsOnMalformedResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write([]byte{0xFF, 0xFF, 0xFF}) // not a valid protobuf message
	}))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush() succeeded against a malformed response body, want an error")
	}
}

// An invalid hub URL must fail request construction with a clear error
// rather than panicking or silently posting nowhere.
func TestFlushFailsOnInvalidHubURL(t *testing.T) {
	cfg := config.Config{
		HubURL:       "http://\x7f",
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval)}
	c := client.New(cfg, collectors)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush() succeeded with an invalid hub URL, want an error")
	}
}

// A collector that fails must not stop the scrape: it is logged and skipped,
// but the rest of the sample is still worth sending. An agent that dies
// because one sensor is unreadable is worse than one reporting partial data.
func TestScrapeOnceSkipsFailingCollector(t *testing.T) {
	cfg := config.Config{
		HubURL:       "http://unused.invalid",
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{
		failingCollector{},
		collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval),
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

// A 503 that carries retry_after_s must surface as a *client.RetryAfterError
// with that duration, so Run can honour it instead of its own backoff.
func TestFlushReturnsRetryAfterFromHub503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := &netrav1.IngestResponse{RetryAfterS: 42}
		out, err := proto.Marshal(resp)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	err := c.Flush(ctx)
	if err == nil {
		t.Fatal("Flush() succeeded against a 503, want an error")
	}

	var raErr *client.RetryAfterError
	if !errors.As(err, &raErr) {
		t.Fatalf("Flush() error = %v, want a *client.RetryAfterError", err)
	}
	if raErr.After != 42*time.Second {
		t.Fatalf("RetryAfterError.After = %v, want 42s", raErr.After)
	}
}

// A 503 body not declared as protobuf (e.g. an intermediary's HTML error
// page) must not be trusted for retry_after_s — arbitrary bytes can still
// "unmarshal" as a zero-value message, and treating that as a real
// instruction would risk sleeping on garbage.
func TestFlushIgnoresRetryAfterWithoutProtobufContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<html>service unavailable</html>"))
	}))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	err := c.Flush(ctx)
	if err == nil {
		t.Fatal("Flush() succeeded against a 503, want an error")
	}

	var raErr *client.RetryAfterError
	if !errors.As(err, &raErr) {
		t.Fatalf("Flush() error = %v, want a *client.RetryAfterError", err)
	}
	if raErr.After != 0 {
		t.Fatalf("RetryAfterError.After = %v, want 0 for a non-protobuf body", raErr.After)
	}
}

// Run must wait at least the hub's retry_after before retrying a failed
// flush, in place of its own exponential backoff. The hub's retry_after (4s)
// is set well outside the 1-2s range the agent's own initial jittered
// backoff could produce, so the gap between retries proves which one ran.
func TestRunHonoursHubRetryAfterInsteadOfOwnBackoff(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()

		resp := &netrav1.IngestResponse{RetryAfterS: 4}
		out, err := proto.Marshal(resp)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval)}
	c := client.NewWithInterval(cfg, collectors, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	deadline := time.Now().Add(6 * time.Second)
	for {
		mu.Lock()
		n := len(times)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return within 2s of context cancellation")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(times) < 2 {
		t.Fatalf("requests = %d, want at least 2 within 6s", len(times))
	}
	gap := times[1].Sub(times[0])
	if gap < 3*time.Second {
		t.Fatalf("gap between retries = %v, want at least ~4s (the hub's retry_after); "+
			"a gap this short means Run used its own 1-2s jittered backoff instead", gap)
	}
}

// Prime must run the collectors without buffering anything, unlike
// ScrapeOnce. It exists so a CPU-collector baseline scrape at startup does
// not store a row whose values are NULL for a reason indistinguishable from
// an absent subsystem.
func TestPrimeDoesNotEnqueueASample(t *testing.T) {
	c := newClient(t, "http://unused.invalid")
	ctx := context.Background()

	c.Prime(ctx)

	if c.BufferDepth() != 0 {
		t.Fatalf("BufferDepth() = %d, want 0 after Prime", c.BufferDepth())
	}

	// The sequence counter must also be untouched: the first real scrape
	// after priming must be seq 1, proving priming consumed no sequence
	// number (and, as a consequence, buffered nothing).
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c2 := newClient(t, srv.URL)
	c2.Prime(ctx)
	c2.ScrapeOnce(ctx)
	if err := c2.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := rec.last().GetSeq(); got != 1 {
		t.Fatalf("first flush after Prime carried seq = %d, want 1", got)
	}
}

// The ring buffer exists so that a hub outage produces a replayable history
// rather than a hole in it. That only holds if Run keeps SCRAPING while it is
// backing off its FLUSH. Run used to sleep the whole loop through the backoff,
// and because time.Ticker drops ticks nobody is receiving, the samples for
// that window were simply never taken — a gap no replay can fill.
//
// This pins the invariant directly: against a hub that always 503s with a
// multi-second retry_after, the buffer must keep growing between flush
// attempts. The assertion is "many samples, few requests", which is the whole
// claim and is robust to how slow the runner is.
func TestRunKeepsScrapingWhileBackingOffFlush(t *testing.T) {
	var mu sync.Mutex
	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()

		out, err := proto.Marshal(&netrav1.IngestResponse{RetryAfterS: 3})
		if err != nil {
			t.Errorf("Marshal: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval)}
	c := client.NewWithInterval(cfg, collectors, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	// Well inside the 3s retry_after, so the flush is still held off.
	time.Sleep(700 * time.Millisecond)

	depth := c.BufferDepth()
	mu.Lock()
	n := requests
	mu.Unlock()

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return within 2s of context cancellation")
	}

	// One request, because the hub asked for a 3s wait and we looked at 700ms.
	if n != 1 {
		t.Fatalf("requests = %d, want exactly 1 while the retry_after is still pending", n)
	}
	// ~70 ticks fit in 700ms at a 10ms interval. Anything well above 1 proves
	// scraping continued; the old blocking-sleep implementation produced
	// exactly 1, since the single tick that failed was the only one serviced.
	if depth < 10 {
		t.Fatalf("buffer depth = %d after 700ms of 10ms ticks, want >= 10; "+
			"a depth this low means Run stopped scraping while backing off", depth)
	}
}

// Per-entity rows must reach the request, and a collector that fails must
// contribute nothing to it -- not even the part it managed to produce before
// the failure.
func TestFlushCarriesPerCoreRowsAndDropsFailedCollectors(t *testing.T) {
	// Given: one collector producing core rows, and one that always fails.
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	c := client.NewWithInterval(cfg,
		[]collector.Collector{wideCollector{cores: 4}, failingCollector{}},
		time.Millisecond)

	// When: one scrape is taken and flushed.
	ctx := context.Background()
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Then: the core rows arrived, keyed by core, each with its own ts.
	req := rec.last()
	if got := len(req.GetCpuCores()); got != 4 {
		t.Fatalf("cpu_cores in request = %d, want 4", got)
	}
	for i, row := range req.GetCpuCores() {
		if row.GetCore() != uint32(i) {
			t.Errorf("row %d has core %d, want %d", i, row.GetCore(), i)
		}
		if row.GetTsMs() == 0 {
			t.Errorf("row %d carries no ts_ms; a per-entity row must timestamp itself", i)
		}
	}
	// And the host row still went, because one collector failing does not
	// cost the scrape the rest of its contributors.
	if got := len(req.GetHostSamples()); got != 1 {
		t.Errorf("host samples = %d, want 1", got)
	}
}

// The batch bound exists to keep a body inside the hub's 4 MiB cap. Counting
// scrapes stopped being a proxy for body size the moment a scrape started
// carrying per-entity rows: 2000 scrapes from a 64-core host is 130,000 rows,
// and the resulting 413 would repeat forever, because the ring re-sends the
// same oversized prefix on every flush.
func TestFlushBoundsBatchByTotalRowsNotScrapeCount(t *testing.T) {
	// Given: a deep ring of wide scrapes, well past one batch of rows.
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newDeepBufferClient(t, srv.URL)
	ctx := context.Background()
	for i := 0; i < 600; i++ {
		c.ScrapeOnce(ctx)
	}

	// When: it flushes once.
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Then: the request stayed inside the row cap...
	req := rec.last()
	rows := len(req.GetHostSamples()) + len(req.GetCpuCores())
	if rows > client.MaxBatchRowsForTest {
		t.Fatalf("flushed %d rows in one request, cap is %d", rows, client.MaxBatchRowsForTest)
	}
	// ...and still carried something, rather than stalling on a cap it could
	// never satisfy.
	if len(req.GetHostSamples()) == 0 {
		t.Fatal("flushed 0 host samples; the bound must still let a batch through")
	}
	// ...leaving the remainder buffered for the next flush rather than losing it.
	if c.BufferDepth() == 0 {
		t.Fatal("BufferDepth() = 0; a partial drain must leave the rest buffered")
	}
}

// A baseline-emitting collector must NOT be primed, or its baseline is lost.
//
// This is the production path, and it is the one a collector-level test cannot
// see: main.go calls Prime before Run, Prime discards the Result it collects,
// and a collector that reports its whole state on the first Collect has by
// then also recorded that state as "previous". The first buffered scrape then
// compares identical states and emits nothing -- so a unit that was already
// failed when the agent started produces no event at all, which is precisely
// the case the baseline exists for.
//
// Asserted through Prime + ScrapeOnce + Flush rather than by calling Collect
// directly, because calling Collect directly is exactly what hid this.
func TestPrimeDoesNotConsumeABaselineCollectorsFirstScrape(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	// A unit that is already failed before the agent ever starts.
	systemd := collector.NewSystemd(config.ScrapeInterval, func(context.Context) ([]collector.Unit, error) {
		return []collector.Unit{
			{Name: "broken.service", Active: "failed", SubState: "failed"},
		}, nil
	})
	c := client.New(cfg, []collector.Collector{systemd})

	// The agent's real startup order, from main.go.
	c.Prime(ctx)
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	events := rec.last().GetSystemdEvents()
	if len(events) != 1 {
		t.Fatalf("first scrape after Prime carried %d systemd events, want 1 -- priming consumed the baseline",
			len(events))
	}
	if got := events[0].GetUnitName(); got != "broken.service" {
		t.Errorf("unit_name = %q, want broken.service", got)
	}
	if got := events[0].GetState(); got != "failed" {
		t.Errorf("state = %q, want failed", got)
	}
}

// Priming must still happen for everything else: a delta-based collector needs
// a baseline before it can report a rate, and skipping it would put a row in
// the first stored scrape whose NULL means "not computable yet" -- which is
// indistinguishable from an absent subsystem.
//
// Counted rather than asserted through a real delta collector: every fixture
// /proc tree is static, so a real one reads the same values twice and reports
// nothing either way, which would pass whether or not priming ran.
func TestPrimeStillPrimesDeltaCollectors(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{
		HubURL:       "http://unused.invalid",
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}

	ordinary := &countingCollector{}
	baseline := &countingCollector{baseline: true}
	c := client.New(cfg, []collector.Collector{ordinary, baseline})

	c.Prime(ctx)

	if ordinary.calls != 1 {
		t.Errorf("ordinary collector Collect calls = %d, want 1 -- priming must still establish its baseline",
			ordinary.calls)
	}
	if baseline.calls != 0 {
		t.Errorf("baseline collector Collect calls = %d, want 0 -- its first scrape is data, not a warm-up",
			baseline.calls)
	}
}

// countingCollector records how many times it was collected, and optionally
// reports itself as a collector.BaselineEmitter.
type countingCollector struct {
	baseline bool
	calls    int
}

func (c *countingCollector) Name() string            { return "counting" }
func (c *countingCollector) Interval() time.Duration { return config.ScrapeInterval }
func (c *countingCollector) EmitsBaseline() bool     { return c.baseline }

func (c *countingCollector) Collect(context.Context) (*collector.Result, error) {
	c.calls++
	return &collector.Result{}, nil
}

// The package inventory must survive priming, through the real startup path.
//
// Packages is the worst case for a discarded first Collect: the parse stamps
// lastMtime and lastParse, so every later scrape short-circuits on "unchanged
// and recent" until the database is written to or the 24h floor elapses. A
// primed-away inventory therefore does not come back on the next scrape -- a
// freshly enrolled host reports no packages at all for up to a day, and an
// existing one keeps whatever it had before the restart, because
// UpsertHostPackages returns early on an empty set.
func TestPrimeDoesNotConsumeThePackageInventory(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	dpkg := filepath.Join(t.TempDir(), "status")
	if err := os.WriteFile(dpkg, []byte(
		"Package: bash\nVersion: 5.3\nArchitecture: amd64\nInstalled-Size: 1024\nStatus: install ok installed\n\n",
	), 0o644); err != nil {
		t.Fatalf("write dpkg status: %v", err)
	}

	ctx := context.Background()
	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	c := client.New(cfg, []collector.Collector{
		collector.NewPackages(dpkg, "", config.ScrapeInterval),
	})

	// The agent's real startup order, from main.go.
	c.Prime(ctx)
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	pkgs := rec.last().GetPackages()
	if len(pkgs) != 1 {
		t.Fatalf("first scrape after Prime carried %d packages, want 1 -- priming consumed the inventory",
			len(pkgs))
	}
	if got := pkgs[0].GetName(); got != "bash" {
		t.Errorf("package name = %q, want bash", got)
	}
}
