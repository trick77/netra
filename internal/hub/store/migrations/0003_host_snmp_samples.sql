-- netra:no-transaction
--
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
