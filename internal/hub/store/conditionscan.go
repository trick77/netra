package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
	"github.com/trick77/netra/internal/hub/systemdstate"
)

// ScanConditions evaluates every host's current state.
//
// Three queries over CURRENT-state tables, not over the sample hypertables:
// host_current has one row per host, filesystem_current one per mount,
// systemd_units one per unit. A hundred hosts with ten mounts each is a
// thousand rows, so a pass costs about what one fleet page load used to cost
// each open browser -- and the browsers stop paying it entirely.
//
// A query that FAILS leaves its kind out of Scan.Evaluated rather than
// reporting an empty result, which is the difference between "nothing is
// wrong" and "nobody looked". Diff refuses to resolve a kind nobody looked at;
// without that, one failed query would resolve every condition of its kind and
// destroy the onsets on the way through.
func (s *Store) ScanConditions(ctx context.Context, now time.Time,
	open map[conditions.Key]bool, since time.Time) (conditions.Scan, error) {
	scan := conditions.Scan{
		Evaluated: map[string]bool{},
		Seen:      map[conditions.Key]bool{},
		Unjudged:  map[conditions.Key]bool{},
		Bad:       map[conditions.Key]conditions.Finding{},
		Reporting: map[int32]bool{},
	}

	// Hosts first, and NOT optional: everything below is interpreted against
	// which hosts are reporting, so a pass that cannot answer that cannot
	// safely conclude anything at all.
	if err := s.scanHosts(ctx, &scan, now); err != nil {
		return conditions.Scan{}, err
	}

	// The rest are independent. One failing costs its own kind and nothing
	// else, which is why each is logged and swallowed rather than returned.
	if err := s.scanFilesystems(ctx, &scan, open); err != nil {
		slog.Error("condition scan: filesystems", "err", err)
	} else {
		scan.Evaluated[conditions.KindDisk] = true
	}

	if err := s.scanUnits(ctx, &scan); err != nil {
		slog.Error("condition scan: units", "err", err)
	} else {
		scan.Evaluated[conditions.KindFailedUnits] = true
	}

	if err := s.scanReporting(ctx, &scan, now, since); err != nil {
		slog.Error("condition scan: reporting", "err", err)
	} else {
		scan.Evaluated[conditions.KindSporadic] = true
	}

	if err := s.scanDrives(ctx, &scan); err != nil {
		slog.Error("condition scan: drives", "err", err)
	} else {
		scan.Evaluated[conditions.KindDrive] = true
	}

	if err := s.scanSensors(ctx, &scan, open); err != nil {
		slog.Error("condition scan: sensors", "err", err)
	} else {
		scan.Evaluated[conditions.KindTemperature] = true
	}

	// One query, two kinds, so both are marked evaluated or neither is. They
	// come from the same row: a failure that hid the process count hid the
	// load average with it, and claiming one was looked at would let Diff
	// resolve conditions nobody judged.
	if err := s.scanHostGauges(ctx, &scan, open); err != nil {
		slog.Error("condition scan: host gauges", "err", err)
	} else {
		scan.Evaluated[conditions.KindProcesses] = true
		scan.Evaluated[conditions.KindLoad] = true
	}

	return scan, nil
}

// scanHosts fills Reporting, and raises the silent condition.
//
// The one kind that is the ABSENCE of data rather than a reading in it, which
// is why nothing but a ticker can find it: no ingest happens for a host that
// has stopped talking, so nothing else would ever look.
func (s *Store) scanHosts(ctx context.Context, scan *conditions.Scan, now time.Time) error {
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, c.last_seen
		  FROM hosts h
		  LEFT JOIN host_current c ON c.host_id = h.id`)
	if err != nil {
		return fmt.Errorf("query hosts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int32
		var lastSeen *time.Time
		if err := rows.Scan(&id, &lastSeen); err != nil {
			return fmt.Errorf("scan host: %w", err)
		}

		reporting := conditions.Reporting(lastSeen, now)
		scan.Reporting[id] = reporting

		key := conditions.Key{HostID: id, Kind: conditions.KindSilent}

		// A host that has NEVER reported is unprovisioned, not silent.
		//
		// admin.CreateHost inserts the row and hands over a token; the operator
		// then goes and installs the agent, which is minutes or hours later. A
		// critical condition raised in that gap -- with an event in the log
		// alerting reads -- says a machine has stopped talking when it has not
		// started yet, and clears two ticks after the agent comes up, leaving a
		// permanent opened/cleared pair describing nothing but the
		// provisioning.
		//
		// Unjudged rather than healthy: netra has no observation of this host,
		// which is not the same as a good one. The cost is that a host whose
		// agent is never installed raises nothing, and that is the right
		// direction -- an empty row in the hosts list already says it, without
		// putting a false outage in the log.
		if lastSeen == nil {
			scan.Unjudged[key] = true
			continue
		}

		scan.Seen[key] = true

		severity := conditions.SilentSeverity(lastSeen, now)
		if severity == "" {
			continue
		}

		// The one condition whose onset needs no derivation: last_seen IS the
		// moment it began.
		scan.Bad[key] = conditions.Finding{
			Key:      key,
			Severity: severity,
			OpenedTS: *lastSeen,
			Detail: map[string]any{
				"last_seen": lastSeen.UTC().Format(time.RFC3339),
			},
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read hosts: %w", err)
	}
	scan.Evaluated[conditions.KindSilent] = true
	return nil
}

// scanFilesystems raises the disk condition, one subject per mount.
//
// Per mount rather than per host, and the fleet page's collapse to a single
// row stays a rendering decision. Collapsed here, the condition would open and
// close every time the fullest mount changed from /var to /mnt.
//
// The SUBJECT is the label, not the mountpoint, and read/family.go states why:
// "the label is the identity -- stable, unique per host, what the inventory
// joins on -- while the mountpoint is what an operator recognises". A state
// machine keys on identity, so a mount moved from /mnt/old to /mnt/new stays
// one condition rather than vanishing and reopening. The mountpoint rides the
// detail, where the sentence needs it, and it is nullable so the label is the
// fallback there too.
func (s *Store) scanFilesystems(ctx context.Context, scan *conditions.Scan, open map[conditions.Key]bool) error {
	// Every mount the host still has a row for, WITH the age of its reading,
	// because that age is what separates a mount this pass can judge from one
	// it can only note the existence of.
	//
	// The query cannot filter on it: `filesystem_current` keeps a row per
	// mount and nothing prunes it, so filtering would erase the difference
	// between "not re-measured" and "gone" -- and the loop below is careful
	// not to claim it can tell them apart either.
	rows, err := s.pool.Query(ctx, `
		SELECT fc.host_id, fc.fs_id, f.label, f.mountpoint, fc.used, fc.free,
		       fc.ts, hc.last_seen
		  FROM filesystem_current fc
		  JOIN filesystems f ON f.id = fc.fs_id AND f.host_id = fc.host_id
		  JOIN host_current hc ON hc.host_id = fc.host_id
		 WHERE hc.last_seen IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("query filesystems: %w", err)
	}
	defer rows.Close()

	// The mounts that need an onset walked, collected rather than walked in
	// place: the walk is another query, and running one while these rows are
	// still streaming would hold two statements open on the pool for every bad
	// mount in the fleet.
	type toWalk struct {
		key  conditions.Key
		fsID int32
	}
	var walks []toWalk

	for rows.Next() {
		var hostID, fsID int32
		var label string
		var mountpoint *string
		var used, free *int64
		var readingTS, lastSeen time.Time
		if err := rows.Scan(&hostID, &fsID, &label, &mountpoint, &used, &free,
			&readingTS, &lastSeen); err != nil {
			return fmt.Errorf("scan filesystem: %w", err)
		}

		key := conditions.Key{HostID: hostID, Kind: conditions.KindDisk, Subject: label}
		age := lastSeen.Sub(readingTS)

		// A stale reading is UNJUDGED, for as long as it stays stale, and this
		// deliberately has no expiry.
		//
		// The tempting rule is "stale beyond what the backoff explains, so the
		// mount is gone" -- and the hub cannot support it. A mount the agent
		// cannot stat produces NO sample at all (Filesystems.Collect skips it),
		// and markWedged re-arms the backoff on every failed retry, so a hung
		// NFS export freezes fc.ts indefinitely. From here that is
		// byte-for-byte what an unmounted volume looks like. Any horizon picked
		// would eventually call a still-mounted, still-full disk "no longer
		// reported", resolve its condition without the hysteresis, and destroy
		// the onset -- while the disk carries on filling.
		//
		// So the two failure modes are: a removed mount leaves a stale
		// condition open until someone dismisses it, or a wedged mount is
		// silently declared recovered. The first is visible and wrong in a way
		// a reader can see; the second is invisible and is a lie. This takes
		// the first.
		//
		// ReasonVanished is still the right resolution for a subject that is
		// genuinely gone -- it wants an observer that can SAY so, which means
		// the agent reporting its live mount list rather than the hub
		// inferring absence from silence.
		if age > conditions.StaleAfter {
			scan.Unjudged[key] = true
			continue
		}

		// Judged only on a CURRENT reading, against the host's own last_seen
		// rather than the wall clock -- the same comparison currentFilesystems
		// makes in ui/src/lib/host.ts, so the hub and the browser retire a
		// mount at the same moment rather than three minutes apart.
		scan.Seen[key] = true

		pct, ok := conditions.UsePct(used, free)
		if !ok {
			// The reading is current and says nothing measurable. Unjudged
			// rather than healthy: a filesystem netra could not size is not a
			// filesystem with room.
			scan.Unjudged[key] = true
			delete(scan.Seen, key)
			continue
		}
		severity := conditions.DiskSeverity(pct, free)
		if severity == "" {
			continue
		}

		mount := label
		if mountpoint != nil && *mountpoint != "" {
			mount = *mountpoint
		}
		detail := map[string]any{"pct": pct, "mount": mount}
		if free != nil {
			detail["free"] = *free
		}
		scan.Bad[key] = conditions.Finding{
			Key:      key,
			Severity: severity,
			Detail:   detail,
			// The reading's own timestamp, as a FLOOR, until the walk below
			// replaces it with the moment it actually crossed.
			//
			// This is what a condition already open keeps: its onset was walked
			// when it opened and must never be walked again -- raw retention is
			// 7 days and the aggregates are materialized_only, so a second walk
			// reaches a different distance and answers differently. It is also
			// the fallback for a walk that finds nothing, which is true and
			// bounded; letting Diff fill in now() instead would silently record
			// DETECTION time as onset, and on a host that has been off for a
			// month that is a month wrong with nothing marking it.
			OpenedTS:      readingTS,
			OpenedAtLeast: true,
		}

		// Only a condition that is NOT already open. See Store.ScanConditions.
		if !open[key] {
			walks = append(walks, toWalk{key: key, fsID: fsID})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, w := range walks {
		onset, atLeast, err := s.diskOnset(ctx, w.key.HostID, w.fsID)
		if err != nil {
			// The condition is held back a tick rather than opened with a floor
			// nothing will ever revisit.
			//
			// The onset is walked ONCE, at open, and this pass is the only
			// chance: the next one finds the key in `open` and skips the walk
			// for the life of the condition. Opening on the fallback would let
			// one dropped connection, at the moment a disk crosses, stamp a
			// mount that has been full for a week with opened_ts = the current
			// reading -- permanently, with the row then printing "over 1 m".
			//
			// Unjudged, not dropped: the subject is there and this pass could
			// not say anything about it, which is exactly the third state.
			// Diff leaves an already-open condition alone and does not count a
			// miss against it, so nothing resolves on the strength of a failed
			// query, and the mount is walked properly on the next tick.
			slog.Error("condition scan: disk onset",
				"host_id", w.key.HostID, "subject", w.key.Subject, "err", err)
			delete(scan.Bad, w.key)
			delete(scan.Seen, w.key)
			scan.Unjudged[w.key] = true
			continue
		}
		if onset.IsZero() {
			continue
		}
		f := scan.Bad[w.key]
		f.OpenedTS = onset
		f.OpenedAtLeast = atLeast
		scan.Bad[w.key] = f
	}
	return nil
}

// diskOnset walks one mount's series back to the moment it crossed.
//
// Ported from crossedAt in ui/src/features/fleet/hostTrends.ts, which did this
// on EVERY RENDER, bounded by the range picker -- so the same disk answered
// "since 14:02" on one range and "over 24 h" on another. Done once here, while
// the raw data still exists, the answer is exact and permanent.
//
// Raw filesystem_samples only. The continuous aggregates are materialized_only
// (read/tier.go), so a walk that fell through to them past the 7-day raw floor
// would return nothing rather than less detail -- and would answer differently
// every week as the same history aged through tiers. History that changes shape
// as it ages is not history.
//
// A sample with nothing measurable in it is SKIPPED rather than treated as a
// drop below the threshold: an outage in the middle of a fill is a gap in the
// evidence, not a recovery, and letting it end the walk would restart the clock
// at whatever came after it.
//
// Returns a zero time when the mount has no usable samples at all, which leaves
// the caller's floor in place.
func (s *Store) diskOnset(ctx context.Context, hostID, fsID int32) (time.Time, bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ts, used, free
		  FROM filesystem_samples
		 WHERE host_id = $1 AND fs_id = $2
		 ORDER BY ts DESC`, hostID, fsID)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("query filesystem samples: %w", err)
	}
	defer rows.Close()

	var onset time.Time
	// dropped records that the walk SAW the mount below the threshold and
	// stopped there, which is what makes the onset a moment rather than a
	// floor. Running out of retained samples instead means the mount was
	// already this full at the far end of what netra kept, and the UI says
	// "over 7 d" rather than naming a bucket where nothing happened.
	dropped := false
	for rows.Next() {
		var ts time.Time
		var used, free *int64
		if err := rows.Scan(&ts, &used, &free); err != nil {
			return time.Time{}, false, fmt.Errorf("scan filesystem sample: %w", err)
		}
		pct, ok := conditions.UsePct(used, free)
		if !ok {
			continue
		}
		if conditions.DiskSeverity(pct, free) == "" {
			dropped = true
			break
		}
		onset = ts
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, false, fmt.Errorf("read filesystem samples: %w", err)
	}
	if onset.IsZero() {
		return time.Time{}, false, nil
	}
	return onset, !dropped, nil
}

// scanUnits raises the failed-units condition, one subject per HOST.
//
// Per host, unlike disk, and that is not an inconsistency: the fleet page
// counts failed units and names up to three, so the condition is "this host
// has failing units" rather than one condition per unit. A host with five
// failures is one thing to go and look at.
//
// The onset is the OLDEST failing unit's state_ts: five units failing at five
// times is one condition that began with the first.
func (s *Store) scanUnits(ctx context.Context, scan *conditions.Scan) error {
	// The COUNT comes from host_current.services_failed, and the names and the
	// onset from the unit rows.
	//
	// Not one source, because the UI already made this decision and stated it:
	// "the count leads and comes from services_failed, which is the agent's
	// own summary; the names annotate it and come from the hub's unit rows.
	// The two are ALLOWED to disagree -- a host heard from once has a summary
	// and no unit rows yet" (fleet/conditions.ts). Counting unit rows here
	// instead would make the hub and the browser disagree by construction: the
	// summary rides every 60s scrape while the snapshot that fills the unit
	// rows arrives every five minutes, so they differ routinely and not only
	// in the edge case. The equality gate this whole engine is measured
	// against would never pass.
	//
	// The FILTER is systemdstate.NotableSQL rather than a fourth `state =
	// 'failed'` written out here. read.Hosts dates failed_since with the same
	// predicate through the same function, and that is exactly the alignment
	// the equality gate checks -- a private copy would let the hub and the
	// hosts endpoint drift on which units count, with nothing able to see it.
	notable := systemdstate.NotableSQL("u")
	rows, err := s.pool.Query(ctx, `
		SELECT hc.host_id,
		       hc.services_failed AS failed,
		       min(u.state_ts) FILTER (WHERE `+notable+`) AS since,
		       (array_agg(u.unit_name ORDER BY u.unit_name)
		          FILTER (WHERE `+notable+`))[1:3] AS names
		  FROM host_current hc
		  LEFT JOIN systemd_units u ON u.host_id = hc.host_id
		 WHERE hc.last_seen IS NOT NULL
		 GROUP BY hc.host_id, hc.services_failed`)
	if err != nil {
		return fmt.Errorf("query units: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int32
		var failed *int
		var since *time.Time
		var names []string
		if err := rows.Scan(&hostID, &failed, &since, &names); err != nil {
			return fmt.Errorf("scan units: %w", err)
		}

		key := conditions.Key{HostID: hostID, Kind: conditions.KindFailedUnits}

		// NULL is "cannot say", never "none failed". 0001_init.sql gives it
		// that meaning -- a host with no systemd, or one not yet heard from --
		// and fleet/conditions.ts keeps the distinction by staying silent on
		// null. Coalescing it to zero would make an agent that stopped
		// collecting systemd look like a host whose units all recovered, and
		// close an open condition on the strength of it.
		if failed == nil {
			scan.Unjudged[key] = true
			continue
		}

		// Seen whether or not anything failed: a host whose units are all
		// healthy has been LOOKED AT, which is what lets a condition on it
		// clear rather than hang.
		scan.Seen[key] = true

		if *failed == 0 {
			continue
		}

		detail := map[string]any{"count": *failed}
		// Never MORE names than the count claims. A row reading "1 failed
		// unit" beside two unit names contradicts itself, and the count is
		// what every other part of netra is counting -- the same three
		// branches failedUnitsShown resolves, resolved the same way.
		if len(names) > *failed {
			names = names[:*failed]
		}
		if len(names) > 0 {
			detail["units"] = names
		}
		f := conditions.Finding{
			Key:      key,
			Severity: conditions.SeverityWarning,
			Detail:   detail,
		}
		if since != nil {
			f.OpenedTS = *since
		}
		scan.Bad[key] = f
	}
	return rows.Err()
}

// scanReporting raises the sporadic condition: a host that answers now but
// keeps dropping scrapes.
//
// The one kind here that is a RATE rather than a reading, and the only one that
// has to look at a hypertable at all. "Online" is exactly as wrong a summary of
// such a host as "offline" -- both say the thing is fine or gone, when the
// interesting state is neither.
//
// The window is FIXED (conditions.SporadicWindow) where the browser used
// whatever range the reader had picked. That is the whole reason it moved: a
// judgement made over the range picker's window is a fact about the reader, and
// changing the range made the condition appear or disappear.
func (s *Store) scanReporting(ctx context.Context, scan *conditions.Scan,
	now, since time.Time) error {
	// Buckets counted at the scrape cadence, with BOTH EDGES trimmed to the
	// first and last bucket that actually carried a sample -- which is what
	// min(ts)/max(ts) do here.
	//
	// Trailing emptiness would otherwise be counted for every host, healthy or
	// not, because every tier materialises behind now. Leading emptiness is the
	// time before the host was reporting at all: the window is a fixed three
	// hours and nothing clamps it to when a host first appeared, so counting the
	// emptiness in front of a host added ten minutes ago called every new agent
	// sporadic on its first day.
	// The window, clamped to what this hub process can vouch for.
	//
	// A bucket the hub was not running for is empty on EVERY host at once, so
	// counting it measures the hub's own downtime and reports it as a fleet of
	// flaky machines. The agent's ring replays what it buffered -- an hour by
	// default -- and a longer outage leaves a hole nothing fills, which would
	// otherwise sit inside this window for the whole three hours it is wide.
	//
	// No warm-up is needed on top: a freshly started hub has a window of
	// minutes, and SporadicMinSpan declines to judge a span that short, so the
	// clamp opens the judgement gradually rather than switching it on.
	from := now.Add(-conditions.SporadicWindow)
	if since.After(from) {
		from = since
	}
	bucket := int(conditions.ScrapeInterval.Seconds())
	rows, err := s.pool.Query(ctx, `
		SELECT host_id,
		       count(DISTINCT time_bucket(make_interval(secs => $2), ts)) AS present,
		       floor(extract(epoch FROM max(ts) - min(ts)) / $2)::int + 1 AS span
		  FROM host_samples
		 WHERE ts >= $1
		 GROUP BY host_id`, from, bucket)
	if err != nil {
		return fmt.Errorf("query host samples: %w", err)
	}
	defer rows.Close()

	// Every host that answered at all, so the ones that did NOT can be told
	// apart from the ones this pass never looked at.
	measured := map[int32]bool{}

	for rows.Next() {
		var hostID int32
		var present, span int
		if err := rows.Scan(&hostID, &present, &span); err != nil {
			return fmt.Errorf("scan host samples: %w", err)
		}
		measured[hostID] = true

		key := conditions.Key{HostID: hostID, Kind: conditions.KindSporadic}

		// A host that is not reporting is UNJUDGED, never judged healthy. Its
		// buckets are missing because it is silent, and silence is its own
		// condition already saying so. Judging it here would put two rows on
		// the page for one fact -- and clearing the sporadic one when the host
		// came back would record an outage as a recovery from flakiness.
		if !scan.Reporting[hostID] {
			scan.Unjudged[key] = true
			continue
		}

		scan.Seen[key] = true

		severity := conditions.SporadicSeverity(present, span)
		if severity == "" {
			continue
		}

		// No onset, and Diff fills in now. A rate has no moment the way
		// last_seen does: the gaps ARE the condition, and dating it to the
		// first of them would name a scrape the host happened to miss rather
		// than when it started missing them.
		scan.Bad[key] = conditions.Finding{
			Key:      key,
			Severity: severity,
			Detail:   map[string]any{"present": present, "span": span},
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read host samples: %w", err)
	}

	// A reporting host with NO samples in the window at all is unjudged, not
	// clean. Same trap as everywhere else in this file: a host whose samples
	// aged out, or whose first scrape has not landed in a bucket yet, has not
	// demonstrated that it reports cleanly.
	for hostID := range scan.Reporting {
		if measured[hostID] {
			continue
		}
		scan.Unjudged[conditions.Key{HostID: hostID, Kind: conditions.KindSporadic}] = true
	}
	return nil
}

// judgedAttrIDs is every SMART attribute conditions.DriveFindings reads.
//
// The query filters on them because this is JUDGING, not rendering: read.Drives
// selects every attribute a drive reported because it draws a table of them,
// and an id DriveFindings has never heard of cannot change the verdict.
var judgedAttrIDs = []int16{
	conditions.ATAReallocatedSectors,
	conditions.ATAReportedUncorrect,
	conditions.ATACurrentPending,
	conditions.ATAOfflineUncorrectable,
	conditions.ATACRCErrors,
	conditions.NVMeCriticalWarning,
	conditions.NVMePercentageUsed,
	conditions.NVMeAvailableSpare,
	conditions.NVMeAvailableSpareThreshold,
	conditions.NVMeMediaErrors,
}

// scanDrives raises the drive condition, one subject per DEVICE.
//
// Per device rather than per host, like disk and unlike failed-units. The fleet
// page collapses a host's drives into one row -- "ONE condition for the host,
// never one per drive" -- and that stays a rendering decision: collapsed here,
// the condition would open and close every time the worst finding moved from
// sda to sdb.
func (s *Store) scanDrives(ctx context.Context, scan *conditions.Scan) error {
	// LEFT JOIN LATERAL so a drive with no attributes still appears: the hub
	// upserts a device before its attribute rows land, and such a drive is
	// present-and-unmeasurable rather than absent.
	rows, err := s.pool.Query(ctx, `
		SELECT d.host_id, d.device, d.last_seen, a.attr_id, a.raw, hc.last_seen
		  FROM devices d
		  JOIN host_current hc ON hc.host_id = d.host_id
		  LEFT JOIN LATERAL (
		       SELECT DISTINCT ON (s.attr_id) s.attr_id, s.raw
		         FROM smart_attributes s
		        WHERE s.host_id = d.host_id AND s.device_id = d.id
		          AND s.attr_id = ANY($1::smallint[])
		        ORDER BY s.attr_id, s.ts DESC
		  ) a ON TRUE
		 WHERE hc.last_seen IS NOT NULL
		 ORDER BY d.host_id, d.device`, judgedAttrIDs)
	if err != nil {
		return fmt.Errorf("query drives: %w", err)
	}
	defer rows.Close()

	type driveKey struct {
		hostID int32
		device string
	}
	// Folded in read order, which the ORDER BY keeps contiguous per drive.
	var order []driveKey
	readings := map[driveKey]*conditions.DriveReading{}
	hostSeen := map[int32]time.Time{}

	for rows.Next() {
		var hostID int32
		var device string
		var driveLastSeen, hostLastSeen time.Time
		var attrID *int16
		var raw *int64
		if err := rows.Scan(&hostID, &device, &driveLastSeen, &attrID, &raw,
			&hostLastSeen); err != nil {
			return fmt.Errorf("scan drive: %w", err)
		}

		k := driveKey{hostID: hostID, device: device}
		d, ok := readings[k]
		if !ok {
			d = &conditions.DriveReading{Device: device, LastSeen: driveLastSeen}
			readings[k] = d
			order = append(order, k)
			hostSeen[hostID] = hostLastSeen
		}
		if attrID != nil {
			d.Attributes = append(d.Attributes, conditions.DriveAttr{ID: *attrID, Raw: raw})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read drives: %w", err)
	}

	for _, k := range order {
		d := readings[k]
		key := conditions.Key{HostID: k.hostID, Kind: conditions.KindDrive, Subject: k.device}
		lastSeen := hostSeen[k.hostID]

		// The 7-day gate, against the HOST'S OWN last_seen rather than the wall
		// clock -- ported from driveIsCurrent, for the mount's reason: an agent
		// with a skewed clock must not lose its inventory to a fact about its
		// NTP config.
		//
		// UNJUDGED, not absent. A drive netra has stopped receiving readings
		// for might be pulled and might be a SMART collector that is failing;
		// the hub cannot tell, and only an observer that can positively say a
		// drive is gone should resolve one as vanished.
		if !conditions.DriveIsCurrent(*d, &lastSeen) {
			scan.Unjudged[key] = true
			continue
		}

		// A drive with no attributes at all has not reported that it is
		// healthy. The same distinction as a filesystem netra could not size.
		if len(d.Attributes) == 0 {
			scan.Unjudged[key] = true
			continue
		}

		scan.Seen[key] = true

		alarms := conditions.SortAlarms(conditions.DriveAlarms(*d))
		if len(alarms) == 0 {
			continue
		}
		worst := alarms[0]

		// No onset, and this one cannot be walked back the way a filesystem
		// can. SMART attributes are counters with no zero baseline, sampled
		// hourly: the first non-zero reading netra holds is when netra started
		// LOOKING, not when the sector went bad. A drive whose agent was
		// installed on Tuesday would claim its sectors failed on Tuesday.
		scan.Bad[key] = conditions.Finding{
			Key:      key,
			Severity: worst.Severity,
			Detail: map[string]any{
				"device": worst.Device,
				"text":   worst.Text,
				// How many alarms this DEVICE carries, so the renderer can add
				// up a host's "+N more" across its drives without re-deriving
				// anything.
				"alarms": len(alarms),
				// Never rendered. It is what lets the renderer pick which drive
				// the host's one line names when two are both critical -- see
				// conditions.Urgency.
				"urgency": int(worst.Urgency),
			},
		}
	}
	return nil
}
