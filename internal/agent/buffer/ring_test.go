package buffer_test

import (
	"testing"

	"github.com/trick77/netra/internal/agent/buffer"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func sample(ts int64) *netrav1.HostSample {
	return &netrav1.HostSample{TsMs: ts}
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

// Resizing smaller than the current depth must drop the oldest entries,
// exactly like Add's overwrite-oldest policy, and count the loss so it stays
// visible to whoever is watching Dropped().
func TestResizeSmallerDropsOldestAndCounts(t *testing.T) {
	r := buffer.New(4)
	r.Add(1, sample(100))
	r.Add(2, sample(200))
	r.Add(3, sample(300))
	r.Add(4, sample(400))

	dropped := r.Resize(2)
	if dropped != 2 {
		t.Fatalf("Resize(2) returned %d, want 2", dropped)
	}
	if r.Dropped() != 2 {
		t.Fatalf("Dropped() = %d, want 2", r.Dropped())
	}
	if r.Depth() != 2 {
		t.Fatalf("Depth() = %d, want 2", r.Depth())
	}

	pending := r.Pending()
	if pending[0].Seq != 3 || pending[1].Seq != 4 {
		t.Fatalf("seqs = %d,%d, want 3,4 — the oldest must be dropped",
			pending[0].Seq, pending[1].Seq)
	}

	// The shrunk capacity must still be enforced by later Adds.
	r.Add(5, sample(500))
	if r.Depth() != 2 {
		t.Fatalf("Depth() after Add past new capacity = %d, want 2", r.Depth())
	}
	if r.Dropped() != 3 {
		t.Fatalf("Dropped() after Add past new capacity = %d, want 3", r.Dropped())
	}
}

// Resizing larger must preserve every currently buffered entry untouched.
func TestResizeLargerPreservesEverything(t *testing.T) {
	r := buffer.New(2)
	r.Add(1, sample(100))
	r.Add(2, sample(200))

	dropped := r.Resize(8)
	if dropped != 0 {
		t.Fatalf("Resize(8) returned %d, want 0", dropped)
	}
	if r.Dropped() != 0 {
		t.Fatalf("Dropped() = %d, want 0", r.Dropped())
	}

	pending := r.Pending()
	if len(pending) != 2 {
		t.Fatalf("len(Pending()) = %d, want 2", len(pending))
	}
	if pending[0].Seq != 1 || pending[1].Seq != 2 {
		t.Fatalf("seqs = %d,%d, want 1,2", pending[0].Seq, pending[1].Seq)
	}

	// The grown capacity must be usable: 6 more Adds should not overflow.
	for seq := uint64(3); seq <= 8; seq++ {
		r.Add(seq, sample(int64(seq)*100))
	}
	if r.Depth() != 8 {
		t.Fatalf("Depth() = %d, want 8 — growing capacity must not drop existing entries", r.Depth())
	}
	if r.Dropped() != 0 {
		t.Fatalf("Dropped() = %d, want 0 after filling exactly to the new capacity", r.Dropped())
	}
}

// Resize must clamp a non-positive capacity to 1, matching New's guard,
// rather than producing a ring that can never hold anything or one whose
// invariants (capacity >= 1) silently break.
func TestResizeClampsNonPositiveCapacityToOne(t *testing.T) {
	r := buffer.New(4)
	r.Add(1, sample(100))

	r.Resize(0)

	r.Add(2, sample(200))
	if r.Depth() != 1 {
		t.Fatalf("Depth() = %d, want 1 — capacity 0 must clamp to 1", r.Depth())
	}
}
