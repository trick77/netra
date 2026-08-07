package httpapi_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trick77/netra/agent/client"
	"github.com/trick77/netra/agent/collector"
	agentconfig "github.com/trick77/netra/agent/config"
	"github.com/trick77/netra/hub/internal/auth"
	"github.com/trick77/netra/hub/internal/httpapi"
	"github.com/trick77/netra/hub/internal/store"
)

// A real agent posting to a real hub backed by a real TimescaleDB. Everything
// below the HTTP boundary is the production code path.
func TestIntegrationAgentToHubRoundTrip(t *testing.T) {
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
		httpapi.NewRouter(auth.NewAuthenticator(s.Pool()), s, time.Minute))
	t.Cleanup(srv.Close)

	cfg := agentconfig.Config{
		HubURL:       srv.URL,
		Token:        token,
		Interval:     time.Minute,
		BufferWindow: time.Hour,
		ProcRoot:     "../../../agent/collector/testdata/proc1",
	}
	c := client.New(cfg, []collector.Collector{
		collector.NewMemory(cfg.ProcRoot, cfg.Interval),
		collector.NewLoad(cfg.ProcRoot, cfg.Interval),
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
