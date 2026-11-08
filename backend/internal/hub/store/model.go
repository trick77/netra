package store

import "time"

// containerState is what the hub already knows about a container: the values
// stored on its row before this batch is applied.
//
// Count and StartedAt are pointers because absent is a third state, and the
// whole derivation turns on it: a delta measured from an unknown is not a
// reading, so a nil on either side of a step yields nothing at all.
//
// Table: containers
type containerState struct {
	Count     *int64
	StartedAt *time.Time
	// LastSeen is the newest sample already stored. Samples at or before it
	// have been accounted for; see restartEvents.
	LastSeen *time.Time
	// Image as stored, so a recreate can name what was replaced.
	Image *string
}
