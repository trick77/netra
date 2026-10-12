-- The agent's build commit. netra identifies a build by its version alone,
-- and agents no longer send it.
ALTER TABLE hosts DROP COLUMN IF EXISTS build_commit;
