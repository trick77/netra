-- Every continuous aggregate becomes a REAL-TIME aggregate.
--
-- The defect: a saturation on eth0 was invisible on the 24h chart and showed
-- up about fifteen minutes later. That number is not a coincidence. Every
-- aggregate here was materialized_only, which is the TimescaleDB default since
-- 2.13, so a query past the refresh policy's end_offset returned NOTHING
-- rather than falling through to the rows sitting behind it. read/tier.go had
-- to clamp the answered window to now - (end_offset + schedule_interval) to
-- avoid asking for buckets no run had written: 15 minutes at the 5m tier, 90
-- at the 1h tier, three hours at 1d. Only the 1h range, which reads the raw
-- table, was ever live.
--
-- The reference has no such step. Observium's rrdtool file is written at poll
-- time and the graph reads it on the next page load, which is what "it graphs
-- immediately" means and what this migration buys back.
--
-- With materialized_only = false the user-facing view becomes, in the
-- extension's own words (tsl/src/continuous_aggs/create.c):
--
--     SELECT ... FROM <materialisation hypertable> WHERE timecol < watermark
--     UNION ALL
--     SELECT ... FROM <source relation>            WHERE timecol >= watermark
--
-- so the un-materialised tail is computed on the fly and every tier reads to
-- the present. The tail is bounded by the policy's end_offset -- ten minutes
-- at 5m, an hour at 1h, two at 1d -- so it is a small scan, not a rebuild.
--
-- Set on EVERY level, not only _5m. The ladder is hierarchical: _1h reads
-- _5m and _1d reads _1h. A real-time cagg over a materialized_only child
-- would union a tail that was itself empty, so 7d and 30d would stay ninety
-- minutes behind. Hierarchical real-time aggregates carry one watermark per
-- level (tsl/src/continuous_aggs/planner.c, init_watermark_map: "The query
-- can contain multiple watermarks (i.e., two hierarchal real-time CAggs)"),
-- which is what makes the chain reach the raw rows.
--
-- Nothing here changes what is STORED. The materialisation hypertables, the
-- refresh policies and the retention policies are all untouched; this changes
-- only what the view returns for the window past the watermark.
--
-- No no-transaction marker: ALTER MATERIALIZED VIEW ... SET is a catalog
-- update and a view redefinition, not a refresh, so unlike CREATE MATERIALIZED
-- VIEW ... WITH (timescaledb.continuous) and CALL refresh_continuous_aggregate
-- it is happy inside a transaction block. That also makes the whole file one
-- atomic step: either every tier is live or none is, which is the property
-- read/tier.go's dropped clamp now depends on.

ALTER MATERIALIZED VIEW host_samples_5m       SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW host_samples_1h       SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW host_samples_1d       SET (timescaledb.materialized_only = false);

ALTER MATERIALIZED VIEW agent_samples_5m      SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW agent_samples_1h      SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW agent_samples_1d      SET (timescaledb.materialized_only = false);

ALTER MATERIALIZED VIEW cpu_core_samples_5m   SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW cpu_core_samples_1h   SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW cpu_core_samples_1d   SET (timescaledb.materialized_only = false);

ALTER MATERIALIZED VIEW disk_io_samples_5m    SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW disk_io_samples_1h    SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW disk_io_samples_1d    SET (timescaledb.materialized_only = false);

ALTER MATERIALIZED VIEW sensor_samples_5m     SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW sensor_samples_1h     SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW sensor_samples_1d     SET (timescaledb.materialized_only = false);

ALTER MATERIALIZED VIEW net_samples_5m        SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW net_samples_1h        SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW net_samples_1d        SET (timescaledb.materialized_only = false);

ALTER MATERIALIZED VIEW collector_samples_5m  SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW collector_samples_1h  SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW collector_samples_1d  SET (timescaledb.materialized_only = false);

ALTER MATERIALIZED VIEW container_samples_5m  SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW container_samples_1h  SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW container_samples_1d  SET (timescaledb.materialized_only = false);

ALTER MATERIALIZED VIEW filesystem_samples_5m SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW filesystem_samples_1h SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW filesystem_samples_1d SET (timescaledb.materialized_only = false);

ALTER MATERIALIZED VIEW host_snmp_samples_5m  SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW host_snmp_samples_1h  SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW host_snmp_samples_1d  SET (timescaledb.materialized_only = false);

ALTER MATERIALIZED VIEW host_proto_samples_5m SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW host_proto_samples_1h SET (timescaledb.materialized_only = false);
ALTER MATERIALIZED VIEW host_proto_samples_1d SET (timescaledb.materialized_only = false);
