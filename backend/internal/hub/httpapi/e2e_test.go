package httpapi_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
	agentconfig "github.com/trick77/netra/internal/agent/config"
	"github.com/trick77/netra/internal/hub/auth"
	hubconfig "github.com/trick77/netra/internal/hub/config"
	"github.com/trick77/netra/internal/hub/httpapi"
	"github.com/trick77/netra/internal/hub/store"
)

// newE2EFixture builds a migrated store, a registered host with a live token,
// and a server running the production router.
func newE2EFixture(t *testing.T) (*store.Store, int32, string, *httptest.Server) {
	t.Helper()
	ctx := context.Background()

	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('e2e') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	token, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID, hash); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	srv := httptest.NewServer(
		httpapi.NewRouter(auth.NewAuthenticator(s.Pool()), s,
			hubconfig.Config{AdminToken: testAdminToken}, nil))
	t.Cleanup(srv.Close)

	return s, hostID, token, srv
}

// A real agent posting to a real hub backed by a real TimescaleDB. Everything
// below the HTTP boundary is the production code path.
func TestIntegrationAgentToHubRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, hostID, token, srv := newE2EFixture(t)

	cfg := agentconfig.Config{
		HubURL:       srv.URL,
		Token:        token,
		BufferWindow: time.Hour,
		ProcRoot:     "../../../internal/agent/collector/testdata/proc1",
	}
	c := client.New(cfg, []collector.Collector{
		collector.NewMemory(cfg.ProcRoot),
		collector.NewLoad(cfg.ProcRoot),
	})

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var (
		count     int
		memTotal  *int64
		load1     *float64
		swapTotal *int64
	)
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples WHERE host_id = $1`, hostID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("stored rows = %d, want 1", count)
	}

	if err := s.Pool().QueryRow(ctx,
		`SELECT mem_total, load1, swap_total FROM host_samples WHERE host_id = $1`,
		hostID).Scan(&memTotal, &load1, &swapTotal); err != nil {
		t.Fatalf("select: %v", err)
	}

	if memTotal == nil || *memTotal != 16_384_000*1024 {
		t.Fatalf("mem_total = %v, want %d", memTotal, int64(16_384_000*1024))
	}
	if load1 == nil || *load1 < 0.51 || *load1 > 0.53 {
		t.Fatalf("load1 = %v, want ~0.52", load1)
	}
	if swapTotal == nil {
		t.Fatal("swap_total is NULL, want a value — the fixture host has swap")
	}

	// Before any metadata has been supplied, the host row must not carry an
	// agent_version. This is what makes the next assertion a real proof of
	// the handshake rather than an assumption: if the client attached
	// metadata unconditionally on every flush, this would already be set.
	var agentVersionBefore *string
	if err := s.Pool().QueryRow(ctx,
		`SELECT agent_version FROM hosts WHERE id = $1`, hostID).Scan(&agentVersionBefore); err != nil {
		t.Fatalf("select agent_version before handshake: %v", err)
	}
	if agentVersionBefore != nil {
		t.Fatalf("agent_version = %q after first flush, want NULL — metadata should not be sent until the hub asks", *agentVersionBefore)
	}

	// The hub had no metadata, so it must have asked; the next flush supplies
	// it and the hostname lands on the host row.
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}

	var agentVersion *string
	if err := s.Pool().QueryRow(ctx,
		`SELECT agent_version FROM hosts WHERE id = $1`, hostID).Scan(&agentVersion); err != nil {
		t.Fatalf("select agent_version: %v", err)
	}
	if agentVersion == nil || *agentVersion == "" {
		t.Fatal("agent_version is empty, want it populated by the metadata handshake")
	}

	var lastSeen *time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM host_current WHERE host_id = $1`, hostID).Scan(&lastSeen); err != nil {
		t.Fatalf("select host_current: %v", err)
	}
	if lastSeen == nil {
		t.Fatal("host_current.last_seen is NULL, want it updated on ingest")
	}
}

// The whole path for a per-entity family, with nothing stubbed: a real
// collector reads a real /proc fixture, the real client buffers and posts it,
// the real handler stores it. The unit tests each cover one hop; this is the
// one that fails if the hops disagree -- a field number changed on one side, a
// family dropped during flush assembly, a row filtered out by the wrong bound.
func TestIntegrationEndToEndPerCoreCPUReachesTheDatabase(t *testing.T) {
	ctx := context.Background()
	s, hostID, token, srv := newE2EFixture(t)

	cfg := agentconfig.Config{
		HubURL:       srv.URL,
		Token:        token,
		BufferWindow: time.Hour,
		ProcRoot:     "../../../internal/agent/collector/testdata/percpu/first",
	}
	percpu := collector.NewPerCoreCPU(cfg.ProcRoot)
	c := client.New(cfg, []collector.Collector{percpu})

	// The first scrape is the baseline: a rate has nothing to average over
	// yet, so nothing should reach the database.
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("baseline Flush: %v", err)
	}

	var baseline int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM cpu_core_samples WHERE host_id = $1`, hostID).Scan(&baseline); err != nil {
		t.Fatalf("count after baseline: %v", err)
	}
	if baseline != 0 {
		t.Fatalf("cpu_core_samples = %d after the baseline scrape, want 0", baseline)
	}

	// Advance the counters and scrape again.
	percpu.SetProcRootForTest("../../../internal/agent/collector/testdata/percpu/second")
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// One row per core, with the percentages the fixtures encode: core 0
	// fully busy over the interval, core 1 idle.
	rows, err := s.Pool().Query(ctx,
		`SELECT core, busy FROM cpu_core_samples WHERE host_id = $1 ORDER BY core`, hostID)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()

	got := map[int32]*float64{}
	for rows.Next() {
		var core int32
		var busy *float64
		if err := rows.Scan(&core, &busy); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[core] = busy
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("cores stored = %d, want 2", len(got))
	}
	if got[0] == nil || *got[0] != 100 {
		t.Errorf("core 0 busy = %v, want 100", got[0])
	}
	if got[1] == nil || *got[1] != 0 {
		t.Errorf("core 1 busy = %v, want 0", got[1])
	}
}
