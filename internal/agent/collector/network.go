package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// netCounters is one interface's line of /proc/net/dev, keeping only the
// fields netra reports. All four are monotonic since boot.
type netCounters struct {
	rxBytes uint64
	rxErrs  uint64
	txBytes uint64
	txErrs  uint64
}

// Network reports per-interface traffic rates from /proc/net/dev.
//
// Requires network_mode: host to see the host's interfaces at all; inside
// Docker's own namespace the agent sees only the container's.
//
// Rates rather than counters, for the same reason as DiskIO: the counters
// reset on reboot and only the agent holds the previous reading needed to
// notice. An interface whose counters went backwards emits no row.
type Network struct {
	procRoot string

	now func() time.Time

	prev   map[string]netCounters
	prevAt time.Time
}

// NewNetwork builds a Network collector reading from procRoot (normally
// "/proc").
func NewNetwork(procRoot string) *Network {
	return &Network{procRoot: procRoot, now: time.Now}
}

// Name implements Collector.
func (n *Network) Name() string { return "network" }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (n *Network) SetProcRootForTest(root string) { n.procRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (n *Network) SetClockForTest(fn func() time.Time) { n.now = fn }

// Collect implements Collector.
func (n *Network) Collect(_ context.Context) (*Result, error) {
	cur, err := n.read()
	if err != nil {
		return nil, err
	}

	prev, prevAt := n.prev, n.prevAt
	at := n.now()
	n.prev, n.prevAt = cur, at

	if prev == nil {
		return &Result{}, nil
	}

	elapsed := at.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return &Result{}, nil
	}

	names := make([]string, 0, len(cur))
	for name := range cur {
		names = append(names, name)
	}
	slices.Sort(names)

	ts := at.UnixMilli()
	rows := make([]*netrav1.NetSample, 0, len(names))

	for _, name := range names {
		c := cur[name]
		p, ok := prev[name]
		if !ok {
			// An interface that appeared this scrape has no interval to
			// average over -- a container starting brings one up routinely.
			continue
		}
		if c.rxBytes < p.rxBytes || c.txBytes < p.txBytes ||
			c.rxErrs < p.rxErrs || c.txErrs < p.txErrs {
			// Reboot, or the interface was recreated. No row rather than a
			// negative rate, and no zero, which would read as a genuinely
			// idle link.
			continue
		}

		rows = append(rows, &netrav1.NetSample{
			TsMs:    ts,
			Iface:   name,
			RxBytes: ptrTo(float64(c.rxBytes-p.rxBytes) / elapsed),
			TxBytes: ptrTo(float64(c.txBytes-p.txBytes) / elapsed),
			RxErrs:  ptrTo(float64(c.rxErrs-p.rxErrs) / elapsed),
			TxErrs:  ptrTo(float64(c.txErrs-p.txErrs) / elapsed),
		})
	}

	return &Result{Nets: rows}, nil
}

// reportableIface reports whether an interface carries traffic worth counting
// as the host's.
//
// lo is not network traffic at all. veth*, br-* and docker0 are the host side
// of container networking, so their bytes are the SAME bytes already counted
// on the real interface -- including them double-counts the host, and on a
// host with forty containers adds forty series measuring nothing new. Tunnel
// devices are excluded for the same double-counting reason: their payload
// traverses a physical interface too.
func reportableIface(name string) bool {
	switch name {
	case "lo", "docker0":
		return false
	}
	for _, prefix := range []string{
		"veth", "br-", "virbr", "docker",
		// Tunnels: the encapsulated bytes also cross a real interface.
		"tun", "tap", "wg", "gre", "sit", "ip6tnl", "erspan",
		// Kubernetes and other CNI plumbing.
		"cni", "flannel", "cali", "cilium", "kube-ipvs",
	} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

// read parses /proc/net/dev into per-interface counters.
func (n *Network) read() (map[string]netCounters, error) {
	path := filepath.Join(n.procRoot, "net", "dev")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out := make(map[string]netCounters)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// The two header lines have no colon; every data line is
		// "  iface: rx_bytes rx_packets ...", with the name sometimes flush
		// against the colon on a long interface name.
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		if !reportableIface(name) {
			continue
		}

		fields := strings.Fields(line[colon+1:])
		// 8 receive columns then 8 transmit columns.
		if len(fields) < 16 {
			continue
		}

		values := make([]uint64, 0, 16)
		for _, raw := range fields[:16] {
			v, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse %s %s: %w", path, name, err)
			}
			values = append(values, v)
		}

		out[name] = netCounters{
			rxBytes: values[0],
			rxErrs:  values[2],
			txBytes: values[8],
			txErrs:  values[10],
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return out, nil
}
