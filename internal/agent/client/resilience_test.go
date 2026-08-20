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
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// blockingCollector never returns until its context is cancelled.
//
// This is a COOPERATIVE blocker, and that is the honest scope of what the
// scrape deadline can enforce: collectOne calls Collect synchronously, so the
// deadline ends a collector that consults ctx and nothing else. A collector
// that blocks on an uncancellable syscall has to be bounded at its own call
// site -- see TestFilesystemsAbandonsAWedgedMountpoint and
// TestSystemSmartctlReturnsOnACancelledContext, which cover the two that could.
type blockingCollector struct {
	// released is closed when Collect returns, so a test can prove the call was
	// abandoned rather than merely slow.
	released chan struct{}
}

func newBlockingCollector() *blockingCollector {
	return &blockingCollector{released: make(chan struct{})}
}

func (blockingCollector) Name() string { return "blocking" }

func (b *blockingCollector) Collect(ctx context.Context) (*collector.Result, error) {
	defer close(b.released)
	<-ctx.Done()
	return nil, ctx.Err()
}

// panickingCollector fails the way a parser meets malformed /proc content.
type panickingCollector struct{}

func (panickingCollector) Name() string { return "panicking" }
func (panickingCollector) Collect(context.Context) (*collector.Result, error) {
	var rows []int
	// Index out of range: the exact shape of the bug this guard exists for.
	_ = rows[3]
	return nil, nil
}

// fatCollector emits more rows in ONE scrape than maxBatchRows, so the batching
// loop's `i > 0` guard lets it through alone and the hub sees an oversized body.
type fatCollector struct{ rows int }

func (fatCollector) Name() string { return "fat" }
func (f fatCollector) Collect(context.Context) (*collector.Result, error) {
	rows := make([]*netrav1.CpuCoreSample, f.rows)
	for i := range rows {
		rows[i] = &netrav1.CpuCoreSample{TsMs: 1, Core: uint32(i)}
	}
	return &collector.Result{Cores: rows}, nil
}

func testConfig(url string) config.Config {
	return config.Config{
		HubURL:       url,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
}

// collectorSample returns the health row a scrape recorded for one collector.
func collectorSample(t *testing.T, req *netrav1.IngestRequest, name string) *netrav1.CollectorSample {
	t.Helper()
	for _, cs := range req.GetCollectors() {
		if cs.GetCollector() == name {
			return cs
		}
	}
	return nil
}

// A collector that never returns must cost one scrape, not the agent.
//
// Before the deadline, Run handed collect the process's SIGTERM context. One
// blocking call held the single goroutine that owns scraping, flushing and the
// ring, so the agent stopped reporting entirely while still looking alive.
func TestWedgedCollectorTimesOutRatherThanStallingTheScrape(t *testing.T) {
	blocker := newBlockingCollector()
	c := client.New(testConfig("http://127.0.0.1:1"), []collector.Collector{
		collector.NewMemory("../collector/testdata/proc1"),
		blocker,
	})
	c.SetScrapeTimeoutForTest(150 * time.Millisecond)

	done := make(chan *netrav1.HostSample, 1)
	go func() { done <- c.ScrapeOnce(context.Background()) }()

	var host *netrav1.HostSample
	select {
	case host = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ScrapeOnce did not return; the scrape deadline is not bounding the loop")
	}

	// The blocked call is released by the deadline, not left holding the loop.
	select {
	case <-blocker.released:
	case <-time.After(2 * time.Second):
		t.Fatal("the blocking collector was never cancelled")
	}

	// The rest of the scrape still happened: a wedged collector costs its own
	// subsystem and nothing else.
	if host.GetMemTotal() == 0 {
		t.Error("memory collector produced nothing; a wedged collector cost the whole scrape")
	}
	if c.BufferDepth() != 1 {
		t.Errorf("buffer depth = %d, want 1: the scrape must still be buffered", c.BufferDepth())
	}
}

// The timeout must be RECORDED, not silently absorbed -- "this collector is
// timing out" is the question the availability panel exists to answer.
func TestWedgedCollectorIsRecordedAsATimeout(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{newBlockingCollector()})
	c.SetScrapeTimeoutForTest(100 * time.Millisecond)

	c.ScrapeOnce(context.Background())
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	cs := collectorSample(t, rec.last(), "blocking")
	if cs == nil {
		t.Fatal("no collector_sample recorded for the wedged collector")
	}
	if cs.GetOk() {
		t.Error("the wedged collector reported ok")
	}
	if got := cs.GetErrorCode(); got != "timeout" {
		t.Errorf("error_code = %q, want %q", got, "timeout")
	}
}

// A scrape cancelled by SHUTDOWN must still not be recorded as a wall of
// failures -- the collectors did not fail, they were never given the chance.
// This is the distinction the deadline must not blur.
func TestShutdownCancellationIsNotRecordedAsCollectorFailure(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{newBlockingCollector()})
	c.SetScrapeTimeoutForTest(10 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	c.ScrapeOnce(ctx)

	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if cs := collectorSample(t, rec.last(), "blocking"); cs != nil {
		t.Errorf("a shutdown-cancelled collector was recorded as a failure: %v", cs)
	}
}

// The production deadline has to leave the cadence intact: a scrape that times
// out must be finished before the tick that follows it.
func TestScrapeTimeoutStaysInsideTheScrapeInterval(t *testing.T) {
	if client.ScrapeTimeoutForTest >= config.ScrapeInterval {
		t.Fatalf("scrapeTimeout %s must be shorter than the %s cadence",
			client.ScrapeTimeoutForTest, config.ScrapeInterval)
	}
}

// A panicking collector must cost its own subsystem, not the process and the
// whole in-memory ring with it.
func TestPanickingCollectorIsRecoveredAndRecorded(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{
		panickingCollector{},
		collector.NewMemory("../collector/testdata/proc1"),
	})

	// The assertion is partly that this line simply returns.
	host := c.ScrapeOnce(context.Background())

	if host.GetMemTotal() == 0 {
		t.Error("the collector after the panicking one never ran")
	}
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	cs := collectorSample(t, rec.last(), "panicking")
	if cs == nil {
		t.Fatal("no collector_sample recorded for the panicking collector")
	}
	if cs.GetOk() {
		t.Error("the panicking collector reported ok")
	}
	if cs.GetErrorCode() == "" {
		t.Error("the panic was recovered but recorded no error_code")
	}
}

// A body the hub will never accept must be dropped, not replayed forever.
//
// maxBatchRows keeps a MULTI-scrape body inside the hub's 4 MiB cap, but the
// batching loop deliberately lets a single oversized scrape through alone -- so
// one fat scrape earned a 413, was never acked, and every later flush re-sent
// the identical bytes while the ring filled behind it.
func TestPermanentlyRejectedBatchIsDroppedNotReplayed(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts++
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
	}))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{
		fatCollector{rows: client.MaxBatchRowsForTest + 1},
	})

	c.ScrapeOnce(context.Background())
	if c.BufferDepth() != 1 {
		t.Fatalf("buffer depth = %d before flush, want 1", c.BufferDepth())
	}

	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("a 413 must still be reported as a failed delivery")
	}

	if c.BufferDepth() != 0 {
		t.Fatalf("buffer depth = %d after a 413, want 0: the poison batch was not dropped",
			c.BufferDepth())
	}

	// And the proof it cannot loop: a second flush has nothing left to send.
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if posts != 1 {
		t.Errorf("hub received %d posts, want 1: the rejected body was offered again", posts)
	}
}

// A 400 is NOT permanent, however much it looks like one. The hub answers 400
// when reading the upload fails part-way and when proto.Unmarshal rejects the
// body -- and these bytes came out of proto.Marshal, so both mean the body was
// damaged in transit. A resend fixes that; dropping loses history for nothing.
func TestBadRequestKeepsTheBufferForARetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "malformed body", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{
		collector.NewMemory("../collector/testdata/proc1"),
	})

	c.ScrapeOnce(context.Background())
	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("a 400 must be reported as a failure")
	}
	if c.BufferDepth() != 1 {
		t.Errorf("buffer depth = %d after a 400, want 1: a truncated upload must not cost the buffer",
			c.BufferDepth())
	}
}

// Prime runs every collector over the same host data the scrape loop does, so
// the panic guard has to cover it too -- otherwise the crash arrives at
// startup, before the ring exists, and repeats on every restart.
func TestPrimePanicIsRecovered(t *testing.T) {
	c := client.New(testConfig("http://127.0.0.1:1"), []collector.Collector{
		panickingCollector{},
		collector.NewMemory("../collector/testdata/proc1"),
	})

	// The assertion is that this line returns at all.
	c.Prime(context.Background())

	if host := c.ScrapeOnce(context.Background()); host.GetMemTotal() == 0 {
		t.Error("the collector after the panicking one never ran")
	}
}

// A 503 is the opposite case and must keep its retry semantics -- the hub is
// down, not refusing this body.
func TestTransientRejectionRetainsTheBuffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c := client.New(testConfig(srv.URL), []collector.Collector{
		collector.NewMemory("../collector/testdata/proc1"),
	})

	c.ScrapeOnce(context.Background())
	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("a 503 must be reported as a failure")
	}
	if c.BufferDepth() != 1 {
		t.Errorf("buffer depth = %d after a 503, want 1: a transient failure must not drop data",
			c.BufferDepth())
	}
}
