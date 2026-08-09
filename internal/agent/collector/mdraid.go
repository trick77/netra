package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// arrayState is the part of an md array's sysfs directory worth reporting.
type arrayState struct {
	State      string `json:"state"`
	Level      string `json:"level"`
	RaidDisks  int    `json:"raid_disks"`
	Degraded   int    `json:"degraded"`
	SyncAction string `json:"sync_action"`
}

// Mdraid reports md array state changes as EVENTS, not samples.
//
// It has no hypertable, and that is by design rather than omission (spec §5.2,
// §5.1 rule 4): an array is "clean" for weeks, so a 60s series saying so is
// the same near-constant-series waste that keeps systemd out of §5.3. Only the
// transition carries information.
//
// The collector therefore holds the last known state per array and emits only
// when it changes. A first sighting emits one event to establish the baseline,
// so the hub knows the array exists and what state it started in.
type Mdraid struct {
	sysRoot  string
	interval time.Duration

	prev map[string]arrayState
}

// NewMdraid builds an Mdraid collector reading from sysRoot (normally "/sys").
func NewMdraid(sysRoot string, interval time.Duration) *Mdraid {
	return &Mdraid{sysRoot: sysRoot, interval: interval}
}

// Name implements Collector.
func (m *Mdraid) Name() string { return "mdraid" }

// Interval implements Collector.
func (m *Mdraid) Interval() time.Duration { return m.interval }

// SetSysRootForTest repoints the collector at a different fixture tree.
func (m *Mdraid) SetSysRootForTest(root string) { m.sysRoot = root }

// Collect implements Collector.
func (m *Mdraid) Collect(_ context.Context) (*Result, error) {
	cur, err := m.read()
	if err != nil {
		return nil, err
	}

	prev := m.prev
	m.prev = cur

	names := make([]string, 0, len(cur))
	for name := range cur {
		names = append(names, name)
	}
	// Deterministic order so a scrape that changes two arrays emits its events
	// the same way twice.
	slices.Sort(names)

	ts := time.Now().UnixMilli()
	var events []*netrav1.Event

	for _, name := range names {
		state := cur[name]
		if p, seen := prev[name]; seen && p == state {
			// Unchanged. Emitting anyway would turn `events` into the 60s
			// series this collector exists to avoid, and bury the transitions
			// that matter under weeks of identical rows.
			continue
		}

		detail, err := json.Marshal(state)
		if err != nil {
			// Marshalling a struct of strings and ints cannot fail in
			// practice; if it somehow does, the array's state change is worth
			// more than the detail, so report it with an empty object.
			detail = []byte("{}")
		}

		events = append(events, &netrav1.Event{
			TsMs:       ts,
			Type:       "mdraid",
			Subject:    name,
			DetailJson: string(detail),
		})
	}

	return &Result{Events: events}, nil
}

// read walks /sys/block/md*/md and returns each array's state.
func (m *Mdraid) read() (map[string]arrayState, error) {
	blockDir := filepath.Join(m.sysRoot, "block")
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No /sys/block at all. A host with no md arrays is the common
			// case, not a failure.
			return map[string]arrayState{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", blockDir, err)
	}

	out := make(map[string]arrayState)

	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "md") {
			continue
		}
		mdDir := filepath.Join(blockDir, e.Name(), "md")
		if _, err := os.Stat(mdDir); err != nil {
			// A block device whose name starts with "md" but which is not an
			// md array -- it has no md/ subdirectory.
			continue
		}

		out[e.Name()] = arrayState{
			State:      readSysString(mdDir, "array_state"),
			Level:      readSysString(mdDir, "level"),
			RaidDisks:  readSysInt(mdDir, "raid_disks"),
			Degraded:   readSysInt(mdDir, "degraded"),
			SyncAction: readSysString(mdDir, "sync_action"),
		}
	}

	return out, nil
}

// readSysString reads one sysfs attribute, returning "" when it is absent.
// A missing attribute is normal -- sync_action does not exist on every level --
// and must not cost the array its whole state.
func readSysString(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readSysInt is readSysString for a numeric attribute, returning 0 when it is
// absent or unparseable.
func readSysInt(dir, name string) int {
	v, err := strconv.Atoi(readSysString(dir, name))
	if err != nil {
		return 0
	}
	return v
}
