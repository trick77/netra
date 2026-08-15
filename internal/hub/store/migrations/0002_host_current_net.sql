-- The fleet's traffic figure, as a scalar rather than a series lookup.
--
-- "Fleet traffic -- ingress + egress" is a RATE, bytes per second, and it
-- read differently depending on which window the sparklines beside it were
-- drawn over. Not because anything computed it wrong: the UI takes the last
-- point of the answered series, and which quantity that point holds is
-- decided by the range. The range picks the step (lib/range.ts), the step
-- picks the tier (selectTier in internal/hub/read/tier.go), and the tier
-- decides both the column and how fresh the trailing edge is:
--
--   1h  -> step 60s -> raw tier -> rx_bytes,     window ends at now
--   6h  -> step 5m  -> 5m tier  -> rx_bytes_avg, window ends ~15 min ago
--   24h -> step 5m  -> 5m tier  -> rx_bytes_avg, window ends ~15 min ago
--
-- So "latest sample" was true only at 1h; at 6h and 24h it was a five-minute
-- mean that had already ended a quarter of an hour earlier. A current rate
-- must not depend on how far back somebody is looking, so it stops being
-- read off the grid at all.
--
-- host_current is where the fleet list already reads its other current
-- gauges from, for the same reason: one row per host, no hypertable, no
-- window. These two columns join them.
--
-- Nullable, with no default. A host that has not posted since this migration
-- ran has no value yet, and NULL is exactly that -- the UI already renders an
-- absent marker for it. A DEFAULT 0 would claim a silent host is moving no
-- traffic, which is the inference every absent/zero distinction in this
-- schema exists to avoid.
--
-- The sum excludes loopback and bridges because the AGENT excludes them
-- (internal/agent/collector/network.go): lo and docker0 never reach the hub,
-- so there is nothing to filter here.

ALTER TABLE host_current
    ADD COLUMN IF NOT EXISTS net_rx_bytes DOUBLE PRECISION;

ALTER TABLE host_current
    ADD COLUMN IF NOT EXISTS net_tx_bytes DOUBLE PRECISION;
