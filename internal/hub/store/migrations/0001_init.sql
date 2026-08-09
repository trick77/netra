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
       max(post_failures_total)  AS post_failures_total
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
       max(post_failures_total)    AS post_failures_total
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
    label   TEXT NOT NULL
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
    temp      DOUBLE PRECISION,
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
       max(temp) AS temp_max
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
       max(temp_max) AS temp_max
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
