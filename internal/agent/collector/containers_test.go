package collector_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web", Project: "proj", Service: "web"}), true)

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
		fakeLister(collector.ContainerMeta{ID: "abc123", Project: "proj", Service: "web"}), true)

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
		}), true)

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
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "standalone"}), true)

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
		fakeLister(collector.ContainerMeta{ID: "def456", Name: "unlimited"}), true)

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
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot, failing, true)

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
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), true)

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
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), true)

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
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), true)

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
//
// The collector is told so (pidHost=false) rather than deducing it: the errno
// cannot separate "this PID is not in my namespace" from "this process exited
// mid-scrape", and the two need different words. See TestContainers...
// ReportsNoHostNSWhenThePidNamespaceIsPresent for the other half of the pair --
// same fixture, same syscall failure, different answer.
func TestContainersReportsNamespacedWhenPidsDoNotResolve(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	// The PID exists in the cgroup and has a namespace, but no net/dev: the
	// signature of looking a host PID up in the wrong namespace.
	if err := os.Remove(filepath.Join(procRoot, "42", "net", "dev")); err != nil {
		t.Fatalf("remove net/dev: %v", err)
	}

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), false)

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

// The socket's own answer, and the whole reason it is preferred: this fixture
// has NO namespace links at all, which is what a real host looks like to an
// agent without CAP_SYS_PTRACE -- readlink on /proc/<pid>/ns/net is denied for
// a non-dumpable target even when the uids match, and no-new-privileges makes
// every target non-dumpable. The counters are still read, because
// /proc/<pid>/net/dev never needed the capability.
func TestContainersMeasuresABridgedContainerWithoutReadingNamespaces(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	// Both links gone: the guard this replaces cannot run at all now.
	if err := os.Remove(filepath.Join(procRoot, "1", "ns", "net")); err != nil {
		t.Fatalf("remove host ns link: %v", err)
	}
	if err := os.Remove(filepath.Join(procRoot, "42", "ns", "net")); err != nil {
		t.Fatalf("remove container ns link: %v", err)
	}

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web", NetworkMode: "bridge"}), true)

	containersAt(t, testee, base)
	advanceNet(t, procRoot, "42", 3000, 1500)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx == nil || row.NetTx == nil {
		t.Fatal("net_rx/net_tx unset; the socket said bridge, so no namespace read was needed")
	}
	// (3000-1000)/10 and (1500-500)/10.
	if got := row.GetNetRx(); got != 200 {
		t.Errorf("net_rx = %v, want 200", got)
	}
	if got := row.GetNetTx(); got != 100 {
		t.Errorf("net_tx = %v, want 100", got)
	}
	if got := testee.Capabilities()["container_network"]; got != "" {
		t.Errorf("capability = %q, want none -- nothing failed", got)
	}
}

// A cgroup scope the socket never listed must not speak for the whole host.
//
// It happens routinely: a container that stops between the list and the walk,
// or a scope Docker does not own at all -- podman, a bare systemd scope. If
// such a scope fell through to the namespace comparison it would fail on a
// stock install (no CAP_SYS_PTRACE), and because container_network is
// host-wide that one scope would blank the Network panel for every container
// that measured perfectly well.
func TestContainersIgnoresAScopeTheSocketDidNotList(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	// The namespace links are gone, as they effectively are without ptrace
	// access -- so the fallback path CANNOT quietly succeed and mask this.
	if err := os.Remove(filepath.Join(procRoot, "1", "ns", "net")); err != nil {
		t.Fatalf("remove host ns link: %v", err)
	}
	if err := os.Remove(filepath.Join(procRoot, "42", "ns", "net")); err != nil {
		t.Fatalf("remove container ns link: %v", err)
	}

	// The socket answers, and names a DIFFERENT container than the one the
	// walk finds.
	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{
			ID: "0000ffff", Name: "somebody-else", NetworkMode: "bridge",
		}), true)

	containersAt(t, testee, base)
	res := containersAt(t, testee, base.Add(10*time.Second))

	if got := testee.Capabilities()["container_network"]; got != "" {
		t.Errorf("capability = %q, want none -- an unlisted scope is not a fact about the host", got)
	}
	_ = res
}

// Without the host PID namespace the socket path must refuse to read at all.
//
// The danger is not that the read fails -- it is that it SUCCEEDS. cgroup.procs
// names host PIDs, and host pid 42 may well exist in the agent's own namespace
// as an unrelated process whose net/dev is the agent's own interface. That
// would be a plausible number attributed to the wrong container, which is worse
// than reporting none.
func TestContainersRefusesToReadHostPidsWithoutThePidNamespace(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	// The fixture HAS a readable /proc/42/net/dev, standing in for the
	// coincidental collision. A correct agent still must not report it.
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{
			ID: "abc123", Name: "web", NetworkMode: "bridge",
		}), false) // NETRA_PID_HOST=0

	containersAt(t, testee, base)
	advanceNet(t, procRoot, "42", 3000, 1500)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx != nil || row.NetTx != nil {
		t.Error("counters reported for a host PID resolved in the agent's own namespace")
	}
	if got := testee.Capabilities()["container_network"]; got != "namespaced" {
		t.Errorf("capability = %q, want namespaced -- and it names a remedy", got)
	}
}

// network_mode: host, on the socket's word. Its net/dev IS the host's file, and
// the Network collector already reports those bytes once.
func TestContainersSkipsAHostNetworkedContainerOnTheSocketsWord(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	// The namespaces DIFFER here, so a fixture-driven comparison would have
	// called this bridged. The socket's answer overrides it.
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web", NetworkMode: "host"}), true)

	containersAt(t, testee, base)
	advanceNet(t, procRoot, "42", 3000, 1500)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx != nil || row.NetTx != nil {
		t.Error("a host-networked container reported traffic; those bytes are the machine's")
	}
	if got := testee.Capabilities()["container_network"]; got != "" {
		t.Errorf("capability = %q, want none -- this is a deliberate skip, not a failure", got)
	}
}

// A sidecar joining a peer's namespace. The peer reports the same counters
// under its own key, so measuring both would double every byte.
func TestContainersSkipsAContainerSharingAPeersNamespace(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{
			ID: "abc123", Name: "web", NetworkMode: "container:deadbeef",
		}), true)

	containersAt(t, testee, base)
	advanceNet(t, procRoot, "42", 3000, 1500)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx != nil || row.NetTx != nil {
		t.Error("a sidecar reported its peer's traffic; the peer already reports it")
	}
}

// The other half of the pair above: the container's OWN namespace link is
// unreadable on a host that does have the PID namespace. That is the ptrace
// access check -- readlink on /proc/<pid>/ns/net needs PTRACE_MODE_READ_FSCRED,
// which a root agent passes only for a dumpable, root-owned process -- and it
// earns different words, because "re-run setup-agent.sh" would not fix it.
//
// This is the test that pins the whole reason NETRA_PID_HOST is passed in:
// nothing in the fixture distinguishes the two runs, only what the operator
// said about the container they deployed.
func TestContainersReportsNoHostNSWhenThePidNamespaceIsPresent(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	// Denied rather than absent: /proc/42 is still there, only the namespace
	// link cannot be resolved. Removing it is how a fixture spells EACCES.
	if err := os.Remove(filepath.Join(procRoot, "42", "ns", "net")); err != nil {
		t.Fatalf("remove container ns link: %v", err)
	}

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), true)

	containersAt(t, testee, base)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx != nil {
		t.Error("net_rx set with no readable namespace to classify it by")
	}
	if got := testee.Capabilities()["container_network"]; got != "no-host-netns" {
		t.Errorf("capability = %q, want no-host-netns -- with pid: host granted, the remaining cause is ptrace access", got)
	}
}

// A process that exits between the read of cgroup.procs and the read of its
// net/dev is ROUTINE, not a misconfiguration -- and it gets commoner the
// shorter-lived the container.
//
// It must not set a capability. container_network is host-wide and
// ContainerPage swaps the entire Network panel for its message, so one
// container exiting mid-scrape would hide correctly measured traffic for every
// other container on the box until the next post. Reaching net/dev at all
// means the namespace link resolved a moment earlier, so /proc DID have this
// PID: nothing here is denied, the process is simply gone.
func TestContainersReportsNoCapabilityForAProcessThatExitedMidScrape(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	// The namespace link stays: the PID was there to classify, and only its
	// counters have gone.
	if err := os.Remove(filepath.Join(procRoot, "42", "net", "dev")); err != nil {
		t.Fatalf("remove net/dev: %v", err)
	}

	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), true)

	containersAt(t, testee, base)
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "web")
	if row.NetRx != nil {
		t.Error("net_rx set with no readable net/dev")
	}
	if got := testee.Capabilities()["container_network"]; got != "" {
		t.Errorf("capability = %q, want none -- a vanished process is not a misconfiguration", got)
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
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), true)

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
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), true)

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
	testee := collector.NewContainers(t.TempDir(), noProcRoot, fakeLister(), true)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Containers) != 0 {
		t.Errorf("rows = %d, want 0", len(res.Containers))
	}
}

// The failure this collector was blind to for its entire life: the socket can
// see containers and the cgroup walk cannot.
//
// That is what an unmounted cgroup hierarchy looks like from inside the agent,
// and what every containerised agent looked like before the mount was added --
// Docker's default cgroup namespace is private, so the agent's own
// /sys/fs/cgroup holds no other container's scope. The walk found nothing,
// which is not an error, so the agent reported perfect health and zero
// containers on a host running thirty.
func TestContainersReportsNoCgroupScopesWhenTheSocketDisagrees(t *testing.T) {
	testee := collector.NewContainers(t.TempDir(), noProcRoot,
		fakeLister(
			collector.ContainerMeta{ID: "abc123", Name: "web"},
			collector.ContainerMeta{ID: "def456", Name: "db"},
		), true)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Containers) != 0 {
		t.Fatalf("rows = %d, want 0 -- there is no cgroup tree to read", len(res.Containers))
	}

	if got := testee.Capabilities()["containers"]; got != "no-cgroup-scopes" {
		t.Errorf("capability = %q, want no-cgroup-scopes", got)
	}
	if got := testee.StartupSummary(); got != "0 cgroup scopes, 2 containers named by the docker socket" {
		t.Errorf("StartupSummary = %q, want both counts side by side", got)
	}
}

// The counterpart, and the reason the capability is gated on the mismatch
// rather than on an empty walk: a host genuinely running no containers is not
// misconfigured and must not be flagged as such.
func TestContainersRaisesNoCapabilityWhenThereIsGenuinelyNothingToSee(t *testing.T) {
	testee := collector.NewContainers(t.TempDir(), noProcRoot, fakeLister(), true)

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if got, ok := testee.Capabilities()["containers"]; ok {
		t.Errorf("capability = %q, want none -- no containers is not a fault", got)
	}
}

// A cgroup root that does not exist at all is an ERROR, not an empty walk.
//
// This is what makes the default /host/sys/fs/cgroup safer than the old
// /sys/fs/cgroup: the old path always exists, so a missing mount produced
// silence, while a path that only exists when the mount was granted produces a
// logged collector failure.
func TestContainersFailsLoudlyWhenTheCgroupRootIsAbsent(t *testing.T) {
	testee := collector.NewContainers(filepath.Join(t.TempDir(), "never-mounted"),
		noProcRoot, fakeLister(), true)

	res, err := testee.Collect(context.Background())
	if err == nil {
		t.Fatal("Collect succeeded on a missing cgroup root; want an error")
	}
	if res != nil {
		t.Error("Collect returned a Result alongside an error; the contract is nil")
	}
}

// Both cgroup drivers name their directories differently, and only one of them
// had a fixture. cgroupfs uses the bare 64-hex id; systemd uses
// docker-<id>.scope, which the fixtures already cover.
func TestContainersReadsTheCgroupfsDriverBareHexDirectory(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	const id = "3f2b1c8d4e5a69708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"

	build := func(usage uint64) string {
		t.Helper()
		root := t.TempDir()
		dir := filepath.Join(root, "docker", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		write := func(name, body string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
		write("cpu.stat", fmt.Sprintf("usage_usec %d\n", usage))
		write("memory.current", "1048576\n")
		write("memory.stat", "inactive_file 0\n")
		return root
	}

	testee := collector.NewContainers(build(1000000), noProcRoot,
		fakeLister(collector.ContainerMeta{ID: id, Name: "cgroupfs-driver"}), true)

	containersAt(t, testee, base)
	testee.SetCgroupRootForTest(build(6000000))
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "cgroupfs-driver")
	if got := row.GetCpuPct(); got != 50 {
		t.Errorf("cpu_pct = %v, want 50", got)
	}
}

// Neither shape: a directory that is neither docker-<id>.scope nor 64 hex
// characters is not a container, and counting one would invent a container the
// host does not have. A 63-hex name and a same-length non-hex name are the two
// ways to miss.
func TestContainersIgnoresDirectoriesThatAreNotContainerScopes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"user.slice",
		"docker-abc123.slice", // right prefix, wrong suffix
		"3f2b1c8d4e5a69708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f",  // 63 hex
		"zzzz1c8d4e5a69708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8", // 64, not hex
	} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 1\n"), 0o644); err != nil {
			t.Fatalf("write cpu.stat: %v", err)
		}
	}

	testee := collector.NewContainers(root, noProcRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), true)

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := testee.StartupSummary(); got != "0 cgroup scopes, 1 container named by the docker socket" {
		t.Errorf("StartupSummary = %q, want 0 scopes -- none of those directories is a container", got)
	}
}

// The summary states BOTH counts even when they agree, and says plainly when
// there is no socket to compare against. It is the line that would have made an
// unmounted hierarchy obvious on day one.
func TestContainersStartupSummaryNamesWhatItSaw(t *testing.T) {
	failing := func(context.Context) ([]collector.ContainerMeta, error) {
		return nil, errors.New("dial /var/run/docker.sock: no such file")
	}
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot, failing, true)

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := testee.StartupSummary(); got != "2 cgroup scopes, docker socket unavailable" {
		t.Errorf("StartupSummary = %q, want the scope count and the socket's absence", got)
	}
}

// A REFUSED namespace link is refused for the life of the container, and the
// agent must ask exactly once.
//
// readlink on /proc/<pid>/ns/net is ptrace-gated, and Docker's default
// AppArmor profile permits ptrace only against another docker-default peer --
// so an unconfined target (a privileged container, or any host process) is
// denied on every attempt, and nothing about that can change until the agent's
// own container restarts. Asking again on the next scrape cannot succeed; it
// only writes another apparmor="DENIED" line to the HOST's kernel audit log.
// At one scrape a minute that is 1440 lines a day, per monitored host, in the
// log netra exists to help the operator read.
//
// The capability must still be reported on every scrape: the latch is about
// the syscall, not about what the UI is told.
func TestContainersAsksOnceForADeniedNamespaceLink(t *testing.T) {
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	denied := 0
	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), true)
	testee.SetReadlinkForTest(func(path string) (string, error) {
		if filepath.Base(filepath.Dir(filepath.Dir(path))) == "42" {
			denied++
			return "", fs.ErrPermission
		}
		return os.Readlink(path)
	})

	containersAt(t, testee, base)
	containersAt(t, testee, base.Add(1*time.Minute))
	res := containersAt(t, testee, base.Add(2*time.Minute))

	if denied != 1 {
		t.Errorf("readlink attempts = %d, want 1 -- a denial that repeats every scrape fills the host's audit log", denied)
	}
	if got := testee.Capabilities()["container_network"]; got != "no-host-netns" {
		t.Errorf("capability = %q, want no-host-netns -- the latch must silence the syscall, not the explanation", got)
	}
	if row := containerRow(t, res.Containers, "web"); row.NetRx != nil {
		t.Error("net_rx set with no readable namespace to classify it by")
	}
}

// The opposite case, and the reason the latch reads the errno instead of
// latching on any failure at all.
//
// A container that exits between the read of cgroup.procs and this readlink
// gives ENOENT, which is routine on a busy host and gets commoner the
// shorter-lived the container. Latching on it would let ONE short-lived
// container switch per-container networking off for every container on the
// box until the agent restarts -- a far worse bug than the log noise the latch
// exists to stop.
func TestContainersKeepsAskingWhenTheProcessMerelyExited(t *testing.T) {
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	cgroupRoot, procRoot := netFixture(t, "42", "net:[4026531992]", "net:[4026532000]", 1000, 500)

	missing := 0
	testee := collector.NewContainers(cgroupRoot, procRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Name: "web"}), true)
	testee.SetReadlinkForTest(func(path string) (string, error) {
		if filepath.Base(filepath.Dir(filepath.Dir(path))) == "42" {
			missing++
			return "", fs.ErrNotExist
		}
		return os.Readlink(path)
	})

	containersAt(t, testee, base)
	containersAt(t, testee, base.Add(1*time.Minute))
	containersAt(t, testee, base.Add(2*time.Minute))

	if missing != 3 {
		t.Errorf("readlink attempts = %d, want 3 -- an exited process must not disable the next scrape's read", missing)
	}
}
