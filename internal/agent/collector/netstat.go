package collector

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// Netstat reports IP, TCP and UDP protocol statistics from /proc/net/snmp,
// /proc/net/netstat and /proc/net/snmp6.
//
// Every value in those files except CurrEstab is a counter that resets on
// reboot, so they are reported as per-second rates computed the same way as
// KernelStat's: a counter that goes backwards yields no value for that key
// alone, and a scrape with no baseline yields no rates at all.
//
// There is no tcp6_* mirror of the TCP fields, and that is the consistent
// treatment rather than an omission: /proc/net/snmp's Tcp: block is the RFC
// 1213 TCP MIB, which Linux maintains as a single family-agnostic counter set
// already covering IPv6 connections, and /proc/net/snmp6 has no Tcp6 block at
// all. UDP, IP and ICMP are accounted per family by the kernel, and all three
// are mirrored here -- ICMP genuinely so: Icmp: counts ICMPv4 alone and snmp6
// carries a full Icmp6* block beside it.
//
// IcmpMsg: is deliberately NOT parsed. Its column names are per-ICMP-type and
// depend on which types the host has actually seen -- one machine publishes
// "InType3 InType8 OutType0", another a different set -- so no fixed field
// list can be derived from it, and readPaired's zip-by-position would
// attribute one host's InType3 to another's InType8. The named Icmp: counters
// cover the types worth charting anyway: DestUnreachs is type 3, Echos is
// type 8.
type Netstat struct {
	procRoot string

	now func() time.Time

	// prev holds the previous reading keyed by "Family.Key", e.g.
	// "Tcp.RetransSegs" or "Ip6.Ip6ReasmFails".
	prev   map[string]uint64
	prevAt time.Time
}

// NewNetstat builds a Netstat collector reading from procRoot.
func NewNetstat(procRoot string) *Netstat {
	return &Netstat{procRoot: procRoot, now: time.Now}
}

// Name implements Collector.
func (n *Netstat) Name() string { return "netstat" }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (n *Netstat) SetProcRootForTest(root string) { n.procRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (n *Netstat) SetClockForTest(fn func() time.Time) { n.now = fn }

// Collect implements Collector.
func (n *Netstat) Collect(_ context.Context) (*Result, error) {
	sample := &netrav1.HostSample{}

	cur := make(map[string]uint64, 64)

	// A missing file is an absent subsystem, not a failure: /proc/net/snmp6
	// does not exist when IPv6 is disabled in the kernel, and a container
	// without network_mode: host may see a reduced set. Every key it would
	// have provided simply stays unset.
	for _, name := range []string{"snmp", "netstat"} {
		if err := n.readPaired(filepath.Join(n.procRoot, "net", name), cur); err != nil {
			return nil, err
		}
	}
	if err := n.readFlat(filepath.Join(n.procRoot, "net", "snmp6"), cur); err != nil {
		return nil, err
	}

	// CurrEstab is a gauge, so it is reported on the first scrape too.
	if v, ok := cur["Tcp.CurrEstab"]; ok {
		e := uint32(v)
		sample.TcpCurrEstab = &e
	}

	at := n.now()
	prev, prevAt := n.prev, n.prevAt
	n.prev, n.prevAt = cur, at

	if prev == nil {
		return &Result{Host: sample}, nil
	}
	elapsed := at.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return &Result{Host: sample}, nil
	}

	// r resolves one key in both readings. A key missing from either -- a
	// kernel that does not publish it, or a file that appeared or vanished
	// between scrapes -- yields nil, never a value derived from an implied
	// zero.
	r := func(key string) *float64 {
		c, okCur := cur[key]
		p, okPrev := prev[key]
		if !okCur || !okPrev {
			return nil
		}
		return rate(&c, &p, elapsed)
	}

	sample.TcpRetransSegsPerS = r("Tcp.RetransSegs")
	sample.TcpOutRstsPerS = r("Tcp.OutRsts")
	sample.TcpInErrsPerS = r("Tcp.InErrs")
	sample.TcpActiveOpensPerS = r("Tcp.ActiveOpens")
	sample.TcpPassiveOpensPerS = r("Tcp.PassiveOpens")
	sample.TcpAttemptFailsPerS = r("Tcp.AttemptFails")

	sample.TcpListenOverflowsPerS = r("TcpExt.ListenOverflows")
	sample.TcpListenDropsPerS = r("TcpExt.ListenDrops")

	sample.UdpInErrorsPerS = r("Udp.InErrors")
	sample.UdpRcvbufErrorsPerS = r("Udp.RcvbufErrors")
	sample.UdpSndbufErrorsPerS = r("Udp.SndbufErrors")
	sample.UdpNoPortsPerS = r("Udp.NoPorts")

	sample.IpReasmReqdsPerS = r("Ip.ReasmReqds")
	sample.IpReasmFailsPerS = r("Ip.ReasmFails")
	sample.IpFragFailsPerS = r("Ip.FragFails")
	sample.IpFragCreatesPerS = r("Ip.FragCreates")

	sample.Udp6InErrorsPerS = r("Snmp6.Udp6InErrors")
	sample.Udp6RcvbufErrorsPerS = r("Snmp6.Udp6RcvbufErrors")
	sample.Udp6SndbufErrorsPerS = r("Snmp6.Udp6SndbufErrors")
	sample.Udp6NoPortsPerS = r("Snmp6.Udp6NoPorts")

	sample.Ip6ReasmReqdsPerS = r("Snmp6.Ip6ReasmReqds")
	sample.Ip6ReasmFailsPerS = r("Snmp6.Ip6ReasmFails")
	sample.Ip6FragFailsPerS = r("Snmp6.Ip6FragFails")
	sample.Ip6FragCreatesPerS = r("Snmp6.Ip6FragCreates")

	// Ip:, beyond the fragmentation counters above. Everything from here
	// down lands in host_snmp_samples rather than host_samples -- see the
	// comment on the proto fields for why that split is load-bearing.
	sample.IpInReceivesPerS = r("Ip.InReceives")
	sample.IpInDeliversPerS = r("Ip.InDelivers")
	sample.IpOutRequestsPerS = r("Ip.OutRequests")
	sample.IpForwDatagramsPerS = r("Ip.ForwDatagrams")
	sample.IpReasmOksPerS = r("Ip.ReasmOKs")
	sample.IpFragOksPerS = r("Ip.FragOKs")
	sample.IpInHdrErrorsPerS = r("Ip.InHdrErrors")
	sample.IpInAddrErrorsPerS = r("Ip.InAddrErrors")
	sample.IpInUnknownProtosPerS = r("Ip.InUnknownProtos")
	sample.IpInDiscardsPerS = r("Ip.InDiscards")
	sample.IpOutDiscardsPerS = r("Ip.OutDiscards")
	sample.IpOutNoRoutesPerS = r("Ip.OutNoRoutes")
	sample.IpReasmTimeoutPerS = r("Ip.ReasmTimeout")

	// Ip6*. Three have no IPv4 peer, and that is the kernel's asymmetry
	// rather than a gap here: the Ip: block has no InNoRoutes at all, and
	// "packet too big" is an ICMPv6 fact IPv4 records as fragmentation.
	sample.Ip6InReceivesPerS = r("Snmp6.Ip6InReceives")
	sample.Ip6InDeliversPerS = r("Snmp6.Ip6InDelivers")
	sample.Ip6OutRequestsPerS = r("Snmp6.Ip6OutRequests")
	sample.Ip6OutForwDatagramsPerS = r("Snmp6.Ip6OutForwDatagrams")
	sample.Ip6ReasmOksPerS = r("Snmp6.Ip6ReasmOKs")
	sample.Ip6FragOksPerS = r("Snmp6.Ip6FragOKs")
	sample.Ip6InHdrErrorsPerS = r("Snmp6.Ip6InHdrErrors")
	sample.Ip6InAddrErrorsPerS = r("Snmp6.Ip6InAddrErrors")
	sample.Ip6InUnknownProtosPerS = r("Snmp6.Ip6InUnknownProtos")
	sample.Ip6InDiscardsPerS = r("Snmp6.Ip6InDiscards")
	sample.Ip6OutDiscardsPerS = r("Snmp6.Ip6OutDiscards")
	sample.Ip6OutNoRoutesPerS = r("Snmp6.Ip6OutNoRoutes")
	sample.Ip6InNoRoutesPerS = r("Snmp6.Ip6InNoRoutes")
	sample.Ip6InTooBigErrorsPerS = r("Snmp6.Ip6InTooBigErrors")
	sample.Ip6ReasmTimeoutPerS = r("Snmp6.Ip6ReasmTimeout")

	// Icmp:, which counts ICMPv4 alone -- unlike Tcp:, the kernel does not
	// fold v6 into it. The last four are the informational half: echo is
	// reachability, not failure, and shares no axis with the errors above.
	sample.IcmpInMsgsPerS = r("Icmp.InMsgs")
	sample.IcmpOutMsgsPerS = r("Icmp.OutMsgs")
	sample.IcmpInErrorsPerS = r("Icmp.InErrors")
	sample.IcmpOutErrorsPerS = r("Icmp.OutErrors")
	sample.IcmpInDestUnreachsPerS = r("Icmp.InDestUnreachs")
	sample.IcmpOutDestUnreachsPerS = r("Icmp.OutDestUnreachs")
	sample.IcmpInTimeExcdsPerS = r("Icmp.InTimeExcds")
	sample.IcmpOutTimeExcdsPerS = r("Icmp.OutTimeExcds")
	sample.IcmpInParmProbsPerS = r("Icmp.InParmProbs")
	sample.IcmpOutParmProbsPerS = r("Icmp.OutParmProbs")
	sample.IcmpInRedirectsPerS = r("Icmp.InRedirects")
	sample.IcmpOutRedirectsPerS = r("Icmp.OutRedirects")
	sample.IcmpInEchosPerS = r("Icmp.InEchos")
	sample.IcmpOutEchosPerS = r("Icmp.OutEchos")
	sample.IcmpInEchoRepsPerS = r("Icmp.InEchoReps")
	sample.IcmpOutEchoRepsPerS = r("Icmp.OutEchoReps")

	// Icmp6*. The spellings follow the kernel's icmp6type2name[] table, so
	// two differ from their v4 peers: ParmProblems, not ParmProbs, and
	// EchoReplies, not EchoReps. The neighbour-discovery counters at the end
	// are IPv6's informational traffic -- there is no ARP, so a host that
	// stops answering neighbour solicits disappears from its segment.
	sample.Icmp6InMsgsPerS = r("Snmp6.Icmp6InMsgs")
	sample.Icmp6OutMsgsPerS = r("Snmp6.Icmp6OutMsgs")
	sample.Icmp6InErrorsPerS = r("Snmp6.Icmp6InErrors")
	sample.Icmp6OutErrorsPerS = r("Snmp6.Icmp6OutErrors")
	sample.Icmp6InDestUnreachsPerS = r("Snmp6.Icmp6InDestUnreachs")
	sample.Icmp6OutDestUnreachsPerS = r("Snmp6.Icmp6OutDestUnreachs")
	sample.Icmp6InTimeExcdsPerS = r("Snmp6.Icmp6InTimeExcds")
	sample.Icmp6OutTimeExcdsPerS = r("Snmp6.Icmp6OutTimeExcds")
	sample.Icmp6InParmProblemsPerS = r("Snmp6.Icmp6InParmProblems")
	sample.Icmp6OutParmProblemsPerS = r("Snmp6.Icmp6OutParmProblems")
	sample.Icmp6InPktTooBigsPerS = r("Snmp6.Icmp6InPktTooBigs")
	sample.Icmp6OutPktTooBigsPerS = r("Snmp6.Icmp6OutPktTooBigs")
	sample.Icmp6InRedirectsPerS = r("Snmp6.Icmp6InRedirects")
	sample.Icmp6OutRedirectsPerS = r("Snmp6.Icmp6OutRedirects")
	sample.Icmp6InEchosPerS = r("Snmp6.Icmp6InEchos")
	sample.Icmp6OutEchosPerS = r("Snmp6.Icmp6OutEchos")
	sample.Icmp6InEchoRepliesPerS = r("Snmp6.Icmp6InEchoReplies")
	sample.Icmp6OutEchoRepliesPerS = r("Snmp6.Icmp6OutEchoReplies")
	sample.Icmp6InNeighborSolicitsPerS = r("Snmp6.Icmp6InNeighborSolicits")
	sample.Icmp6OutNeighborSolicitsPerS = r("Snmp6.Icmp6OutNeighborSolicits")
	sample.Icmp6InNeighborAdvertisementsPerS = r("Snmp6.Icmp6InNeighborAdvertisements")
	sample.Icmp6OutNeighborAdvertisementsPerS = r("Snmp6.Icmp6OutNeighborAdvertisements")
	sample.Icmp6InRouterSolicitsPerS = r("Snmp6.Icmp6InRouterSolicits")
	sample.Icmp6OutRouterSolicitsPerS = r("Snmp6.Icmp6OutRouterSolicits")
	sample.Icmp6InRouterAdvertisementsPerS = r("Snmp6.Icmp6InRouterAdvertisements")
	sample.Icmp6OutRouterAdvertisementsPerS = r("Snmp6.Icmp6OutRouterAdvertisements")

	return &Result{Host: sample}, nil
}

// readPaired parses the format of /proc/net/snmp and /proc/net/netstat, where
// a header line of names is immediately followed by a line of values under
// the same family prefix:
//
//	Tcp: RtoAlgorithm RtoMin ... RetransSegs InErrs OutRsts
//	Tcp: 1 200 ... 42 0 7
//
// Names and values are zipped by position. A pair whose lengths disagree is
// skipped rather than mis-zipped, because aligning them by guesswork would
// silently attribute one counter's value to a different counter.
func (n *Netstat) readPaired(path string, out map[string]uint64) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	var pendingFamily string
	var pendingNames []string

	scanner := bufio.NewScanner(f)
	// The TcpExt line carries ~90 counters and can exceed bufio's 64 KiB
	// default on a kernel with many extensions.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || !strings.HasSuffix(fields[0], ":") {
			continue
		}
		family := strings.TrimSuffix(fields[0], ":")

		if pendingFamily != family {
			pendingFamily, pendingNames = family, fields[1:]
			continue
		}

		values := fields[1:]
		if len(values) == len(pendingNames) {
			for i, name := range pendingNames {
				v, err := strconv.ParseUint(values[i], 10, 64)
				if err != nil {
					// Some counters are signed in the kernel's own printf
					// (MaxConn is -1 by convention). Skip rather than fail:
					// none of the keys this collector reports is signed.
					continue
				}
				out[family+"."+name] = v
			}
		}
		pendingFamily, pendingNames = "", nil
	}

	return scanner.Err()
}

// readFlat parses /proc/net/snmp6, which is one "Name Value" pair per line
// rather than the paired header/value layout of its IPv4 counterpart. Keys
// are stored under a synthetic "Snmp6." family because the names there are
// already self-prefixed (Udp6InErrors, Ip6ReasmFails).
func (n *Netstat) readFlat(path string, out map[string]uint64) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		out["Snmp6."+fields[0]] = v
	}

	return scanner.Err()
}
