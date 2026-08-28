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
