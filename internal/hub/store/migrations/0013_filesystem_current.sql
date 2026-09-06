-- The last reading netra has for one filesystem, as a gauge rather than as
-- history.
--
-- A host that is not permanently up -- a NAS switched off overnight -- lost its
-- Disk cell entirely while it was down, chart and reading both. The chart is a
-- separate bug in the UI; this table is about the reading.
--
-- Disk fullness is the one saturation metric that stays TRUE across an outage.
-- A frozen CPU percentage is a lie the moment the machine stops; a frozen
-- "87% full" is simply the last thing anybody measured, and it is still what
-- the array will read when it comes back. The fleet cell nevertheless went
-- blank, because it takes its figure from the last slot of the ANSWERED
-- WINDOW and the fleet page is pinned to 24h -- so a host off since Friday has
-- no bucket to read at all.
--
-- host_current, in 0001, is this argument one dimension up, and its comment is
-- the long form: a gauge read off the grid changes MEANING with the range,
-- because the range picks the step, the step picks the tier, and the tier
-- picks both the column and how stale the trailing edge is. Everything that
-- comment says about mem_used and the traffic pair holds here, plus one thing
-- it never had to face: a filesystem's series is subject to the retention
-- policies at the bottom of 0001, and this reading must outlive all of them.
--
-- So: no hypertable, no retention policy, no window. One row per (host,
-- filesystem), replaced in place.

CREATE TABLE IF NOT EXISTS filesystem_current (
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    fs_id   INTEGER NOT NULL,

    -- The READING's own timestamp, never now(). Two things depend on it.
    --
    -- The UI's retired-mount rule is the first: `filesystems` is never pruned,
    -- so a mount that stopped being reported keeps its row forever, and
    -- without a date on the reading a mount frozen at 94% outranks every live
    -- disk on the host and names itself in the fleet cell. Comparing this
    -- against the host's own last_seen separates "this host is off, so every
    -- mount's reading is legitimately its last" from "this host is talking and
    -- this one mount is not, so drop it".
    --
    -- The upsert's own guard is the second -- see the WHERE clause in
    -- upsertFilesystemCurrent. An agent ring buffer replays hours-old scrapes
    -- after a hub outage, and a gauge that took whichever row landed last
    -- would walk backwards.
    ts      TIMESTAMPTZ NOT NULL,

    -- As measured, in bytes. used + free is NOT total: the gap is the root
    -- reserve, which is neither in use nor allocatable. filesystem_samples in
    -- 0001 carries the full argument and the Use% rule that follows from it.
    --
    -- Nullable with no default, on 0001's rule for every reading in this
    -- schema: a filesystem the agent could not measure has no value, and NULL
    -- is exactly that. A DEFAULT 0 would report an empty disk.
    total   BIGINT,
    used    BIGINT,
    free    BIGINT,

    PRIMARY KEY (host_id, fs_id),
    FOREIGN KEY (fs_id, host_id) REFERENCES filesystems (id, host_id) ON DELETE CASCADE
);

-- Seed from the history that is still on disk, so a host ALREADY offline when
-- this ships is not blank until it next reports -- which for the machine that
-- prompted this table may be days.
--
-- Bounded by the 7-day retention on filesystem_samples, and no index serves
-- it: filesystem_samples_fs_id_host_id_idx is (fs_id, host_id) with no ts,
-- so this is a scan and a sort of that whole retention window inside the
-- migration's transaction. Accepted rather than indexed around, because an
-- index built for one statement that never runs again costs the same scan
-- and then stays on a hypertable forever. It is a one-off at upgrade, on a
-- table whose per-host row count is mounts x scrapes.
--
-- DO NOTHING rather than DO UPDATE
-- because a live agent's write is newer than anything here: on a re-run this
-- statement must never overwrite the gauge with history.
INSERT INTO filesystem_current (host_id, fs_id, ts, total, used, free)
SELECT DISTINCT ON (host_id, fs_id) host_id, fs_id, ts, total, used, free
  FROM filesystem_samples
 ORDER BY host_id, fs_id, ts DESC
ON CONFLICT (host_id, fs_id) DO NOTHING;
