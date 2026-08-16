-- A unit's current state becomes a COLUMN, instead of "whatever the newest
-- event said".
--
-- systemd_unit_events was doing two jobs. It is a log -- a unit went failed at
-- 14:02, recovered at 14:31 -- and it was also the only place the CURRENT
-- state lived, read back through a LEFT JOIN LATERAL in read.Units. Those two
-- jobs disagree, and the disagreement is why "exim4.service failed" could
-- never clear itself:
--
--   * A log has no way to say "nothing changed, and here is the truth anyway".
--     The agent emits an event only on a transition, so if the transition that
--     would have fixed the hub's view is never sent, the last event stands
--     forever. Three routine things suppress it: the unit recovered while the
--     agent was down (the restart baseline reports only FAILED units), the
--     unit vanished from the bus entirely (`apt purge`), or the scrape
--     carrying the recovery was dropped by the agent's ring buffer.
--
--   * The log is PRUNED. netra_prune_discrete_events deletes events past 90
--     days, so a unit that has been failed and untouched for longer had its
--     only event deleted and its state silently became NULL -- the hub
--     forgetting a live problem and calling it resolved.
--
-- With state on the row, the agent can send a periodic snapshot that states
-- what IS rather than what changed, and a divergence cannot outlive it. The
-- events table goes back to being only a log, and pruning it is safe again.
--
-- Nullable with no default, for the reason 0002_host_current_net.sql gives: a
-- unit the backfill below finds no event for has no known state, and NULL is
-- exactly that. read.Units already renders an absent marker for it.

ALTER TABLE systemd_units
    ADD COLUMN IF NOT EXISTS state TEXT;

ALTER TABLE systemd_units
    ADD COLUMN IF NOT EXISTS substate TEXT;

ALTER TABLE systemd_units
    ADD COLUMN IF NOT EXISTS state_ts TIMESTAMPTZ;

-- Seed the columns from what the LATERAL would have returned, so no existing
-- host loses its unit states at upgrade. Without this every unit on every host
-- reads as absent until its next transition -- which, for a unit that is
-- currently failed and staying failed, is never.
--
-- DISTINCT ON rides systemd_unit_events_unit_id_host_id_idx and reads the
-- table once. The correlated-subquery form of the same statement is the one
-- that runs for minutes on a real fleet while holding the migration's advisory
-- lock, blocking every hub replica trying to start.
UPDATE systemd_units u
   SET state    = e.state,
       substate = e.substate,
       state_ts = e.ts
  FROM (SELECT DISTINCT ON (host_id, unit_id)
               host_id, unit_id, state, substate, ts
          FROM systemd_unit_events
         ORDER BY host_id, unit_id, ts DESC) e
 WHERE u.id = e.unit_id
   AND u.host_id = e.host_id;

-- ------------------------------------------------ the service count, current
--
-- The host page's Units summary reads "397 units - 0 failed". It counted the
-- rows the units endpoint returned, which worked only while that endpoint
-- returned every unit. It no longer does: units are now listed when they need
-- attention, so on a healthy host the endpoint returns nothing and the summary
-- would have read "0 units - 0 failed" on a host running 397 of them.
--
-- The real counts have been collected on every scrape since the beginning
-- (collector/systemd.go sets them, store/ingest.go writes them) but only into
-- host_samples and its rollups, which nothing reads. host_current is where the
-- page already reads its other current gauges, for the reason
-- 0002_host_current_net.sql spells out: reading a scalar off a gridded series
-- makes its value depend on which time range the viewer happens to be looking
-- at, and a count of services is not a function of the chart range.

ALTER TABLE host_current
    ADD COLUMN IF NOT EXISTS services_total INTEGER;

ALTER TABLE host_current
    ADD COLUMN IF NOT EXISTS services_failed INTEGER;
