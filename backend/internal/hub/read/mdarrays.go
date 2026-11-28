package read

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// MdStaleAfter is how long an array's snapshot is taken as current: five
// scrapes, the window the load condition's md_sync uses too
// (conditions.CurrentWindow).
const MdStaleAfter = 5 * time.Minute

// Arrays lists a host's md arrays, by name. Empty for a host without any, or
// whose agent predates the snapshot.
func (s *Service) Arrays(ctx context.Context, hostID int32) ([]MdArray, error) {
	if err := s.hostExists(ctx, hostID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT name, level, raid_disks, degraded, state, sync_action,
		       sync_done, sync_total, sync_speed_kib, mismatch_cnt, members, ts
		  FROM md_arrays
		 WHERE host_id = $1
		 ORDER BY name`, hostID)
	if err != nil {
		return nil, fmt.Errorf("query md arrays: %w", err)
	}
	defer rows.Close()

	out := []MdArray{}
	for rows.Next() {
		var a MdArray
		var done, total, speedKiB *int64
		var members []byte
		if err := rows.Scan(&a.Name, &a.Level, &a.RaidDisks, &a.Degraded, &a.State,
			&a.SyncAction, &done, &total, &speedKiB, &a.MismatchCnt, &members, &a.Ts); err != nil {
			return nil, fmt.Errorf("scan md array: %w", err)
		}
		if err := json.Unmarshal(members, &a.Members); err != nil {
			return nil, fmt.Errorf("md array %s members: %w", a.Name, err)
		}
		if a.Members == nil {
			a.Members = []MdMember{}
		}
		a.Progress, a.SpeedBps, a.EtaS = syncFigures(done, total, speedKiB)
		a.Stale = time.Since(a.Ts) > MdStaleAfter
		out = append(out, a)
	}
	return out, rowsErr(rows.Err(), "md arrays")
}

// syncFigures turns md's sectors and KiB/s into a fraction, bytes per second
// and seconds left. Each is nil when its inputs are: no sync, or the kernel
// not saying.
func syncFigures(done, total, speedKiB *int64) (progress *float64, speedBps, etaS *int64) {
	if done != nil && total != nil && *total > 0 {
		p := min(float64(*done)/float64(*total), 1)
		progress = &p
	}
	if speedKiB != nil {
		b := *speedKiB * 1024
		speedBps = &b
	}
	if done != nil && total != nil && speedBps != nil && *speedBps > 0 && *total >= *done {
		e := (*total - *done) * 512 / *speedBps
		etaS = &e
	}
	return progress, speedBps, etaS
}
