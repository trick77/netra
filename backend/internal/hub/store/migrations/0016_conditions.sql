-- What is currently wrong with each host, as rows rather than as a derivation
-- in the browser.
--
-- The rules lived only in TypeScript: DISK_WARN_PCT and its free-bytes floor
-- in ui/src/features/fleet/conditions.ts, STALE_THRESHOLD_MS in lib/host.ts.
-- That was survivable while a person reading a page was the only consumer, and
-- it is not once anything else has to ask what is wrong -- alerting cannot call
-- into a browser. It also meant every open tab recomputed the whole fleet's
-- conditions from a per-host metrics fan-out on every poll.
--
-- The deeper problem it fixes is that a derived condition has no onset. Four of
-- the eight kinds left `since` empty because nothing had recorded when they
-- began, so a disk that filled at 03:00 and drained by 09:00 left no trace
-- anywhere. A condition is opened by an event and closed by an event now, and
-- opened_ts is what the UI prints.
--
-- Not a hypertable: this is small, hot, and read on every fleet page load.

CREATE TABLE IF NOT EXISTS host_conditions (
    -- An identity key, NOT (host_id, kind, subject).
    --
    -- Cleared rows are kept, so a host can fill /var twice and the second
    -- occurrence is a second row. The triple is unique only among rows that
    -- are still open, which is what the partial index below says.
    id       INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id  INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,

    -- The kind's stable name: silent, sporadic, disk, failed-units, drive.
    -- Text rather than an enum, matching every other vocabulary in this schema
    -- and the words the API and the UI already use.
    kind     TEXT NOT NULL,

    -- What the condition is ABOUT, at the granularity where a transition is
    -- meaningful: a mount for disk, a device for drive, empty for a condition
    -- about the host as a whole.
    --
    -- Finer than the UI displays, deliberately. The fleet page collapses a
    -- host's drives into one row ("ONE condition for the host, never one per
    -- drive"), and that is a RENDERING decision. If the state machine
    -- collapsed too, the condition would open and close every time the fullest
    -- mount changed from /var to /mnt -- transition pairs describing nothing.
    subject  TEXT NOT NULL DEFAULT '',

    severity TEXT NOT NULL CHECK (severity IN ('warning', 'critical')),

    -- When this started, and whether that is a floor rather than a moment.
    --
    -- Walked back through the mount's own series ONCE, at the moment of
    -- opening, while the raw data still exists -- raw retention is 7 days
    -- (0001_init.sql) and the continuous aggregates are materialized_only, so
    -- a walk attempted later reaches a different distance and returns a
    -- different answer. Doing it here makes the answer exact and permanent.
    --
    -- opened_at_least marks the case where the condition was already true at
    -- the far end of what could be walked, so the UI says "over 7 d" rather
    -- than naming a moment where nothing happened.
    opened_ts       TIMESTAMPTZ NOT NULL,
    opened_at_least BOOLEAN NOT NULL DEFAULT FALSE,

    -- NULL while the condition is open. Set when it clears, and the row then
    -- stays: alert history is the point of keeping it.
    resolved_ts     TIMESTAMPTZ,

    -- WHY it stopped, which are not the same fact.
    --
    -- 'cleared' is the predicate going false: the disk drained, the unit came
    -- back. 'vanished' is the SUBJECT going away while the host kept talking:
    -- a mount unmounted, a drive pulled, a unit purged. Without the
    -- distinction a vanished subject either stays open forever -- nothing ever
    -- evaluates it again, so the predicate is never false, merely absent -- or
    -- is recorded as a recovery that never happened. A fleet that goes green
    -- because nobody is looking at it is the failure this column exists to
    -- prevent.
    resolved_reason TEXT CHECK (resolved_reason IN ('cleared', 'vanished')),

    -- How many consecutive evaluations have found the predicate false.
    --
    -- Hysteresis, and asymmetric on purpose: a condition opens on the first
    -- observation and clears only after it has been false for two of them. You
    -- want to know quickly, and you do not need a fast all-clear -- a disk
    -- oscillating either side of 90% must not write a transition pair per tick.
    missing_ticks   INTEGER NOT NULL DEFAULT 0,

    -- The condition's own numbers, for the sentence the UI writes: the
    -- percentage and bytes free for a disk, the failing unit names, the SMART
    -- finding. Shaped by the kind, like an event's detail.
    detail   JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- resolved_ts is NULL exactly when resolved_reason is, or the row claims
    -- to be both open and closed.
    CONSTRAINT host_conditions_resolution_complete
        CHECK ((resolved_ts IS NULL) = (resolved_reason IS NULL))
);

-- One open condition per subject, as a database invariant rather than as
-- something the evaluator has to remember.
--
-- This is also the index behind "what is firing now": the fleet page's read is
-- WHERE resolved_ts IS NULL, which rides this directly and never touches
-- history. Partial, so it stays the size of what is actually wrong rather than
-- growing with everything that ever was.
CREATE UNIQUE INDEX IF NOT EXISTS host_conditions_open_key
    ON host_conditions (host_id, kind, subject)
    WHERE resolved_ts IS NULL;

-- "What has happened on this host", the way the history is read.
CREATE INDEX IF NOT EXISTS host_conditions_host_opened_idx
    ON host_conditions (host_id, opened_ts DESC);

-- Retention deletes RESOLVED rows only, and this is not a preference.
--
-- 0001_init.sql carries the scar: systemd_unit_events is pruned at 90 days, so
-- a unit failed and untouched for longer had its only event deleted and its
-- state silently became NULL -- "the hub forgetting a live problem and calling
-- it resolved". An open condition is exactly that live problem, so it survives
-- however old it gets.
--
-- A predicated DELETE rather than a TimescaleDB retention policy, because this
-- is a plain table and because drop_chunks removes whole chunks by time and
-- cannot see whether a row is resolved.
--
-- 90 days matches the event log's own retention: the transitions that explain
-- these rows are pruned there, so history kept longer would be rows nobody can
-- explain.
CREATE OR REPLACE PROCEDURE netra_prune_conditions(job_id INTEGER, config JSONB)
LANGUAGE plpgsql AS $$
DECLARE
    -- From the job's config rather than hardcoded, as netra_prune_discrete_events
    -- does, so the horizon can be changed with alter_job on a running hub
    -- instead of a schema edit.
    horizon INTERVAL := coalesce((config ->> 'retention')::INTERVAL, INTERVAL '90 days');
BEGIN
    DELETE FROM host_conditions
     WHERE resolved_ts IS NOT NULL
       AND resolved_ts < now() - horizon;
END;
$$;

-- Guarded for the reason 0001's own add_job call is: add_job has no
-- if_not_exists, and a migration that fails part-way re-runs from the top,
-- which would otherwise register a second copy of the same job.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM timescaledb_information.jobs
         WHERE proc_name = 'netra_prune_conditions'
    ) THEN
        PERFORM add_job('netra_prune_conditions', INTERVAL '1 day',
                        config => '{"retention": "90 days"}'::jsonb);
    END IF;
END;
$$;
