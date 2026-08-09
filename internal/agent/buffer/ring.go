// Package buffer holds unacknowledged samples while the hub is unreachable.
package buffer

import (
	"sync"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Entry is one buffered sample and its batch sequence number.
type Entry struct {
	Seq    uint64
	Sample *netrav1.HostSample
}

// Ring is a bounded, overwrite-oldest buffer of unacknowledged samples.
//
// It is deliberately in memory only. Persisting it would need a state volume
// and corruption handling to cover a case that barely exists: an agent
// restart usually means a host reboot or an image update, and during an image
// update the hub is up, so nothing would be buffered.
type Ring struct {
	mu       sync.Mutex
	capacity int
	entries  []Entry
	dropped  uint64
}

// New builds a Ring holding at most capacity entries.
func New(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{
		capacity: capacity,
		entries:  make([]Entry, 0, capacity),
	}
}

// Add appends a sample, discarding the oldest entry when full.
func (r *Ring) Add(seq uint64, s *netrav1.HostSample) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.entries) == r.capacity {
		r.entries = r.entries[1:]
		r.dropped++
	}
	r.entries = append(r.entries, Entry{Seq: seq, Sample: s})
}

// Pending returns a copy of the buffered entries, oldest first. Replay sends
// them in this order so history fills in forwards.
func (r *Ring) Pending() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

// AckThrough drops every entry with a sequence number at or below seq.
func (r *Ring) AckThrough(seq uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	keep := r.entries[:0]
	for _, e := range r.entries {
		if e.Seq > seq {
			keep = append(keep, e)
		}
	}
	r.entries = keep
}

// Depth reports how many entries are waiting to be acknowledged.
func (r *Ring) Depth() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Capacity reports the maximum number of entries the ring currently holds.
func (r *Ring) Capacity() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.capacity
}

// Dropped reports how many entries have been discarded through overflow.
// This is cumulative and resets when the agent restarts, so the hub must
// treat a decrease as a reset rather than a negative delta.
func (r *Ring) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}
