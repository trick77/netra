package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
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
//
// Sent through execBatch like every family in families.go, so this path cannot
// quietly opt out of the poison-row quarantine that keeps an unstorable row
// from wedging a host's ring buffer forever.
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
			load1, load5, load15, uptime_s,
			ctxt_per_s, intr_per_s, forks_per_s,
			procs_running, procs_blocked, boot_time_s,
			processes_total, users_logged_in,
			services_total, services_failed,
			tcp_retrans_segs_per_s, tcp_out_rsts_per_s, tcp_in_errs_per_s,
			tcp_active_opens_per_s, tcp_passive_opens_per_s,
			tcp_attempt_fails_per_s, tcp_curr_estab,
			tcp_listen_overflows_per_s, tcp_listen_drops_per_s,
			udp_in_errors_per_s, udp_rcvbuf_errors_per_s,
			udp_sndbuf_errors_per_s, udp_no_ports_per_s,
			ip_reasm_reqds_per_s, ip_reasm_fails_per_s,
			ip_frag_fails_per_s, ip_frag_creates_per_s,
			udp6_in_errors_per_s, udp6_rcvbuf_errors_per_s,
			udp6_sndbuf_errors_per_s, udp6_no_ports_per_s,
			ip6_reasm_reqds_per_s, ip6_reasm_fails_per_s,
			ip6_frag_fails_per_s, ip6_frag_creates_per_s,
			-- Appended rather than grouped with the other mem_ columns
			-- above, for the same reason the proto appends its field
			-- numbers: inserting them in the middle would renumber forty
			-- placeholders, and a transposed pair in a statement this long
			-- is invisible until a chart reads the wrong metric.
			mem_free, mem_buffers, mem_cached, mem_shared, mem_sreclaimable,
			pgmajfault_per_s, pswpin_per_s, pswpout_per_s, oom_kill_total,
			sockets_used, tcp_orphan, tcp_tw, tcp_alloc,
			fd_used, fd_limit, conntrack_count, conntrack_limit,
			tcp_tw_limit, tcp_orphan_limit
		) VALUES (
			$1, $2,
			$3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13,
			$14, $15,
			$16, $17, $18, $19,
			$20, $21, $22,
			$23, $24, $25,
			$26, $27,
			$28, $29,
			$30, $31, $32,
			$33, $34,
			$35, $36,
			$37, $38,
			$39, $40,
			$41, $42,
			$43, $44,
			$45, $46,
			$47, $48,
			$49, $50,
			$51, $52,
			$53, $54,
			$55, $56, $57, $58, $59,
			$60, $61, $62, $63,
			$64, $65, $66, $67,
			$68, $69, $70, $71,
			$72, $73
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
			f64(m.CtxtPerS), f64(m.IntrPerS), f64(m.ForksPerS),
			u32(m.ProcsRunning), u32(m.ProcsBlocked), u64(m.BootTimeS),
			u32(m.ProcessesTotal), u32(m.UsersLoggedIn),
			u32(m.ServicesTotal), u32(m.ServicesFailed),
			f64(m.TcpRetransSegsPerS), f64(m.TcpOutRstsPerS), f64(m.TcpInErrsPerS),
			f64(m.TcpActiveOpensPerS), f64(m.TcpPassiveOpensPerS),
			f64(m.TcpAttemptFailsPerS), u32(m.TcpCurrEstab),
			f64(m.TcpListenOverflowsPerS), f64(m.TcpListenDropsPerS),
			f64(m.UdpInErrorsPerS), f64(m.UdpRcvbufErrorsPerS),
			f64(m.UdpSndbufErrorsPerS), f64(m.UdpNoPortsPerS),
			f64(m.IpReasmReqdsPerS), f64(m.IpReasmFailsPerS),
			f64(m.IpFragFailsPerS), f64(m.IpFragCreatesPerS),
			f64(m.Udp6InErrorsPerS), f64(m.Udp6RcvbufErrorsPerS),
			f64(m.Udp6SndbufErrorsPerS), f64(m.Udp6NoPortsPerS),
			f64(m.Ip6ReasmReqdsPerS), f64(m.Ip6ReasmFailsPerS),
			f64(m.Ip6FragFailsPerS), f64(m.Ip6FragCreatesPerS),
			u64(m.MemFree), u64(m.MemBuffers), u64(m.MemCached),
			u64(m.MemShared), u64(m.MemSreclaimable),
			f64(m.PgmajfaultPerS), f64(m.PswpinPerS), f64(m.PswpoutPerS), u64(m.OomKillTotal),
			u32(m.SocketsUsed), u32(m.TcpOrphan), u32(m.TcpTw), u32(m.TcpAlloc),
			u64(m.FdUsed), u64(m.FdLimit), u32(m.ConntrackCount), u32(m.ConntrackLimit),
			u32(m.TcpTwLimit), u32(m.TcpOrphanLimit),
		)
	}

	return execBatch(ctx, s.pool, batch, "host sample")
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

// InsertCpuCoreSamples writes one row per CPU core.
//
// Unlike the host families above, these rows carry their own timestamps: a
// request holds a batch of scrapes, so the rows in it span several instants
// and are not positionally tied to any one host sample.
//
// ON CONFLICT DO NOTHING for the same reason InsertHostSamples has it -- a
// replayed batch re-sends rows the hub already stored, and failing the INSERT
// would pin the agent's ring buffer on a batch it can never land.
func (s *Store) InsertCpuCoreSamples(ctx context.Context, hostID int32, rows []*netrav1.CpuCoreSample) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO cpu_core_samples (host_id, ts, core, busy)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (host_id, ts, core) DO NOTHING`

	batch := &pgx.Batch{}
	for _, r := range rows {
		// Busy is passed as the *float64 it already is: nil becomes SQL NULL,
		// which means "not computable this scrape" -- a different fact from a
		// core genuinely measured at 0%.
		batch.Queue(stmt, hostID,
			time.UnixMilli(r.GetTsMs()).UTC(),
			int32(r.GetCore()),
			r.Busy)
	}

	return execBatch(ctx, s.pool, batch, "cpu core sample")
}

// InsertAgentSamples writes the agent self-telemetry carried by a batch and
// returns the number of rows stored.
//
// Samples without an AgentSample are skipped rather than written as an
// all-NULL row: an agent too old to send one has said nothing about itself,
// which is not the same as having reported nothing.
//
// Same conflict handling as InsertHostSamples, for the same reason -- these
// rows share that table's (host_id, ts) key and are replayed with it.
func (s *Store) InsertAgentSamples(ctx context.Context, hostID int32, samples []*netrav1.HostSample) (int64, error) {
	const stmt = `
		INSERT INTO agent_samples (
			host_id, ts,
			uptime_s, rss_bytes, goroutines,
			scrape_duration_ms, buffer_depth,
			buffer_dropped_total, post_failures_total, post_latency_ms,
			hub_connect_us, hub_connect_max_us, hub_connect_failures_total
		) VALUES (
			$1, $2,
			$3, $4, $5,
			$6, $7,
			$8, $9, $10,
			$11, $12, $13
		)
		ON CONFLICT (host_id, ts) DO NOTHING`

	batch := &pgx.Batch{}
	for _, m := range samples {
		a := m.GetAgent()
		if a == nil {
			continue
		}
		batch.Queue(stmt,
			hostID, time.UnixMilli(m.GetTsMs()).UTC(),
			u64(a.UptimeS), u64(a.RssBytes), u32(a.Goroutines),
			u32(a.ScrapeDurationMs), u32(a.BufferDepth),
			u64(a.BufferDroppedTotal), u64(a.PostFailuresTotal),
			u32(a.PostLatencyMs),
			u32(a.HubConnectUs), u32(a.HubConnectMaxUs), u64(a.HubConnectFailuresTotal),
		)
	}
	if batch.Len() == 0 {
		return 0, nil
	}

	return execBatch(ctx, s.pool, batch, "agent sample")
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

// u32 is the same mapping for the counts stored in INTEGER columns.
func u32(p *uint32) any {
	if p == nil {
		return nil
	}
	return int32(*p)
}

// MetadataHash returns the stored metadata hash for a host, or nil if the hub
// has never received one.
func (s *Store) MetadataHash(ctx context.Context, hostID int32) ([]byte, error) {
	var hash []byte
	err := s.pool.QueryRow(ctx,
		`SELECT metadata_hash FROM hosts WHERE id = $1`, hostID).Scan(&hash)
	if err != nil {
		return nil, fmt.Errorf("read metadata hash: %w", err)
	}
	return hash, nil
}

// SaveMetadata persists the static facts an agent reports, together with the
// hash that lets the hub detect the next change.
//
// The incoming fingerprint is compared against the one already on file
// before it is overwritten. A mismatch against a non-empty stored value is
// exactly what spec §7.2 asks the hub to catch: a compose file (and its
// token) copied to a second host. The request is still accepted and stored —
// flagging, not rejecting, is the specified behaviour.
//
// hostname is NOT written here, on purpose. The operator names a host when they
// create it, and that name is what rotate and delete are reasoned about in the
// UI. Letting the agent overwrite it renamed the row out from under them — and
// worse, could not be stored at all in the common case: every host created
// through the UI has site_id NULL, hosts_site_id_hostname_key is NULLS NOT
// DISTINCT, so two cloned VMs both reporting `raspberrypi` (or two agents both
// reporting nothing, writing ”) collide on 23505. This statement has no
// poisonRow quarantine, so that 23505 became a 503, and the agent answers a 503
// by re-sending the IDENTICAL batch — a permanent wedge on that host. The
// fingerprint warning above already covers the case reading the reported
// hostname was implicitly guarding.
//
// So Metadata.hostname is received and deliberately NOT read. The spec lists it
// among the metadata contents (§7.4) but nowhere asks the hub to record it over
// the name the admin API assigned, and hosts.hostname is that name. A missing
// assignment here is the point, not an omission.
func (s *Store) SaveMetadata(ctx context.Context, hostID int32, hash []byte, md *netrav1.Metadata) error {
	var storedFingerprint *string
	if err := s.pool.QueryRow(ctx,
		`SELECT fingerprint FROM hosts WHERE id = $1`, hostID).Scan(&storedFingerprint); err != nil {
		return fmt.Errorf("read stored fingerprint: %w", err)
	}
	if storedFingerprint != nil && *storedFingerprint != "" &&
		*storedFingerprint != md.GetFingerprint() {
		slog.Warn("host fingerprint changed; token may have been copied to another host",
			"host_id", hostID,
			"stored_fingerprint", *storedFingerprint,
			"reported_fingerprint", md.GetFingerprint())
	}

	_, err := s.pool.Exec(ctx, `
		UPDATE hosts SET
			fingerprint   = $2,
			host_type     = NULLIF($3, ''),
			agent_version = $4,
			go_version    = $5,
			build_commit  = $6,
			-- NULLIF on every field the agent may legitimately leave unset.
			-- BuildMetadata fills kernel, cpu_model, cores and memory_total
			-- from /proc, and leaves them empty when it is unreadable — a
			-- container without the host /proc mounted, or a permission
			-- error. Without NULLIF those saves would write '' and 0, and a
			-- host page would render "0 cores / 0 B RAM" as though it had been
			-- measured. An unset field reaches the database as NULL; that
			-- invariant is stated in both collector.go's package doc and the
			-- HostSample proto comment.
			kernel        = NULLIF($7, ''),
			os_name       = NULLIF($8, ''),
			arch          = NULLIF($9, ''),
			cpu_model     = NULLIF($10, ''),
			cores         = NULLIF($11, 0),
			threads       = NULLIF($12, 0),
			memory_total  = NULLIF($13, 0::BIGINT),
			metadata_hash = $14,
			-- The column is NOT NULL DEFAULT '{}', so an agent reporting no
			-- capabilities writes an empty object. Unlike the sample columns,
			-- absence here is not a distinct fact worth preserving: no
			-- collector reporting a capability and every collector reporting
			-- that it is fine are both "nothing to flag".
			capabilities  = $15
		WHERE id = $1`,
		hostID,
		md.GetFingerprint(), md.GetHostType(),
		md.GetAgentVersion(), md.GetGoVersion(), md.GetBuildCommit(),
		md.GetKernel(), md.GetOsName(), md.GetArch(), md.GetCpuModel(),
		int32(md.GetCores()), int32(md.GetThreads()), int64(md.GetMemoryTotal()),
		hash, capabilitiesJSON(md.GetCapabilities()))
	if err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}
	return nil
}

// capabilitiesJSON renders the capability map for the JSONB column.
//
// An agent reporting none writes '{}' rather than NULL, because the column is
// NOT NULL DEFAULT '{}' and an empty object is already its "nothing to say"
// value.
func capabilitiesJSON(caps map[string]string) any {
	const empty = "{}"

	if len(caps) == 0 {
		return empty
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		// A map[string]string cannot fail to marshal. Writing the empty
		// object is still better than failing the whole metadata save.
		slog.Warn("marshal capabilities", "err", err)
		return empty
	}
	return raw
}
