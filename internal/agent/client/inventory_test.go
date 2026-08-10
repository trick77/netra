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

// inventoryCollector reports a different address set on each scrape, the way
// the real Addresses collector does when an address is removed.
type inventoryCollector struct{ sets [][]string }

func (*inventoryCollector) Name() string { return "inventory" }

func (c *inventoryCollector) Collect(context.Context) (*collector.Result, error) {
	if len(c.sets) == 0 {
		return &collector.Result{}, nil
	}
	set := c.sets[0]
	c.sets = c.sets[1:]

	rows := make([]*netrav1.HostAddress, 0, len(set))
	for _, a := range set {
		rows = append(rows, &netrav1.HostAddress{Iface: "eth0", Address: a, Family: 4})
	}
	return &collector.Result{Addresses: rows}, nil
}

// Inventory is a WHOLE SET that the hub replaces what it holds with -- it
// deletes anything the set omits. Concatenating the sets of several buffered
// scrapes into one request would send their UNION, and an address removed
// during an outage would then survive the replay on the hub forever.
func TestFlushSendsTheNewestInventorySetNotTheUnion(t *testing.T) {
	// Given: two buffered scrapes, the second reporting one fewer address.
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	col := &inventoryCollector{sets: [][]string{
		{"10.0.0.1", "10.0.0.2"},
		{"10.0.0.1"},
	}}
	c := client.NewWithInterval(cfg, []collector.Collector{col}, time.Millisecond)

	ctx := context.Background()
	c.ScrapeOnce(ctx)
	c.ScrapeOnce(ctx)

	// When: both go out in one flush.
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Then: the request carries only the newest set.
	req := rec.last()
	got := make([]string, 0, len(req.GetAddresses()))
	for _, a := range req.GetAddresses() {
		got = append(got, a.GetAddress())
	}
	if len(got) != 1 || got[0] != "10.0.0.1" {
		t.Errorf("addresses = %v, want [10.0.0.1]; the union would keep 10.0.0.2 alive on the hub forever", got)
	}
}

// nilResultCollector returns no result and no error, which the Collector
// contract does not describe. Nothing enforces the contract, and one collector
// slipping must not take the whole agent down with a nil dereference.
type nilResultCollector struct{}

func (nilResultCollector) Name() string { return "nilresult" }
func (nilResultCollector) Collect(context.Context) (*collector.Result, error) {
	return nil, nil //nolint:nilnil // deliberately the contract violation under test
}

func TestScrapeSurvivesACollectorReturningNoResultAndNoError(t *testing.T) {
	// Given: a client whose collector list holds such a collector.
	cfg := config.Config{
		HubURL:       "http://127.0.0.1:1",
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	c := client.NewWithInterval(cfg,
		[]collector.Collector{
			nilResultCollector{},
			collector.NewMemory(cfg.ProcRoot),
		},
		time.Millisecond)

	// When: a scrape runs. Then: it does not panic, and the rest is collected.
	s := c.ScrapeOnce(context.Background())
	if s == nil {
		t.Fatal("ScrapeOnce returned no host sample")
	}
	if s.MemTotal == nil {
		t.Error("the surviving collector's fields are missing from the sample")
	}
}

// Priming must not eat the address inventory.
//
// Prime runs every collector once and throws the result away, which is exactly
// right for a delta-based collector and data loss for a whole-set one. Packages
// was already excluded; Addresses was not, so priming consumed the set and left
// the collector believing it had reported. The first real scrape then carried
// no addresses -- and neither did any scrape after it, because nothing had
// changed. The hub cannot fill that gap: UpsertHostAddresses returns early on
// an empty set. A freshly enrolled host with a static address reported no
// addresses at all, indefinitely.
func TestPrimeDoesNotConsumeTheFirstAddressSet(t *testing.T) {
	// Given: an agent whose host has one address.
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	lister := func() ([]collector.Iface, error) {
		return []collector.Iface{
			{Name: "eth0", Index: 2, Addrs: []string{"10.0.0.5/24"}},
		}, nil
	}
	c := client.NewWithInterval(cfg,
		[]collector.Collector{collector.NewAddresses(lister)}, time.Millisecond)
	ctx := context.Background()

	// When: the agent primes at startup, then takes its first real scrape.
	c.Prime(ctx)
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Then: the address reaches the hub.
	addrs := rec.last().GetAddresses()
	if len(addrs) != 1 {
		t.Fatalf("addresses = %d in the first request, want 1 -- priming must not consume the set", len(addrs))
	}
	if got := addrs[0].GetAddress(); got != "10.0.0.5" {
		t.Errorf("address = %q, want 10.0.0.5", got)
	}
}

// rearmCollector reports a fixed set ONCE and then stays quiet, the way the
// real Addresses and Packages collectors do, and counts how often the agent
// asks it to report again.
type rearmCollector struct {
	reported bool
	resends  int
}

func (*rearmCollector) Name() string { return "rearm" }

func (c *rearmCollector) Collect(context.Context) (*collector.Result, error) {
	if c.reported {
		return &collector.Result{}, nil
	}
	c.reported = true
	return &collector.Result{Addresses: []*netrav1.HostAddress{
		{Iface: "eth0", Address: "10.0.0.1", Family: 4},
	}}, nil
}

func (c *rearmCollector) ResendInventory() {
	c.reported = false
	c.resends++
}

// An inventory set the ring dropped is gone unless the collector is told.
//
// Inventory collectors advance their own "already reported" state at collect
// time, so a scrape overwritten by the ring takes its set with it: the
// collector will not report it again, and the hub cannot notice because it
// stores inventory by replacement and returns early on an empty set. A static
// host would then serve a stale address list indefinitely.
func TestRingOverflowRearmsTheInventoryCollectorsOnceOnRecovery(t *testing.T) {
	// Given: a two-slot ring and a hub that is down, so every scrape past the
	// second overwrites the oldest.
	var reachable bool
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !reachable {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rec.handler(t).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_test",
		BufferWindow: 2 * time.Millisecond,
		ProcRoot:     "../collector/testdata/proc1",
	}
	col := &rearmCollector{}
	c := client.NewWithInterval(cfg, []collector.Collector{col}, time.Millisecond)
	if c.BufferCapacity() != 2 {
		t.Fatalf("BufferCapacity() = %d, want 2 for this test's arithmetic", c.BufferCapacity())
	}
	ctx := context.Background()

	// When: the outage runs long enough to drop several scrapes.
	for range 6 {
		c.ScrapeOnce(ctx)
		_ = c.Flush(ctx)
	}

	// Then: nothing has been re-armed YET. Re-arming per drop would re-parse a
	// 20 MB dpkg status file every scrape and push thousands of inventory rows
	// into a ring that is already overflowing -- making the overflow worse,
	// exactly when the host can least afford it.
	if col.resends != 0 {
		t.Fatalf("resends = %d during the outage, want 0 -- a drop must not re-arm on the spot",
			col.resends)
	}

	// When: the hub comes back and the backlog drains.
	reachable = true
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush after recovery: %v", err)
	}
	if c.BufferDepth() != 0 {
		t.Fatalf("BufferDepth() = %d, want 0 after recovery", c.BufferDepth())
	}

	// Then: exactly one re-arm, however many scrapes were dropped getting here.
	if col.resends != 1 {
		t.Fatalf("resends = %d after recovery, want exactly 1", col.resends)
	}

	// And: the next scrape carries the set, which then reaches the hub.
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := len(rec.last().GetAddresses()); got != 1 {
		t.Errorf("addresses in the request = %d, want 1 -- the re-armed set must reach the hub", got)
	}

	// And: a quiet, fully drained agent is not re-armed again.
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if col.resends != 1 {
		t.Errorf("resends = %d once recovered, want it to stay at 1", col.resends)
	}
}

// A 401 discards the WHOLE buffer, which may have held an inventory set the
// hub never saw. Once the token is fixed the agent must report it again rather
// than assume it landed -- but not before, since a set emitted while the token
// is still rejected goes straight into the buffer that is being dumped.
func TestUnauthorizedBufferDumpRearmsOnceTheTokenWorksAgain(t *testing.T) {
	// Given: a hub that rejects the token.
	var revoked = true
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if revoked {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rec.handler(t).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		HubURL:       srv.URL,
		Token:        "nta_revoked",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	col := &rearmCollector{}
	c := client.NewWithInterval(cfg, []collector.Collector{col}, time.Millisecond)
	ctx := context.Background()

	// When: the scrape carrying the inventory is flushed and rejected.
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush succeeded against a 401, want an error")
	}
	if c.BufferDepth() != 0 {
		t.Fatalf("BufferDepth() = %d, want 0 — a 401 drops the whole buffer", c.BufferDepth())
	}
	if col.resends != 0 {
		t.Fatalf("resends = %d while the token is still rejected, want 0", col.resends)
	}

	// When: the operator fixes the token and a flush gets through.
	revoked = false
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush after the token was fixed: %v", err)
	}

	// Then: the collector is re-armed, and its set reaches the hub.
	if col.resends != 1 {
		t.Fatalf("resends = %d after the token was fixed, want 1", col.resends)
	}
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := len(rec.last().GetAddresses()); got != 1 {
		t.Errorf("addresses in the request = %d, want 1 -- the re-armed set must reach the hub", got)
	}
}
