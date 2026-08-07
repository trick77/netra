package collector_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/trick77/netra/agent/internal/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// The first scrape has no previous snapshot to diff against, so it must
// report nothing rather than a fabricated value.
func TestCPUFirstCollectYieldsNoValue(t *testing.T) {
	c := collector.NewCPU("testdata/proc1", time.Minute)

	var sample netrav1.HostSample
	if err := c.Collect(context.Background(), &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if sample.CpuTotal != nil {
		t.Fatalf("CpuTotal = %v, want absent on the first scrape", *sample.CpuTotal)
	}
}

func TestCPUSecondCollectComputesDelta(t *testing.T) {
	c := collector.NewCPU("testdata/proc1", time.Minute)
	ctx := context.Background()

	var first netrav1.HostSample
	if err := c.Collect(ctx, &first); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	c.SetProcRootForTest("testdata/proc2")

	var second netrav1.HostSample
	if err := c.Collect(ctx, &second); err != nil {
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
	c := collector.NewCPU("testdata/proc2", time.Minute)
	ctx := context.Background()

	var first netrav1.HostSample
	if err := c.Collect(ctx, &first); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	c.SetProcRootForTest("testdata/proc1") // counters go backwards

	var second netrav1.HostSample
	if err := c.Collect(ctx, &second); err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if second.CpuTotal != nil {
		t.Fatalf("CpuTotal = %v, want absent after a counter reset", *second.CpuTotal)
	}
}

func TestCPUNameAndInterval(t *testing.T) {
	c := collector.NewCPU("testdata/proc1", 30*time.Second)
	if c.Name() != "cpu" {
		t.Fatalf("Name() = %q, want %q", c.Name(), "cpu")
	}
	if c.Interval() != 30*time.Second {
		t.Fatalf("Interval() = %v, want 30s", c.Interval())
	}
}
