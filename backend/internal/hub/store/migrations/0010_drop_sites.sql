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
-- Re-keying cannot fail, and the NULLS NOT DISTINCT on the OLD index is what
-- guarantees it. The feature went unused: no host was ever assigned a site, so
-- every site_id is NULL -- and with NULLS NOT DISTINCT a second (NULL, 'web-01')
-- already violated the old index. Duplicate hostnames were therefore impossible
-- before this migration, and the new index has nothing to trip over.
--
-- This deliberately does NOT de-duplicate first. An earlier draft renamed
-- colliding hosts by appending their id, which was both unreachable here and
-- wrong where it would have run: `web-01`, `web-01` and an unrelated `web-01-2`
-- rename into a fresh collision, the index still fails, the whole file rolls
-- back unrecorded, and a hub that migrates on every start never boots again.
-- Guarding a rename properly is a loop, and writing one for a case that cannot
-- occur is how a migration acquires a bug nothing will ever exercise.
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
