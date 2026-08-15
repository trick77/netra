package collector_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// cpu_pct says how much CPU a container took; it cannot say what for. A
// service pinned in system time is doing syscalls or contending on the
// kernel, which is a different problem from one pinned in user time -- and
// the difference decides where an operator looks next.
func TestContainersSplitsCPUIntoUserAndSystem(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Project: "proj", Service: "web"}))

	containersAt(t, testee, base)
	testee.SetCgroupRootForTest("testdata/cgroup/second/sys/fs/cgroup")
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "proj/web")
	// user_usec 600000 -> 3600000 over 10s = 30%.
	if got := row.GetCpuUser(); got != 30 {
		t.Errorf("cpu_user = %v, want 30", got)
	}
	// system_usec 400000 -> 2400000 over 10s = 20%.
	if got := row.GetCpuSystem(); got != 20 {
		t.Errorf("cpu_system = %v, want 20", got)
	}
	// The parts are a split of the whole, not a separate measurement: a
	// chart stacking them against cpu_pct must not overshoot it.
	if got := row.GetCpuUser() + row.GetCpuSystem(); got != row.GetCpuPct() {
		t.Errorf("user + system = %v, want cpu_pct %v", got, row.GetCpuPct())
	}
}

// A kernel that does not report the split is not a container that spent no
// time in user space. Zero would be a reading; absent is the truth.
func TestContainersLeavesTheCPUSplitUnsetWhenTheKernelOmitsIt(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot,
		fakeLister(collector.ContainerMeta{ID: "def456", Project: "proj", Service: "db"}))

	containersAt(t, testee, base)
	testee.SetCgroupRootForTest("testdata/cgroup/second/sys/fs/cgroup")
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "proj/db")
	if row.CpuUser != nil {
		t.Errorf("cpu_user = %v, want absent with no user_usec line", *row.CpuUser)
	}
	if row.CpuSystem != nil {
		t.Errorf("cpu_system = %v, want absent with no system_usec line", *row.CpuSystem)
	}
	// The total still reports: usage_usec is there.
	if row.CpuPct == nil {
		t.Error("cpu_pct is absent, want the total even without the split")
	}
}

// mem_used is one number that cannot tell a container holding 2 GB of heap
// from one that read 2 GB of files. These are memory.stat's own parts, and
// they answer whether a limit needs raising or a workload needs fixing.
func TestContainersReportsTheMemoryBreakdown(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Project: "proj", Service: "web"}))

	containersAt(t, testee, base)
	testee.SetCgroupRootForTest("testdata/cgroup/second/sys/fs/cgroup")
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "proj/web")
	if got, want := row.GetMemAnon(), uint64(268435456); got != want {
		t.Errorf("mem_anon = %d, want %d", got, want)
	}
	if got, want := row.GetMemShmem(), uint64(67108864); got != want {
		t.Errorf("mem_shmem = %d, want %d", got, want)
	}
	if got, want := row.GetMemKernel(), uint64(1048576); got != want {
		t.Errorf("mem_kernel = %d, want %d", got, want)
	}
	// file MINUS shmem: the kernel counts shmem inside file, and a chart
	// stacking the two verbatim draws the same pages twice.
	if got, want := row.GetMemFile(), uint64(805306368-67108864); got != want {
		t.Errorf("mem_file = %d, want %d (file minus shmem)", got, want)
	}
	if row.GetMemFile() == 805306368 {
		t.Error("mem_file is raw file; shmem must be subtracted")
	}
}

// shmem is absent on this container's fixture, which is not the same as a
// container with no tmpfs -- but file must still be reported whole rather
// than silently losing a subtraction it could not make.
func TestContainersKeepsFileWholeWithoutAShmemLine(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup", noProcRoot,
		fakeLister(collector.ContainerMeta{ID: "def456", Project: "proj", Service: "db"}))

	containersAt(t, testee, base)
	testee.SetCgroupRootForTest("testdata/cgroup/second/sys/fs/cgroup")
	res := containersAt(t, testee, base.Add(10*time.Second))

	row := containerRow(t, res.Containers, "proj/db")
	if got := row.GetMemShmem(); got != 0 {
		t.Errorf("mem_shmem = %d, want 0 with no shmem line", got)
	}
	if got := row.GetMemAnon(); got != 104857600 {
		t.Errorf("mem_anon = %d, want 104857600", got)
	}
}

// memStatFixture builds a one-container cgroup tree whose memory.stat is
// exactly the body given, so a test can say "this kernel has no slab line"
// rather than "this kernel has a slab line reading zero". Built in a TempDir
// because the point of each of these is one line's ABSENCE, and a checked-in
// fixture per missing line would be a directory of near-identical trees.
func memStatFixture(t *testing.T, memStat string) string {
	t.Helper()

	root := t.TempDir()
	scope := filepath.Join(root, "system.slice", "docker-abc123.scope")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatalf("mkdir scope: %v", err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(scope, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("cpu.stat", "usage_usec 1000000\n")
	write("memory.current", "1000\n")
	write("memory.stat", memStat)
	write("memory.max", "max\n")
	write("io.stat", "8:0 rbytes=0 wbytes=0\n")
	write("cgroup.procs", "\n")
	return root
}

// breakdownOf scrapes the fixture twice -- the collector needs a previous
// reading before it emits a row at all -- and returns the container's row.
func breakdownOf(t *testing.T, memStat string) *netrav1.ContainerSample {
	t.Helper()

	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	root := memStatFixture(t, memStat)
	testee := collector.NewContainers(root, noProcRoot,
		fakeLister(collector.ContainerMeta{ID: "abc123", Project: "proj", Service: "web"}))

	containersAt(t, testee, base)
	res := containersAt(t, testee, base.Add(10*time.Second))
	return containerRow(t, res.Containers, "proj/web")
}

// A kernel with no `slab` line is not a container holding zero bytes of
// kernel slab -- and the difference is not academic: it is exactly the
// container being OOM-killed BY kernel slab whose chart would assert it was
// using none. The memory fields were read with readKeyedUint (missing -> 0)
// while cpu_user and cpu_system, eight lines below, already used
// lookupKeyedUint for precisely this reason.
func TestContainersLeavesAbsentMemoryStatKeysUnset(t *testing.T) {
	// A kernel reporting only anon: no file, no shmem, no slab.
	row := breakdownOf(t, "anon 104857600\ninactive_file 0\n")

	if row.MemKernel != nil {
		t.Errorf("mem_kernel = %d, want absent with no slab line", *row.MemKernel)
	}
	if row.MemShmem != nil {
		t.Errorf("mem_shmem = %d, want absent with no shmem line", *row.MemShmem)
	}
	if row.MemFile != nil {
		t.Errorf("mem_file = %d, want absent with no file line", *row.MemFile)
	}
	// anon was reported, so it is a reading and must survive.
	if got := row.GetMemAnon(); got != 104857600 {
		t.Errorf("mem_anon = %d, want 104857600", got)
	}
}

// No `shmem` line means there is no shmem for the kernel to have counted
// inside `file`, so file is whole -- the same rule memory.go follows for the
// host's Cached (TestMemoryAbsentShmemLeavesCachedWhole). Absent must not
// cost the subtrahend AND the minuend.
func TestContainersKeepsFileWholeWhenOnlyShmemIsAbsent(t *testing.T) {
	row := breakdownOf(t, "anon 104857600\nfile 805306368\ninactive_file 0\n")

	if got, want := row.GetMemFile(), uint64(805306368); got != want {
		t.Errorf("mem_file = %d, want %d whole with no shmem line", got, want)
	}
	if row.MemShmem != nil {
		t.Errorf("mem_shmem = %d, want absent with no shmem line", *row.MemShmem)
	}
}

// A `file` smaller than its own `shmem` is a kernel disagreeing with itself
// across two lines. The subtraction cannot be made, and the else-branch of
// the guard used to leave mem_file at its zero value -- a measurement of zero
// page cache that nobody took, drawn by the chart as one.
func TestContainersLeavesFileUnsetWhenItIsSmallerThanShmem(t *testing.T) {
	row := breakdownOf(t, "anon 104857600\nfile 1048576\nshmem 67108864\ninactive_file 0\n")

	if row.MemFile != nil {
		t.Errorf("mem_file = %d, want absent when file < shmem", *row.MemFile)
	}
	// shmem itself was reported and stands on its own.
	if got, want := row.GetMemShmem(), uint64(67108864); got != want {
		t.Errorf("mem_shmem = %d, want %d", got, want)
	}
}
