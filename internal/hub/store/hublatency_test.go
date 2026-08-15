package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// The handshake gauges and the round trip are different measurements and must
// stay separately storable: post_latency_ms includes TLS, the hub's handling
// and the Postgres write, while hub_connect_us stops at SYN-ACK.
//
// The NULL case is the one that matters. An unreachable hub has no round-trip
// time AND no handshake time, but it does have failures -- so a row where the
// gauges are NULL and the counter is not is exactly what an outage looks like,
// and a 0 in either gauge would claim an instantaneous connection.
func TestIntegrationHubLatencyStoresGaugesAndCounterSeparately(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	samples := []*netrav1.HostSample{{
		TsMs: recentBucket().UnixMilli(),
		Agent: &netrav1.AgentSample{
			// The outage shape: no handshake completed, so both gauges are
			// unset while the counter records that they were attempted.
			HubConnectFailuresTotal: proto.Uint64(9),
		},
	}}
	if _, err := s.InsertAgentSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("InsertAgentSamples: %v", err)
	}

	var (
		connectMs  *int32
		connectMax *int32
		failures   *int64
	)
	if err := s.Pool().QueryRow(ctx,
		`SELECT hub_connect_us, hub_connect_max_us, hub_connect_failures_total
		   FROM agent_samples WHERE host_id = $1`,
		hostID).Scan(&connectMs, &connectMax, &failures); err != nil {
		t.Fatalf("query: %v", err)
	}

	if connectMs != nil {
		t.Errorf("hub_connect_us = %d during an outage; want NULL", *connectMs)
	}
	if connectMax != nil {
		t.Errorf("hub_connect_max_us = %d during an outage; want NULL", *connectMax)
	}
	if failures == nil {
		t.Fatal("hub_connect_failures_total is NULL; nothing would record the outage")
	}
	if *failures != 9 {
		t.Errorf("hub_connect_failures_total = %d, want 9", *failures)
	}
}

// A column added to agent_samples but forgotten in the two continuous
// aggregates is invisible until someone looks past the raw retention window.
// This table has form: rss_bytes and goroutines are in the raw table and in
// neither rollup.
func TestIntegrationHubLatencyReachesBothRollupTiers(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)
	base := rollupHour()

	// Two 5m buckets, distinct values, so a function transposition shows: with
	// one row per series the tier's avg, max and last are all the same number.
	samples := []*netrav1.HostSample{
		{TsMs: base.UnixMilli(), Agent: &netrav1.AgentSample{
			HubConnectUs:            proto.Uint32(10),
			HubConnectMaxUs:         proto.Uint32(30),
			HubConnectFailuresTotal: proto.Uint64(1),
		}},
		{TsMs: base.Add(time.Minute).UnixMilli(), Agent: &netrav1.AgentSample{
			HubConnectUs:            proto.Uint32(50),
			HubConnectMaxUs:         proto.Uint32(90),
			HubConnectFailuresTotal: proto.Uint64(2),
		}},
		{TsMs: base.Add(7 * time.Minute).UnixMilli(), Agent: &netrav1.AgentSample{
			HubConnectUs:            proto.Uint32(20),
			HubConnectMaxUs:         proto.Uint32(40),
			HubConnectFailuresTotal: proto.Uint64(4),
		}},
	}
	if _, err := s.InsertAgentSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("InsertAgentSamples: %v", err)
	}

	refreshTiers(t, s, "agent_samples")

	// 5m: first bucket averages 10 and 50 to 30, peaks at 50.
	assertHostTier(t, s, "agent_samples_5m", "hub_connect_us_avg", hostID, []float64{30, 20})
	assertHostTier(t, s, "agent_samples_5m", "hub_connect_us_max", hostID, []float64{50, 20})
	assertHostTier(t, s, "agent_samples_5m", "hub_connect_max_us_max", hostID, []float64{90, 40})
	assertHostTier(t, s, "agent_samples_5m", "hub_connect_failures_total", hostID, []float64{2, 4})

	// 1h: avg of avgs (30, 20) is 25, max of maxes is 50. max(x_avg) here
	// would report 30 -- the busiest five minutes -- as an instantaneous peak.
	assertHostTier(t, s, "agent_samples_1h", "hub_connect_us_avg", hostID, []float64{25})
	assertHostTier(t, s, "agent_samples_1h", "hub_connect_us_max", hostID, []float64{50})
	assertHostTier(t, s, "agent_samples_1h", "hub_connect_max_us_max", hostID, []float64{90})
	// A monotonic counter takes the largest, which is the value at the end.
	assertHostTier(t, s, "agent_samples_1h", "hub_connect_failures_total", hostID, []float64{4})
}
