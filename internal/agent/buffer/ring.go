// Package buffer holds unacknowledged samples while the hub is unreachable.
package buffer

import (
	"sync"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// Scrape is everything one scrape produced: the wide host row plus the
// per-entity rows measured at the same instant.
//
// The ring buffers whole scrapes rather than host rows alone. Replaying a host
// row without the rows collected beside it would lose data that nothing
// reports as lost -- the host samples arrive intact and the batch looks
// complete, so the gap is invisible on both ends.
type Scrape struct {
	Host          *netrav1.HostSample
	Cores         []*netrav1.CpuCoreSample
	Disks         []*netrav1.DiskIoSample
	Sensors       []*netrav1.SensorSample
	Nets          []*netrav1.NetSample
	Containers    []*netrav1.ContainerSample
	Filesystems   []*netrav1.FilesystemSample
	Smart         []*netrav1.SmartAttribute
	Processes     []*netrav1.ProcessSample
	Events        []*netrav1.Event
	SystemdEvents []*netrav1.SystemdUnitEvent
	PackageEvents []*netrav1.PackageEvent
	Addresses     []*netrav1.HostAddress
	Packages      []*netrav1.HostPackage

	// Collectors is the only family with no counterpart on collector.Result:
	// a collector cannot time itself, so the scrape loop measures it from the
	// outside and fills this in. It buffers and replays with the scrape it
	// describes, because "the smart collector was timing out" is only useful
	// alongside the samples that were thin because of it.
	Collectors []*netrav1.CollectorSample
}

// Entry is one buffered scrape and its batch sequence number.
type Entry struct {
	Seq    uint64
	Scrape *Scrape
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

// Add appends a scrape, discarding the oldest entry when full.
func (r *Ring) Add(seq uint64, s *Scrape) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.entries) == r.capacity {
		// Shifted down and the vacated tail slot cleared, rather than
		// r.entries = r.entries[1:].
		//
		// Resliding the start pointer forward left the dropped Entry -- and
		// through it the whole *Scrape, a host row plus every per-entity row
		// measured with it -- in a backing-array slot the garbage collector
		// could still reach, so a ring that had been dropping for an hour held
		// on to every scrape it reported as dropped. It also walked the start
		// pointer up the array until append had to reallocate, which then
		// copied the live window into a fresh block and abandoned the old one.
		//
		// Shifting keeps the slice anchored at the array's start for the life
		// of the ring: nothing is retained past its window, and the array is
		// allocated exactly once. The memmove is over at most capacity entries
		// of two words each -- 360 slots at the 6h maximum -- which is nothing
		// against the scrape that produced the entry.
		copy(r.entries, r.entries[1:])
		r.entries[len(r.entries)-1] = Entry{}
		r.entries = r.entries[:len(r.entries)-1]
		r.dropped++
	}
	r.entries = append(r.entries, Entry{Seq: seq, Scrape: s})
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
	// The filter compacts in place, so the tail beyond len(keep) still holds
	// pointers to the acknowledged scrapes. Left there they stay reachable for
	// as long as the ring lives: a host that drained a full buffer after an
	// outage would report Depth() == 0 while still holding every scrape it had
	// buffered. Zeroing the vacated slots is what actually releases them.
	for i := len(keep); i < len(r.entries); i++ {
		r.entries[i] = Entry{}
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
