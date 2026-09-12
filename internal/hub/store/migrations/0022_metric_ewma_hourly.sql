-- Normal becomes normal FOR THIS HOUR, because one average per subject fires
-- every morning on every host that works during the day.
--
-- 0021 kept one slow/var pair per subject. Measured over thirty simulated days
-- with no fault anywhere in the data:
--
--   coretemp 40/70 C, busy 16h   slow stuck at 40.05, sd stuck at the floor,
--                                27,840 firing minutes
--   load5 0.2/3.0, busy 12h      slow stuck at 0.203, 16,560 firing minutes
--
-- So a CPU that is busy during working hours opened a condition every morning
-- and cleared it every night, indefinitely -- which is how a fleet learns to
-- ignore its own attention list. The p01/p99 window 0021 replaced did NOT have
-- this failure: the busy hours were inside the percentile.
--
-- The cause is the freeze, not the estimator, and a second wrong answer is what
-- established that. A decaying q99 -- robust to a two-humped distribution by
-- construction, as the percentile was -- measured 27,927 firing minutes, worse.
-- At cold start the estimate sits at idle, so the band sits just above idle, the
-- busy hours fall outside it, learning freezes, and the estimate can never rise
-- to contain them. Any estimator that stops learning while outside its own band
-- cannot learn a mode above that band. And the freeze cannot just be dropped:
-- without it a sustained fault walks the variance up until the band overtakes
-- the reading and the hub reports a recovery for a drive that never cooled.
--
-- Twenty-four buckets dissolve that instead of trading it off. A host busy at
-- noon is busy at noon every day, so the 23:00 bucket sees idle, the 12:00
-- bucket sees busy, and neither is ever outside its own band. Measured the same
-- way: 29 and 23 firing minutes over thirty days.
--
-- That first cut only held for a day that begins on the hour, and review caught
-- it: a busy period starting at 08:15 put 1,305 minutes back, because the 08:00
-- bucket seeded on its idle quarter, froze the moment the reading crossed into
-- the busy three quarters, and never learned them. The `weight` and `exc`
-- columns below are what make a MIXED hour work. Measured across mode changes at
-- :00, :15, :30 and :45: 29, 29, 42 and 89 firing minutes, and what remains is
-- the minute either side of a mode change while `fast` catches up, which OpenFor
-- swallows.

-- The seasonal half, one row per subject per hour of day.
CREATE TABLE IF NOT EXISTS metric_ewma_hour (
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    kind    TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',

    -- 0-23, UTC. Not the host's local hour: the cycle repeats every twenty-four
    -- hours whichever offset it is labelled in, so a host in CET lands its busy
    -- hours in a consistent set of UTC buckets and each still sees one mode.
    -- Local time would mean reading a timezone the hub does not collect in order
    -- to relabel buckets whose behaviour would not change.
    hour    SMALLINT NOT NULL CHECK (hour >= 0 AND hour < 24),

    slow DOUBLE PRECISION NOT NULL,
    var  DOUBLE PRECISION NOT NULL DEFAULT 0,

    -- How much observation this bucket rests on, approaching one. It is the
    -- bucket's own warm-up -- below WarmWeight its band is trusted neither to
    -- judge against nor to freeze on -- and its seed flag, since a bucket no
    -- reading has reached is zero.
    --
    -- NO updated_ts, and the omission is deliberate. A bucket sees its hour
    -- once a day: sixty readings a minute apart, then a twenty-three-hour gap.
    -- Decaying by the gap since the bucket's own last reading gave the first
    -- reading after the gap 0.128 of the weight and the other fifty-nine about
    -- 0.006 between them, so the bucket averaged one reading per day rather
    -- than the hour. Every reading is instead one observation of the hour at a
    -- constant weight (conditions.BucketAlpha), and a full visit still totals
    -- what a day of wall clock would. Ordering and de-duplication are the
    -- subject row's job, against its own updated_ts.
    weight DOUBLE PRECISION NOT NULL DEFAULT 0,

    -- The fraction of this bucket's observations that found the subject
    -- outside the bucket's band, decayed at the same rate as everything else.
    --
    -- This is how a recurring pattern is told from a fault. The subject-level
    -- rule accepts an excursion as the new normal once it has lasted TauSlow,
    -- but only a CONTINUOUS one: a cron job at 03:15 is outside the 03:00 band
    -- for thirty minutes and back inside for the rest of the day, so that clock
    -- resets every morning and the bucket would freeze on it forever. As a
    -- share of the bucket's own visits it is 50%, a fault is 100%, an ordinary
    -- hour is 0%, and past ModeFraction the bucket learns it.
    exc DOUBLE PRECISION NOT NULL DEFAULT 0,

    PRIMARY KEY (host_id, kind, subject, hour)
);

-- No secondary index: the fold reads a kind's rows once per pass and the scan
-- looks one up by primary key. Twenty-four rows per judged subject is what this
-- was sized to be -- a twenty-host fleet with eight sensors each is under four
-- thousand.

-- metric_ewma keeps the per-subject half: what the subject is doing NOW, how
-- much history it rests on, and when it left its band.
--
-- `fast` is deliberately not bucketed. It is the present tense rather than a
-- seasonal expectation: there is one current reading whatever hour it arrives
-- in. `excursion_since` likewise -- it drives OpenFor, which is a fact about the
-- subject being in trouble and not about an hour.
ALTER TABLE metric_ewma DROP COLUMN IF EXISTS slow;
ALTER TABLE metric_ewma DROP COLUMN IF EXISTS var;

-- The state is dropped rather than migrated, and there is nothing to salvage: a
-- single average of a two-humped subject sits between the humps, which is not
-- the value either bucket wants and cannot be split back into them. Forward-only
-- and empty, so every subject re-warms over MinSpan.
--
-- Open conditions are untouched meanwhile. A subject with no state is Unjudged,
-- which leaves its condition exactly as it is rather than clearing it -- the
-- distinction 0016_conditions.sql:110-116 records this codebase getting wrong
-- once already.
TRUNCATE TABLE metric_ewma;
