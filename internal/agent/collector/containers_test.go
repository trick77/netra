package collector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func fakeLister(metas ...collector.ContainerMeta) collector.ContainerLister {
	return func(context.Context) ([]collector.ContainerMeta, error) { return metas, nil }
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
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", time.Minute,
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
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", time.Minute,
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
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", time.Minute,
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
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", time.Minute,
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
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", time.Minute,
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
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", time.Minute, failing)

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

// A host with no containers is the common case for a plain VPS. Absent, not
// broken.
func TestContainersReportsNothingWithNoContainers(t *testing.T) {
	testee := collector.NewContainers(t.TempDir(), time.Minute, fakeLister())

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Containers) != 0 {
		t.Errorf("rows = %d, want 0", len(res.Containers))
	}
}
