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
// all. Only UDP and IP fragmentation are accounted per family by the kernel,
// and only those are mirrored here.
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
