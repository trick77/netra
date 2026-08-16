-- netra:no-transaction
-- TimescaleDB refuses to create a continuous aggregate inside a transaction
-- block, so this whole migration runs outside one.
--
-- Because there is no transaction to roll back, every statement here must be
-- individually re-runnable. applyMigration only records the migration in
-- schema_migrations once all of them have succeeded, so a failure part-way
-- through (a busy job scheduler, a dropped connection) leaves the schema
-- half-built and the migration unrecorded — and the hub re-runs this file
-- from the top on its next start. Without IF NOT EXISTS the first
-- already-applied statement would then fail with 42P07 and the hub would
-- refuse to start, permanently, until someone hand-edited schema_migrations.

CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE IF NOT EXISTS providers (
    id   INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS sites (
    id           INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider_id  INTEGER REFERENCES providers (id),
    name         TEXT NOT NULL,
    facility     TEXT,
    address      TEXT,
    latitude     DOUBLE PRECISION,
    longitude    DOUBLE PRECISION,
    country_code TEXT,
    timezone     TEXT,
    UNIQUE (provider_id, name)
);

CREATE TABLE IF NOT EXISTS hosts (
    id            INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    site_id       INTEGER REFERENCES sites (id),
    hostname      TEXT,
    fingerprint   TEXT,
    host_type     TEXT,
    agent_version TEXT,
    go_version    TEXT,
    build_commit  TEXT,
    kernel        TEXT,
    os_name       TEXT,
    arch          TEXT,
    cpu_model     TEXT,
    cores         INTEGER,
    threads       INTEGER,
    memory_total  BIGINT,
    -- Stored as 8 raw bytes rather than an integer: the wire value is an
    -- unsigned 64-bit hash and Postgres has no unsigned integer type.
    metadata_hash BYTEA,
    capabilities  JSONB NOT NULL DEFAULT '{}'::jsonb,
    latitude      DOUBLE PRECISION,
    longitude     DOUBLE PRECISION,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One hostname per site. Two machines at different sites may legitimately
-- share a name — that is what site_id is for — but two at the same site
-- cannot, and without this the admin API happily creates rows that are
-- indistinguishable in every view that shows a hostname.
--
-- NULLS NOT DISTINCT because site_id and hostname are both nullable, and
-- Postgres's default treats every NULL as unique: without it, hosts with no
-- site assigned could collide freely, which is the common case on a new hub
-- and exactly what this is meant to prevent. Requires PostgreSQL 15+.
--
-- A unique index rather than a table constraint: ALTER TABLE ADD CONSTRAINT
-- has no IF NOT EXISTS, and every statement in this file has to be
-- individually re-runnable — see the header.
CREATE UNIQUE INDEX IF NOT EXISTS hosts_site_id_hostname_key
    ON hosts (site_id, hostname) NULLS NOT DISTINCT;

CREATE TABLE IF NOT EXISTS tokens (
    id           INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id      INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    token_hash   BYTEA NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS host_current (
    host_id   INTEGER PRIMARY KEY REFERENCES hosts (id) ON DELETE CASCADE,
    last_seen TIMESTAMPTZ,
    cpu_total DOUBLE PRECISION,
    mem_used  BIGINT,
    mem_total BIGINT,
    uptime_s  BIGINT,

    -- The fleet's traffic figure, as a scalar rather than a series lookup.
    --
    -- "Fleet traffic -- ingress + egress" is a RATE, and read off the grid it
    -- changed meaning with the range the sparklines beside it were drawn over.
    -- Not because anything computed it wrong: the UI takes the last point of
    -- the answered series, and which quantity that point holds is decided by
    -- the range. The range picks the step (lib/range.ts), the step picks the
    -- tier (selectTier in internal/hub/read/tier.go), and the tier decides
    -- both the column and how fresh the trailing edge is:
    --
    --   1h  -> step 60s -> raw tier -> rx_bytes,     window ends at now
    --   6h  -> step 5m  -> 5m tier  -> rx_bytes_avg, window ends ~15 min ago
    --   24h -> step 5m  -> 5m tier  -> rx_bytes_avg, window ends ~15 min ago
    --
    -- So "latest sample" was true only at 1h; at 6h and 24h it was a
    -- five-minute mean that had already ended a quarter of an hour earlier. A
    -- current rate must not depend on how far back somebody is looking, so it
    -- is not read off the grid at all. host_current is where the fleet list
    -- already reads its other current gauges from, for the same reason: one
    -- row per host, no hypertable, no window.
    --
    -- Nullable with no default. A host that has not posted yet has no value,
    -- and NULL is exactly that -- the UI already renders an absent marker for
    -- it. A DEFAULT 0 would claim a silent host is moving no traffic, which is
    -- the inference every absent/zero distinction in this schema exists to
    -- avoid.
    --
    -- The sum excludes loopback and bridges because the AGENT excludes them
    -- (internal/agent/collector/network.go): lo and docker0 never reach the
    -- hub, so there is nothing to filter here.
    net_rx_bytes DOUBLE PRECISION,
    net_tx_bytes DOUBLE PRECISION,

    -- The service counts, on the same argument as the traffic pair above.
    --
    -- The host page's Units summary reads "397 units - 0 failed". It counted
    -- the rows the units endpoint returned, which worked only while that
    -- endpoint returned every unit. It no longer does: units are listed when
    -- they need attention, so on a healthy host the endpoint returns nothing
    -- and the summary read "0 units - 0 failed" on a host running 397 of them.
    --
    -- The real counts have been collected on every scrape since the beginning
    -- (collector/systemd.go sets them, store/ingest.go writes them) but only
    -- into host_samples and its rollups, which nothing reads.
    services_total  INTEGER,
    services_failed INTEGER
);

CREATE TABLE IF NOT EXISTS host_samples (
    host_id       INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts            TIMESTAMPTZ NOT NULL,
    cpu_total     DOUBLE PRECISION,
    cpu_user      DOUBLE PRECISION,
    cpu_system    DOUBLE PRECISION,
    cpu_iowait    DOUBLE PRECISION,
    cpu_steal     DOUBLE PRECISION,
    cpu_idle      DOUBLE PRECISION,
    mem_total     BIGINT,
    mem_used      BIGINT,
    mem_available BIGINT,
    mem_buffcache BIGINT,
    mem_zfs_arc   BIGINT,
    -- The parts a stacked memory chart partitions mem_total into. mem_used
    -- above is total minus available, which already contains the ARC and the
    -- unreclaimable shmem pages, so it cannot be the bottom of a stack built
    -- from the others. mem_cached is Cached MINUS Shmem, which is what keeps
    -- mem_buffcache = mem_buffers + mem_cached + mem_shared true.
    mem_free         BIGINT,
    mem_buffers      BIGINT,
    mem_cached       BIGINT,
    mem_shared       BIGINT,
    mem_sreclaimable BIGINT,
    swap_total    BIGINT,
    swap_used     BIGINT,
    load1         DOUBLE PRECISION,
    load5         DOUBLE PRECISION,
    load15        DOUBLE PRECISION,
    uptime_s      BIGINT,

    -- /proc/stat, the lines the CPU collector steps over. The three *_per_s
    -- columns are rates the agent computed: the underlying counters reset on
    -- reboot, and only the agent holds the previous value needed to notice.
    ctxt_per_s      DOUBLE PRECISION,
    intr_per_s      DOUBLE PRECISION,
    forks_per_s     DOUBLE PRECISION,
    procs_running   INTEGER,
    procs_blocked   INTEGER,
    -- Absolute unix timestamp, not derived from uptime_s + ts: that
    -- derivation drifts a second or two per sample, so every row of one boot
    -- would disagree about when the boot was.
    boot_time_s     BIGINT,

    processes_total INTEGER,
    users_logged_in INTEGER,

    -- systemd summary (spec 5.3), from the Systemd collector. NULL on a host
    -- with no systemd: no units is not the same fact as zero failed units.
    services_total  INTEGER,
    services_failed INTEGER,

    -- /proc/vmstat: what the machine had to DO to keep memory available.
    pgmajfault_per_s DOUBLE PRECISION,
    pswpin_per_s     DOUBLE PRECISION,
    pswpout_per_s    DOUBLE PRECISION,
    -- Cumulative, not a rate: one kill per interval is a discrete event.
    oom_kill_total   BIGINT,

    -- Exhaustion gauges, each with its ceiling beside it. Running out of
    -- these presents as a broken network rather than a resource problem.
    sockets_used     INTEGER,
    tcp_orphan       INTEGER,
    tcp_tw           INTEGER,
    tcp_alloc        INTEGER,
    fd_used          BIGINT,
    fd_limit         BIGINT,
    -- NULL when the conntrack module is not loaded, which is normal.
    conntrack_count  INTEGER,
    conntrack_limit  INTEGER,
    -- The ceilings the socket gauges are read against, so each has something
    -- to be compared to.
    tcp_tw_limit     INTEGER,
    tcp_orphan_limit INTEGER,

    -- /proc/net/snmp Tcp:. There is no tcp6_* mirror because the kernel keeps
    -- one family-agnostic TCP MIB -- these already count IPv6 connections --
    -- and /proc/net/snmp6 has no Tcp6 block at all. Only UDP and IP
    -- fragmentation are accounted per family, and only those are mirrored.
    tcp_retrans_segs_per_s     DOUBLE PRECISION,
    tcp_out_rsts_per_s         DOUBLE PRECISION,
    tcp_in_errs_per_s          DOUBLE PRECISION,
    tcp_active_opens_per_s     DOUBLE PRECISION,
    tcp_passive_opens_per_s    DOUBLE PRECISION,
    tcp_attempt_fails_per_s    DOUBLE PRECISION,
    tcp_curr_estab             INTEGER,
    -- /proc/net/netstat TcpExt:
    tcp_listen_overflows_per_s DOUBLE PRECISION,
    tcp_listen_drops_per_s     DOUBLE PRECISION,

    -- /proc/net/snmp Udp: and Ip:
    udp_in_errors_per_s     DOUBLE PRECISION,
    udp_rcvbuf_errors_per_s DOUBLE PRECISION,
    udp_sndbuf_errors_per_s DOUBLE PRECISION,
    udp_no_ports_per_s      DOUBLE PRECISION,
    ip_reasm_reqds_per_s    DOUBLE PRECISION,
    ip_reasm_fails_per_s    DOUBLE PRECISION,
    ip_frag_fails_per_s     DOUBLE PRECISION,
    ip_frag_creates_per_s   DOUBLE PRECISION,

    -- /proc/net/snmp6
    udp6_in_errors_per_s     DOUBLE PRECISION,
    udp6_rcvbuf_errors_per_s DOUBLE PRECISION,
    udp6_sndbuf_errors_per_s DOUBLE PRECISION,
    udp6_no_ports_per_s      DOUBLE PRECISION,
    ip6_reasm_reqds_per_s    DOUBLE PRECISION,
    ip6_reasm_fails_per_s    DOUBLE PRECISION,
    ip6_frag_fails_per_s     DOUBLE PRECISION,
    ip6_frag_creates_per_s   DOUBLE PRECISION,

    -- Natural key. Replayed batches collide here and are discarded by
    -- ON CONFLICT DO NOTHING, which is what makes at-least-once safe.
    PRIMARY KEY (host_id, ts)
);

SELECT create_hypertable('host_samples', by_range('ts'), if_not_exists => TRUE);

-- drop_chunks removes a chunk only once its NEWEST row is past the cutoff, so
-- the retention policy further down keeps data for up to
-- retention + chunk_interval.
-- Timescale's default is a 7-day chunk, which turns "7 days" into as much as
-- 14. One-day chunks bound the overshoot to a day.
SELECT set_chunk_time_interval('host_samples', INTERVAL '1 day');

CREATE MATERIALIZED VIEW IF NOT EXISTS host_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(cpu_total)  AS cpu_total_avg,
       max(cpu_total)  AS cpu_total_max,
       avg(mem_used)   AS mem_used_avg,
       max(mem_used)   AS mem_used_max,
       avg(swap_used)  AS swap_used_avg,
       -- The stacked CPU and memory charts, at every range rather than only
       -- at 1h. The fleet's 6h and 24h ranges answer from this tier and 7d
       -- and 30d from the 1h one, so a breakdown that lives only in the raw
       -- table is a breakdown almost nobody ever sees.
       --
       -- avg and never max, deliberately: the memory chart derives "used" as
       -- mem_total - free - buffers - cached - shared - arc, and that
       -- subtraction is only valid if every input is the same aggregate.
       -- Averaging is linear, so the remainder of the averages is the average
       -- of the remainders; mixing a max into it would silently break the
       -- partition. The same rule governs cpu_core's busy_avg.
       avg(cpu_user)   AS cpu_user_avg,
       avg(cpu_system) AS cpu_system_avg,
       avg(cpu_iowait) AS cpu_iowait_avg,
       avg(cpu_steal)  AS cpu_steal_avg,
       avg(mem_total)        AS mem_total_avg,
       avg(mem_free)         AS mem_free_avg,
       avg(mem_buffers)      AS mem_buffers_avg,
       avg(mem_cached)       AS mem_cached_avg,
       avg(mem_shared)       AS mem_shared_avg,
       avg(mem_sreclaimable) AS mem_sreclaimable_avg,
       avg(mem_zfs_arc)      AS mem_zfs_arc_avg,
       avg(load1)      AS load1_avg,
       max(load1)      AS load1_max,
       last(uptime_s, ts) AS uptime_s,
       -- Rates carry a max alongside the average: a five-minute mean flattens
       -- exactly the interrupt storm or retransmit burst these exist to show.
       avg(ctxt_per_s) AS ctxt_per_s_avg,
       max(ctxt_per_s) AS ctxt_per_s_max,
       avg(intr_per_s) AS intr_per_s_avg,
       max(intr_per_s) AS intr_per_s_max,
       avg(forks_per_s) AS forks_per_s_avg,
       max(forks_per_s) AS forks_per_s_max,
       avg(procs_running) AS procs_running_avg,
       max(procs_running) AS procs_running_max,
       avg(procs_blocked) AS procs_blocked_avg,
       max(procs_blocked) AS procs_blocked_max,
       -- Facts about a moment, not quantities to average: the last reading in
       -- the bucket is the answer, exactly as for uptime_s.
       last(boot_time_s, ts) AS boot_time_s,
       last(processes_total, ts) AS processes_total,
       last(users_logged_in, ts) AS users_logged_in,
       last(services_total, ts) AS services_total,
       last(services_failed, ts) AS services_failed,
       avg(tcp_retrans_segs_per_s) AS tcp_retrans_segs_per_s_avg,
       max(tcp_retrans_segs_per_s) AS tcp_retrans_segs_per_s_max,
       avg(tcp_out_rsts_per_s) AS tcp_out_rsts_per_s_avg,
       max(tcp_out_rsts_per_s) AS tcp_out_rsts_per_s_max,
       avg(tcp_in_errs_per_s) AS tcp_in_errs_per_s_avg,
       max(tcp_in_errs_per_s) AS tcp_in_errs_per_s_max,
       avg(tcp_active_opens_per_s) AS tcp_active_opens_per_s_avg,
       avg(tcp_passive_opens_per_s) AS tcp_passive_opens_per_s_avg,
       avg(tcp_attempt_fails_per_s) AS tcp_attempt_fails_per_s_avg,
       max(tcp_attempt_fails_per_s) AS tcp_attempt_fails_per_s_max,
       avg(tcp_curr_estab) AS tcp_curr_estab_avg,
       max(tcp_curr_estab) AS tcp_curr_estab_max,
       avg(tcp_listen_overflows_per_s) AS tcp_listen_overflows_per_s_avg,
       max(tcp_listen_overflows_per_s) AS tcp_listen_overflows_per_s_max,
       avg(tcp_listen_drops_per_s) AS tcp_listen_drops_per_s_avg,
       max(tcp_listen_drops_per_s) AS tcp_listen_drops_per_s_max,
       avg(udp_in_errors_per_s) AS udp_in_errors_per_s_avg,
       max(udp_in_errors_per_s) AS udp_in_errors_per_s_max,
       avg(udp_rcvbuf_errors_per_s) AS udp_rcvbuf_errors_per_s_avg,
       max(udp_rcvbuf_errors_per_s) AS udp_rcvbuf_errors_per_s_max,
       avg(udp_sndbuf_errors_per_s) AS udp_sndbuf_errors_per_s_avg,
       max(udp_sndbuf_errors_per_s) AS udp_sndbuf_errors_per_s_max,
       avg(udp_no_ports_per_s) AS udp_no_ports_per_s_avg,
       max(udp_no_ports_per_s) AS udp_no_ports_per_s_max,
       avg(ip_reasm_reqds_per_s) AS ip_reasm_reqds_per_s_avg,
       avg(ip_reasm_fails_per_s) AS ip_reasm_fails_per_s_avg,
       max(ip_reasm_fails_per_s) AS ip_reasm_fails_per_s_max,
       avg(ip_frag_fails_per_s) AS ip_frag_fails_per_s_avg,
       max(ip_frag_fails_per_s) AS ip_frag_fails_per_s_max,
       avg(ip_frag_creates_per_s) AS ip_frag_creates_per_s_avg,
       avg(udp6_in_errors_per_s) AS udp6_in_errors_per_s_avg,
       max(udp6_in_errors_per_s) AS udp6_in_errors_per_s_max,
       avg(udp6_rcvbuf_errors_per_s) AS udp6_rcvbuf_errors_per_s_avg,
       max(udp6_rcvbuf_errors_per_s) AS udp6_rcvbuf_errors_per_s_max,
       avg(udp6_sndbuf_errors_per_s) AS udp6_sndbuf_errors_per_s_avg,
       max(udp6_sndbuf_errors_per_s) AS udp6_sndbuf_errors_per_s_max,
       avg(udp6_no_ports_per_s) AS udp6_no_ports_per_s_avg,
       max(udp6_no_ports_per_s) AS udp6_no_ports_per_s_max,
       avg(ip6_reasm_reqds_per_s) AS ip6_reasm_reqds_per_s_avg,
       avg(ip6_reasm_fails_per_s) AS ip6_reasm_fails_per_s_avg,
       max(ip6_reasm_fails_per_s) AS ip6_reasm_fails_per_s_max,
       avg(ip6_frag_fails_per_s) AS ip6_frag_fails_per_s_avg,
       max(ip6_frag_fails_per_s) AS ip6_frag_fails_per_s_max,
       avg(ip6_frag_creates_per_s) AS ip6_frag_creates_per_s_avg,
       -- Pressure rates carry a max: a five-minute mean flattens exactly the
       -- thrashing burst these exist to show.
       avg(pgmajfault_per_s) AS pgmajfault_per_s_avg,
       max(pgmajfault_per_s) AS pgmajfault_per_s_max,
       avg(pswpin_per_s) AS pswpin_per_s_avg,
       max(pswpin_per_s) AS pswpin_per_s_max,
       avg(pswpout_per_s) AS pswpout_per_s_avg,
       max(pswpout_per_s) AS pswpout_per_s_max,
       -- Monotonic: the last reading in the bucket is the running total.
       last(oom_kill_total, ts) AS oom_kill_total,
       -- Exhaustion gauges keep a max, which is the whole question: the peak
       -- is what approached the ceiling, and an average hides it.
       avg(sockets_used) AS sockets_used_avg,
       max(sockets_used) AS sockets_used_max,
       avg(tcp_orphan) AS tcp_orphan_avg,
       max(tcp_orphan) AS tcp_orphan_max,
       avg(tcp_tw) AS tcp_tw_avg,
       max(tcp_tw) AS tcp_tw_max,
       avg(tcp_alloc) AS tcp_alloc_avg,
       max(tcp_alloc) AS tcp_alloc_max,
       avg(fd_used) AS fd_used_avg,
       max(fd_used) AS fd_used_max,
       last(fd_limit, ts) AS fd_limit,
       avg(conntrack_count) AS conntrack_count_avg,
       max(conntrack_count) AS conntrack_count_max,
       last(conntrack_limit, ts) AS conntrack_limit,
       last(tcp_tw_limit, ts) AS tcp_tw_limit,
       last(tcp_orphan_limit, ts) AS tcp_orphan_limit
  FROM host_samples
 GROUP BY host_id, bucket
WITH NO DATA;

-- Same overshoot, worse: Timescale sizes a continuous aggregate's chunks at
-- 10x the raw interval -- 70 days here -- so a 30-day retention was really
-- retaining ~100. Sized to roughly a fifteenth of each tier's retention.

SELECT set_chunk_time_interval('host_samples_5m', INTERVAL '2 days');

CREATE MATERIALIZED VIEW IF NOT EXISTS host_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(cpu_total_avg) AS cpu_total_avg,
       max(cpu_total_max) AS cpu_total_max,
       avg(mem_used_avg)  AS mem_used_avg,
       max(mem_used_max)  AS mem_used_max,
       avg(swap_used_avg) AS swap_used_avg,
       -- The band columns, rolled up from the 5m tier like everything else
       -- here. Same avg-only rule as there: the memory stack's "used" is a
       -- remainder, and it is only valid if every input is the same
       -- aggregate.
       avg(cpu_user_avg)   AS cpu_user_avg,
       avg(cpu_system_avg) AS cpu_system_avg,
       avg(cpu_iowait_avg) AS cpu_iowait_avg,
       avg(cpu_steal_avg)  AS cpu_steal_avg,
       avg(mem_total_avg)        AS mem_total_avg,
       avg(mem_free_avg)         AS mem_free_avg,
       avg(mem_buffers_avg)      AS mem_buffers_avg,
       avg(mem_cached_avg)       AS mem_cached_avg,
       avg(mem_shared_avg)       AS mem_shared_avg,
       avg(mem_sreclaimable_avg) AS mem_sreclaimable_avg,
       avg(mem_zfs_arc_avg)      AS mem_zfs_arc_avg,
       avg(load1_avg)     AS load1_avg,
       max(load1_max)     AS load1_max,
       last(uptime_s, bucket) AS uptime_s,
       -- Rolls up the 5m tier, never the raw table: averaging an average of
       -- equal-width buckets is the average, and max-of-max is the max.
       avg(ctxt_per_s_avg) AS ctxt_per_s_avg,
       max(ctxt_per_s_max) AS ctxt_per_s_max,
       avg(intr_per_s_avg) AS intr_per_s_avg,
       max(intr_per_s_max) AS intr_per_s_max,
       avg(forks_per_s_avg) AS forks_per_s_avg,
       max(forks_per_s_max) AS forks_per_s_max,
       avg(procs_running_avg) AS procs_running_avg,
       max(procs_running_max) AS procs_running_max,
       avg(procs_blocked_avg) AS procs_blocked_avg,
       max(procs_blocked_max) AS procs_blocked_max,
       last(boot_time_s, bucket) AS boot_time_s,
       last(processes_total, bucket) AS processes_total,
       last(users_logged_in, bucket) AS users_logged_in,
       last(services_total, bucket) AS services_total,
       last(services_failed, bucket) AS services_failed,
       avg(tcp_retrans_segs_per_s_avg) AS tcp_retrans_segs_per_s_avg,
       max(tcp_retrans_segs_per_s_max) AS tcp_retrans_segs_per_s_max,
       avg(tcp_out_rsts_per_s_avg) AS tcp_out_rsts_per_s_avg,
       max(tcp_out_rsts_per_s_max) AS tcp_out_rsts_per_s_max,
       avg(tcp_in_errs_per_s_avg) AS tcp_in_errs_per_s_avg,
       max(tcp_in_errs_per_s_max) AS tcp_in_errs_per_s_max,
       avg(tcp_active_opens_per_s_avg) AS tcp_active_opens_per_s_avg,
       avg(tcp_passive_opens_per_s_avg) AS tcp_passive_opens_per_s_avg,
       avg(tcp_attempt_fails_per_s_avg) AS tcp_attempt_fails_per_s_avg,
       max(tcp_attempt_fails_per_s_max) AS tcp_attempt_fails_per_s_max,
       avg(tcp_curr_estab_avg) AS tcp_curr_estab_avg,
       max(tcp_curr_estab_max) AS tcp_curr_estab_max,
       avg(tcp_listen_overflows_per_s_avg) AS tcp_listen_overflows_per_s_avg,
       max(tcp_listen_overflows_per_s_max) AS tcp_listen_overflows_per_s_max,
       avg(tcp_listen_drops_per_s_avg) AS tcp_listen_drops_per_s_avg,
       max(tcp_listen_drops_per_s_max) AS tcp_listen_drops_per_s_max,
       avg(udp_in_errors_per_s_avg) AS udp_in_errors_per_s_avg,
       max(udp_in_errors_per_s_max) AS udp_in_errors_per_s_max,
       avg(udp_rcvbuf_errors_per_s_avg) AS udp_rcvbuf_errors_per_s_avg,
       max(udp_rcvbuf_errors_per_s_max) AS udp_rcvbuf_errors_per_s_max,
       avg(udp_sndbuf_errors_per_s_avg) AS udp_sndbuf_errors_per_s_avg,
       max(udp_sndbuf_errors_per_s_max) AS udp_sndbuf_errors_per_s_max,
       avg(udp_no_ports_per_s_avg) AS udp_no_ports_per_s_avg,
       max(udp_no_ports_per_s_max) AS udp_no_ports_per_s_max,
       avg(ip_reasm_reqds_per_s_avg) AS ip_reasm_reqds_per_s_avg,
       avg(ip_reasm_fails_per_s_avg) AS ip_reasm_fails_per_s_avg,
       max(ip_reasm_fails_per_s_max) AS ip_reasm_fails_per_s_max,
       avg(ip_frag_fails_per_s_avg) AS ip_frag_fails_per_s_avg,
       max(ip_frag_fails_per_s_max) AS ip_frag_fails_per_s_max,
       avg(ip_frag_creates_per_s_avg) AS ip_frag_creates_per_s_avg,
       avg(udp6_in_errors_per_s_avg) AS udp6_in_errors_per_s_avg,
       max(udp6_in_errors_per_s_max) AS udp6_in_errors_per_s_max,
       avg(udp6_rcvbuf_errors_per_s_avg) AS udp6_rcvbuf_errors_per_s_avg,
       max(udp6_rcvbuf_errors_per_s_max) AS udp6_rcvbuf_errors_per_s_max,
       avg(udp6_sndbuf_errors_per_s_avg) AS udp6_sndbuf_errors_per_s_avg,
       max(udp6_sndbuf_errors_per_s_max) AS udp6_sndbuf_errors_per_s_max,
       avg(udp6_no_ports_per_s_avg) AS udp6_no_ports_per_s_avg,
       max(udp6_no_ports_per_s_max) AS udp6_no_ports_per_s_max,
       avg(ip6_reasm_reqds_per_s_avg) AS ip6_reasm_reqds_per_s_avg,
       avg(ip6_reasm_fails_per_s_avg) AS ip6_reasm_fails_per_s_avg,
       max(ip6_reasm_fails_per_s_max) AS ip6_reasm_fails_per_s_max,
       avg(ip6_frag_fails_per_s_avg) AS ip6_frag_fails_per_s_avg,
       max(ip6_frag_fails_per_s_max) AS ip6_frag_fails_per_s_max,
       avg(ip6_frag_creates_per_s_avg) AS ip6_frag_creates_per_s_avg,
       -- Rolled up FROM the 5m tier, so each aggregate composes with its own
       -- kind: avg of avgs, max of maxes, last of lasts. Taking max(x_avg)
       -- here would report the busiest five minutes as if it were an
       -- instantaneous peak.
       avg(pgmajfault_per_s_avg) AS pgmajfault_per_s_avg,
       max(pgmajfault_per_s_max) AS pgmajfault_per_s_max,
       avg(pswpin_per_s_avg) AS pswpin_per_s_avg,
       max(pswpin_per_s_max) AS pswpin_per_s_max,
       avg(pswpout_per_s_avg) AS pswpout_per_s_avg,
       max(pswpout_per_s_max) AS pswpout_per_s_max,
       last(oom_kill_total, bucket) AS oom_kill_total,
       avg(sockets_used_avg) AS sockets_used_avg,
       max(sockets_used_max) AS sockets_used_max,
       avg(tcp_orphan_avg) AS tcp_orphan_avg,
       max(tcp_orphan_max) AS tcp_orphan_max,
       avg(tcp_tw_avg) AS tcp_tw_avg,
       max(tcp_tw_max) AS tcp_tw_max,
       avg(tcp_alloc_avg) AS tcp_alloc_avg,
       max(tcp_alloc_max) AS tcp_alloc_max,
       avg(fd_used_avg) AS fd_used_avg,
       max(fd_used_max) AS fd_used_max,
       last(fd_limit, bucket) AS fd_limit,
       avg(conntrack_count_avg) AS conntrack_count_avg,
       max(conntrack_count_max) AS conntrack_count_max,
       last(conntrack_limit, bucket) AS conntrack_limit,
       last(tcp_tw_limit, bucket) AS tcp_tw_limit,
       last(tcp_orphan_limit, bucket) AS tcp_orphan_limit
  FROM host_samples_5m
 GROUP BY host_id, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('host_samples_1h', INTERVAL '7 days');

-- start_offset (6h) must stay above the agent ring-buffer window (1h).
-- Timescale cuts invalidations against the refresh window, so anything
-- backfilled older than start_offset is never re-materialised.
SELECT add_continuous_aggregate_policy('host_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('host_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

-- Raw retention must exceed the refresh lag, or chunks are dropped before
-- being materialised into the 5m tier.
SELECT add_retention_policy('host_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('host_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('host_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);

-- The agent's telemetry about itself.
--
-- A separate table rather than more columns on host_samples, because
-- agent_samples.uptime_s and host_samples.uptime_s are different facts: agent
-- uptime resetting while host uptime keeps climbing means the agent restarted
-- on its own and lost its ring buffer. Merged into one row, a crash-looping
-- agent would be indistinguishable from a healthy one.
--
-- Same (host_id, ts) natural key as host_samples, so a replayed batch dedupes
-- identically.
CREATE TABLE IF NOT EXISTS agent_samples (
    host_id              INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts                   TIMESTAMPTZ NOT NULL,
    uptime_s             BIGINT,
    rss_bytes            BIGINT,
    goroutines           INTEGER,
    scrape_duration_ms   INTEGER,
    buffer_depth         INTEGER,
    buffer_dropped_total BIGINT,
    post_failures_total  BIGINT,
    -- NULL for every scrape taken while the hub was unreachable. That is the
    -- measurement, not a gap: an outage has no round-trip time.
    post_latency_ms      INTEGER,
    -- The network path to the hub, as distinct from the round trip above.
    -- post_latency_ms includes TLS, the hub's handling and the Postgres
    -- write, so a slow database inflates it exactly like a slow network
    -- does; these stop at SYN-ACK. Both NULL when no handshake completed --
    -- hub_connect_failures_total is what records the outage.
    hub_connect_us       INTEGER,
    hub_connect_max_us   INTEGER,
    hub_connect_failures_total BIGINT,
    PRIMARY KEY (host_id, ts)
);

SELECT create_hypertable('agent_samples', by_range('ts'), if_not_exists => TRUE);
SELECT set_chunk_time_interval('agent_samples', INTERVAL '1 day');

CREATE MATERIALIZED VIEW IF NOT EXISTS agent_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(scrape_duration_ms) AS scrape_duration_ms_avg,
       max(scrape_duration_ms) AS scrape_duration_ms_max,
       avg(post_latency_ms)    AS post_latency_ms_avg,
       max(post_latency_ms)    AS post_latency_ms_max,
       avg(buffer_depth)       AS buffer_depth_avg,
       max(buffer_depth)       AS buffer_depth_max,
       -- A monotonic total, so the last reading in the bucket is the answer;
       -- averaging it would understate what was actually dropped.
       max(buffer_dropped_total) AS buffer_dropped_total,
       max(post_failures_total)  AS post_failures_total,
       avg(hub_connect_us)     AS hub_connect_us_avg,
       -- The peak of the per-scrape MINIMA. Not a second-order minimum: the
       -- agent already took the best of three handshakes, so this bucket's
       -- max is the worst the path looked at its best within it.
       max(hub_connect_us)     AS hub_connect_us_max,
       avg(hub_connect_max_us) AS hub_connect_max_us_avg,
       max(hub_connect_max_us) AS hub_connect_max_us_max,
       max(hub_connect_failures_total) AS hub_connect_failures_total
  FROM agent_samples
 GROUP BY host_id, bucket
WITH NO DATA;

SELECT set_chunk_time_interval('agent_samples_5m', INTERVAL '2 days');

CREATE MATERIALIZED VIEW IF NOT EXISTS agent_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(scrape_duration_ms_avg) AS scrape_duration_ms_avg,
       max(scrape_duration_ms_max) AS scrape_duration_ms_max,
       avg(post_latency_ms_avg)    AS post_latency_ms_avg,
       max(post_latency_ms_max)    AS post_latency_ms_max,
       avg(buffer_depth_avg)       AS buffer_depth_avg,
       max(buffer_depth_max)       AS buffer_depth_max,
       max(buffer_dropped_total)   AS buffer_dropped_total,
       max(post_failures_total)    AS post_failures_total,
       avg(hub_connect_us_avg)     AS hub_connect_us_avg,
       max(hub_connect_us_max)     AS hub_connect_us_max,
       avg(hub_connect_max_us_avg) AS hub_connect_max_us_avg,
       max(hub_connect_max_us_max) AS hub_connect_max_us_max,
       max(hub_connect_failures_total) AS hub_connect_failures_total
  FROM agent_samples_5m
 GROUP BY host_id, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('agent_samples_1h', INTERVAL '7 days');

-- Identical start_offsets to host_samples, and for the identical reason: the
-- agent replays this table's rows from the same ring buffer.
SELECT add_continuous_aggregate_policy('agent_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('agent_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

SELECT add_retention_policy('agent_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('agent_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('agent_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);

-- ---------------------------------------------------------------------------
-- Group 1 collectors: per-core CPU, disk I/O, sensors, network, and the
-- per-collector telemetry every collector emits. Each hypertable below ships
-- with both continuous aggregates and all three retention policies in the
-- same block, so no tier is ever half-configured.
--
-- All five carry a dimension alongside (host_id, ts) and every one of them
-- has that dimension in its PRIMARY KEY. This is load-bearing: ingest
-- deduplicates replayed batches with ON CONFLICT DO NOTHING (spec 5.5), so
-- a key of (host_id, ts) alone would silently keep one row per scrape and
-- discard the other fifteen cores without raising anything.
-- ---------------------------------------------------------------------------

-- Sensor dimension. The natural key is chip + label, never hwmonN: the hwmon
-- index is assigned in probe order and moves between boots, so keying on it
-- forks a sensor's history every time the kernel enumerates differently.
CREATE TABLE IF NOT EXISTS sensors (
    id      INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    chip    TEXT NOT NULL,
    label   TEXT NOT NULL,
    -- What the sensor measures: temperature, fan, voltage, current, power.
    -- Charting a 1200 RPM fan on the same axis as a 45 degree package is the
    -- mistake this exists to prevent. Defaulted rather than NOT NULL: an
    -- agent predating the field sends nothing, and temperature is the only
    -- kind such an agent could have meant.
    kind    TEXT NOT NULL DEFAULT 'temperature'
);

-- A unique index rather than a table constraint, for the reason given in the
-- header: ALTER TABLE ADD CONSTRAINT has no IF NOT EXISTS.
CREATE UNIQUE INDEX IF NOT EXISTS sensors_host_id_chip_label_key
    ON sensors (host_id, chip, label);

-- Redundant on its own -- id is already the primary key -- but it is what
-- lets sensor_samples declare a composite foreign key on (sensor_id,
-- host_id). Without that, a sample row can carry host B's host_id and host
-- A's sensor_id, and the join then attributes A's chip and label to B's
-- sample with nothing raised.
CREATE UNIQUE INDEX IF NOT EXISTS sensors_id_host_id_key
    ON sensors (id, host_id);

-- The general discrete-state table (spec 5.2): mdraid degradation, SMART
-- threshold crossings, public IP changes, agent version changes.
--
-- Deliberately NOT a hypertable. Spec 5.1 rule 4 sends anything that is
-- constant for hours and matters at the moment it changes here rather than to
-- a sample table -- an array is "clean" for weeks, and a 60s series saying so
-- is the same near-constant-series waste that keeps systemd out of 5.3.
-- This is also why mdraid appears in 5.2's list and in no 5.3 row: it has
-- no hypertable by design, not by omission.
CREATE TABLE IF NOT EXISTS events (
    id      INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts      TIMESTAMPTZ NOT NULL,
    type    TEXT NOT NULL,
    -- The thing the event is about: an array name, a device, an interface.
    -- NULL for events about the host as a whole, such as an agent upgrade.
    subject TEXT,
    detail  JSONB NOT NULL DEFAULT '{}'::jsonb
);

-- Natural key, so a ring buffer replayed after an outage re-delivers the same
-- degradation event harmlessly. NULLS NOT DISTINCT because subject is
-- nullable and Postgres's default would treat every subjectless event as
-- unique, which is exactly the row that gets replayed.
CREATE UNIQUE INDEX IF NOT EXISTS events_host_id_ts_type_subject_key
    ON events (host_id, ts, type, subject) NULLS NOT DISTINCT;

-- "What happened on this host recently", the only way this table is read.
CREATE INDEX IF NOT EXISTS events_host_id_ts_idx ON events (host_id, ts DESC);

-- ------------------------------------------------- dimensions for Groups 2-4

-- Every dimension below follows the shape sensors established: an identity
-- surrogate id that hypertables reference, a natural key unique WITHIN a host
-- (two hosts both having an "sda" is normal), and an (id, host_id) index so a
-- sample table can declare a composite foreign key and never attribute one
-- host's entity to another's row.

-- container_key is the natural key: compose project + service, falling back to
-- the container name (spec 6.2). Deliberately NOT the Docker id, which changes
-- on every recreate -- keying on it would restart the history of a service
-- that merely got a new image.
CREATE TABLE IF NOT EXISTS containers (
    id            INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id       INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    container_key TEXT NOT NULL,
    name          TEXT,
    image         TEXT,
    is_agent      BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE UNIQUE INDEX IF NOT EXISTS containers_host_id_container_key_key
    ON containers (host_id, container_key);
CREATE UNIQUE INDEX IF NOT EXISTS containers_id_host_id_key
    ON containers (id, host_id);

CREATE TABLE IF NOT EXISTS filesystems (
    id         INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id    INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    label      TEXT NOT NULL,
    mountpoint TEXT,
    -- st_dev of the mountpoint. Bind mounts of one filesystem share it, which
    -- is how the collector dedups them -- counting both would overstate the
    -- host's disk usage by however many bind mounts it happens to have.
    device_id  BIGINT
);

CREATE UNIQUE INDEX IF NOT EXISTS filesystems_host_id_label_key
    ON filesystems (host_id, label);
CREATE UNIQUE INDEX IF NOT EXISTS filesystems_id_host_id_key
    ON filesystems (id, host_id);

CREATE TABLE IF NOT EXISTS devices (
    id      INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    device  TEXT NOT NULL,
    model   TEXT,
    serial  TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS devices_host_id_device_key
    ON devices (host_id, device);
CREATE UNIQUE INDEX IF NOT EXISTS devices_id_host_id_key
    ON devices (id, host_id);

-- A unit's current state is a COLUMN here, not "whatever the newest event
-- said".
--
-- systemd_unit_events was doing two jobs. It is a log -- a unit went failed at
-- 14:02, recovered at 14:31 -- and it was also the only place the CURRENT state
-- lived, read back through a LEFT JOIN LATERAL. Those two jobs disagree, and
-- the disagreement is why "exim4.service failed" could never clear itself:
--
--   * A log has no way to say "nothing changed, and here is the truth anyway".
--     The agent emits an event only on a transition, so if the transition that
--     would have fixed the hub's view is never sent, the last event stands
--     forever. Three routine things suppress it: the unit recovered while the
--     agent was down (the restart baseline reports only FAILED units), the unit
--     vanished from the bus entirely (`apt purge`), or the scrape carrying the
--     recovery was dropped by the agent's ring buffer.
--
--   * The log is PRUNED. netra_prune_discrete_events deletes events past 90
--     days, so a unit failed and untouched for longer had its only event
--     deleted and its state silently became NULL -- the hub forgetting a live
--     problem and calling it resolved.
--
-- With state on the row, the agent sends a periodic snapshot that states what
-- IS rather than what changed, and a divergence cannot outlive it. The events
-- table goes back to being only a log, and pruning it is safe again.
--
-- Nullable with no default, for the reason host_current's traffic columns give:
-- a unit with no known state has none, and NULL is exactly that. read.Units
-- renders an absent marker for it.
CREATE TABLE IF NOT EXISTS systemd_units (
    id        INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id   INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    unit_name TEXT NOT NULL,
    state     TEXT,
    substate  TEXT,
    state_ts  TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS systemd_units_host_id_unit_name_key
    ON systemd_units (host_id, unit_name);
CREATE UNIQUE INDEX IF NOT EXISTS systemd_units_id_host_id_key
    ON systemd_units (id, host_id);

-- address is INET rather than TEXT so subnet queries work -- "every host with
-- an address in 172.19.0.0/16", "every host with a public IPv4". As text those
-- are string matches and get the answer wrong.
--
-- scope is derived BY THE HUB from the address (spec 5.2). The agent reports
-- raw facts only, so loopback/private/public classification is one
-- implementation that can be corrected without redeploying every agent.
-- IPv4 and IPv6 are treated identically throughout.
CREATE TABLE IF NOT EXISTS host_addresses (
    host_id     INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    iface       TEXT NOT NULL,
    if_index    INTEGER,
    address     INET NOT NULL,
    family      SMALLINT NOT NULL,
    scope       TEXT,
    vrf         TEXT,
    description TEXT,
    first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (host_id, iface, address)
);

-- arch is in the key because a host can legitimately carry the same package
-- for two architectures (amd64 and i386 on a multiarch Debian), and they are
-- different installations with their own versions.
CREATE TABLE IF NOT EXISTS host_packages (
    host_id    INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    version    TEXT NOT NULL,
    arch       TEXT NOT NULL DEFAULT '',
    format     TEXT NOT NULL,
    size_bytes BIGINT,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (host_id, name, arch)
);

-- systemd_unit_events and package_events are separate from `events` because
-- both carry structured, queryable columns that would otherwise be buried in
-- jsonb, and both arrive in bursts large enough to want their own indexes
-- (spec 5.2).
--
-- Plain Postgres tables, NOT hypertables: a unit changes state a handful of
-- times a month. They must not move the counts in rollup_test.go.
CREATE TABLE IF NOT EXISTS systemd_unit_events (
    host_id  INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    unit_id  INTEGER NOT NULL,
    ts       TIMESTAMPTZ NOT NULL,
    state    TEXT NOT NULL,
    substate TEXT,
    PRIMARY KEY (host_id, unit_id, ts),
    FOREIGN KEY (unit_id, host_id) REFERENCES systemd_units (id, host_id) ON DELETE CASCADE
);

-- The composite foreign key's cascade path needs its own index, for the same
-- reason sensor_samples does: deleting a host cascades to systemd_units first,
-- and each deleted unit then searches for its events. The primary key leads
-- with host_id, so without this that search is a sequential scan per unit.
CREATE INDEX IF NOT EXISTS systemd_unit_events_unit_id_host_id_idx
    ON systemd_unit_events (unit_id, host_id);

CREATE TABLE IF NOT EXISTS package_events (
    host_id      INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts           TIMESTAMPTZ NOT NULL,
    name         TEXT NOT NULL,
    action       TEXT NOT NULL,
    from_version TEXT,
    to_version   TEXT,
    PRIMARY KEY (host_id, ts, name)
);

-- "What changed on this host recently", the only way this table is read.
CREATE INDEX IF NOT EXISTS package_events_host_id_ts_idx
    ON package_events (host_id, ts DESC);

-- ------------------------------------------ retention for the event tables

-- events, systemd_unit_events and package_events are the three tables in this
-- file with no retention at all, and they are plain Postgres tables rather
-- than hypertables, so add_retention_policy cannot be pointed at them.
--
-- That was sized on "a unit changes state a handful of times a month", which
-- held until the agent began emitting a failed-unit baseline on restart: the
-- baseline is bounded, but a crash-looping agent re-emits it every restart
-- into a table nothing prunes. events has the identical problem for the same
-- reason -- an mdraid array that flaps writes a row per transition -- and is
-- included here rather than left as the next surprise.
--
-- PRUNED, NOT CONVERTED. Turning them into hypertables would buy chunk drops
-- these row volumes do not need, and would cost the composite foreign keys
-- systemd_unit_events uses to keep one host's units off another host's
-- events. A DELETE on an indexed ts is the cheaper answer at this size.
--
-- 90 days matches the 1h tier: an event is the thing a metric chart is read
-- ALONGSIDE, and an event log that expired before the series it explains
-- would leave a spike with no cause.
CREATE OR REPLACE PROCEDURE netra_prune_discrete_events(job_id INTEGER, config JSONB)
LANGUAGE plpgsql AS $$
DECLARE
    -- Read from the job's config rather than hardcoded, so the horizon can be
    -- changed with alter_job on a running hub instead of a schema edit.
    horizon INTERVAL := coalesce((config ->> 'retention')::INTERVAL, INTERVAL '90 days');
    cutoff  TIMESTAMPTZ := now() - horizon;
BEGIN
    -- Separate statements, in dependency order, each committing on its own:
    -- one transaction spanning all three would hold row locks on every event
    -- table for the length of the slowest delete.
    DELETE FROM systemd_unit_events WHERE ts < cutoff;
    COMMIT;
    DELETE FROM package_events WHERE ts < cutoff;
    COMMIT;
    DELETE FROM events WHERE ts < cutoff;
    COMMIT;
END;
$$;

-- add_job has no if_not_exists, unlike every other Timescale registration in
-- this file, and this migration re-runs from the top whenever it fails
-- part-way (see the header). An unguarded call would therefore register a
-- second, third and fourth copy of the same job. The DO block is what keeps
-- the statement individually re-runnable.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM timescaledb_information.jobs
         WHERE proc_name = 'netra_prune_discrete_events'
    ) THEN
        PERFORM add_job('netra_prune_discrete_events', INTERVAL '1 day',
                        config => '{"retention": "90 days"}'::jsonb);
    END IF;
END;
$$;

-- --------------------------------------------------------------- per-core CPU

-- All cores, always -- ~800 series at target scale, which the earlier
-- cardinality concern overestimated.
CREATE TABLE IF NOT EXISTS cpu_core_samples (
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts      TIMESTAMPTZ NOT NULL,
    -- The N in /proc/stat's cpuN, not a physical package or thread id.
    core    INTEGER NOT NULL,
    busy    DOUBLE PRECISION,
    PRIMARY KEY (host_id, ts, core)
);

SELECT create_hypertable('cpu_core_samples', by_range('ts'), if_not_exists => TRUE);
SELECT set_chunk_time_interval('cpu_core_samples', INTERVAL '1 day');

CREATE MATERIALIZED VIEW IF NOT EXISTS cpu_core_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       core,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(busy) AS busy_avg,
       max(busy) AS busy_max
  FROM cpu_core_samples
 GROUP BY host_id, core, bucket
WITH NO DATA;

SELECT set_chunk_time_interval('cpu_core_samples_5m', INTERVAL '2 days');

CREATE MATERIALIZED VIEW IF NOT EXISTS cpu_core_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       core,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(busy_avg) AS busy_avg,
       max(busy_max) AS busy_max
  FROM cpu_core_samples_5m
 GROUP BY host_id, core, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('cpu_core_samples_1h', INTERVAL '7 days');

SELECT add_continuous_aggregate_policy('cpu_core_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('cpu_core_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

SELECT add_retention_policy('cpu_core_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('cpu_core_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('cpu_core_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);

-- ------------------------------------------------------------------- disk I/O

-- /proc/diskstats. The counters there are monotonic since boot and reset on
-- reboot, so every column here is a per-second rate or an interval-derived
-- percentage the AGENT computed -- only the agent holds the previous reading
-- needed to notice a reset, and a reset emits no sample at all rather than a
-- negative rate or a spike. Column names follow spec 5.3 verbatim.
--
-- device is the kernel name (sda, nvme0n1) and stays a string on purpose: the
-- surrogate-id rule (5.1 rule 2) exists for renameable identities, and 5.3
-- gives this column, net_samples.iface and collector_samples.collector as
-- bare names while sensor_samples, filesystem_samples and smart_attributes
-- get ids.
CREATE TABLE IF NOT EXISTS disk_io_samples (
    host_id         INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts              TIMESTAMPTZ NOT NULL,
    device          TEXT NOT NULL,
    read_bytes      DOUBLE PRECISION,
    write_bytes     DOUBLE PRECISION,
    read_ops        DOUBLE PRECISION,
    write_ops       DOUBLE PRECISION,
    io_util_pct     DOUBLE PRECISION,
    r_await_ms      DOUBLE PRECISION,
    w_await_ms      DOUBLE PRECISION,
    weighted_io_pct DOUBLE PRECISION,
    PRIMARY KEY (host_id, ts, device)
);

SELECT create_hypertable('disk_io_samples', by_range('ts'), if_not_exists => TRUE);
SELECT set_chunk_time_interval('disk_io_samples', INTERVAL '1 day');

CREATE MATERIALIZED VIEW IF NOT EXISTS disk_io_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       device,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       -- Every rate carries a max alongside the average, for the reason
       -- host_samples' rates do: a five-minute mean flattens exactly the
       -- I/O burst or latency spike these exist to show.
       avg(read_bytes)      AS read_bytes_avg,
       max(read_bytes)      AS read_bytes_max,
       avg(write_bytes)     AS write_bytes_avg,
       max(write_bytes)     AS write_bytes_max,
       avg(read_ops)        AS read_ops_avg,
       max(read_ops)        AS read_ops_max,
       avg(write_ops)       AS write_ops_avg,
       max(write_ops)       AS write_ops_max,
       avg(io_util_pct)     AS io_util_pct_avg,
       max(io_util_pct)     AS io_util_pct_max,
       avg(r_await_ms)      AS r_await_ms_avg,
       max(r_await_ms)      AS r_await_ms_max,
       avg(w_await_ms)      AS w_await_ms_avg,
       max(w_await_ms)      AS w_await_ms_max,
       avg(weighted_io_pct) AS weighted_io_pct_avg,
       max(weighted_io_pct) AS weighted_io_pct_max
  FROM disk_io_samples
 GROUP BY host_id, device, bucket
WITH NO DATA;

SELECT set_chunk_time_interval('disk_io_samples_5m', INTERVAL '2 days');

CREATE MATERIALIZED VIEW IF NOT EXISTS disk_io_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       device,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(read_bytes_avg)      AS read_bytes_avg,
       max(read_bytes_max)      AS read_bytes_max,
       avg(write_bytes_avg)     AS write_bytes_avg,
       max(write_bytes_max)     AS write_bytes_max,
       avg(read_ops_avg)        AS read_ops_avg,
       max(read_ops_max)        AS read_ops_max,
       avg(write_ops_avg)       AS write_ops_avg,
       max(write_ops_max)       AS write_ops_max,
       avg(io_util_pct_avg)     AS io_util_pct_avg,
       max(io_util_pct_max)     AS io_util_pct_max,
       avg(r_await_ms_avg)      AS r_await_ms_avg,
       max(r_await_ms_max)      AS r_await_ms_max,
       avg(w_await_ms_avg)      AS w_await_ms_avg,
       max(w_await_ms_max)      AS w_await_ms_max,
       avg(weighted_io_pct_avg) AS weighted_io_pct_avg,
       max(weighted_io_pct_max) AS weighted_io_pct_max
  FROM disk_io_samples_5m
 GROUP BY host_id, device, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('disk_io_samples_1h', INTERVAL '7 days');

SELECT add_continuous_aggregate_policy('disk_io_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('disk_io_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

SELECT add_retention_policy('disk_io_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('disk_io_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('disk_io_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);

-- -------------------------------------------------------------------- sensors

-- References sensors.id, not chip and label: a board that renames a sensor
-- between kernel versions changes one dimension row and no history.
CREATE TABLE IF NOT EXISTS sensor_samples (
    host_id   INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts        TIMESTAMPTZ NOT NULL,
    sensor_id INTEGER NOT NULL,
    -- Temperature in Celsius, unchanged and still what existing panels read.
    -- NULL for every non-temperature kind.
    temp      DOUBLE PRECISION,
    -- The reading in the kind's own unit. Set for EVERY kind, duplicating
    -- temp for temperatures on purpose: one column a reader can chart
    -- without first knowing which kind it asked for.
    value     DOUBLE PRECISION,
    PRIMARY KEY (host_id, ts, sensor_id),
    -- Composite rather than sensor_id alone, so the sensor is required to
    -- belong to the host on the same row. Every other Group 1 table's
    -- dimension is self-describing; this is the one that can disagree.
    FOREIGN KEY (sensor_id, host_id) REFERENCES sensors (id, host_id) ON DELETE CASCADE
);

SELECT create_hypertable('sensor_samples', by_range('ts'), if_not_exists => TRUE);
SELECT set_chunk_time_interval('sensor_samples', INTERVAL '1 day');

-- The composite foreign key's cascade path needs its own index. Deleting a
-- host cascades to sensors first (that constraint is created earlier, so its
-- trigger fires first), and each deleted sensor makes Postgres run
-- "WHERE sensor_id = $1 AND host_id = $2" against every chunk. The primary
-- key leads with host_id, not sensor_id, so without this index that is a
-- sequential scan per chunk per sensor -- twenty sensors across a week of
-- one-day chunks is a hundred and sixty full chunk scans inside
-- DELETE /api/v1/hosts/{id}. Column order matches the foreign key.
CREATE INDEX IF NOT EXISTS sensor_samples_sensor_id_host_id_idx
    ON sensor_samples (sensor_id, host_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS sensor_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       sensor_id,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(temp) AS temp_avg,
       max(temp) AS temp_max,
       avg(value) AS value_avg,
       max(value) AS value_max,
       -- A fan's failure is its MINIMUM, not its peak: a stopped fan inside a
       -- five-minute bucket is invisible in both the average and the maximum.
       min(value) AS value_min
  FROM sensor_samples
 GROUP BY host_id, sensor_id, bucket
WITH NO DATA;

SELECT set_chunk_time_interval('sensor_samples_5m', INTERVAL '2 days');

CREATE MATERIALIZED VIEW IF NOT EXISTS sensor_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       sensor_id,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(temp_avg) AS temp_avg,
       max(temp_max) AS temp_max,
       avg(value_avg) AS value_avg,
       max(value_max) AS value_max,
       min(value_min) AS value_min
  FROM sensor_samples_5m
 GROUP BY host_id, sensor_id, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('sensor_samples_1h', INTERVAL '7 days');

SELECT add_continuous_aggregate_policy('sensor_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('sensor_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

SELECT add_retention_policy('sensor_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('sensor_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('sensor_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);

-- -------------------------------------------------------------------- network

-- /proc/net/dev, same counter-reset treatment as disk_io_samples: per-second
-- rates computed agent-side, no sample at all across a reset.
--
-- iface is the interface name and is the join key with host_addresses.iface
-- (spec 6.2), which is why it is not a surrogate id.
CREATE TABLE IF NOT EXISTS net_samples (
    host_id  INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts       TIMESTAMPTZ NOT NULL,
    iface    TEXT NOT NULL,
    rx_bytes DOUBLE PRECISION,
    tx_bytes DOUBLE PRECISION,
    rx_errs  DOUBLE PRECISION,
    tx_errs  DOUBLE PRECISION,
    PRIMARY KEY (host_id, ts, iface)
);

SELECT create_hypertable('net_samples', by_range('ts'), if_not_exists => TRUE);
SELECT set_chunk_time_interval('net_samples', INTERVAL '1 day');

CREATE MATERIALIZED VIEW IF NOT EXISTS net_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       iface,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(rx_bytes) AS rx_bytes_avg,
       max(rx_bytes) AS rx_bytes_max,
       avg(tx_bytes) AS tx_bytes_avg,
       max(tx_bytes) AS tx_bytes_max,
       avg(rx_errs)  AS rx_errs_avg,
       max(rx_errs)  AS rx_errs_max,
       avg(tx_errs)  AS tx_errs_avg,
       max(tx_errs)  AS tx_errs_max
  FROM net_samples
 GROUP BY host_id, iface, bucket
WITH NO DATA;

SELECT set_chunk_time_interval('net_samples_5m', INTERVAL '2 days');

CREATE MATERIALIZED VIEW IF NOT EXISTS net_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       iface,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(rx_bytes_avg) AS rx_bytes_avg,
       max(rx_bytes_max) AS rx_bytes_max,
       avg(tx_bytes_avg) AS tx_bytes_avg,
       max(tx_bytes_max) AS tx_bytes_max,
       avg(rx_errs_avg)  AS rx_errs_avg,
       max(rx_errs_max)  AS rx_errs_max,
       avg(tx_errs_avg)  AS tx_errs_avg,
       max(tx_errs_max)  AS tx_errs_max
  FROM net_samples_5m
 GROUP BY host_id, iface, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('net_samples_1h', INTERVAL '7 days');

SELECT add_continuous_aggregate_policy('net_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('net_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

SELECT add_retention_policy('net_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('net_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('net_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);

-- -------------------------------------------------------- collector telemetry

-- One row per collector per scrape (spec 6.1). This is what turns beszel's
-- silent Debug-level degradation into something queryable: a collector that
-- has been failing for three days is a row shape here, not a log line nobody
-- reads.
--
-- ok is NOT NULL rather than following the absent-is-NULL rule, because it is
-- a verdict about a run that demonstrably happened -- the row exists only
-- because the collector ran. Absence is expressed by there being no row.
CREATE TABLE IF NOT EXISTS collector_samples (
    host_id     INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts          TIMESTAMPTZ NOT NULL,
    collector   TEXT NOT NULL,
    duration_ms INTEGER,
    ok          BOOLEAN NOT NULL,
    -- NULL on success. A short stable token (permission_denied, timeout),
    -- never a formatted message: this column is grouped by, not read.
    error_code  TEXT,
    PRIMARY KEY (host_id, ts, collector)
);

SELECT create_hypertable('collector_samples', by_range('ts'), if_not_exists => TRUE);
SELECT set_chunk_time_interval('collector_samples', INTERVAL '1 day');

CREATE MATERIALIZED VIEW IF NOT EXISTS collector_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       collector,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(duration_ms) AS duration_ms_avg,
       max(duration_ms) AS duration_ms_max,
       -- Counts rather than a success ratio: a ratio cannot be rolled up into
       -- the 1h tier without weighting, and two counts can simply be summed.
       count(*)               AS sample_count,
       sum((NOT ok)::INTEGER) AS failure_count,
       -- Which failure, not how many -- the last one in the bucket answers
       -- "what is wrong with this collector right now".
       last(error_code, ts)   AS error_code
  FROM collector_samples
 GROUP BY host_id, collector, bucket
WITH NO DATA;

SELECT set_chunk_time_interval('collector_samples_5m', INTERVAL '2 days');

CREATE MATERIALIZED VIEW IF NOT EXISTS collector_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       collector,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(duration_ms_avg)     AS duration_ms_avg,
       max(duration_ms_max)     AS duration_ms_max,
       -- Cast back to bigint: sum(bigint) returns numeric, which would give
       -- collector_samples_1h a different column type from
       -- collector_samples_5m. 1D's tier selection reads both through one
       -- query path, so a shared scan target would break on the wider range.
       sum(sample_count)::BIGINT  AS sample_count,
       sum(failure_count)::BIGINT AS failure_count,
       last(error_code, bucket) AS error_code
  FROM collector_samples_5m
 GROUP BY host_id, collector, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('collector_samples_1h', INTERVAL '7 days');

SELECT add_continuous_aggregate_policy('collector_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('collector_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

SELECT add_retention_policy('collector_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('collector_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('collector_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);

-- ------------------------------------------------------------- containers

-- container_id references containers.id, never the Docker id: a recreate
-- issues a new Docker id for the same service, and keying history on it would
-- restart every series whenever an image is bumped.
--
-- mem_used already has cache and inactive_file subtracted by the agent. Raw
-- cgroup memory.current counts the page cache as consumption, so a container
-- that merely read files would look like it is holding that memory.
CREATE TABLE IF NOT EXISTS container_samples (
    host_id      INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts           TIMESTAMPTZ NOT NULL,
    container_id INTEGER NOT NULL,
    cpu_pct      DOUBLE PRECISION,
    mem_used     BIGINT,
    mem_limit    BIGINT,
    net_rx       DOUBLE PRECISION,
    net_tx       DOUBLE PRECISION,
    io_read      DOUBLE PRECISION,
    io_write     DOUBLE PRECISION,
    -- cgroup v2's own splits of the two numbers above. cpu_user and
    -- cpu_system sum to cpu_pct; the four mem_ columns are memory.stat's
    -- parts, with mem_file already net of mem_shmem so a stack of them does
    -- not draw the same pages twice.
    cpu_user     DOUBLE PRECISION,
    cpu_system   DOUBLE PRECISION,
    mem_anon     BIGINT,
    mem_file     BIGINT,
    mem_shmem    BIGINT,
    mem_kernel   BIGINT,
    PRIMARY KEY (host_id, ts, container_id),
    FOREIGN KEY (container_id, host_id) REFERENCES containers (id, host_id) ON DELETE CASCADE
);

SELECT create_hypertable('container_samples', by_range('ts'), if_not_exists => TRUE);
SELECT set_chunk_time_interval('container_samples', INTERVAL '1 day');

-- The composite foreign key's cascade path needs its own index, exactly as
-- sensor_samples does: deleting a host cascades to containers first, and each
-- deleted container then scans every chunk for its samples. The primary key
-- leads with host_id, not container_id, so without this that is a sequential
-- scan per chunk per container inside DELETE /api/v1/hosts/{id}.
CREATE INDEX IF NOT EXISTS container_samples_container_id_host_id_idx
    ON container_samples (container_id, host_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS container_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       container_id,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(cpu_pct)   AS cpu_pct_avg,
       max(cpu_pct)   AS cpu_pct_max,
       avg(mem_used)  AS mem_used_avg,
       max(mem_used)  AS mem_used_max,
       max(mem_limit) AS mem_limit_max,
       -- avg only, and for the reason the host tiers give: these are parts
       -- of a whole, and a chart stacking a max of one against an avg of
       -- another composes two different instants into one bar.
       avg(cpu_user)   AS cpu_user_avg,
       avg(cpu_system) AS cpu_system_avg,
       avg(mem_anon)   AS mem_anon_avg,
       avg(mem_file)   AS mem_file_avg,
       avg(mem_shmem)  AS mem_shmem_avg,
       avg(mem_kernel) AS mem_kernel_avg,
       avg(net_rx)    AS net_rx_avg,
       max(net_rx)    AS net_rx_max,
       avg(net_tx)    AS net_tx_avg,
       max(net_tx)    AS net_tx_max,
       avg(io_read)   AS io_read_avg,
       max(io_read)   AS io_read_max,
       avg(io_write)  AS io_write_avg,
       max(io_write)  AS io_write_max
  FROM container_samples
 GROUP BY host_id, container_id, bucket
WITH NO DATA;

SELECT set_chunk_time_interval('container_samples_5m', INTERVAL '2 days');

CREATE MATERIALIZED VIEW IF NOT EXISTS container_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       container_id,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(cpu_pct_avg)  AS cpu_pct_avg,
       max(cpu_pct_max)  AS cpu_pct_max,
       avg(mem_used_avg) AS mem_used_avg,
       max(mem_used_max) AS mem_used_max,
       max(mem_limit_max) AS mem_limit_max,
       -- The same six the 5m tier rolls up, and for the same reason they are
       -- avg only there: they are parts of a whole. Missing here, a container
       -- page answered from this tier -- 7d and 30d -- carried no split at
       -- all and silently collapsed to the single cpu_pct/mem_used line,
       -- while 6h and 24h showed the breakdown. host_samples_1h was updated
       -- when the host charts were split; this one was not, so the container
       -- charts degraded with the range and the host charts did not.
       avg(cpu_user_avg)   AS cpu_user_avg,
       avg(cpu_system_avg) AS cpu_system_avg,
       avg(mem_anon_avg)   AS mem_anon_avg,
       avg(mem_file_avg)   AS mem_file_avg,
       avg(mem_shmem_avg)  AS mem_shmem_avg,
       avg(mem_kernel_avg) AS mem_kernel_avg,
       avg(net_rx_avg)   AS net_rx_avg,
       max(net_rx_max)   AS net_rx_max,
       avg(net_tx_avg)   AS net_tx_avg,
       max(net_tx_max)   AS net_tx_max,
       avg(io_read_avg)  AS io_read_avg,
       max(io_read_max)  AS io_read_max,
       avg(io_write_avg) AS io_write_avg,
       max(io_write_max) AS io_write_max
  FROM container_samples_5m
 GROUP BY host_id, container_id, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('container_samples_1h', INTERVAL '7 days');

SELECT add_continuous_aggregate_policy('container_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('container_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

SELECT add_retention_policy('container_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('container_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('container_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);

-- ------------------------------------------------------------ filesystems

-- read_bytes and write_bytes are NULL, not zero, when the st_dev -> block
-- device mapping fails: "we could not attribute I/O to this filesystem" is a
-- different fact from "this filesystem did no I/O", and averaging the two
-- together would understate every host where the mapping is unavailable.
--
-- used AND free DO NOT SUM TO total, by construction, and a consumer that
-- assumes they do will be wrong on every default ext4 filesystem. The agent
-- reports what df reports:
--
--   total = statfs Blocks           every block on the filesystem
--   free  = statfs Bavail           what an unprivileged process may allocate
--   used  = Blocks - Bfree          what actually holds data
--
-- The gap is the root reserve (5% by default), which holds no data and is not
-- available either. So a fullness percentage MUST be used / (used + free) --
-- df's Use% -- and never used / total, which reads ~5% low. The rollups below
-- inherit this: used_avg, used_max and free_min are the same three quantities
-- bucketed, and mixing them with total the wrong way is wrong at every
-- resolution.
CREATE TABLE IF NOT EXISTS filesystem_samples (
    host_id      INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts           TIMESTAMPTZ NOT NULL,
    fs_id        INTEGER NOT NULL,
    total        BIGINT,
    used         BIGINT,
    free         BIGINT,
    inodes_total BIGINT,
    inodes_used  BIGINT,
    read_bytes   DOUBLE PRECISION,
    write_bytes  DOUBLE PRECISION,
    PRIMARY KEY (host_id, ts, fs_id),
    FOREIGN KEY (fs_id, host_id) REFERENCES filesystems (id, host_id) ON DELETE CASCADE
);

SELECT create_hypertable('filesystem_samples', by_range('ts'), if_not_exists => TRUE);
SELECT set_chunk_time_interval('filesystem_samples', INTERVAL '1 day');

CREATE INDEX IF NOT EXISTS filesystem_samples_fs_id_host_id_idx
    ON filesystem_samples (fs_id, host_id);

-- total_max and inodes_total_max, NOT total and inodes_total. An aggregate
-- column carrying the raw column's own name is a bucket statistic wearing an
-- instantaneous reading's clothes: max(total) over five minutes differs from
-- the raw total exactly when a filesystem is resized down mid-bucket, and a
-- reader who asked for "total" has no way to notice.
--
-- It is also what makes the read API's tier guarantee true rather than nearly
-- true. Every value column must be named differently at every tier, so a
-- client that ignores which tier answered gets a key it does not recognise
-- instead of a plausible number -- see internal/hub/read/metrics.go and
-- TestIntegrationNoValueColumnNameIsSharedBetweenTiers, which enumerates every
-- family and fails if a new aggregate reintroduces one.
--
-- The exemptions are last() columns and monotonic counters, where the bucket
-- value IS the raw quantity. They are listed in that test with the reason.
CREATE MATERIALIZED VIEW IF NOT EXISTS filesystem_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       fs_id,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       max(total)         AS total_max,
       avg(used)          AS used_avg,
       max(used)          AS used_max,
       min(free)          AS free_min,
       max(inodes_total)  AS inodes_total_max,
       max(inodes_used)   AS inodes_used_max,
       avg(read_bytes)    AS read_bytes_avg,
       max(read_bytes)    AS read_bytes_max,
       avg(write_bytes)   AS write_bytes_avg,
       max(write_bytes)   AS write_bytes_max
  FROM filesystem_samples
 GROUP BY host_id, fs_id, bucket
WITH NO DATA;

SELECT set_chunk_time_interval('filesystem_samples_5m', INTERVAL '2 days');

CREATE MATERIALIZED VIEW IF NOT EXISTS filesystem_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       fs_id,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       max(total_max)        AS total_max,
       avg(used_avg)         AS used_avg,
       max(used_max)         AS used_max,
       min(free_min)         AS free_min,
       max(inodes_total_max) AS inodes_total_max,
       max(inodes_used_max)  AS inodes_used_max,
       avg(read_bytes_avg)   AS read_bytes_avg,
       max(read_bytes_max)   AS read_bytes_max,
       avg(write_bytes_avg)  AS write_bytes_avg,
       max(write_bytes_max)  AS write_bytes_max
  FROM filesystem_samples_5m
 GROUP BY host_id, fs_id, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('filesystem_samples_1h', INTERVAL '7 days');

SELECT add_continuous_aggregate_policy('filesystem_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('filesystem_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

SELECT add_retention_policy('filesystem_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('filesystem_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('filesystem_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);

-- ------------------------------------------------------------------ SMART

-- Deliberately generic: SMART attribute sets vary per drive model, so a column
-- per attribute would need a migration for every new drive (spec 5.3).
--
-- RAW ONLY, and that is a decision rather than an omission. SMART is read
-- hourly, so a 5-minute bucket holds at most one reading and a 1-hour bucket
-- exactly one -- continuous aggregates here would restate the raw table at
-- triple the storage and answer no question the raw table cannot.
-- TestIntegrationRawOnlyTablesHaveNoContinuousAggregates pins this, so adding
-- one later is a deliberate act rather than an accident.
--
-- Retention is 90 days, matching the 1h tier elsewhere: at one reading an hour
-- the row count is tiny, and drive degradation is a slow trend worth keeping
-- as long as any aggregate.
CREATE TABLE IF NOT EXISTS smart_attributes (
    host_id    INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts         TIMESTAMPTZ NOT NULL,
    device_id  INTEGER NOT NULL,
    attr_id    SMALLINT NOT NULL,
    raw        BIGINT,
    normalized SMALLINT,
    PRIMARY KEY (host_id, ts, device_id, attr_id),
    FOREIGN KEY (device_id, host_id) REFERENCES devices (id, host_id) ON DELETE CASCADE
);

SELECT create_hypertable('smart_attributes', by_range('ts'), if_not_exists => TRUE);

-- 7 days rather than 1: at one reading an hour a day holds ~24 rows per
-- attribute, so daily chunks would be thousands of near-empty chunks over the
-- 90-day retention, each with its own planning cost.
SELECT set_chunk_time_interval('smart_attributes', INTERVAL '7 days');

CREATE INDEX IF NOT EXISTS smart_attributes_device_id_host_id_idx
    ON smart_attributes (device_id, host_id);

SELECT add_retention_policy('smart_attributes', INTERVAL '90 days', if_not_exists => TRUE);

-- -------------------------------------------------------------- processes

-- name is comm, from /proc/PID/stat -- NEVER argv. Reading cmdline or environ
-- would capture secrets passed on command lines, and argv_guard_test.go fails
-- the build if either name appears in a Go string literal.
--
-- RAW ONLY, 48 hours: the spec's stated exception (5.4). A 1-hour average of a
-- top-N list whose membership changes between buckets is close to meaningless,
-- and process data answers recent-past questions rather than trend ones.
CREATE TABLE IF NOT EXISTS process_samples (
    host_id    INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts         TIMESTAMPTZ NOT NULL,
    name       TEXT NOT NULL,
    cpu_pct    DOUBLE PRECISION,
    mem_bytes  BIGINT,
    -- How many processes were aggregated under this name.
    count      INTEGER,
    PRIMARY KEY (host_id, ts, name)
);

SELECT create_hypertable('process_samples', by_range('ts'), if_not_exists => TRUE);

-- 6 hours against 48 hours of retention: a chunk is dropped only once its
-- NEWEST row expires, so a 1-day chunk would retain up to 72 hours against a
-- stated 48.
SELECT set_chunk_time_interval('process_samples', INTERVAL '6 hours');

SELECT add_retention_policy('process_samples', INTERVAL '48 hours', if_not_exists => TRUE);

-- ---------------------------------------------------------------------------
-- IP and ICMP MIBs (folded in from 0003_host_snmp_samples.sql)
-- ---------------------------------------------------------------------------

-- The IP and ICMP MIBs from /proc/net/snmp and /proc/net/snmp6.
--
-- A separate table rather than seventy more columns on host_samples, and that
-- is load-bearing rather than tidiness. A TimescaleDB continuous aggregate
-- cannot gain a column: adding one to host_samples means DROP and CREATE on
-- host_samples_5m and host_samples_1h, which re-materialises only from the
-- seven days of raw still on disk. Every rolled-up host metric older than
-- that -- CPU, memory, load, disk, not just the new ones -- would be
-- destroyed: 23 days of the 5m tier and 83 days of the 1h tier. A parallel
-- table costs one read family and one more fetch on the graphs tab, and
-- loses nothing.
--
-- The counter set is deliberately wider than what the UI charts today, for
-- the same reason: a counter added later costs that same drop-and-recreate,
-- while a counter stored and never drawn costs a column.
--
-- Read alongside host_samples above, whose /proc/net/snmp section stops at
-- Udp: and IP fragmentation.
--
-- This arrived as 0003_host_snmp_samples.sql and was folded in here when the
-- schema was squashed and the database recreated. The separate TABLE is not
-- part of that squash and stays: the argument above is about continuous
-- aggregates, not about which file the DDL lives in, and it holds for every
-- future column too.
--
-- IcmpMsg: is not here. Its column names are per-ICMP-type and depend on
-- which types the host has actually seen, so no fixed column set can be
-- derived from it. The named Icmp: counters below cover the types worth
-- charting: DestUnreachs is type 3, Echos is type 8.

CREATE TABLE IF NOT EXISTS host_snmp_samples (
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts      TIMESTAMPTZ NOT NULL,

    -- /proc/net/snmp, Ip: what the host moved. These three are the
    -- volume the IP statistics panel draws; the interface counters in
    -- net_samples measure bytes on a wire, which is a different
    -- question from how many datagrams the stack accepted and passed up.
    ip_in_receives_per_s                     DOUBLE PRECISION,
    ip_in_delivers_per_s                     DOUBLE PRECISION,
    ip_out_requests_per_s                    DOUBLE PRECISION,
    ip_forw_datagrams_per_s                  DOUBLE PRECISION,
    ip_reasm_oks_per_s                       DOUBLE PRECISION,
    ip_frag_oks_per_s                        DOUBLE PRECISION,

    -- /proc/net/snmp, Ip: what it could not. in_hdr_errors and
    -- in_addr_errors are malformed or misdirected traffic ARRIVING;
    -- out_no_routes is this host failing to send. Reading them on one
    -- axis with the volume above would bury all six.
    ip_in_hdr_errors_per_s                   DOUBLE PRECISION,
    ip_in_addr_errors_per_s                  DOUBLE PRECISION,
    ip_in_unknown_protos_per_s               DOUBLE PRECISION,
    ip_in_discards_per_s                     DOUBLE PRECISION,
    ip_out_discards_per_s                    DOUBLE PRECISION,
    ip_out_no_routes_per_s                   DOUBLE PRECISION,
    ip_reasm_timeout_per_s                   DOUBLE PRECISION,

    -- /proc/net/snmp6, Ip6*. Three have no IPv4 peer above and that is
    -- the kernel's asymmetry, not a gap here: the Ip: block has no
    -- InNoRoutes at all, and "packet too big" is an ICMPv6 fact that
    -- IPv4 records as fragmentation instead.
    ip6_in_receives_per_s                    DOUBLE PRECISION,
    ip6_in_delivers_per_s                    DOUBLE PRECISION,
    ip6_out_requests_per_s                   DOUBLE PRECISION,
    ip6_out_forw_datagrams_per_s             DOUBLE PRECISION,
    ip6_reasm_oks_per_s                      DOUBLE PRECISION,
    ip6_frag_oks_per_s                       DOUBLE PRECISION,
    ip6_in_hdr_errors_per_s                  DOUBLE PRECISION,
    ip6_in_addr_errors_per_s                 DOUBLE PRECISION,
    ip6_in_unknown_protos_per_s              DOUBLE PRECISION,
    ip6_in_discards_per_s                    DOUBLE PRECISION,
    ip6_out_discards_per_s                   DOUBLE PRECISION,
    ip6_out_no_routes_per_s                  DOUBLE PRECISION,
    ip6_in_no_routes_per_s                   DOUBLE PRECISION,
    ip6_in_too_big_errors_per_s              DOUBLE PRECISION,
    ip6_reasm_timeout_per_s                  DOUBLE PRECISION,

    -- /proc/net/snmp, Icmp: the error and control half. dest_unreachs
    -- IN means somewhere ahead is refusing this host's traffic;
    -- dest_unreachs OUT means this host is refusing someone else's.
    -- Both directions are kept because they answer opposite questions.
    icmp_in_msgs_per_s                       DOUBLE PRECISION,
    icmp_out_msgs_per_s                      DOUBLE PRECISION,
    icmp_in_errors_per_s                     DOUBLE PRECISION,
    icmp_out_errors_per_s                    DOUBLE PRECISION,
    icmp_in_dest_unreachs_per_s              DOUBLE PRECISION,
    icmp_out_dest_unreachs_per_s             DOUBLE PRECISION,
    icmp_in_time_excds_per_s                 DOUBLE PRECISION,
    icmp_out_time_excds_per_s                DOUBLE PRECISION,
    icmp_in_parm_probs_per_s                 DOUBLE PRECISION,
    icmp_out_parm_probs_per_s                DOUBLE PRECISION,
    icmp_in_redirects_per_s                  DOUBLE PRECISION,
    icmp_out_redirects_per_s                 DOUBLE PRECISION,

    -- /proc/net/snmp, Icmp: the informational half, on its own panel.
    -- Echo is reachability rather than failure: a host answering pings
    -- is not a host in trouble, and charting it beside dest_unreachs
    -- would put a healthy signal on a failure axis.
    icmp_in_echos_per_s                      DOUBLE PRECISION,
    icmp_out_echos_per_s                     DOUBLE PRECISION,
    icmp_in_echo_reps_per_s                  DOUBLE PRECISION,
    icmp_out_echo_reps_per_s                 DOUBLE PRECISION,

    -- /proc/net/snmp6, Icmp6*. Unlike Tcp:, ICMP really is per-family:
    -- Icmp: counts v4 alone. Spellings follow the kernel's
    -- icmp6type2name[] table, which is why two differ from their v4
    -- peers -- ParmProblems, not ParmProbs, and EchoReplies, not
    -- EchoReps.
    icmp6_in_msgs_per_s                      DOUBLE PRECISION,
    icmp6_out_msgs_per_s                     DOUBLE PRECISION,
    icmp6_in_errors_per_s                    DOUBLE PRECISION,
    icmp6_out_errors_per_s                   DOUBLE PRECISION,
    icmp6_in_dest_unreachs_per_s             DOUBLE PRECISION,
    icmp6_out_dest_unreachs_per_s            DOUBLE PRECISION,
    icmp6_in_time_excds_per_s                DOUBLE PRECISION,
    icmp6_out_time_excds_per_s               DOUBLE PRECISION,
    icmp6_in_parm_problems_per_s             DOUBLE PRECISION,
    icmp6_out_parm_problems_per_s            DOUBLE PRECISION,
    icmp6_in_pkt_too_bigs_per_s              DOUBLE PRECISION,
    icmp6_out_pkt_too_bigs_per_s             DOUBLE PRECISION,
    icmp6_in_redirects_per_s                 DOUBLE PRECISION,
    icmp6_out_redirects_per_s                DOUBLE PRECISION,
    icmp6_in_echos_per_s                     DOUBLE PRECISION,
    icmp6_out_echos_per_s                    DOUBLE PRECISION,
    icmp6_in_echo_replies_per_s              DOUBLE PRECISION,
    icmp6_out_echo_replies_per_s             DOUBLE PRECISION,

    -- Neighbour discovery. On IPv6 this IS the informational traffic:
    -- there is no ARP, so solicits and advertisements carry what an
    -- operator would otherwise read out of an ARP table, and a host
    -- that stops answering neighbour solicits vanishes from its
    -- segment while every other counter here still looks healthy.
    icmp6_in_neighbor_solicits_per_s         DOUBLE PRECISION,
    icmp6_out_neighbor_solicits_per_s        DOUBLE PRECISION,
    icmp6_in_neighbor_advertisements_per_s   DOUBLE PRECISION,
    icmp6_out_neighbor_advertisements_per_s  DOUBLE PRECISION,
    icmp6_in_router_solicits_per_s           DOUBLE PRECISION,
    icmp6_out_router_solicits_per_s          DOUBLE PRECISION,
    icmp6_in_router_advertisements_per_s     DOUBLE PRECISION,
    icmp6_out_router_advertisements_per_s    DOUBLE PRECISION,

    -- Natural key, the same one host_samples uses. Replayed batches collide
    -- here and are discarded by ON CONFLICT DO NOTHING.
    PRIMARY KEY (host_id, ts)
);

SELECT create_hypertable('host_snmp_samples', by_range('ts'), if_not_exists => TRUE);

-- One-day chunks, as host_samples uses: drop_chunks removes a chunk only once
-- its newest row is past the cutoff, so a coarser chunk turns "7 days" into
-- as much as 14.
SELECT set_chunk_time_interval('host_snmp_samples', INTERVAL '1 day');

CREATE MATERIALIZED VIEW IF NOT EXISTS host_snmp_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(ip_in_receives_per_s) AS ip_in_receives_per_s_avg,
       avg(ip_in_delivers_per_s) AS ip_in_delivers_per_s_avg,
       avg(ip_out_requests_per_s) AS ip_out_requests_per_s_avg,
       avg(ip_forw_datagrams_per_s) AS ip_forw_datagrams_per_s_avg,
       avg(ip_reasm_oks_per_s) AS ip_reasm_oks_per_s_avg,
       avg(ip_frag_oks_per_s) AS ip_frag_oks_per_s_avg,
       avg(ip_in_hdr_errors_per_s) AS ip_in_hdr_errors_per_s_avg,
       max(ip_in_hdr_errors_per_s) AS ip_in_hdr_errors_per_s_max,
       avg(ip_in_addr_errors_per_s) AS ip_in_addr_errors_per_s_avg,
       max(ip_in_addr_errors_per_s) AS ip_in_addr_errors_per_s_max,
       avg(ip_in_unknown_protos_per_s) AS ip_in_unknown_protos_per_s_avg,
       max(ip_in_unknown_protos_per_s) AS ip_in_unknown_protos_per_s_max,
       avg(ip_in_discards_per_s) AS ip_in_discards_per_s_avg,
       max(ip_in_discards_per_s) AS ip_in_discards_per_s_max,
       avg(ip_out_discards_per_s) AS ip_out_discards_per_s_avg,
       max(ip_out_discards_per_s) AS ip_out_discards_per_s_max,
       avg(ip_out_no_routes_per_s) AS ip_out_no_routes_per_s_avg,
       max(ip_out_no_routes_per_s) AS ip_out_no_routes_per_s_max,
       avg(ip_reasm_timeout_per_s) AS ip_reasm_timeout_per_s_avg,
       max(ip_reasm_timeout_per_s) AS ip_reasm_timeout_per_s_max,
       avg(ip6_in_receives_per_s) AS ip6_in_receives_per_s_avg,
       avg(ip6_in_delivers_per_s) AS ip6_in_delivers_per_s_avg,
       avg(ip6_out_requests_per_s) AS ip6_out_requests_per_s_avg,
       avg(ip6_out_forw_datagrams_per_s) AS ip6_out_forw_datagrams_per_s_avg,
       avg(ip6_reasm_oks_per_s) AS ip6_reasm_oks_per_s_avg,
       avg(ip6_frag_oks_per_s) AS ip6_frag_oks_per_s_avg,
       avg(ip6_in_hdr_errors_per_s) AS ip6_in_hdr_errors_per_s_avg,
       max(ip6_in_hdr_errors_per_s) AS ip6_in_hdr_errors_per_s_max,
       avg(ip6_in_addr_errors_per_s) AS ip6_in_addr_errors_per_s_avg,
       max(ip6_in_addr_errors_per_s) AS ip6_in_addr_errors_per_s_max,
       avg(ip6_in_unknown_protos_per_s) AS ip6_in_unknown_protos_per_s_avg,
       max(ip6_in_unknown_protos_per_s) AS ip6_in_unknown_protos_per_s_max,
       avg(ip6_in_discards_per_s) AS ip6_in_discards_per_s_avg,
       max(ip6_in_discards_per_s) AS ip6_in_discards_per_s_max,
       avg(ip6_out_discards_per_s) AS ip6_out_discards_per_s_avg,
       max(ip6_out_discards_per_s) AS ip6_out_discards_per_s_max,
       avg(ip6_out_no_routes_per_s) AS ip6_out_no_routes_per_s_avg,
       max(ip6_out_no_routes_per_s) AS ip6_out_no_routes_per_s_max,
       avg(ip6_in_no_routes_per_s) AS ip6_in_no_routes_per_s_avg,
       max(ip6_in_no_routes_per_s) AS ip6_in_no_routes_per_s_max,
       avg(ip6_in_too_big_errors_per_s) AS ip6_in_too_big_errors_per_s_avg,
       max(ip6_in_too_big_errors_per_s) AS ip6_in_too_big_errors_per_s_max,
       avg(ip6_reasm_timeout_per_s) AS ip6_reasm_timeout_per_s_avg,
       max(ip6_reasm_timeout_per_s) AS ip6_reasm_timeout_per_s_max,
       avg(icmp_in_msgs_per_s) AS icmp_in_msgs_per_s_avg,
       avg(icmp_out_msgs_per_s) AS icmp_out_msgs_per_s_avg,
       avg(icmp_in_errors_per_s) AS icmp_in_errors_per_s_avg,
       max(icmp_in_errors_per_s) AS icmp_in_errors_per_s_max,
       avg(icmp_out_errors_per_s) AS icmp_out_errors_per_s_avg,
       max(icmp_out_errors_per_s) AS icmp_out_errors_per_s_max,
       avg(icmp_in_dest_unreachs_per_s) AS icmp_in_dest_unreachs_per_s_avg,
       max(icmp_in_dest_unreachs_per_s) AS icmp_in_dest_unreachs_per_s_max,
       avg(icmp_out_dest_unreachs_per_s) AS icmp_out_dest_unreachs_per_s_avg,
       max(icmp_out_dest_unreachs_per_s) AS icmp_out_dest_unreachs_per_s_max,
       avg(icmp_in_time_excds_per_s) AS icmp_in_time_excds_per_s_avg,
       max(icmp_in_time_excds_per_s) AS icmp_in_time_excds_per_s_max,
       avg(icmp_out_time_excds_per_s) AS icmp_out_time_excds_per_s_avg,
       max(icmp_out_time_excds_per_s) AS icmp_out_time_excds_per_s_max,
       avg(icmp_in_parm_probs_per_s) AS icmp_in_parm_probs_per_s_avg,
       max(icmp_in_parm_probs_per_s) AS icmp_in_parm_probs_per_s_max,
       avg(icmp_out_parm_probs_per_s) AS icmp_out_parm_probs_per_s_avg,
       max(icmp_out_parm_probs_per_s) AS icmp_out_parm_probs_per_s_max,
       avg(icmp_in_redirects_per_s) AS icmp_in_redirects_per_s_avg,
       max(icmp_in_redirects_per_s) AS icmp_in_redirects_per_s_max,
       avg(icmp_out_redirects_per_s) AS icmp_out_redirects_per_s_avg,
       max(icmp_out_redirects_per_s) AS icmp_out_redirects_per_s_max,
       avg(icmp_in_echos_per_s) AS icmp_in_echos_per_s_avg,
       avg(icmp_out_echos_per_s) AS icmp_out_echos_per_s_avg,
       avg(icmp_in_echo_reps_per_s) AS icmp_in_echo_reps_per_s_avg,
       avg(icmp_out_echo_reps_per_s) AS icmp_out_echo_reps_per_s_avg,
       avg(icmp6_in_msgs_per_s) AS icmp6_in_msgs_per_s_avg,
       avg(icmp6_out_msgs_per_s) AS icmp6_out_msgs_per_s_avg,
       avg(icmp6_in_errors_per_s) AS icmp6_in_errors_per_s_avg,
       max(icmp6_in_errors_per_s) AS icmp6_in_errors_per_s_max,
       avg(icmp6_out_errors_per_s) AS icmp6_out_errors_per_s_avg,
       max(icmp6_out_errors_per_s) AS icmp6_out_errors_per_s_max,
       avg(icmp6_in_dest_unreachs_per_s) AS icmp6_in_dest_unreachs_per_s_avg,
       max(icmp6_in_dest_unreachs_per_s) AS icmp6_in_dest_unreachs_per_s_max,
       avg(icmp6_out_dest_unreachs_per_s) AS icmp6_out_dest_unreachs_per_s_avg,
       max(icmp6_out_dest_unreachs_per_s) AS icmp6_out_dest_unreachs_per_s_max,
       avg(icmp6_in_time_excds_per_s) AS icmp6_in_time_excds_per_s_avg,
       max(icmp6_in_time_excds_per_s) AS icmp6_in_time_excds_per_s_max,
       avg(icmp6_out_time_excds_per_s) AS icmp6_out_time_excds_per_s_avg,
       max(icmp6_out_time_excds_per_s) AS icmp6_out_time_excds_per_s_max,
       avg(icmp6_in_parm_problems_per_s) AS icmp6_in_parm_problems_per_s_avg,
       max(icmp6_in_parm_problems_per_s) AS icmp6_in_parm_problems_per_s_max,
       avg(icmp6_out_parm_problems_per_s) AS icmp6_out_parm_problems_per_s_avg,
       max(icmp6_out_parm_problems_per_s) AS icmp6_out_parm_problems_per_s_max,
       avg(icmp6_in_pkt_too_bigs_per_s) AS icmp6_in_pkt_too_bigs_per_s_avg,
       max(icmp6_in_pkt_too_bigs_per_s) AS icmp6_in_pkt_too_bigs_per_s_max,
       avg(icmp6_out_pkt_too_bigs_per_s) AS icmp6_out_pkt_too_bigs_per_s_avg,
       max(icmp6_out_pkt_too_bigs_per_s) AS icmp6_out_pkt_too_bigs_per_s_max,
       avg(icmp6_in_redirects_per_s) AS icmp6_in_redirects_per_s_avg,
       max(icmp6_in_redirects_per_s) AS icmp6_in_redirects_per_s_max,
       avg(icmp6_out_redirects_per_s) AS icmp6_out_redirects_per_s_avg,
       max(icmp6_out_redirects_per_s) AS icmp6_out_redirects_per_s_max,
       avg(icmp6_in_echos_per_s) AS icmp6_in_echos_per_s_avg,
       avg(icmp6_out_echos_per_s) AS icmp6_out_echos_per_s_avg,
       avg(icmp6_in_echo_replies_per_s) AS icmp6_in_echo_replies_per_s_avg,
       avg(icmp6_out_echo_replies_per_s) AS icmp6_out_echo_replies_per_s_avg,
       avg(icmp6_in_neighbor_solicits_per_s) AS icmp6_in_neighbor_solicits_per_s_avg,
       avg(icmp6_out_neighbor_solicits_per_s) AS icmp6_out_neighbor_solicits_per_s_avg,
       avg(icmp6_in_neighbor_advertisements_per_s) AS icmp6_in_neighbor_advertisements_per_s_avg,
       avg(icmp6_out_neighbor_advertisements_per_s) AS icmp6_out_neighbor_advertisements_per_s_avg,
       avg(icmp6_in_router_solicits_per_s) AS icmp6_in_router_solicits_per_s_avg,
       avg(icmp6_out_router_solicits_per_s) AS icmp6_out_router_solicits_per_s_avg,
       avg(icmp6_in_router_advertisements_per_s) AS icmp6_in_router_advertisements_per_s_avg,
       avg(icmp6_out_router_advertisements_per_s) AS icmp6_out_router_advertisements_per_s_avg
  FROM host_snmp_samples
 GROUP BY host_id, time_bucket(INTERVAL '5 minutes', ts)
WITH NO DATA;

SELECT set_chunk_time_interval('host_snmp_samples_5m', INTERVAL '2 days');

CREATE MATERIALIZED VIEW IF NOT EXISTS host_snmp_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(ip_in_receives_per_s_avg) AS ip_in_receives_per_s_avg,
       avg(ip_in_delivers_per_s_avg) AS ip_in_delivers_per_s_avg,
       avg(ip_out_requests_per_s_avg) AS ip_out_requests_per_s_avg,
       avg(ip_forw_datagrams_per_s_avg) AS ip_forw_datagrams_per_s_avg,
       avg(ip_reasm_oks_per_s_avg) AS ip_reasm_oks_per_s_avg,
       avg(ip_frag_oks_per_s_avg) AS ip_frag_oks_per_s_avg,
       avg(ip_in_hdr_errors_per_s_avg) AS ip_in_hdr_errors_per_s_avg,
       max(ip_in_hdr_errors_per_s_max) AS ip_in_hdr_errors_per_s_max,
       avg(ip_in_addr_errors_per_s_avg) AS ip_in_addr_errors_per_s_avg,
       max(ip_in_addr_errors_per_s_max) AS ip_in_addr_errors_per_s_max,
       avg(ip_in_unknown_protos_per_s_avg) AS ip_in_unknown_protos_per_s_avg,
       max(ip_in_unknown_protos_per_s_max) AS ip_in_unknown_protos_per_s_max,
       avg(ip_in_discards_per_s_avg) AS ip_in_discards_per_s_avg,
       max(ip_in_discards_per_s_max) AS ip_in_discards_per_s_max,
       avg(ip_out_discards_per_s_avg) AS ip_out_discards_per_s_avg,
       max(ip_out_discards_per_s_max) AS ip_out_discards_per_s_max,
       avg(ip_out_no_routes_per_s_avg) AS ip_out_no_routes_per_s_avg,
       max(ip_out_no_routes_per_s_max) AS ip_out_no_routes_per_s_max,
       avg(ip_reasm_timeout_per_s_avg) AS ip_reasm_timeout_per_s_avg,
       max(ip_reasm_timeout_per_s_max) AS ip_reasm_timeout_per_s_max,
       avg(ip6_in_receives_per_s_avg) AS ip6_in_receives_per_s_avg,
       avg(ip6_in_delivers_per_s_avg) AS ip6_in_delivers_per_s_avg,
       avg(ip6_out_requests_per_s_avg) AS ip6_out_requests_per_s_avg,
       avg(ip6_out_forw_datagrams_per_s_avg) AS ip6_out_forw_datagrams_per_s_avg,
       avg(ip6_reasm_oks_per_s_avg) AS ip6_reasm_oks_per_s_avg,
       avg(ip6_frag_oks_per_s_avg) AS ip6_frag_oks_per_s_avg,
       avg(ip6_in_hdr_errors_per_s_avg) AS ip6_in_hdr_errors_per_s_avg,
       max(ip6_in_hdr_errors_per_s_max) AS ip6_in_hdr_errors_per_s_max,
       avg(ip6_in_addr_errors_per_s_avg) AS ip6_in_addr_errors_per_s_avg,
       max(ip6_in_addr_errors_per_s_max) AS ip6_in_addr_errors_per_s_max,
       avg(ip6_in_unknown_protos_per_s_avg) AS ip6_in_unknown_protos_per_s_avg,
       max(ip6_in_unknown_protos_per_s_max) AS ip6_in_unknown_protos_per_s_max,
       avg(ip6_in_discards_per_s_avg) AS ip6_in_discards_per_s_avg,
       max(ip6_in_discards_per_s_max) AS ip6_in_discards_per_s_max,
       avg(ip6_out_discards_per_s_avg) AS ip6_out_discards_per_s_avg,
       max(ip6_out_discards_per_s_max) AS ip6_out_discards_per_s_max,
       avg(ip6_out_no_routes_per_s_avg) AS ip6_out_no_routes_per_s_avg,
       max(ip6_out_no_routes_per_s_max) AS ip6_out_no_routes_per_s_max,
       avg(ip6_in_no_routes_per_s_avg) AS ip6_in_no_routes_per_s_avg,
       max(ip6_in_no_routes_per_s_max) AS ip6_in_no_routes_per_s_max,
       avg(ip6_in_too_big_errors_per_s_avg) AS ip6_in_too_big_errors_per_s_avg,
       max(ip6_in_too_big_errors_per_s_max) AS ip6_in_too_big_errors_per_s_max,
       avg(ip6_reasm_timeout_per_s_avg) AS ip6_reasm_timeout_per_s_avg,
       max(ip6_reasm_timeout_per_s_max) AS ip6_reasm_timeout_per_s_max,
       avg(icmp_in_msgs_per_s_avg) AS icmp_in_msgs_per_s_avg,
       avg(icmp_out_msgs_per_s_avg) AS icmp_out_msgs_per_s_avg,
       avg(icmp_in_errors_per_s_avg) AS icmp_in_errors_per_s_avg,
       max(icmp_in_errors_per_s_max) AS icmp_in_errors_per_s_max,
       avg(icmp_out_errors_per_s_avg) AS icmp_out_errors_per_s_avg,
       max(icmp_out_errors_per_s_max) AS icmp_out_errors_per_s_max,
       avg(icmp_in_dest_unreachs_per_s_avg) AS icmp_in_dest_unreachs_per_s_avg,
       max(icmp_in_dest_unreachs_per_s_max) AS icmp_in_dest_unreachs_per_s_max,
       avg(icmp_out_dest_unreachs_per_s_avg) AS icmp_out_dest_unreachs_per_s_avg,
       max(icmp_out_dest_unreachs_per_s_max) AS icmp_out_dest_unreachs_per_s_max,
       avg(icmp_in_time_excds_per_s_avg) AS icmp_in_time_excds_per_s_avg,
       max(icmp_in_time_excds_per_s_max) AS icmp_in_time_excds_per_s_max,
       avg(icmp_out_time_excds_per_s_avg) AS icmp_out_time_excds_per_s_avg,
       max(icmp_out_time_excds_per_s_max) AS icmp_out_time_excds_per_s_max,
       avg(icmp_in_parm_probs_per_s_avg) AS icmp_in_parm_probs_per_s_avg,
       max(icmp_in_parm_probs_per_s_max) AS icmp_in_parm_probs_per_s_max,
       avg(icmp_out_parm_probs_per_s_avg) AS icmp_out_parm_probs_per_s_avg,
       max(icmp_out_parm_probs_per_s_max) AS icmp_out_parm_probs_per_s_max,
       avg(icmp_in_redirects_per_s_avg) AS icmp_in_redirects_per_s_avg,
       max(icmp_in_redirects_per_s_max) AS icmp_in_redirects_per_s_max,
       avg(icmp_out_redirects_per_s_avg) AS icmp_out_redirects_per_s_avg,
       max(icmp_out_redirects_per_s_max) AS icmp_out_redirects_per_s_max,
       avg(icmp_in_echos_per_s_avg) AS icmp_in_echos_per_s_avg,
       avg(icmp_out_echos_per_s_avg) AS icmp_out_echos_per_s_avg,
       avg(icmp_in_echo_reps_per_s_avg) AS icmp_in_echo_reps_per_s_avg,
       avg(icmp_out_echo_reps_per_s_avg) AS icmp_out_echo_reps_per_s_avg,
       avg(icmp6_in_msgs_per_s_avg) AS icmp6_in_msgs_per_s_avg,
       avg(icmp6_out_msgs_per_s_avg) AS icmp6_out_msgs_per_s_avg,
       avg(icmp6_in_errors_per_s_avg) AS icmp6_in_errors_per_s_avg,
       max(icmp6_in_errors_per_s_max) AS icmp6_in_errors_per_s_max,
       avg(icmp6_out_errors_per_s_avg) AS icmp6_out_errors_per_s_avg,
       max(icmp6_out_errors_per_s_max) AS icmp6_out_errors_per_s_max,
       avg(icmp6_in_dest_unreachs_per_s_avg) AS icmp6_in_dest_unreachs_per_s_avg,
       max(icmp6_in_dest_unreachs_per_s_max) AS icmp6_in_dest_unreachs_per_s_max,
       avg(icmp6_out_dest_unreachs_per_s_avg) AS icmp6_out_dest_unreachs_per_s_avg,
       max(icmp6_out_dest_unreachs_per_s_max) AS icmp6_out_dest_unreachs_per_s_max,
       avg(icmp6_in_time_excds_per_s_avg) AS icmp6_in_time_excds_per_s_avg,
       max(icmp6_in_time_excds_per_s_max) AS icmp6_in_time_excds_per_s_max,
       avg(icmp6_out_time_excds_per_s_avg) AS icmp6_out_time_excds_per_s_avg,
       max(icmp6_out_time_excds_per_s_max) AS icmp6_out_time_excds_per_s_max,
       avg(icmp6_in_parm_problems_per_s_avg) AS icmp6_in_parm_problems_per_s_avg,
       max(icmp6_in_parm_problems_per_s_max) AS icmp6_in_parm_problems_per_s_max,
       avg(icmp6_out_parm_problems_per_s_avg) AS icmp6_out_parm_problems_per_s_avg,
       max(icmp6_out_parm_problems_per_s_max) AS icmp6_out_parm_problems_per_s_max,
       avg(icmp6_in_pkt_too_bigs_per_s_avg) AS icmp6_in_pkt_too_bigs_per_s_avg,
       max(icmp6_in_pkt_too_bigs_per_s_max) AS icmp6_in_pkt_too_bigs_per_s_max,
       avg(icmp6_out_pkt_too_bigs_per_s_avg) AS icmp6_out_pkt_too_bigs_per_s_avg,
       max(icmp6_out_pkt_too_bigs_per_s_max) AS icmp6_out_pkt_too_bigs_per_s_max,
       avg(icmp6_in_redirects_per_s_avg) AS icmp6_in_redirects_per_s_avg,
       max(icmp6_in_redirects_per_s_max) AS icmp6_in_redirects_per_s_max,
       avg(icmp6_out_redirects_per_s_avg) AS icmp6_out_redirects_per_s_avg,
       max(icmp6_out_redirects_per_s_max) AS icmp6_out_redirects_per_s_max,
       avg(icmp6_in_echos_per_s_avg) AS icmp6_in_echos_per_s_avg,
       avg(icmp6_out_echos_per_s_avg) AS icmp6_out_echos_per_s_avg,
       avg(icmp6_in_echo_replies_per_s_avg) AS icmp6_in_echo_replies_per_s_avg,
       avg(icmp6_out_echo_replies_per_s_avg) AS icmp6_out_echo_replies_per_s_avg,
       avg(icmp6_in_neighbor_solicits_per_s_avg) AS icmp6_in_neighbor_solicits_per_s_avg,
       avg(icmp6_out_neighbor_solicits_per_s_avg) AS icmp6_out_neighbor_solicits_per_s_avg,
       avg(icmp6_in_neighbor_advertisements_per_s_avg) AS icmp6_in_neighbor_advertisements_per_s_avg,
       avg(icmp6_out_neighbor_advertisements_per_s_avg) AS icmp6_out_neighbor_advertisements_per_s_avg,
       avg(icmp6_in_router_solicits_per_s_avg) AS icmp6_in_router_solicits_per_s_avg,
       avg(icmp6_out_router_solicits_per_s_avg) AS icmp6_out_router_solicits_per_s_avg,
       avg(icmp6_in_router_advertisements_per_s_avg) AS icmp6_in_router_advertisements_per_s_avg,
       avg(icmp6_out_router_advertisements_per_s_avg) AS icmp6_out_router_advertisements_per_s_avg
  FROM host_snmp_samples_5m
 GROUP BY host_id, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('host_snmp_samples_1h', INTERVAL '7 days');

-- Identical to host_samples, and not by taste: registering the family with
-- rolledUpTiers makes TestIntegrationTierSpecsMatchTheSchema pin every one of
-- these against the shared rawTier/fiveMinuteTier/hourlyTier constants in
-- internal/hub/read/tier.go. A deviation would need a fourth tierSpec triple
-- for no benefit.
SELECT add_continuous_aggregate_policy('host_snmp_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('host_snmp_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

-- Raw retention must exceed the refresh lag, or chunks are dropped before
-- being materialised into the 5m tier.
SELECT add_retention_policy('host_snmp_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('host_snmp_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('host_snmp_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);
