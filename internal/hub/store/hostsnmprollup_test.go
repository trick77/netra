package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// The IP and ICMP columns have to survive both continuous aggregates, or the
// three panels they feed empty out silently above the raw window -- which is
// exactly the failure pressurerollup_test.go documents for host_samples.
//
// Two 5m buckets with different values, and two rows inside the first, are
// what make a transposed AGGREGATE FUNCTION visible. With one row per bucket
// the avg and the max of that bucket are the same number, so a 1h view
// written as avg(x_max) -- the average of the worst buckets, rather than the
// worst reading in the hour -- passes without anyone noticing.
func TestIntegrationHostSnmpColumnsSurviveBothRollupTiers(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)
	base := rollupHour()

	// Bucket one holds 10 and 90, so its avg (50) and its max (90) are far
	// apart; bucket two holds a single 20. At the hourly tier that makes
	// avg = 35 and max = 90, and no transposition of the four expressions
	// produces both numbers.
	samples := []*netrav1.HostSample{
		{
			TsMs: base.UnixMilli(),
			// An error counter: avg AND max at both tiers.
			IpInDiscardsPerS: proto.Float64(10),
			// A volume counter: avg only, deliberately no _max peer.
			IpInReceivesPerS: proto.Float64(1000),
			// One from each of the other three blocks, so a block dropped
			// wholesale from a view is caught rather than assumed.
			Icmp6InErrorsPerS:            proto.Float64(10),
			IcmpInEchosPerS:              proto.Float64(1000),
			Icmp6InNeighborSolicitsPerS:  proto.Float64(1000),
			Icmp6OutNeighborSolicitsPerS: proto.Float64(1000),
		},
		{
			TsMs:                         base.Add(time.Minute).UnixMilli(),
			IpInDiscardsPerS:             proto.Float64(90),
			IpInReceivesPerS:             proto.Float64(3000),
			Icmp6InErrorsPerS:            proto.Float64(90),
			IcmpInEchosPerS:              proto.Float64(3000),
			Icmp6InNeighborSolicitsPerS:  proto.Float64(3000),
			Icmp6OutNeighborSolicitsPerS: proto.Float64(3000),
		},
		{
			TsMs:                         base.Add(7 * time.Minute).UnixMilli(),
			IpInDiscardsPerS:             proto.Float64(20),
			IpInReceivesPerS:             proto.Float64(1400),
			Icmp6InErrorsPerS:            proto.Float64(20),
			IcmpInEchosPerS:              proto.Float64(1400),
			Icmp6InNeighborSolicitsPerS:  proto.Float64(1400),
			Icmp6OutNeighborSolicitsPerS: proto.Float64(1400),
		},
	}
	if _, err := s.InsertHostSnmpSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("InsertHostSnmpSamples: %v", err)
	}

	refreshTiers(t, s, "host_snmp_samples")

	// 5m: bucket one averages 10 and 90 to 50 and peaks at 90; bucket two
	// holds the single 20.
	assertHostTier(t, s, "host_snmp_samples_5m", "ip_in_discards_per_s_avg", hostID, []float64{50, 20})
	assertHostTier(t, s, "host_snmp_samples_5m", "ip_in_discards_per_s_max", hostID, []float64{90, 20})
	assertHostTier(t, s, "host_snmp_samples_5m", "icmp6_in_errors_per_s_avg", hostID, []float64{50, 20})
	assertHostTier(t, s, "host_snmp_samples_5m", "icmp6_in_errors_per_s_max", hostID, []float64{90, 20})

	// Volume columns are avg only, so the 5m tier carries the mean of the
	// bucket and nothing else.
	assertHostTier(t, s, "host_snmp_samples_5m", "ip_in_receives_per_s_avg", hostID, []float64{2000, 1400})
	assertHostTier(t, s, "host_snmp_samples_5m", "icmp_in_echos_per_s_avg", hostID, []float64{2000, 1400})
	assertHostTier(t, s, "host_snmp_samples_5m", "icmp6_in_neighbor_solicits_per_s_avg", hostID, []float64{2000, 1400})

	// 1h: both buckets fall in one hour. avg is avg(50, 20) = 35 and max is
	// max(90, 20) = 90. avg(x_max) would give 55 and max(x_avg) would give
	// 50, so neither transposition survives.
	assertHostTier(t, s, "host_snmp_samples_1h", "ip_in_discards_per_s_avg", hostID, []float64{35})
	assertHostTier(t, s, "host_snmp_samples_1h", "ip_in_discards_per_s_max", hostID, []float64{90})
	assertHostTier(t, s, "host_snmp_samples_1h", "icmp6_in_errors_per_s_avg", hostID, []float64{35})
	assertHostTier(t, s, "host_snmp_samples_1h", "icmp6_in_errors_per_s_max", hostID, []float64{90})
	assertHostTier(t, s, "host_snmp_samples_1h", "ip_in_receives_per_s_avg", hostID, []float64{1700})
	assertHostTier(t, s, "host_snmp_samples_1h", "icmp_in_echos_per_s_avg", hostID, []float64{1700})
	assertHostTier(t, s, "host_snmp_samples_1h", "icmp6_out_neighbor_solicits_per_s_avg", hostID, []float64{1700})
}

// A volume column must NOT have a _max peer at either aggregate tier.
//
// Not pedantry about column counts: averaging is what makes a rate column
// composable across tiers, and a _max quietly added to one of these would be
// charted by peakBase() in the UI, which prefers _max wherever it exists. The
// IP statistics panel would then draw the burstiest single 60s scrape of each
// bucket as though it were the period's throughput.
func TestIntegrationHostSnmpVolumeColumnsHaveNoMaxPeer(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, view := range []string{"host_snmp_samples_5m", "host_snmp_samples_1h"} {
		for _, col := range []string{
			"ip_in_receives_per_s_max",
			"ip_out_requests_per_s_max",
			"icmp_in_msgs_per_s_max",
			"icmp_in_echos_per_s_max",
			"icmp6_in_neighbor_solicits_per_s_max",
		} {
			var exists bool
			if err := s.Pool().QueryRow(ctx,
				`SELECT EXISTS (
				    SELECT 1 FROM information_schema.columns
				     WHERE table_name = $1 AND column_name = $2)`,
				view, col).Scan(&exists); err != nil {
				t.Fatalf("look up %s.%s: %v", view, col, err)
			}
			if exists {
				t.Errorf("%s.%s exists; volume counters are averaged, never maxed", view, col)
			}
		}
	}
}
