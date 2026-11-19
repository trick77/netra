-- Mutes on one alert rather than on a host or a whole kind.
--
-- subject: the condition's or event's subject the mute covers ('' for one
-- about the host as a whole, e.g. silent). NULL covers every subject of the
-- kind, which is what every mute before this migration meant; a mute without
-- a kind is always NULL here.
--
-- condition_id: set for a mute that lasts until that one condition resolves.
-- The same trouble coming back later is a new row in host_conditions, so a
-- new alert that is announced. The notifier deletes the mute once the
-- condition is resolved and its all-clear is no longer owed: an all-clear
-- under a mute is dropped, so the mute has to outlive the resolve by one pass.
ALTER TABLE alert_mutes
    ADD COLUMN IF NOT EXISTS subject      TEXT,
    ADD COLUMN IF NOT EXISTS condition_id INTEGER
        REFERENCES host_conditions (id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS alert_mutes_condition_idx
    ON alert_mutes (condition_id) WHERE condition_id IS NOT NULL;
