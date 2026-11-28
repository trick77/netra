package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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

// arrayProgress is the part of an array that moves while it syncs, and its
// members. Kept apart from arrayState because none of it may make an event:
// sync_completed changes every scrape of a check, and compareKey works on
// arrayState alone. It travels in the snapshot instead.
type arrayProgress struct {
	syncDone, syncTotal *uint64
	speedKiBs           *uint64
	mismatches          *uint64
	members             []*netrav1.MdMember
}

// arrayRead is one array as read from sysfs.
type arrayRead struct {
	state    arrayState
	progress arrayProgress
}

// eventDetail is what an mdraid event's detail_json carries: the array's state,
// plus what it was doing before, so a reader can tell a check starting from one
// finishing without the previous event at hand. prev_sync_action is absent on
// a first sighting, where there is no before; mismatch_cnt rides only the
// event of a sync ending, the one moment the count is a result.
type eventDetail struct {
	arrayState
	PrevSyncAction *string `json:"prev_sync_action,omitempty"`
	MismatchCnt    *uint64 `json:"mismatch_cnt,omitempty"`
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
//
// Beside the events it sends a snapshot of every array each scrape (see
// MdSnapshot): where a sync stands is a level, not a transition.
type Mdraid struct {
	sysRoot string

	prev map[string]arrayState

	// hadArrays is whether the last snapshot listed any array. A host that
	// lost its last one sends one empty snapshot, which is how the hub learns
	// to drop the rows, and then nothing, like a host that never had one.
	hadArrays bool
}

// NewMdraid builds an Mdraid collector reading from sysRoot (normally "/sys").
func NewMdraid(sysRoot string) *Mdraid {
	// hadArrays starts true: the hub may still hold arrays this host lost
	// while the agent was down, so the first scrape always says what is there,
	// even nothing. One empty snapshot per start on a host without arrays.
	return &Mdraid{sysRoot: sysRoot, hadArrays: true}
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

// Collect implements Collector.
func (m *Mdraid) Collect(_ context.Context) (*Result, error) {
	read, err := m.read()
	if err != nil {
		return nil, err
	}

	cur := make(map[string]arrayState, len(read))
	for name, r := range read {
		cur[name] = r.state
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
		p, seen := prev[name]
		if seen && p.compareKey() == state.compareKey() {
			// Unchanged, as compareKey defines it. Emitting anyway would
			// turn `events` into the 60s series this collector exists to
			// avoid, and bury the transitions that matter under weeks of
			// identical rows.
			continue
		}

		d := eventDetail{arrayState: state}
		if seen {
			d.PrevSyncAction = &p.SyncAction
			// Only a check or a repair counts mismatches, and only its end
			// has a result: the kernel leaves the count alone across a
			// rebuild, so a recover ending would claim the last check's
			// figure, and a check frozen halfway has counted half the array.
			if scrubs(p.SyncAction) && state.SyncAction == "idle" {
				d.MismatchCnt = read[name].progress.mismatches
			}
		}

		severity := state.severityOf()
		detail, err := json.Marshal(d)
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
			Severity:   &severity,
			DetailJson: string(detail),
		})
	}

	return &Result{Events: events, MdSnapshot: m.snapshot(ts, names, read)}, nil
}

// snapshot is every array as read this scrape, or nil when there is nothing
// the hub needs to hear: no array now and none in the last snapshot either.
func (m *Mdraid) snapshot(ts int64, names []string, read map[string]arrayRead) *netrav1.MdSnapshot {
	if len(names) == 0 && !m.hadArrays {
		return nil
	}
	m.hadArrays = len(names) > 0

	arrays := make([]*netrav1.MdArray, 0, len(names))
	for _, name := range names {
		r := read[name]
		arrays = append(arrays, &netrav1.MdArray{
			Name:             name,
			Level:            r.state.Level,
			RaidDisks:        clampUint32(r.state.RaidDisks),
			Degraded:         clampUint32(r.state.Degraded),
			State:            r.state.State,
			SyncAction:       r.state.SyncAction,
			SyncDoneSectors:  r.progress.syncDone,
			SyncTotalSectors: r.progress.syncTotal,
			SyncSpeedKibs:    r.progress.speedKiBs,
			MismatchCnt:      r.progress.mismatches,
			Members:          r.progress.members,
		})
	}
	return &netrav1.MdSnapshot{TsMs: ts, Arrays: arrays}
}

// scrubs is whether sync_action is one that counts mismatch_cnt.
func scrubs(action string) bool {
	return action == "check" || action == "repair"
}

func clampUint32(n int) uint32 {
	if n < 0 {
		return 0
	}
	return uint32(min(n, math.MaxUint32)) //nolint:gosec // clamped to the uint32 range on the line above
}

// read walks /sys/block/md*/md and returns each array's state.
func (m *Mdraid) read() (map[string]arrayRead, error) {
	blockDir := filepath.Join(m.sysRoot, "block")
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No /sys/block at all. A host with no md arrays is the common
			// case, not a failure.
			return map[string]arrayRead{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", blockDir, err)
	}

	out := make(map[string]arrayRead)

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

		done, total := readSyncCompleted(mdDir)
		out[e.Name()] = arrayRead{
			state: arrayState{
				State:      readSysString(mdDir, "array_state"),
				Level:      readSysString(mdDir, "level"),
				RaidDisks:  readSysInt(mdDir, "raid_disks"),
				Degraded:   readSysInt(mdDir, "degraded"),
				SyncAction: readSysString(mdDir, "sync_action"),
			},
			progress: arrayProgress{
				syncDone:   done,
				syncTotal:  total,
				speedKiBs:  readSysUint(mdDir, "sync_speed"),
				mismatches: readSysUint(mdDir, "mismatch_cnt"),
				members:    readMembers(mdDir),
			},
		}
	}

	return out, nil
}

// readSyncCompleted parses md/sync_completed, "<done> / <total>" in sectors
// while a sync runs and "none" otherwise. Anything else is no reading.
func readSyncCompleted(mdDir string) (done, total *uint64) {
	a, b, ok := strings.Cut(readSysString(mdDir, "sync_completed"), "/")
	if !ok {
		return nil, nil
	}
	d, errD := strconv.ParseUint(strings.TrimSpace(a), 10, 64)
	t, errT := strconv.ParseUint(strings.TrimSpace(b), 10, 64)
	if errD != nil || errT != nil || t == 0 {
		return nil, nil
	}
	return &d, &t
}

// readMembers lists md/dev-*: one directory per member, its state and slot
// inside. Sorted by device, so an unchanged array reads the same every time.
func readMembers(mdDir string) []*netrav1.MdMember {
	entries, err := os.ReadDir(mdDir)
	if err != nil {
		return nil
	}
	var out []*netrav1.MdMember
	for _, e := range entries {
		device, ok := strings.CutPrefix(e.Name(), "dev-")
		if !ok {
			continue
		}
		dir := filepath.Join(mdDir, e.Name())
		m := &netrav1.MdMember{Device: device, State: readSysString(dir, "state")}
		if slot := readSysUint(dir, "slot"); slot != nil && *slot <= math.MaxUint32 {
			s := uint32(*slot)
			m.Slot = &s
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b *netrav1.MdMember) int { return strings.Compare(a.Device, b.Device) })
	return out
}

// readSysUint is readSysString for an unsigned counter, nil when the attribute
// is absent or not a number -- "none" on sync_speed and slot included.
func readSysUint(dir, name string) *uint64 {
	v, err := strconv.ParseUint(readSysString(dir, name), 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

// readSysString reads one sysfs attribute, returning "" when it is absent.
// A missing attribute is normal -- sync_action does not exist on every level --
// and must not cost the array its whole state.
func readSysString(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // reads the host pseudo-filesystem under procRoot/sysRoot, which come from AGENT_PROC_ROOT / AGENT_SYSFS_ROOT at startup, never from request data
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
