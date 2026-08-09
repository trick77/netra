package collector_test

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func procsAt(t *testing.T, c *collector.Processes, at time.Time) *collector.Result {
	t.Helper()
	c.SetClockForTest(func() time.Time { return at })
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return res
}

func procRow(t *testing.T, rows []*netrav1.ProcessSample, name string) *netrav1.ProcessSample {
	t.Helper()
	for _, r := range rows {
		if r.GetName() == name {
			return r
		}
	}
	t.Fatalf("no row for %q in %d rows", name, len(rows))
	return nil
}

// Fixture arithmetic over ten seconds at 100 Hz:
//
//	postgres utime+stime 1500 -> 2500 = 1000 jiffies = 10s CPU = 100%
//	nginx    utime+stime  300 ->  450 =  150 jiffies = 1.5s CPU = 15%
//	postgres rss 1000 pages -> 1200 pages = 4915200 bytes
//
// The two percentages differ so a collector dividing by the wrong quantity
// lands on a number the test is not looking for.
func TestProcessesComputesCPUAndMemoryPerName(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewProcesses("testdata/processes/first", true, time.Minute)

	res := procsAt(t, testee, base)
	if len(res.Processes) != 0 {
		t.Fatalf("first scrape produced %d rows, want 0 -- CPU is a delta", len(res.Processes))
	}

	testee.SetProcRootForTest("testdata/processes/second")
	res = procsAt(t, testee, base.Add(10*time.Second))

	pg := procRow(t, res.Processes, "postgres")
	if got := pg.GetCpuPct(); got != 100 {
		t.Errorf("postgres cpu_pct = %v, want 100", got)
	}
	if got := pg.GetMemBytes(); got != 1200*4096 {
		t.Errorf("postgres mem_bytes = %d, want %d", got, 1200*4096)
	}
	if got := pg.GetCount(); got != 1 {
		t.Errorf("postgres count = %d, want 1", got)
	}

	if got := procRow(t, res.Processes, "nginx").GetCpuPct(); got != 15 {
		t.Errorf("nginx cpu_pct = %v, want 15", got)
	}
	if pg.GetTsMs() == 0 {
		t.Error("row carries no ts_ms")
	}
}

// A recycled PID must not produce a garbage spike.
//
// PIDs wrap. When they do, the new process's total CPU minus the old one's is
// attributed to whatever the new process happens to be called -- a number that
// can be enormous, and that an operator would chase. Keying the delta on
// (pid, starttime) makes the reuse visible: starttime differs, so there is no
// previous reading for this process and it simply has no rate yet.
func TestProcessesTreatsARecycledPIDAsANewProcess(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewProcesses("testdata/processes/first", true, time.Minute)
	procsAt(t, testee, base)

	// PID 100 is now python3 with a different starttime: the kernel reused the
	// number for an unrelated process.
	testee.SetProcRootForTest("testdata/processes/recycled")
	res := procsAt(t, testee, base.Add(10*time.Second))

	// python3 IS reported -- its memory and count are real, measured this
	// instant. What must not appear is a CPU figure: the whole hazard of a
	// recycled PID is that the new process's total minus the old one's gets
	// attributed to it, and that number can be enormous.
	for _, r := range res.Processes {
		if r.GetName() != "python3" {
			continue
		}
		if r.CpuPct != nil {
			t.Errorf("python3 cpu_pct = %v on the scrape its PID was reused; a recycled PID has no baseline and must report no rate",
				r.GetCpuPct())
		}
		if r.GetMemBytes() == 0 {
			t.Error("python3 mem_bytes = 0; the process occupies memory whether or not its CPU is measurable")
		}
	}

	// nginx kept its PID and starttime, so it still reports normally -- the
	// recycled neighbour must not cost it its own reading.
	if got := procRow(t, res.Processes, "nginx").GetCpuPct(); got != 15 {
		t.Errorf("nginx cpu_pct = %v, want 15", got)
	}
}

// Processes are aggregated BY NAME: twenty nginx workers are one row whose
// count is twenty, not twenty rows. Per-PID rows would be unbounded
// cardinality for data whose PIDs are meaningless a minute later.
func TestProcessesAggregatesByName(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewProcesses("testdata/processes/multi/first", true, time.Minute)
	procsAt(t, testee, base)

	testee.SetProcRootForTest("testdata/processes/multi/second")
	res := procsAt(t, testee, base.Add(10*time.Second))

	nginx := procRow(t, res.Processes, "nginx")
	if got := nginx.GetCount(); got != 3 {
		t.Errorf("nginx count = %d, want 3 -- workers aggregate into one row", got)
	}
	// 100 + 50 + 25 jiffies over 10s at 100Hz = 1.75s = 17.5%
	if got := nginx.GetCpuPct(); got != 17.5 {
		t.Errorf("nginx cpu_pct = %v, want 17.5 (the sum across workers)", got)
	}
	for _, r := range res.Processes {
		if r.GetName() == "nginx" && r.GetCount() == 1 {
			t.Error("nginx reported per-PID; processes must aggregate by name")
		}
	}
}

// Without pid: host the agent sees only its own PID namespace, so the numbers
// would describe the container rather than the host. That is wrong data rather
// than missing data, so it reports a capability and no rows.
func TestProcessesReportsNamespacedWithoutPidHost(t *testing.T) {
	testee := collector.NewProcesses("testdata/processes/first", false, time.Minute)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Processes) != 0 {
		t.Errorf("rows = %d, want 0 without pid: host", len(res.Processes))
	}
	if got := testee.Capabilities()["processes"]; got != "namespaced" {
		t.Errorf("capability = %q, want namespaced", got)
	}
}

// A process that exits between the directory listing and the read is normal at
// any moment on a busy host. It must be skipped, not fail the whole scrape.
func TestProcessesSkipsAProcessThatVanishes(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewProcesses("testdata/processes/second", true, time.Minute)
	procsAt(t, testee, base)

	// The first tree has both PIDs; the recycled tree drops none, so use a
	// tree with one fewer process to stand in for the exit.
	testee.SetProcRootForTest("testdata/processes/gone")
	res := procsAt(t, testee, base.Add(10*time.Second))

	for _, r := range res.Processes {
		if r.GetName() == "postgres" {
			t.Error("postgres reported after it exited")
		}
	}
	if len(res.Processes) != 1 {
		t.Errorf("rows = %d, want 1 (only nginx remains)", len(res.Processes))
	}
}

// Memory and count are GAUGES: both are true of this instant and need no
// previous reading.
//
// Skipping them alongside the CPU delta made a churn-heavy host -- a CI
// runner, anything cron-driven -- systematically under-report how much memory
// its processes held and how many there were, because every process started
// since the last scrape contributed nothing at all.
func TestProcessesCountsMemoryOfProcessesWithNoBaseline(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewProcesses("testdata/processes/gone", true, time.Minute)
	procsAt(t, testee, base)

	// The second tree adds postgres, which the first scrape never saw.
	testee.SetProcRootForTest("testdata/processes/second")
	res := procsAt(t, testee, base.Add(10*time.Second))

	pg := procRow(t, res.Processes, "postgres")
	if got := pg.GetMemBytes(); got != 1200*4096 {
		t.Errorf("postgres mem_bytes = %d, want %d -- a new process still occupies memory", got, 1200*4096)
	}
	if got := pg.GetCount(); got != 1 {
		t.Errorf("postgres count = %d, want 1 -- a new process is still a process", got)
	}

	// ...but its CPU is not measurable yet, and must stay UNSET rather than
	// being reported as 0%, which would read as a genuinely idle process.
	if pg.CpuPct != nil {
		t.Errorf("postgres cpu_pct = %v with no baseline; want unset", pg.GetCpuPct())
	}
}
