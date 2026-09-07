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

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// arrayState is the part of an md array's sysfs directory worth reporting.
type arrayState struct {
	State      string `json:"state"`
	Level      string `json:"level"`
	RaidDisks  int    `json:"raid_disks"`
	Degraded   int    `json:"degraded"`
	SyncAction string `json:"sync_action"`
}

// mdraidDetail is what lands in events.detail: the array's state, plus the
// severity this collector decided.
//
// Embedded rather than a field, so the detail keeps the flat shape it has
// always had -- state, level, raid_disks, degraded, sync_action -- and gains
// one key. A nested object would have broken every reader of the old shape,
// including the migration that backfills historical rows.
type mdraidDetail struct {
	arrayState
	Severity string `json:"severity"`
}

// healthyStates are the md/array_state values that all mean "nothing is wrong
// with this array".
//
// The kernel moves between them as the superblock dirty bit toggles -- `active`
// while a write is outstanding, `clean` once the metadata has been flushed --
// so on any array taking writes the raw string flaps, and during a `check` it
// flaps every few minutes for days. None of that is a state change: it is the
// same healthy array, twice.
//
// Anything NOT listed here -- inactive, readonly, suspended, clear, or an empty
// read -- is a real condition, keeps its own identity, and still raises an
// event.
var healthyStates = map[string]bool{
	"clean":         true,
	"active":        true,
	"active-idle":   true,
	"write-pending": true,
	"read-auto":     true,
}

// normalizeArrayState collapses the healthy array_state words onto one of them.
func normalizeArrayState(state string) string {
	if healthyStates[state] {
		return "clean"
	}
	return state
}

// rebuildActions are the sync_action values that mean the kernel is putting a
// missing member back.
var rebuildActions = map[string]bool{
	"recover": true,
	"resync":  true,
	"repair":  true,
}

// severityOf is how bad this array's state is, decided HERE rather than by
// whoever renders the event.
//
// It lived in the browser until now (mdraidCondition in
// ui/src/features/events/message.ts), which was survivable while a person
// reading a page was the only consumer and is not once anything else has to
// ask "what is critical". A rule that only exists in TypeScript is a rule an
// alerting engine cannot apply, and a degraded array is the single thing this
// collector exists to report.
//
// `state` is deliberately NOT consulted, and that is the whole subtlety. It is
// sysfs array_state, whose vocabulary is clear / inactive / suspended /
// readonly / read-auto / clean / active / write-pending / active-idle -- and
// "degraded" is not among them. The kernel reports a raid1 with one disk left
// as `clean`, because clean is about consistency and not about how many disks
// are still there; the repo's own fixture says so, testdata/mdraid/degraded
// reads array_state=clean with degraded=1. So the device count is the only
// honest source for "is this array in trouble", and sync_action the only one
// for "is it fixing itself".
//
// An array that is missing members and NOT rebuilding is critical: nothing is
// coming to help it, and the next failure is data loss. One that is rebuilding
// is a warning -- it is degraded now, but the kernel is already doing the thing
// an operator would otherwise be woken up to start.
func (s arrayState) severityOf() string {
	if s.Degraded <= 0 {
		return "info"
	}
	if rebuildActions[s.SyncAction] {
		return "warning"
	}
	return "critical"
}

// compareKey is the arrayState reduced to what a CHANGE means.
//
// Identical to arrayState except that State is normalized. level, raid_disks,
// degraded and sync_action still compare exactly, so `idle` -> `check` and
// `check` -> `idle` each still emit one event -- the scrub start and finish are
// the mdraid collector's own account of a scrub, and survive untouched. Only
// the dirty-bit flapping underneath them is dropped.
//
// The value receiver copies, so the caller's arrayState is untouched and the
// stored detail keeps the RAW state the kernel reported. Normalizing what is
// REPORTED would throw away the one place the exact word is visible.
func (a arrayState) compareKey() arrayState {
	a.State = normalizeArrayState(a.State)
	return a
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
	sysRoot string

	prev map[string]arrayState
}

// NewMdraid builds an Mdraid collector reading from sysRoot (normally "/sys").
func NewMdraid(sysRoot string) *Mdraid {
	return &Mdraid{sysRoot: sysRoot}
}

// EmitsBaseline implements BaselineEmitter, keeping this collector out of the
// agent's startup priming. Its first Collect reports every array's state, and
// priming would discard exactly that -- so an array already degraded when the
// agent started would raise no event.
func (m *Mdraid) EmitsBaseline() bool { return true }

// Name implements Collector.
func (m *Mdraid) Name() string { return "mdraid" }

// ResendInventory implements InventoryResender.
//
// Forgetting the last seen states is the whole re-arm: the next Collect finds
// nothing to compare against and re-reports every array. This collector is
// event-based precisely so that the LAST event is the state, so a scrape
// carrying "md0 went degraded" that the ring dropped left the hub serving
// "clean" permanently -- the array never changes again, so neither does the
// event.
func (m *Mdraid) ResendInventory() { m.prev = nil }

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
		if p, seen := prev[name]; seen && p.compareKey() == state.compareKey() {
			// Unchanged, as compareKey defines it. Emitting anyway would
			// turn `events` into the 60s series this collector exists to
			// avoid, and bury the transitions that matter under weeks of
			// identical rows.
			continue
		}

		severity := state.severityOf()
		detail, err := json.Marshal(mdraidDetail{arrayState: state, Severity: severity})
		if err != nil {
			// Marshalling a struct of strings and ints cannot fail in
			// practice; if it somehow does, the array's state change is worth
			// more than the detail, so report it with an empty object.
			detail = []byte("{}")
		}

		events = append(events, &netrav1.Event{
			TsMs:     ts,
			Type:     "mdraid",
			Subject:  name,
			Severity: &severity,
			// Severity is in BOTH the field and the detail, deliberately. The
			// field is what the hub stores and what a non-browser reader uses;
			// the key is what both event views read first, and the host Events
			// tab accepts nothing else -- so dropping it here would leave a
			// degraded array uncoloured on the very page that lists it.
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
