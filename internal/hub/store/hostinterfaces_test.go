package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// link builds one HostInterface with the absent fields genuinely absent, so a
// test asserting "NULL, not zero" is asserting about what the agent sent
// rather than about a helper's defaults.
func link(iface string, opts ...func(*netrav1.HostInterface)) *netrav1.HostInterface {
	l := &netrav1.HostInterface{Iface: iface}
	for _, o := range opts {
		o(l)
	}
	return l
}

func upHundredMeg(l *netrav1.HostInterface) {
	l.OperState = "up"
	l.SpeedMbps = proto.Uint64(1000)
	l.Duplex = "full"
	l.Mtu = proto.Uint32(1500)
	l.Mac = "52:54:00:3a:1c:07"
}

func seedInterfaceHost(t *testing.T, s *store.Store, hostname string) int32 {
	t.Helper()
	var id int32
	if err := s.Pool().QueryRow(context.Background(),
		`INSERT INTO hosts (hostname) VALUES ($1) RETURNING id`, hostname).Scan(&id); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	return id
}

func openMigrated(t *testing.T) *store.Store {
	t.Helper()
	s := store.OpenTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// The whole-set replacement, which is what makes this an upsert rather than an
// append: the agent sends every interface whenever any of them changes, so
// anything the newest set omits is an interface the host no longer has.
func TestIntegrationUpsertHostInterfacesReplacesTheSet(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "links")

	// Given: three interfaces.
	first := []*netrav1.HostInterface{
		link("eth0", upHundredMeg),
		link("eth1", func(l *netrav1.HostInterface) { l.OperState = "down" }),
		link("lo", func(l *netrav1.HostInterface) {
			l.OperState = "unknown"
			l.Mtu = proto.Uint32(65536)
		}),
	}
	if _, err := s.UpsertHostInterfaces(ctx, id, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// When: eth1 is removed from the reported set.
	second := []*netrav1.HostInterface{first[0], first[2]}
	if _, err := s.UpsertHostInterfaces(ctx, id, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	// Then: it is gone rather than left behind as a link the host still has.
	var names []string
	rows, err := s.Pool().Query(ctx,
		`SELECT iface FROM host_interfaces WHERE host_id = $1 ORDER BY iface`, id)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}
	if len(names) != 2 || names[0] != "eth0" || names[1] != "lo" {
		t.Errorf("interfaces = %v, want [eth0 lo]; a pruned interface that survives is a "+
			"link the host does not have", names)
	}
}

// An interface's attributes change without its name changing -- a cable is
// pulled and operstate drops while speed becomes unreadable -- and that is the
// change most worth storing correctly.
func TestIntegrationUpsertHostInterfacesUpdatesInPlace(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "unplugged")

	if _, err := s.UpsertHostInterfaces(ctx, id,
		[]*netrav1.HostInterface{link("eth0", upHundredMeg)}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// When: the cable is pulled. operstate drops and the kernel stops
	// answering for speed and duplex -- both absent, not zero.
	pulled := link("eth0", func(l *netrav1.HostInterface) {
		l.OperState = "down"
		l.Mtu = proto.Uint32(1500)
		l.Mac = "52:54:00:3a:1c:07"
	})
	if _, err := s.UpsertHostInterfaces(ctx, id,
		[]*netrav1.HostInterface{pulled}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var state *string
	var speed *int64
	var duplex *string
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT oper_state, speed_mbps, duplex, count(*) OVER ()
		   FROM host_interfaces WHERE host_id = $1`, id,
	).Scan(&state, &speed, &duplex, &n); err != nil {
		t.Fatalf("query: %v", err)
	}

	if n != 1 {
		t.Errorf("rows = %d, want 1; the second report must update the row rather than add one", n)
	}
	if state == nil || *state != "down" {
		t.Errorf("oper_state = %v, want down", state)
	}
	// The reading that used to be 1000 must go back to absent, not stay at
	// its last known value: a down link has no speed, and reporting the old
	// one asserts a measurement nobody took.
	if speed != nil {
		t.Errorf("speed_mbps = %d, want NULL once the link is down", *speed)
	}
	if duplex != nil {
		t.Errorf("duplex = %q, want NULL once the link is down", *duplex)
	}
}

// proto3 has no absent string, so an interface with no MAC and one whose MAC
// could not be read both arrive as "". Stored verbatim, that ” is a measured
// empty value and every `?? ABSENT` in the UI becomes dead code -- a column of
// blank cells where it meant to say "not reported".
func TestIntegrationUpsertHostInterfacesStoresEmptyStringsAsNull(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "virtual")

	// Given: a device with no link layer, no alias and no duplex -- every
	// text field empty on the wire.
	if _, err := s.UpsertHostInterfaces(ctx, id, []*netrav1.HostInterface{
		link("wg0", func(l *netrav1.HostInterface) {
			l.OperState = "unknown"
			l.Mtu = proto.Uint32(1420)
		}),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var mac, duplex, description *string
	if err := s.Pool().QueryRow(ctx,
		`SELECT mac, duplex, description FROM host_interfaces WHERE host_id = $1`, id,
	).Scan(&mac, &duplex, &description); err != nil {
		t.Fatalf("query: %v", err)
	}

	for name, got := range map[string]*string{
		"mac": mac, "duplex": duplex, "description": description,
	} {
		if got != nil {
			t.Errorf("%s = %q, want NULL: an empty string reads as a measured empty value",
				name, *got)
		}
	}
}

// An empty set is "unchanged", not "the host has no interfaces". The collector
// reports nothing when nothing changed, and acting on that would delete every
// row on every quiet scrape.
func TestIntegrationUpsertHostInterfacesIgnoresAnEmptySet(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "quiet")

	if _, err := s.UpsertHostInterfaces(ctx, id,
		[]*netrav1.HostInterface{link("eth0", upHundredMeg)}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := s.UpsertHostInterfaces(ctx, id, nil); err != nil {
		t.Fatalf("empty upsert: %v", err)
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_interfaces WHERE host_id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rows = %d, want 1; an empty report means unchanged, not gone", n)
	}
}

// One host's interfaces must not prune another's. eth0 exists on every host in
// a fleet, and the prune is keyed on iface alone within a host_id.
func TestIntegrationUpsertHostInterfacesPrunesWithinOneHost(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	a := seedInterfaceHost(t, s, "host-a")
	b := seedInterfaceHost(t, s, "host-b")

	for _, id := range []int32{a, b} {
		if _, err := s.UpsertHostInterfaces(ctx, id, []*netrav1.HostInterface{
			link("eth0", upHundredMeg),
			link("eth1", func(l *netrav1.HostInterface) { l.OperState = "down" }),
		}); err != nil {
			t.Fatalf("seed %d: %v", id, err)
		}
	}

	// When: host A loses eth1.
	if _, err := s.UpsertHostInterfaces(ctx, a,
		[]*netrav1.HostInterface{link("eth0", upHundredMeg)}); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var bCount int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_interfaces WHERE host_id = $1`, b).Scan(&bCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if bCount != 2 {
		t.Errorf("host B has %d interfaces, want 2; one host's prune reached another's rows", bCount)
	}
}

// The third host-level sample table. A sample with none of the seven counters
// set is skipped rather than written as a row of NULLs -- which is every first
// scrape after an agent restart, because a rate needs a baseline.
func TestIntegrationInsertHostProtoSamplesSkipsEmptySamples(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "proto")

	const ts = 1_754_784_000_000
	samples := []*netrav1.HostSample{
		// The first scrape after a restart: a host row with no rates on it.
		{TsMs: ts},
		{TsMs: ts + 60_000, TcpInSegsPerS: proto.Float64(4200)},
	}

	n, err := s.InsertHostProtoSamples(ctx, id, samples)
	if err != nil {
		t.Fatalf("InsertHostProtoSamples: %v", err)
	}
	if n != 1 {
		t.Errorf("inserted %d rows, want 1; an all-NULL row claims a measurement was taken", n)
	}

	var segs *float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT tcp_in_segs_per_s FROM host_proto_samples WHERE host_id = $1`, id).Scan(&segs); err != nil {
		t.Fatalf("query: %v", err)
	}
	if segs == nil || *segs != 4200 {
		t.Errorf("tcp_in_segs_per_s = %v, want 4200", segs)
	}
}

// Replayed batches collide on (host_id, ts) and are discarded, so a retry
// after a partial failure costs one repeated POST rather than duplicating.
func TestIntegrationInsertHostProtoSamplesDedupesAReplay(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "replay")

	sample := &netrav1.HostSample{
		TsMs:                 1_754_784_000_000,
		TcpOutSegsPerS:       proto.Float64(4800),
		UdpInDatagramsPerS:   proto.Float64(640),
		Udp6OutDatagramsPerS: proto.Float64(38),
	}

	if _, err := s.InsertHostProtoSamples(ctx, id, []*netrav1.HostSample{sample}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	n, err := s.InsertHostProtoSamples(ctx, id, []*netrav1.HostSample{sample})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n != 0 {
		t.Errorf("replay inserted %d rows, want 0", n)
	}

	var rows int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_proto_samples WHERE host_id = $1`, id).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1", rows)
	}
}

// Nothing to insert is not an error, and must not send an empty batch.
func TestIntegrationInsertHostProtoSamplesAcceptsNothing(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "silent")

	n, err := s.InsertHostProtoSamples(ctx, id, nil)
	if err != nil {
		t.Fatalf("InsertHostProtoSamples: %v", err)
	}
	if n != 0 {
		t.Errorf("inserted %d, want 0", n)
	}
}

// devices is the one inventory table that cannot prune by set difference, and
// the reason is the cascade: smart_attributes references it ON DELETE CASCADE,
// and the collector drops a single drive it cannot read while reporting the
// others. Deleting whatever the newest report omits would therefore let one
// unreadable drive on one scrape destroy ninety days of its own readings.
//
// So the prune is on a timestamp with a horizon far longer than any transient
// failure, and these tests pin both halves of that: reporting a drive keeps it
// alive, and only real silence removes it.
func TestIntegrationReportingADriveKeepsItAlive(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "drives")

	smart := func(at time.Time, raw int64) *netrav1.SmartAttribute {
		return &netrav1.SmartAttribute{
			TsMs: at.UnixMilli(), Device: "sda",
			Model: "ST16000NM000J", Serial: "ZR5A1M0K",
			AttrId: 5, Raw: proto.Int64(raw),
		}
	}

	// A reading from long past any horizon, then the drive reported again now.
	if _, err := s.InsertSmartAttributes(ctx, id,
		[]*netrav1.SmartAttribute{
			smart(time.Now().Add(-200*24*time.Hour), 0),
		}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := s.InsertSmartAttributes(ctx, id,
		[]*netrav1.SmartAttribute{smart(time.Now(), 1)}); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM devices WHERE host_id = $1`, id).Scan(&lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if age := time.Since(lastSeen); age > time.Minute {
		t.Errorf("last_seen is %s old after the drive was reported again; "+
			"the prune would eventually delete a live drive's history", age)
	}
}

// last_seen is stamped from the READING's timestamp, not from the hub's clock.
//
// The agent's ring buffer replays buffered scrapes after a hub outage, so a
// batch landing now can carry readings taken overnight. now() would date them
// to arrival and report the drive as read "just now" -- the exact opposite of
// the staleness cue the Drives table reads this column for.
func TestIntegrationDeviceLastSeenComesFromTheReadingNotTheClock(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "replayed")

	sixHoursAgo := time.Now().Add(-6 * time.Hour)
	if _, err := s.InsertSmartAttributes(ctx, id, []*netrav1.SmartAttribute{{
		TsMs: sixHoursAgo.UnixMilli(), Device: "sda",
		Model: "ST16000NM000J", Serial: "A", AttrId: 5, Raw: proto.Int64(0),
	}}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM devices WHERE host_id = $1`, id).Scan(&lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if age := time.Since(lastSeen); age < 5*time.Hour {
		t.Errorf("last_seen is only %s old for a reading taken six hours ago; "+
			"a replayed scrape must not report the drive as read just now", age)
	}
}

// And a replay of OLDER readings must not walk it backwards, which would hand
// the prune a drive that looks stale while its newest reading is current.
func TestIntegrationDeviceLastSeenNeverMovesBackwards(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "out-of-order")

	row := func(at time.Time, raw int64) *netrav1.SmartAttribute {
		return &netrav1.SmartAttribute{
			TsMs: at.UnixMilli(), Device: "sda",
			Model: "ST16000NM000J", Serial: "A", AttrId: 5, Raw: proto.Int64(raw),
		}
	}

	current := time.Now()
	if _, err := s.InsertSmartAttributes(ctx, id,
		[]*netrav1.SmartAttribute{row(current, 1)}); err != nil {
		t.Fatalf("current insert: %v", err)
	}
	// A late batch carrying week-old readings.
	if _, err := s.InsertSmartAttributes(ctx, id,
		[]*netrav1.SmartAttribute{row(current.Add(-7*24*time.Hour), 0)}); err != nil {
		t.Fatalf("late insert: %v", err)
	}

	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM devices WHERE host_id = $1`, id).Scan(&lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if age := time.Since(lastSeen); age > time.Minute {
		t.Errorf("last_seen moved back to %s old after a late batch of older "+
			"readings; the newest reading is current", age)
	}
}

// The prune itself, called directly rather than waited on: it is registered as
// a daily job, and a test that slept for one would be a test nobody runs.
func TestIntegrationStaleDevicesArePrunedAndLiveOnesAreNot(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "mixed")

	// Real time, not a fixed literal: last_seen is stamped from the reading's
	// ts now, so a hardcoded instant becomes stale as the calendar moves and
	// the test would eventually prune the drive it means to keep.
	now := time.Now().UnixMilli()
	rows := []*netrav1.SmartAttribute{
		{
			TsMs: now, Device: "sda", Model: "ST16000NM000J",
			Serial: "A", AttrId: 5, Raw: proto.Int64(0),
		},
		{
			TsMs: now, Device: "sdb", Model: "ST16000NM000J",
			Serial: "B", AttrId: 5, Raw: proto.Int64(0),
		},
	}
	if _, err := s.InsertSmartAttributes(ctx, id, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// sdb was pulled out of the machine most of a year ago.
	//
	// BOTH columns, because the prune requires both: last_seen is the agent's
	// clock and first_seen is the hub's, and a row is only stale when it is
	// stale by both. Ageing last_seen alone leaves sdb alive, which is the
	// guard doing its job rather than the fixture being wrong.
	if _, err := s.Pool().Exec(ctx,
		`UPDATE devices
		    SET last_seen = now() - INTERVAL '200 days',
		        first_seen = now() - INTERVAL '400 days'
		  WHERE host_id = $1 AND device = 'sdb'`, id); err != nil {
		t.Fatalf("age sdb: %v", err)
	}

	if _, err := s.Pool().Exec(ctx,
		`CALL netra_prune_stale_devices(0, '{"retention": "120 days"}'::jsonb)`); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var names []string
	pgRows, err := s.Pool().Query(ctx,
		`SELECT device FROM devices WHERE host_id = $1 ORDER BY device`, id)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer pgRows.Close()
	for pgRows.Next() {
		var n string
		if err := pgRows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}

	if len(names) != 1 || names[0] != "sda" {
		t.Errorf("devices = %v, want [sda]; the drive still being reported must "+
			"survive and the one silent for four months must not", names)
	}

	// The cascade took sdb's readings with it. At this horizon there were none
	// left to take -- smart_attributes' own retention drops them at 90 days --
	// but the row count is what proves the FK is doing what the migration says.
	var attrs int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM smart_attributes WHERE host_id = $1`, id).Scan(&attrs); err != nil {
		t.Fatalf("count attributes: %v", err)
	}
	if attrs != 1 {
		t.Errorf("smart_attributes = %d, want 1 (sda's); sdb's went with its device row", attrs)
	}
}

// A host whose clock is months behind must not have its drives deleted and
// re-created for ever.
//
// last_seen comes from the agent's ts, and the hub accepts anything inside
// [2020-01-01, now+1h] -- so an RTC-less box before NTP settles, or a restored
// snapshot, reports readings stamped long before any cutoff. Keyed on
// last_seen alone the drive is inserted already stale, deleted that night with
// its readings, re-created by the next scrape and deleted again. GREATEST does
// not help: it protects a row that already holds a newer value, and a
// first-seen row holds nothing.
//
// first_seen is a HUB timestamp and cannot be moved by a bad agent clock, so
// the prune requires both.
func TestIntegrationADriveFromAHostWithASkewedClockSurvives(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "skewed")

	// Two years behind, and still inside the hub's plausibility window.
	stamped := time.Now().Add(-2 * 365 * 24 * time.Hour)
	if _, err := s.InsertSmartAttributes(ctx, id, []*netrav1.SmartAttribute{{
		TsMs: stamped.UnixMilli(), Device: "sda",
		Model: "ST16000NM000J", Serial: "A", AttrId: 5, Raw: proto.Int64(0),
	}}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// last_seen is honestly ancient -- that is what the agent reported.
	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM devices WHERE host_id = $1`, id).Scan(&lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if time.Since(lastSeen) < 300*24*time.Hour {
		t.Fatalf("last_seen = %s; the fixture is meant to be years old", lastSeen)
	}

	if _, err := s.Pool().Exec(ctx,
		`CALL netra_prune_stale_devices(0, '{"retention": "120 days"}'::jsonb)`); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var devices, attrs int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM devices WHERE host_id = $1`, id).Scan(&devices); err != nil {
		t.Fatalf("count devices: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM smart_attributes WHERE host_id = $1`, id).Scan(&attrs); err != nil {
		t.Fatalf("count attributes: %v", err)
	}

	if devices != 1 {
		t.Errorf("devices = %d, want 1; a drive the hub has only just learned about "+
			"was deleted because the host's clock is wrong", devices)
	}
	if attrs != 1 {
		t.Errorf("smart_attributes = %d, want 1; the cascade took a live drive's "+
			"readings with it", attrs)
	}
}
