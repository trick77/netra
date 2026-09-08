-- "How many times did this container restart yesterday?" had no answer.
--
-- 0012 put restart_count on containers (the latest value, at any range) and on
-- container_samples (a dense series, so a hole in a chart could be attributed)
-- and argued -- correctly, and the argument still stands -- that it must NOT be
-- added to container_samples_5m/_1h/_1d: a continuous aggregate cannot gain a
-- column, all three would have to be dropped and rematerialised from the tier
-- below, and that trades real CPU and memory history for a counter with no
-- history to rebuild. 0003 set the precedent: a new relation, never a widened
-- aggregate.
--
-- The consequence was a 7-day cliff. The raw tier is the only tier carrying the
-- column, so a 24h fleet question already resolves to a rollup that lacks it and
-- a 30-day one has nothing at all. Worse, the counter is a GAUGE and the
-- question is a COUNT: two samples an hour apart reading 5 and 7 mean two
-- restarts, and no aggregate of a gauge says so.
--
-- So a restart is recorded as what it is: an EVENT. A count over `events` has no
-- tiers, no rollup lag and no 7-day cliff; it answers at any range the 90-day
-- prune still holds; the Events tab renders it with no new read path; and 0017
-- already built the index it rides,
-- events_host_type_subject_ts_idx (host_id, type, subject, ts DESC).
--
-- The producer is the hub, at ingest, in the same transaction as the containers
-- upsert -- see resolveContainerIDs in store/families.go. It is the only place
-- that sees every transition: the counter's history is not stored anywhere else
-- past 7 days, and a scheduled scanner would miss a redeploy between two runs.
--
-- TWO types, because they are two facts:
--
--   container_restart   the counter went UP. Docker restarted the container in
--                       place: it died. severity warning. detail carries
--                       {from, to, delta} and EVERY window query must SUM the
--                       delta, never count the rows -- a crash-looping
--                       container advances the counter by more than one between
--                       two observations, and the unique index on
--                       (host_id, ts, type, subject) could not hold one row per
--                       restart at a single ts even if it wanted to.
--
--   container_recreate  the counter went DOWN, or the container's started_at
--                       moved while the counter did not rise. Docker resets
--                       RestartCount for a new container, and container_key is
--                       compose project/service, so this is a redeploy: the same
--                       service, a new container. detail carries the old and new
--                       image, which the upsert has in hand. severity info -- a
--                       deploy is an operator's own action, not a fault.
--
-- NEITHER is a state type, and neither may be added to eventStateKeys in
-- store/event_state.go. 0017 collapses a restated STATE because the second row
-- says nothing the first did not; these are OCCURRENCES, where two identical
-- rows are two real facts and how often they repeat is the entire reading --
-- the same distinction 0017 draws to exclude the kmsg family.
--
-- The event ts is Docker's own State.StartedAt for the new incarnation where the
-- agent has it (0018), which is exact to the second. Where it does not, the ts
-- is the sample the increase was OBSERVED on, and it is an UPPER BOUND: the
-- agent's inspect cache can be up to ten scrapes (ten minutes at the 60s
-- default) behind, so the restart happened at or before that ts, not on it.
-- detail.ts_source says which of the two a row is, and no reader may treat an
-- `observed` row as a precise moment.
--
-- WHAT THIS CANNOT SEE, said out loud: a restart that happens entirely inside a
-- window where the agent cannot inspect is never counted, because the count is
-- NULL on both sides of it. No design can close that -- an agent that cannot
-- inspect has no counts at all in that window by definition -- and it is
-- strictly better than the raw-tier column, which loses it too AND loses
-- everything past 7 days.

-- The events already implied by the raw tier.
--
-- container_samples keeps 7 days and carries restart_count densely, so the last
-- week of restarts is derivable and there is no reason to ship this feature
-- empty. lag() over each container's own series, oldest first.
--
-- Every backfilled row is ts_source 'observed': started_at did not exist before
-- 0018 and no sample carries it, so nothing here can be dated exactly. They are
-- marked `backfilled` too, because a reader comparing this log against a chart
-- deserves to know which rows the database derived after the fact rather than
-- observed as they happened.
--
-- image_from is absent on a backfilled recreate: the history holds only the
-- image the container has NOW, not the one it replaced.
--
-- ON CONFLICT DO NOTHING, so re-running this file on a database where ingest has
-- already written live events is a no-op rather than a violation.
WITH stepped AS (
    SELECT s.host_id,
           s.ts,
           c.container_key,
           c.image,
           s.restart_count AS cur,
           lag(s.restart_count) OVER (PARTITION BY s.container_id
                                          ORDER BY s.ts) AS prev
      FROM container_samples s
      JOIN containers c ON c.id = s.container_id AND c.host_id = s.host_id
     WHERE s.restart_count IS NOT NULL
)
INSERT INTO events (host_id, ts, type, subject, detail, severity)
SELECT host_id,
       ts,
       CASE WHEN cur > prev THEN 'container_restart' ELSE 'container_recreate' END,
       container_key,
       jsonb_build_object(
           'from', prev,
           'to', cur,
           'delta', GREATEST(cur - prev, 0),
           'ts_source', 'observed',
           'backfilled', true
       ) || CASE WHEN cur < prev AND image IS NOT NULL
                 THEN jsonb_build_object('image_to', image)
                 ELSE '{}'::jsonb END,
       CASE WHEN cur > prev THEN 'warning' ELSE 'info' END
  FROM stepped
 WHERE prev IS NOT NULL
   AND cur IS DISTINCT FROM prev
ON CONFLICT (host_id, ts, type, subject) DO NOTHING;

-- No index is created. 0017's events_host_type_subject_ts_idx is exactly the
-- shape "how many restarts for this container_key on this host since T" wants,
-- and events_host_id_ts_idx already covers the unfiltered log the Events tab
-- reads. A third would be paid for on every insert to serve neither.
--
-- No retention either: netra_prune_discrete_events (0001, 90 days, configurable
-- through the job's config rather than a schema edit) already governs this
-- table, and 90 days matches the 1h tier for the reason it states -- an event is
-- the thing a metric chart is read ALONGSIDE. So restarts answer at 24h, 7d and
-- 30d and stop answering at 90; containers.restart_count remains the all-time
-- number. Those are different questions and both stay answerable.
