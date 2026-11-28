package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// mdMemberJSON is one element of md_arrays.members.
type mdMemberJSON struct {
	Device string  `json:"device"`
	State  string  `json:"state"`
	Slot   *uint32 `json:"slot,omitempty"`
}

// ApplyMdSnapshot replaces a host's md_arrays rows with the arrays in snap.
//
// A whole set, like the systemd snapshot: an array missing from it is gone and
// its row is deleted, and an empty snapshot deletes them all. Both the delete
// and the upsert are guarded on ts, so a snapshot that arrives after a newer
// one -- a replayed batch -- changes nothing.
func (s *Store) ApplyMdSnapshot(ctx context.Context, hostID int32, snap *netrav1.MdSnapshot) error {
	ts := tsOf(snap.GetTsMs())
	names := make([]string, 0, len(snap.GetArrays()))
	for _, a := range snap.GetArrays() {
		if a.GetName() != "" {
			names = append(names, a.GetName())
		}
	}

	batch := &pgx.Batch{}
	batch.Queue(`
		DELETE FROM md_arrays
		 WHERE host_id = $1 AND ts <= $2 AND NOT (name = ANY($3))`,
		hostID, ts, names)

	const upsert = `
		INSERT INTO md_arrays (host_id, name, ts, level, raid_disks, degraded,
		                       state, sync_action, sync_done, sync_total,
		                       sync_speed_kib, mismatch_cnt, members)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (host_id, name) DO UPDATE
		   SET ts             = EXCLUDED.ts,
		       level          = EXCLUDED.level,
		       raid_disks     = EXCLUDED.raid_disks,
		       degraded       = EXCLUDED.degraded,
		       state          = EXCLUDED.state,
		       sync_action    = EXCLUDED.sync_action,
		       sync_done      = EXCLUDED.sync_done,
		       sync_total     = EXCLUDED.sync_total,
		       sync_speed_kib = EXCLUDED.sync_speed_kib,
		       mismatch_cnt   = EXCLUDED.mismatch_cnt,
		       members        = EXCLUDED.members
		 WHERE md_arrays.ts <= EXCLUDED.ts`

	for _, a := range snap.GetArrays() {
		if a.GetName() == "" {
			continue
		}
		members := make([]mdMemberJSON, 0, len(a.GetMembers()))
		for _, m := range a.GetMembers() {
			members = append(members, mdMemberJSON{Device: m.GetDevice(), State: m.GetState(), Slot: m.Slot})
		}
		raw, err := json.Marshal(members)
		if err != nil {
			return fmt.Errorf("md array %s members: %w", a.GetName(), err)
		}
		batch.Queue(upsert, hostID, a.GetName(), ts, a.GetLevel(),
			int32(a.GetRaidDisks()), int32(a.GetDegraded()), //nolint:gosec // device counts, far below int32
			a.GetState(), a.GetSyncAction(),
			int64OrNil(a.SyncDoneSectors), int64OrNil(a.SyncTotalSectors),
			int64OrNil(a.SyncSpeedKibs), int64OrNil(a.MismatchCnt), raw)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin md snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The row guards alone would let an older snapshot INSERT an array a
	// newer one already removed -- there is no row left to compare against.
	// So the whole snapshot is judged against the newest the host has stored,
	// under a row lock on the host so two batches cannot interleave. A host
	// whose rows were all deleted by an empty snapshot has nothing to compare
	// with, and the agent never sends an older snapshot after a newer one (it
	// keeps only the newest, until acked), so that gap stays theoretical.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM hosts WHERE id = $1 FOR UPDATE`, hostID); err != nil {
		return fmt.Errorf("lock host for md snapshot: %w", err)
	}
	var stale bool
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(ts) > $2, false) FROM md_arrays WHERE host_id = $1`,
		hostID, ts).Scan(&stale); err != nil {
		return fmt.Errorf("compare md snapshot: %w", err)
	}
	if stale {
		return nil
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("apply md snapshot: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit md snapshot: %w", err)
	}
	return nil
}
