package collector_test

import (
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

func TestMemoryReadsMeminfo(t *testing.T) {
	c := collector.NewMemory("testdata/proc1")

	var s netrav1.HostSample
	if err := collectInto(c, &s); err != nil {
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
	if got, want := s.GetMemFree(), uint64(2_048_000*kb); got != want {
		t.Fatalf("MemFree = %d, want %d", got, want)
	}
	if got, want := s.GetMemBuffers(), uint64(512_000*kb); got != want {
		t.Fatalf("MemBuffers = %d, want %d", got, want)
	}
	if got, want := s.GetMemShared(), uint64(256_000*kb); got != want {
		t.Fatalf("MemShared = %d, want %d", got, want)
	}
	if got, want := s.GetMemSreclaimable(), uint64(384_000*kb); got != want {
		t.Fatalf("MemSreclaimable = %d, want %d", got, want)
	}
	// Cached MINUS Shmem. /proc/meminfo counts tmpfs pages in both, and a
	// chart that stacks the two verbatim draws those bytes twice.
	if got, want := s.GetMemCached(), uint64((4_096_000-256_000)*kb); got != want {
		t.Fatalf("MemCached = %d, want %d (Cached minus Shmem)", got, want)
	}
	if s.GetMemCached() == uint64(4_096_000*kb) {
		t.Error("MemCached is raw Cached; Shmem must be subtracted")
	}
}

// The invariant the stacked memory chart is built on: splitting buffcache
// into three parts must not change what it sums to, or the old single band
// and the new stack would disagree about the same host.
func TestMemoryBuffcacheEqualsItsThreeParts(t *testing.T) {
	c := collector.NewMemory("testdata/proc1")

	var s netrav1.HostSample
	if err := collectInto(c, &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	parts := s.GetMemBuffers() + s.GetMemCached() + s.GetMemShared()
	if got := s.GetMemBuffcache(); got != parts {
		t.Errorf("MemBuffcache = %d, want %d (buffers + cached + shared)", got, parts)
	}
}

// A kernel too old to report Shmem still reports a cache. The field must be
// absent rather than zero, and mem_cached must then be the whole of Cached
// rather than silently losing the subtraction.
func TestMemoryAbsentShmemLeavesCachedWhole(t *testing.T) {
	c := collector.NewMemory("testdata/noswap")

	var s netrav1.HostSample
	if err := collectInto(c, &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if s.MemShared != nil {
		t.Fatalf("MemShared = %d, want absent with no Shmem line", *s.MemShared)
	}
	if s.MemSreclaimable != nil {
		t.Fatalf("MemSreclaimable = %d, want absent with no SReclaimable line", *s.MemSreclaimable)
	}
	const kb = 1024
	if got, want := s.GetMemCached(), uint64(4_096_000*kb); got != want {
		t.Errorf("MemCached = %d, want %d (all of Cached when Shmem is absent)", got, want)
	}
}

// A host with no swap must report NULL, not 0: "swap is fine" and "there is
// no swap" are different facts and an alert rule has to tell them apart.
func TestMemoryAbsentSwapIsUnset(t *testing.T) {
	c := collector.NewMemory("testdata/noswap")

	var s netrav1.HostSample
	if err := collectInto(c, &s); err != nil {
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
	c := collector.NewMemory("testdata/proc1")

	var s netrav1.HostSample
	if err := collectInto(c, &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if s.MemZfsArc != nil {
		t.Fatalf("MemZfsArc = %d, want absent with no ZFS kstat", *s.MemZfsArc)
	}
}
