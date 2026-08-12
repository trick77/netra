package collector

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
type Limits struct {
	procRoot string
}

// NewLimits builds a Limits collector reading from procRoot.
func NewLimits(procRoot string) *Limits {
	return &Limits{procRoot: procRoot}
}

// Name implements Collector.
func (l *Limits) Name() string { return "limits" }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (l *Limits) SetProcRootForTest(root string) { l.procRoot = root }

// Collect implements Collector.
func (l *Limits) Collect(_ context.Context) (*Result, error) {
	sample := &netrav1.HostSample{}

	l.readSockstat(sample)
	l.readFileNr(sample)
	l.readConntrack(sample)

	return &Result{Host: sample}, nil
}

// readSockstat parses /proc/net/sockstat, whose lines look like
// "TCP: inuse 12 orphan 0 tw 3 alloc 15 mem 2".
func (l *Limits) readSockstat(sample *netrav1.HostSample) {
	f, err := os.Open(filepath.Join(l.procRoot, "net", "sockstat"))
	if err != nil {
		return
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
}

// readFileNr parses /proc/sys/fs/file-nr: "allocated free max", where the
// second field has been 0 since 2.6 and is ignored.
func (l *Limits) readFileNr(sample *netrav1.HostSample) {
	raw, err := os.ReadFile(filepath.Join(l.procRoot, "sys", "fs", "file-nr"))
	if err != nil {
		return
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return
	}
	if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
		sample.FdUsed = &v
	}
	if v, err := strconv.ParseUint(fields[2], 10, 64); err == nil {
		sample.FdLimit = &v
	}
}

// readConntrack reads the netfilter connection tracking count and ceiling.
//
// Both stay unset when the module is not loaded, which is the common case on
// a host that is not doing NAT or filtering -- absent, not zero.
func (l *Limits) readConntrack(sample *netrav1.HostSample) {
	if v, ok := l.readU32(filepath.Join("sys", "net", "netfilter", "nf_conntrack_count")); ok {
		sample.ConntrackCount = &v
	}
	if v, ok := l.readU32(filepath.Join("sys", "net", "netfilter", "nf_conntrack_max")); ok {
		sample.ConntrackLimit = &v
	}
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
