package collector_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// noProcRoot points the network read at a tree that does not exist, so the
// tests that predate per-container networking keep exercising exactly the
// cgroup paths they were written for.
const noProcRoot = "testdata/proc-absent"

func fakeLister(metas ...collector.ContainerMeta) collector.ContainerLister {
	return func(context.Context) ([]collector.ContainerMeta, error) { return metas, nil }
}

// netFixture builds a cgroup tree with one container plus the /proc tree its
// network counters are read through.
//
// Built in a TempDir rather than checked in because the namespace identity is
// a symlink whose target ("net:[...]") is not a path -- a dangling link by
// design, which does not belong in the repository.
func netFixture(t *testing.T, pid, hostNS, containerNS string, rx, tx uint64) (cgroupRoot, procRoot string) {
	t.Helper()

	root := t.TempDir()
	cgroupRoot = filepath.Join(root, "cgroup")
	procRoot = filepath.Join(root, "proc")

	scope := filepath.Join(cgroupRoot, "system.slice", "docker-abc123.scope")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatalf("mkdir scope: %v", err)
	}
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write(scope, "cpu.stat", "usage_usec 1000000\n")
	write(scope, "memory.current", "1000\n")
	write(scope, "memory.stat", "inactive_file 0\n")
	write(scope, "memory.max", "max\n")
	write(scope, "io.stat", "8:0 rbytes=0 wbytes=0\n")
	if pid != "" {
		write(scope, "cgroup.procs", pid+"\n")
	} else {
		write(scope, "cgroup.procs", "\n")
	}

	link := func(nsPath, target string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(nsPath), 0o755); err != nil {
			t.Fatalf("mkdir ns: %v", err)
		}
		if err := os.Symlink(target, nsPath); err != nil {
			t.Fatalf("symlink %s: %v", nsPath, err)
		}
	}
	link(filepath.Join(procRoot, "1", "ns", "net"), hostNS)

	if pid != "" {
		link(filepath.Join(procRoot, pid, "ns", "net"), containerNS)
		netDir := filepath.Join(procRoot, pid, "net")
		if err := os.MkdirAll(netDir, 0o755); err != nil {
			t.Fatalf("mkdir net: %v", err)
		}
		// Header lines, then lo (which must be skipped) and eth0.
		body := "Inter-|   Receive                        |  Transmit\n" +
			" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
			"    lo: 9999 1 0 0 0 0 0 0 9999 1 0 0 0 0 0 0\n" +
			fmt.Sprintf("  eth0: %d 10 0 0 0 0 0 0 %d 10 0 0 0 0 0 0\n", rx, tx)
		write(netDir, "dev", body)
	}

	return cgroupRoot, procRoot
}

// advanceNet rewrites the fixture's counters for the second scrape.
func advanceNet(t *testing.T, procRoot, pid string, rx, tx uint64) {
	t.Helper()
	body := "Inter-|   Receive                        |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"    lo: 9999 1 0 0 0 0 0 0 9999 1 0 0 0 0 0 0\n" +
		fmt.Sprintf("  eth0: %d 10 0 0 0 0 0 0 %d 10 0 0 0 0 0 0\n", rx, tx)
	if err := os.WriteFile(filepath.Join(procRoot, pid, "net", "dev"), []byte(body), 0o644); err != nil {
		t.Fatalf("rewrite net/dev: %v", err)
	}
}

func containersAt(t *testing.T, c *collector.Containers, at time.Time) *collector.Result {
	t.Helper()
	c.SetClockForTest(func() time.Time { return at })
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return res
}

func containerRow(t *testing.T, rows []*netrav1.ContainerSample, key string) *netrav1.ContainerSample {
	t.Helper()
	for _, r := range rows {
		if r.GetContainerKey() == key {
			return r
		}
	}
	t.Fatalf("no row for %q in %d rows", key, len(rows))
	return nil
}

// Raw memory.current counts the PAGE CACHE as consumption.
//
// The fixture container has 1 GiB of memory.current, of which 512 MiB is
// inactive_file -- pages it merely read, which the kernel will reclaim on
// demand. Reporting 1 GiB would tell an operator sizing a limit that the
// service needs twice what it does, and every container on a busy host reads
// files.
func TestContainersSubtractsPageCacheFromMemory(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web", Project: "proj", Service: "web"}))

	containersAt(t, testee, base)
	testee.SetCgroupRootForTest("testdata/cgroup/second/sys/fs/cgroup")
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "proj/web")
	const gib = 1073741824
	const inactiveFile = 536870912
	if got := row.GetMemUsed(); got != gib-inactiveFile {
		t.Errorf("mem_used = %d, want %d (memory.current minus inactive_file)", got, gib-inactiveFile)
	}
	if row.GetMemUsed() == gib {
		t.Error("mem_used is raw memory.current; the page cache must be subtracted")
	}
}

// usage_usec 1000000 -> 6000000 is 5 seconds of CPU over a 10 second interval:
// 50% of one core.
func TestContainersComputesCPUFromUsageMicroseconds(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Project: "proj", Service: "web"}))

	res := containersAt(t, testee, base)
	if len(res.Containers) != 0 {
		t.Fatalf("first scrape produced %d rows, want 0", len(res.Containers))
	}

	testee.SetCgroupRootForTest("testdata/cgroup/second/sys/fs/cgroup")
	res = containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "proj/web")
	if got := row.GetCpuPct(); got != 50 {
		t.Errorf("cpu_pct = %v, want 50", got)
	}
	// rbytes 1000000 -> 3000000 over 10s = 200000 B/s.
	if got := row.GetIoRead(); got != 200000 {
		t.Errorf("io_read = %v, want 200000", got)
	}
	if got := row.GetIoWrite(); got != 100000 {
		t.Errorf("io_write = %v, want 100000", got)
	}
	if row.GetTsMs() == 0 {
		t.Error("row carries no ts_ms")
	}
}

// Identity is compose project + service, NOT the Docker id.
//
// A recreate issues a new id for the same service, so keying on it would
// restart the history of a service that merely got a new image -- the graph
// goes blank and the old series is orphaned.
func TestContainersIdentityIsComposeProjectAndService(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot,
		fakeLister(collector.ContainerMeta{
			ID: "abc123", Name: "proj-web-1", Image: "nginx:1", Project: "proj", Service: "web",
		}))

	containersAt(t, testee, base)
	testee.SetCgroupRootForTest("testdata/cgroup/second/sys/fs/cgroup")
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "proj/web")
	if row.GetContainerKey() == "abc123" {
		t.Error("container_key is the Docker id; it must be the compose identity")
	}
	if got := row.GetName(); got != "proj-web-1" {
		t.Errorf("name = %q, want proj-web-1", got)
	}
	if got := row.GetImage(); got != "nginx:1" {
		t.Errorf("image = %q, want nginx:1", got)
	}
}

// Without compose labels the container name is the identity. It is stable
// across restarts of the same container, which the id is not.
func TestContainersFallsBackToTheContainerName(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "standalone"}))

	containersAt(t, testee, base)
	testee.SetCgroupRootForTest("testdata/cgroup/second/sys/fs/cgroup")
	res := containersAt(t, testee, base.Add(10*time.Second))

	containerRow(t, res.Containers, "standalone")
}

// "max" in memory.max means unlimited. Reporting the host's total instead
// would invent a limit the operator never set, and any "percent of limit"
// derived from it would be fiction.
func TestContainersLeavesMemLimitUnsetWhenUnlimited(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot,
		fakeLister(collector.ContainerMeta{ID: "def456", Name: "unlimited"}))

	containersAt(t, testee, base)
	testee.SetCgroupRootForTest("testdata/cgroup/second/sys/fs/cgroup")
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "unlimited")
	if row.MemLimit != nil {
		t.Errorf("mem_limit = %d for an unlimited container; want unset", row.GetMemLimit())
	}
	// The limited container in the same fixture does report one.
	limited := containerRow(t, res.Containers, "abc123")
	if limited.MemLimit == nil {
		t.Error("mem_limit unset for a container with a real limit")
	}
}

// The Docker socket supplies names only. Without it the metrics still arrive
// from cgroup v2 -- the socket is an enrichment, not a dependency -- and the
// capability says why the names are missing.
func TestContainersReportsMetricsWithoutTheDockerSocket(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	failing := func(context.Context) ([]collector.ContainerMeta, error) {
		return nil, errors.New("dial /var/run/docker.sock: no such file")
	}
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot, failing)

	containersAt(t, testee, base)
	testee.SetCgroupRootForTest("testdata/cgroup/second/sys/fs/cgroup")
	res := containersAt(t, testee, base.Add(10*time.Second))

	if len(res.Containers) != 2 {
		t.Fatalf("rows = %d, want 2 -- cgroup metrics do not need the socket", len(res.Containers))
	}
	// Falls back to the id, which is all that is knowable without the socket.
	containerRow(t, res.Containers, "abc123")

	if got := testee.Capabilities()["containers"]; got != "no-docker-socket" {
		t.Errorf("capability = %q, want no-docker-socket", got)
	}
}

// net_rx and net_tx were on the wire and in the schema from the start, and
// the hub inserted them, but the collector never set either -- cgroup v2 has
// no network counters. They came out NULL for every real host while the
// container page rendered a panel for them.
//
// The counters live in the container's own network namespace, reached via its
// cgroup.procs, which keeps the socket an enrichment rather than a dependency.
func TestContainersReportsNetworkFromTheContainerNamespace(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}))

	containersAt(t, testee, base)
	advanceNet(t, procRoot, "42", 3000, 1500)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	// 1000 -> 3000 bytes over 10s = 200 B/s, and lo's 9999 is excluded.
	if got := row.GetNetRx(); got != 200 {
		t.Errorf("net_rx = %v, want 200", got)
	}
	if got := row.GetNetTx(); got != 100 {
		t.Errorf("net_tx = %v, want 100", got)
	}
}

// A network_mode: host container's /proc/<pid>/net/dev IS the host's file.
// Counting it would attribute the whole machine's traffic to one container,
// and those bytes are already reported once by the Network collector.
func TestContainersSkipsNetworkForHostNetworkedContainers(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	const hostNS = "net:[4026531992]"
	// Same namespace as PID 1: this container shares the host's network.
	cgroupRoot, procRoot := netFixture(t, "42", hostNS, hostNS, 1000, 500)

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}))

	containersAt(t, testee, base)
	advanceNet(t, procRoot, "42", 3000, 1500)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx != nil {
		t.Errorf("net_rx = %v for a host-networked container; want unset", row.GetNetRx())
	}
	if row.NetTx != nil {
		t.Errorf("net_tx = %v for a host-networked container; want unset", row.GetNetTx())
	}
	// The rest of the row still reports: only networking is unknowable here.
	if row.MemUsed == nil {
		t.Error("mem_used unset; the host-network guard must not suppress the whole row")
	}
}

// Without the host's namespace to compare against, a bridged container and a
// network_mode: host one are indistinguishable -- and the two mistakes are
// not symmetric. Reporting nothing loses a series; guessing attributes the
// entire machine's traffic to one container on every scrape.
//
// Reachable rather than theoretical: /proc/<pid>/net/dev is world-readable
// while /proc/<pid>/ns/net needs ptrace access, so an unprivileged agent is
// exactly the case that fails the guard and succeeds the read.
func TestContainersFailsClosedWhenTheHostNamespaceIsUnreadable(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	// Remove PID 1's namespace link: the host's namespace becomes unknowable
	// while the container's counters stay perfectly readable.
	if err := os.Remove(filepath.Join(procRoot, "1", "ns", "net")); err != nil {
		t.Fatalf("remove host ns link: %v", err)
	}

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}))

	containersAt(t, testee, base)
	advanceNet(t, procRoot, "42", 3000, 1500)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx != nil || row.NetTx != nil {
		t.Errorf("net_rx/net_tx set with the host namespace unknown; a host-networked container would report the whole machine's traffic")
	}
	if got := testee.Capabilities()["container_network"]; got != "no-host-netns" {
		t.Errorf("capability = %q, want no-host-netns -- silence must be explained", got)
	}
}

// cgroup.procs names HOST pids. Without pid: host the agent resolves them in
// its own namespace and finds nothing, which is a deployment fact rather than
// an absence of traffic -- so it is reported as a capability.
func TestContainersReportsNamespacedWhenPidsDoNotResolve(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	// The PID exists in the cgroup and has a namespace, but no net/dev: the
	// signature of looking a host PID up in the wrong namespace.
	if err := os.Remove(filepath.Join(procRoot, "42", "net", "dev")); err != nil {
		t.Fatalf("remove net/dev: %v", err)
	}

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}))

	containersAt(t, testee, base)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx != nil {
		t.Error("net_rx set with no readable net/dev")
	}
	if got := testee.Capabilities()["container_network"]; got != "namespaced" {
		t.Errorf("capability = %q, want namespaced", got)
	}
	// Only networking is missing; the rest of the row is fine.
	if row.MemUsed == nil {
		t.Error("mem_used unset; only the network read failed")
	}
}

// A network_mode: none container has nothing but lo, and its traffic is
// genuinely 0 rather than unmeasured -- the one case where zero is the true,
// knowable answer.
func TestContainersReportsZeroForAContainerWithNoInterfaces(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 0, 0)

	loOnly := "Inter-|   Receive                        |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"    lo: 9999 1 0 0 0 0 0 0 9999 1 0 0 0 0 0 0\n"
	devPath := filepath.Join(procRoot, "42", "net", "dev")
	if err := os.WriteFile(devPath, []byte(loOnly), 0o644); err != nil {
		t.Fatalf("write net/dev: %v", err)
	}

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}))

	containersAt(t, testee, base)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx == nil || row.NetTx == nil {
		t.Fatal("net_rx/net_tx unset for a container with no interfaces; 0 is the knowable answer here")
	}
	if got := row.GetNetRx(); got != 0 {
		t.Errorf("net_rx = %v, want 0 -- lo must not be counted", got)
	}
}

// An empty cgroup.procs means the container is stopped or restarting. There
// is no namespace to read, which is not the same as having moved no bytes --
// so the fields stay unset rather than reporting a zero rate.
func TestContainersLeavesNetworkUnsetWithoutARunningProcess(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "", "net:[4026531992]", "", 0, 0)

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}))

	containersAt(t, testee, base)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx != nil || row.NetTx != nil {
		t.Errorf("net_rx/net_tx set with no process in the cgroup; want unset")
	}
}

// A host with no containers is the common case for a plain VPS. Absent, not
// broken.
func TestContainersReportsNothingWithNoContainers(t *testing.T) {
	testee := collector.NewContainers(t.TempDir(), noProcRoot, fakeLister())

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Containers) != 0 {
		t.Errorf("rows = %d, want 0", len(res.Containers))
	}
}
