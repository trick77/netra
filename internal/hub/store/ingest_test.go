package store_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
	"github.com/trick77/netra/internal/hub/store"
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
// natural key plus ON CONFLICT DO NOTHING is what makes that harmless: the
// first write is authoritative and a replay with a changed value must not
// overwrite it, nor be reported as a new insert.
func TestIntegrationInsertHostSamplesIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	original := []*netrav1.HostSample{
		{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(1)},
		{TsMs: 1_700_000_060_000, CpuTotal: proto.Float64(2)},
	}

	n, err := s.InsertHostSamples(ctx, hostID, original)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if n != 2 {
		t.Fatalf("first insert returned %d, want 2", n)
	}

	// Same (host_id, ts) pair, but with a changed value: this must be
	// silently discarded rather than overwriting the original row.
	replayed := []*netrav1.HostSample{
		{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(99)},
		{TsMs: 1_700_000_060_000, CpuTotal: proto.Float64(99)},
	}

	n, err = s.InsertHostSamples(ctx, hostID, replayed)
	if err != nil {
		t.Fatalf("replayed insert: %v", err)
	}
	if n != 0 {
		t.Fatalf("replayed insert returned %d, want 0 — a full replay must insert nothing", n)
	}

	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples WHERE host_id = $1`, hostID).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 2 {
		t.Fatalf("row count = %d, want 2 after a replay", count)
	}

	var cpu float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT cpu_total FROM host_samples WHERE host_id = $1 ORDER BY ts ASC LIMIT 1`,
		hostID).Scan(&cpu); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cpu != 1 {
		t.Fatalf("cpu_total = %v, want 1 — the replay's changed value must not overwrite the original", cpu)
	}
}

// The later sample must win, and an older sample arriving after it (e.g. a
// reordered delivery) must not overwrite the newer value already stored.
func TestIntegrationUpsertHostCurrent(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	older := &netrav1.HostSample{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(10)}
	newer := &netrav1.HostSample{TsMs: 1_700_000_060_000, CpuTotal: proto.Float64(20)}

	if err := s.UpsertHostCurrent(ctx, hostID, older); err != nil {
		t.Fatalf("older upsert: %v", err)
	}
	if err := s.UpsertHostCurrent(ctx, hostID, newer); err != nil {
		t.Fatalf("newer upsert: %v", err)
	}
	// Apply the older sample again, as if it arrived late or out of order.
	// The guard clause in UpsertHostCurrent's ON CONFLICT must keep the
	// newer value in place.
	if err := s.UpsertHostCurrent(ctx, hostID, older); err != nil {
		t.Fatalf("re-applied older upsert: %v", err)
	}

	var cpu float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT cpu_total FROM host_current WHERE host_id = $1`, hostID).Scan(&cpu); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cpu != 20 {
		t.Fatalf("cpu_total = %v, want 20 — a stale sample must not overwrite the newer one", cpu)
	}
}

// Pins the column-to-placeholder mapping: every one of the 17 metric
// columns gets a distinct value, so a swap between two adjacent same-typed
// fields (e.g. cpu_user and cpu_system) would fail this test.
func TestIntegrationInsertHostSamplesAllColumnsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	sample := &netrav1.HostSample{
		TsMs:         1_700_000_000_000,
		CpuTotal:     proto.Float64(1.1),
		CpuUser:      proto.Float64(2.2),
		CpuSystem:    proto.Float64(3.3),
		CpuIowait:    proto.Float64(4.4),
		CpuSteal:     proto.Float64(5.5),
		CpuIdle:      proto.Float64(6.6),
		MemTotal:     proto.Uint64(10),
		MemUsed:      proto.Uint64(11),
		MemAvailable: proto.Uint64(12),
		MemBuffcache: proto.Uint64(13),
		MemZfsArc:    proto.Uint64(14),
		SwapTotal:    proto.Uint64(15),
		SwapUsed:     proto.Uint64(16),
		Load1:        proto.Float64(7.7),
		Load5:        proto.Float64(8.8),
		Load15:       proto.Float64(9.9),
		UptimeS:      proto.Uint64(17),
	}

	if _, err := s.InsertHostSamples(ctx, hostID, []*netrav1.HostSample{sample}); err != nil {
		t.Fatalf("InsertHostSamples: %v", err)
	}

	var (
		cpuTotal, cpuUser, cpuSystem, cpuIowait, cpuSteal, cpuIdle float64
		memTotal, memUsed, memAvailable, memBuffcache, memZfsArc   int64
		swapTotal, swapUsed                                        int64
		load1, load5, load15                                       float64
		uptimeS                                                    int64
	)
	if err := s.Pool().QueryRow(ctx, `
		SELECT cpu_total, cpu_user, cpu_system, cpu_iowait, cpu_steal, cpu_idle,
		       mem_total, mem_used, mem_available, mem_buffcache, mem_zfs_arc,
		       swap_total, swap_used,
		       load1, load5, load15, uptime_s
		  FROM host_samples WHERE host_id = $1`, hostID).Scan(
		&cpuTotal, &cpuUser, &cpuSystem, &cpuIowait, &cpuSteal, &cpuIdle,
		&memTotal, &memUsed, &memAvailable, &memBuffcache, &memZfsArc,
		&swapTotal, &swapUsed,
		&load1, &load5, &load15, &uptimeS,
	); err != nil {
		t.Fatalf("query: %v", err)
	}

	want := []struct {
		name string
		got  float64
		want float64
	}{
		{"cpu_total", cpuTotal, 1.1},
		{"cpu_user", cpuUser, 2.2},
		{"cpu_system", cpuSystem, 3.3},
		{"cpu_iowait", cpuIowait, 4.4},
		{"cpu_steal", cpuSteal, 5.5},
		{"cpu_idle", cpuIdle, 6.6},
		{"mem_total", float64(memTotal), 10},
		{"mem_used", float64(memUsed), 11},
		{"mem_available", float64(memAvailable), 12},
		{"mem_buffcache", float64(memBuffcache), 13},
		{"mem_zfs_arc", float64(memZfsArc), 14},
		{"swap_total", float64(swapTotal), 15},
		{"swap_used", float64(swapUsed), 16},
		{"load1", load1, 7.7},
		{"load5", load5, 8.8},
		{"load15", load15, 9.9},
		{"uptime_s", float64(uptimeS), 17},
	}
	for _, c := range want {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// A nil f64 (unset CpuTotal) must read back as SQL NULL, and a present but
// zero f64 (Load1) must read back as 0, not NULL — the same distinction the
// u64 helper is checked for above.
func TestIntegrationInsertHostSamplesFloatNullVsZero(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	samples := []*netrav1.HostSample{{
		TsMs: 1_700_000_000_000,
		// CpuTotal unset.
		Load1: proto.Float64(0), // present and zero
	}}

	if _, err := s.InsertHostSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("InsertHostSamples: %v", err)
	}

	var (
		cpuTotal *float64
		load1    *float64
	)
	if err := s.Pool().QueryRow(ctx,
		`SELECT cpu_total, load1 FROM host_samples WHERE host_id = $1`,
		hostID).Scan(&cpuTotal, &load1); err != nil {
		t.Fatalf("query: %v", err)
	}

	if cpuTotal != nil {
		t.Fatalf("cpu_total = %v, want NULL — the field was never set", *cpuTotal)
	}
	if load1 == nil {
		t.Fatal("load1 is NULL, want 0 — a present zero must not become NULL")
	}
	if *load1 != 0 {
		t.Fatalf("load1 = %v, want 0", *load1)
	}
}

// TestIntegrationSaveMetadataFlagsFingerprintMismatch covers spec §7.2: a
// token presented from a different fingerprint than the one on file must be
// flagged (logged), not rejected — this is what catches a compose file
// copied to a second host. The request must still succeed and the new
// fingerprint must still be stored.
func TestIntegrationSaveMetadataFlagsFingerprintMismatch(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	if err := s.SaveMetadata(ctx, hostID, []byte{1}, &netrav1.Metadata{
		Hostname: "h1", Fingerprint: "aaaa",
	}); err != nil {
		t.Fatalf("SaveMetadata (first): %v", err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := s.SaveMetadata(ctx, hostID, []byte{2}, &netrav1.Metadata{
		Hostname: "h1", Fingerprint: "bbbb",
	}); err != nil {
		t.Fatalf("SaveMetadata (second): %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "fingerprint") {
		t.Fatalf("log output = %q, want a fingerprint mismatch warning", logged)
	}
	for _, want := range []string{"aaaa", "bbbb"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log output = %q, want it to name the stored and reported fingerprints", logged)
		}
	}

	var fingerprint string
	if err := s.Pool().QueryRow(ctx,
		`SELECT fingerprint FROM hosts WHERE id = $1`, hostID).Scan(&fingerprint); err != nil {
		t.Fatalf("query: %v", err)
	}
	if fingerprint != "bbbb" {
		t.Fatalf("fingerprint = %q, want %q — mismatch is flagged, not rejected", fingerprint, "bbbb")
	}
}

// TestIntegrationSaveMetadataDoesNotFlagFirstSighting covers the empty-stored
// case: a host's first metadata report has nothing to compare against and
// must not be logged as a mismatch.
func TestIntegrationSaveMetadataDoesNotFlagFirstSighting(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := s.SaveMetadata(ctx, hostID, []byte{1}, &netrav1.Metadata{
		Hostname: "h1", Fingerprint: "aaaa",
	}); err != nil {
		t.Fatalf("SaveMetadata: %v", err)
	}

	if strings.Contains(buf.String(), "fingerprint changed") {
		t.Fatalf("log output = %q, want no mismatch warning on first sighting", buf.String())
	}
}

// One row per core, and a replayed row absorbed rather than duplicated or
// failed: a replayed batch re-sends rows the hub already has, and rejecting
// the INSERT would pin the agent's ring buffer on a batch it can never land.
func TestIntegrationInsertCpuCoreSamplesStoresOneRowPerCore(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	// Given: two cores measured at the same instant, one of them idle.
	ts := time.Now().Add(-time.Minute).UnixMilli()
	busy0, busy1 := 100.0, 0.0
	n, err := s.InsertCpuCoreSamples(ctx, hostID, []*netrav1.CpuCoreSample{
		{TsMs: ts, Core: 0, Busy: &busy0},
		{TsMs: ts, Core: 1, Busy: &busy1},
	})
	if err != nil {
		t.Fatalf("InsertCpuCoreSamples: %v", err)
	}
	if n != 2 {
		t.Fatalf("inserted = %d, want 2", n)
	}

	// When: the same row arrives again, as a replay would send it.
	if _, err := s.InsertCpuCoreSamples(ctx, hostID, []*netrav1.CpuCoreSample{
		{TsMs: ts, Core: 0, Busy: &busy0},
	}); err != nil {
		t.Fatalf("replay insert: %v", err)
	}

	// Then: it is absorbed, not duplicated.
	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM cpu_core_samples WHERE host_id = $1`, hostID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("rows = %d, want 2 -- a replayed row must not duplicate", count)
	}

	// And the idle core stored a real 0, not a NULL.
	var busy *float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT busy FROM cpu_core_samples WHERE host_id = $1 AND core = 1`, hostID).Scan(&busy); err != nil {
		t.Fatalf("select core 1: %v", err)
	}
	if busy == nil {
		t.Fatal("core 1 busy is NULL; a core measured at 0% must store 0")
	}
	if *busy != 0 {
		t.Errorf("core 1 busy = %v, want 0", *busy)
	}
}

// An unmeasurable core stores NULL, not zero. The distinction is the whole
// reason busy is optional: "not computable this scrape" is a different fact
// from "this core was idle", and a rollup averaging them together would be
// quietly wrong.
func TestIntegrationInsertCpuCoreSamplesPreservesUnsetBusyAsNull(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	ts := time.Now().Add(-time.Minute).UnixMilli()
	if _, err := s.InsertCpuCoreSamples(ctx, hostID, []*netrav1.CpuCoreSample{
		{TsMs: ts, Core: 0}, // Busy deliberately unset
	}); err != nil {
		t.Fatalf("InsertCpuCoreSamples: %v", err)
	}

	var busy *float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT busy FROM cpu_core_samples WHERE host_id = $1 AND core = 0`, hostID).Scan(&busy); err != nil {
		t.Fatalf("select: %v", err)
	}
	if busy != nil {
		t.Errorf("busy = %v, want NULL for an unmeasured core", *busy)
	}
}
