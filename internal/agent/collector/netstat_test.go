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
		{"IpFragFailsPerS", sample.IpFragFailsPerS, (11 - 5) / 60.0},
		{"IpFragCreatesPerS", sample.IpFragCreatesPerS, (310 - 250) / 60.0},

		// Udp6/Ip6 from /proc/net/snmp6, the flat key/value format.
		{"Udp6InErrorsPerS", sample.Udp6InErrorsPerS, (11 - 5) / 60.0},
		{"Udp6RcvbufErrorsPerS", sample.Udp6RcvbufErrorsPerS, (8 - 2) / 60.0},
		{"Udp6SndbufErrorsPerS", sample.Udp6SndbufErrorsPerS, (4 - 1) / 60.0},
		{"Udp6NoPortsPerS", sample.Udp6NoPortsPerS, (26 - 14) / 60.0},
		{"Ip6ReasmReqdsPerS", sample.Ip6ReasmReqdsPerS, (150 - 120) / 60.0},
		{"Ip6ReasmFailsPerS", sample.Ip6ReasmFailsPerS, (16 - 10) / 60.0},
		{"Ip6FragFailsPerS", sample.Ip6FragFailsPerS, (9 - 3) / 60.0},
		{"Ip6FragCreatesPerS", sample.Ip6FragCreatesPerS, (120 - 90) / 60.0},
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
		"Udp6InErrorsPerS":     sample.Udp6InErrorsPerS,
		"Udp6RcvbufErrorsPerS": sample.Udp6RcvbufErrorsPerS,
		"Udp6SndbufErrorsPerS": sample.Udp6SndbufErrorsPerS,
		"Udp6NoPortsPerS":      sample.Udp6NoPortsPerS,
		"Ip6ReasmReqdsPerS":    sample.Ip6ReasmReqdsPerS,
		"Ip6ReasmFailsPerS":    sample.Ip6ReasmFailsPerS,
		"Ip6FragFailsPerS":     sample.Ip6FragFailsPerS,
		"Ip6FragCreatesPerS":   sample.Ip6FragCreatesPerS,
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
