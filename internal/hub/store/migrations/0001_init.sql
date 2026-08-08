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
    uptime_s  BIGINT
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

    -- systemd summary (spec 5.3). NULL until the systemd collector lands.
    services_total  INTEGER,
    services_failed INTEGER,

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

CREATE MATERIALIZED VIEW IF NOT EXISTS host_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(cpu_total)  AS cpu_total_avg,
       max(cpu_total)  AS cpu_total_max,
       avg(mem_used)   AS mem_used_avg,
       max(mem_used)   AS mem_used_max,
       avg(swap_used)  AS swap_used_avg,
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
       avg(ip6_frag_creates_per_s) AS ip6_frag_creates_per_s_avg
  FROM host_samples
 GROUP BY host_id, bucket
WITH NO DATA;

CREATE MATERIALIZED VIEW IF NOT EXISTS host_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(cpu_total_avg) AS cpu_total_avg,
       max(cpu_total_max) AS cpu_total_max,
       avg(mem_used_avg)  AS mem_used_avg,
       max(mem_used_max)  AS mem_used_max,
       avg(swap_used_avg) AS swap_used_avg,
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
       avg(ip6_frag_creates_per_s_avg) AS ip6_frag_creates_per_s_avg
  FROM host_samples_5m
 GROUP BY host_id, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

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
    PRIMARY KEY (host_id, ts)
);

SELECT create_hypertable('agent_samples', by_range('ts'), if_not_exists => TRUE);

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
       max(post_failures_total)  AS post_failures_total
  FROM agent_samples
 GROUP BY host_id, bucket
WITH NO DATA;

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
       max(post_failures_total)    AS post_failures_total
  FROM agent_samples_5m
 GROUP BY host_id, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

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
