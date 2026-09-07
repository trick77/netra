package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
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
func (s *Store) ScanConditions(ctx context.Context, now time.Time) (conditions.Scan, error) {
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
	if err := s.scanFilesystems(ctx, &scan); err != nil {
		slog.Error("condition scan: filesystems", "err", err)
	} else {
		scan.Evaluated[conditions.KindDisk] = true
	}

	if err := s.scanUnits(ctx, &scan); err != nil {
		slog.Error("condition scan: units", "err", err)
	} else {
		scan.Evaluated[conditions.KindFailedUnits] = true
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
		scan.Seen[key] = true

		severity := conditions.SilentSeverity(lastSeen, now)
		if severity == "" {
			continue
		}

		detail := map[string]any{}
		f := conditions.Finding{Key: key, Severity: severity, Detail: detail}
		if lastSeen != nil {
			// The one condition whose onset needs no derivation: last_seen IS
			// the moment it began.
			f.OpenedTS = *lastSeen
			detail["last_seen"] = lastSeen.UTC().Format(time.RFC3339)
		} else {
			// Never seen at all. There is no honest onset -- the host was
			// registered at some point, but netra has never had a reading --
			// so the row takes the tick's time and says why.
			detail["never_reported"] = true
		}
		scan.Bad[key] = f
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
func (s *Store) scanFilesystems(ctx context.Context, scan *conditions.Scan) error {
	// Every mount the host still has a row for, WITH the age of its reading,
	// because that age is the only thing separating three different states.
	//
	// The query cannot filter on it: `filesystem_current` keeps a row per
	// mount and nothing prunes it, so a stale reading means the mount was not
	// re-measured, not that the mount is gone. Which of the two it is depends
	// on how stale, and the loop below decides -- see mountGoneAfter.
	rows, err := s.pool.Query(ctx, `
		SELECT fc.host_id, f.label, f.mountpoint, fc.used, fc.free,
		       fc.ts, hc.last_seen
		  FROM filesystem_current fc
		  JOIN filesystems f ON f.id = fc.fs_id AND f.host_id = fc.host_id
		  JOIN host_current hc ON hc.host_id = fc.host_id
		 WHERE hc.last_seen IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("query filesystems: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int32
		var label string
		var mountpoint *string
		var used, free *int64
		var readingTS, lastSeen time.Time
		if err := rows.Scan(&hostID, &label, &mountpoint, &used, &free,
			&readingTS, &lastSeen); err != nil {
			return fmt.Errorf("scan filesystem: %w", err)
		}

		key := conditions.Key{HostID: hostID, Kind: conditions.KindDisk, Subject: label}
		age := lastSeen.Sub(readingTS)

		switch {
		case age >= mountGoneAfter:
			// Long past anything the agent's backoff can explain, so the mount
			// is gone rather than slow. Left out of Seen AND out of Unjudged,
			// which is what makes ReasonVanished reachable at all.
			//
			// Nothing deletes rows from `filesystems` or `filesystem_current`
			// -- 0013 says filesystems are never pruned -- so an unmounted
			// volume keeps its row forever. Without this branch its condition
			// would resolve as `cleared`: a recovery that never happened,
			// which is the exact failure resolved_reason exists to prevent.
			continue
		case age > conditions.StaleAfter:
			// Stale, but inside what the statfs backoff can produce on a
			// still-mounted disk. Present, unmeasurable, and emphatically not
			// healthy.
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
			// The reading's own timestamp, as a FLOOR rather than a moment.
			//
			// The honest answer is a walk back through the mount's series to
			// the first sample over the threshold, and that walk is not
			// written yet. Until it is, this says "it was already this full
			// when this reading was taken", which is true and is bounded; the
			// alternative -- letting Diff fill in now() -- silently records
			// detection time as onset, and on a host that has been off for a
			// month that is a month wrong with nothing marking it.
			OpenedTS:      readingTS,
			OpenedAtLeast: true,
		}
	}
	return rows.Err()
}

// mountGoneAfter is how far a mount's reading may lag the host's before the
// mount is treated as removed rather than merely unmeasured.
//
// Sized against the agent, not guessed. markWedged backs a timing-out
// mountpoint off by 2^min(failures-1, wedgedBackoffShifts) scrapes, and
// wedgedBackoffShifts is 10, so a still-mounted disk can legitimately go
// 1024 scrapes -- about seventeen hours at the 60s cadence -- without a fresh
// reading. The collector's own comments call it "a seventeen-hour cadence".
//
// A day clears that ceiling with room to spare, which is the direction to err
// in: calling a present mount gone resolves a real condition as a recovery,
// while calling a gone mount present leaves a stale row open a few hours
// longer than necessary.
const mountGoneAfter = 24 * time.Hour

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
	rows, err := s.pool.Query(ctx, `
		SELECT hc.host_id,
		       hc.services_failed AS failed,
		       min(u.state_ts) FILTER (WHERE u.state = 'failed') AS since,
		       (array_agg(u.unit_name ORDER BY u.unit_name)
		          FILTER (WHERE u.state = 'failed'))[1:3] AS names
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
