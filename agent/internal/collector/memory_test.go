package collector_test

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/netra/agent/internal/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func TestMemoryReadsMeminfo(t *testing.T) {
	c := collector.NewMemory("testdata/proc1", time.Minute)

	var s netrav1.HostSample
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	const kb = 1024
	if got, want := s.GetMemTotal(), uint64(16_384_000*kb); got != want {
		t.Fatalf("MemTotal = %d, want %d", got, want)
	}
	if got, want := s.GetMemAvailable(), uint64(8_192_000*kb); got != want {
		t.Fatalf("MemAvailable = %d, want %d", got, want)
	}
	// Used is total minus available, which is what a human means by "used".
	if got, want := s.GetMemUsed(), uint64((16_384_000-8_192_000)*kb); got != want {
		t.Fatalf("MemUsed = %d, want %d", got, want)
	}
	if got, want := s.GetMemBuffcache(), uint64((512_000+4_096_000)*kb); got != want {
		t.Fatalf("MemBuffcache = %d, want %d", got, want)
	}
	if got, want := s.GetSwapUsed(), uint64((4_096_000-3_072_000)*kb); got != want {
		t.Fatalf("SwapUsed = %d, want %d", got, want)
	}
}

// A host with no swap must report NULL, not 0: "swap is fine" and "there is
// no swap" are different facts and an alert rule has to tell them apart.
func TestMemoryAbsentSwapIsUnset(t *testing.T) {
	c := collector.NewMemory("testdata/noswap", time.Minute)

	var s netrav1.HostSample
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if s.SwapTotal != nil {
		t.Fatalf("SwapTotal = %d, want absent when the host has no swap", *s.SwapTotal)
	}
	if s.SwapUsed != nil {
		t.Fatalf("SwapUsed = %d, want absent when the host has no swap", *s.SwapUsed)
	}
	// Memory itself is still reported.
	if s.MemTotal == nil {
		t.Fatal("MemTotal is absent, want a value")
	}
}

// ZFS ARC is only present on hosts running ZFS.
func TestMemoryAbsentZfsArcIsUnset(t *testing.T) {
	c := collector.NewMemory("testdata/proc1", time.Minute)

	var s netrav1.HostSample
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if s.MemZfsArc != nil {
		t.Fatalf("MemZfsArc = %d, want absent with no ZFS kstat", *s.MemZfsArc)
	}
}
