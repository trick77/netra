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
// A mount whose own reading predates the host's last_seen is skipped entirely
// -- not seen, not judged. `filesystems` is never pruned, so a mount that
// stopped being reported keeps its row forever; 0013's comment carries the
// argument. Skipping it means Diff sees the subject disappear while the host
// is still talking, which is exactly ReasonVanished.
func (s *Store) scanFilesystems(ctx context.Context, scan *conditions.Scan) error {
	// The SUBJECT is the label, not the mountpoint, and read/family.go states
	// why: "the label is the identity -- stable, unique per host, what the
	// inventory joins on -- while the mountpoint is what an operator
	// recognises". A state machine keys on identity, so a mount moved from
	// /mnt/old to /mnt/new stays one condition rather than vanishing and
	// reopening. The mountpoint rides the detail, where the sentence needs it,
	// and it is nullable so the label is the fallback there too.
	rows, err := s.pool.Query(ctx, `
		SELECT fc.host_id, f.label, f.mountpoint, fc.used, fc.free
		  FROM filesystem_current fc
		  JOIN filesystems f ON f.id = fc.fs_id AND f.host_id = fc.host_id
		  JOIN host_current hc ON hc.host_id = fc.host_id
		 WHERE hc.last_seen IS NOT NULL
		   AND fc.ts >= hc.last_seen - $1::interval`,
		staleMountWindow.String())
	if err != nil {
		return fmt.Errorf("query filesystems: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int32
		var label string
		var mountpoint *string
		var used, free *int64
		if err := rows.Scan(&hostID, &label, &mountpoint, &used, &free); err != nil {
			return fmt.Errorf("scan filesystem: %w", err)
		}

		key := conditions.Key{HostID: hostID, Kind: conditions.KindDisk, Subject: label}
		scan.Seen[key] = true

		pct, ok := conditions.UsePct(used, free)
		if !ok {
			// Measured nothing. Seen, so it does not read as vanished, but
			// there is nothing to judge.
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
		scan.Bad[key] = conditions.Finding{Key: key, Severity: severity, Detail: detail}
	}
	return rows.Err()
}

// staleMountWindow is how far a mount's own reading may lag the host's before
// the mount is treated as no longer reported.
//
// Generous, because the two timestamps come from different places: last_seen
// advances on every scrape, and a filesystem reading is written when the
// filesystem collector runs. A tight window would call a mount vanished
// because of ordinary skew, and a vanished resolution is not something to be
// wrong about -- it closes a condition without waiting out the hysteresis.
const staleMountWindow = 15 * time.Minute

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
	rows, err := s.pool.Query(ctx, `
		SELECT u.host_id,
		       count(*) FILTER (WHERE u.state = 'failed') AS failed,
		       min(u.state_ts) FILTER (WHERE u.state = 'failed') AS since,
		       (array_agg(u.unit_name ORDER BY u.unit_name)
		          FILTER (WHERE u.state = 'failed'))[1:3] AS names
		  FROM systemd_units u
		  JOIN host_current hc ON hc.host_id = u.host_id
		 WHERE hc.last_seen IS NOT NULL
		 GROUP BY u.host_id`)
	if err != nil {
		return fmt.Errorf("query units: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int32
		var failed int
		var since *time.Time
		var names []string
		if err := rows.Scan(&hostID, &failed, &since, &names); err != nil {
			return fmt.Errorf("scan units: %w", err)
		}

		key := conditions.Key{HostID: hostID, Kind: conditions.KindFailedUnits}
		// Seen whether or not anything failed: a host whose units are all
		// healthy has been LOOKED AT, which is what lets a condition on it
		// clear rather than hang.
		scan.Seen[key] = true

		if failed == 0 {
			continue
		}

		detail := map[string]any{"count": failed}
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
