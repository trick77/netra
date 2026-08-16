package netrav1_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// The seventy IP and ICMP fields, marshalled, unmarshalled and read back
// through their getters.
//
// Every value is distinct, and that is the assertion: these were appended as
// tags 74 to 143 in one block, which is the shape where a field number typed
// twice puts two meanings on one wire tag. A duplicate tag surfaces here as a
// field returning its neighbour's value, not as a compile error.
func TestHostSampleSnmpFieldsSet(t *testing.T) {
	in := &netrav1.HostSample{
		TsMs: 1_700_000_000_123,

		IpInReceivesPerS:                   proto.Float64(1.5),
		IpInDeliversPerS:                   proto.Float64(2.5),
		IpOutRequestsPerS:                  proto.Float64(3.5),
		IpForwDatagramsPerS:                proto.Float64(4.5),
		IpReasmOksPerS:                     proto.Float64(5.5),
		IpFragOksPerS:                      proto.Float64(6.5),
		IpInHdrErrorsPerS:                  proto.Float64(7.5),
		IpInAddrErrorsPerS:                 proto.Float64(8.5),
		IpInUnknownProtosPerS:              proto.Float64(9.5),
		IpInDiscardsPerS:                   proto.Float64(10.5),
		IpOutDiscardsPerS:                  proto.Float64(11.5),
		IpOutNoRoutesPerS:                  proto.Float64(12.5),
		IpReasmTimeoutPerS:                 proto.Float64(13.5),
		Ip6InReceivesPerS:                  proto.Float64(14.5),
		Ip6InDeliversPerS:                  proto.Float64(15.5),
		Ip6OutRequestsPerS:                 proto.Float64(16.5),
		Ip6OutForwDatagramsPerS:            proto.Float64(17.5),
		Ip6ReasmOksPerS:                    proto.Float64(18.5),
		Ip6FragOksPerS:                     proto.Float64(19.5),
		Ip6InHdrErrorsPerS:                 proto.Float64(20.5),
		Ip6InAddrErrorsPerS:                proto.Float64(21.5),
		Ip6InUnknownProtosPerS:             proto.Float64(22.5),
		Ip6InDiscardsPerS:                  proto.Float64(23.5),
		Ip6OutDiscardsPerS:                 proto.Float64(24.5),
		Ip6OutNoRoutesPerS:                 proto.Float64(25.5),
		Ip6InNoRoutesPerS:                  proto.Float64(26.5),
		Ip6InTooBigErrorsPerS:              proto.Float64(27.5),
		Ip6ReasmTimeoutPerS:                proto.Float64(28.5),
		IcmpInMsgsPerS:                     proto.Float64(29.5),
		IcmpOutMsgsPerS:                    proto.Float64(30.5),
		IcmpInErrorsPerS:                   proto.Float64(31.5),
		IcmpOutErrorsPerS:                  proto.Float64(32.5),
		IcmpInDestUnreachsPerS:             proto.Float64(33.5),
		IcmpOutDestUnreachsPerS:            proto.Float64(34.5),
		IcmpInTimeExcdsPerS:                proto.Float64(35.5),
		IcmpOutTimeExcdsPerS:               proto.Float64(36.5),
		IcmpInParmProbsPerS:                proto.Float64(37.5),
		IcmpOutParmProbsPerS:               proto.Float64(38.5),
		IcmpInRedirectsPerS:                proto.Float64(39.5),
		IcmpOutRedirectsPerS:               proto.Float64(40.5),
		IcmpInEchosPerS:                    proto.Float64(41.5),
		IcmpOutEchosPerS:                   proto.Float64(42.5),
		IcmpInEchoRepsPerS:                 proto.Float64(43.5),
		IcmpOutEchoRepsPerS:                proto.Float64(44.5),
		Icmp6InMsgsPerS:                    proto.Float64(45.5),
		Icmp6OutMsgsPerS:                   proto.Float64(46.5),
		Icmp6InErrorsPerS:                  proto.Float64(47.5),
		Icmp6OutErrorsPerS:                 proto.Float64(48.5),
		Icmp6InDestUnreachsPerS:            proto.Float64(49.5),
		Icmp6OutDestUnreachsPerS:           proto.Float64(50.5),
		Icmp6InTimeExcdsPerS:               proto.Float64(51.5),
		Icmp6OutTimeExcdsPerS:              proto.Float64(52.5),
		Icmp6InParmProblemsPerS:            proto.Float64(53.5),
		Icmp6OutParmProblemsPerS:           proto.Float64(54.5),
		Icmp6InPktTooBigsPerS:              proto.Float64(55.5),
		Icmp6OutPktTooBigsPerS:             proto.Float64(56.5),
		Icmp6InRedirectsPerS:               proto.Float64(57.5),
		Icmp6OutRedirectsPerS:              proto.Float64(58.5),
		Icmp6InEchosPerS:                   proto.Float64(59.5),
		Icmp6OutEchosPerS:                  proto.Float64(60.5),
		Icmp6InEchoRepliesPerS:             proto.Float64(61.5),
		Icmp6OutEchoRepliesPerS:            proto.Float64(62.5),
		Icmp6InNeighborSolicitsPerS:        proto.Float64(63.5),
		Icmp6OutNeighborSolicitsPerS:       proto.Float64(64.5),
		Icmp6InNeighborAdvertisementsPerS:  proto.Float64(65.5),
		Icmp6OutNeighborAdvertisementsPerS: proto.Float64(66.5),
		Icmp6InRouterSolicitsPerS:          proto.Float64(67.5),
		Icmp6OutRouterSolicitsPerS:         proto.Float64(68.5),
		Icmp6InRouterAdvertisementsPerS:    proto.Float64(69.5),
		Icmp6OutRouterAdvertisementsPerS:   proto.Float64(70.5),
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out netrav1.HostSample
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cases := []struct {
		name string
		got  float64
		want float64
	}{
		{"IpInReceivesPerS", out.GetIpInReceivesPerS(), 1.5},
		{"IpInDeliversPerS", out.GetIpInDeliversPerS(), 2.5},
		{"IpOutRequestsPerS", out.GetIpOutRequestsPerS(), 3.5},
		{"IpForwDatagramsPerS", out.GetIpForwDatagramsPerS(), 4.5},
		{"IpReasmOksPerS", out.GetIpReasmOksPerS(), 5.5},
		{"IpFragOksPerS", out.GetIpFragOksPerS(), 6.5},
		{"IpInHdrErrorsPerS", out.GetIpInHdrErrorsPerS(), 7.5},
		{"IpInAddrErrorsPerS", out.GetIpInAddrErrorsPerS(), 8.5},
		{"IpInUnknownProtosPerS", out.GetIpInUnknownProtosPerS(), 9.5},
		{"IpInDiscardsPerS", out.GetIpInDiscardsPerS(), 10.5},
		{"IpOutDiscardsPerS", out.GetIpOutDiscardsPerS(), 11.5},
		{"IpOutNoRoutesPerS", out.GetIpOutNoRoutesPerS(), 12.5},
		{"IpReasmTimeoutPerS", out.GetIpReasmTimeoutPerS(), 13.5},
		{"Ip6InReceivesPerS", out.GetIp6InReceivesPerS(), 14.5},
		{"Ip6InDeliversPerS", out.GetIp6InDeliversPerS(), 15.5},
		{"Ip6OutRequestsPerS", out.GetIp6OutRequestsPerS(), 16.5},
		{"Ip6OutForwDatagramsPerS", out.GetIp6OutForwDatagramsPerS(), 17.5},
		{"Ip6ReasmOksPerS", out.GetIp6ReasmOksPerS(), 18.5},
		{"Ip6FragOksPerS", out.GetIp6FragOksPerS(), 19.5},
		{"Ip6InHdrErrorsPerS", out.GetIp6InHdrErrorsPerS(), 20.5},
		{"Ip6InAddrErrorsPerS", out.GetIp6InAddrErrorsPerS(), 21.5},
		{"Ip6InUnknownProtosPerS", out.GetIp6InUnknownProtosPerS(), 22.5},
		{"Ip6InDiscardsPerS", out.GetIp6InDiscardsPerS(), 23.5},
		{"Ip6OutDiscardsPerS", out.GetIp6OutDiscardsPerS(), 24.5},
		{"Ip6OutNoRoutesPerS", out.GetIp6OutNoRoutesPerS(), 25.5},
		{"Ip6InNoRoutesPerS", out.GetIp6InNoRoutesPerS(), 26.5},
		{"Ip6InTooBigErrorsPerS", out.GetIp6InTooBigErrorsPerS(), 27.5},
		{"Ip6ReasmTimeoutPerS", out.GetIp6ReasmTimeoutPerS(), 28.5},
		{"IcmpInMsgsPerS", out.GetIcmpInMsgsPerS(), 29.5},
		{"IcmpOutMsgsPerS", out.GetIcmpOutMsgsPerS(), 30.5},
		{"IcmpInErrorsPerS", out.GetIcmpInErrorsPerS(), 31.5},
		{"IcmpOutErrorsPerS", out.GetIcmpOutErrorsPerS(), 32.5},
		{"IcmpInDestUnreachsPerS", out.GetIcmpInDestUnreachsPerS(), 33.5},
		{"IcmpOutDestUnreachsPerS", out.GetIcmpOutDestUnreachsPerS(), 34.5},
		{"IcmpInTimeExcdsPerS", out.GetIcmpInTimeExcdsPerS(), 35.5},
		{"IcmpOutTimeExcdsPerS", out.GetIcmpOutTimeExcdsPerS(), 36.5},
		{"IcmpInParmProbsPerS", out.GetIcmpInParmProbsPerS(), 37.5},
		{"IcmpOutParmProbsPerS", out.GetIcmpOutParmProbsPerS(), 38.5},
		{"IcmpInRedirectsPerS", out.GetIcmpInRedirectsPerS(), 39.5},
		{"IcmpOutRedirectsPerS", out.GetIcmpOutRedirectsPerS(), 40.5},
		{"IcmpInEchosPerS", out.GetIcmpInEchosPerS(), 41.5},
		{"IcmpOutEchosPerS", out.GetIcmpOutEchosPerS(), 42.5},
		{"IcmpInEchoRepsPerS", out.GetIcmpInEchoRepsPerS(), 43.5},
		{"IcmpOutEchoRepsPerS", out.GetIcmpOutEchoRepsPerS(), 44.5},
		{"Icmp6InMsgsPerS", out.GetIcmp6InMsgsPerS(), 45.5},
		{"Icmp6OutMsgsPerS", out.GetIcmp6OutMsgsPerS(), 46.5},
		{"Icmp6InErrorsPerS", out.GetIcmp6InErrorsPerS(), 47.5},
		{"Icmp6OutErrorsPerS", out.GetIcmp6OutErrorsPerS(), 48.5},
		{"Icmp6InDestUnreachsPerS", out.GetIcmp6InDestUnreachsPerS(), 49.5},
		{"Icmp6OutDestUnreachsPerS", out.GetIcmp6OutDestUnreachsPerS(), 50.5},
		{"Icmp6InTimeExcdsPerS", out.GetIcmp6InTimeExcdsPerS(), 51.5},
		{"Icmp6OutTimeExcdsPerS", out.GetIcmp6OutTimeExcdsPerS(), 52.5},
		{"Icmp6InParmProblemsPerS", out.GetIcmp6InParmProblemsPerS(), 53.5},
		{"Icmp6OutParmProblemsPerS", out.GetIcmp6OutParmProblemsPerS(), 54.5},
		{"Icmp6InPktTooBigsPerS", out.GetIcmp6InPktTooBigsPerS(), 55.5},
		{"Icmp6OutPktTooBigsPerS", out.GetIcmp6OutPktTooBigsPerS(), 56.5},
		{"Icmp6InRedirectsPerS", out.GetIcmp6InRedirectsPerS(), 57.5},
		{"Icmp6OutRedirectsPerS", out.GetIcmp6OutRedirectsPerS(), 58.5},
		{"Icmp6InEchosPerS", out.GetIcmp6InEchosPerS(), 59.5},
		{"Icmp6OutEchosPerS", out.GetIcmp6OutEchosPerS(), 60.5},
		{"Icmp6InEchoRepliesPerS", out.GetIcmp6InEchoRepliesPerS(), 61.5},
		{"Icmp6OutEchoRepliesPerS", out.GetIcmp6OutEchoRepliesPerS(), 62.5},
		{"Icmp6InNeighborSolicitsPerS", out.GetIcmp6InNeighborSolicitsPerS(), 63.5},
		{"Icmp6OutNeighborSolicitsPerS", out.GetIcmp6OutNeighborSolicitsPerS(), 64.5},
		{"Icmp6InNeighborAdvertisementsPerS", out.GetIcmp6InNeighborAdvertisementsPerS(), 65.5},
		{"Icmp6OutNeighborAdvertisementsPerS", out.GetIcmp6OutNeighborAdvertisementsPerS(), 66.5},
		{"Icmp6InRouterSolicitsPerS", out.GetIcmp6InRouterSolicitsPerS(), 67.5},
		{"Icmp6OutRouterSolicitsPerS", out.GetIcmp6OutRouterSolicitsPerS(), 68.5},
		{"Icmp6InRouterAdvertisementsPerS", out.GetIcmp6InRouterAdvertisementsPerS(), 69.5},
		{"Icmp6OutRouterAdvertisementsPerS", out.GetIcmp6OutRouterAdvertisementsPerS(), 70.5},
	}
	if len(cases) != 70 {
		t.Fatalf("assertions cover %d fields, want all 70", len(cases))
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}
