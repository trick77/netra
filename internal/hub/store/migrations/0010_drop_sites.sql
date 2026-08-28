-- Sites and providers go. Nothing reads them.
--
-- They were the hub's answer to "where is this machine": a human created a
-- site through the admin UI, assigned hosts to it, and optionally filled in a
-- facility and a country. Migration 0009 replaced that with what each host's
-- own agent reports (AGENT_LOCATION, AGENT_PROVIDER, AGENT_FACILITY), which
-- needs nobody to maintain it and was already being sent on every metadata
-- post. Two mechanisms for one fact is one too many, and this is the one that
-- required work from the operator to say anything at all.
--
-- Dropping the tables is not reversible and the site names, facilities,
-- countries and coordinates in them are not recoverable afterwards. That is
-- the intent: they are superseded, and leaving them behind unread would be a
-- schema nobody maintains and a UI nobody should use.

-- What made a host unique has to change with them.
--
-- The old index was UNIQUE (site_id, hostname) NULLS NOT DISTINCT, and 0001
-- explains why: two machines at DIFFERENT sites may legitimately share a name.
-- Without site_id there is no such thing as a different site, so a hostname is
-- the identity, and the same protection has to be re-stated against hostname
-- alone or the admin API goes back to creating rows indistinguishable in every
-- view that shows a name.
--
-- Which leaves the one case that cannot be waved through: a hub that really
-- does have two hosts sharing a name at two sites. Creating the new index over
-- them fails, and a failed migration means the hub does not start -- so this
-- cannot simply be attempted and hoped for.
--
-- Every such host keeps its row, its history and its token; the duplicates are
-- renamed with their own id appended, and the OLDEST (lowest id) of each group
-- keeps the bare name. A renamed host is visible immediately -- it is the name
-- on the fleet list -- which is the point: this is a collision only an operator
-- can really resolve, and it must not be resolved silently by dropping a row.
-- The agent never writes hostname back (SaveMetadata reads every metadata field
-- except that one, deliberately), so the rename stands until someone changes it.
UPDATE hosts h
   SET hostname = h.hostname || '-' || h.id
 WHERE EXISTS (
       SELECT 1 FROM hosts o
        WHERE o.hostname IS NOT DISTINCT FROM h.hostname
          AND o.id < h.id
 );

DROP INDEX IF EXISTS hosts_site_id_hostname_key;

-- NULLS NOT DISTINCT for the reason 0001 gives: hostname is nullable, and
-- Postgres treats each NULL as unique by default, so without it every host
-- that has never reported a name could collide freely.
CREATE UNIQUE INDEX IF NOT EXISTS hosts_hostname_key
    ON hosts (hostname) NULLS NOT DISTINCT;

-- Drops the column and the foreign key on it in one statement.
ALTER TABLE hosts DROP COLUMN IF EXISTS site_id;

-- sites first: it carries the foreign key into providers.
DROP TABLE IF EXISTS sites;
DROP TABLE IF EXISTS providers;
