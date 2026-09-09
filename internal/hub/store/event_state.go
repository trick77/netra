package store

import (
	"encoding/json"
	"fmt"
)

// Some event types carry a STATE, and for those a repeat is not an event.
//
// `events` is one table with two kinds of row in it. Most types are
// occurrences -- a kernel log line, an OOM kill, an ATA reset -- and two
// identical ones are two facts: how OFTEN a drive resets is the reading, so
// collapsing them would hide exactly what a reader is looking for. A few types
// instead report WHAT SOMETHING IS right now, and there the second identical
// row says nothing the first did not.
//
// The agent already emits state types on change only (see
// agent/collector/mdraid.go), but two paths legitimately re-send a state the
// hub already has: the baseline every agent start emits (the hub cannot tell
// "array it has never heard of" from "array that has not changed") and the
// re-arm after a dropped scrape. Stored verbatim, both fill the log with rows
// that describe nothing -- which is what the fleet's mdraid line did, minutes
// apart, for arrays that had simply been clean the whole time.
//
// This is the same guarantee InsertSystemdUnitEvents already gives its table.
//
// A type absent from this map is an occurrence and is stored unconditionally.
// Adding a state type is one entry.
//
// container_restart and container_recreate must NEVER be added, and the
// temptation will come from the fact that two of them can look identical. They
// are occurrences: two restarts are two facts, and HOW OFTEN a container
// restarts is the entire reading -- the same argument that keeps the kmsg
// family out of this map. Collapsing them would turn a crash loop into a single
// line and delete exactly the signal someone went looking for.
var eventStateKeys = map[string]func(json.RawMessage) (string, error){
	"mdraid": mdraidStateKey,
}

// eventSubject is what one state belongs to: a type and the thing within the
// host it is about (an array name, or "" for the host as a whole).
type eventSubject struct {
	typ     string
	subject string
}

// stateRow is one incoming event's place in the batch and the state it
// carries, so the comparison never re-parses a detail.
type stateRow struct {
	idx int
	key string
}

// eventStateKey reports the comparable state of an event, and false when the
// event has none -- an occurrence type, or a detail that will not parse.
//
// An unreadable detail is deliberately NOT dropped. A row the hub cannot
// interpret is still a row an agent sent, and a guard that silences what it
// fails to understand loses events for the same reason it was written to keep
// them.
func eventStateKey(typ, detailJSON string) (string, bool) {
	fn, ok := eventStateKeys[typ]
	if !ok {
		return "", false
	}
	key, err := fn(json.RawMessage(detailJSON))
	if err != nil {
		return "", false
	}
	return key, true
}

// mdraidState is the part of an mdraid event's detail that says what the array
// IS. Mirrors agent/collector/mdraid.go's arrayState; severity is not read,
// because it is derived from these fields rather than independent of them.
type mdraidState struct {
	State      string `json:"state"`
	Level      string `json:"level"`
	RaidDisks  int    `json:"raid_disks"`
	Degraded   int    `json:"degraded"`
	SyncAction string `json:"sync_action"`
}

func mdraidStateKey(detail json.RawMessage) (string, error) {
	var s mdraidState
	if err := json.Unmarshal(detail, &s); err != nil {
		return "", fmt.Errorf("parse mdraid detail: %w", err)
	}
	// state is taken verbatim. The kernel toggles `active` -> `clean` as the
	// superblock dirty bit clears, so on an array taking writes the raw word
	// flaps every few minutes -- but the agent collapses the healthy words
	// before sending (normalizeArrayState in agent/collector/mdraid.go), so
	// what arrives here is already stable and a second copy of that list would
	// only be a second place to change it.
	//
	// level, raid_disks, degraded and sync_action compare exactly, so a scrub
	// starting and finishing (idle -> check -> idle) still records both: those
	// are the collector's own account of the scrub, and only the flapping
	// underneath them is dropped.
	return fmt.Sprintf("%s|%s|%d|%d|%s", s.State, s.Level, s.RaidDisks, s.Degraded, s.SyncAction), nil
}
