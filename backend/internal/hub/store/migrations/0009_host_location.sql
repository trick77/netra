-- Where a host is, as the AGENT reports it.
--
-- The agent has read AGENT_LOCATION, AGENT_PROVIDER and AGENT_FACILITY since
-- it was written (internal/agent/config/config.go), puts all three on every
-- Metadata post (internal/agent/client/metadata.go), and the proto has carried
-- them as fields 12-14 the whole time. The hub received them and stored none
-- of them: SaveMetadata's UPDATE simply never listed the columns, because the
-- columns never existed. Three variables an operator could set, with nothing
-- anywhere that could ever show them.
--
-- The alternative already in the schema is the sites/providers pair, which is
-- a different mechanism with a different owner: a human fills it in through
-- the admin UI and assigns hosts to it. These columns do not feed it and do
-- not replace it -- nothing here creates a site. They are what the machine
-- says about itself, which is the answer that needs no one to maintain it.
--
-- Nullable with no default, and no backfill. NULL is "the agent did not report
-- one", which is the truth for every existing row and stays the truth for any
-- host whose operator sets none of the variables. An empty string would be a
-- host claiming its location is "", and the NULLIF in SaveMetadata exists to
-- keep that out.
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS location TEXT;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS provider TEXT;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS facility TEXT;

-- Now make every agent say it again, because otherwise not one of them ever
-- will and these columns stay NULL on every existing host forever.
--
-- Metadata is sent once and then only on change, and "change" is decided by a
-- hash the AGENT computes over the whole Metadata message -- which has carried
-- location, provider and facility all along (client/metadata.go: HashMetadata).
-- So a host that reported "Roubaix, France" months ago has a stored hash that
-- still matches exactly what its agent would send today. reconcileMetadata
-- (httpapi/ingest.go) asks for metadata only when the stored hash DIFFERS or is
-- empty, and the agent's own sendMetadata flag starts false and is set only by
-- such a request -- so not even an agent restart resends it. Adding the columns
-- alone would leave every upgraded hub exactly as blank as before, which is the
-- bug this migration exists to fix.
--
-- Clearing the hash makes `len(stored) == 0` true once per host, the hub asks
-- for metadata on that host's next post, and the agent answers with what it has
-- been holding all along. One extra Metadata block per host, once.
--
-- NULL rather than a sentinel: the column is nullable and empty is already the
-- "never stored one" state the same branch tests for, so this asks the question
-- the code was already able to answer.
UPDATE hosts SET metadata_hash = NULL;
