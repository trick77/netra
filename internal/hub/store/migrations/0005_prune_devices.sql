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
-- one scrape delete ninety days of readings for it. That is a worse failure
-- than the stale row.
--
-- So: a timestamp, and a horizon far longer than any transient failure. A
-- device the agent has not mentioned in 90 days is gone, and by then its
-- readings have aged out under smart_attributes' own retention anyway -- the
-- row is deleted at the point where it has stopped being able to say anything.

ALTER TABLE devices ADD COLUMN IF NOT EXISTS first_seen TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_seen  TIMESTAMPTZ NOT NULL DEFAULT now();

-- Existing rows get now() from the DEFAULT, which dates them to the migration
-- rather than to when the drive was first seen. That is the safe direction:
-- it grants every current device a full horizon before it can be pruned, so
-- an upgrade cannot delete a live drive's history on its first night.

-- The prune reads this on every pass.
CREATE INDEX IF NOT EXISTS devices_last_seen_idx ON devices (last_seen);

-- A separate procedure and job from netra_prune_discrete_events, which is
-- named for event logs and prunes on a horizon that means something different
-- there: an event is kept as long as the metric series it explains, while a
-- device is kept as long as the agent keeps mentioning it. Two concepts, two
-- knobs -- alter_job can retune either without touching the other.
CREATE OR REPLACE PROCEDURE netra_prune_stale_devices(job_id INTEGER, config JSONB)
LANGUAGE plpgsql AS $$
DECLARE
    horizon INTERVAL := coalesce((config ->> 'retention')::INTERVAL, INTERVAL '90 days');
    cutoff  TIMESTAMPTZ := now() - horizon;
BEGIN
    -- The CASCADE takes the drive's remaining smart_attributes with it. At
    -- this cutoff there are none left to take: the table's own retention
    -- policy drops them at 90 days, so a device silent for that long has an
    -- empty history by definition.
    DELETE FROM devices WHERE last_seen < cutoff;
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
                        config => '{"retention": "90 days"}'::jsonb);
    END IF;
END;
$$;
