package store_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// All seventy IP and ICMP columns, each with a distinct value, round-tripped
// through the real INSERT.
//
// Distinct values are the whole point. host_snmp_samples is written by a
// 72-placeholder statement whose column list and argument list are two
// separate sequences that have to stay in lockstep; a transposed adjacent
// pair is the defect this shape invites, and identical test values would let
// it through. Reading back into a map keyed by column name means a failure
// names the column rather than a position in a seventy-variable Scan.
func TestIntegrationHostSnmpColumnsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	sample := &netrav1.HostSample{
		TsMs: recentBucket().UnixMilli(),

		IpInReceivesPerS:                   proto.Float64(1000.5),
		IpInDeliversPerS:                   proto.Float64(1001.5),
		IpOutRequestsPerS:                  proto.Float64(1002.5),
		IpForwDatagramsPerS:                proto.Float64(1003.5),
		IpReasmOksPerS:                     proto.Float64(1004.5),
		IpFragOksPerS:                      proto.Float64(1005.5),
		IpInHdrErrorsPerS:                  proto.Float64(1006.5),
		IpInAddrErrorsPerS:                 proto.Float64(1007.5),
		IpInUnknownProtosPerS:              proto.Float64(1008.5),
		IpInDiscardsPerS:                   proto.Float64(1009.5),
		IpOutDiscardsPerS:                  proto.Float64(1010.5),
		IpOutNoRoutesPerS:                  proto.Float64(1011.5),
		IpReasmTimeoutPerS:                 proto.Float64(1012.5),
		Ip6InReceivesPerS:                  proto.Float64(1013.5),
		Ip6InDeliversPerS:                  proto.Float64(1014.5),
		Ip6OutRequestsPerS:                 proto.Float64(1015.5),
		Ip6OutForwDatagramsPerS:            proto.Float64(1016.5),
		Ip6ReasmOksPerS:                    proto.Float64(1017.5),
		Ip6FragOksPerS:                     proto.Float64(1018.5),
		Ip6InHdrErrorsPerS:                 proto.Float64(1019.5),
		Ip6InAddrErrorsPerS:                proto.Float64(1020.5),
		Ip6InUnknownProtosPerS:             proto.Float64(1021.5),
		Ip6InDiscardsPerS:                  proto.Float64(1022.5),
		Ip6OutDiscardsPerS:                 proto.Float64(1023.5),
		Ip6OutNoRoutesPerS:                 proto.Float64(1024.5),
		Ip6InNoRoutesPerS:                  proto.Float64(1025.5),
		Ip6InTooBigErrorsPerS:              proto.Float64(1026.5),
		Ip6ReasmTimeoutPerS:                proto.Float64(1027.5),
		IcmpInMsgsPerS:                     proto.Float64(1028.5),
		IcmpOutMsgsPerS:                    proto.Float64(1029.5),
		IcmpInErrorsPerS:                   proto.Float64(1030.5),
		IcmpOutErrorsPerS:                  proto.Float64(1031.5),
		IcmpInDestUnreachsPerS:             proto.Float64(1032.5),
		IcmpOutDestUnreachsPerS:            proto.Float64(1033.5),
		IcmpInTimeExcdsPerS:                proto.Float64(1034.5),
		IcmpOutTimeExcdsPerS:               proto.Float64(1035.5),
		IcmpInParmProbsPerS:                proto.Float64(1036.5),
		IcmpOutParmProbsPerS:               proto.Float64(1037.5),
		IcmpInRedirectsPerS:                proto.Float64(1038.5),
		IcmpOutRedirectsPerS:               proto.Float64(1039.5),
		IcmpInEchosPerS:                    proto.Float64(1040.5),
		IcmpOutEchosPerS:                   proto.Float64(1041.5),
		IcmpInEchoRepsPerS:                 proto.Float64(1042.5),
		IcmpOutEchoRepsPerS:                proto.Float64(1043.5),
		Icmp6InMsgsPerS:                    proto.Float64(1044.5),
		Icmp6OutMsgsPerS:                   proto.Float64(1045.5),
		Icmp6InErrorsPerS:                  proto.Float64(1046.5),
		Icmp6OutErrorsPerS:                 proto.Float64(1047.5),
		Icmp6InDestUnreachsPerS:            proto.Float64(1048.5),
		Icmp6OutDestUnreachsPerS:           proto.Float64(1049.5),
		Icmp6InTimeExcdsPerS:               proto.Float64(1050.5),
		Icmp6OutTimeExcdsPerS:              proto.Float64(1051.5),
		Icmp6InParmProblemsPerS:            proto.Float64(1052.5),
		Icmp6OutParmProblemsPerS:           proto.Float64(1053.5),
		Icmp6InPktTooBigsPerS:              proto.Float64(1054.5),
		Icmp6OutPktTooBigsPerS:             proto.Float64(1055.5),
		Icmp6InRedirectsPerS:               proto.Float64(1056.5),
		Icmp6OutRedirectsPerS:              proto.Float64(1057.5),
		Icmp6InEchosPerS:                   proto.Float64(1058.5),
		Icmp6OutEchosPerS:                  proto.Float64(1059.5),
		Icmp6InEchoRepliesPerS:             proto.Float64(1060.5),
		Icmp6OutEchoRepliesPerS:            proto.Float64(1061.5),
		Icmp6InNeighborSolicitsPerS:        proto.Float64(1062.5),
		Icmp6OutNeighborSolicitsPerS:       proto.Float64(1063.5),
		Icmp6InNeighborAdvertisementsPerS:  proto.Float64(1064.5),
		Icmp6OutNeighborAdvertisementsPerS: proto.Float64(1065.5),
		Icmp6InRouterSolicitsPerS:          proto.Float64(1066.5),
		Icmp6OutRouterSolicitsPerS:         proto.Float64(1067.5),
		Icmp6InRouterAdvertisementsPerS:    proto.Float64(1068.5),
		Icmp6OutRouterAdvertisementsPerS:   proto.Float64(1069.5),
	}

	if _, err := s.InsertHostSnmpSamples(ctx, hostID, []*netrav1.HostSample{sample}); err != nil {
		t.Fatalf("InsertHostSnmpSamples: %v", err)
	}

	want := map[string]float64{
		"ip_in_receives_per_s":                    1000.5,
		"ip_in_delivers_per_s":                    1001.5,
		"ip_out_requests_per_s":                   1002.5,
		"ip_forw_datagrams_per_s":                 1003.5,
		"ip_reasm_oks_per_s":                      1004.5,
		"ip_frag_oks_per_s":                       1005.5,
		"ip_in_hdr_errors_per_s":                  1006.5,
		"ip_in_addr_errors_per_s":                 1007.5,
		"ip_in_unknown_protos_per_s":              1008.5,
		"ip_in_discards_per_s":                    1009.5,
		"ip_out_discards_per_s":                   1010.5,
		"ip_out_no_routes_per_s":                  1011.5,
		"ip_reasm_timeout_per_s":                  1012.5,
		"ip6_in_receives_per_s":                   1013.5,
		"ip6_in_delivers_per_s":                   1014.5,
		"ip6_out_requests_per_s":                  1015.5,
		"ip6_out_forw_datagrams_per_s":            1016.5,
		"ip6_reasm_oks_per_s":                     1017.5,
		"ip6_frag_oks_per_s":                      1018.5,
		"ip6_in_hdr_errors_per_s":                 1019.5,
		"ip6_in_addr_errors_per_s":                1020.5,
		"ip6_in_unknown_protos_per_s":             1021.5,
		"ip6_in_discards_per_s":                   1022.5,
		"ip6_out_discards_per_s":                  1023.5,
		"ip6_out_no_routes_per_s":                 1024.5,
		"ip6_in_no_routes_per_s":                  1025.5,
		"ip6_in_too_big_errors_per_s":             1026.5,
		"ip6_reasm_timeout_per_s":                 1027.5,
		"icmp_in_msgs_per_s":                      1028.5,
		"icmp_out_msgs_per_s":                     1029.5,
		"icmp_in_errors_per_s":                    1030.5,
		"icmp_out_errors_per_s":                   1031.5,
		"icmp_in_dest_unreachs_per_s":             1032.5,
		"icmp_out_dest_unreachs_per_s":            1033.5,
		"icmp_in_time_excds_per_s":                1034.5,
		"icmp_out_time_excds_per_s":               1035.5,
		"icmp_in_parm_probs_per_s":                1036.5,
		"icmp_out_parm_probs_per_s":               1037.5,
		"icmp_in_redirects_per_s":                 1038.5,
		"icmp_out_redirects_per_s":                1039.5,
		"icmp_in_echos_per_s":                     1040.5,
		"icmp_out_echos_per_s":                    1041.5,
		"icmp_in_echo_reps_per_s":                 1042.5,
		"icmp_out_echo_reps_per_s":                1043.5,
		"icmp6_in_msgs_per_s":                     1044.5,
		"icmp6_out_msgs_per_s":                    1045.5,
		"icmp6_in_errors_per_s":                   1046.5,
		"icmp6_out_errors_per_s":                  1047.5,
		"icmp6_in_dest_unreachs_per_s":            1048.5,
		"icmp6_out_dest_unreachs_per_s":           1049.5,
		"icmp6_in_time_excds_per_s":               1050.5,
		"icmp6_out_time_excds_per_s":              1051.5,
		"icmp6_in_parm_problems_per_s":            1052.5,
		"icmp6_out_parm_problems_per_s":           1053.5,
		"icmp6_in_pkt_too_bigs_per_s":             1054.5,
		"icmp6_out_pkt_too_bigs_per_s":            1055.5,
		"icmp6_in_redirects_per_s":                1056.5,
		"icmp6_out_redirects_per_s":               1057.5,
		"icmp6_in_echos_per_s":                    1058.5,
		"icmp6_out_echos_per_s":                   1059.5,
		"icmp6_in_echo_replies_per_s":             1060.5,
		"icmp6_out_echo_replies_per_s":            1061.5,
		"icmp6_in_neighbor_solicits_per_s":        1062.5,
		"icmp6_out_neighbor_solicits_per_s":       1063.5,
		"icmp6_in_neighbor_advertisements_per_s":  1064.5,
		"icmp6_out_neighbor_advertisements_per_s": 1065.5,
		"icmp6_in_router_solicits_per_s":          1066.5,
		"icmp6_out_router_solicits_per_s":         1067.5,
		"icmp6_in_router_advertisements_per_s":    1068.5,
		"icmp6_out_router_advertisements_per_s":   1069.5,
	}
	if len(want) != 70 {
		t.Fatalf("expectation map covers %d columns, want all 70", len(want))
	}

	for col, expected := range want {
		var got *float64
		//nolint:gosec // column names are literals from this test's own map
		if err := s.Pool().QueryRow(ctx,
			`SELECT `+col+` FROM host_snmp_samples WHERE host_id = $1`,
			hostID).Scan(&got); err != nil {
			t.Fatalf("select %s: %v", col, err)
		}
		if got == nil {
			t.Errorf("%s is NULL, want %v", col, expected)
			continue
		}
		if *got != expected {
			t.Errorf("%s = %v, want %v", col, *got, expected)
		}
	}
}

// A scrape that carries no SNMP rates at all writes no row.
//
// This is the normal first scrape after an agent restart: every value here is
// a per-second rate, and a rate needs a baseline, so the first reading after
// a restart legitimately produces seventy NULLs. Storing that row would claim
// a measurement was taken -- and it would then be indistinguishable from a
// host whose /proc/net was unreadable.
func TestIntegrationHostSnmpSampleWithNoRatesWritesNoRow(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	// A sample that is otherwise a perfectly good host sample: it carries
	// CPU, which lands in host_samples, and nothing this table stores.
	sample := &netrav1.HostSample{
		TsMs:     recentBucket().UnixMilli(),
		CpuTotal: proto.Float64(17.5),
	}

	n, err := s.InsertHostSnmpSamples(ctx, hostID, []*netrav1.HostSample{sample})
	if err != nil {
		t.Fatalf("InsertHostSnmpSamples: %v", err)
	}
	if n != 0 {
		t.Errorf("rows written = %d, want 0", n)
	}

	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_snmp_samples WHERE host_id = $1`,
		hostID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("host_snmp_samples rows = %d, want 0", count)
	}
}
