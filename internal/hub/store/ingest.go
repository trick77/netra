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
//
// netRx and netTx are the host's traffic summed across its interfaces at the
// most recent scrape, or nil when this post carried no net samples. They live
// here rather than being read back off net_samples because the fleet's
// traffic figure is a CURRENT RATE, and reading a rate out of a time series
// makes it depend on the window the series was fetched over -- which is the
// bug 0002_host_current_net.sql exists to end. A nil stores NULL, which the
// UI renders as absent; it must never become a zero, since "no samples" and
// "no traffic" are different facts.
func (s *Store) UpsertHostCurrent(
	ctx context.Context, hostID int32, m *netrav1.HostSample, netRx, netTx *float64,
) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO host_current (host_id, last_seen, cpu_total, mem_used, mem_total, uptime_s,
		                          net_rx_bytes, net_tx_bytes, services_total, services_failed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (host_id) DO UPDATE SET
			last_seen = EXCLUDED.last_seen,
			cpu_total = EXCLUDED.cpu_total,
			mem_used  = EXCLUDED.mem_used,
			mem_total = EXCLUDED.mem_total,
			uptime_s  = EXCLUDED.uptime_s,
			-- coalesce, unlike the columns above: a post can legitimately
			-- carry host samples and no net samples (the collector failed
			-- this scrape, or the capability is off), and overwriting a
			-- known rate with NULL would make the fleet tile flicker to
			-- absent and back. The other columns come from the host sample
			-- that is always present when this runs at all.
			net_rx_bytes = coalesce(EXCLUDED.net_rx_bytes, host_current.net_rx_bytes),
			net_tx_bytes = coalesce(EXCLUDED.net_tx_bytes, host_current.net_tx_bytes),
			-- coalesced for the same reason as the net columns, and a
			-- sharper one: the systemd collector goes quiet on a host with
			-- no systemd, and on any scrape where the D-Bus call failed.
			-- Overwriting the counts with NULL there would make the Units
			-- summary blink to absent every time the bus hiccuped.
			services_total  = coalesce(EXCLUDED.services_total, host_current.services_total),
			services_failed = coalesce(EXCLUDED.services_failed, host_current.services_failed)
		WHERE host_current.last_seen IS NULL
		   OR host_current.last_seen <= EXCLUDED.last_seen`,
		hostID, time.UnixMilli(m.GetTsMs()).UTC(),
		f64(m.CpuTotal), u64(m.MemUsed), u64(m.MemTotal), u64(m.UptimeS),
		f64(netRx), f64(netTx), u32(m.ServicesTotal), u32(m.ServicesFailed))
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

// InsertHostSnmpSamples writes the IP and ICMP MIB counters carried by a
// batch and returns the number of rows stored.
//
// A separate table and therefore a separate statement, not seventy more
// placeholders on InsertHostSamples. Three reasons, in order of weight: a
// continuous aggregate cannot gain a column, so these columns had to live
// somewhere host_samples' rollups would not have to be recreated for (see
// 0003_host_snmp_samples.sql); InsertAgentSamples is the existing precedent
// for a second table fed from the same []*netrav1.HostSample on the same
// natural key; and a 143-placeholder statement is past the point where a
// transposed argument can be caught by reading it.
//
// The two batches are not atomic with each other, which is already true of
// host_samples and agent_samples and safe for the same reason: both key on
// (host_id, ts) with ON CONFLICT DO NOTHING, so a replay after a failure
// between them makes the half that landed a no-op.
//
// A sample with none of the seventy set is skipped rather than written as an
// all-NULL row -- the first scrape after an agent restart has no baseline and
// so produces no rates at all, and a row of NULLs there would claim a
// measurement was taken.
func (s *Store) InsertHostSnmpSamples(ctx context.Context, hostID int32, samples []*netrav1.HostSample) (int64, error) {
	const stmt = `
		INSERT INTO host_snmp_samples (
			host_id, ts,
			ip_in_receives_per_s, ip_in_delivers_per_s, ip_out_requests_per_s,
			ip_forw_datagrams_per_s, ip_reasm_oks_per_s, ip_frag_oks_per_s,
			ip_in_hdr_errors_per_s, ip_in_addr_errors_per_s, ip_in_unknown_protos_per_s,
			ip_in_discards_per_s, ip_out_discards_per_s, ip_out_no_routes_per_s,
			ip_reasm_timeout_per_s, ip6_in_receives_per_s, ip6_in_delivers_per_s,
			ip6_out_requests_per_s, ip6_out_forw_datagrams_per_s, ip6_reasm_oks_per_s,
			ip6_frag_oks_per_s, ip6_in_hdr_errors_per_s, ip6_in_addr_errors_per_s,
			ip6_in_unknown_protos_per_s, ip6_in_discards_per_s, ip6_out_discards_per_s,
			ip6_out_no_routes_per_s, ip6_in_no_routes_per_s, ip6_in_too_big_errors_per_s,
			ip6_reasm_timeout_per_s, icmp_in_msgs_per_s, icmp_out_msgs_per_s,
			icmp_in_errors_per_s, icmp_out_errors_per_s, icmp_in_dest_unreachs_per_s,
			icmp_out_dest_unreachs_per_s, icmp_in_time_excds_per_s, icmp_out_time_excds_per_s,
			icmp_in_parm_probs_per_s, icmp_out_parm_probs_per_s, icmp_in_redirects_per_s,
			icmp_out_redirects_per_s, icmp_in_echos_per_s, icmp_out_echos_per_s,
			icmp_in_echo_reps_per_s, icmp_out_echo_reps_per_s, icmp6_in_msgs_per_s,
			icmp6_out_msgs_per_s, icmp6_in_errors_per_s, icmp6_out_errors_per_s,
			icmp6_in_dest_unreachs_per_s, icmp6_out_dest_unreachs_per_s, icmp6_in_time_excds_per_s,
			icmp6_out_time_excds_per_s, icmp6_in_parm_problems_per_s, icmp6_out_parm_problems_per_s,
			icmp6_in_pkt_too_bigs_per_s, icmp6_out_pkt_too_bigs_per_s, icmp6_in_redirects_per_s,
			icmp6_out_redirects_per_s, icmp6_in_echos_per_s, icmp6_out_echos_per_s,
			icmp6_in_echo_replies_per_s, icmp6_out_echo_replies_per_s, icmp6_in_neighbor_solicits_per_s,
			icmp6_out_neighbor_solicits_per_s, icmp6_in_neighbor_advertisements_per_s, icmp6_out_neighbor_advertisements_per_s,
			icmp6_in_router_solicits_per_s, icmp6_out_router_solicits_per_s, icmp6_in_router_advertisements_per_s,
			icmp6_out_router_advertisements_per_s
		) VALUES (
			$1, $2,
			$3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19, $20,
			$21, $22, $23, $24, $25, $26,
			$27, $28, $29, $30, $31, $32,
			$33, $34, $35, $36, $37, $38,
			$39, $40, $41, $42, $43, $44,
			$45, $46, $47, $48, $49, $50,
			$51, $52, $53, $54, $55, $56,
			$57, $58, $59, $60, $61, $62,
			$63, $64, $65, $66, $67, $68,
			$69, $70, $71, $72
		)
		ON CONFLICT (host_id, ts) DO NOTHING`

	batch := &pgx.Batch{}
	for _, m := range samples {
		args := []any{
			hostID, time.UnixMilli(m.GetTsMs()).UTC(),
			f64(m.IpInReceivesPerS), f64(m.IpInDeliversPerS), f64(m.IpOutRequestsPerS),
			f64(m.IpForwDatagramsPerS), f64(m.IpReasmOksPerS), f64(m.IpFragOksPerS),
			f64(m.IpInHdrErrorsPerS), f64(m.IpInAddrErrorsPerS), f64(m.IpInUnknownProtosPerS),
			f64(m.IpInDiscardsPerS), f64(m.IpOutDiscardsPerS), f64(m.IpOutNoRoutesPerS),
			f64(m.IpReasmTimeoutPerS), f64(m.Ip6InReceivesPerS), f64(m.Ip6InDeliversPerS),
			f64(m.Ip6OutRequestsPerS), f64(m.Ip6OutForwDatagramsPerS), f64(m.Ip6ReasmOksPerS),
			f64(m.Ip6FragOksPerS), f64(m.Ip6InHdrErrorsPerS), f64(m.Ip6InAddrErrorsPerS),
			f64(m.Ip6InUnknownProtosPerS), f64(m.Ip6InDiscardsPerS), f64(m.Ip6OutDiscardsPerS),
			f64(m.Ip6OutNoRoutesPerS), f64(m.Ip6InNoRoutesPerS), f64(m.Ip6InTooBigErrorsPerS),
			f64(m.Ip6ReasmTimeoutPerS), f64(m.IcmpInMsgsPerS), f64(m.IcmpOutMsgsPerS),
			f64(m.IcmpInErrorsPerS), f64(m.IcmpOutErrorsPerS), f64(m.IcmpInDestUnreachsPerS),
			f64(m.IcmpOutDestUnreachsPerS), f64(m.IcmpInTimeExcdsPerS), f64(m.IcmpOutTimeExcdsPerS),
			f64(m.IcmpInParmProbsPerS), f64(m.IcmpOutParmProbsPerS), f64(m.IcmpInRedirectsPerS),
			f64(m.IcmpOutRedirectsPerS), f64(m.IcmpInEchosPerS), f64(m.IcmpOutEchosPerS),
			f64(m.IcmpInEchoRepsPerS), f64(m.IcmpOutEchoRepsPerS), f64(m.Icmp6InMsgsPerS),
			f64(m.Icmp6OutMsgsPerS), f64(m.Icmp6InErrorsPerS), f64(m.Icmp6OutErrorsPerS),
			f64(m.Icmp6InDestUnreachsPerS), f64(m.Icmp6OutDestUnreachsPerS), f64(m.Icmp6InTimeExcdsPerS),
			f64(m.Icmp6OutTimeExcdsPerS), f64(m.Icmp6InParmProblemsPerS), f64(m.Icmp6OutParmProblemsPerS),
			f64(m.Icmp6InPktTooBigsPerS), f64(m.Icmp6OutPktTooBigsPerS), f64(m.Icmp6InRedirectsPerS),
			f64(m.Icmp6OutRedirectsPerS), f64(m.Icmp6InEchosPerS), f64(m.Icmp6OutEchosPerS),
			f64(m.Icmp6InEchoRepliesPerS), f64(m.Icmp6OutEchoRepliesPerS), f64(m.Icmp6InNeighborSolicitsPerS),
			f64(m.Icmp6OutNeighborSolicitsPerS), f64(m.Icmp6InNeighborAdvertisementsPerS), f64(m.Icmp6OutNeighborAdvertisementsPerS),
			f64(m.Icmp6InRouterSolicitsPerS), f64(m.Icmp6OutRouterSolicitsPerS), f64(m.Icmp6InRouterAdvertisementsPerS),
			f64(m.Icmp6OutRouterAdvertisementsPerS),
		}
		// args[2:] is the seventy values; scanning them beats seventy
		// explicit nil checks, which would repeat the field list a second
		// time and give it a second chance to fall out of order.
		if !anyNonNil(args[2:]) {
			continue
		}
		batch.Queue(stmt, args...)
	}
	if batch.Len() == 0 {
		return 0, nil
	}

	return execBatch(ctx, s.pool, batch, "host snmp sample")
}

// anyNonNil reports whether any of the values is a stored measurement rather
// than a SQL NULL. f64 returns an untyped nil for an unset optional, so a
// plain interface comparison is enough.
func anyNonNil(vals []any) bool {
	for _, v := range vals {
		if v != nil {
			return true
		}
	}
	return false
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
