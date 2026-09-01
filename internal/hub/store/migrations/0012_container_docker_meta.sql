-- What the Docker socket says about a container, as opposed to what its cgroup
-- measures.
--
-- The container detail page carried a "Not collected" card naming four fields
-- netra did not have: health, restarts, state and labels. Three of them were in
-- a response the agent already fetched and decoded away -- /containers/json
-- carries a top-level State, a Status string whose suffix is the only health
-- the endpoint reports, and the full label map, of which exactly two compose
-- keys were read and then folded into container_key. A container started
-- outside compose contributed no label at all.
--
-- Every column is nullable with no default, which is the rule systemd_units
-- states for its own state/substate in 0001: a container with no known state
-- has none, and NULL is exactly that. A default of 'running' or 'healthy'
-- would have the hub assert something no agent ever said, which is the failure
-- the card existed to describe.

ALTER TABLE containers ADD COLUMN IF NOT EXISTS docker_state TEXT;

-- Docker's health, one of healthy / unhealthy / starting / none.
--
-- 'none' is a READING, not an absence: the agent looked and the image defines
-- no HEALTHCHECK, which is `docker ps --filter health=none`. NULL is the other
-- fact -- nobody could look -- and the UI words them differently.
ALTER TABLE containers ADD COLUMN IF NOT EXISTS health TEXT;

-- When docker_state was ENTERED, not when the hub last heard about it, which
-- is the rule read.Unit.Since already documents for systemd. The upsert
-- advances it only when the state actually changes.
ALTER TABLE containers ADD COLUMN IF NOT EXISTS state_ts TIMESTAMPTZ;

-- Every label the daemon reports.
--
-- No GIN index yet. It costs write amplification on an upsert that runs once
-- per container per scrape, and nothing queries inside the document today; it
-- belongs with the feature that searches labels, not ahead of it.
ALTER TABLE containers ADD COLUMN IF NOT EXISTS labels JSONB;

-- The latest restart count, so the detail page can state a number at ANY time
-- range. The per-sample column below cannot: it lives in the raw tier only
-- (see the note at the end), so a page asking for 30 days would find no
-- restart series and would have to say nothing rather than say 4.
ALTER TABLE containers ADD COLUMN IF NOT EXISTS restart_count BIGINT;

-- The same counter per sample, which is what makes restarts chartable and lets
-- a hole in a container's series be attributed: a gap with a rising restart
-- count is a restart, and a gap without one is missing samples. The UI has been
-- refusing to name the difference precisely because this did not exist.
--
-- Dense, not one point in ten: the agent reports its cached count on every
-- scrape rather than only on the scrapes that called the inspect endpoint. NULL
-- here is an agent that cannot answer, never a container that did not restart.
ALTER TABLE container_samples ADD COLUMN IF NOT EXISTS restart_count BIGINT;

-- Deliberately NOT added to container_samples_5m, _1h or _1d.
--
-- A TimescaleDB continuous aggregate cannot gain a column: all three would have
-- to be dropped and recreated, and each rematerialises only from the tier below
-- it -- _1h from what _5m still retains, _1d from _1h. That trades real CPU and
-- memory history, which people read, for a counter that has no history to
-- rebuild. 0003_host_proto_samples.sql makes the same argument for host
-- metrics and sets the precedent: a new relation, never a widened aggregate.
--
-- It degrades cleanly rather than breaking. read/columns.go discovers value
-- columns from information_schema, so restart_count simply appears at the raw
-- tier and is absent above it, and read/metrics.go turns a column missing at
-- the chosen tier into a warning rather than an error. The restarts CHART is
-- therefore a short-window feature; the NUMBER, from containers.restart_count
-- above, always answers.
