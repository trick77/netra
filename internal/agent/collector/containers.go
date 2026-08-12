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
	"sync"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// ContainerMeta is what the Docker socket contributes: names and labels, never
// metrics. The metrics come from cgroup v2, which needs no socket -- so a host
// that declines to mount it still gets numbers, just without friendly names.
type ContainerMeta struct {
	ID      string
	Name    string
	Image   string
	Project string // compose project label
	Service string // compose service label
	IsAgent bool
}

// ContainerLister returns the containers currently running.
//
// Injected so the collector is testable without a Docker daemon, and so the
// socket stays an optional enrichment rather than a hard dependency.
type ContainerLister func(ctx context.Context) ([]ContainerMeta, error)

// containerCounters is the cumulative cgroup state of one container.
type containerCounters struct {
	cpuUsec  uint64
	memUsed  uint64
	memLimit uint64
	rbytes   uint64
	wbytes   uint64
	hasLimit bool

	// cpu.stat's split of usage_usec. Cumulative like usage_usec, so these
	// become percentages the same way: a delta over the interval. hasSplit
	// is false on a kernel that does not report them, which is not the same
	// fact as a container that spent no time in user space.
	userUsec   uint64
	systemUsec uint64
	hasSplit   bool

	// memory.stat's own parts. Gauges, not counters -- read straight through.
	memAnon   uint64
	memFile   uint64
	memShmem  uint64
	memKernel uint64

	// rxBytes and txBytes are summed across the container's own network
	// namespace. hasNet is false when there was no namespace to read -- a
	// stopped container, or one sharing the host's -- which is different from
	// a container that genuinely moved no bytes.
	rxBytes uint64
	txBytes uint64
	hasNet  bool
}

// Containers reports per-container CPU, memory and I/O from cgroup v2.
//
// Identity is the compose project + service, falling back to the container
// name (spec §6.2). Deliberately NOT the Docker id: a recreate issues a new id
// for the same service, so keying history on it would restart every series
// whenever an image is bumped.
type Containers struct {
	cgroupRoot string
	procRoot   string
	lister     ContainerLister

	now func() time.Time

	prev   map[string]containerCounters
	prevAt time.Time

	socketAbsent bool

	// hostNetNS identifies the host's network namespace, resolved once.
	// Empty when it could not be read, which makes per-container networking
	// unmeasurable rather than unguarded -- see containerNet.
	hostNetNS     string
	hostNetNSOnce sync.Once

	mu sync.Mutex
	// netCapability explains why per-container networking is absent, or is
	// empty when it is working.
	netCapability string
}

// NewContainers builds a Containers collector. cgroupRoot is the mounted
// cgroup v2 hierarchy, procRoot the mounted /proc that per-container network
// counters are read through; lister may be nil when the Docker socket is not
// available.
func NewContainers(cgroupRoot, procRoot string, lister ContainerLister) *Containers {
	return &Containers{cgroupRoot: cgroupRoot, procRoot: procRoot, lister: lister, now: time.Now}
}

// Name implements Collector.
func (c *Containers) Name() string { return "containers" }

// SetCgroupRootForTest repoints the collector at a different fixture tree.
func (c *Containers) SetCgroupRootForTest(root string) { c.cgroupRoot = root }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (c *Containers) SetProcRootForTest(root string) { c.procRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (c *Containers) SetClockForTest(fn func() time.Time) { c.now = fn }

// Capability values reported by Containers for per-container networking.
const (
	// capNetNamespaced: cgroup.procs names host PIDs, and without pid: host
	// the agent resolves them in its own namespace and finds nothing.
	capNetNamespaced = "namespaced"

	// capNetNoHostNS: /proc/1/ns/net or the container's own is unreadable, so
	// a host-networked container cannot be told from a bridged one.
	capNetNoHostNS = "no-host-netns"
)

// Capabilities implements CapabilityReporter.
func (c *Containers) Capabilities() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out map[string]string
	if c.socketAbsent {
		// Metrics still arrive from cgroup v2; only the names and compose
		// labels are missing. Saying so distinguishes "no containers" from
		// "containers whose identity we cannot resolve".
		out = map[string]string{"containers": "no-docker-socket"}
	}
	if c.netCapability != "" {
		if out == nil {
			out = make(map[string]string, 1)
		}
		// Separate key from "containers": CPU, memory and I/O are fine in
		// every case this reports, and only networking is missing.
		out["container_network"] = c.netCapability
	}
	return out
}

// setCapability records why per-container networking produced nothing. Reset
// each scrape by Collect, so a container that starts reporting again clears
// it rather than leaving the agent looking permanently broken.
func (c *Containers) setCapability(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.netCapability = value
}

// Collect implements Collector.
func (c *Containers) Collect(ctx context.Context) (*Result, error) {
	meta := map[string]ContainerMeta{}
	if c.lister != nil {
		list, err := c.lister(ctx)
		if err != nil {
			c.socketAbsent = true
		} else {
			c.socketAbsent = false
			for _, m := range list {
				meta[m.ID] = m
			}
		}
	} else {
		c.socketAbsent = true
	}

	// Cleared before the walk so a capability reflects THIS scrape. A host
	// that gains pid: host on restart must stop reporting "namespaced".
	c.setCapability("")

	cur, err := c.read()
	if err != nil {
		return nil, err
	}

	prev, prevAt := c.prev, c.prevAt
	at := c.now()
	c.prev, c.prevAt = cur, at

	if prev == nil {
		return &Result{}, nil
	}

	elapsed := at.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return &Result{}, nil
	}

	ids := make([]string, 0, len(cur))
	for id := range cur {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	ts := at.UnixMilli()
	rows := make([]*netrav1.ContainerSample, 0, len(ids))

	for _, id := range ids {
		n := cur[id]
		p, ok := prev[id]
		if !ok {
			// A container started this scrape: no interval to rate over yet.
			continue
		}
		if n.cpuUsec < p.cpuUsec {
			// The cgroup was recreated under the same id. No row rather than
			// a negative rate.
			continue
		}

		m := meta[id]
		row := &netrav1.ContainerSample{
			TsMs:         ts,
			ContainerKey: containerKey(m, id),
			Name:         m.Name,
			Image:        m.Image,
			IsAgent:      m.IsAgent,
			// usage_usec is microseconds of CPU. Over `elapsed` seconds, one
			// fully-busy core is 1e6 usec per second.
			CpuPct:  ptrTo(float64(n.cpuUsec-p.cpuUsec) / (elapsed * 1e6) * 100),
			MemUsed: ptrTo(n.memUsed),
			// Gauges: reported on every scrape a container survives, unlike
			// the rates above which need a previous reading.
			MemAnon:   ptrTo(n.memAnon),
			MemFile:   ptrTo(n.memFile),
			MemShmem:  ptrTo(n.memShmem),
			MemKernel: ptrTo(n.memKernel),
		}
		// Same guard as usage_usec above: a cgroup recreated under one id
		// resets these, and a negative delta is no reading rather than a
		// negative percentage. Both scrapes must carry the split, or there is
		// no interval to rate over.
		if n.hasSplit && p.hasSplit && n.userUsec >= p.userUsec && n.systemUsec >= p.systemUsec {
			row.CpuUser = ptrTo(float64(n.userUsec-p.userUsec) / (elapsed * 1e6) * 100)
			row.CpuSystem = ptrTo(float64(n.systemUsec-p.systemUsec) / (elapsed * 1e6) * 100)
		}
		if n.hasLimit {
			// "max" means unlimited. Reporting the host's total instead would
			// invent a limit the operator never set.
			row.MemLimit = ptrTo(n.memLimit)
		}
		if n.rbytes >= p.rbytes {
			row.IoRead = ptrTo(float64(n.rbytes-p.rbytes) / elapsed)
		}
		if n.wbytes >= p.wbytes {
			row.IoWrite = ptrTo(float64(n.wbytes-p.wbytes) / elapsed)
		}
		// Both namespaces must have been readable, or the delta spans a gap
		// rather than an interval. A counter that went backwards means the
		// namespace was replaced -- a restart -- so no row rather than a
		// negative rate, matching Network's handling of the same case.
		if n.hasNet && p.hasNet {
			if n.rxBytes >= p.rxBytes {
				row.NetRx = ptrTo(float64(n.rxBytes-p.rxBytes) / elapsed)
			}
			if n.txBytes >= p.txBytes {
				row.NetTx = ptrTo(float64(n.txBytes-p.txBytes) / elapsed)
			}
		}

		rows = append(rows, row)
	}

	return &Result{Containers: rows}, nil
}

// containerKey is compose project + service, falling back to the container
// name, and finally to the id.
//
// The id is the last resort precisely because it is unstable: Docker issues a
// new one on every recreate, so a service that merely got a new image would
// start a fresh series and lose its history.
func containerKey(m ContainerMeta, id string) string {
	if m.Project != "" && m.Service != "" {
		return m.Project + "/" + m.Service
	}
	if m.Name != "" {
		return m.Name
	}
	return id
}

// read walks the cgroup v2 hierarchy for container scopes.
func (c *Containers) read() (map[string]containerCounters, error) {
	out := make(map[string]containerCounters)

	err := filepath.WalkDir(c.cgroupRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is not fatal: cgroups appear and vanish
			// constantly, and one missing directory must not cost the rest.
			return nil //nolint:nilerr // deliberate: skip, do not abort
		}
		if !d.IsDir() {
			return nil
		}
		id, ok := containerIDFromCgroup(d.Name())
		if !ok {
			return nil
		}

		cpuStat := filepath.Join(path, "cpu.stat")
		memStat := filepath.Join(path, "memory.stat")
		cc := containerCounters{
			cpuUsec:  readKeyedUint(cpuStat, "usage_usec"),
			memUsed:  containerMemory(path),
			memAnon:  readKeyedUint(memStat, "anon"),
			memShmem: readKeyedUint(memStat, "shmem"),
			// slab, not slab_reclaimable + slab_unreclaimable: the kernel
			// reports the total as its own line, and summing the two parts
			// would double count on kernels that report all three.
			memKernel: readKeyedUint(memStat, "slab"),
		}
		user, hasUser := lookupKeyedUint(cpuStat, "user_usec")
		system, hasSystem := lookupKeyedUint(cpuStat, "system_usec")
		cc.userUsec, cc.systemUsec = user, system
		cc.hasSplit = hasUser && hasSystem

		// `file` counts shmem inside it. Subtracting leaves the reclaimable
		// page cache, so a chart stacking file and shmem does not draw the
		// same pages twice.
		if file := readKeyedUint(memStat, "file"); file >= cc.memShmem {
			cc.memFile = file - cc.memShmem
		}
		if raw := strings.TrimSpace(readFileString(filepath.Join(path, "memory.max"))); raw != "" && raw != "max" {
			if v, err := strconv.ParseUint(raw, 10, 64); err == nil {
				cc.memLimit, cc.hasLimit = v, true
			}
		}
		cc.rbytes, cc.wbytes = readIOStat(filepath.Join(path, "io.stat"))
		cc.rxBytes, cc.txBytes, cc.hasNet = c.containerNet(path)

		out[id] = cc
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", c.cgroupRoot, err)
	}

	return out, nil
}

// containerMemory is memory.current MINUS the page cache.
//
// This is the whole reason the collector reads memory.stat at all. Raw
// memory.current counts the page cache as consumption, so a container that has
// merely read files looks like it is holding that memory -- and an operator
// sizing a limit from it would over-provision every service on the host.
// Subtracting inactive_file leaves what the container actually needs.
func containerMemory(dir string) uint64 {
	current := readUint(filepath.Join(dir, "memory.current"))
	inactiveFile := readKeyedUint(filepath.Join(dir, "memory.stat"), "inactive_file")
	if inactiveFile > current {
		return 0
	}
	return current - inactiveFile
}

// containerIDFromCgroup extracts a container id from a cgroup directory name,
// covering both drivers: "docker-<id>.scope" (systemd) and a bare 64-hex
// directory (cgroupfs).
func containerIDFromCgroup(name string) (string, bool) {
	if strings.HasPrefix(name, "docker-") && strings.HasSuffix(name, ".scope") {
		return strings.TrimSuffix(strings.TrimPrefix(name, "docker-"), ".scope"), true
	}
	if len(name) == 64 && isHex(name) {
		return name, true
	}
	return "", false
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func readFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func readUint(path string) uint64 {
	v, err := strconv.ParseUint(strings.TrimSpace(readFileString(path)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// readKeyedUint reads "key value" lines, as cpu.stat and memory.stat use.
func readKeyedUint(path, key string) uint64 {
	v, _ := lookupKeyedUint(path, key)
	return v
}

// lookupKeyedUint is readKeyedUint plus whether the key was actually there.
//
// The distinction matters for cpu.stat's user_usec and system_usec: a kernel
// that does not report them is not a container that spent no time in user
// space. The first must reach the database as NULL; the second is a reading
// of zero, and an operator hunting a busy service needs to tell them apart.
func lookupKeyedUint(path, key string) (uint64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == key {
			v, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, false
			}
			return v, true
		}
	}
	return 0, false
}

// readIOStat sums rbytes and wbytes across every device in io.stat. A
// container's I/O is the sum over the devices it touched, not one of them.
func readIOStat(path string) (rbytes, wbytes uint64) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		for _, field := range strings.Fields(scanner.Text()) {
			k, v, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				continue
			}
			switch k {
			case "rbytes":
				rbytes += n
			case "wbytes":
				wbytes += n
			}
		}
	}
	return rbytes, wbytes
}

// containerNet sums the bytes moved inside one container's own network
// namespace, and reports false when there is no such namespace to read.
//
// The counters come from /proc/<pid>/net/dev, reached through the container's
// own cgroup.procs rather than the Docker socket. That keeps the split this
// collector is built on: the socket supplies names, cgroup v2 supplies every
// metric, so a host that declines to mount the socket still gets numbers.
//
// A container sharing the host's namespace -- network_mode: host -- is
// deliberately reported as having no measurement. Its /proc/<pid>/net/dev IS
// the host's file, so counting it would attribute the entire machine's
// traffic to one container, and those same bytes are already reported once by
// the Network collector.
func (c *Containers) containerNet(cgroupDir string) (rx, tx uint64, ok bool) {
	pid, ok := firstPID(filepath.Join(cgroupDir, "cgroup.procs"))
	if !ok {
		// No processes in the cgroup: the container is stopped or restarting.
		return 0, 0, false
	}

	// FAIL CLOSED. Without the host's namespace to compare against there is no
	// way to tell a bridged container from a network_mode: host one, and the
	// two failure modes are not symmetric: reporting nothing loses a series,
	// while guessing attributes the ENTIRE machine's traffic to one container
	// on every scrape -- bytes the Network collector already reports once.
	//
	// The asymmetry is reachable rather than theoretical. /proc/<pid>/net/dev
	// is world-readable while /proc/<pid>/ns/net needs ptrace access, so an
	// unprivileged agent is exactly the case that fails the guard and
	// succeeds the read.
	host := c.hostNetNamespace()
	if host == "" {
		c.setCapability(capNetNoHostNS)
		return 0, 0, false
	}

	ns, nsOK := readNamespace(filepath.Join(c.procRoot, pid, "ns", "net"))
	if !nsOK {
		c.setCapability(capNetNoHostNS)
		return 0, 0, false
	}
	if ns == host {
		// network_mode: host. Its net/dev IS the host's file.
		return 0, 0, false
	}

	rx, tx, ok = sumNetDev(filepath.Join(c.procRoot, pid, "net", "dev"))
	if !ok {
		// The cgroup named a PID that /proc does not have. That is the
		// signature of a PID namespace: cgroup.procs reports host PIDs, so
		// without pid: host the agent looks them up in its own namespace and
		// finds nothing. Saying so distinguishes it from "no traffic".
		c.setCapability(capNetNamespaced)
	}
	return rx, tx, ok
}

// hostNetNamespace resolves the host's network namespace once. PID 1 is the
// host's init whenever /proc is the host's, which is the same mount this
// collector already needs in order to read per-container counters at all.
//
// Empty when it cannot be read, which callers must treat as "cannot measure"
// rather than "no guard needed" -- see containerNet.
func (c *Containers) hostNetNamespace() string {
	c.hostNetNSOnce.Do(func() {
		if ns, ok := readNamespace(filepath.Join(c.procRoot, "1", "ns", "net")); ok {
			c.hostNetNS = ns
		}
	})
	return c.hostNetNS
}

// readNamespace returns the "net:[4026531992]" identity behind a namespace
// symlink. Comparing two of these is how processes are told to share a
// namespace, and it needs no privilege beyond reading the link.
func readNamespace(path string) (string, bool) {
	target, err := os.Readlink(path)
	if err != nil {
		return "", false
	}
	return target, true
}

// firstPID returns the first entry of a cgroup.procs file. Any process in the
// cgroup will do: they all share the container's namespaces, which is the
// only property being used here.
func firstPID(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		pid := strings.TrimSpace(scanner.Text())
		if pid == "" {
			continue
		}
		if _, err := strconv.ParseUint(pid, 10, 64); err != nil {
			continue
		}
		return pid, true
	}
	return "", false
}

// sumNetDev totals receive and transmit bytes across a namespace's
// interfaces, skipping only loopback.
//
// It deliberately does NOT apply reportableIface. That list exists to stop
// the HOST double-counting container traffic it already sees on a real
// interface -- veth, br- and docker0 carry the same bytes twice. Inside a
// container's own namespace there is no such duplication: eth0 is the
// container's only path to the network, and excluding it by prefix would
// report zero for every container on a bridge.
func sumNetDev(path string) (rx, tx uint64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = f.Close() }()

	// Readable is what makes the answer known. A network_mode: none container
	// has nothing but lo, and its traffic is genuinely 0 rather than
	// unmeasured -- the one case where zero is the true, knowable answer.
	// Requiring a parsed non-lo line instead would report it as NULL.
	ok = true

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			// One of the two header lines.
			continue
		}
		if strings.TrimSpace(line[:colon]) == "lo" {
			continue
		}

		fields := strings.Fields(line[colon+1:])
		// 8 receive columns then 8 transmit columns.
		if len(fields) < 16 {
			continue
		}
		r, rerr := strconv.ParseUint(fields[0], 10, 64)
		t, terr := strconv.ParseUint(fields[8], 10, 64)
		if rerr != nil || terr != nil {
			continue
		}
		rx += r
		tx += t
	}
	return rx, tx, ok
}
