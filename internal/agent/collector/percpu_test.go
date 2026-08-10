package collector_test

import (
	"context"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
)

// Fixture arithmetic, per core, between first and second:
//
//	cpu0: total 500 -> 700 (delta 200), busy 100 -> 300 (delta 200) = 100%
//	cpu1: total 500 -> 900 (delta 400), busy 100 -> 100 (delta   0) =   0%
//
// The two differ on purpose: a collector that divided by the wrong total, or
// reported the aggregate line per core, would land on the same number twice.
func TestPerCoreCPUReportsPerCoreBusyPercentages(t *testing.T) {
	// Given: a collector that has taken one baseline reading.
	testee := collector.NewPerCoreCPU("testdata/percpu/first")

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	if len(res.Cores) != 0 {
		t.Fatalf("first scrape produced %d rows, want 0 -- a rate needs a baseline", len(res.Cores))
	}

	// When: the counters advance and it collects again.
	testee.SetProcRootForTest("testdata/percpu/second")
	res, err = testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	// Then: one row per core, in core order, each with its own percentage.
	if len(res.Cores) != 2 {
		t.Fatalf("cores = %d, want 2", len(res.Cores))
	}
	if got := res.Cores[0].GetCore(); got != 0 {
		t.Errorf("first row is core %d, want 0", got)
	}
	if got := res.Cores[1].GetCore(); got != 1 {
		t.Errorf("second row is core %d, want 1", got)
	}
	if got := res.Cores[0].GetBusy(); got != 100 {
		t.Errorf("core 0 busy = %v, want 100", got)
	}
	if got := res.Cores[1].GetBusy(); got != 0 {
		t.Errorf("core 1 busy = %v, want 0", got)
	}
	// An idle core reports 0 rather than nothing: 0% busy is a measurement.
	if res.Cores[1].Busy == nil {
		t.Error("core 1 Busy is nil; an idle core measured at 0% must say so")
	}
	if res.Cores[0].GetTsMs() == 0 {
		t.Error("row carries no ts_ms; a per-entity row must timestamp itself")
	}
}

// The per-core collector owns only the cpuN lines. Writing host fields too
// would collide with the CPU collector during the merge, where two collectors
// setting the same field makes the result depend on registration order.
func TestPerCoreCPUContributesNoHostFields(t *testing.T) {
	testee := collector.NewPerCoreCPU("testdata/percpu/first")
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	testee.SetProcRootForTest("testdata/percpu/second")

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Host != nil {
		t.Errorf("Host = %+v, want nil -- the aggregate line belongs to the CPU collector", res.Host)
	}
}

// A core that vanishes between scrapes (offlined, or hotplugged out) must not
// produce a row derived from a baseline that no longer describes anything.
func TestPerCoreCPUSkipsCoresMissingFromTheCurrentRead(t *testing.T) {
	testee := collector.NewPerCoreCPU("testdata/percpu/second")
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	testee.SetProcRootForTest("testdata/percpu/onecore")
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	for _, c := range res.Cores {
		if c.GetCore() == 1 {
			t.Error("core 1 reported after it vanished from /proc/stat")
		}
	}
	if len(res.Cores) != 1 {
		t.Fatalf("cores = %d, want 1 -- only the core still present", len(res.Cores))
	}
}

// A core that appears for the first time has no previous reading, so there is
// no interval to compute a percentage over. It is skipped this scrape and
// reported on the next one.
func TestPerCoreCPUSkipsNewlyAppearedCores(t *testing.T) {
	testee := collector.NewPerCoreCPU("testdata/percpu/onecore")
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	// second has cpu0 and cpu1; only cpu0 has a baseline. cpu0's counters also
	// go backwards from onecore, which is the reset path, so the useful
	// assertion here is simply that the unbaselined core produces nothing.
	testee.SetProcRootForTest("testdata/percpu/second")
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, c := range res.Cores {
		if c.GetCore() == 1 {
			t.Error("core 1 reported on the scrape it first appeared; it has no baseline")
		}
	}
}

// An unreadable /proc/stat is an error, not an empty result: the caller must
// be able to tell "no cores on this host" from "could not read the file".
func TestPerCoreCPUReportsAnUnreadableProcStat(t *testing.T) {
	testee := collector.NewPerCoreCPU(t.TempDir())

	res, err := testee.Collect(context.Background())
	if err == nil {
		t.Fatal("Collect succeeded with no /proc/stat, want an error")
	}
	if res != nil {
		t.Errorf("Collect returned %+v alongside an error; want nil", res)
	}
}
