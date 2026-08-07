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

// Run must scrape and flush on every tick, and stop promptly once its context
// is cancelled — nothing in the ticker loop should outlive the caller.
func TestRunFlushesOnEveryTickAndStopsOnCancel(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		Interval:     5 * time.Millisecond,
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, cfg.Interval)}
	c := client.New(cfg, collectors)

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
		Interval:     5 * time.Millisecond,
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, cfg.Interval)}
	c := client.New(cfg, collectors)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	// Let at least one tick fail and enter the backoff sleep, which is far
	// longer than the tick interval, before cancelling.
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
		Interval:     5 * time.Millisecond,
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, cfg.Interval)}
	c := client.New(cfg, collectors)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	// The unauthorized path sleeps for 5 minutes, far longer than this test
	// can wait, so cancelling shortly after the first tick is the only way to
	// prove sleep() actually honors ctx.Done() in that branch.
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
		Interval:     time.Minute,
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, cfg.Interval)}
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

// A hub-supplied interval_s must be adopted for subsequent scrapes.
func TestFlushAdoptsHubSuppliedInterval(t *testing.T) {
	rec := &recorder{
		respond: func(req *netrav1.IngestRequest) *netrav1.IngestResponse {
			return &netrav1.IngestResponse{AckSeq: req.GetSeq(), IntervalS: 30}
		},
	}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL) // built with Interval: time.Minute
	ctx := context.Background()

	if got := c.Interval(); got != time.Minute {
		t.Fatalf("Interval() before any flush = %v, want the configured 1m", got)
	}

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := c.Interval(); got != 30*time.Second {
		t.Fatalf("Interval() after flush = %v, want 30s (adopted from the hub)", got)
	}
}

// A zero interval_s (the field's default, meaning "no opinion") must not be
// adopted — the client keeps its configured interval.
func TestFlushIgnoresZeroIntervalFromHub(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := c.Interval(); got != time.Minute {
		t.Fatalf("Interval() = %v, want the configured 1m unchanged", got)
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
		Interval:     5 * time.Millisecond,
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, cfg.Interval)}
	c := client.New(cfg, collectors)

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

// Run must actually retune its ticker when it adopts a hub-supplied
// interval_s, not merely record the new value: the initial cadence is slow
// (4s) and the hub asks for a 1s cadence starting with the very first
// response, so several more requests arriving well inside a window a 4s
// cadence alone could not produce proves the ticker was reset.
func TestRunAdoptsHubSuppliedIntervalForSubsequentTicks(t *testing.T) {
	var reqCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		resp := &netrav1.IngestResponse{AckSeq: 1, IntervalS: 1}
		out, err := proto.Marshal(resp)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		Interval:     4 * time.Second, // slow initial cadence
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{collector.NewMemory(cfg.ProcRoot, cfg.Interval)}
	c := client.New(cfg, collectors)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	// A 4s-only cadence gives exactly 1 request by t=7.5s (the next would land
	// at t=8s). Requiring 4 within 7.5s proves the ticker adopted the 1s
	// interval after the first response.
	deadline := time.Now().Add(7500 * time.Millisecond)
	for reqCount.Load() < 4 && time.Now().Before(deadline) {
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

	if got := reqCount.Load(); got < 4 {
		t.Fatalf("requests within 7.5s = %d, want at least 4 — the ticker never adopted the faster interval_s", got)
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
