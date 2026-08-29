-- netra:no-transaction
--
-- Matching 0001 and 0003: TimescaleDB refuses to create a continuous
-- aggregate inside a transaction block, and CALL refresh_continuous_aggregate
-- refuses outright. Every statement below is IF NOT EXISTS or
-- if_not_exists => TRUE, so a partial application self-heals on the next
-- start rather than needing a hand-written repair.
--
-- A DAILY tier, so a chart can be asked about a year.
--
-- The ladder stopped at the 1h tier's 90 days, which meant the widest window
-- anything could answer was three months -- and the 1h tier answers it with
-- 2160 points, which is a chart nobody can read and a response nobody wants
-- to send. The question an operator actually asks over a year is "was this
-- disk always this full", and a day is the right bucket for it: twelve months
-- is 365 points, the same order as the 30d chart already draws.
--
-- Storage is not the reason it did not exist and is not an argument against
-- it: a day holds 1/24 of the rows an hour does, so 400 days of daily is
-- cheaper per family than the 90 days of hourly already kept.
--
-- The finer tiers keep the retention they had. 1h stays at 90 days even
-- though 1d now serves anything past 30d, because it is what a request for a
-- window BETWEEN 30 and 90 days resolves to when it names an hourly step,
-- and dropping it would trade a real answer for a small saving.
--
-- Each view is the family's _1h view with the bucket widened, read from _1h
-- rather than _5m. The column names are identical at every rolled-up tier by
-- construction -- avg(x_avg) AS x_avg -- which is what lets read/columns.go
-- discover them from information_schema and know nothing about this file.
--
-- start_offset is 7 days: the 1h policy reaches back 12 hours, so a day's
-- bucket is settled long before this stops revisiting it. end_offset is
-- 2 hours, which with the hourly schedule puts materialisedThrough() three
-- hours back -- truncated to a day, that is "every whole day up to today",
-- which is exactly what a daily tier can honestly answer.
--
-- The CALL after each view backfills it from the 1h tier's 90 days in one
-- go. Without it the widest tiles would ship empty and fill in at one day
-- per day. It is the slow part of this migration on a hub with history.

-- host_samples: one row per day, rolled from host_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS host_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
       avg(cpu_total_avg) AS cpu_total_avg,
       max(cpu_total_max) AS cpu_total_max,
       avg(mem_used_avg)  AS mem_used_avg,
       max(mem_used_max)  AS mem_used_max,
       avg(swap_used_avg) AS swap_used_avg,
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
  FROM host_samples_1h
 GROUP BY host_id, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('host_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('host_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('host_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('host_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);


-- agent_samples: one row per day, rolled from agent_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS agent_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
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
  FROM agent_samples_1h
 GROUP BY host_id, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('agent_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('agent_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('agent_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('agent_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);


-- cpu_core_samples: one row per day, rolled from cpu_core_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS cpu_core_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       core,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
       avg(busy_avg) AS busy_avg,
       max(busy_max) AS busy_max
  FROM cpu_core_samples_1h
 GROUP BY host_id, core, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('cpu_core_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('cpu_core_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('cpu_core_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('cpu_core_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);


-- disk_io_samples: one row per day, rolled from disk_io_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS disk_io_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       device,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
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
  FROM disk_io_samples_1h
 GROUP BY host_id, device, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('disk_io_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('disk_io_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('disk_io_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('disk_io_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);


-- sensor_samples: one row per day, rolled from sensor_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS sensor_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       sensor_id,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
       avg(temp_avg) AS temp_avg,
       max(temp_max) AS temp_max,
       avg(value_avg) AS value_avg,
       max(value_max) AS value_max,
       min(value_min) AS value_min
  FROM sensor_samples_1h
 GROUP BY host_id, sensor_id, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('sensor_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('sensor_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('sensor_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('sensor_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);


-- net_samples: one row per day, rolled from net_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS net_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       iface,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
       avg(rx_bytes_avg) AS rx_bytes_avg,
       max(rx_bytes_max) AS rx_bytes_max,
       avg(tx_bytes_avg) AS tx_bytes_avg,
       max(tx_bytes_max) AS tx_bytes_max,
       avg(rx_errs_avg)  AS rx_errs_avg,
       max(rx_errs_max)  AS rx_errs_max,
       avg(tx_errs_avg)  AS tx_errs_avg,
       max(tx_errs_max)  AS tx_errs_max
  FROM net_samples_1h
 GROUP BY host_id, iface, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('net_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('net_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('net_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('net_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);


-- collector_samples: one row per day, rolled from collector_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS collector_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       collector,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
       avg(duration_ms_avg)     AS duration_ms_avg,
       max(duration_ms_max)     AS duration_ms_max,
       sum(sample_count)::BIGINT  AS sample_count,
       sum(failure_count)::BIGINT AS failure_count,
       last(error_code, bucket) AS error_code
  FROM collector_samples_1h
 GROUP BY host_id, collector, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('collector_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('collector_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('collector_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('collector_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);


-- container_samples: one row per day, rolled from container_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS container_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       container_id,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
       avg(cpu_pct_avg)  AS cpu_pct_avg,
       max(cpu_pct_max)  AS cpu_pct_max,
       avg(mem_used_avg) AS mem_used_avg,
       max(mem_used_max) AS mem_used_max,
       max(mem_limit_max) AS mem_limit_max,
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
  FROM container_samples_1h
 GROUP BY host_id, container_id, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('container_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('container_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('container_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('container_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);


-- filesystem_samples: one row per day, rolled from filesystem_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS filesystem_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       fs_id,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
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
  FROM filesystem_samples_1h
 GROUP BY host_id, fs_id, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('filesystem_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('filesystem_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('filesystem_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('filesystem_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);


-- host_snmp_samples: one row per day, rolled from host_snmp_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS host_snmp_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
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
  FROM host_snmp_samples_1h
 GROUP BY host_id, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('host_snmp_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('host_snmp_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('host_snmp_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('host_snmp_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);


-- host_proto_samples: one row per day, rolled from host_proto_samples_1h.
CREATE MATERIALIZED VIEW IF NOT EXISTS host_proto_samples_1d
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '1 day', bucket) AS bucket,
       avg(tcp_in_segs_per_s_avg) AS tcp_in_segs_per_s_avg,
       avg(tcp_out_segs_per_s_avg) AS tcp_out_segs_per_s_avg,
       avg(tcp_estab_resets_per_s_avg) AS tcp_estab_resets_per_s_avg,
       max(tcp_estab_resets_per_s_max) AS tcp_estab_resets_per_s_max,
       avg(udp_in_datagrams_per_s_avg) AS udp_in_datagrams_per_s_avg,
       avg(udp_out_datagrams_per_s_avg) AS udp_out_datagrams_per_s_avg,
       avg(udp6_in_datagrams_per_s_avg) AS udp6_in_datagrams_per_s_avg,
       avg(udp6_out_datagrams_per_s_avg) AS udp6_out_datagrams_per_s_avg
  FROM host_proto_samples_1h
 GROUP BY host_id, time_bucket(INTERVAL '1 day', bucket)
WITH NO DATA;

SELECT set_chunk_time_interval('host_proto_samples_1d', INTERVAL '90 days');

CALL refresh_continuous_aggregate('host_proto_samples_1d', NULL, NULL);

SELECT add_continuous_aggregate_policy('host_proto_samples_1d',
    start_offset      => INTERVAL '7 days',
    end_offset        => INTERVAL '2 hours',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists     => TRUE);

SELECT add_retention_policy('host_proto_samples_1d', INTERVAL '400 days', if_not_exists => TRUE);
