-- host_proto_samples: the TCP and UDP volume counters, and host_interfaces:
-- the links the addresses sit on.
--
-- A THIRD host-level sample table rather than seven more columns on an
-- existing one, for exactly the reason host_snmp_samples gives for being the
-- second: a TimescaleDB continuous aggregate cannot gain a column. Widening
-- host_samples means dropping and recreating host_samples_5m and _1h, which
-- re-materialise only from the seven days of raw still on disk and destroy
-- every rolled-up host metric older than that -- CPU and memory included.
-- Widening host_snmp_samples costs the same, in the IP and ICMP history that
-- table exists to hold. A new table with its own aggregates costs one read
-- family and loses nothing.
--
-- Forward-only: 0001_init.sql is not touched.

CREATE TABLE IF NOT EXISTS host_proto_samples (
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts      TIMESTAMPTZ NOT NULL,

    -- /proc/net/snmp, Tcp:. The denominator the failure counters in
    -- host_samples never had -- tcp_retrans_segs_per_s alone cannot
    -- distinguish a host retransmitting 40 segments out of 400 from one
    -- retransmitting 40 out of 400,000.
    --
    -- No tcp6_* peers: Linux's Tcp: block counts both families together and
    -- /proc/net/snmp6 has no Tcp6 block at all.
    tcp_in_segs_per_s        DOUBLE PRECISION,
    tcp_out_segs_per_s       DOUBLE PRECISION,
    tcp_estab_resets_per_s   DOUBLE PRECISION,

    -- /proc/net/snmp Udp: and /proc/net/snmp6 Udp6:. UDP had no throughput
    -- measure at all before this -- only its four error counters.
    udp_in_datagrams_per_s   DOUBLE PRECISION,
    udp_out_datagrams_per_s  DOUBLE PRECISION,
    udp6_in_datagrams_per_s  DOUBLE PRECISION,
    udp6_out_datagrams_per_s DOUBLE PRECISION,

    -- Natural key, the same one host_samples and host_snmp_samples use.
    -- Replayed batches collide here and are discarded by ON CONFLICT DO
    -- NOTHING.
    PRIMARY KEY (host_id, ts)
);

SELECT create_hypertable('host_proto_samples', by_range('ts'), if_not_exists => TRUE);

-- One-day chunks, as the other two host tiers use: drop_chunks removes a chunk
-- only once its newest row is past the cutoff, so a coarser chunk turns
-- "7 days" into as much as 14.
SELECT set_chunk_time_interval('host_proto_samples', INTERVAL '1 day');

-- avg alone for the four volume counters, avg AND max for estab_resets: a
-- burst of resets inside a bucket is the reading, and averaging it into five
-- minutes is what would hide it. The same split the other two tables make
-- between their volume and their error columns.
CREATE MATERIALIZED VIEW IF NOT EXISTS host_proto_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(tcp_in_segs_per_s) AS tcp_in_segs_per_s_avg,
       avg(tcp_out_segs_per_s) AS tcp_out_segs_per_s_avg,
       avg(tcp_estab_resets_per_s) AS tcp_estab_resets_per_s_avg,
       max(tcp_estab_resets_per_s) AS tcp_estab_resets_per_s_max,
       avg(udp_in_datagrams_per_s) AS udp_in_datagrams_per_s_avg,
       avg(udp_out_datagrams_per_s) AS udp_out_datagrams_per_s_avg,
       avg(udp6_in_datagrams_per_s) AS udp6_in_datagrams_per_s_avg,
       avg(udp6_out_datagrams_per_s) AS udp6_out_datagrams_per_s_avg
  FROM host_proto_samples
 GROUP BY host_id, time_bucket(INTERVAL '5 minutes', ts)
WITH NO DATA;

SELECT set_chunk_time_interval('host_proto_samples_5m', INTERVAL '2 days');

-- The hourly tier rolls up the 5m one rather than the raw table: avg of avgs
-- over equal-width buckets is the mean, and max of maxes is the max.
CREATE MATERIALIZED VIEW IF NOT EXISTS host_proto_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(tcp_in_segs_per_s_avg) AS tcp_in_segs_per_s_avg,
       avg(tcp_out_segs_per_s_avg) AS tcp_out_segs_per_s_avg,
       avg(tcp_estab_resets_per_s_avg) AS tcp_estab_resets_per_s_avg,
       max(tcp_estab_resets_per_s_max) AS tcp_estab_resets_per_s_max,
       avg(udp_in_datagrams_per_s_avg) AS udp_in_datagrams_per_s_avg,
       avg(udp_out_datagrams_per_s_avg) AS udp_out_datagrams_per_s_avg,
       avg(udp6_in_datagrams_per_s_avg) AS udp6_in_datagrams_per_s_avg,
       avg(udp6_out_datagrams_per_s_avg) AS udp6_out_datagrams_per_s_avg
  FROM host_proto_samples_5m
 GROUP BY host_id, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('host_proto_samples_1h', INTERVAL '7 days');

-- Identical to host_samples and host_snmp_samples, and not by taste:
-- registering the family with rolledUpTiers makes
-- TestIntegrationTierSpecsMatchTheSchema pin every one of these against the
-- shared rawTier/fiveMinuteTier/hourlyTier constants in
-- internal/hub/read/tier.go.
SELECT add_continuous_aggregate_policy('host_proto_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes',
    if_not_exists     => TRUE);

SELECT add_continuous_aggregate_policy('host_proto_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes',
    if_not_exists     => TRUE);

-- Raw retention must exceed the refresh lag, or chunks are dropped before
-- being materialised into the 5m tier.
SELECT add_retention_policy('host_proto_samples',    INTERVAL '7 days',  if_not_exists => TRUE);
SELECT add_retention_policy('host_proto_samples_5m', INTERVAL '30 days', if_not_exists => TRUE);
SELECT add_retention_policy('host_proto_samples_1h', INTERVAL '90 days', if_not_exists => TRUE);


-- host_interfaces: one row per link, beside host_addresses' one row per
-- address.
--
-- A plain table, not a hypertable, for the same reason host_addresses is one:
-- an operator changes an MTU a handful of times a year, and a link that goes
-- down is an event rather than a series. The columns here are what
-- /sys/class/net/<iface>/ answers.
--
-- The key is (host_id, iface) rather than (host_id, iface, address): an
-- interface with NO address is precisely what this table exists to be able to
-- show -- a failed bond and an unplugged spare NIC both have none, and both
-- are invisible in an address-keyed table.
CREATE TABLE IF NOT EXISTS host_interfaces (
    host_id     INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    iface       TEXT NOT NULL,
    if_index    INTEGER,

    -- /sys/class/net/<iface>/operstate verbatim: up, down, unknown,
    -- lowerlayerdown, dormant, testing, notpresent. The kernel's own word,
    -- not a bool -- see HostInterface.oper_state in the proto for why the
    -- agent does not classify.
    oper_state  TEXT,

    -- NULL, not 0, wherever the kernel has no answer: a virtual device has no
    -- link speed and a down one refuses to report its.
    speed_mbps  BIGINT,
    duplex      TEXT,
    mtu         INTEGER,
    mac         TEXT,

    -- The interface alias. host_addresses.description carries the same string
    -- and keeps carrying it -- this is where it is read from now, since an
    -- address-keyed table repeated one alias once per address.
    description TEXT,

    first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (host_id, iface)
);
