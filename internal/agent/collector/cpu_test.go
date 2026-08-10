package collector_test

import (
	"math"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// The first scrape has no previous snapshot to diff against, so it must
// report nothing rather than a fabricated value.
func TestCPUFirstCollectYieldsNoValue(t *testing.T) {
	c := collector.NewCPU("testdata/proc1")

	var sample netrav1.HostSample
	if err := collectInto(c, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if sample.CpuTotal != nil {
		t.Fatalf("CpuTotal = %v, want absent on the first scrape", *sample.CpuTotal)
	}
}

func TestCPUSecondCollectComputesDelta(t *testing.T) {
	c := collector.NewCPU("testdata/proc1")

	var first netrav1.HostSample
	if err := collectInto(c, &first); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	c.SetProcRootForTest("testdata/proc2")

	var second netrav1.HostSample
	if err := collectInto(c, &second); err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	if second.CpuTotal == nil {
		t.Fatal("CpuTotal is absent, want a computed value")
	}
	const want = 160.0 / 580.0 * 100.0
	if math.Abs(*second.CpuTotal-want) > 0.001 {
		t.Fatalf("CpuTotal = %v, want %v", *second.CpuTotal, want)
	}

	if second.CpuIowait == nil {
		t.Fatal("CpuIowait is absent, want a computed value")
	}
	const wantIowait = 20.0 / 580.0 * 100.0
	if math.Abs(*second.CpuIowait-wantIowait) > 0.001 {
		t.Fatalf("CpuIowait = %v, want %v", *second.CpuIowait, wantIowait)
	}
}

// Counters reset to zero on reboot. A naive delta would produce a negative
// or an enormous spike; the collector must emit nothing instead.
func TestCPUCounterResetProducesNoValue(t *testing.T) {
	c := collector.NewCPU("testdata/proc2")

	var first netrav1.HostSample
	if err := collectInto(c, &first); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	c.SetProcRootForTest("testdata/proc1") // counters go backwards

	var second netrav1.HostSample
	if err := collectInto(c, &second); err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if second.CpuTotal != nil {
		t.Fatalf("CpuTotal = %v, want absent after a counter reset", *second.CpuTotal)
	}
}

func TestCPUName(t *testing.T) {
	c := collector.NewCPU("testdata/proc1")
	if c.Name() != "cpu" {
		t.Fatalf("Name() = %q, want %q", c.Name(), "cpu")
	}
}

// steal arrived in 2.6.11 and some emulated /proc trees still omit it. A
// missing TRAILING counter is genuinely zero, not a malformed line -- the
// kernel does not skip columns in the middle -- so the line must be read
// rather than rejected, leaving the host with no CPU utilisation at all.
func TestCPUReadsALineWithoutTheStealColumn(t *testing.T) {
	// Given: a /proc/stat whose cpu line stops at softirq.
	c := collector.NewCPU("testdata/percpu/shortline-first")

	var first netrav1.HostSample
	if err := collectInto(c, &first); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	// When: a second scrape gives it an interval.
	c.SetProcRootForTest("testdata/percpu/shortline-second")
	var second netrav1.HostSample
	if err := collectInto(c, &second); err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	// Then: utilisation is reported, with steal read as the zero it is.
	if second.CpuTotal == nil {
		t.Fatal("CpuTotal is unset; a cpu line without steal is readable, not malformed")
	}
	if second.CpuSteal == nil || second.GetCpuSteal() != 0 {
		t.Errorf("CpuSteal = %v, want 0 -- an absent trailing counter has not moved",
			second.CpuSteal)
	}
}
