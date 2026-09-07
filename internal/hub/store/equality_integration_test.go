package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
	"github.com/trick77/netra/internal/hub/store"
)

// The dark-launch gate: the hub's verdict must equal the browser's.
//
// One fixture, ui/test/conditions/equality.json, read by this test and by
// ui/src/features/fleet/conditions.equality.test.ts. No cross-language runner
// and no shelling out -- each side seeds itself from the same description and
// asserts the same verdicts, so a disagreement shows up as one of them going
// red rather than as a comparison nobody runs.
//
// This is the load-bearing test in the whole plan. Nothing on the browser side
// may be deleted until it is green, because it is the only thing standing
// between "the hub decides now" and "the hub decides differently now".
//
// What it deliberately does not cover, and why, is written in the fixture.

type fixtureSample struct {
	OffsetS int    `json:"offset_s"`
	Used    *int64 `json:"used"`
	Free    *int64 `json:"free"`
}

type fixtureFS struct {
	Label      string          `json:"label"`
	Mountpoint string          `json:"mountpoint"`
	Samples    []fixtureSample `json:"samples"`
}

type fixtureUnit struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	StateTSOffs int    `json:"state_ts_offset_s"`
}

type fixtureHost struct {
	Hostname        string        `json:"hostname"`
	LastSeenOffsetS *int          `json:"last_seen_offset_s"`
	ServicesFailed  *int          `json:"services_failed"`
	Units           []fixtureUnit `json:"units"`
	Filesystems     []fixtureFS   `json:"filesystems"`
}

type fixtureVerdict struct {
	Hostname     string `json:"hostname"`
	Kind         string `json:"kind"`
	Subject      string `json:"subject"`
	Severity     string `json:"severity"`
	SinceOffsetS *int   `json:"since_offset_s"`
	SinceAtLeast bool   `json:"since_at_least"`
}

type equalityFixture struct {
	StepS  int              `json:"step_s"`
	Hosts  []fixtureHost    `json:"hosts"`
	Expect []fixtureVerdict `json:"expect"`
}

func loadEqualityFixture(t *testing.T) equalityFixture {
	t.Helper()
	// Under ui/, not beside this file, and that is the container build's doing:
	// the SPA is compiled in a node stage that copies ui/ alone, so a fixture at
	// the repo root cannot be type-checked there -- see the note on the
	// TypeScript half. One file either way; this is the side that can reach it.
	path := filepath.Join("..", "..", "..", "ui", "test", "conditions", "equality.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f equalityFixture
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(f.Hosts) == 0 || len(f.Expect) == 0 {
		t.Fatal("the fixture describes no fleet; a vacuous gate proves nothing")
	}
	return f
}

// verdict is one condition at the granularity both sides render.
type verdict struct {
	hostname     string
	kind         string
	subject      string
	severity     string
	sinceOffsetS *int
	sinceAtLeast bool
}

func (v verdict) String() string {
	since := "none"
	if v.sinceOffsetS != nil {
		since = fmt.Sprintf("%ds", *v.sinceOffsetS)
	}
	at := ""
	if v.sinceAtLeast {
		at = " (at least)"
	}
	return fmt.Sprintf("%s %s/%s %s since %s%s",
		v.hostname, v.kind, v.subject, v.severity, since, at)
}

func sortVerdicts(in []verdict) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].hostname != in[j].hostname {
			return in[i].hostname < in[j].hostname
		}
		if in[i].kind != in[j].kind {
			return in[i].kind < in[j].kind
		}
		return in[i].subject < in[j].subject
	})
}

func seedEqualityFixture(t *testing.T, ctx context.Context, s *store.Store,
	f equalityFixture, now time.Time) map[int32]string {
	t.Helper()
	names := map[int32]string{}

	for _, h := range f.Hosts {
		id := newHost(t, ctx, s, h.Hostname)
		names[id] = h.Hostname

		// host_current carries BOTH the last_seen every kind is interpreted
		// against and the services_failed count failed-units leads with. The
		// scan reads current-state tables; the samples below feed only the
		// onset walk.
		var lastSeen *time.Time
		if h.LastSeenOffsetS != nil {
			at := now.Add(time.Duration(*h.LastSeenOffsetS) * time.Second)
			lastSeen = &at
		}
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO host_current (host_id, last_seen, services_failed)
			VALUES ($1, $2, $3)`, id, lastSeen, h.ServicesFailed); err != nil {
			t.Fatalf("host_current %s: %v", h.Hostname, err)
		}

		for _, u := range h.Units {
			at := now.Add(time.Duration(u.StateTSOffs) * time.Second)
			if _, err := s.Pool().Exec(ctx, `
				INSERT INTO systemd_units (host_id, unit_name, state, state_ts)
				VALUES ($1, $2, $3, $4)`, id, u.Name, u.State, at); err != nil {
				t.Fatalf("systemd_units %s/%s: %v", h.Hostname, u.Name, err)
			}
		}

		for _, fs := range h.Filesystems {
			var fsID int32
			if err := s.Pool().QueryRow(ctx, `
				INSERT INTO filesystems (host_id, label, mountpoint)
				VALUES ($1, $2, $3) RETURNING id`,
				id, fs.Label, fs.Mountpoint).Scan(&fsID); err != nil {
				t.Fatalf("filesystems %s/%s: %v", h.Hostname, fs.Label, err)
			}
			if len(fs.Samples) == 0 {
				continue
			}
			newest := fs.Samples[len(fs.Samples)-1]
			for _, smp := range fs.Samples {
				at := now.Add(time.Duration(smp.OffsetS) * time.Second)
				if _, err := s.Pool().Exec(ctx, `
					INSERT INTO filesystem_samples (host_id, ts, fs_id, total, used, free)
					VALUES ($1, $2, $3, $4, $5, $6)`,
					id, at, fsID, sum(smp.Used, smp.Free), smp.Used, smp.Free); err != nil {
					t.Fatalf("filesystem_samples %s/%s: %v", h.Hostname, fs.Label, err)
				}
				if smp.OffsetS > newest.OffsetS {
					newest = smp
				}
			}
			// filesystem_current MUST equal the newest sample. It is what the
			// scan judges from, and a fixture where the two disagree would be
			// testing a state ingest never produces.
			at := now.Add(time.Duration(newest.OffsetS) * time.Second)
			if _, err := s.Pool().Exec(ctx, `
				INSERT INTO filesystem_current (host_id, fs_id, ts, total, used, free)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				id, fsID, at, sum(newest.Used, newest.Free), newest.Used, newest.Free); err != nil {
				t.Fatalf("filesystem_current %s/%s: %v", h.Hostname, fs.Label, err)
			}
		}
	}
	return names
}

func sum(a, b *int64) *int64 {
	if a == nil || b == nil {
		return nil
	}
	v := *a + *b
	return &v
}

func TestIntegrationHubVerdictEqualsTheBrowsersOnTheSharedFixture(t *testing.T) {
	ctx, s := condCtx(t)
	f := loadEqualityFixture(t)
	// Truncated to the second so an offset lands on a whole second and the
	// comparison below is not decided by microseconds.
	now := time.Now().UTC().Truncate(time.Second)
	names := seedEqualityFixture(t, ctx, s, f, now)

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Every kind the fixture asserts on must actually have been LOOKED AT. A
	// query that failed leaves its kind out of Evaluated, and without this the
	// gate would pass green on a scan that found nothing because it ran
	// nothing.
	for _, kind := range []string{
		conditions.KindSilent, conditions.KindDisk, conditions.KindFailedUnits,
	} {
		if !scan.Evaluated[kind] {
			t.Fatalf("the %s kind was not evaluated; its query failed and the gate would pass vacuously", kind)
		}
	}

	var got []verdict
	for key, finding := range scan.Bad {
		// sporadic and drive are out of scope for the equality -- see the
		// fixture's own note. Filtering here rather than in the fixture keeps
		// the reason in one place and the fixture readable.
		if key.Kind == conditions.KindSporadic || key.Kind == conditions.KindDrive {
			continue
		}
		v := verdict{
			hostname:     names[key.HostID],
			kind:         key.Kind,
			subject:      key.Subject,
			severity:     finding.Severity,
			sinceAtLeast: finding.OpenedAtLeast,
		}
		if !finding.OpenedTS.IsZero() {
			offset := int(finding.OpenedTS.UTC().Sub(now).Round(time.Second).Seconds())
			v.sinceOffsetS = &offset
		}
		got = append(got, v)
	}

	want := make([]verdict, 0, len(f.Expect))
	for _, e := range f.Expect {
		want = append(want, verdict{
			hostname:     e.Hostname,
			kind:         e.Kind,
			subject:      e.Subject,
			severity:     e.Severity,
			sinceOffsetS: e.SinceOffsetS,
			sinceAtLeast: e.SinceAtLeast,
		})
	}

	sortVerdicts(got)
	sortVerdicts(want)

	if len(got) != len(want) {
		t.Fatalf("hub found %d conditions, the fixture expects %d\n got: %v\nwant: %v",
			len(got), len(want), render(got), render(want))
	}
	for i := range want {
		if !sameVerdict(got[i], want[i]) {
			t.Errorf("verdict %d:\n got: %s\nwant: %s", i, got[i], want[i])
		}
	}
}

func sameVerdict(a, b verdict) bool {
	if a.hostname != b.hostname || a.kind != b.kind || a.subject != b.subject ||
		a.severity != b.severity || a.sinceAtLeast != b.sinceAtLeast {
		return false
	}
	if (a.sinceOffsetS == nil) != (b.sinceOffsetS == nil) {
		return false
	}
	return a.sinceOffsetS == nil || *a.sinceOffsetS == *b.sinceOffsetS
}

func render(in []verdict) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, v.String())
	}
	return out
}

// The two rules that were misaligned, pinned as their own assertions.
//
// Both were bugs, and both are the kind a fixture comparison can pass over: the
// counts still add up while the SOURCE is wrong, and the gate would go green on
// a fleet where nothing happened to expose it.
func TestIntegrationFailedUnitsCountsTheSummaryAndDatesTheRows(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-units-source")
	now := time.Now().UTC().Truncate(time.Second)
	oldest := now.Add(-2 * time.Hour)

	// The summary says THREE; the hub holds unit rows for two of them. That is
	// routine rather than exotic -- the summary rides every 60 s scrape and the
	// snapshot that fills the unit rows arrives every five minutes -- and the
	// count is what every other part of netra counts.
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_current (host_id, last_seen, services_failed)
		VALUES ($1, $2, 3)`, host, now); err != nil {
		t.Fatalf("host_current: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO systemd_units (host_id, unit_name, state, state_ts)
		VALUES ($1, 'a.service', 'failed', $2),
		       ($1, 'b.service', 'failed', $3),
		       ($1, 'c.service', 'active', $2)`,
		host, oldest, now.Add(-time.Hour)); err != nil {
		t.Fatalf("systemd_units: %v", err)
	}

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	f, bad := scan.Bad[conditions.Key{HostID: host, Kind: conditions.KindFailedUnits}]
	if !bad {
		t.Fatalf("no failed-units condition: %+v", scan.Bad)
	}
	if f.Detail["count"] != 3 {
		t.Errorf("count = %v, want 3 from services_failed -- not the number of unit rows",
			f.Detail["count"])
	}
	names, _ := f.Detail["units"].([]string)
	if len(names) != 2 {
		t.Errorf("units = %v, want the two rows the hub actually holds", names)
	}
	// Five units failing at five times is ONE condition that began with the
	// first.
	if !f.OpenedTS.UTC().Equal(oldest) {
		t.Errorf("onset = %v, want the oldest failing unit's state_ts %v", f.OpenedTS.UTC(), oldest)
	}
}

// A mount is judged only on a reading no older than StaleAfter against the
// HOST'S OWN last_seen -- never against the wall clock.
//
// The two halves matter separately. A host that is OFF keeps its mounts, because
// their readings are as old as its last_seen and no older: a 96% disk on a
// machine that is off is still a 96% disk. A host that is TALKING while one
// mount's reading stands still has that mount left unjudged, because the agent
// skipping a wedged mountpoint is not the same fact as the disk recovering.
func TestIntegrationAMountIsDatedAgainstItsHostNotTheClock(t *testing.T) {
	ctx, s := condCtx(t)
	now := time.Now().UTC().Truncate(time.Second)
	full, room := 97*gib, 3*gib

	// Off for a day, with a reading from just before it went.
	off := newHost(t, ctx, s, "cond-off")
	offSeen := now.Add(-24 * time.Hour)
	seedHostCurrent(t, ctx, s, off, offSeen)
	seedFilesystem(t, ctx, s, off, "root", "/", full, room, offSeen)

	// Talking now, with a mount nobody has re-read for an hour.
	wedged := newHost(t, ctx, s, "cond-wedged")
	seedHostCurrent(t, ctx, s, wedged, now)
	seedFilesystem(t, ctx, s, wedged, "backup", "/mnt/backup", full, room, now.Add(-time.Hour))

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	offKey := diskKey(off, "root")
	if _, bad := scan.Bad[offKey]; !bad {
		t.Error("a full disk on a machine that is off stopped being a full disk")
	}
	if !scan.Seen[offKey] {
		t.Error("the offline host's mount was not judged; its reading is current against ITS OWN last_seen")
	}

	wedgedKey := diskKey(wedged, "backup")
	if !scan.Unjudged[wedgedKey] {
		t.Error("a mount not re-read for an hour on a talking host must be unjudged")
	}
	if scan.Seen[wedgedKey] {
		t.Error("the wedged mount was marked seen; its condition could then clear on a reading nobody took")
	}
}
