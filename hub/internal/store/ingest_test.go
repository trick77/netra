package store_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/hub/internal/store"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func seedHost(t *testing.T, s *store.Store) int32 {
	t.Helper()
	var id int32
	if err := s.Pool().QueryRow(context.Background(),
		`INSERT INTO hosts (hostname) VALUES ('h1') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	return id
}

func TestIntegrationInsertHostSamplesPreservesNulls(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	samples := []*netrav1.HostSample{{
		TsMs:     1_700_000_000_000,
		CpuTotal: proto.Float64(12.5),
		SwapUsed: proto.Uint64(0), // present and zero
		// MemZfsArc unset: this host has no ZFS
	}}

	n, err := s.InsertHostSamples(ctx, hostID, samples)
	if err != nil {
		t.Fatalf("InsertHostSamples: %v", err)
	}
	if n != 1 {
		t.Fatalf("inserted = %d, want 1", n)
	}

	var (
		swapUsed  *int64
		memZfsArc *int64
	)
	if err := s.Pool().QueryRow(ctx,
		`SELECT swap_used, mem_zfs_arc FROM host_samples WHERE host_id = $1`,
		hostID).Scan(&swapUsed, &memZfsArc); err != nil {
		t.Fatalf("query: %v", err)
	}

	if swapUsed == nil {
		t.Fatal("swap_used is NULL, want 0 — a present zero must not become NULL")
	}
	if *swapUsed != 0 {
		t.Fatalf("swap_used = %d, want 0", *swapUsed)
	}
	if memZfsArc != nil {
		t.Fatalf("mem_zfs_arc = %d, want NULL — this host has no ZFS", *memZfsArc)
	}
}

// Replay after an outage re-sends batches the hub may already hold. The
// natural key plus ON CONFLICT DO NOTHING is what makes that harmless.
func TestIntegrationInsertHostSamplesIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	samples := []*netrav1.HostSample{
		{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(1)},
		{TsMs: 1_700_000_060_000, CpuTotal: proto.Float64(2)},
	}

	if _, err := s.InsertHostSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := s.InsertHostSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("replayed insert: %v", err)
	}

	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples WHERE host_id = $1`, hostID).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 2 {
		t.Fatalf("row count = %d, want 2 after a replay", count)
	}
}

func TestIntegrationUpsertHostCurrent(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	first := &netrav1.HostSample{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(10)}
	second := &netrav1.HostSample{TsMs: 1_700_000_060_000, CpuTotal: proto.Float64(20)}

	if err := s.UpsertHostCurrent(ctx, hostID, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := s.UpsertHostCurrent(ctx, hostID, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var cpu float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT cpu_total FROM host_current WHERE host_id = $1`, hostID).Scan(&cpu); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cpu != 20 {
		t.Fatalf("cpu_total = %v, want 20 — the later sample must win", cpu)
	}
}
