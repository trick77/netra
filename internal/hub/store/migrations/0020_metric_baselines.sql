-- What "normal" is for one subject, measured rather than assumed.
--
-- The five kinds that exist today judge against constants: 90% full, three
-- missed scrapes, a SMART counter above zero. That works because every one of
-- them means the same thing on every machine. The metrics this migration
-- serves do not. 300 processes is idle on one box and a fork bomb on another;
-- a NAS drive that has run at 44 C for two years deserves attention at 58 C,
-- while a busy NVMe at 58 C is doing exactly what its datasheet promises. A
-- single number cannot judge both, and picking one per fleet means picking one
-- that is wrong for most of it.
--
-- So the threshold is calibrated from each subject's own history, following
-- Observium's model: a 7-day window, percentile baselines rather than min/max,
-- and a margin that adapts to how wide the subject's normal range is.
--
-- WHAT THIS TABLE DOES NOT STORE, and the omission is deliberate: the warning
-- and critical thresholds themselves. Only p01, p99 and the sample count live
-- here -- what the history MEASURED. The margin arithmetic, the family floors,
-- the hardware limit and the fallback ceiling are all applied in Go
-- (conditions/deviation.go), for two reasons. Stored thresholds would go stale
-- the moment a constant changed, silently, until the next daily run; and the
-- repo's own convention is that judgement is pure and testable without a
-- database, which is what rules.go and drive.go already are.
--
-- Not a hypertable: one row per subject, rewritten daily, read on every
-- evaluator tick. That is the shape of host_conditions, not of a series.

CREATE TABLE IF NOT EXISTS metric_baselines (
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,

    -- The condition kind this baseline serves: temperature, processes, load.
    -- Text, matching every other vocabulary in this schema.
    kind    TEXT NOT NULL,

    -- The subject, at the same granularity host_conditions uses, because the
    -- two are joined on it: a sensor identity for temperature, empty for a
    -- kind about the host as a whole.
    subject TEXT NOT NULL DEFAULT '',

    -- The robust bounds of normal. p01/p99 rather than min/max so that one
    -- spike -- a single reboot, one backup window -- cannot move the
    -- threshold, which is the entire reason percentiles are used here.
    p01 DOUBLE PRECISION NOT NULL,
    p99 DOUBLE PRECISION NOT NULL,

    -- How many samples the pair was computed from, AFTER the exclusion below.
    -- Carried rather than recomputed because it is what the min-samples gate
    -- reads, and because a reader looking at a surprising threshold needs to
    -- know whether it rests on a week or on an afternoon.
    sample_count INTEGER NOT NULL,

    computed_ts TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (host_id, kind, subject)
);

-- The evaluator's read is "every baseline, once a tick", so no secondary index
-- exists: the primary key is the only access path, and a full scan of one row
-- per judged subject is what this table was sized to be.

-- One spelling of a sensor's identity, defined once because two callers need
-- it to agree exactly.
--
-- The recompute builds a subject from `sensors` to store it; the scan builds
-- the same subject to look it up; host_conditions stores it a third time. If
-- those three drifted by so much as a separator the baseline would never be
-- found, every sensor would read as unjudged, and nothing would be raised --
-- the quietest possible failure. A function is the only way to make the
-- compiler's absence not matter.
--
-- instance is appended only when set, so coretemp/Package id 0 keeps the
-- two-part name it would have had anyway and only the storage chips -- the
-- ones that genuinely collide -- carry a third part.
CREATE OR REPLACE FUNCTION netra_sensor_subject(chip TEXT, label TEXT, instance TEXT)
RETURNS TEXT
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN instance IS NULL OR instance = '' THEN chip || '/' || label
        ELSE chip || '/' || label || '/' || instance
    END;
$$;

-- The chip's own limits, alongside the identity they belong to.
--
-- On `sensors` rather than in sensor_samples because they are constant for the
-- life of the drive, which is spec 5.1 rule 4 verbatim: anything that holds
-- still for hours belongs with the dimension, not in a 60-second series. A
-- firmware update that changes CCTEMP overwrites the row, as it should.
--
-- NULL means the chip publishes nothing (k10temp, acpitz), or the agent
-- predates the field, or the read failed. All three are the same fact to a
-- reader -- "no limit from the hardware" -- and all three fall back to the
-- calibrated threshold under a fixed ceiling.
ALTER TABLE sensors
    ADD COLUMN IF NOT EXISTS limit_high      DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS limit_high_crit DOUBLE PRECISION;

-- ---------------------------------------------------------------------------
-- The recompute
-- ---------------------------------------------------------------------------

-- Once a day, not once a tick.
--
-- percentile_cont over seven days of raw sensor_samples, fleet-wide, is a sort
-- of every reading every host has taken in a week. That is a perfectly
-- reasonable thing to do daily and an absurd thing to do inside a 60-second
-- loop, which is the whole reason this is a table and not a view.
--
-- THE EXCLUSION IS THE POINT OF THIS PROCEDURE, and without it the feature
-- destroys itself inside a day. Walk it: a drive's p99 is 46, its margin 6, so
-- critical sits at 52. The drive goes to 58 and stays there; the condition
-- opens. Tomorrow's recompute sees one of seven days at 58 -- fourteen per cent
-- of the window, against the one per cent p99 discards -- so p99 becomes 58,
-- the threshold rises above it, the miss counter runs out and the log records
-- "cleared". The drive is still at 58 C. A detector that un-detects a fault it
-- already found is worse than no detector, because it also writes a recovery
-- nobody should believe.
--
-- So samples taken while a condition was open for that exact subject are not
-- evidence of what normal looks like. Resolved intervals are excluded too: a
-- fault that lasted two days and was fixed would otherwise keep inflating the
-- baseline for the rest of the week.
CREATE OR REPLACE PROCEDURE netra_recompute_baselines(job_id INTEGER, config JSONB)
LANGUAGE plpgsql AS $$
DECLARE
    -- From the job's config, as netra_prune_conditions and
    -- netra_prune_discrete_events both are, so a running hub can be retuned
    -- with alter_job rather than a schema edit.
    window_len INTERVAL := coalesce((config ->> 'window')::INTERVAL, INTERVAL '7 days');

    -- Below this the subject has no baseline and is left UNJUDGED rather than
    -- called healthy. ~34 h at the 60 s scrape cadence.
    --
    -- This gate is also what makes the exclusion above safe. A condition open
    -- long enough to consume the window leaves too few samples behind, the
    -- gate refuses the row, and the UPSERT never runs -- so the existing
    -- baseline survives untouched. Deleting it or writing a low count instead
    -- would push a subject with a LIVE condition into unjudged, and an
    -- unjudged subject's condition is left alone forever: the hub would forget
    -- a problem it was actively reporting. 0016_conditions.sql:110-116 records
    -- the last time this codebase made that mistake.
    min_samples INTEGER := coalesce((config ->> 'min_samples')::INTEGER, 2000);

    cutoff TIMESTAMPTZ := now() - window_len;
BEGIN
    -- ------------------------------------------------------------ temperature
    INSERT INTO metric_baselines (host_id, kind, subject, p01, p99, sample_count, computed_ts)
    SELECT s.host_id,
           'temperature',
           netra_sensor_subject(sen.chip, sen.label, sen.instance),
           percentile_cont(0.01) WITHIN GROUP (ORDER BY s.temp),
           percentile_cont(0.99) WITHIN GROUP (ORDER BY s.temp),
           count(*),
           now()
      FROM sensor_samples s
      JOIN sensors sen ON sen.id = s.sensor_id AND sen.host_id = s.host_id
     WHERE s.ts >= cutoff
       AND s.temp IS NOT NULL
       AND sen.kind = 'temperature'
       AND NOT EXISTS (
           SELECT 1
             FROM host_conditions c
            WHERE c.host_id = s.host_id
              AND c.kind    = 'temperature'
              AND c.subject = netra_sensor_subject(sen.chip, sen.label, sen.instance)
              AND s.ts >= c.opened_ts
              AND s.ts <  coalesce(c.resolved_ts, 'infinity'::timestamptz)
       )
     GROUP BY s.host_id, netra_sensor_subject(sen.chip, sen.label, sen.instance)
    HAVING count(*) >= min_samples
        ON CONFLICT (host_id, kind, subject) DO UPDATE
       SET p01 = excluded.p01,
           p99 = excluded.p99,
           sample_count = excluded.sample_count,
           computed_ts = excluded.computed_ts;

    -- -------------------------------------------------- processes and load
    --
    -- Both come from host_samples with an empty subject, so one statement per
    -- column rather than a join: the two columns are NULL independently -- a
    -- kernel without /proc/loadavg is not a kernel without a process count --
    -- and a shared GROUP BY would let either one's absence shorten the other's
    -- window.
    INSERT INTO metric_baselines (host_id, kind, subject, p01, p99, sample_count, computed_ts)
    SELECT h.host_id,
           'processes',
           '',
           percentile_cont(0.01) WITHIN GROUP (ORDER BY h.processes_total),
           percentile_cont(0.99) WITHIN GROUP (ORDER BY h.processes_total),
           count(*),
           now()
      FROM host_samples h
     WHERE h.ts >= cutoff
       AND h.processes_total IS NOT NULL
       AND NOT EXISTS (
           SELECT 1
             FROM host_conditions c
            WHERE c.host_id = h.host_id
              AND c.kind    = 'processes'
              AND c.subject = ''
              AND h.ts >= c.opened_ts
              AND h.ts <  coalesce(c.resolved_ts, 'infinity'::timestamptz)
       )
     GROUP BY h.host_id
    HAVING count(*) >= min_samples
        ON CONFLICT (host_id, kind, subject) DO UPDATE
       SET p01 = excluded.p01,
           p99 = excluded.p99,
           sample_count = excluded.sample_count,
           computed_ts = excluded.computed_ts;

    INSERT INTO metric_baselines (host_id, kind, subject, p01, p99, sample_count, computed_ts)
    SELECT h.host_id,
           'load',
           '',
           percentile_cont(0.01) WITHIN GROUP (ORDER BY h.load5),
           percentile_cont(0.99) WITHIN GROUP (ORDER BY h.load5),
           count(*),
           now()
      FROM host_samples h
     WHERE h.ts >= cutoff
       AND h.load5 IS NOT NULL
       AND NOT EXISTS (
           SELECT 1
             FROM host_conditions c
            WHERE c.host_id = h.host_id
              AND c.kind    = 'load'
              AND c.subject = ''
              AND h.ts >= c.opened_ts
              AND h.ts <  coalesce(c.resolved_ts, 'infinity'::timestamptz)
       )
     GROUP BY h.host_id
    HAVING count(*) >= min_samples
        ON CONFLICT (host_id, kind, subject) DO UPDATE
       SET p01 = excluded.p01,
           p99 = excluded.p99,
           sample_count = excluded.sample_count,
           computed_ts = excluded.computed_ts;

    -- Subjects that have stopped reporting entirely.
    --
    -- Judged WITHOUT the condition exclusion, deliberately. A drive whose
    -- condition has been open all week has every sample excluded from the
    -- percentiles above, and it is still very much reporting -- deleting its
    -- baseline would strand a live condition exactly as a low sample_count
    -- would. The question here is only "has anything arrived at all".
    DELETE FROM metric_baselines b
     WHERE b.computed_ts < cutoff
       AND NOT EXISTS (
           SELECT 1 FROM sensor_samples s
            WHERE s.host_id = b.host_id AND s.ts >= cutoff
       )
       AND NOT EXISTS (
           SELECT 1 FROM host_samples h
            WHERE h.host_id = b.host_id AND h.ts >= cutoff
       );
END;
$$;

-- Guarded for the reason 0001's and 0016's add_job calls are: add_job has no
-- if_not_exists, and a migration that fails part-way re-runs from the top,
-- which would otherwise register a second copy of the same job.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM timescaledb_information.jobs
         WHERE proc_name = 'netra_recompute_baselines'
    ) THEN
        PERFORM add_job('netra_recompute_baselines', INTERVAL '1 day',
                        config => '{"window": "7 days", "min_samples": 2000}'::jsonb);
    END IF;
END;
$$;
