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
       last(uptime_s, ts) AS uptime_s
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
       last(uptime_s, bucket) AS uptime_s
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
