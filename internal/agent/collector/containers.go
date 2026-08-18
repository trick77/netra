package collector

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
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

	// NetworkMode is HostConfig.NetworkMode as the daemon reports it: "host",
	// "bridge", "none", a user-defined network's name, or "container:<id>".
	//
	// Empty when the socket is absent, which is a THIRD state rather than a
	// default -- see containerNet, which falls back to comparing namespace
	// links only when it has no answer here.
	NetworkMode string
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
	//
	// Each carries its own has* flag for the reason cpu.stat's split does: a
	// kernel that does not report a line is not a container holding zero
	// bytes of it. A container being OOM-killed by kernel slab on a kernel
	// with no `slab` line must not have its chart assert "0 bytes of kernel
	// slab" -- that is a measurement nobody took, and absent is not zero.
	memAnon   uint64
	hasAnon   bool
	memFile   uint64
	hasFile   bool
	memShmem  uint64
	hasShmem  bool
	memKernel uint64
	hasKernel bool

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

	// lastScopes and lastListed are what the most recent scrape actually saw:
	// container cgroups found by the walk, and containers the Docker socket
	// named. Kept as a PAIR because neither number means much alone -- it is
	// the mismatch between them that identifies the failure this collector was
	// blind to for its whole life: scopes 0 against listed 30 is a host whose
	// cgroup hierarchy was never mounted, which an empty walk reports as
	// success. Both are guarded by mu; see Capabilities.
	lastScopes int
	lastListed int

	// hostNetNS identifies the host's network namespace, resolved once.
	// Empty when it could not be read, which makes per-container networking
	// unmeasurable rather than unguarded -- see containerNet.
	hostNetNS     string
	hostNetNSOnce sync.Once

	// pidHost is the operator's own statement, from NETRA_PID_HOST, mirroring
	// what Procs and Processes are already given. It exists so containerNet can
	// SAY why a namespace link was unreadable instead of inferring it from an
	// errno: without the host PID namespace, cgroup.procs names host PIDs this
	// agent's /proc does not have, and the readlink fails with ENOENT -- which
	// is also what a process exiting mid-scrape looks like. The two need
	// different words and errno cannot tell them apart.
	pidHost bool

	// readlink is os.Readlink, replaced only by tests. It is a seam because
	// the latch below turns on the first EACCES and never off, and no fixture
	// tree spells EACCES portably: a suite running as root reads a 0000
	// directory perfectly well, and removing the link spells ENOENT, which is
	// deliberately the case that must NOT latch. The syscall is the only place
	// the two can be told apart on demand.
	readlink func(string) (string, error)

	mu sync.Mutex
	// netCapability explains why per-container networking is absent, or is
	// empty when it is working.
	netCapability string

	// netNSDenied latches the namespace comparison OFF after its first
	// failure. The comparison is a ptrace-gated readlink and a stock Docker
	// host denies it for every target -- see containerNet -- so a retry cannot
	// succeed: docker-default only permits ptrace against another
	// docker-default peer, and changing that needs a container restart, which
	// makes a new collector with this flag clear. The retry is not free
	// either. Every denied attempt writes one apparmor="DENIED" line to the
	// host's kernel audit log, and the scrape loop runs once a minute, so an
	// agent left alone contributes 1440 lines a day to the log the operator
	// installed it to help them read. One line per agent start is a diagnosis;
	// one a minute is vandalism.
	netNSDenied bool
}

// NewContainers builds a Containers collector. cgroupRoot is the mounted
// cgroup v2 hierarchy, procRoot the mounted /proc that per-container network
// counters are read through; lister may be nil when the Docker socket is not
// available. pidHost is NETRA_PID_HOST, and is what lets an unreadable
// namespace link be reported as the configuration it is rather than guessed at
// from an errno -- see the field comment.
func NewContainers(cgroupRoot, procRoot string, lister ContainerLister, pidHost bool) *Containers {
	return &Containers{
		cgroupRoot: cgroupRoot,
		procRoot:   procRoot,
		lister:     lister,
		pidHost:    pidHost,
		readlink:   os.Readlink,
		now:        time.Now,
	}
}

// Name implements Collector.
func (c *Containers) Name() string { return "containers" }

// SetCgroupRootForTest repoints the collector at a different fixture tree.
func (c *Containers) SetCgroupRootForTest(root string) { c.cgroupRoot = root }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (c *Containers) SetProcRootForTest(root string) { c.procRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (c *Containers) SetClockForTest(fn func() time.Time) { c.now = fn }

// SetReadlinkForTest replaces the readlink used to resolve namespace links.
func (c *Containers) SetReadlinkForTest(fn func(string) (string, error)) { c.readlink = fn }

// Capability values reported by Containers for per-container networking.
const (
	// capNetNamespaced: cgroup.procs names host PIDs, and without pid: host
	// the agent resolves them in its own namespace and finds nothing. Reported
	// on the operator's own statement (NETRA_PID_HOST), never inferred from an
	// errno -- a process exiting mid-scrape produces the same ENOENT.
	capNetNamespaced = "namespaced"

	// capNetNoHostNS: /proc/1/ns/net or the container's own is unreadable on a
	// host that DOES have the PID namespace, so a host-networked container
	// cannot be told from a bridged one. In practice this is the ptrace access
	// check: readlink on /proc/<pid>/ns/net needs PTRACE_MODE_READ_FSCRED,
	// which a root agent passes only for a root-owned process.
	capNetNoHostNS = "no-host-netns"

	// capNoCgroupScopes: the Docker socket named containers that the cgroup
	// walk could not find. cgroupRoot is either unmounted or is the agent's own
	// private-namespace hierarchy, which contains no sibling container's scope.
	//
	// Deliberately NOT raised when the socket named nothing: a host genuinely
	// running no containers must not be flagged as misconfigured, and with the
	// socket absent there is no second opinion to disagree with.
	capNoCgroupScopes = "no-cgroup-scopes"
)

// Capabilities implements CapabilityReporter.
func (c *Containers) Capabilities() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out map[string]string
	switch {
	case c.socketAbsent:
		// Metrics still arrive from cgroup v2; only the names and compose
		// labels are missing. Saying so distinguishes "no containers" from
		// "containers whose identity we cannot resolve".
		out = map[string]string{"containers": "no-docker-socket"}
	case c.lastScopes == 0 && c.lastListed > 0:
		// The reverse case, and the worse one: the socket can see containers
		// and the cgroup walk cannot. Nothing is collected at all, so without
		// this the fleet page would show a host that looks entirely healthy and
		// simply has no containers.
		out = map[string]string{"containers": capNoCgroupScopes}
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

// socketWasAbsent reports the socket state the PREVIOUS scrape left behind, so
// the transition logging in Collect compares against it without reading a
// mutex-guarded field bare.
func (c *Containers) socketWasAbsent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.socketAbsent
}

// observe records what this scrape saw, for Capabilities and StartupSummary to
// report. It is the only writer of socketAbsent: that field used to be assigned
// straight from Collect, unguarded, while Capabilities read it under the mutex.
func (c *Containers) observe(socketAbsent bool, scopes, listed int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.socketAbsent = socketAbsent
	c.lastScopes = scopes
	c.lastListed = listed
}

// StartupSummary implements StartupSummarizer.
//
// Both counts, always, even when they agree -- the agent saying "31 cgroup
// scopes, 30 containers named by the socket" on the line after it starts is
// what makes the broken case ("0 cgroup scopes, 30 containers named by the
// socket") legible at a glance, by anyone who has ever seen the working one.
func (c *Containers) StartupSummary() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	socket := fmt.Sprintf("%d %s named by the docker socket",
		c.lastListed, plural(c.lastListed, "container", "containers"))
	if c.socketAbsent {
		socket = "docker socket unavailable"
	}
	return fmt.Sprintf("%d cgroup %s, %s",
		c.lastScopes, plural(c.lastScopes, "scope", "scopes"), socket)
}

// setCapability records why per-container networking produced nothing. Reset
// each scrape by Collect, so a container that starts reporting again clears
// it rather than leaving the agent looking permanently broken.
func (c *Containers) setCapability(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.netCapability = value
}

// netNSIsDenied reports whether the namespace comparison has already been
// refused once, and must not be attempted again.
func (c *Containers) netNSIsDenied() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.netNSDenied
}

// denyNetNS latches the comparison off. Never called while mu is held.
func (c *Containers) denyNetNS() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.netNSDenied = true
}

// Collect implements Collector.
func (c *Containers) Collect(ctx context.Context) (*Result, error) {
	meta := map[string]ContainerMeta{}
	absent, listed := true, 0
	// Read once, under the mutex, rather than touching the field directly in
	// the two branches below. observe() exists because this field was assigned
	// from here unguarded while Capabilities read it under the lock; reading it
	// here unguarded would leave the same class of access the split was meant
	// to remove, even though every caller is on the scrape goroutine today.
	wasAbsent := c.socketWasAbsent()
	if c.lister != nil {
		list, err := c.lister(ctx)
		if err != nil {
			// Logged on the TRANSITION only. Every message dockermeta.go builds
			// used to die right here, discarded, so a daemon that started
			// refusing connections was indistinguishable from one that was
			// never mounted. Logging it every scrape would be the opposite
			// mistake: a host that deliberately declines the socket is a
			// supported configuration, and it must not warn once a minute
			// forever.
			if !wasAbsent {
				slog.Warn("docker socket unreadable; containers will be keyed by id rather than compose service",
					"err", err)
			}
		} else {
			if wasAbsent {
				slog.Info("docker socket readable again", "containers", len(list))
			}
			absent, listed = false, len(list)
			for _, m := range list {
				meta[m.ID] = m
			}
		}
	}

	// Cleared before the walk so a capability reflects THIS scrape. A host
	// that gains pid: host on restart must stop reporting "namespaced".
	c.setCapability("")

	cur, err := c.read(meta, !absent)
	if err != nil {
		c.observe(absent, 0, listed)
		return nil, err
	}
	c.observe(absent, len(cur), listed)

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
		}
		// Gauges: reported on every scrape a container survives, unlike the
		// rates above which need a previous reading -- but only where the
		// kernel actually reported the line. An unset field reaches the
		// database as NULL, which the charts draw as a gap; a zero would be
		// drawn as a measurement.
		if n.hasAnon {
			row.MemAnon = ptrTo(n.memAnon)
		}
		if n.hasFile {
			row.MemFile = ptrTo(n.memFile)
		}
		if n.hasShmem {
			row.MemShmem = ptrTo(n.memShmem)
		}
		if n.hasKernel {
			row.MemKernel = ptrTo(n.memKernel)
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

	return &Result{Containers: capContainerRows(rows)}, nil
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
func (c *Containers) read(meta map[string]ContainerMeta, socketAnswered bool) (map[string]containerCounters, error) {
	out := make(map[string]containerCounters)

	// Containers whose counters SHOULD have been readable, and those that were.
	// Only ever compared against each other, at the end of the walk -- see the
	// warning there for why a total failure earns a word and a partial one does
	// not. The walk callback is synchronous, so no lock is needed.
	netEligible, netMeasured := 0, 0

	err := filepath.WalkDir(c.cgroupRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// The ROOT is different from anything below it. An unreadable
			// subtree is not fatal -- cgroups appear and vanish constantly, and
			// one missing directory must not cost the rest -- but an unreadable
			// root means there is no hierarchy here at all, which is a
			// configuration fault and the one this collector spent its whole
			// life failing to report. Swallowing it made an agent whose cgroup
			// mount was never granted indistinguishable from a host running no
			// containers.
			if path == c.cgroupRoot {
				return err
			}
			return nil //nolint:nilerr // deliberate: skip a subtree, not the root
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
			cpuUsec: readKeyedUint(cpuStat, "usage_usec"),
			memUsed: containerMemory(path),
		}
		// lookupKeyedUint throughout, never readKeyedUint: every one of these
		// is a line a kernel may simply not have, and readKeyedUint's missing
		// -> 0 would store that absence as a measured zero.
		cc.memAnon, cc.hasAnon = lookupKeyedUint(memStat, "anon")
		cc.memShmem, cc.hasShmem = lookupKeyedUint(memStat, "shmem")
		// slab, not slab_reclaimable + slab_unreclaimable: the kernel
		// reports the total as its own line, and summing the two parts
		// would double count on kernels that report all three.
		cc.memKernel, cc.hasKernel = lookupKeyedUint(memStat, "slab")

		user, hasUser := lookupKeyedUint(cpuStat, "user_usec")
		system, hasSystem := lookupKeyedUint(cpuStat, "system_usec")
		cc.userUsec, cc.systemUsec = user, system
		cc.hasSplit = hasUser && hasSystem

		// `file` counts shmem inside it. Subtracting leaves the reclaimable
		// page cache, so a chart stacking file and shmem does not draw the
		// same pages twice.
		//
		// Three cases, and only the third is absent. No `file` line at all is
		// nothing to report. A `file` line with no `shmem` line is file
		// WHOLE -- there is no shmem for the kernel to have counted inside
		// it, the same rule memory.go follows for the host's Cached
		// (TestMemoryAbsentShmemLeavesCachedWhole). A `file` smaller than
		// `shmem` is the kernel disagreeing with itself across two lines, not
		// a container holding zero bytes of page cache, so it reports nothing
		// rather than falling through to a zero drawn as a measurement.
		if file, ok := lookupKeyedUint(memStat, "file"); ok {
			switch {
			case !cc.hasShmem:
				cc.memFile, cc.hasFile = file, true
			case file >= cc.memShmem:
				cc.memFile, cc.hasFile = file-cc.memShmem, true
			}
		}
		if raw := strings.TrimSpace(readFileString(filepath.Join(path, "memory.max"))); raw != "" && raw != "max" {
			if v, err := strconv.ParseUint(raw, 10, 64); err == nil {
				cc.memLimit, cc.hasLimit = v, true
			}
		}
		cc.rbytes, cc.wbytes = readIOStat(filepath.Join(path, "io.stat"))
		m, listed := meta[id]
		if socketAnswered && listed && m.NetworkMode != "" &&
			!sharesForeignNetNS(m.NetworkMode) {
			netEligible++
		}
		cc.rxBytes, cc.txBytes, cc.hasNet = c.containerNet(path, m.NetworkMode, listed, socketAnswered)
		if cc.hasNet {
			netMeasured++
		}

		out[id] = cc
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", c.cgroupRoot, err)
	}

	// EVERY eligible container failing is a different fact from a few failing,
	// and only the first is worth a word.
	//
	// A single failed net/dev read is a process that exited mid-scrape --
	// routine, and deliberately silent, because container_network is host-wide
	// and one short-lived container must not blank the panel for the rest. But
	// if not one eligible container yielded counters, the cause is systemic:
	// procRoot points somewhere that is not this container's /proc, which is
	// reachable by setting NETRA_PROC_ROOT by hand and looks exactly like a
	// fleet of quiet containers.
	//
	// Logged rather than raised as a capability. The capability values name
	// what the operator should DO, and both existing ones would be lies here:
	// the namespace is granted and the socket answered. A warning names the
	// variable, which is the actionable part.
	if netEligible > 0 && netMeasured == 0 {
		slog.Warn("no container reported network counters; is NETRA_PROC_ROOT this container's own /proc?",
			"proc_root", c.procRoot, "containers", netEligible)
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

// plural picks the form matching n. One container is not "1 containers", and a
// line an operator is meant to read at a glance should not read like a counter.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
// A container sharing another namespace -- network_mode: host, or
// network_mode: "container:<id>" -- is deliberately reported as having no
// measurement. Its /proc/<pid>/net/dev is somebody ELSE's file, so counting it
// would report the same bytes twice: for host networking those bytes are the
// whole machine's and the Network collector already has them, and for a shared
// container namespace the container that owns it reports them itself.
//
// networkMode is HostConfig.NetworkMode when the Docker socket answered, and
// empty when it did not. It is preferred over comparing namespace links
// because the comparison is not reachable on a stock install: readlink on
// /proc/<pid>/ns/net goes through ptrace_may_access, which requires
// CAP_SYS_PTRACE for a non-dumpable target EVEN WHEN THE UIDS MATCH, and
// `security_opt: no-new-privileges` makes the targets non-dumpable. Measured
// on a live host: every container denied, root-owned ones included, and only
// --cap-add SYS_PTRACE lifted it. The counters themselves never needed the
// capability -- /proc/<pid>/net/dev is world-readable -- so asking for it to
// answer a question the socket already answers would have been a large
// privilege for nothing.
func (c *Containers) containerNet(cgroupDir, networkMode string, listed, socketAnswered bool) (rx, tx uint64, ok bool) {
	pid, ok := firstPID(filepath.Join(cgroupDir, "cgroup.procs"))
	if !ok {
		// No processes in the cgroup: the container is stopped or restarting.
		return 0, 0, false
	}

	if socketAnswered {
		// cgroup.procs names HOST pids. Without the host PID namespace they
		// cannot be resolved here at all, and the danger is not that the read
		// fails -- it is that it SUCCEEDS: host pid 1234 may well exist in the
		// agent's own namespace as some unrelated process, whose net/dev is
		// the agent's own container interface. That would be a plausible
		// number attributed to the wrong container, which is worse than none.
		//
		// Reported once for the host rather than per container, because it is
		// a fact about the agent's deployment and identical for every scope.
		if !c.pidHost {
			c.setCapability(capNetNamespaced)
			return 0, 0, false
		}
		// The walk found a cgroup scope the socket did not list. A container
		// that stopped between the two calls, or a scope Docker does not own
		// at all -- podman, a bare systemd scope. Either way there is no answer
		// for it, and it must NOT fall through to the namespace comparison
		// below: that comparison fails on a stock install, and the capability
		// it sets is HOST-WIDE, so one unlistable scope would blank the Network
		// panel for every container that measured perfectly well.
		//
		// Keyed on presence in the map rather than on an empty NetworkMode.
		// Docker always reports a mode for a container it lists -- "default"
		// when nothing was asked for -- so the two are the same thing in
		// production, but conflating them means a daemon that omits the field
		// is treated as "not ours" and silently stops being measured.
		if !listed {
			return 0, 0, false
		}
		if networkMode != "" {
			if sharesForeignNetNS(networkMode) {
				return 0, 0, false
			}
			return c.readNetDev(pid)
		}
		// Listed, but the daemon named no mode. Nothing to trust, so fall
		// through to the comparison, which at least fails closed.
	}

	// NO SOCKET: fall back to comparing namespace links, and FAIL CLOSED.
	// Without the host's namespace to compare against there is no way to tell
	// a bridged container from a network_mode: host one, and the two failure
	// modes are not symmetric: reporting nothing loses a series, while
	// guessing attributes the ENTIRE machine's traffic to one container on
	// every scrape -- bytes the Network collector already reports once.
	//
	// Attempted at most ONCE per agent process. The readlinks below are
	// ptrace-gated and a stock host refuses them, and a refusal that repeats
	// every scrape is a line a minute in the host's kernel audit log for an
	// answer that cannot change while the container lives -- see netNSDenied.
	// The capability is still set on every scrape, so what the UI reports is
	// exactly what it reported before the latch.
	if c.netNSIsDenied() {
		c.setCapability(c.netFailure())
		return 0, 0, false
	}

	host := c.hostNetNamespace()
	if host == "" {
		c.denyNetNS()
		c.setCapability(c.netFailure())
		return 0, 0, false
	}

	// Latched on EACCES ONLY, never on ENOENT. The two reach this line for
	// opposite reasons: EACCES is the ptrace refusal, a property of the
	// profile that holds for every container until the agent restarts, while
	// ENOENT is one container having exited between the read of cgroup.procs
	// and this readlink -- routine on a busy host, and commoner the
	// shorter-lived the container. Latching on ENOENT would let a single
	// short-lived container switch per-container networking off for the whole
	// host until the next restart, which is a far worse bug than the log noise
	// this latch exists to stop.
	ns, err := c.readNamespace(filepath.Join(c.procRoot, pid, "ns", "net"))
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			c.denyNetNS()
		}
		c.setCapability(c.netFailure())
		return 0, 0, false
	}
	if ns == host {
		// network_mode: host. Its net/dev IS the host's file.
		return 0, 0, false
	}

	return c.readNetDev(pid)
}

// sharesForeignNetNS reports whether a HostConfig.NetworkMode names a namespace
// this container did not get to itself.
//
// "host" is the machine's own, already counted once by the Network collector.
// "container:<id>" is a sidecar joining a peer, and the peer reports the same
// counters under its own key -- attributing them to both would double every
// byte a sidecar's pod moves. Everything else ("bridge", "none", or a
// user-defined network's name) is the container's own namespace, and "none"
// legitimately measures as zero: an interface list of nothing but lo is a
// knowable answer rather than a missing one.
func sharesForeignNetNS(networkMode string) bool {
	return networkMode == "host" || strings.HasPrefix(networkMode, "container:")
}

// readNetDev sums one PID's interface counters.
//
// Reached only once the PID namespace is known to be the host's, so a missing
// net/dev here is NOT a configuration fault: it is the process having exited
// between the read of cgroup.procs and this read, which is routine on a busy
// host and gets commoner the shorter-lived the container. It therefore reports
// no measurement and sets NO capability.
//
// Saying "the agent could not read this host's network namespaces" here would
// have been false twice over -- no namespace was read on this path, and
// nothing is misconfigured -- and expensively so: container_network is
// host-wide, and ContainerPage swaps the whole Network panel for the message
// whenever it is set. One container exiting mid-scrape would have hidden
// correctly measured traffic for every other container on the box.
func (c *Containers) readNetDev(pid string) (rx, tx uint64, ok bool) {
	return sumNetDev(filepath.Join(c.procRoot, pid, "net", "dev"))
}

// netFailure names the reason the NAMESPACE COMPARISON produced nothing.
//
// Reached only on the socket-absent path -- the socket path answers without
// reading a namespace at all, so it must never call this: naming a namespace
// failure for a syscall that was never attempted is a sentence the operator
// cannot act on, and container_network is host-wide, so it would blank the
// Network panel for every container on the box.
//
// A link that could not be read has exactly two causes: no host PID namespace,
// or no ptrace access to a process owned by another user. The errno does not
// separate them -- a missing PID and a process that exited during the scrape
// are both ENOENT -- so this reads NETRA_PID_HOST, which the operator set to
// describe the container they actually deployed.
//
// Getting it wrong is not cosmetic. Each value has its own remedy in the UI,
// and "could not read this host's network namespaces" sent to an operator whose
// real problem is a missing `pid: host` is a sentence they cannot act on.
func (c *Containers) netFailure() string {
	if !c.pidHost {
		return capNetNamespaced
	}
	return capNetNoHostNS
}

// hostNetNamespace resolves the host's network namespace once. PID 1 is the
// host's init whenever /proc is the host's, which is the same mount this
// collector already needs in order to read per-container counters at all.
//
// Empty when it cannot be read, which callers must treat as "cannot measure"
// rather than "no guard needed" -- see containerNet.
func (c *Containers) hostNetNamespace() string {
	c.hostNetNSOnce.Do(func() {
		if ns, err := c.readNamespace(filepath.Join(c.procRoot, "1", "ns", "net")); err == nil {
			c.hostNetNS = ns
		}
	})
	return c.hostNetNS
}

// readNamespace returns the "net:[4026531992]" identity behind a namespace
// symlink. Comparing two of these is how processes are told to share a
// namespace, and it needs no privilege beyond reading the link.
//
// The error is returned rather than a bare ok, because the CALLER has to tell
// EACCES from ENOENT: only the first is a standing refusal worth latching on.
// See containerNet.
func (c *Containers) readNamespace(path string) (string, error) {
	return c.readlink(path)
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

// maxContainerRows caps how many containers one scrape reports.
//
// This is the missing half of the ingest bound. maxBatchRows keeps a
// MULTI-scrape body inside the hub's 4 MiB cap, but the flush deliberately lets
// a single oversized scrape through alone rather than never flushing at all --
// so a scrape that exceeds the cap by itself is undeliverable, and the agent now
// drops it. Dropping beats wedging, but a host that produces one every tick
// still reports nothing, forever. Containers was the family that could: it
// emitted one row per cgroup scope with no bound at all, which is exactly the
// case the maxBufferSlots comment names as the one to revisit.
//
// 500 is far above any host an operator runs deliberately -- Docker's own
// practical ceiling per daemon is in the low hundreds -- and far below the
// 20000-row batch limit, so the cap is a backstop rather than a routine
// truncation.
//
// Processes solves the same problem by ranking (topByCPUAndMemory); containers
// are not ranked, because a container is an entity an operator names and looks
// for rather than a population to sample. Truncating the tail of a stable sort
// keeps the same containers reported scrape after scrape, so a chart does not
// flicker between them -- and the log line says what was left out, which
// silently returning 500 rows would not.
const maxContainerRows = 500

// capContainerRows truncates an implausibly large container set, loudly.
func capContainerRows(rows []*netrav1.ContainerSample) []*netrav1.ContainerSample {
	if len(rows) <= maxContainerRows {
		return rows
	}
	// rows is built in sorted-id order, so the survivors are stable across
	// scrapes rather than an arbitrary subset that changes every minute.
	slog.Warn("more containers than one scrape can carry; reporting a truncated set",
		"found", len(rows), "reported", maxContainerRows)
	return rows[:maxContainerRows]
}
