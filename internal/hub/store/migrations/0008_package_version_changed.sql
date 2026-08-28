-- When a package's VERSION last changed, which host_packages could not answer.
--
-- The table is keyed (host_id, name, arch), so an upgrade rewrites the row in
-- place: first_seen keeps the original install date and last_seen moves on
-- every daily re-emit, changed or not. Neither says "this package was upgraded
-- on Tuesday", which is the question a reader opens the packages list with.
--
-- Existing rows backfill to first_seen. The install date is the only change
-- date netra holds for a package it has never watched upgrade, and it is the
-- honest answer until that package's next version arrives.
ALTER TABLE host_packages ADD COLUMN IF NOT EXISTS version_changed_at TIMESTAMPTZ;

UPDATE host_packages SET version_changed_at = first_seen WHERE version_changed_at IS NULL;

ALTER TABLE host_packages ALTER COLUMN version_changed_at SET DEFAULT now();
ALTER TABLE host_packages ALTER COLUMN version_changed_at SET NOT NULL;
