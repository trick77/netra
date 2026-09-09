package store

import "testing"

func TestEventStateKeyOnlyAnswersForStateShapedTypes(t *testing.T) {
	const clean = `{"state":"clean","level":"raid1","raid_disks":2,"degraded":0,"sync_action":"idle","severity":"info"}`

	tests := []struct {
		name    string
		typ     string
		detail  string
		wantKey string
		wantOK  bool
	}{
		{
			name:    "mdraid state",
			typ:     "mdraid",
			detail:  clean,
			wantKey: "clean|raid1|2|0|idle",
			wantOK:  true,
		},
		{
			// The dirty bit toggling under a write is the same healthy array
			// twice, and the agent sends the raw word: it normalizes only
			// inside compareKey, for its own change detection, so a restart or
			// a re-arm re-reports the array with whatever word is current.
			name:    "active is clean",
			typ:     "mdraid",
			detail:  `{"state":"active","level":"raid1","raid_disks":2,"degraded":0,"sync_action":"idle"}`,
			wantKey: "clean|raid1|2|0|idle",
			wantOK:  true,
		},
		{
			// Not in the healthy set, so it keeps its own identity.
			name:    "inactive keeps its word",
			typ:     "mdraid",
			detail:  `{"state":"inactive","level":"raid1","raid_disks":2,"degraded":0,"sync_action":"idle"}`,
			wantKey: "inactive|raid1|2|0|idle",
			wantOK:  true,
		},
		{
			// Severity is derived from these fields, so it must not be part of
			// the key. Rows written before the detail key was retired still
			// carry one, and it must not make an unchanged array read as a
			// changed one.
			name:    "severity is not part of the state",
			typ:     "mdraid",
			detail:  `{"state":"clean","level":"raid1","raid_disks":2,"degraded":0,"sync_action":"idle","severity":"critical"}`,
			wantKey: "clean|raid1|2|0|idle",
			wantOK:  true,
		},
		{
			// Two identical kernel resets are two resets. How often a drive
			// does this IS the reading.
			name:   "occurrence type has no state",
			typ:    "ata_error",
			detail: `{"message":"ata1: hard resetting link"}`,
		},
		{
			name:   "unreadable detail has no state",
			typ:    "mdraid",
			detail: `not json`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := eventStateKey(tc.typ, tc.detail)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if key != tc.wantKey {
				t.Errorf("key = %q, want %q", key, tc.wantKey)
			}
		})
	}
}
