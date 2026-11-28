-- Where each md array stands now, from the agent's MdSnapshot.
--
-- A plain table with one row per array and no history, like
-- filesystem_current. The mdraid EVENTS keep the transitions; this keeps the
-- level, which the events cannot: a check moves its progress every scrape,
-- and an event or a hypertable row per scrape is weeks of "clean" to say
-- "a check is 70% through" once a month.
--
-- sync_* and mismatch_cnt are NULL while the kernel says "none" (no sync
-- running). members is jsonb because nothing queries a member, the page only
-- lists them: [{"device":"sdd1","state":"in_sync","slot":1}, ...].
CREATE TABLE IF NOT EXISTS md_arrays (
    host_id        INTEGER     NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    name           TEXT        NOT NULL,
    ts             TIMESTAMPTZ NOT NULL,
    level          TEXT        NOT NULL,
    raid_disks     INTEGER     NOT NULL,
    degraded       INTEGER     NOT NULL,
    state          TEXT        NOT NULL,
    sync_action    TEXT        NOT NULL,
    sync_done      BIGINT,
    sync_total     BIGINT,
    sync_speed_kib BIGINT,
    mismatch_cnt   BIGINT,
    members        JSONB       NOT NULL DEFAULT '[]',
    PRIMARY KEY (host_id, name)
);
