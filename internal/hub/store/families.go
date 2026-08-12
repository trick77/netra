package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// batchExecer is the pool as this file uses it: batches for the bulk path,
// single statements for the quarantine path below.
type batchExecer interface {
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// poisonRow reports whether err is a row the hub can never store -- one that
// will fail identically on every retry -- rather than a database that is
// merely unavailable.
//
// This distinction is the whole point. A 503 tells the agent to re-send, and
// the agent re-sends the IDENTICAL batch: for a transient failure that is
// exactly right, and for an unstorable row it is a permanent wedge, because
// the ring buffer only drops a prefix the hub acknowledged. A NUL byte in a
// process comm (22021) or an address INET will not parse (22P02) would
// otherwise stall every later scrape on that host forever.
//
// Class 22 (data exception) and class 23 (integrity constraint violation) are
// the only two treated this way, ON PURPOSE. Class 42 -- undefined table,
// syntax error -- is equally permanent but is the HUB's bug rather than the
// agent's, and quarantining it would drop every row of a family and answer
// 200, turning a schema mistake into silent fleet-wide data loss. Everything
// else (class 08 connection, 53 resources, 57 operator intervention, a
// context deadline) is transient and must reach the agent as a retry.
func poisonRow(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23")
}

// execBatch sends a batch and totals the rows affected, quarantining the rows
// Postgres refuses. Every family below inserts the same way, so the loop lives
// here once -- and so does the quarantine, which a family must not be able to
// opt out of by accident.
func execBatch(ctx context.Context, db batchExecer, batch *pgx.Batch, what string) (int64, error) {
	inserted, err := sendBatch(ctx, db, batch)
	if err == nil {
		return inserted, nil
	}
	if !poisonRow(err) {
		return 0, fmt.Errorf("insert %s: %w", what, err)
	}
	return quarantine(ctx, db, batch, what, err)
}

// sendBatch is the fast path: one round trip for the whole family.
func sendBatch(ctx context.Context, db batchExecer, batch *pgx.Batch) (int64, error) {
	results := db.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()

	var inserted int64
	for range batch.Len() {
		tag, err := results.Exec()
		if err != nil {
			return 0, err
		}
		inserted += tag.RowsAffected()
	}
	return inserted, nil
}

// quarantine re-runs a poisoned batch one statement at a time, keeping every
// row Postgres accepts and dropping the ones it does not.
//
// The WHOLE batch is replayed, not just the rows from the failure onward. pgx
// sends a batch with a single trailing Sync, which makes it one implicit
// transaction: a failure anywhere rolls back the rows that had already
// reported success, so after a poisoned batch nothing at all was stored.
// Replaying is safe because every statement in this file is ON CONFLICT DO
// NOTHING or DO UPDATE, so a row that did land is absorbed rather than
// duplicated -- the same property that makes a post-outage replay safe.
//
// One round trip per row is the cost, paid only on a path that is rare by
// construction: the agent generates these rows from the kernel, so a value
// Postgres rejects means a genuinely malformed name or address.
func quarantine(ctx context.Context, db batchExecer, batch *pgx.Batch, what string, cause error) (int64, error) {
	var inserted, dropped int64

	for _, q := range batch.QueuedQueries {
		tag, err := db.Exec(ctx, q.SQL, q.Arguments...)
		if err == nil {
			inserted += tag.RowsAffected()
			continue
		}
		if !poisonRow(err) {
			// The database went away mid-quarantine. That is a retry, not a
			// drop.
			return 0, fmt.Errorf("insert %s: %w", what, err)
		}
		dropped++
	}

	if dropped == 0 {
		// Every statement succeeded on its own, so the batch failed for a
		// reason that did not survive being taken apart. Saying "dropped
		// rows" here would send an operator looking for data loss that did
		// not happen.
		slog.Warn("a batch failed but every row stored individually",
			"family", what, "kept", inserted, "err", cause)
		return inserted, nil
	}

	slog.Warn("dropped rows Postgres refused to store",
		"family", what, "dropped", dropped, "kept", inserted, "err", cause)
	return inserted, nil
}

// resolveOne runs one dimension upsert, distinguishing a natural key the hub
// can never store from a database that is merely unavailable.
//
// ok=false means the key was poison -- a NUL byte in a container name, a label
// Postgres will not accept. The caller leaves it out of the id map, and the
// `id, ok := ids[...]` guard every family already has drops the samples that
// referenced it. Without this the resolvers would 503 the request BEFORE the
// batch quarantine ever ran, which is the same permanent wedge by an earlier
// door.
func (s *Store) resolveOne(ctx context.Context, dimension, key, stmt string, args ...any) (int32, bool, error) {
	var id int32
	if err := s.pool.QueryRow(ctx, stmt, args...).Scan(&id); err != nil {
		if poisonRow(err) {
			slog.Warn("dropped a dimension row Postgres refused to store",
				"dimension", dimension, "key", key, "err", err)
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("resolve %s %s: %w", dimension, key, err)
	}
	return id, true, nil
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
	return execBatch(ctx, s.pool, batch, "disk io sample")
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
	return execBatch(ctx, s.pool, batch, "net sample")
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
	return execBatch(ctx, s.pool, batch, "collector sample")
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
	return execBatch(ctx, s.pool, batch, "event")
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

		// An empty kind means an agent predating the field, and temperature
		// is the only kind such an agent could have sent. Defaulted here
		// rather than left to the column default so an UPDATE of an existing
		// row is equally well-defined.
		kind := r.GetKind()
		if kind == "" {
			kind = "temperature"
		}

		id, ok, err := s.resolveOne(ctx, "sensor", r.GetChip()+"/"+r.GetLabel(), `
			INSERT INTO sensors (host_id, chip, label, kind) VALUES ($1, $2, $3, $4)
			ON CONFLICT (host_id, chip, label) DO UPDATE SET kind = EXCLUDED.kind
			RETURNING id`, hostID, r.GetChip(), r.GetLabel(), kind)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
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
		INSERT INTO sensor_samples (host_id, ts, sensor_id, temp, value)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (host_id, ts, sensor_id) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		id, ok := ids[r.GetChip()+"\x00"+r.GetLabel()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id, r.Temp, r.Value)
	}
	return execBatch(ctx, s.pool, batch, "sensor sample")
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

	// tried records every key this batch has already attempted, INCLUDING the
	// ones that failed. Keying the skip on the output map alone would re-issue
	// a failed query for every row carrying the same poison key -- a host with
	// two hundred samples for one unstorable name would make two hundred round
	// trips and log two hundred warnings, on every ingest, forever, since the
	// agent keeps re-sending it.
	tried := make(map[string]bool, len(rows))

	for _, r := range rows {
		key := r.GetContainerKey()
		if key == "" || tried[key] {
			continue
		}
		tried[key] = true

		id, ok, err := s.resolveOne(ctx, "container", key, `
			INSERT INTO containers (host_id, container_key, name, image, is_agent)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (host_id, container_key) DO UPDATE
			   SET name = EXCLUDED.name, image = EXCLUDED.image, is_agent = EXCLUDED.is_agent
			RETURNING id`,
			hostID, key, r.GetName(), r.GetImage(), r.GetIsAgent())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
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
			net_rx, net_tx, io_read, io_write,
			cpu_user, cpu_system, mem_anon, mem_file, mem_shmem, mem_kernel
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		          $11, $12, $13, $14, $15, $16)
		ON CONFLICT (host_id, ts, container_id) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		id, ok := ids[r.GetContainerKey()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id,
			r.CpuPct, int64OrNil(r.MemUsed), int64OrNil(r.MemLimit),
			r.NetRx, r.NetTx, r.IoRead, r.IoWrite,
			r.CpuUser, r.CpuSystem,
			int64OrNil(r.MemAnon), int64OrNil(r.MemFile),
			int64OrNil(r.MemShmem), int64OrNil(r.MemKernel))
	}
	return execBatch(ctx, s.pool, batch, "container sample")
}

// resolveFilesystemIDs upserts the filesystems named in rows and returns
// label -> id.
func (s *Store) resolveFilesystemIDs(ctx context.Context, hostID int32, rows []*netrav1.FilesystemSample) (map[string]int32, error) {
	out := make(map[string]int32)
	if len(rows) == 0 {
		return out, nil
	}

	// See resolveContainerIDs on why the skip is keyed on attempts rather than
	// on successful resolutions.
	tried := make(map[string]bool, len(rows))

	for _, r := range rows {
		label := r.GetLabel()
		if label == "" || tried[label] {
			continue
		}
		tried[label] = true

		id, ok, err := s.resolveOne(ctx, "filesystem", label, `
			INSERT INTO filesystems (host_id, label, mountpoint, device_id)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (host_id, label) DO UPDATE
			   SET mountpoint = EXCLUDED.mountpoint, device_id = EXCLUDED.device_id
			RETURNING id`,
			hostID, label, r.GetMountpoint(), int64OrNil(r.DeviceId))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
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
	for _, r := range rows {
		id, ok := ids[r.GetLabel()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, tsOf(r.GetTsMs()), id,
			int64OrNil(r.Total), int64OrNil(r.Used), int64OrNil(r.Free),
			int64OrNil(r.InodesTotal), int64OrNil(r.InodesUsed),
			r.ReadBytes, r.WriteBytes)
	}
	return execBatch(ctx, s.pool, batch, "filesystem sample")
}

// resolveDeviceIDs upserts the drives named in rows and returns device -> id.
func (s *Store) resolveDeviceIDs(ctx context.Context, hostID int32, rows []*netrav1.SmartAttribute) (map[string]int32, error) {
	out := make(map[string]int32)
	if len(rows) == 0 {
		return out, nil
	}

	// See resolveContainerIDs on why the skip is keyed on attempts rather than
	// on successful resolutions.
	tried := make(map[string]bool, len(rows))

	for _, r := range rows {
		name := r.GetDevice()
		if name == "" || tried[name] {
			continue
		}
		tried[name] = true

		id, ok, err := s.resolveOne(ctx, "device", name, `
			INSERT INTO devices (host_id, device, model, serial)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (host_id, device) DO UPDATE
			   SET model = EXCLUDED.model, serial = EXCLUDED.serial
			RETURNING id`,
			hostID, name, r.GetModel(), r.GetSerial())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
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
	}
	return execBatch(ctx, s.pool, batch, "smart attribute")
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
	return execBatch(ctx, s.pool, batch, "process sample")
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

	n, err := execBatch(ctx, s.pool, batch, "host address")
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
		if !poisonRow(err) {
			return 0, fmt.Errorf("prune host addresses: %w", err)
		}
		// One unstorable address in the keep set poisons the comparison. A
		// stale row outliving its address is a wrong answer to a subnet
		// query; a 503 here is a permanent wedge. Skip this round's prune and
		// let the next scrape -- which will carry the same set minus whatever
		// the quarantine dropped -- do it.
		slog.Warn("skipped the address prune: the keep set carries a value Postgres refuses",
			"host_id", hostID, "err", err)
	}

	return n, nil
}

// UpsertHostPackages replaces the host's package inventory.
//
// Packages that vanished are deleted, for the reason UpsertHostAddresses
// prunes: `apt remove nginx` writes a remove row to package_events, and an
// inventory that still lists nginx contradicts it. The two halves of the same
// answer must not disagree.
//
// An EMPTY set is "the agent did not re-parse", never "this host has no
// packages", and the early return above is what keeps those apart. The
// packages collector parses only when the database mtime moved or the daily
// floor elapsed, and sends nothing otherwise; a parse error returns before
// Packages is populated, so a partial set cannot arise. The client makes the
// same guarantee on the wire: it carries the newest NON-EMPTY set of a batch
// whole rather than concatenating sets, and maxBatchRows drops entire scrapes
// rather than truncating a family. So a non-empty set is always a complete
// inventory, which is exactly what makes deleting the difference safe.
//
// The residual gap is a host that legitimately loses its last package. It
// keeps a stale row, because that is indistinguishable here from a scrape
// that did not re-parse. Closing it needs an explicit "empty" signal on the
// wire, which the protocol does not have; a host with zero packages is not a
// state a running Linux system reaches.
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

	n, err := execBatch(ctx, s.pool, batch, "host package")
	if err != nil {
		return 0, err
	}

	// Drop what the host no longer has installed. Keyed on name AND arch,
	// matching the primary key: a multiarch Debian carries the same package
	// for amd64 and i386 as two separate installations, and pruning on name
	// alone would delete one of them every time the other was reported.
	keep := make([]string, 0, len(rows))
	for _, r := range rows {
		keep = append(keep, r.GetName()+" "+r.GetArch())
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM host_packages
		 WHERE host_id = $1 AND (name || ' ' || arch) <> ALL($2)`,
		hostID, keep); err != nil {
		if !poisonRow(err) {
			return 0, fmt.Errorf("prune host packages: %w", err)
		}
		// Same trade as the address prune: a stale row beats a wedged agent.
		slog.Warn("skipped the package prune: the keep set carries a value Postgres refuses",
			"host_id", hostID, "err", err)
	}

	return n, nil
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
	return execBatch(ctx, s.pool, batch, "package event")
}

// InsertSystemdUnitEvents resolves unit names to ids and writes the events.
func (s *Store) InsertSystemdUnitEvents(ctx context.Context, hostID int32, rows []*netrav1.SystemdUnitEvent) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	ids := make(map[string]int32, len(rows))
	// See resolveContainerIDs on why the skip is keyed on attempts rather than
	// on successful resolutions.
	tried := make(map[string]bool, len(rows))
	for _, r := range rows {
		name := r.GetUnitName()
		if name == "" || tried[name] {
			continue
		}
		tried[name] = true
		id, ok, err := s.resolveOne(ctx, "systemd unit", name, `
			INSERT INTO systemd_units (host_id, unit_name) VALUES ($1, $2)
			ON CONFLICT (host_id, unit_name) DO UPDATE SET unit_name = EXCLUDED.unit_name
			RETURNING id`, hostID, name)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		ids[name] = id
	}

	const stmt = `
		INSERT INTO systemd_unit_events (host_id, unit_id, ts, state, substate)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (host_id, unit_id, ts) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		id, ok := ids[r.GetUnitName()]
		if !ok {
			continue
		}
		batch.Queue(stmt, hostID, id, tsOf(r.GetTsMs()), r.GetState(), emptyToNil(r.GetSubstate()))
	}
	return execBatch(ctx, s.pool, batch, "systemd unit event")
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
