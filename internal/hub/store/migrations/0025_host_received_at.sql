-- When the hub last received a post from the host, on the hub's clock.
--
-- last_seen is the sample's own ts, from the agent's clock, and judging
-- silence on it against the hub's now() called a host with a clock a few
-- minutes slow silent while its data arrived every minute. Silence is judged
-- on this; last_seen stays for display.
--
-- Backfilled from last_seen so no host changes state at deploy. The default
-- stamps the row an ingest first inserts. NOT NULL because the silent scan
-- reads it for every host with a row.
ALTER TABLE host_current ADD COLUMN IF NOT EXISTS received_at TIMESTAMPTZ;
UPDATE host_current SET received_at = coalesce(last_seen, now()) WHERE received_at IS NULL;
ALTER TABLE host_current ALTER COLUMN received_at SET DEFAULT now(),
                         ALTER COLUMN received_at SET NOT NULL;
