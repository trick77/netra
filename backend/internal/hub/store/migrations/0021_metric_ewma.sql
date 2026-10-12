-- Normal becomes a moving average, and the window that computed it goes away.
--
-- 0020 measured "normal" as p01/p99 over seven days of raw samples, rebuilt
-- nightly by netra_recompute_baselines. It worked, and the bill was:
--
--   * a fleet-wide percentile_cont -- a sort of every reading every host took
--     in a week -- as a scheduled job;
--   * a table of percentiles to keep in step with it;
--   * a NOT EXISTS join against host_conditions, so a fault could not enter its
--     own baseline and get reported as a recovery;
--   * a bound on that join (opened_ts > cutoff), so a permanent legitimate
--     shift could not freeze the baseline forever;
--   * two gates expressed as SAMPLE COUNTS -- 2000 and 8000 -- each a scrape
--     cadence multiplied by a calendar requirement and readable as neither.
--     8000 meant "five and a half days" and said nothing of the kind, and it
--     had to sit under a perfect week's worth of samples or a host that ever
--     missed a scrape could never be judged at all.
--
-- An exponentially weighted moving average is four numbers per subject, updated
-- as readings arrive. The job, the percentiles, the join, the bound and both
-- gates are all arithmetic in internal/hub/conditions/ewma.go instead.
--
-- TWO THINGS IT DOES NOT FIX, because it would be easy to imply otherwise:
--
--   * The warm-up. At a seven-day time constant the variance has 3.5% of its
--     steady-state weight after six hours, so a 4-sigma band is really 0.75
--     sigma and everything fires. A seven-day constant needs about seven days
--     of observation whatever the mechanism. What goes away is the sample count
--     standing in for a calendar; FamilyRule.MinSpan is the same requirement
--     written as a duration.
--   * The fault-versus-new-normal decision. "The drive is cooking" and "this
--     host is busier now" are the same signal. Something must decide when a
--     persistent excursion becomes the normal; that was the opened_ts bound,
--     and it is now excursion_since plus one comparison.

CREATE TABLE IF NOT EXISTS metric_ewma (
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,

    -- The condition kind and the subject, at the same granularity
    -- host_conditions uses, because they are looked up together.
    kind    TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',

    -- slow is the subject's normal, fast is what it is doing now, var is the
    -- exponentially weighted variance of the deviation from slow.
    --
    -- Two averages rather than one because they answer different questions at
    -- different speeds. Judging the raw reading would open a condition on a
    -- ninety-second cron burst; judging `slow` would never notice anything,
    -- since slow IS the thing being departed from.
    slow DOUBLE PRECISION NOT NULL,
    fast DOUBLE PRECISION NOT NULL,
    var  DOUBLE PRECISION NOT NULL DEFAULT 0,

    -- first_ts is the oldest reading folded in, and the warm-up is measured
    -- against it. updated_ts is the newest, and is BOTH the reference the decay
    -- is computed from and the high-water mark the fold reads forward from --
    -- which is what lets a reconnecting agent's buffered hour fold in as sixty
    -- readings a minute apart rather than as one jump.
    first_ts   TIMESTAMPTZ NOT NULL,
    updated_ts TIMESTAMPTZ NOT NULL,

    -- When fast last left the band, or NULL while it is inside.
    --
    -- THE COLUMN THAT KEEPS A FAULT OUT OF ITS OWN AVERAGE. Learning is frozen
    -- while a subject is outside its band, because without that the variance
    -- channel recreates exactly the bug 0020's exclusion existed to prevent: a
    -- drive 14 sigma out drives var toward 196, reaching 26 within a day, so sd
    -- becomes 5.1 and the band becomes slow + 20 against a reading of slow + 14
    -- -- the reading is inside its own band, the condition clears, and the hub
    -- has reported a recovery for a drive that never cooled.
    --
    -- And it is what lets a real new normal be accepted: an excursion that has
    -- outlasted TauSlow is not an excursion, so learning resumes and the band
    -- follows. Same decision as opened_ts > cutoff, without the join.
    excursion_since TIMESTAMPTZ,

    PRIMARY KEY (host_id, kind, subject)
);

-- No secondary index. The fold reads every row once per tick and the scan looks
-- them up by the primary key; this table is one row per judged subject, sized
-- to be scanned.

-- ---------------------------------------------------------------------------
-- Retiring 0020's window
-- ---------------------------------------------------------------------------

-- The job first, because a procedure cannot be dropped while a job references
-- it.
--
-- NO MIGRATION IN THIS SCHEMA HAS REMOVED A JOB BEFORE -- 0001, 0011 and 0016
-- only add_job -- so the shape is worth stating: delete_job takes the job's id,
-- not its proc name, so the id has to be looked up. Wrapped in a DO block
-- against timescaledb_information.jobs rather than called bare, for the reason
-- every add_job here is guarded: a migration that fails part-way re-runs from
-- the top, and the second pass must find nothing and carry on rather than
-- erroring on an absent job.
DO $$
DECLARE
    id INTEGER;
BEGIN
    FOR id IN
        SELECT job_id FROM timescaledb_information.jobs
         WHERE proc_name = 'netra_recompute_baselines'
    LOOP
        PERFORM delete_job(id);
    END LOOP;
END;
$$;

DROP PROCEDURE IF EXISTS netra_recompute_baselines(INTEGER, JSONB);
DROP TABLE IF EXISTS metric_baselines;

-- netra_sensor_subject and the two `sensors` limit columns from 0020 are NOT
-- dropped. Neither had anything to do with the window: the function is the one
-- spelling of a sensor's identity that the fold, the scan and host_conditions
-- all have to agree on, and the limits are the hardware's own opinion of itself,
-- which is the tier that makes accepting a new normal safe in the first place.
