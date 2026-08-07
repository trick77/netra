CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE providers (
    id   INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE sites (
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

CREATE TABLE hosts (
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

CREATE TABLE tokens (
    id           INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id      INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    token_hash   BYTEA NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);

CREATE TABLE host_current (
    host_id   INTEGER PRIMARY KEY REFERENCES hosts (id) ON DELETE CASCADE,
    last_seen TIMESTAMPTZ,
    cpu_total DOUBLE PRECISION,
    mem_used  BIGINT,
    mem_total BIGINT,
    uptime_s  BIGINT
);
