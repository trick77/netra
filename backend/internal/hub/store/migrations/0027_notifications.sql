-- Notifications: what has been told to whom, and what is muted.
--
-- The notifier is driven by these columns rather than by an in-memory hook on
-- the evaluator. A message that failed to send (SMTP down, a timeout) stays
-- pending and goes out on the next tick, and a hub restart neither repeats a
-- message nor forgets one.
--
-- notified_ts: when the last message about this open condition went out
-- (opened, escalated or a reminder). NULL means nobody has been told yet, so
-- every condition open when this migration runs is announced once the
-- notifier is configured -- that is the honest state of the fleet.
--
-- notified_severity: the severity that message carried, so a rise from
-- warning to critical is announced exactly once and an easing off is not.
--
-- cleared_notified_ts: when the all-clear went out. Only conditions somebody
-- was told about get one (notified_ts IS NOT NULL), so history resolved before
-- this migration, or while muted, sends nothing.
ALTER TABLE host_conditions
    ADD COLUMN IF NOT EXISTS notified_ts         TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS notified_severity   TEXT,
    ADD COLUMN IF NOT EXISTS cleared_notified_ts TIMESTAMPTZ;

-- The all-clears still owed. Partial, so it holds only the handful of rows
-- between "resolved" and "told", not the 90 days of history.
CREATE INDEX IF NOT EXISTS host_conditions_clear_pending_idx
    ON host_conditions (id)
    WHERE resolved_ts IS NOT NULL
      AND notified_ts IS NOT NULL
      AND cleared_notified_ts IS NULL;

-- A mute silences notifications for a host, or for one kind on a host. The
-- kind is a condition kind (disk, silent, ...) or an event type (disk_error,
-- oom_kill, ...); '' mutes the whole host.
--
-- until_ts NULL means until somebody lifts it. Expired rows are inert -- every
-- reader filters on until_ts -- and the notifier deletes them.
--
-- A muted condition is not forgotten: it stays unannounced and is announced
-- when the mute ends if it is still open. A muted all-clear is dropped, since
-- the problem it reports on is gone.
CREATE TABLE IF NOT EXISTS alert_mutes (
    id         INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id    INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL DEFAULT '',
    until_ts   TIMESTAMPTZ,
    note       TEXT NOT NULL DEFAULT '',
    created_ts TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS alert_mutes_host_idx ON alert_mutes (host_id);
