package collector_test

import (
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// fixedClock returns a clock that advances by step on every call after the
// first, so a two-scrape test controls elapsed time exactly instead of
// depending on how fast the test machine is.
func fixedClock(start time.Time, step time.Duration) func() time.Time {
	calls := 0
	return func() time.Time {
		t := start.Add(time.Duration(calls) * step)
		calls++
		return t
	}
}

func newKernelStat(t *testing.T, root string, clock func() time.Time) *collector.KernelStat {
	t.Helper()
	k := collector.NewKernelStat(root)
	if clock != nil {
		k.SetClockForTest(clock)
	}
	return k
}

// Gauges describe an instant and need no baseline, so they must be reported
// on the very first scrape -- unlike the rates alongside them. Getting this
// backwards would mean a host that just started reports no runnable-task
// count for a full minute.
func TestKernelStatFirstCollectEmitsGaugesButNoRates(t *testing.T) {
	k := newKernelStat(t, "testdata/proc1", fixedClock(time.Unix(1000, 0), time.Minute))

	var sample netrav1.HostSample
	if err := collectInto(k, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.ProcsRunning == nil || *sample.ProcsRunning != 2 {
		t.Errorf("ProcsRunning = %v, want 2", sample.ProcsRunning)
	}
	if sample.ProcsBlocked == nil || *sample.ProcsBlocked != 0 {
		t.Errorf("ProcsBlocked = %v, want 0", sample.ProcsBlocked)
	}
	if sample.BootTimeS == nil || *sample.BootTimeS != 1700000000 {
		t.Errorf("BootTimeS = %v, want 1700000000", sample.BootTimeS)
	}

	if sample.CtxtPerS != nil {
		t.Errorf("CtxtPerS = %v, want nil on the first scrape", *sample.CtxtPerS)
	}
	if sample.IntrPerS != nil {
		t.Errorf("IntrPerS = %v, want nil on the first scrape", *sample.IntrPerS)
	}
	if sample.ForksPerS != nil {
		t.Errorf("ForksPerS = %v, want nil on the first scrape", *sample.ForksPerS)
	}
}

func TestKernelStatSecondCollectComputesRates(t *testing.T) {
	k := newKernelStat(t, "testdata/proc1", fixedClock(time.Unix(1000, 0), time.Minute))

	var first netrav1.HostSample
	if err := collectInto(k, &first); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	k.SetProcRootForTest("testdata/proc2")

	var sample netrav1.HostSample
	if err := collectInto(k, &sample); err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	// proc1 -> proc2 over exactly 60s:
	//   ctxt      67890 -> 77890  =>  10000/60 = 166.66...
	//   intr      12345 -> 22345  =>  10000/60 = 166.66...
	//   processes  4500 ->  4560  =>     60/60 = 1
	const want = 10000.0 / 60.0

	if sample.CtxtPerS == nil || *sample.CtxtPerS != want {
		t.Errorf("CtxtPerS = %v, want %v", sample.CtxtPerS, want)
	}
	if sample.IntrPerS == nil || *sample.IntrPerS != want {
		t.Errorf("IntrPerS = %v, want %v", sample.IntrPerS, want)
	}
	if sample.ForksPerS == nil || *sample.ForksPerS != 1 {
		t.Errorf("ForksPerS = %v, want 1", sample.ForksPerS)
	}

	// The gauges track the second fixture, not the first.
	if sample.ProcsRunning == nil || *sample.ProcsRunning != 5 {
		t.Errorf("ProcsRunning = %v, want 5", sample.ProcsRunning)
	}
	if sample.ProcsBlocked == nil || *sample.ProcsBlocked != 1 {
		t.Errorf("ProcsBlocked = %v, want 1", sample.ProcsBlocked)
	}
}

// Counters reset to zero on reboot. A naive delta would report a hugely
// negative rate, or wrap into an enormous positive one. The gauges are still
// valid across that boundary and must survive it.
func TestKernelStatCounterResetProducesNoRatesButKeepsGauges(t *testing.T) {
	k := newKernelStat(t, "testdata/proc2", fixedClock(time.Unix(1000, 0), time.Minute))

	var first netrav1.HostSample
	if err := collectInto(k, &first); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	// Going back to the lower fixture is what a reboot looks like.
	k.SetProcRootForTest("testdata/proc1")

	var sample netrav1.HostSample
	if err := collectInto(k, &sample); err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	if sample.CtxtPerS != nil {
		t.Errorf("CtxtPerS = %v, want nil after a counter reset", *sample.CtxtPerS)
	}
	if sample.IntrPerS != nil {
		t.Errorf("IntrPerS = %v, want nil after a counter reset", *sample.IntrPerS)
	}
	if sample.ForksPerS != nil {
		t.Errorf("ForksPerS = %v, want nil after a counter reset", *sample.ForksPerS)
	}

	if sample.BootTimeS == nil {
		t.Error("BootTimeS = nil, want it still reported across a reset")
	}
	if sample.ProcsRunning == nil {
		t.Error("ProcsRunning = nil, want it still reported across a reset")
	}
}

// A clock that does not advance would divide by zero. A clock that steps
// backwards (NTP correction, a suspended VM resuming) would produce a
// negative rate. Both must yield no measurement.
func TestKernelStatNonAdvancingClockProducesNoRates(t *testing.T) {
	for _, tc := range []struct {
		name string
		step time.Duration
	}{
		{"stopped", 0},
		{"stepped backwards", -30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newKernelStat(t, "testdata/proc1", fixedClock(time.Unix(1000, 0), tc.step))

			var first netrav1.HostSample
			if err := collectInto(k, &first); err != nil {
				t.Fatalf("first Collect: %v", err)
			}

			k.SetProcRootForTest("testdata/proc2")

			var sample netrav1.HostSample
			if err := collectInto(k, &sample); err != nil {
				t.Fatalf("second Collect: %v", err)
			}

			if sample.CtxtPerS != nil {
				t.Errorf("CtxtPerS = %v, want nil", *sample.CtxtPerS)
			}
			if sample.BootTimeS == nil {
				t.Error("BootTimeS = nil, want the gauges reported regardless")
			}
		})
	}
}

// An older kernel omits procs_blocked, and a container's /proc may omit more.
// A missing line is an absent fact, not a parse failure, and above all it must
// not read as zero.
func TestKernelStatMissingLinesStayUnsetWithoutError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/stat", "cpu  1 2 3 4 5 6 7 8 0 0\nctxt 500\n")

	k := newKernelStat(t, dir, fixedClock(time.Unix(1000, 0), time.Minute))

	var sample netrav1.HostSample
	if err := collectInto(k, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.ProcsRunning != nil {
		t.Errorf("ProcsRunning = %v, want nil when the line is absent", *sample.ProcsRunning)
	}
	if sample.BootTimeS != nil {
		t.Errorf("BootTimeS = %v, want nil when the line is absent", *sample.BootTimeS)
	}
}

// A counter that only appears on the second scrape has no baseline, so its
// first delta would be the entire since-boot total attributed to one
// interval. Absence must not be read as a zero baseline.
func TestKernelStatCounterAppearingLateProducesNoRate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/stat", "cpu  1 2 3 4 5 6 7 8 0 0\nintr 900\n")

	k := newKernelStat(t, dir, fixedClock(time.Unix(1000, 0), time.Minute))

	var first netrav1.HostSample
	if err := collectInto(k, &first); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	// ctxt shows up only now, with a large since-boot value.
	writeFile(t, dir+"/stat", "cpu  1 2 3 4 5 6 7 8 0 0\nintr 960\nctxt 5000000\n")

	var sample netrav1.HostSample
	if err := collectInto(k, &sample); err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	if sample.CtxtPerS != nil {
		t.Errorf("CtxtPerS = %v, want nil without a baseline", *sample.CtxtPerS)
	}
	if sample.IntrPerS == nil || *sample.IntrPerS != 1 {
		t.Errorf("IntrPerS = %v, want 1 -- the counter that did have a baseline", sample.IntrPerS)
	}
}

func TestKernelStatName(t *testing.T) {
	k := collector.NewKernelStat("testdata/proc1")

	if got := k.Name(); got != "kernelstat" {
		t.Errorf("Name() = %q, want %q", got, "kernelstat")
	}
}

func TestKernelStatMissingProcRootIsAnError(t *testing.T) {
	k := collector.NewKernelStat(t.TempDir())

	var sample netrav1.HostSample
	if err := collectInto(k, &sample); err == nil {
		t.Fatal("Collect() = nil, want an error when /proc/stat cannot be opened")
	}
}
