-- An event is a state CHANGE, and the events table did not enforce it.
--
-- systemd_unit_events has refused a re-sent state since it was written -- "a
-- repeat of the state already recorded is NOT a transition" -- but the generic
-- events table stored every row an agent sent. So the fleet log filled with
-- the same line minutes apart: `mdraid md3 clean - raid1, 2 devices`, over and
-- over, for an array that had simply been clean the whole time. Three paths
-- produce those repeats: the baseline every agent start emits, the re-arm
-- after a dropped scrape, and an agent older than #214, which reports the
-- kernel's superblock dirty bit flapping (`active` <-> `clean`) as a change.
--
-- The guard itself lives in the hub (store/event_state.go, InsertEvents). This
-- migration gives it the index it rides, and clears the repeats already
-- stored.

-- The lookup the guard makes on every ingest carrying a state event: the
-- newest row for one (host, type, subject). Without this it walks
-- events_host_id_ts_idx and filters, which grows with the host's whole
-- history.
CREATE INDEX IF NOT EXISTS events_host_type_subject_ts_idx
    ON events (host_id, type, subject, ts DESC);

-- The repeats already in the log.
--
-- Only mdraid, because it is the only state-shaped type events carries today:
-- the kmsg family (disk_error, ata_error, md_fail, fs_error, oom_kill, ...) is
-- occurrences, where two identical rows are two real facts and how often they
-- repeat is the reading.
--
-- Consecutive duplicates only. The FIRST row of each run is kept -- it is the
-- one that recorded the transition into that state -- and a genuine
-- clean -> degraded -> clean sequence keeps all three, because neighbours
-- differ. The key matches what mdraidStateKey builds in Go, healthy-state
-- normalization included, or the dirty-bit flap rows would survive the clean-up
-- the guard was written to stop.
WITH keyed AS (
    SELECT id,
           CASE WHEN detail ->> 'state' IN
                     ('clean', 'active', 'active-idle', 'write-pending', 'read-auto')
                THEN 'clean'
                ELSE detail ->> 'state'
           END                       AS state,
           detail ->> 'level'        AS level,
           detail ->> 'raid_disks'   AS raid_disks,
           detail ->> 'degraded'     AS degraded,
           detail ->> 'sync_action'  AS sync_action,
           host_id, subject, ts
      FROM events
     WHERE type = 'mdraid'
), runs AS (
    SELECT id,
           lag(ROW (state, level, raid_disks, degraded, sync_action))
               OVER (PARTITION BY host_id, subject ORDER BY ts) AS prev,
           ROW (state, level, raid_disks, degraded, sync_action) AS cur
      FROM keyed
)
DELETE FROM events e
 USING runs r
 WHERE e.id = r.id
   AND r.prev IS NOT DISTINCT FROM r.cur;
