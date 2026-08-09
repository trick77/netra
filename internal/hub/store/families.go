package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// This file holds the per-entity families the Group 1-4 collectors write.
//
// Two rules run through all of it.
//
// ON CONFLICT DO NOTHING everywhere, for the reason InsertHostSamples has it:
// a replayed batch re-sends rows the hub already stored, and failing the
// INSERT would 503 the flush and pin the agent's ring buffer on a batch it can
// never land.
//
// Agents send NATURAL keys -- container_key, chip+label, device, mountpoint --
// and the hub resolves them to the surrogate ids the hypertables reference.
// An agent cannot know an id the hub assigns, and making it ask would add a
// round trip the protocol deliberately does not have.

// execBatch sends a batch and totals the rows affected. Every family below
// inserts the same way, so the loop lives here once.
func execBatch(ctx context.Context, tx interface {
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
}, batch *pgx.Batch, n int, what string) (int64, error) {
	results := tx.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()

	var inserted int64
	for range n {
		tag, err := results.Exec()
		if err != nil {
			return 0, fmt.Errorf("insert %s: %w", what, err)
		}
		inserted += tag.RowsAffected()
	}
	return inserted, nil
}

func tsOf(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// --------------------------------------------------------------- disk I/O

// InsertDiskIoSamples writes one row per block device.
func (s *Store) InsertDiskIoSamples(ctx context.Context, hostID int32, rows []*netrav1.DiskIoSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO disk_io_samples (
			host_id, ts, device, read_bytes, write_bytes, read_ops, write_ops,
			io_util_pct, r_await_ms, w_await_ms, weighted_io_pct
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (host_id, ts, device) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetDevice(),
			r.ReadBytes, r.WriteBytes, r.ReadOps, r.WriteOps,
			r.IoUtilPct, r.RAwaitMs, r.WAwaitMs, r.WeightedIoPct)
	}
	return execBatch(ctx, s.pool, batch, len(rows), "disk io sample")
}

// ----------------------------------------------------------------- network

// InsertNetSamples writes one row per interface.
func (s *Store) InsertNetSamples(ctx context.Context, hostID int32, rows []*netrav1.NetSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO net_samples (host_id, ts, iface, rx_bytes, tx_bytes, rx_errs, tx_errs)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (host_id, ts, iface) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetIface(),
			r.RxBytes, r.TxBytes, r.RxErrs, r.TxErrs)
	}
	return execBatch(ctx, s.pool, batch, len(rows), "net sample")
}

// ---------------------------------------------------- collector telemetry

// InsertCollectorSamples writes each collector's own health for one scrape.
func (s *Store) InsertCollectorSamples(ctx context.Context, hostID int32, rows []*netrav1.CollectorSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO collector_samples (host_id, ts, collector, duration_ms, ok, error_code)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id, ts, collector) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetCollector(),
			r.DurationMs, r.GetOk(), r.ErrorCode)
	}
	return execBatch(ctx, s.pool, batch, len(rows), "collector sample")
}

// ------------------------------------------------------------------ events

// InsertEvents writes discrete state changes.
func (s *Store) InsertEvents(ctx context.Context, hostID int32, rows []*netrav1.Event) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO events (host_id, ts, type, subject, detail)
		VALUES ($1, $2, $3, $4, COALESCE($5::jsonb, '{}'::jsonb))
		ON CONFLICT (host_id, ts, type, subject) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		var subject *string
		if r.GetSubject() != "" {
			v := r.GetSubject()
			subject = &v
		}
		var detail *string
		if r.GetDetailJson() != "" {
			v := r.GetDetailJson()
			detail = &v
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetType(), subject, detail)
	}
	return execBatch(ctx, s.pool, batch, len(rows), "event")
}

// -------------------------------------------------------------- dimensions

// resolveSensorIDs upserts the sensors named in rows and returns
// "chip\x00label" -> id.
//
// ON CONFLICT DO UPDATE rather than DO NOTHING: DO NOTHING makes RETURNING
// yield no row for an existing sensor, so the second scrape would resolve
// nothing and drop every sample. Updating a column to itself is the standard
// way to force the row back.
func (s *Store) resolveSensorIDs(ctx context.Context, hostID int32, rows []*netrav1.SensorSample) (map[string]int32, error) {
	out := make(map[string]int32)
	if len(rows) == 0 {
		return out, nil
	}

	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		key := r.GetChip() + "\x00" + r.GetLabel()
		if seen[key] {
			continue
		}
		seen[key] = true

		var id int32
		if err := s.pool.QueryRow(ctx, `
			INSERT INTO sensors (host_id, chip, label) VALUES ($1, $2, $3)
			ON CONFLICT (host_id, chip, label) DO UPDATE SET chip = EXCLUDED.chip
			RETURNING id`, hostID, r.GetChip(), r.GetLabel()).Scan(&id); err != nil {
			return nil, fmt.Errorf("resolve sensor %s/%s: %w", r.GetChip(), r.GetLabel(), err)
		}
		out[key] = id
	}
	return out, nil
}

// InsertSensorSamples resolves chip+label to sensor ids and writes the rows.
func (s *Store) InsertSensorSamples(ctx context.Context, hostID int32, rows []*netrav1.SensorSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids, err := s.resolveSensorIDs(ctx, hostID, rows)
	if err != nil {
		return 0, err
	}

	const stmt = `
		INSERT INTO sensor_samples (host_id, ts, sensor_id, temp)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (host_id, ts, sensor_id) DO NOTHING`

	batch := &pgx.Batch{}
	queued := 0
	for _, r := range rows {
		id, ok := ids[r.GetChip()+"\x00"+r.GetLabel()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id, r.Temp)
		queued++
	}
	return execBatch(ctx, s.pool, batch, queued, "sensor sample")
}

// resolveContainerIDs upserts the containers named in rows and returns
// container_key -> id.
//
// The image is updated on conflict because a service that was redeployed has a
// new one, and leaving the old value would describe a container that no longer
// exists.
func (s *Store) resolveContainerIDs(ctx context.Context, hostID int32, rows []*netrav1.ContainerSample) (map[string]int32, error) {
	out := make(map[string]int32)
	if len(rows) == 0 {
		return out, nil
	}

	for _, r := range rows {
		key := r.GetContainerKey()
		if key == "" || out[key] != 0 {
			continue
		}

		var id int32
		if err := s.pool.QueryRow(ctx, `
			INSERT INTO containers (host_id, container_key, name, image, is_agent)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (host_id, container_key) DO UPDATE
			   SET name = EXCLUDED.name, image = EXCLUDED.image, is_agent = EXCLUDED.is_agent
			RETURNING id`,
			hostID, key, r.GetName(), r.GetImage(), r.GetIsAgent()).Scan(&id); err != nil {
			return nil, fmt.Errorf("resolve container %s: %w", key, err)
		}
		out[key] = id
	}
	return out, nil
}

// InsertContainerSamples resolves container keys to ids and writes the rows.
func (s *Store) InsertContainerSamples(ctx context.Context, hostID int32, rows []*netrav1.ContainerSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids, err := s.resolveContainerIDs(ctx, hostID, rows)
	if err != nil {
		return 0, err
	}

	const stmt = `
		INSERT INTO container_samples (
			host_id, ts, container_id, cpu_pct, mem_used, mem_limit,
			net_rx, net_tx, io_read, io_write
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (host_id, ts, container_id) DO NOTHING`

	batch := &pgx.Batch{}
	queued := 0
	for _, r := range rows {
		id, ok := ids[r.GetContainerKey()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id,
			r.CpuPct, int64OrNil(r.MemUsed), int64OrNil(r.MemLimit),
			r.NetRx, r.NetTx, r.IoRead, r.IoWrite)
		queued++
	}
	return execBatch(ctx, s.pool, batch, queued, "container sample")
}

// resolveFilesystemIDs upserts the filesystems named in rows and returns
// label -> id.
func (s *Store) resolveFilesystemIDs(ctx context.Context, hostID int32, rows []*netrav1.FilesystemSample) (map[string]int32, error) {
	out := make(map[string]int32)
	if len(rows) == 0 {
		return out, nil
	}

	for _, r := range rows {
		label := r.GetLabel()
		if label == "" || out[label] != 0 {
			continue
		}

		var id int32
		if err := s.pool.QueryRow(ctx, `
			INSERT INTO filesystems (host_id, label, mountpoint, device_id)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (host_id, label) DO UPDATE
			   SET mountpoint = EXCLUDED.mountpoint, device_id = EXCLUDED.device_id
			RETURNING id`,
			hostID, label, r.GetMountpoint(), int64OrNil(r.DeviceId)).Scan(&id); err != nil {
			return nil, fmt.Errorf("resolve filesystem %s: %w", label, err)
		}
		out[label] = id
	}
	return out, nil
}

// InsertFilesystemSamples resolves labels to filesystem ids and writes rows.
func (s *Store) InsertFilesystemSamples(ctx context.Context, hostID int32, rows []*netrav1.FilesystemSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids, err := s.resolveFilesystemIDs(ctx, hostID, rows)
	if err != nil {
		return 0, err
	}

	const stmt = `
		INSERT INTO filesystem_samples (
			host_id, ts, fs_id, total, used, free,
			inodes_total, inodes_used, read_bytes, write_bytes
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (host_id, ts, fs_id) DO NOTHING`

	batch := &pgx.Batch{}
	queued := 0
	for _, r := range rows {
		id, ok := ids[r.GetLabel()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id,
			int64OrNil(r.Total), int64OrNil(r.Used), int64OrNil(r.Free),
			int64OrNil(r.InodesTotal), int64OrNil(r.InodesUsed),
			r.ReadBytes, r.WriteBytes)
		queued++
	}
	return execBatch(ctx, s.pool, batch, queued, "filesystem sample")
}

// resolveDeviceIDs upserts the drives named in rows and returns device -> id.
func (s *Store) resolveDeviceIDs(ctx context.Context, hostID int32, rows []*netrav1.SmartAttribute) (map[string]int32, error) {
	out := make(map[string]int32)
	if len(rows) == 0 {
		return out, nil
	}

	for _, r := range rows {
		name := r.GetDevice()
		if name == "" || out[name] != 0 {
			continue
		}

		var id int32
		if err := s.pool.QueryRow(ctx, `
			INSERT INTO devices (host_id, device, model, serial)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (host_id, device) DO UPDATE
			   SET model = EXCLUDED.model, serial = EXCLUDED.serial
			RETURNING id`,
			hostID, name, r.GetModel(), r.GetSerial()).Scan(&id); err != nil {
			return nil, fmt.Errorf("resolve device %s: %w", name, err)
		}
		out[name] = id
	}
	return out, nil
}

// InsertSmartAttributes resolves device names to ids and writes the rows.
func (s *Store) InsertSmartAttributes(ctx context.Context, hostID int32, rows []*netrav1.SmartAttribute) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids, err := s.resolveDeviceIDs(ctx, hostID, rows)
	if err != nil {
		return 0, err
	}

	const stmt = `
		INSERT INTO smart_attributes (host_id, ts, device_id, attr_id, raw, normalized)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id, ts, device_id, attr_id) DO NOTHING`

	batch := &pgx.Batch{}
	queued := 0
	for _, r := range rows {
		id, ok := ids[r.GetDevice()]
		if !ok {
			continue
		}
		var normalized *int16
		if r.Normalized != nil {
			v := int16(r.GetNormalized())
			normalized = &v
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id, int16(r.GetAttrId()), r.Raw, normalized)
		queued++
	}
	return execBatch(ctx, s.pool, batch, queued, "smart attribute")
}

// ---------------------------------------------------------------- processes

// InsertProcessSamples writes one row per process name.
func (s *Store) InsertProcessSamples(ctx context.Context, hostID int32, rows []*netrav1.ProcessSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO process_samples (host_id, ts, name, cpu_pct, mem_bytes, count)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id, ts, name) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		var count *int32
		if r.Count != nil {
			v := int32(r.GetCount())
			count = &v
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetName(),
			r.CpuPct, int64OrNil(r.MemBytes), count)
	}
	return execBatch(ctx, s.pool, batch, len(rows), "process sample")
}

// --------------------------------------------------------------- inventory

// UpsertHostAddresses replaces the host's address set.
//
// scope is derived HERE, not by the agent (spec §5.2): the agent reports raw
// addresses, so the loopback/private/public rules are one implementation that
// can be corrected without redeploying every agent in the fleet.
//
// Addresses that vanished are deleted rather than left behind: an address the
// host no longer has is not inventory, it is a stale row that a subnet query
// would still match.
func (s *Store) UpsertHostAddresses(ctx context.Context, hostID int32, rows []*netrav1.HostAddress) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO host_addresses (host_id, iface, if_index, address, family, scope, vrf, description)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (host_id, iface, address) DO UPDATE
		   SET if_index = EXCLUDED.if_index, scope = EXCLUDED.scope,
		       vrf = EXCLUDED.vrf, description = EXCLUDED.description,
		       last_seen = now()`

	batch := &pgx.Batch{}
	for _, r := range rows {
		var ifIndex *int32
		if r.IfIndex != nil {
			v := int32(r.GetIfIndex())
			ifIndex = &v
		}
		batch.Queue(stmt, hostID, r.GetIface(), ifIndex, r.GetAddress(),
			int16(r.GetFamily()), AddressScope(r.GetAddress()), r.GetVrf(), r.GetDescription())
	}

	n, err := execBatch(ctx, s.pool, batch, len(rows), "host address")
	if err != nil {
		return 0, err
	}

	// Drop what the host no longer reports. The agent sends the whole set
	// whenever anything changes, so anything untouched by this batch is gone.
	keep := make([]string, 0, len(rows))
	for _, r := range rows {
		keep = append(keep, r.GetIface()+" "+r.GetAddress())
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM host_addresses
		 WHERE host_id = $1 AND (iface || ' ' || host(address)) <> ALL($2)`,
		hostID, keep); err != nil {
		return 0, fmt.Errorf("prune host addresses: %w", err)
	}

	return n, nil
}

// UpsertHostPackages replaces the host's package inventory.
func (s *Store) UpsertHostPackages(ctx context.Context, hostID int32, rows []*netrav1.HostPackage) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO host_packages (host_id, name, version, arch, format, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id, name, arch) DO UPDATE
		   SET version = EXCLUDED.version, format = EXCLUDED.format,
		       size_bytes = EXCLUDED.size_bytes, last_seen = now()`

	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(stmt, hostID, r.GetName(), r.GetVersion(), r.GetArch(),
			r.GetFormat(), int64OrNil(r.SizeBytes))
	}
	return execBatch(ctx, s.pool, batch, len(rows), "host package")
}

// InsertPackageEvents writes install, upgrade and remove events.
func (s *Store) InsertPackageEvents(ctx context.Context, hostID int32, rows []*netrav1.PackageEvent) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO package_events (host_id, ts, name, action, from_version, to_version)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id, ts, name) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), r.GetName(), r.GetAction(),
			emptyToNil(r.GetFromVersion()), emptyToNil(r.GetToVersion()))
	}
	return execBatch(ctx, s.pool, batch, len(rows), "package event")
}

// InsertSystemdUnitEvents resolves unit names to ids and writes the events.
func (s *Store) InsertSystemdUnitEvents(ctx context.Context, hostID int32, rows []*netrav1.SystemdUnitEvent) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids := make(map[string]int32, len(rows))
	for _, r := range rows {
		name := r.GetUnitName()
		if name == "" || ids[name] != 0 {
			continue
		}
		var id int32
		if err := s.pool.QueryRow(ctx, `
			INSERT INTO systemd_units (host_id, unit_name) VALUES ($1, $2)
			ON CONFLICT (host_id, unit_name) DO UPDATE SET unit_name = EXCLUDED.unit_name
			RETURNING id`, hostID, name).Scan(&id); err != nil {
			return 0, fmt.Errorf("resolve systemd unit %s: %w", name, err)
		}
		ids[name] = id
	}

	const stmt = `
		INSERT INTO systemd_unit_events (host_id, unit_id, ts, state, substate)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (host_id, unit_id, ts) DO NOTHING`

	batch := &pgx.Batch{}
	queued := 0
	for _, r := range rows {
		id, ok := ids[r.GetUnitName()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, id, tsOf(r.GetTsMs()), r.GetState(), emptyToNil(r.GetSubstate()))
		queued++
	}
	return execBatch(ctx, s.pool, batch, queued, "systemd unit event")
}

// int64OrNil converts an optional unsigned protobuf field to the signed type
// Postgres stores, preserving "unset" as NULL.
//
// The schema uses BIGINT rather than an unsigned type because Postgres has
// none. Values this large do not occur for byte counts on real hosts.
func int64OrNil(v *uint64) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

// emptyToNil maps proto3's zero-value empty string to SQL NULL, so "no
// previous version" is stored as the absence of one rather than as "".
func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
