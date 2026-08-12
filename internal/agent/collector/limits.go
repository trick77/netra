package collector

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Limits reports how close the host is to running out of the things it
// silently stops working without: sockets, file descriptors, and conntrack
// entries.
//
// Every field here is a gauge with a ceiling beside it, which is the whole
// point. netra already reports TCP behaviour in depth -- retransmits, resets,
// listen overflows -- but none of it answers "am I about to hit a limit".
// Exhaustion does not look like a resource problem from the outside: accept()
// starts failing, conntrack drops new flows, and the symptom presents as the
// network being broken.
//
// Nothing here is fatal to read. A host without conntrack loaded, or a
// container without /proc/net/sockstat, leaves those fields unset rather than
// failing the scrape -- every one of these files is independently optional.
//
// Which is exactly why it reports capabilities. Collect cannot fail, so
// collector health would read "ok" whether every file was read or none were,
// and a NULL conntrack_count would be indistinguishable between "the module
// is not loaded, nothing to worry about" and "this collector read nothing".
// That ambiguity is the stated rationale for CapabilityReporter.
type Limits struct {
	procRoot string

	mu           sync.Mutex
	capabilities map[string]string
}

// Capability keys reported by Limits, one per independent source.
const (
	capSockstat  = "sockets"
	capFileNr    = "file_descriptors"
	capConntrack = "conntrack"
)

// NewLimits builds a Limits collector reading from procRoot.
func NewLimits(procRoot string) *Limits {
	return &Limits{procRoot: procRoot}
}

// Name implements Collector.
func (l *Limits) Name() string { return "limits" }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (l *Limits) SetProcRootForTest(root string) { l.procRoot = root }

// Capabilities implements CapabilityReporter.
func (l *Limits) Capabilities() map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make(map[string]string, len(l.capabilities))
	for k, v := range l.capabilities {
		out[k] = v
	}
	return out
}

// setCapability records one source as readable or not, every scrape, so a
// module loaded or a mount added later clears the report rather than latching.
func (l *Limits) setCapability(key string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.capabilities == nil {
		l.capabilities = make(map[string]string, 3)
	}
	if ok {
		delete(l.capabilities, key)
		return
	}
	l.capabilities[key] = "unavailable"
}

// Collect implements Collector.
func (l *Limits) Collect(_ context.Context) (*Result, error) {
	sample := &netrav1.HostSample{}

	l.setCapability(capSockstat, l.readSockstat(sample))
	l.setCapability(capFileNr, l.readFileNr(sample))
	l.readTCPLimits(sample)
	l.setCapability(capConntrack, l.readConntrack(sample))

	return &Result{Host: sample}, nil
}

// readSockstat parses /proc/net/sockstat, whose lines look like
// "TCP: inuse 12 orphan 0 tw 3 alloc 15 mem 2".
func (l *Limits) readSockstat(sample *netrav1.HostSample) bool {
	f, err := os.Open(filepath.Join(l.procRoot, "net", "sockstat"))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		head, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)

		switch head {
		case "sockets":
			// "sockets: used 231" -- every socket on the host, not just TCP.
			if v, ok := keyedU32(fields, "used"); ok {
				sample.SocketsUsed = &v
			}
		case "TCP":
			if v, ok := keyedU32(fields, "orphan"); ok {
				sample.TcpOrphan = &v
			}
			// Sockets in TIME_WAIT. A host that churns short-lived
			// connections accumulates these against tcp_max_tw_buckets, and
			// hitting that ceiling is invisible in every other TCP counter.
			if v, ok := keyedU32(fields, "tw"); ok {
				sample.TcpTw = &v
			}
			if v, ok := keyedU32(fields, "alloc"); ok {
				sample.TcpAlloc = &v
			}
		}
	}
	return true
}

// readFileNr parses /proc/sys/fs/file-nr: "allocated free max", where the
// second field has been 0 since 2.6 and is ignored.
func (l *Limits) readFileNr(sample *netrav1.HostSample) bool {
	raw, err := os.ReadFile(filepath.Join(l.procRoot, "sys", "fs", "file-nr"))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return false
	}
	if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
		sample.FdUsed = &v
	}
	if v, err := strconv.ParseUint(fields[2], 10, 64); err == nil {
		sample.FdLimit = &v
	}
	return true
}

// readTCPLimits reads the ceilings the socket gauges are read against.
//
// Without them four of the six gauges here ship as bare numbers with nothing
// to compare against, while this file's own contract is that every gauge has
// a ceiling beside it. tcp_tw pinned at tcp_max_tw_buckets means the kernel
// is silently dropping TIME_WAIT sockets; tcp_orphan at tcp_max_orphans means
// it is resetting connections. Neither appears in any other TCP counter, and
// neither is answerable from the gauge alone -- which is exactly what the
// comment above tcp_tw claimed while never reading the limit.
//
// No capability of its own: these are sysctls that exist wherever
// /proc/net/sockstat does, so their absence is already covered by that
// source's report.
func (l *Limits) readTCPLimits(sample *netrav1.HostSample) {
	if v, ok := l.readU32(filepath.Join("sys", "net", "ipv4", "tcp_max_tw_buckets")); ok {
		sample.TcpTwLimit = &v
	}
	if v, ok := l.readU32(filepath.Join("sys", "net", "ipv4", "tcp_max_orphans")); ok {
		sample.TcpOrphanLimit = &v
	}
}

// readConntrack reads the netfilter connection tracking count and ceiling.
//
// Both stay unset when the module is not loaded, which is the common case on
// a host that is not doing NAT or filtering -- absent, not zero.
func (l *Limits) readConntrack(sample *netrav1.HostSample) bool {
	count, ok := l.readU32(filepath.Join("sys", "net", "netfilter", "nf_conntrack_count"))
	if ok {
		sample.ConntrackCount = &count
	}
	if v, vok := l.readU32(filepath.Join("sys", "net", "netfilter", "nf_conntrack_max")); vok {
		sample.ConntrackLimit = &v
	}
	return ok
}

func (l *Limits) readU32(rel string) (uint32, bool) {
	raw, err := os.ReadFile(filepath.Join(l.procRoot, rel))
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// keyedU32 finds "key value" within a line's fields.
func keyedU32(fields []string, key string) (uint32, bool) {
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] != key {
			continue
		}
		v, err := strconv.ParseUint(fields[i+1], 10, 32)
		if err != nil {
			return 0, false
		}
		return uint32(v), true
	}
	return 0, false
}
