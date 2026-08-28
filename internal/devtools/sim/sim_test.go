package sim

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// window used by every test here, so a failure is reproducible without
// depending on when the suite runs.
var (
	testTo   = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	testFrom = testTo.Add(-90 * 24 * time.Hour)
)

func TestTheSameSeedRegeneratesIdenticalHistory(t *testing.T) {
	for _, p := range Fleet() {
		ts := testTo.Add(-33 * time.Hour)
		opt := Options{Smart: true, Collectors: true, Inventory: true}

		a := NewGenerator(p, 7, testFrom, testTo).Scrape(ts, opt)
		b := NewGenerator(p, 7, testFrom, testTo).Scrape(ts, opt)

		if !proto.Equal(a.Host, b.Host) {
			t.Fatalf("%s: two generators with the same seed produced different host samples", p.Name)
		}
		if len(a.Cores) != len(b.Cores) {
			t.Fatalf("%s: core count differs: %d vs %d", p.Name, len(a.Cores), len(b.Cores))
		}
		for i := range a.Cores {
			if !proto.Equal(a.Cores[i], b.Cores[i]) {
				t.Fatalf("%s: core %d differs between runs", p.Name, i)
			}
		}
	}
}

func TestADifferentSeedProducesDifferentHistory(t *testing.T) {
	p := Fleet()[0]
	ts := testTo.Add(-time.Hour)

	a := NewGenerator(p, 1, testFrom, testTo).Scrape(ts, Options{})
	b := NewGenerator(p, 2, testFrom, testTo).Scrape(ts, Options{})

	if proto.Equal(a.Host, b.Host) {
		t.Fatal("two seeds produced the identical sample, so --seed does nothing")
	}
}

// An absent subsystem must reach the database as NULL rather than as a
// fabricated zero, which is the whole reason every metric field is optional.
func TestAbsentSubsystemsAreUnsetRatherThanZero(t *testing.T) {
	ts := testTo.Add(-2 * time.Hour)
	opt := Options{Smart: true, Collectors: true, Inventory: true}

	byName := map[string]*Scrape{}
	profiles := map[string]*Profile{}
	for _, p := range Fleet() {
		byName[p.Name] = NewGenerator(p, 1, testFrom, testTo).Scrape(ts, opt)
		profiles[p.Name] = p
	}

	if s := byName["rpi5"]; s.Host.SwapTotal != nil {
		t.Error("rpi5 has no swap, so swap_total must be unset")
	}
	if s := byName["rpi5"]; s.Host.ProcessesTotal != nil {
		t.Error("rpi5 reports a namespaced process collector, so processes_total must be unset")
	}
	if s := byName["rpi5"]; len(s.Smart) != 0 {
		t.Error("rpi5 has no drive that answers SMART")
	}
	if s := byName["nvme-vps"]; len(s.Sensors) != 0 {
		t.Error("nvme-vps reports no sensors")
	}
	if s := byName["nvme-vps"]; s.Host.CpuSteal == nil {
		t.Error("a VPS must report steal time; that is the point of the profile")
	}
	if s := byName["smart-baremetal"]; s.Host.CpuSteal != nil {
		t.Error("bare metal has no hypervisor to steal from, so cpu_steal must be unset")
	}
	if s := byName["smart-baremetal"]; s.Host.MemZfsArc == nil {
		t.Error("the ZFS host must report an ARC size")
	}
	if s := byName["minimal-vps"]; len(s.Containers) != 0 {
		t.Error("minimal-vps has no docker socket, so it reports no containers")
	}
	if s := byName["minimal-vps"]; s.Host.Udp6InErrorsPerS != nil {
		t.Error("minimal-vps has no IPv6 address, so the snmp6 block must be unset")
	}
	if s := byName["minimal-vps"]; s.Host.ServicesTotal != nil {
		t.Error("minimal-vps reports systemd unavailable, so the unit summary must be unset")
	}
}

// The fleet must cover every table in the schema, or the simulator leaves
// exactly the gap it was built to close.
func TestTheFleetPopulatesEveryFamily(t *testing.T) {
	opt := Options{Smart: true, Collectors: true, Inventory: true}
	seen := map[string]bool{}

	for _, p := range Fleet() {
		g := NewGenerator(p, 1, testFrom, testTo)
		// Walk a few days on the coarse grid so the scheduled discrete events
		// have a chance to come due.
		for ts := testFrom; ts.Before(testFrom.Add(30 * 24 * time.Hour)); ts = ts.Add(30 * time.Minute) {
			s := g.Scrape(ts, opt)
			mark(seen, "host", s.Host != nil)
			mark(seen, "agent", s.Host.GetAgent() != nil)
			mark(seen, "cores", len(s.Cores) > 0)
			mark(seen, "disks", len(s.Disks) > 0)
			mark(seen, "sensors", len(s.Sensors) > 0)
			mark(seen, "nets", len(s.Nets) > 0)
			mark(seen, "collectors", len(s.Collectors) > 0)
			mark(seen, "containers", len(s.Containers) > 0)
			mark(seen, "filesystems", len(s.Filesystems) > 0)
			mark(seen, "smart", len(s.Smart) > 0)
			mark(seen, "events", len(s.Events) > 0)
			mark(seen, "systemd_events", len(s.SystemdEvents) > 0)
			mark(seen, "package_events", len(s.PackageEvents) > 0)
			mark(seen, "addresses", len(s.Addresses) > 0)
			mark(seen, "packages", len(s.Packages) > 0)
		}
	}

	for _, family := range []string{
		"host", "agent", "cores", "disks", "sensors", "nets", "collectors",
		"containers", "filesystems", "smart", "events",
		"systemd_events", "package_events", "addresses", "packages",
	} {
		if !seen[family] {
			t.Errorf("no simulated host ever emits %s, so that table stays empty", family)
		}
	}
}

func mark(seen map[string]bool, key string, ok bool) {
	if ok {
		seen[key] = true
	}
}

// The failing drive must actually fail, and only that one: the whole point of
// the profile is that a "which drive is dying" query has exactly one answer.
func TestOnlyTheFailingDriveReallocatesSectors(t *testing.T) {
	p := Fleet()[2]
	if p.Name != "smart-baremetal" {
		t.Fatalf("expected the baremetal profile, got %s", p.Name)
	}
	g := NewGenerator(p, 1, testFrom, testTo)

	worst := map[string]int64{}
	for ts := testFrom; ts.Before(testTo); ts = ts.Add(6 * time.Hour) {
		for _, a := range g.Scrape(ts, Options{Smart: true}).Smart {
			if a.GetAttrId() == 5 && a.GetRaw() > worst[a.GetDevice()] {
				worst[a.GetDevice()] = a.GetRaw()
			}
		}
	}

	if worst["sdc"] == 0 {
		t.Error("sdc is marked failing but never reallocated a sector")
	}
	for device, raw := range worst {
		if device != "sdc" && raw != 0 {
			t.Errorf("%s is healthy but reallocated %d sectors", device, raw)
		}
	}
}

// A batch must stay inside the hub's body cap. Exceeding it is a 413, which
// no retry fixes.
func TestABatchNeverExceedsTheHubsBodyCap(t *testing.T) {
	for _, p := range Fleet() {
		g := NewGenerator(p, 1, testFrom, testTo)
		h := &host{profile: p, gen: g}

		ts := testTo.Add(-24 * time.Hour)
		for h.pendingRows < maxBatchRows {
			h.append(g.Scrape(ts, Options{Smart: true, Collectors: true, Inventory: true}))
			ts = ts.Add(time.Minute)
		}

		size := proto.Size(h.request(h.pending, true))
		if size > maxBodyBytes {
			t.Errorf("%s: a full batch marshals to %d bytes, over the hub's %d cap", p.Name, size, maxBodyBytes)
		}
	}
}

// The row count the batcher bounds on has to match what is actually sent, or
// the bound means nothing.
func TestRowsMatchesWhatTheRequestCarries(t *testing.T) {
	p := Fleet()[2]
	g := NewGenerator(p, 1, testFrom, testTo)
	s := g.Scrape(testTo.Add(-time.Hour), Options{Smart: true, Collectors: true, Inventory: true})

	h := &host{profile: p, gen: g}
	h.append(s)
	req := h.request(h.pending, true)

	got := len(req.GetHostSamples()) + len(req.GetCpuCores()) + len(req.GetDiskIo()) +
		len(req.GetSensors()) + len(req.GetNet()) + len(req.GetCollectors()) +
		len(req.GetEvents()) + len(req.GetContainers()) + len(req.GetFilesystems()) +
		len(req.GetSmart()) + len(req.GetSystemdEvents()) +
		len(req.GetPackageEvents()) + len(req.GetAddresses()) +
		len(req.GetInterfaces()) + len(req.GetPackages()) +
		len(req.GetSystemdSnapshot().GetUnits())

	if got != s.Rows() {
		t.Errorf("Rows() reports %d but the request carries %d", s.Rows(), got)
	}
}

// The refresh segments have to tile the window exactly: a gap is a range of
// history that never reaches the 5m and 1h tiers, and the raw rows behind it
// are deleted by the retention policy a day later.
func TestRefreshSegmentsTileTheWindowWithoutGaps(t *testing.T) {
	segs := refreshSegments(testFrom, testTo)
	if len(segs) == 0 {
		t.Fatal("no refresh segments for a 90-day window")
	}
	if !segs[0].from.Equal(testFrom) {
		t.Errorf("first segment starts at %s, want %s", segs[0].from, testFrom)
	}
	if !segs[len(segs)-1].to.Equal(testTo) {
		t.Errorf("last segment ends at %s, want %s", segs[len(segs)-1].to, testTo)
	}
	for i := 1; i < len(segs); i++ {
		if !segs[i].from.Equal(segs[i-1].to) {
			t.Fatalf("gap between segment %d (ends %s) and %d (starts %s)",
				i-1, segs[i-1].to, i, segs[i].from)
		}
	}
}

// Tiling in wall-clock time is not enough: refresh_continuous_aggregate
// materialises only the buckets that fall ENTIRELY inside its window, so a
// segment boundary in the middle of a bucket leaves that bucket to nobody.
// A 90-day run lost 18 hourly buckets that way, one per boundary.
func TestEveryBucketIsCoveredBySomeRefreshWindow(t *testing.T) {
	// An origin deliberately off every bucket boundary.
	to := time.Date(2026, 8, 10, 8, 31, 0, 0, time.UTC)
	from := to.Add(-90 * 24 * time.Hour)

	for _, bucket := range []time.Duration{tier5m, tier1h} {
		covered := map[int64]bool{}
		for _, seg := range refreshSegments(from, to) {
			f, e := bucketWindow(seg.from, seg.to, bucket)
			for ts := f; ts.Before(e); ts = ts.Add(bucket) {
				covered[ts.Unix()] = true
			}
		}

		for ts := from.Truncate(bucket); ts.Before(to); ts = ts.Add(bucket) {
			if !covered[ts.Unix()] {
				t.Fatalf("%s bucket at %s is materialised by no refresh window", bucket, ts.UTC())
			}
		}
	}
}

// A window shorter than one bucket is rejected outright by TimescaleDB with
// "refresh window too small", which aborted a short run after all its data
// had already been ingested.
func TestARefreshWindowAlwaysCoversAtLeastOneWholeBucket(t *testing.T) {
	to := time.Date(2026, 8, 10, 13, 50, 0, 0, time.UTC)

	for _, backfill := range []time.Duration{45 * time.Minute, 20 * time.Minute, 2 * time.Minute} {
		from := to.Add(-backfill)
		for _, seg := range refreshSegments(from, to) {
			for _, bucket := range []time.Duration{tier5m, tier1h} {
				f, e := bucketWindow(seg.from, seg.to, bucket)
				if e.Sub(f) < bucket {
					t.Errorf("--backfill %s: %s window [%s,%s) is under one bucket", backfill, bucket, f, e)
				}
				if !f.Truncate(bucket).Equal(f) || !e.Truncate(bucket).Equal(e) {
					t.Errorf("--backfill %s: %s window [%s,%s) is not bucket-aligned", backfill, bucket, f, e)
				}
			}
		}
	}
}

// The coarse refresh segment must stay clear of the raw retention threshold:
// every row in that region is already older than the drop threshold when it
// is written, so the segment width is the entire margin.
func TestTheCoarseRefreshSegmentLeavesMarginAgainstRetention(t *testing.T) {
	if coarseRefreshSegment >= rawRetention {
		t.Fatalf("coarse segment %s is not shorter than raw retention %s: raw chunks can be "+
			"dropped before they are ever materialised", coarseRefreshSegment, rawRetention)
	}
}

func TestTheGridIsCoarseOnlyOutsideTheRawRetentionWindow(t *testing.T) {
	if got := gridStep(testTo.Add(-30*24*time.Hour), testTo); got != 5*time.Minute {
		t.Errorf("30 days back: step %s, want 5m", got)
	}
	if got := gridStep(testTo.Add(-time.Hour), testTo); got != time.Minute {
		t.Errorf("1 hour back: step %s, want 1m", got)
	}
}

// A short window need not contain a midnight, and a daily-only inventory rule
// left host_addresses and host_packages empty for every such run.
func TestInventoryIsSentEvenWhenTheWindowSpansNoMidnight(t *testing.T) {
	to := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	from := to.Add(-6 * time.Hour)

	var sent int
	for ts := from; ts.Before(to); ts = ts.Add(gridStep(ts, to)) {
		if optionsFor(ts, from, to).Inventory {
			sent++
		}
	}
	if sent == 0 {
		t.Fatal("a 6-hour window sent no inventory, so host_addresses and host_packages stay empty")
	}
}

// Over a long window the inventory must go out repeatedly rather than once,
// or last_seen on those rows trails the rest of the history by three months.
func TestInventoryIsRepeatedAcrossALongWindow(t *testing.T) {
	var sent int
	for ts := testFrom; ts.Before(testTo); ts = ts.Add(gridStep(ts, testTo)) {
		if optionsFor(ts, testFrom, testTo).Inventory {
			sent++
		}
	}
	if sent < 80 {
		t.Errorf("inventory sent %d times over 90 days, want roughly daily", sent)
	}
}

// Series names differ only in a trailing index almost everywhere -- "core/0",
// "core/1", "disk/sda", "disk/sdb". Raw FNV-1a leaves the top bits of the
// digest nearly untouched by a change in the last byte, and unit() reads the
// top bits, so those series came out identical to seven decimal places.
func TestSeriesDifferingOnlyInTheLastCharacterAreUncorrelated(t *testing.T) {
	s := newSignal(1)
	ts := testTo

	// The spread across the whole set, not the gap between neighbours: two
	// genuinely random values land close together often enough that a
	// pairwise check would be flaky. With the bug every one of these agreed
	// to within a thousandth.
	lo, hi := 1.0, 0.0
	for i := range 32 {
		v := s.unit(fmt.Sprintf("core/%d", i), ts)
		lo = math.Min(lo, v)
		hi = math.Max(hi, v)
	}
	if hi-lo < 0.5 {
		t.Errorf("32 series named core/N span only [%g,%g]; the hash is not avalanching", lo, hi)
	}
}

// Per-core busy values must actually spread out, which is the visible
// consequence of the hash bug above: a 32-thread host whose cores all report
// the same number makes a per-core heatmap pointless.
func TestPerCoreUtilisationVaries(t *testing.T) {
	p := Fleet()[2]
	cores := NewGenerator(p, 1, testFrom, testTo).Scrape(testTo.Add(-time.Hour), Options{}).Cores

	lo, hi := cores[0].GetBusy(), cores[0].GetBusy()
	for _, c := range cores {
		lo = math.Min(lo, c.GetBusy())
		hi = math.Max(hi, c.GetBusy())
	}
	if hi-lo < 1 {
		t.Errorf("all %d cores report between %g and %g; they are effectively identical", len(cores), lo, hi)
	}
}

// SMART is hourly and collector health is five-minutely. If the grid origin
// is not aligned to those boundaries they never fire outside the fine-grained
// window, which left smart_attributes covering seven days of a ninety-day run.
func TestTheHourlyAndFiveMinuteFamiliesFireInTheCoarseRegion(t *testing.T) {
	to := testTo.Truncate(coarseStep)
	from := to.Add(-90 * 24 * time.Hour).Truncate(coarseStep)
	coarseEnd := to.Add(-rawRetention)

	var smart, collectors int
	for ts := from; ts.Before(coarseEnd); ts = ts.Add(gridStep(ts, to)) {
		opt := optionsFor(ts, from, to)
		if opt.Smart {
			smart++
		}
		if opt.Collectors {
			collectors++
		}
	}
	if smart == 0 {
		t.Error("no SMART sample in the coarse region: the grid never lands on the hour")
	}
	if collectors == 0 {
		t.Error("no collector sample in the coarse region: the grid never lands on a 5-minute boundary")
	}
}

// A host that reports systemd as unavailable must not emit unit events: it
// has no systemd to observe them with.
func TestAHostWithoutSystemdEmitsNoUnitEvents(t *testing.T) {
	for _, p := range Fleet() {
		if p.Capabilities["systemd"] != "unavailable" {
			continue
		}
		if len(p.Units) != 0 {
			t.Errorf("%s reports systemd unavailable but lists %d units", p.Name, len(p.Units))
		}
		sc := newSchedule(p, newSignal(1), testFrom, testTo)
		for _, e := range sc.events {
			if e.unit != nil {
				t.Errorf("%s reports systemd unavailable but emits a unit event for %s", p.Name, e.unit.GetUnitName())
				break
			}
		}
	}
}

// Two events about the same subject at the same timestamp collapse into one
// row: events is unique on (host_id, ts, type, subject) and package_events is
// keyed on (host_id, ts, name). The schedule has to space them further apart
// than the coarsest grid step or the history silently loses transitions.
func TestScheduledEventsAreSpacedWiderThanTheCoarseGridStep(t *testing.T) {
	for _, p := range Fleet() {
		sc := newSchedule(p, newSignal(1), testFrom, testTo)

		type key struct {
			slot    int64
			subject string
		}
		seen := map[key]bool{}
		for _, e := range sc.events {
			var subject string
			switch {
			case e.event != nil:
				subject = "event/" + e.event.GetType() + "/" + e.event.GetSubject()
			case e.pkg != nil:
				subject = "pkg/" + e.pkg.GetName()
			default:
				continue
			}
			// The baseline events all share the window's first instant by
			// design; only later transitions have to be spaced.
			if e.ts.Equal(testFrom) {
				continue
			}
			k := key{slot: e.ts.Truncate(coarseStep).Unix(), subject: subject}
			if seen[k] {
				t.Errorf("%s: two %s events land in the same %s slot and would collapse to one row",
					p.Name, subject, coarseStep)
			}
			seen[k] = true
		}
	}
}

// --hub is a flag and someone will eventually point it at a real hub. Nothing
// the simulator does may then reach a host it did not create: DeleteHost
// cascades a host's entire history, and EnsureHost rotates a live agent's
// token out from under it.
func TestTheSimulatorRefusesToTouchAHostItDidNotCreate(t *testing.T) {
	for _, p := range Fleet() {
		if !strings.HasPrefix(p.Hostname, HostnamePrefix) {
			t.Errorf("%s is named %q, which the safety guard would refuse", p.Name, p.Hostname)
		}
	}

	hub := NewHub("http://127.0.0.1:1", "token")
	if err := hub.DeleteHost(t.Context(), 1, "prod-db-01"); err == nil {
		t.Error("DeleteHost accepted a host the simulator does not manage")
	}
	if _, _, err := hub.EnsureHost(t.Context(), "prod-db-01"); err == nil {
		t.Error("EnsureHost accepted a host the simulator does not manage")
	}
}

// A batch too large for the hub is split in half and re-posted -- which
// throws away the request that was already built. Everything that build
// consumed has to be given back, or the metadata block goes out with a
// request that is never sent and the hub never learns the host's arch,
// kernel or capabilities.
func TestSplittingAnOversizedBatchStillDeliversTheMetadata(t *testing.T) {
	var posts, withMetadata int
	var seqs []uint64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req netrav1.IngestRequest
		if err := proto.Unmarshal(raw, &req); err != nil {
			t.Errorf("hub got an unparseable body: %v", err)
		}
		if len(raw) > maxBodyBytes {
			t.Errorf("a POST of %d bytes exceeds the hub's %d cap", len(raw), maxBodyBytes)
		}
		posts++
		seqs = append(seqs, req.GetSeq())
		if req.GetMetadata() != nil {
			withMetadata++
		}
		out, _ := proto.Marshal(&netrav1.IngestResponse{AckSeq: req.GetSeq()})
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	p := Fleet()[2]
	g := NewGenerator(p, 1, testFrom, testTo)
	meta := p.Metadata("sim", "sim", "simulated")
	h := &host{profile: p, gen: g, meta: meta, metaHash: HashMetadata(meta), sendMeta: true}

	// Enough scrapes to push the marshalled request past the split threshold.
	opt := Options{Smart: true, Collectors: true, Inventory: true}
	for ts := testTo.Add(-2000 * time.Minute); ts.Before(testTo); ts = ts.Add(time.Minute) {
		h.append(g.Scrape(ts, opt))
	}
	if proto.Size(h.request(h.pending, true)) <= maxBodyBytes-bodyHeadroom {
		t.Fatal("the test batch is not large enough to trigger a split")
	}
	// request() above consumed the one-shot metadata; restore it so the flush
	// under test starts from the same state a real run would.
	h.sendMeta = true
	h.seq = 0

	if err := h.flush(t.Context(), NewHub(srv.URL, "admin"), true); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if posts < 2 {
		t.Fatalf("the batch was posted in %d request(s); the split never happened", posts)
	}
	if withMetadata != 1 {
		t.Errorf("%d of %d posts carried the metadata block, want exactly 1", withMetadata, posts)
	}
	// Sequence numbers must stay dense: the hub acks by echoing them, and a
	// gap left by a rolled-back build would ack a batch that was never sent.
	for i, seq := range seqs {
		if seq != uint64(i+1) {
			t.Errorf("post %d used seq %d, want %d", i, seq, i+1)
		}
	}
}

// The inventory and the event log are two halves of the same answer.
// store.UpsertHostPackages exists to keep them consistent, so a fixture whose
// events say a package was installed while its inventory has never heard of
// it is a state no real hub can produce.
func TestTheInventoryAgreesWithThePackageEvents(t *testing.T) {
	p := Fleet()[1]
	g := NewGenerator(p, 1, testFrom, testTo)

	// Walk the window so the schedule's cursor passes every event, then read
	// the inventory as of the end.
	for ts := testFrom; ts.Before(testTo); ts = ts.Add(6 * time.Hour) {
		g.Scrape(ts, Options{})
	}
	inventory := map[string]string{}
	for _, pkg := range g.packages(testTo) {
		inventory[pkg.GetName()] = pkg.GetVersion()
	}

	var checked int
	for _, e := range g.sched.events {
		if e.pkg == nil || e.ts.After(testTo) {
			continue
		}
		name, want := e.pkg.GetName(), e.pkg.GetToVersion()
		got, present := inventory[name]
		if !present {
			t.Errorf("%s %s at %s, but it is absent from the inventory",
				e.pkg.GetAction(), name, e.ts.Format(time.RFC3339))
			continue
		}
		// Only the LAST event for a package fixes its version; earlier ones
		// are superseded.
		if last := lastEventFor(g.sched, name, testTo); last == want && got != want {
			t.Errorf("%s was %s %s -> %s, but the inventory still reports %s",
				name, e.pkg.GetAction(), e.pkg.GetFromVersion(), want, got)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no package events in the window; the test proves nothing")
	}
}

func lastEventFor(sc *schedule, name string, until time.Time) string {
	var version string
	for _, e := range sc.events {
		if e.ts.After(until) {
			break
		}
		if e.pkg != nil && e.pkg.GetName() == name {
			version = e.pkg.GetToVersion()
		}
	}
	return version
}

// A package upgraded twice must not report the same from_version both times,
// and its version must not grow a second suffix.
func TestRepeatedUpgradesChainTheirVersions(t *testing.T) {
	v := "1.2.3-1"
	for range 3 {
		v = bumpVersion(v)
	}
	if v != "1.2.3-1+deb12u3" {
		t.Errorf("three upgrades produced %q, want 1.2.3-1+deb12u3", v)
	}
}

// Every family a profile emits must have its collector in the list
// collector_samples is built from, or the health table contradicts the data
// table.
func TestAProfileNeverEmitsAFamilyItsCollectorListDoesNotCover(t *testing.T) {
	opt := Options{Smart: true, Collectors: true, Inventory: true}

	for _, p := range Fleet() {
		s := NewGenerator(p, 1, testFrom, testTo).Scrape(testTo.Add(-time.Hour), opt)

		for _, check := range []struct {
			collector string
			emitted   bool
		}{
			{"smart", len(s.Smart) > 0},
			{"sensors", len(s.Sensors) > 0},
			{"containers", len(s.Containers) > 0},
			{"filesystems", len(s.Filesystems) > 0},
			{"diskio", len(s.Disks) > 0},
			{"network", len(s.Nets) > 0},
		} {
			if check.emitted && !p.runsCollector(check.collector) {
				t.Errorf("%s emits %s rows but does not list the %s collector",
					p.Name, check.collector, check.collector)
			}
		}
	}
}

// The prefix guard covered sites and providers as well as hosts, because
// EnsureSite matched an existing site by name and an unguarded run against a
// real hub would have attached its invented hosts to a real one. Sites are
// gone, and with them that whole class of collateral: the simulator now writes
// its location into each host's own row through the metadata it already sends,
// so the only thing it can touch is a host it created, which checkSimulated
// still guards by hostname.

// A 503 carries retry_after_s precisely so the client waits and re-posts.
// Treating it as fatal threw away a ninety-day backfill over a condition the
// protocol defines as transient.
func TestATransientHubFailureIsRetriedRatherThanKillingTheRun(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req netrav1.IngestRequest
		_ = proto.Unmarshal(raw, &req)
		attempts++

		w.Header().Set("Content-Type", "application/x-protobuf")
		if attempts == 1 {
			// One second, so the test does not sit through the default.
			out, _ := proto.Marshal(&netrav1.IngestResponse{RetryAfterS: 1})
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write(out)
			return
		}
		out, _ := proto.Marshal(&netrav1.IngestResponse{AckSeq: req.GetSeq()})
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	hub := NewHub(srv.URL, "admin")
	resp, err := hub.Ingest(t.Context(), "nta_x", &netrav1.IngestRequest{Seq: 1})
	if err != nil {
		t.Fatalf("a 503 with retry_after_s killed the run: %v", err)
	}
	if resp.GetAckSeq() != 1 {
		t.Errorf("acked seq %d, want 1", resp.GetAckSeq())
	}
	if attempts != 2 {
		t.Errorf("hub saw %d attempts, want 2", attempts)
	}
}

// A permanent failure must still be fatal: retrying a 401 forever would hang
// a run behind a token that is never coming back.
func TestAPermanentFailureIsNotRetried(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := NewHub(srv.URL, "admin").Ingest(t.Context(), "nta_x", &netrav1.IngestRequest{Seq: 1}); err == nil {
		t.Fatal("a 401 was treated as success")
	}
	if attempts != 1 {
		t.Errorf("a 401 was retried %d times; it can never succeed", attempts)
	}
}

// Live mode runs past the backfill window, and the schedule has to run with
// it: a process left running overnight that never emits another event makes
// the events UI look permanently empty.
func TestLiveModeStillHasEventsAfterTheBackfillWindowEnds(t *testing.T) {
	p := Fleet()[2]
	g := NewGenerator(p, 1, testFrom, testTo)

	// Consume everything inside the backfill window first.
	for ts := testFrom; ts.Before(testTo); ts = ts.Add(30 * time.Minute) {
		g.Scrape(ts, Options{})
	}

	var events int
	for ts := testTo; ts.Before(testTo.Add(30 * 24 * time.Hour)); ts = ts.Add(30 * time.Minute) {
		s := g.Scrape(ts, Options{})
		events += len(s.Events) + len(s.SystemdEvents) + len(s.PackageEvents)
	}
	if events == 0 {
		t.Error("no discrete event in 30 days of live mode; the event tables would stay frozen")
	}
}

func TestByNameRejectsAProfileThatDoesNotExist(t *testing.T) {
	if _, err := ByName([]string{"rpi5", "nope"}); err == nil {
		t.Fatal("an unknown profile must be an error, not a silently empty fleet")
	}
	got, err := ByName([]string{"minimal-vps", "rpi5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Fleet order, not argument order: the run is reproducible either way.
	if len(got) != 2 || got[0].Name != "rpi5" || got[1].Name != "minimal-vps" {
		t.Errorf("got %v, want [rpi5 minimal-vps] in fleet order", names(got))
	}
}

func names(ps []*Profile) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// A unit that fails has to recover, and services_failed has to agree with the
// events that produced it.
func TestAFailedUnitRecoversAndIsCountedWhileItIsDown(t *testing.T) {
	p := Fleet()[1]
	sc := newSchedule(p, newSignal(1), testFrom, testTo)

	var down, up int
	for _, e := range sc.events {
		if e.unit == nil {
			continue
		}
		if e.unit.GetState() == "failed" {
			down++
		} else {
			up++
		}
	}
	if down == 0 {
		t.Fatal("no unit ever fails, so systemd_unit_events records no transition")
	}
	// One baseline "active" per unit, plus one recovery per failure.
	if want := len(p.Units) + down; up != want {
		t.Errorf("got %d active events, want %d (%d baseline + %d recoveries)", up, want, len(p.Units), down)
	}

	firstFailure := firstFailedAt(sc)
	if got := sc.failedUnits(firstFailure); got != 1 {
		t.Errorf("at the first failure services_failed is %d, want 1", got)
	}
	if got := sc.failedUnits(firstFailure.Add(-time.Minute)); got != 0 {
		t.Errorf("before the first failure services_failed is %d, want 0", got)
	}
}

func firstFailedAt(sc *schedule) time.Time {
	for _, e := range sc.events {
		if e.unit != nil && e.unit.GetState() == "failed" {
			return e.ts
		}
	}
	return time.Time{}
}

// Every event has to be delivered exactly once as the grid advances: twice
// would be a duplicate row, never would be a table that stays empty.
func TestEveryScheduledEventIsDeliveredExactlyOnce(t *testing.T) {
	p := Fleet()[2]
	sc := newSchedule(p, newSignal(1), testFrom, testTo)
	total := len(sc.events)

	var delivered int
	for ts := testFrom; ts.Before(testTo); ts = ts.Add(time.Hour) {
		delivered += len(sc.due(ts))
	}
	delivered += len(sc.due(testTo))

	if delivered != total {
		t.Errorf("delivered %d of %d scheduled events", delivered, total)
	}
}

// The metadata hash has to be stable across calls, or the hub asks for a
// resend on every single POST.
func TestTheMetadataHashIsStable(t *testing.T) {
	p := Fleet()[0]
	a := HashMetadata(p.Metadata("sim", "sim", "simulated"))
	b := HashMetadata(p.Metadata("sim", "sim", "simulated"))
	if string(a) != string(b) {
		t.Fatalf("metadata hash is not stable: %x vs %x", a, b)
	}
	if len(a) != 8 {
		t.Errorf("metadata hash is %d bytes, the wire field is 8", len(a))
	}
}

// Every container key the simulator emits must be compose project/service,
// the shape a real agent produces (internal/agent/collector/containers.go:
// `m.Project + "/" + m.Service`). They were written with an underscore for a
// while, which no agent has ever sent -- so the UI, which splits on the
// slash to name the project, correctly showed no project for every row in
// the fleet. A simulator shaped differently from the wire tests the wrong
// thing, quietly.
func TestEveryContainerKeyIsComposeProjectAndService(t *testing.T) {
	for _, profile := range Fleet() {
		for _, container := range profile.Containers {
			project, service, ok := strings.Cut(container.Key, "/")
			if !ok || project == "" || service == "" {
				t.Errorf(
					"%s: container key %q is not project/service; a real agent never emits this",
					profile.Hostname, container.Key,
				)
			}
		}
	}
}

// A package's versions form a chain: each upgrade starts from whatever the
// previous one left behind. Nothing enforced it across the two upgrade
// schedules, and the dist-upgrade run broke it -- the weekly runs were laid
// out over the whole window first, so the dist-upgrade events, dated near the
// beginning, read end-of-window versions and the log showed a package moving
// backwards. packageStateAt replays by ts, so the rendered inventory could
// settle below a version an earlier event had already reported.
func TestPackageUpgradesFormOneChainPerPackage(t *testing.T) {
	// Given: a profile with packages, over a window long enough to contain
	// several weekly runs AND at least one dist-upgrade.
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(60 * 24 * time.Hour)

	for _, p := range Fleet() {
		if len(p.Packages) == 0 {
			continue
		}

		// When: the schedule is laid out.
		evs := packageChanges(p, newSignal(1), from, to)
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].ts.Before(evs[j].ts) })

		// Then: read in time order, every upgrade starts where the previous
		// one for that package ended.
		last := map[string]string{}
		var sawDistUpgrade bool
		byTs := map[time.Time]int{}

		for _, e := range evs {
			if e.pkg == nil || e.pkg.GetAction() != "upgrade" {
				continue
			}
			byTs[e.ts]++
			name := e.pkg.GetName()
			if prev, seen := last[name]; seen && e.pkg.GetFromVersion() != prev {
				t.Fatalf("%s: %s upgrade at %s starts from %q, but the previous "+
					"upgrade left it at %q -- the version chain is broken",
					p.Hostname, name, e.ts, e.pkg.GetFromVersion(), prev)
			}
			last[name] = e.pkg.GetToVersion()
		}

		for _, n := range byTs {
			if n > packageRunRowsForTest {
				sawDistUpgrade = true
			}
		}
		if !sawDistUpgrade {
			t.Errorf("%s: no run larger than %d packages in 60 days -- the events "+
				"log's fold is unreachable in the simulator", p.Hostname, packageRunRowsForTest)
		}
	}
}

// The events log caps one apt run at this many rows (packageRunRows in
// internal/hub/read/events.go). Duplicated rather than imported because the
// hub is not a dependency of the simulator; the test above exists to catch the
// simulator drifting under it.
const packageRunRowsForTest = 3
