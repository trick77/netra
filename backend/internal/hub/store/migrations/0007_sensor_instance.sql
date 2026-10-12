-- Sensor identity gains the block device a chip is attached to, because chip +
-- label is not unique for storage chips and never was.
--
-- The drivetemp driver names every chip it registers "drivetemp" and publishes
-- no tempN_label, so the agent's identity rules -- which correctly refuse to
-- key on hwmonN, since the N moves across reboots -- had nothing left to tell
-- two disks apart. A host with four SATA drives reported four sensors calling
-- themselves drivetemp/temp1, resolveSensorIDs collapsed them onto ONE row,
-- and sensor_samples' ON CONFLICT (host_id, ts, sensor_id) DO NOTHING then
-- kept exactly one reading per scrape and dropped the other three. Four
-- drives, one temperature, nothing raised. Two NVMe drives collided the same
-- way as nvme/Composite.
--
-- DEFAULT '' RATHER THAN NULL, and NOT NULL. The column is part of a unique
-- index, and in Postgres two NULLs are distinct: with a nullable column every
-- coretemp row on every host would stop matching itself on the next scrape and
-- mint a duplicate sensor. The empty string is a value, and it compares equal.
ALTER TABLE sensors ADD COLUMN IF NOT EXISTS instance TEXT NOT NULL DEFAULT '';

-- Every existing row keeps '', which is also what an agent predating the wire
-- field sends. So nothing that works today moves: coretemp, k10temp, acpitz
-- and every fan and rail keep the identity and the history they have.
--
-- What does NOT happen is a repair of the rows already collapsed. The old
-- drivetemp sensor keeps its accumulated series and stops receiving samples
-- once its agent is upgraded; the per-drive rows start fresh beside it and the
-- stale one ages out under sensor_samples' own 7/30/90 day retention. Trying
-- to split it would mean deciding which of four drives each historical sample
-- came from, and that information was never recorded -- it is exactly what was
-- lost.
DROP INDEX IF EXISTS sensors_host_id_chip_label_key;

CREATE UNIQUE INDEX IF NOT EXISTS sensors_host_id_chip_label_instance_key
    ON sensors (host_id, chip, label, instance);
