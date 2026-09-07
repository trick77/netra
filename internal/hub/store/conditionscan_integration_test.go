package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
	"github.com/trick77/netra/internal/hub/store"
)

// seedFilesystem inserts a mount and its current reading, and returns the fs
// id so a caller can write the samples the onset walk reads.
func seedFilesystem(t *testing.T, ctx context.Context, s *store.Store,
	host int32, label, mountpoint string, used, free int64, at time.Time) int32 {
	t.Helper()
	var fsID int32
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO filesystems (host_id, label, mountpoint)
		VALUES ($1, $2, $3) RETURNING id`, host, label, mountpoint).Scan(&fsID); err != nil {
		t.Fatalf("filesystems: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO filesystem_current (host_id, fs_id, ts, total, used, free)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		host, fsID, at, used+free, used, free); err != nil {
		t.Fatalf("filesystem_current: %v", err)
	}
	return fsID
}

func seedFilesystemSample(t *testing.T, ctx context.Context, s *store.Store,
	host, fsID int32, at time.Time, used, free int64) {
	t.Helper()
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO filesystem_samples (host_id, ts, fs_id, total, used, free)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		host, at, fsID, used+free, used, free); err != nil {
		t.Fatalf("filesystem_samples: %v", err)
	}
}

func seedHostCurrent(t *testing.T, ctx context.Context, s *store.Store, host int32, lastSeen time.Time) {
	t.Helper()
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, $2)`, host, lastSeen); err != nil {
		t.Fatalf("host_current: %v", err)
	}
}

const gib = int64(1024) * 1024 * 1024

// The onset is WALKED back to the sample the mount crossed on, not taken from
// the newest reading.
//
// This is the payoff of the whole engine: a disk that filled at 03:00 says so,
// rather than saying it filled the moment netra happened to look.
func TestIntegrationDiskOnsetIsWalkedBackToTheCrossing(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-onset")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	// 97 GiB used, 3 GiB free: over both halves of the compound rule.
	full, room := 97*gib, 3*gib
	// And a reading with plenty of room, which is what ends the walk.
	empty, space := 40*gib, 60*gib

	fsID := seedFilesystem(t, ctx, s, host, "root", "/", full, room, now)

	// Six samples a minute apart. The oldest two are healthy; the mount crossed
	// on the third and has been over the line ever since.
	crossing := now.Add(-3 * time.Minute)
	seedFilesystemSample(t, ctx, s, host, fsID, now.Add(-5*time.Minute), empty, space)
	seedFilesystemSample(t, ctx, s, host, fsID, now.Add(-4*time.Minute), empty, space)
	seedFilesystemSample(t, ctx, s, host, fsID, crossing, full, room)
	seedFilesystemSample(t, ctx, s, host, fsID, now.Add(-2*time.Minute), full, room)
	seedFilesystemSample(t, ctx, s, host, fsID, now.Add(-time.Minute), full, room)
	seedFilesystemSample(t, ctx, s, host, fsID, now, full, room)

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	f, bad := scan.Bad[diskKey(host, "root")]
	if !bad {
		t.Fatalf("the full mount was not flagged: %+v", scan.Bad)
	}
	if !f.OpenedTS.UTC().Equal(crossing) {
		t.Errorf("onset = %v, want the sample it crossed on %v", f.OpenedTS.UTC(), crossing)
	}
	if f.OpenedAtLeast {
		t.Error("opened_at_least is set, but the walk SAW the mount below the line " +
			"-- this is a moment, not a floor")
	}
}

// A gap in the middle of a fill is a gap in the evidence, not a recovery.
//
// crossedAt skipped unreadable buckets for exactly this reason: an outage
// halfway through a disk filling must not restart the clock at whatever came
// after it.
func TestIntegrationDiskOnsetSkipsAGapRatherThanRestarting(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-onset-gap")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	full, room := 97*gib, 3*gib
	empty, space := 40*gib, 60*gib
	fsID := seedFilesystem(t, ctx, s, host, "root", "/", full, room, now)

	crossing := now.Add(-4 * time.Minute)
	seedFilesystemSample(t, ctx, s, host, fsID, now.Add(-5*time.Minute), empty, space)
	seedFilesystemSample(t, ctx, s, host, fsID, crossing, full, room)
	// The agent was away here: a row with nothing measurable in it.
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO filesystem_samples (host_id, ts, fs_id, total, used, free)
		VALUES ($1, $2, $3, NULL, NULL, NULL)`,
		host, now.Add(-3*time.Minute), fsID); err != nil {
		t.Fatalf("gap sample: %v", err)
	}
	seedFilesystemSample(t, ctx, s, host, fsID, now.Add(-2*time.Minute), full, room)
	seedFilesystemSample(t, ctx, s, host, fsID, now, full, room)

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	f := scan.Bad[diskKey(host, "root")]
	if !f.OpenedTS.UTC().Equal(crossing) {
		t.Errorf("onset = %v, want %v -- the gap must not restart the clock",
			f.OpenedTS.UTC(), crossing)
	}
}

// Run out of retained samples and the onset is a FLOOR, marked as one.
//
// Raw retention is 7 days, so a mount that was already full at the far end of
// what netra kept cannot be dated. The row says "over" rather than naming a
// bucket where nothing happened.
func TestIntegrationDiskOnsetIsAFloorWhenTheWalkRunsOut(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-onset-floor")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	full, room := 97*gib, 3*gib
	fsID := seedFilesystem(t, ctx, s, host, "root", "/", full, room, now)

	oldest := now.Add(-3 * time.Minute)
	seedFilesystemSample(t, ctx, s, host, fsID, oldest, full, room)
	seedFilesystemSample(t, ctx, s, host, fsID, now.Add(-2*time.Minute), full, room)
	seedFilesystemSample(t, ctx, s, host, fsID, now, full, room)

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	f := scan.Bad[diskKey(host, "root")]
	if !f.OpenedTS.UTC().Equal(oldest) {
		t.Errorf("onset = %v, want the oldest sample %v", f.OpenedTS.UTC(), oldest)
	}
	if !f.OpenedAtLeast {
		t.Error("opened_at_least is not set, but the walk never saw the mount below the line")
	}
}

// A condition ALREADY OPEN is not walked again, and its onset is not touched.
//
// 0016 is explicit that the walk can be done once: raw retention is 7 days and
// the aggregates are materialized_only, so a second walk reaches a different
// distance and answers differently. Re-deriving would rewrite an exact onset
// with a worse one every week.
func TestIntegrationDiskOnsetIsNotWalkedForAnAlreadyOpenCondition(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-onset-open")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	full, room := 97*gib, 3*gib
	empty, space := 40*gib, 60*gib
	readingTS := now
	fsID := seedFilesystem(t, ctx, s, host, "root", "/", full, room, readingTS)
	seedFilesystemSample(t, ctx, s, host, fsID, now.Add(-5*time.Minute), empty, space)
	seedFilesystemSample(t, ctx, s, host, fsID, now.Add(-4*time.Minute), full, room)
	seedFilesystemSample(t, ctx, s, host, fsID, now, full, room)

	k := diskKey(host, "root")
	scan, err := s.ScanConditions(ctx, now, map[conditions.Key]bool{k: true}, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	f := scan.Bad[k]
	// The reading's own timestamp, which is what an already-open condition
	// carries: Diff ignores the finding's onset for a row that exists, and the
	// walk that would have refined it never ran.
	if !f.OpenedTS.UTC().Equal(readingTS) {
		t.Errorf("onset = %v, want the reading's own ts %v -- an open condition "+
			"must not be re-walked", f.OpenedTS.UTC(), readingTS)
	}
	if !f.OpenedAtLeast {
		t.Error("the un-walked fallback is a floor and must say so")
	}
}

// sporadic: a host that answers now but keeps dropping scrapes.
func TestIntegrationScanFindsASporadicHost(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-sporadic")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	// Thirty minutes of scrapes with six missing: a fifth of the span, which is
	// exactly the ratio.
	for i := 29; i >= 0; i-- {
		if i%5 == 3 {
			continue
		}
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO host_samples (host_id, ts) VALUES ($1, $2)`,
			host, now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("host_samples: %v", err)
		}
	}

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !scan.Evaluated[conditions.KindSporadic] {
		t.Fatal("the sporadic kind was not evaluated; its query failed")
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindSporadic}
	if !scan.Seen[k] {
		t.Fatalf("the host was not judged: %+v", scan.Seen)
	}
	f, bad := scan.Bad[k]
	if !bad {
		t.Fatalf("a host missing a fifth of its scrapes was not flagged: %+v", scan.Bad)
	}
	if f.Severity != conditions.SeverityWarning {
		t.Errorf("severity = %q, want warning", f.Severity)
	}
	// No onset. The gaps ARE the condition, and dating it to the first of them
	// would name a scrape the host happened to miss.
	if !f.OpenedTS.IsZero() {
		t.Errorf("opened_ts = %v, want none -- a rate has no moment", f.OpenedTS)
	}
}

// A host reporting cleanly is SEEN and not flagged, which is what lets a
// sporadic condition on it clear rather than hang.
func TestIntegrationScanJudgesACleanHostSporadicallyHealthy(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-clean")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	for i := 29; i >= 0; i-- {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO host_samples (host_id, ts) VALUES ($1, $2)`,
			host, now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("host_samples: %v", err)
		}
	}

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindSporadic}
	if !scan.Seen[k] {
		t.Error("a host reporting cleanly must be SEEN, or a condition on it could never clear")
	}
	if _, bad := scan.Bad[k]; bad {
		t.Error("a host with no gaps was flagged sporadic")
	}
}

// A SILENT host is unjudged, never judged healthy and never flagged sporadic.
//
// Its buckets are missing because it is silent, which is its own condition
// already saying so. Judging it here would put two rows on the page for one
// fact -- and clearing the sporadic one when the host came back would record an
// outage as a recovery from flakiness.
func TestIntegrationScanLeavesASilentHostUnjudgedForSporadic(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-sporadic-silent")
	now := time.Now().UTC().Truncate(time.Minute)
	// Last heard from an hour ago: well past StaleAfter.
	seedHostCurrent(t, ctx, s, host, now.Add(-time.Hour))

	for i := 90; i >= 60; i-- {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO host_samples (host_id, ts) VALUES ($1, $2)`,
			host, now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("host_samples: %v", err)
		}
	}

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindSporadic}
	if !scan.Unjudged[k] {
		t.Error("a silent host must be unjudged for sporadic, not judged either way")
	}
	if scan.Seen[k] {
		t.Error("a silent host was marked seen; a condition on it could then clear on nothing")
	}
	if _, bad := scan.Bad[k]; bad {
		t.Error("a silent host was also flagged sporadic -- one outage, two rows")
	}
}

// The rate is NOT counted over the hub's own downtime.
//
// This is the fleet-wide false positive the clamp exists for. A hub that was
// down for two hours leaves a hole in every host's series at once -- the
// agent's ring replays an hour by default and no more -- so an unclamped
// window finds a third of it empty on machine after machine and opens
// `sporadic` on all of them. That is one hub outage recorded as N host
// outages, in the log an alerting engine reads, which is exactly what the
// evaluator's warm-up was written to prevent and could not prevent here: the
// window is three hours wide, so the hole outlives any warm-up.
func TestIntegrationSporadicIsNotJudgedOverTheHubsOwnDowntime(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-hub-restart")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	// The shape a hub restart actually leaves: samples on BOTH sides of the
	// hole. The hub was ingesting until it went down two hours ago, and has
	// been ingesting again for the last thirty minutes.
	//
	// Samples on both sides is what makes it a hole rather than a short
	// window: the query trims the window's EDGES to the first and last sample
	// it can see, so emptiness only in front of the host's first sample is
	// discarded already. An interior gap is not, and cannot be -- from the
	// hub's side a gap it caused and a gap the host caused are identical.
	for i := 179; i >= 150; i-- {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO host_samples (host_id, ts) VALUES ($1, $2)`,
			host, now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("host_samples: %v", err)
		}
	}
	for i := 29; i >= 0; i-- {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO host_samples (host_id, ts) VALUES ($1, $2)`,
			host, now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("host_samples: %v", err)
		}
	}

	// The hub came back thirty minutes ago.
	scan, err := s.ScanConditions(ctx, now, nil, now.Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindSporadic}
	if _, bad := scan.Bad[k]; bad {
		t.Error("a host that reported cleanly since the hub came back was called sporadic; " +
			"the window was counted across the hub's own downtime")
	}

	// And the control: the same rows, judged by a hub that claims to have been
	// up the whole time, DO read as a host missing most of its scrapes. Without
	// this the test above could pass because nothing is ever flagged.
	stale, err := s.ScanConditions(ctx, now, nil, now.Add(-3*time.Hour))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, bad := stale.Bad[k]; !bad {
		t.Error("the unclamped window found nothing; this test proves nothing")
	}
}

// A freshly started hub judges nothing until it has a window worth judging.
//
// No extra guard is needed for that: the clamp leaves a span of minutes, and
// SporadicMinSpan already declines to judge one that short. The judgement
// opens gradually rather than switching on.
func TestIntegrationSporadicDeclinesToJudgeAFreshlyStartedHub(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-fresh-hub")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	// Two buckets since the hub came up, one of them missed.
	for _, offset := range []time.Duration{2 * time.Minute, 0} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO host_samples (host_id, ts) VALUES ($1, $2)`,
			host, now.Add(-offset)); err != nil {
			t.Fatalf("host_samples: %v", err)
		}
	}

	scan, err := s.ScanConditions(ctx, now, nil, now.Add(-3*time.Minute))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindSporadic}
	if _, bad := scan.Bad[k]; bad {
		t.Error("judged a span too short to tell a gap from a hub that just started")
	}
}

// A walk that could not run leaves the mount UNJUDGED, and does not open the
// condition on a fabricated onset.
//
// The onset is walked once, at open, and this pass is the only chance: the
// next one finds the key already open and skips the walk for the life of the
// condition. Opening on the reading-timestamp fallback would let one dropped
// connection stamp a disk that has been full for a week with opened_ts = now,
// permanently, and the row would then print "over 1 m".
func TestIntegrationAFailedOnsetWalkDefersRatherThanFabricating(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-onset-fail")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	full, room := 97*gib, 3*gib
	seedFilesystem(t, ctx, s, host, "root", "/", full, room, now)

	// The walk reads filesystem_samples. Dropping it is the bluntest way to
	// make exactly that query fail while everything else this pass does still
	// works -- which is the shape of the failure being guarded against.
	if _, err := s.Pool().Exec(ctx, `DROP TABLE filesystem_samples CASCADE`); err != nil {
		t.Fatalf("drop samples: %v", err)
	}

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := diskKey(host, "root")
	if _, bad := scan.Bad[k]; bad {
		t.Error("opened a condition on a fabricated onset after the walk failed; " +
			"it would never be re-walked")
	}
	if !scan.Unjudged[k] {
		t.Error("the mount was not left unjudged, so the next pass may resolve it on nothing")
	}
	if scan.Seen[k] {
		t.Error("the mount was still marked seen; an open condition could clear on a pass that could not measure it")
	}
}

// A reporting host with NO samples in the window is unjudged, not clean.
//
// The same absence-of-data trap the rest of the scan is built around: a host
// whose first scrape has not landed in a bucket yet has not demonstrated that
// it reports cleanly.
func TestIntegrationScanLeavesAHostWithNoSamplesUnjudgedForSporadic(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-sporadic-quiet")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindSporadic}
	if !scan.Unjudged[k] {
		t.Error("a host with no samples in the window must be unjudged, not clean")
	}
}

func seedDrive(t *testing.T, ctx context.Context, s *store.Store,
	host int32, device string, lastSeen time.Time, attrs map[int16]int64) {
	t.Helper()
	var devID int32
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO devices (host_id, device, first_seen, last_seen)
		VALUES ($1, $2, $3, $3) RETURNING id`, host, device, lastSeen).Scan(&devID); err != nil {
		t.Fatalf("devices: %v", err)
	}
	for id, raw := range attrs {
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO smart_attributes (host_id, ts, device_id, attr_id, raw)
			VALUES ($1, $2, $3, $4, $5)`, host, lastSeen, devID, id, raw); err != nil {
			t.Fatalf("smart_attributes: %v", err)
		}
	}
}

// drive: one subject per DEVICE, judged by the rule the Storage tab has always
// used.
func TestIntegrationScanFindsAFailingDrive(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-drive")
	now := time.Now().UTC().Truncate(time.Second)
	seedHostCurrent(t, ctx, s, host, now)

	seedDrive(t, ctx, s, host, "sda", now, map[int16]int64{
		conditions.ATACurrentPending:     2,
		conditions.ATAReallocatedSectors: 12,
		// A warning, which must not escalate on its own.
		conditions.ATACRCErrors: 5,
	})
	// A healthy drive on the same host, to prove the subject is the device.
	seedDrive(t, ctx, s, host, "sdb", now, map[int16]int64{
		conditions.ATAReallocatedSectors: 0,
	})

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !scan.Evaluated[conditions.KindDrive] {
		t.Fatal("the drive kind was not evaluated; its query failed")
	}

	bad := conditions.Key{HostID: host, Kind: conditions.KindDrive, Subject: "sda"}
	good := conditions.Key{HostID: host, Kind: conditions.KindDrive, Subject: "sdb"}
	f, flagged := scan.Bad[bad]
	if !flagged {
		t.Fatalf("a drive with pending sectors was not flagged: %+v", scan.Bad)
	}
	if f.Severity != conditions.SeverityCritical {
		t.Errorf("severity = %q, want critical", f.Severity)
	}
	// The worst finding leads: an unreadable sector ahead of a counter that is
	// merely climbing.
	if f.Detail["text"] != "2 pending sectors" {
		t.Errorf("text = %v, want the acute finding", f.Detail["text"])
	}
	// Two ALARMS on this device -- the CRC warning is not one of them.
	if f.Detail["alarms"] != 2 {
		t.Errorf("alarms = %v, want 2 -- a warning does not escalate", f.Detail["alarms"])
	}
	if !scan.Seen[good] {
		t.Error("the healthy drive was not judged")
	}
	if _, flagged := scan.Bad[good]; flagged {
		t.Error("a drive with a zero counter was flagged")
	}
}

// A drive past the 7-day gate is UNJUDGED, never absent.
//
// The hub cannot say a disk was pulled; it can only say it stopped receiving
// readings, and only an observer that can positively say a subject is gone
// should resolve one as vanished.
func TestIntegrationScanLeavesAnUnreadDriveUnjudged(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-drive-stale")
	now := time.Now().UTC().Truncate(time.Second)
	seedHostCurrent(t, ctx, s, host, now)

	seedDrive(t, ctx, s, host, "sda", now.Add(-conditions.DriveStaleAfter-time.Hour),
		map[int16]int64{conditions.ATACurrentPending: 2})

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindDrive, Subject: "sda"}
	if !scan.Unjudged[k] {
		t.Error("a drive the host stopped reading must be unjudged, not judged either way")
	}
	if scan.Seen[k] {
		t.Error("an unread drive was marked seen; its condition could then resolve as vanished")
	}
}

// A drive with no attributes at all has not reported that it is healthy.
//
// The hub upserts a device before its attribute rows land, so this is routine
// rather than exotic -- and reading it as "no alarms" is the same mistake as
// reading a null failed-unit count as zero.
func TestIntegrationScanLeavesADriveWithNoAttributesUnjudged(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-drive-bare")
	now := time.Now().UTC().Truncate(time.Second)
	seedHostCurrent(t, ctx, s, host, now)

	seedDrive(t, ctx, s, host, "sda", now, nil)

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindDrive, Subject: "sda"}
	if !scan.Unjudged[k] {
		t.Error("a drive with no readings must be unjudged, not clean")
	}
}
