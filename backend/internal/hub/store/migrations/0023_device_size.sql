-- The drive table said what a drive was and never how big it was.
--
-- smartctl has reported user_capacity on every ATA and NVMe drive all along,
-- on the same --all output the agent already parses model and serial out of.
-- Reading it costs one struct tag, and the Storage tab gets the column an
-- operator looks for first when a mount fills up.

-- Capacity in bytes as smartctl reports it. NULL until the first reading that
-- carries one: an agent older than this field, or a smartctl that could not
-- size the drive, sends nothing, and the upsert keeps whatever is here rather
-- than nulling a size it knew yesterday. Never 0.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS size_bytes BIGINT;
