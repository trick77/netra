package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// recentBucket is a timestamp an hour ago, aligned to a 5-minute boundary.
//
// A fixed timestamp in the past cannot be used here: these hypertables carry
// a 7-day retention policy, and TimescaleDB's background scheduler will drop
// a chunk that old out from under the test. That produces a failure that only
// appears when something slow ran first and gave the scheduler time to start
// -- so it looks like a test-ordering bug rather than the race it is.
//
// An hour ago is inside retention and in a bucket that has already closed,
// which is also what a continuous aggregate will refresh.
func recentBucket() time.Time {
	return time.Now().UTC().Add(-time.Hour).Truncate(5 * time.Minute)
}

// post_latency_ms is NULL for every scrape taken while the hub was
// unreachable, and that NULL is the measurement. A zero would claim an
// instantaneous round trip during an outage.
func TestIntegrationInsertAgentSamplesPreservesNulls(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	samples := []*netrav1.HostSample{{
		TsMs: recentBucket().UnixMilli(),
		Agent: &netrav1.AgentSample{
			ScrapeDurationMs: proto.Uint32(0), // present and zero: a fast scrape
			BufferDepth:      proto.Uint32(3),
			// PostLatencyMs unset: the hub was unreachable
			// UptimeS unset: the Self collector has not landed yet
		},
	}}

	n, err := s.InsertAgentSamples(ctx, hostID, samples)
	if err != nil {
		t.Fatalf("InsertAgentSamples: %v", err)
	}
	if n != 1 {
		t.Fatalf("inserted = %d, want 1", n)
	}

	var (
		scrapeDuration *int32
		postLatency    *int32
		uptime         *int64
	)
	if err := s.Pool().QueryRow(ctx,
		`SELECT scrape_duration_ms, post_latency_ms, uptime_s
		   FROM agent_samples WHERE host_id = $1`,
		hostID).Scan(&scrapeDuration, &postLatency, &uptime); err != nil {
		t.Fatalf("query: %v", err)
	}

	if scrapeDuration == nil {
		t.Fatal("scrape_duration_ms is NULL, want 0 — a present zero must not become NULL")
	}
	if *scrapeDuration != 0 {
		t.Fatalf("scrape_duration_ms = %d, want 0", *scrapeDuration)
	}
	if postLatency != nil {
		t.Fatalf("post_latency_ms = %d, want NULL — the hub was unreachable", *postLatency)
	}
	if uptime != nil {
		t.Fatalf("uptime_s = %d, want NULL — the Self collector has not landed", *uptime)
	}
}

// An agent too old to send self-telemetry has said nothing about itself,
// which is different from having reported nothing. It must not leave an
// all-NULL row behind.
func TestIntegrationSamplesWithoutAgentTelemetryInsertNoRow(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	base := recentBucket()
	samples := []*netrav1.HostSample{
		{TsMs: base.UnixMilli()},
		{
			TsMs:  base.Add(time.Minute).UnixMilli(),
			Agent: &netrav1.AgentSample{BufferDepth: proto.Uint32(1)},
		},
	}

	n, err := s.InsertAgentSamples(ctx, hostID, samples)
	if err != nil {
		t.Fatalf("InsertAgentSamples: %v", err)
	}
	if n != 1 {
		t.Fatalf("inserted = %d, want 1 — only the sample carrying telemetry", n)
	}
}

// These rows share host_samples' natural key and are replayed with it, so
// they must dedupe the same way. Without this a recovering agent would fail
// its whole batch on a primary-key violation.
func TestIntegrationInsertAgentSamplesIsIdempotentOnReplay(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	samples := []*netrav1.HostSample{{
		TsMs:  recentBucket().UnixMilli(),
		Agent: &netrav1.AgentSample{ScrapeDurationMs: proto.Uint32(12)},
	}}

	if _, err := s.InsertAgentSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	n, err := s.InsertAgentSamples(ctx, hostID, samples)
	if err != nil {
		t.Fatalf("replayed insert: %v", err)
	}
	if n != 0 {
		t.Fatalf("inserted = %d on replay, want 0", n)
	}

	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM agent_samples WHERE host_id = $1`, hostID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("rows = %d, want 1", count)
	}
}

func TestIntegrationAgentSamplesIsAHypertable(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.hypertables
		  WHERE hypertable_name = 'agent_samples'`).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("agent_samples hypertables = %d, want 1", count)
	}
}

// The point of D5's squash into 0001 was that the new metrics get full rollup
// coverage rather than being raw-only. If a column reaches host_samples but
// not the aggregates, it silently disappears after 7 days -- so assert it
// actually materialises rather than trusting the view definition.
func TestIntegrationNewHostColumnsReachTheRollup(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	// Two samples in the same 5-minute bucket, so avg and max differ.
	base := recentBucket()
	samples := []*netrav1.HostSample{
		{
			TsMs:      base.UnixMilli(),
			CtxtPerS:  proto.Float64(100),
			BootTimeS: proto.Uint64(1_699_000_000),
		},
		{
			TsMs:      base.Add(time.Minute).UnixMilli(),
			CtxtPerS:  proto.Float64(300),
			BootTimeS: proto.Uint64(1_699_000_000),
		},
	}
	if _, err := s.InsertHostSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("InsertHostSamples: %v", err)
	}

	if _, err := s.Pool().Exec(ctx,
		`CALL refresh_continuous_aggregate('host_samples_5m', NULL, NULL)`); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	var (
		ctxtAvg  *float64
		ctxtMax  *float64
		bootTime *int64
	)
	if err := s.Pool().QueryRow(ctx,
		`SELECT ctxt_per_s_avg, ctxt_per_s_max, boot_time_s
		   FROM host_samples_5m WHERE host_id = $1`,
		hostID).Scan(&ctxtAvg, &ctxtMax, &bootTime); err != nil {
		t.Fatalf("query rollup: %v", err)
	}

	if ctxtAvg == nil || *ctxtAvg != 200 {
		t.Errorf("ctxt_per_s_avg = %v, want 200", ctxtAvg)
	}
	if ctxtMax == nil || *ctxtMax != 300 {
		t.Errorf("ctxt_per_s_max = %v, want 300 — a mean would hide the spike", ctxtMax)
	}
	if bootTime == nil || *bootTime != 1_699_000_000 {
		t.Errorf("boot_time_s = %v, want 1699000000 carried through as last()", bootTime)
	}
}

// The agent's own account of where it is.
//
// This test exists because the fields did not: the agent read AGENT_LOCATION,
// AGENT_PROVIDER and AGENT_FACILITY, put all three on every Metadata post, and
// this UPDATE simply never listed the columns -- so three variables an
// operator could set reached the hub and went on the floor, with nothing
// anywhere that could have shown them. Nothing failed; the data was silently
// dropped.
func TestIntegrationSaveMetadataStoresTheReportedLocation(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	md := &netrav1.Metadata{
		Hostname: "h1",
		Location: "Roubaix, France",
		Provider: "OVH",
		Facility: "RBX2",
	}
	if err := s.SaveMetadata(ctx, hostID, []byte("hash"), md); err != nil {
		t.Fatalf("SaveMetadata: %v", err)
	}

	var location, provider, facility *string
	if err := s.Pool().QueryRow(ctx,
		`SELECT location, provider, facility FROM hosts WHERE id = $1`,
		hostID).Scan(&location, &provider, &facility); err != nil {
		t.Fatalf("query: %v", err)
	}
	if location == nil || *location != "Roubaix, France" {
		t.Errorf("location = %v, want Roubaix, France", location)
	}
	if provider == nil || *provider != "OVH" {
		t.Errorf("provider = %v, want OVH", provider)
	}
	if facility == nil || *facility != "RBX2" {
		t.Errorf("facility = %v, want RBX2", facility)
	}
}

// Declared and left blank is the same as not declared: an operator who writes
// AGENT_LOCATION= in a unit file sends "", and a host whose location is the
// empty string would draw a separator with nothing either side of it. NULLIF
// is what keeps that out, the same way it does for kernel and os_name.
func TestIntegrationSaveMetadataStoresABlankLocationAsNull(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	if err := s.SaveMetadata(ctx, hostID, []byte("hash"),
		&netrav1.Metadata{Hostname: "h1", Location: "", Provider: ""}); err != nil {
		t.Fatalf("SaveMetadata: %v", err)
	}

	var location, provider *string
	if err := s.Pool().QueryRow(ctx,
		`SELECT location, provider FROM hosts WHERE id = $1`,
		hostID).Scan(&location, &provider); err != nil {
		t.Fatalf("query: %v", err)
	}
	if location != nil {
		t.Errorf("location = %q, want nil", *location)
	}
	if provider != nil {
		t.Errorf("provider = %q, want nil", *provider)
	}
}

// Capabilities are the hub's only record of why a metric is NULL.
func TestIntegrationSaveMetadataStoresCapabilities(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	md := &netrav1.Metadata{
		Hostname:     "h1",
		Capabilities: map[string]string{"processes": "namespaced", "users": "ok"},
	}
	if err := s.SaveMetadata(ctx, hostID, []byte("hash"), md); err != nil {
		t.Fatalf("SaveMetadata: %v", err)
	}

	var processes, users *string
	if err := s.Pool().QueryRow(ctx,
		`SELECT capabilities->>'processes', capabilities->>'users'
		   FROM hosts WHERE id = $1`, hostID).Scan(&processes, &users); err != nil {
		t.Fatalf("query: %v", err)
	}

	if processes == nil || *processes != "namespaced" {
		t.Errorf("capabilities->processes = %v, want namespaced", processes)
	}
	if users == nil || *users != "ok" {
		t.Errorf("capabilities->users = %v, want ok", users)
	}
}

// The column is NOT NULL DEFAULT '{}', so an agent reporting no capabilities
// writes an empty object. Unlike the sample columns, absence is not a
// distinct fact here -- there is nothing to flag either way -- and writing
// NULL would violate the constraint outright.
func TestIntegrationSaveMetadataWithoutCapabilitiesStoresEmptyObject(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	if err := s.SaveMetadata(ctx, hostID, []byte("hash"),
		&netrav1.Metadata{Hostname: "h1"}); err != nil {
		t.Fatalf("SaveMetadata: %v", err)
	}

	var caps string
	if err := s.Pool().QueryRow(ctx,
		`SELECT capabilities::text FROM hosts WHERE id = $1`, hostID).Scan(&caps); err != nil {
		t.Fatalf("query: %v", err)
	}
	if caps != "{}" {
		t.Errorf("capabilities = %q, want %q", caps, "{}")
	}
}
