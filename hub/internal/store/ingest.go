package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// InsertHostSamples writes a batch and returns the number of rows stored.
//
// Rows already present are skipped rather than updated: an agent replaying its
// ring buffer after an outage re-sends batches the hub may already hold, and
// the first write is authoritative.
//
// This uses a multi-row INSERT rather than COPY because COPY cannot express
// ON CONFLICT. Batches are one scrape deep, so the row count per statement is
// small and the difference does not matter here; bulk backfill paths in later
// plans may revisit this.
func (s *Store) InsertHostSamples(ctx context.Context, hostID int32, samples []*netrav1.HostSample) (int64, error) {
	if len(samples) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO host_samples (
			host_id, ts,
			cpu_total, cpu_user, cpu_system, cpu_iowait, cpu_steal, cpu_idle,
			mem_total, mem_used, mem_available, mem_buffcache, mem_zfs_arc,
			swap_total, swap_used,
			load1, load5, load15, uptime_s
		) VALUES (
			$1, $2,
			$3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13,
			$14, $15,
			$16, $17, $18, $19
		)
		ON CONFLICT (host_id, ts) DO NOTHING`

	batch := &pgx.Batch{}
	for _, m := range samples {
		batch.Queue(stmt,
			hostID, time.UnixMilli(m.GetTsMs()).UTC(),
			f64(m.CpuTotal), f64(m.CpuUser), f64(m.CpuSystem),
			f64(m.CpuIowait), f64(m.CpuSteal), f64(m.CpuIdle),
			u64(m.MemTotal), u64(m.MemUsed), u64(m.MemAvailable),
			u64(m.MemBuffcache), u64(m.MemZfsArc),
			u64(m.SwapTotal), u64(m.SwapUsed),
			f64(m.Load1), f64(m.Load5), f64(m.Load15), u64(m.UptimeS),
		)
	}

	results := s.pool.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()

	var inserted int64
	for range samples {
		tag, err := results.Exec()
		if err != nil {
			return 0, fmt.Errorf("insert host sample: %w", err)
		}
		inserted += tag.RowsAffected()
	}

	return inserted, nil
}

// UpsertHostCurrent keeps the denormalised latest snapshot fresh so the host
// list never has to touch a hypertable.
func (s *Store) UpsertHostCurrent(ctx context.Context, hostID int32, m *netrav1.HostSample) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO host_current (host_id, last_seen, cpu_total, mem_used, mem_total, uptime_s)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id) DO UPDATE SET
			last_seen = EXCLUDED.last_seen,
			cpu_total = EXCLUDED.cpu_total,
			mem_used  = EXCLUDED.mem_used,
			mem_total = EXCLUDED.mem_total,
			uptime_s  = EXCLUDED.uptime_s
		WHERE host_current.last_seen IS NULL
		   OR host_current.last_seen <= EXCLUDED.last_seen`,
		hostID, time.UnixMilli(m.GetTsMs()).UTC(),
		f64(m.CpuTotal), u64(m.MemUsed), u64(m.MemTotal), u64(m.UptimeS))
	if err != nil {
		return fmt.Errorf("upsert host_current: %w", err)
	}
	return nil
}

// f64 and u64 map an unset protobuf optional to a SQL NULL. Returning the
// pointer directly would work for float64 but not for the uint64 -> int64
// column mapping, so both are explicit.
func f64(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func u64(p *uint64) any {
	if p == nil {
		return nil
	}
	return int64(*p)
}
