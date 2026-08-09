package buffer_test

import (
	"testing"

	"github.com/trick77/netra/internal/agent/buffer"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// sample builds a scrape carrying only a host row, which is all the ordering
// and overflow tests below need.
func sample(ts int64) *buffer.Scrape {
	return &buffer.Scrape{Host: &netrav1.HostSample{TsMs: ts}}
}

func TestAddAndPendingPreserveOrder(t *testing.T) {
	r := buffer.New(4)
	r.Add(1, sample(100))
	r.Add(2, sample(200))

	pending := r.Pending()
	if len(pending) != 2 {
		t.Fatalf("len(Pending()) = %d, want 2", len(pending))
	}
	if pending[0].Seq != 1 || pending[1].Seq != 2 {
		t.Fatalf("seqs = %d,%d, want 1,2", pending[0].Seq, pending[1].Seq)
	}
	if r.Depth() != 2 {
		t.Fatalf("Depth() = %d, want 2", r.Depth())
	}
}

// Overflow overwrites the oldest entry and counts the loss. Without the
// counter, a long hub outage silently eats data.
func TestOverflowDropsOldestAndCounts(t *testing.T) {
	r := buffer.New(2)
	r.Add(1, sample(100))
	r.Add(2, sample(200))
	r.Add(3, sample(300))

	if r.Depth() != 2 {
		t.Fatalf("Depth() = %d, want 2", r.Depth())
	}
	if r.Dropped() != 1 {
		t.Fatalf("Dropped() = %d, want 1", r.Dropped())
	}

	pending := r.Pending()
	if pending[0].Seq != 2 || pending[1].Seq != 3 {
		t.Fatalf("seqs = %d,%d, want 2,3 — the oldest must be dropped",
			pending[0].Seq, pending[1].Seq)
	}
}

func TestAckThroughRemovesAckedEntries(t *testing.T) {
	r := buffer.New(4)
	r.Add(1, sample(100))
	r.Add(2, sample(200))
	r.Add(3, sample(300))

	r.AckThrough(2)

	pending := r.Pending()
	if len(pending) != 1 {
		t.Fatalf("len(Pending()) = %d, want 1", len(pending))
	}
	if pending[0].Seq != 3 {
		t.Fatalf("remaining seq = %d, want 3", pending[0].Seq)
	}
}

func TestAckThroughIgnoresStaleAck(t *testing.T) {
	r := buffer.New(4)
	r.Add(5, sample(500))

	r.AckThrough(2) // older than anything held

	if r.Depth() != 1 {
		t.Fatalf("Depth() = %d, want 1 — a stale ack must not drop entries", r.Depth())
	}
}

func TestPendingReturnsACopy(t *testing.T) {
	r := buffer.New(4)
	r.Add(1, sample(100))

	pending := r.Pending()
	pending[0].Seq = 99

	if r.Pending()[0].Seq != 1 {
		t.Fatal("mutating the slice returned by Pending() changed the buffer")
	}
}

// An outage replays whole scrapes. Buffering only the host row would replay
// host samples while silently discarding every per-entity row measured at the
// same instant -- a data loss nothing reports, because the host rows arrive
// intact and the batch looks complete.
func TestRingPreservesPerFamilyRowsAcrossReplay(t *testing.T) {
	// Given: a scrape carrying a per-core row alongside its host row.
	r := buffer.New(4)
	busy := 12.5
	r.Add(1, &buffer.Scrape{
		Host:  &netrav1.HostSample{TsMs: 100},
		Cores: []*netrav1.CpuCoreSample{{TsMs: 100, Core: 0, Busy: &busy}},
	})

	// When: it is read back for a flush.
	pending := r.Pending()

	// Then: the per-core row survived buffering.
	if len(pending) != 1 {
		t.Fatalf("len(Pending()) = %d, want 1", len(pending))
	}
	if got := len(pending[0].Scrape.Cores); got != 1 {
		t.Fatalf("buffered cores = %d, want 1 -- per-family rows must survive buffering", got)
	}
	if got := pending[0].Scrape.Cores[0].GetBusy(); got != 12.5 {
		t.Errorf("busy = %v, want 12.5", got)
	}
}
