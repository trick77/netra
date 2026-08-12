package collector_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
)

func writeVmstat(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "vmstat"), []byte(body), 0o644); err != nil {
		t.Fatalf("write vmstat: %v", err)
	}
	return root
}

func vmstatAt(t *testing.T, c *collector.VMStat, at time.Time) *collector.Result {
	t.Helper()
	c.SetClockForTest(func() time.Time { return at })
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return res
}

// The counters are monotonic since boot, so the first scrape has no interval
// to rate over. Reporting one would attribute the entire since-boot total to
// a single minute.
func TestVMStatReportsRatesFromTheSecondScrape(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	first := writeVmstat(t, "pgmajfault 1000\npswpin 20\npswpout 40\noom_kill 2\n")

	testee := collector.NewVMStat(first)

	res := vmstatAt(t, testee, base)
	if res.Host.PgmajfaultPerS != nil {
		t.Error("a rate was reported on the first scrape, with no baseline to measure against")
	}

	second := writeVmstat(t, "pgmajfault 1100\npswpin 25\npswpout 40\noom_kill 2\n")
	testee.SetProcRootForTest(second)
	res = vmstatAt(t, testee, base.Add(10*time.Second))

	// 1000 -> 1100 over 10s = 10/s.
	if got := res.Host.GetPgmajfaultPerS(); got != 10 {
		t.Errorf("pgmajfault_per_s = %v, want 10", got)
	}
	if got := res.Host.GetPswpinPerS(); got != 0.5 {
		t.Errorf("pswpin_per_s = %v, want 0.5", got)
	}
	// Unchanged counter is a real zero, not an absence.
	if res.Host.PswpoutPerS == nil {
		t.Error("pswpout_per_s unset; an unchanged counter is a measured 0")
	}
}

// "Has this host ever OOM-killed" is answerable without a baseline, and is
// worth answering on the very first scrape.
func TestVMStatReportsOomKillTotalImmediately(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewVMStat(writeVmstat(t, "pgmajfault 1\noom_kill 7\n"))

	res := vmstatAt(t, testee, base)

	if got := res.Host.GetOomKillTotal(); got != 7 {
		t.Errorf("oom_kill_total = %d, want 7 on the first scrape", got)
	}
}

// oom_kill is absent before kernel 4.13 and inside some containers. A kernel
// that does not publish it must not read as a host that has never OOM-killed.
func TestVMStatLeavesOomKillUnsetWhenTheKernelOmitsIt(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewVMStat(writeVmstat(t, "pgmajfault 1\npswpin 0\n"))

	res := vmstatAt(t, testee, base)

	if res.Host.OomKillTotal != nil {
		t.Errorf("oom_kill_total = %d with no oom_kill line; want unset",
			res.Host.GetOomKillTotal())
	}
}

// A counter that went backwards means a reboot. No rate rather than a
// negative one, and each counter is judged on its own.
func TestVMStatReportsNoRateAfterACounterReset(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewVMStat(writeVmstat(t, "pgmajfault 1000\npswpin 20\n"))

	vmstatAt(t, testee, base)
	testee.SetProcRootForTest(writeVmstat(t, "pgmajfault 5\npswpin 30\n"))
	res := vmstatAt(t, testee, base.Add(10*time.Second))

	if res.Host.PgmajfaultPerS != nil {
		t.Errorf("pgmajfault_per_s = %v after a reset; want unset", res.Host.GetPgmajfaultPerS())
	}
	// The counter that did not reset still reports.
	if got := res.Host.GetPswpinPerS(); got != 1 {
		t.Errorf("pswpin_per_s = %v, want 1 -- one counter's reset must not cost the others", got)
	}
}
