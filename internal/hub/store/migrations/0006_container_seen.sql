-- containers was the second inventory table nothing prunes, and the reason is
-- the same one 0005 spells out for devices: container_samples references it
-- ON DELETE CASCADE, so the four-line set-difference prune that
-- UpsertHostAddresses, UpsertHostInterfaces and UpsertHostPackages each use --
-- delete what the newest report omits -- would destroy a container's entire
-- CPU and memory history the first time a scrape failed to mention it.
--
-- A container is mentioned only while it is RUNNING: the agent enumerates
-- cgroup v2 scopes, and a stopped container has none. So "the agent stopped
-- mentioning it" covers a container that was removed, one that is merely
-- stopped, and one whose host is having a bad minute. Deleting on the first of
-- those is not something this table can tell apart from the other two.
--
-- So the same shape as devices: a timestamp the UI can read, and a horizon far
-- longer than any transient absence. What an operator actually wants -- "this
-- one is gone, take it out of my table now" -- is a decision only a person can
-- make, and it is a DELETE endpoint on the API rather than a rule here.

ALTER TABLE containers ADD COLUMN IF NOT EXISTS first_seen TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE containers ADD COLUMN IF NOT EXISTS last_seen  TIMESTAMPTZ NOT NULL DEFAULT now();

-- Existing rows get now() from the DEFAULT, which dates them to the migration
-- rather than to when the container was first seen. That is the safe
-- direction: every current container gets a full horizon before it can be
-- pruned, so an upgrade cannot delete a live container's history on its first
-- night. It also means every pre-existing row reads as "last seen at the
-- upgrade" until its next scrape stamps it, which is one scrape.
--
-- THE TWO COLUMNS ARE ON DIFFERENT CLOCKS, deliberately, exactly as in 0005.
--
-- last_seen is the agent's: the ts of the newest sample, so the Containers
-- table can say when a container was actually seen rather than when the row
-- happened to land. first_seen is the HUB's, from this DEFAULT, and stays that
-- way -- it is what the prune uses as a floor.
--
-- That floor is load-bearing. The hub accepts any ts inside
-- [2020-01-01, now+1h] (minPlausibleTs, httpapi/ingest.go), so a host whose
-- clock is months behind -- an RTC-less box before NTP settles, a restored
-- snapshot -- reports samples stamped long before the cutoff. Keyed on
-- last_seen alone, that host's containers would be inserted already stale,
-- deleted by the next nightly run along with their samples, re-created by the
-- next scrape and deleted again, for ever.
--
-- first_seen is NOT exposed by the read API, for the same reason it is not for
-- devices: it is a hub-side guard, and shipping it beside a last_seen on the
-- other clock would put a pair on the wire that can read first_seen > last_seen
-- for a replayed batch.

CREATE INDEX IF NOT EXISTS containers_seen_idx ON containers (last_seen, first_seen);

-- Its own procedure and job, not a branch of netra_prune_stale_devices: the
-- two horizons answer to different retentions and alter_job has to be able to
-- retune either without touching the other.
CREATE OR REPLACE PROCEDURE netra_prune_stale_containers(job_id INTEGER, config JSONB)
LANGUAGE plpgsql AS $$
DECLARE
    horizon INTERVAL := coalesce((config ->> 'retention')::INTERVAL, INTERVAL '120 days');
    cutoff  TIMESTAMPTZ := now() - horizon;
BEGIN
    -- The CASCADE takes the container's remaining container_samples with it,
    -- and the horizon is set so there are none left to take.
    --
    -- 120 days, not 90, and the extra month is the point -- the same
    -- arithmetic as devices. container_samples_1h retains 90 days, but
    -- add_retention_policy drops a CHUNK only once its whole range is past the
    -- horizon, and those chunks are 7 days wide, so readings survive to about
    -- 97 days. A prune firing at 90 would cascade away up to a week of samples
    -- the retention policy had not reached yet.
    --
    -- Both columns, not just last_seen: see the note above the ALTER TABLEs.
    DELETE FROM containers WHERE last_seen < cutoff AND first_seen < cutoff;
    COMMIT;
END;
$$;

-- add_job has no if_not_exists, and this migration must stay re-runnable --
-- Migrate replays a file whose schema_migrations row is missing. Same DO-block
-- guard 0001 and 0005 use for the same reason.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM timescaledb_information.jobs
         WHERE proc_name = 'netra_prune_stale_containers'
    ) THEN
        PERFORM add_job('netra_prune_stale_containers', INTERVAL '1 day',
                        config => '{"retention": "120 days"}'::jsonb);
    END IF;
END;
$$;
