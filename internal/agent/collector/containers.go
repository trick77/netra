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
}

// Containers reports per-container CPU, memory and I/O from cgroup v2.
//
// Identity is the compose project + service, falling back to the container
// name (spec §6.2). Deliberately NOT the Docker id: a recreate issues a new id
// for the same service, so keying history on it would restart every series
// whenever an image is bumped.
type Containers struct {
	cgroupRoot string
	interval   time.Duration
	lister     ContainerLister

	now func() time.Time

	prev   map[string]containerCounters
	prevAt time.Time

	socketAbsent bool
}

// NewContainers builds a Containers collector. cgroupRoot is the mounted
// cgroup v2 hierarchy; lister may be nil when the Docker socket is not
// available.
func NewContainers(cgroupRoot string, interval time.Duration, lister ContainerLister) *Containers {
	return &Containers{cgroupRoot: cgroupRoot, interval: interval, lister: lister, now: time.Now}
}

// Name implements Collector.
func (c *Containers) Name() string { return "containers" }

// Interval implements Collector.
func (c *Containers) Interval() time.Duration { return c.interval }

// SetCgroupRootForTest repoints the collector at a different fixture tree.
func (c *Containers) SetCgroupRootForTest(root string) { c.cgroupRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (c *Containers) SetClockForTest(fn func() time.Time) { c.now = fn }

// Capabilities implements CapabilityReporter.
func (c *Containers) Capabilities() map[string]string {
	if c.socketAbsent {
		// Metrics still arrive from cgroup v2; only the names and compose
		// labels are missing. Saying so distinguishes "no containers" from
		// "containers whose identity we cannot resolve".
		return map[string]string{"containers": "no-docker-socket"}
	}
	return nil
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

		cc := containerCounters{
			cpuUsec: readKeyedUint(filepath.Join(path, "cpu.stat"), "usage_usec"),
			memUsed: containerMemory(path),
		}
		if raw := strings.TrimSpace(readFileString(filepath.Join(path, "memory.max"))); raw != "" && raw != "max" {
			if v, err := strconv.ParseUint(raw, 10, 64); err == nil {
				cc.memLimit, cc.hasLimit = v, true
			}
		}
		cc.rbytes, cc.wbytes = readIOStat(filepath.Join(path, "io.stat"))

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
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == key {
			v, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return v
		}
	}
	return 0
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
