-- devices is the one inventory table nothing prunes, and the Drives table is
-- what made that visible: a disk physically removed from a host keeps its row
-- for ever, and once its smart_attributes age out at 90 days the row renders
-- as a permanent "not read" drive nobody can account for.
--
-- Every other inventory table already prunes -- UpsertHostAddresses,
-- UpsertHostInterfaces and UpsertHostPackages each delete what the newest
-- report omits, because a thing the host no longer has is not inventory.
--
-- devices CANNOT PRUNE THAT WAY, and the difference matters. smart_attributes
-- references it ON DELETE CASCADE, so removing a device row destroys that
-- drive's entire SMART history -- and the collector drops a single drive it
-- cannot read while reporting the others (`continue` in smart.go's device
-- loop). A set-difference prune would therefore let one unreadable drive on
-- one scrape delete every reading it had. That is a worse failure
-- than the stale row.
--
-- So: a timestamp, and a horizon far longer than any transient failure. A
-- device with no reading in 120 days is gone, and by then its readings have
-- aged out under smart_attributes' own retention anyway -- the row is deleted
-- at the point where it has stopped being able to say anything.
--
-- "No reading", precisely, and NOT "the agent stopped mentioning it": the
-- collector `continue`s past a drive whose --all fails, before appending any
-- row (smart.go's device loop), and the wire carries no device-present-but-
-- unreadable message. So a drive that --scan still finds while --all has
-- started failing -- a dying controller, a passthrough removed from a
-- container -- stops being reported at all, and this prune eventually removes
-- it along with whatever history it has left.
--
-- That is a real gap and it is stated here rather than papered over: netra
-- cannot currently distinguish "this disk was unplugged" from "this disk has
-- stopped answering", and the second is the more interesting of the two.
-- Closing it needs the collector to emit something for a drive it can name and
-- cannot read, which is an agent and wire change rather than a schema one.

ALTER TABLE devices ADD COLUMN IF NOT EXISTS first_seen TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_seen  TIMESTAMPTZ NOT NULL DEFAULT now();

-- Existing rows get now() from the DEFAULT, which dates them to the migration
-- rather than to when the drive was first seen. That is the safe direction:
-- it grants every current device a full horizon before it can be pruned, so
-- an upgrade cannot delete a live drive's history on its first night.
--
-- THE TWO COLUMNS ARE ON DIFFERENT CLOCKS, deliberately.
--
-- last_seen is the agent's: the ts of the newest reading, so the Drives table
-- can say when a drive was actually read rather than when the row happened to
-- land. first_seen is the HUB's, from this DEFAULT, and stays that way -- it
-- is what the prune uses as a floor.
--
-- That floor is load-bearing. The hub accepts any ts inside
-- [2020-01-01, now+1h] (minPlausibleTs, httpapi/ingest.go), so a host whose
-- clock is months behind -- an RTC-less box before NTP settles, a restored
-- snapshot -- reports readings stamped long before the cutoff. Keyed on
-- last_seen alone, that host's drive would be inserted already stale, deleted
-- by the next nightly run along with its readings, re-created by the next
-- scrape and deleted again, for ever. GREATEST does not help: it protects a
-- row that already holds a newer value, and a first-seen row holds nothing.
--
-- first_seen is a hub timestamp and cannot be moved by a bad agent clock, so
-- requiring BOTH past the cutoff means a drive always gets a full horizon of
-- real elapsed time before anything deletes it.
--
-- first_seen is NOT exposed by the read API. It is a hub-side guard, and
-- shipping it beside a last_seen on the other clock would put a pair on the
-- wire that can read first_seen > last_seen for a replayed batch.

-- The prune reads both columns, so both are indexed together.
CREATE INDEX IF NOT EXISTS devices_seen_idx ON devices (last_seen, first_seen);

-- A separate procedure and job from netra_prune_discrete_events, which is
-- named for event logs and prunes on a horizon that means something different
-- there: an event is kept as long as the metric series it explains, while a
-- device is kept until its own readings have aged out. Two concepts, two
-- knobs -- alter_job can retune either without touching the other.
CREATE OR REPLACE PROCEDURE netra_prune_stale_devices(job_id INTEGER, config JSONB)
LANGUAGE plpgsql AS $$
DECLARE
    horizon INTERVAL := coalesce((config ->> 'retention')::INTERVAL, INTERVAL '120 days');
    cutoff  TIMESTAMPTZ := now() - horizon;
BEGIN
    -- The CASCADE takes the drive's remaining smart_attributes with it, and
    -- the horizon is set so there are none left to take.
    --
    -- 120 days, not 90, and the extra month is the point. smart_attributes
    -- retains 90 days, but add_retention_policy drops a CHUNK only once its
    -- whole range is past the horizon, and the chunks are 7 days wide -- so
    -- readings survive to about 97 days, not 90. A device prune firing at 90
    -- would cascade away up to a week of readings the retention policy had
    -- not reached yet. Past 120 days there is nothing left for the cascade to
    -- destroy.
    --
    -- Both horizons are read from their jobs' config, so alter_job can move
    -- them. Moving THIS one below smart_attributes' effective retention is
    -- what makes the cascade destructive, and is why the gap is stated here
    -- rather than left to be rediscovered.
    --
    -- Both columns, not just last_seen: see the note above the ALTER TABLEs
    -- for why a hub-clocked floor is what keeps a host with a bad clock from
    -- having its drives deleted and re-created for ever.
    DELETE FROM devices WHERE last_seen < cutoff AND first_seen < cutoff;
    COMMIT;
END;
$$;

-- add_job has no if_not_exists, and this migration must stay re-runnable --
-- Migrate replays a file whose schema_migrations row is missing. Same DO-block
-- guard 0001 uses for the same reason.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM timescaledb_information.jobs
         WHERE proc_name = 'netra_prune_stale_devices'
    ) THEN
        PERFORM add_job('netra_prune_stale_devices', INTERVAL '1 day',
                        config => '{"retention": "120 days"}'::jsonb);
    END IF;
END;
$$;
