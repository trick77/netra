package collector_test

import (
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
)

// cpu_pct says how much CPU a container took; it cannot say what for. A
// service pinned in system time is doing syscalls or contending on the
// kernel, which is a different problem from one pinned in user time -- and
// the difference decides where an operator looks next.
func TestContainersSplitsCPUIntoUserAndSystem(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup",
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
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup",
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
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup",
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
	testee := collector.NewContainers("testdata/cgroup/first/sys/fs/cgroup",
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
