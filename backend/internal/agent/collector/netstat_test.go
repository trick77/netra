package collector_test

import (
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

func newNetstat(t *testing.T, root string, clock func() time.Time) *collector.Netstat {
	t.Helper()
	n := collector.NewNetstat(root)
	if clock != nil {
		n.SetClockForTest(clock)
	}
	return n
}

// collectTwice runs the collector against two fixture trees 60s apart and
// returns the second sample -- the shape almost every rate assertion needs.
func collectTwice(t *testing.T, first, second string) *netrav1.HostSample {
	t.Helper()

	n := newNetstat(t, first, fixedClock(time.Unix(1000, 0), time.Minute))

	var warmup netrav1.HostSample
	if err := collectInto(n, &warmup); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	n.SetProcRootForTest(second)

	var sample netrav1.HostSample
	if err := collectInto(n, &sample); err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	return &sample
}

// CurrEstab is the one gauge in these files, so it is the one value that must
// appear without a baseline.
func TestNetstatFirstCollectEmitsOnlyCurrEstab(t *testing.T) {
	n := newNetstat(t, "testdata/proc1", fixedClock(time.Unix(1000, 0), time.Minute))

	var sample netrav1.HostSample
	if err := collectInto(n, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.TcpCurrEstab == nil || *sample.TcpCurrEstab != 87 {
		t.Errorf("TcpCurrEstab = %v, want 87", sample.TcpCurrEstab)
	}

	for name, got := range map[string]*float64{
		"TcpRetransSegsPerS": sample.TcpRetransSegsPerS,
		"UdpInErrorsPerS":    sample.UdpInErrorsPerS,
		"IpFragFailsPerS":    sample.IpFragFailsPerS,
		"IpInReceivesPerS":   sample.IpInReceivesPerS,
		"IcmpInMsgsPerS":     sample.IcmpInMsgsPerS,
		"Icmp6InMsgsPerS":    sample.Icmp6InMsgsPerS,
		"Udp6InErrorsPerS":   sample.Udp6InErrorsPerS,
		"TcpListenDropsPerS": sample.TcpListenDropsPerS,
	} {
		if got != nil {
			t.Errorf("%s = %v, want nil on the first scrape", name, *got)
		}
	}
}

// One assertion per family, so a parser regression in any of the three file
// formats is attributable rather than just "netstat broke".
func TestNetstatSecondCollectComputesRatesForAllFamilies(t *testing.T) {
	sample := collectTwice(t, "testdata/proc1", "testdata/proc2")

	// Every delta below is over exactly 60 seconds.
	for _, tc := range []struct {
		name string
		got  *float64
		want float64
	}{
		// Tcp: from /proc/net/snmp paired lines.
		{"TcpRetransSegsPerS", sample.TcpRetransSegsPerS, (1440 - 1200) / 60.0},
		{"TcpOutRstsPerS", sample.TcpOutRstsPerS, (310 - 250) / 60.0},
		{"TcpInErrsPerS", sample.TcpInErrsPerS, (9 - 3) / 60.0},
		{"TcpActiveOpensPerS", sample.TcpActiveOpensPerS, (5600 - 5000) / 60.0},
		{"TcpPassiveOpensPerS", sample.TcpPassiveOpensPerS, (3180 - 3000) / 60.0},
		{"TcpAttemptFailsPerS", sample.TcpAttemptFailsPerS, (46 - 40) / 60.0},

		// TcpExt: from /proc/net/netstat, a separate file in the same format.
		{"TcpListenOverflowsPerS", sample.TcpListenOverflowsPerS, (60 - 30) / 60.0},
		{"TcpListenDropsPerS", sample.TcpListenDropsPerS, (105 - 45) / 60.0},

		// Udp: and Ip: from /proc/net/snmp.
		{"UdpInErrorsPerS", sample.UdpInErrorsPerS, (25 - 7) / 60.0},
		{"UdpRcvbufErrorsPerS", sample.UdpRcvbufErrorsPerS, (10 - 4) / 60.0},
		{"UdpSndbufErrorsPerS", sample.UdpSndbufErrorsPerS, (5 - 2) / 60.0},
		{"UdpNoPortsPerS", sample.UdpNoPortsPerS, (120 - 60) / 60.0},
		{"IpReasmReqdsPerS", sample.IpReasmReqdsPerS, (460 - 400) / 60.0},
		{"IpReasmFailsPerS", sample.IpReasmFailsPerS, (26 - 20) / 60.0},
		{"IpFragFailsPerS", sample.IpFragFailsPerS, (14 - 5) / 60.0},
		{"IpFragCreatesPerS", sample.IpFragCreatesPerS, (316 - 250) / 60.0},

		// Udp6/Ip6 from /proc/net/snmp6, the flat key/value format.
		{"Udp6InErrorsPerS", sample.Udp6InErrorsPerS, (11 - 5) / 60.0},
		{"Udp6RcvbufErrorsPerS", sample.Udp6RcvbufErrorsPerS, (8 - 2) / 60.0},
		{"Udp6SndbufErrorsPerS", sample.Udp6SndbufErrorsPerS, (4 - 1) / 60.0},
		{"Udp6NoPortsPerS", sample.Udp6NoPortsPerS, (26 - 14) / 60.0},
		{"Ip6ReasmReqdsPerS", sample.Ip6ReasmReqdsPerS, (150 - 120) / 60.0},
		{"Ip6ReasmFailsPerS", sample.Ip6ReasmFailsPerS, (16 - 10) / 60.0},
		{"Ip6FragFailsPerS", sample.Ip6FragFailsPerS, (9 - 3) / 60.0},
		{"Ip6FragCreatesPerS", sample.Ip6FragCreatesPerS, (120 - 90) / 60.0},

		// The rest of Ip:, which the IP statistics panel draws.
		{"IpInReceivesPerS", sample.IpInReceivesPerS, (1090000 - 1000000) / 60.0},
		{"IpInDeliversPerS", sample.IpInDeliversPerS, (1071000 - 999000) / 60.0},
		{"IpOutRequestsPerS", sample.IpOutRequestsPerS, (936000 - 888000) / 60.0},
		{"IpForwDatagramsPerS", sample.IpForwDatagramsPerS, (2120 - 2000) / 60.0},
		{"IpReasmOksPerS", sample.IpReasmOksPerS, (434 - 380) / 60.0},
		{"IpFragOksPerS", sample.IpFragOksPerS, (130 - 100) / 60.0},
		{"IpInHdrErrorsPerS", sample.IpInHdrErrorsPerS, (28 - 10) / 60.0},
		{"IpInAddrErrorsPerS", sample.IpInAddrErrorsPerS, (29 - 5) / 60.0},
		{"IpInUnknownProtosPerS", sample.IpInUnknownProtosPerS, (6 - 3) / 60.0},
		{"IpInDiscardsPerS", sample.IpInDiscardsPerS, (19 - 7) / 60.0},
		{"IpOutDiscardsPerS", sample.IpOutDiscardsPerS, (53 - 11) / 60.0},
		{"IpOutNoRoutesPerS", sample.IpOutNoRoutesPerS, (38 - 2) / 60.0},
		{"IpReasmTimeoutPerS", sample.IpReasmTimeoutPerS, (52 - 4) / 60.0},

		// The Ip6* half of the same. Every delta is distinct so a
		// transposed pair cannot pass.
		{"Ip6InReceivesPerS", sample.Ip6InReceivesPerS, (212000 - 200000) / 60.0},
		{"Ip6InDeliversPerS", sample.Ip6InDeliversPerS, (211000 - 199000) / 60.0},
		{"Ip6OutRequestsPerS", sample.Ip6OutRequestsPerS, (192000 - 180000) / 60.0},
		{"Ip6ReasmOksPerS", sample.Ip6ReasmOksPerS, (134 - 110) / 60.0},
		{"Ip6FragOksPerS", sample.Ip6FragOksPerS, (52 - 40) / 60.0},

		// Icmp:, error and informational alike. IPv4 only -- the kernel
		// keeps a separate Icmp6 block rather than folding v6 in here.
		{"IcmpInMsgsPerS", sample.IcmpInMsgsPerS, (280 - 100) / 60.0},
		{"IcmpOutMsgsPerS", sample.IcmpOutMsgsPerS, (268 - 100) / 60.0},
		{"IcmpInErrorsPerS", sample.IcmpInErrorsPerS, (25 - 4) / 60.0},
		{"IcmpOutErrorsPerS", sample.IcmpOutErrorsPerS, (60 - 3) / 60.0},
		{"IcmpInDestUnreachsPerS", sample.IcmpInDestUnreachsPerS, (83 - 50) / 60.0},
		{"IcmpOutDestUnreachsPerS", sample.IcmpOutDestUnreachsPerS, (95 - 50) / 60.0},
		{"IcmpInTimeExcdsPerS", sample.IcmpInTimeExcdsPerS, (45 - 6) / 60.0},
		{"IcmpOutTimeExcdsPerS", sample.IcmpOutTimeExcdsPerS, (68 - 5) / 60.0},
		{"IcmpInParmProbsPerS", sample.IcmpInParmProbsPerS, (17 - 2) / 60.0},
		{"IcmpOutParmProbsPerS", sample.IcmpOutParmProbsPerS, (52 - 1) / 60.0},
		{"IcmpInRedirectsPerS", sample.IcmpInRedirectsPerS, (35 - 8) / 60.0},
		{"IcmpOutRedirectsPerS", sample.IcmpOutRedirectsPerS, (76 - 7) / 60.0},
		{"IcmpInEchosPerS", sample.IcmpInEchosPerS, (200 - 50) / 60.0},
		{"IcmpOutEchosPerS", sample.IcmpOutEchosPerS, (123 - 9) / 60.0},
		{"IcmpInEchoRepsPerS", sample.IcmpInEchoRepsPerS, (114 - 12) / 60.0},
		{"IcmpOutEchoRepsPerS", sample.IcmpOutEchoRepsPerS, (188 - 50) / 60.0},

		// Icmp6*, including the neighbour-discovery counters that are
		// IPv6's answer to ARP.
		{"Icmp6InMsgsPerS", sample.Icmp6InMsgsPerS, (4300 - 4000) / 60.0},
		{"Icmp6InErrorsPerS", sample.Icmp6InErrorsPerS, (346 - 40) / 60.0},
		{"Icmp6OutMsgsPerS", sample.Icmp6OutMsgsPerS, (4112 - 3800) / 60.0},
		{"Icmp6OutErrorsPerS", sample.Icmp6OutErrorsPerS, (348 - 30) / 60.0},
		{"Icmp6InDestUnreachsPerS", sample.Icmp6InDestUnreachsPerS, (384 - 60) / 60.0},
		{"Icmp6InPktTooBigsPerS", sample.Icmp6InPktTooBigsPerS, (342 - 12) / 60.0},
		{"Icmp6InTimeExcdsPerS", sample.Icmp6InTimeExcdsPerS, (345 - 9) / 60.0},
		{"Icmp6InParmProblemsPerS", sample.Icmp6InParmProblemsPerS, (347 - 5) / 60.0},
		{"Icmp6InEchosPerS", sample.Icmp6InEchosPerS, (1048 - 700) / 60.0},
		{"Icmp6InEchoRepliesPerS", sample.Icmp6InEchoRepliesPerS, (1004 - 650) / 60.0},
		{"Icmp6InRouterSolicitsPerS", sample.Icmp6InRouterSolicitsPerS, (378 - 18) / 60.0},
		{"Icmp6InRouterAdvertisementsPerS", sample.Icmp6InRouterAdvertisementsPerS, (456 - 90) / 60.0},
		{"Icmp6InNeighborSolicitsPerS", sample.Icmp6InNeighborSolicitsPerS, (792 - 420) / 60.0},
		{"Icmp6InNeighborAdvertisementsPerS", sample.Icmp6InNeighborAdvertisementsPerS, (758 - 380) / 60.0},
		{"Icmp6InRedirectsPerS", sample.Icmp6InRedirectsPerS, (391 - 7) / 60.0},
		{"Icmp6OutDestUnreachsPerS", sample.Icmp6OutDestUnreachsPerS, (445 - 55) / 60.0},
		{"Icmp6OutPktTooBigsPerS", sample.Icmp6OutPktTooBigsPerS, (406 - 10) / 60.0},
		{"Icmp6OutTimeExcdsPerS", sample.Icmp6OutTimeExcdsPerS, (410 - 8) / 60.0},
		{"Icmp6OutParmProblemsPerS", sample.Icmp6OutParmProblemsPerS, (412 - 4) / 60.0},
		{"Icmp6OutEchosPerS", sample.Icmp6OutEchosPerS, (1054 - 640) / 60.0},
		{"Icmp6OutEchoRepliesPerS", sample.Icmp6OutEchoRepliesPerS, (1120 - 700) / 60.0},
		{"Icmp6OutRouterSolicitsPerS", sample.Icmp6OutRouterSolicitsPerS, (442 - 16) / 60.0},
		{"Icmp6OutRouterAdvertisementsPerS", sample.Icmp6OutRouterAdvertisementsPerS, (517 - 85) / 60.0},
		{"Icmp6OutNeighborSolicitsPerS", sample.Icmp6OutNeighborSolicitsPerS, (838 - 400) / 60.0},
		{"Icmp6OutNeighborAdvertisementsPerS", sample.Icmp6OutNeighborAdvertisementsPerS, (804 - 360) / 60.0},
		{"Icmp6OutRedirectsPerS", sample.Icmp6OutRedirectsPerS, (456 - 6) / 60.0},
	} {
		if tc.got == nil {
			t.Errorf("%s = nil, want %v", tc.name, tc.want)
			continue
		}
		if *tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, *tc.got, tc.want)
		}
	}

	if sample.TcpCurrEstab == nil || *sample.TcpCurrEstab != 93 {
		t.Errorf("TcpCurrEstab = %v, want 93", sample.TcpCurrEstab)
	}
}

// Resets are judged per counter, not per file. One counter going backwards
// says nothing about its neighbours, and suppressing the whole scrape over it
// would lose good data for a bad reason.
func TestNetstatCounterResetIsPerKey(t *testing.T) {
	sample := collectTwice(t, "testdata/proc1", "testdata/proc-partialreset")

	if sample.UdpInErrorsPerS != nil {
		t.Errorf("UdpInErrorsPerS = %v, want nil -- that counter went backwards",
			*sample.UdpInErrorsPerS)
	}

	// Every other counter in the same file moved forward normally.
	if sample.UdpRcvbufErrorsPerS == nil {
		t.Error("UdpRcvbufErrorsPerS = nil, want the sibling counter still reported")
	}
	want := (1440 - 1200) / 60.0
	if sample.TcpRetransSegsPerS == nil || *sample.TcpRetransSegsPerS != want {
		t.Errorf("TcpRetransSegsPerS = %v, want %v", sample.TcpRetransSegsPerS, want)
	}
}

// IPv6 disabled in the kernel means no /proc/net/snmp6 at all. That is an
// absent subsystem, and every IPv6 field must be NULL rather than 0 -- "no
// IPv6 fragmentation failures" and "no IPv6" are different facts.
func TestNetstatSnmp6AbsentLeavesAllIPv6FieldsUnset(t *testing.T) {
	sample := collectTwice(t, "testdata/proc-noipv6", "testdata/proc-noipv6")

	for name, got := range map[string]*float64{
		"Udp6InErrorsPerS":            sample.Udp6InErrorsPerS,
		"Udp6RcvbufErrorsPerS":        sample.Udp6RcvbufErrorsPerS,
		"Udp6SndbufErrorsPerS":        sample.Udp6SndbufErrorsPerS,
		"Udp6NoPortsPerS":             sample.Udp6NoPortsPerS,
		"Ip6ReasmReqdsPerS":           sample.Ip6ReasmReqdsPerS,
		"Ip6ReasmFailsPerS":           sample.Ip6ReasmFailsPerS,
		"Ip6FragFailsPerS":            sample.Ip6FragFailsPerS,
		"Ip6FragCreatesPerS":          sample.Ip6FragCreatesPerS,
		"Ip6InReceivesPerS":           sample.Ip6InReceivesPerS,
		"Ip6InTooBigErrorsPerS":       sample.Ip6InTooBigErrorsPerS,
		"Icmp6InMsgsPerS":             sample.Icmp6InMsgsPerS,
		"Icmp6InEchosPerS":            sample.Icmp6InEchosPerS,
		"Icmp6InNeighborSolicitsPerS": sample.Icmp6InNeighborSolicitsPerS,
	} {
		if got != nil {
			t.Errorf("%s = %v, want nil when /proc/net/snmp6 is absent", name, *got)
		}
	}

	// The IPv4 side of the same scrape is unaffected.
	if sample.TcpCurrEstab == nil {
		t.Error("TcpCurrEstab = nil, want IPv4 still collected without snmp6")
	}
}

// /proc/net/netstat is absent in some minimal container /proc mounts. Only
// the TcpExt-derived fields should disappear with it.
func TestNetstatNetstatFileAbsentLeavesListenFieldsUnset(t *testing.T) {
	sample := collectTwice(t, "testdata/proc-noipv6", "testdata/proc-noipv6")

	if sample.TcpListenOverflowsPerS != nil {
		t.Errorf("TcpListenOverflowsPerS = %v, want nil", *sample.TcpListenOverflowsPerS)
	}
	if sample.TcpListenDropsPerS != nil {
		t.Errorf("TcpListenDropsPerS = %v, want nil", *sample.TcpListenDropsPerS)
	}
}

// Some kernels omit an individual counter while still publishing the file it
// lives in. That is a narrower case than a missing file and has its own way
// of going wrong: treating the absent key as 0 would report a large negative
// or positive rate on the scrape either side of it.
func TestNetstatIndividuallyMissingKeyLeavesOnlyThatFieldUnset(t *testing.T) {
	dir := t.TempDir()

	// Ip6ReasmFails is deliberately absent; Udp6InErrors is present in both.
	writeFile(t, dir+"/net/snmp6", "Udp6InErrors 5\nIp6FragCreates 90\n")
	n := newNetstat(t, dir, fixedClock(time.Unix(1000, 0), time.Minute))

	var warmup netrav1.HostSample
	if err := collectInto(n, &warmup); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	writeFile(t, dir+"/net/snmp6", "Udp6InErrors 11\nIp6FragCreates 120\n")

	var sample netrav1.HostSample
	if err := collectInto(n, &sample); err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	if sample.Ip6ReasmFailsPerS != nil {
		t.Errorf("Ip6ReasmFailsPerS = %v, want nil for a key the kernel never published",
			*sample.Ip6ReasmFailsPerS)
	}
	if sample.Udp6InErrorsPerS == nil || *sample.Udp6InErrorsPerS != (11-5)/60.0 {
		t.Errorf("Udp6InErrorsPerS = %v, want %v", sample.Udp6InErrorsPerS, (11-5)/60.0)
	}
}

// A header line and a value line of different lengths cannot be zipped
// safely. Aligning them by guesswork would attribute one counter's value to
// another, which is worse than reporting nothing.
func TestNetstatMismatchedHeaderAndValueLineIsSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/net/snmp",
		"Udp: InDatagrams NoPorts InErrors\nUdp: 100 5\n")

	n := newNetstat(t, dir, fixedClock(time.Unix(1000, 0), time.Minute))

	var warmup netrav1.HostSample
	if err := collectInto(n, &warmup); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	var sample netrav1.HostSample
	if err := collectInto(n, &sample); err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	if sample.UdpInErrorsPerS != nil {
		t.Errorf("UdpInErrorsPerS = %v, want nil for a malformed pair", *sample.UdpInErrorsPerS)
	}
}

// The kernel prints MaxConn as -1 by convention. ParseUint rejects it, and
// that must not abort the rest of the Tcp: line.
func TestNetstatSignedCounterDoesNotAbortTheLine(t *testing.T) {
	sample := collectTwice(t, "testdata/proc1", "testdata/proc2")

	if sample.TcpRetransSegsPerS == nil {
		t.Error("TcpRetransSegsPerS = nil, want the fields after MaxConn still parsed")
	}
}

func TestNetstatName(t *testing.T) {
	n := collector.NewNetstat("testdata/proc1")

	if got := n.Name(); got != "netstat" {
		t.Errorf("Name() = %q, want %q", got, "netstat")
	}
}

// Every one of these files can legitimately be missing. None of them is worth
// failing a scrape over.
func TestNetstatAllFilesAbsentIsNotAnError(t *testing.T) {
	n := collector.NewNetstat(t.TempDir())

	var sample netrav1.HostSample
	if err := collectInto(n, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if sample.TcpCurrEstab != nil {
		t.Error("TcpCurrEstab set, want nil with no files present")
	}
}
