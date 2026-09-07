-- Severity becomes a column on events, rather than a convention inside detail.
--
-- It has ridden `detail.severity` since the kmsg collector landed, and that
-- worked because both event views read the key before anything else. It stops
-- working the moment something other than a browser has to ask "what is
-- critical since T": alerting cannot call into the UI, and a jsonb path over a
-- table with no index for it is the wrong shape for the one query alerting
-- makes most.
--
-- The detail key is NOT removed. Producers keep writing it for one release so
-- an agent older than the proto field still classifies correctly through the
-- ingest fallback, and the UI keeps reading it -- message.ts already strips
-- `severity` from rendered sentences via NOT_FACTS, which is this codebase
-- saying it was always metadata rather than a fact about the event.
--
-- Text with a CHECK rather than an enum type: nothing else in this schema
-- creates a type or a domain, and these three words are already the vocabulary
-- of the wire, the API and the UI. An enum would also make adding a fourth
-- level an ALTER TYPE, which is exactly the migration nobody wants to be
-- holding a lock for.
-- The CHECK rides the ADD COLUMN rather than arriving as a separate NOT VALID
-- constraint validated afterwards. That split is the right pattern for adding
-- a constraint to a populated column, because VALIDATE takes only SHARE UPDATE
-- EXCLUSIVE where a plain ADD CONSTRAINT holds ACCESS EXCLUSIVE through the
-- scan -- and it buys exactly nothing here. applyMigration runs a whole file
-- in ONE transaction unless it carries the no-transaction marker
-- (internal/hub/store/migrate.go), so the ACCESS EXCLUSIVE lock this ALTER
-- takes is held until commit regardless, through both backfills and the index
-- below. Splitting it would have looked careful while changing nothing.
--
-- Nothing needs scanning in any case: every existing row takes the DEFAULT,
-- which satisfies the constraint by construction.
ALTER TABLE events
    ADD COLUMN IF NOT EXISTS severity TEXT NOT NULL DEFAULT 'info'
        CHECK (severity IN ('info', 'warning', 'critical'));

-- Backfill, in the same migration as the column.
--
-- A severity that reads 'info' for everything written before today is a column
-- that lies about history -- and the rows it would lie about are the ones
-- worth finding: every ATA exception and block-layer error the kmsg collector
-- has recorded states its severity in the detail already.
UPDATE events
   SET severity = detail ->> 'severity'
 WHERE detail ->> 'severity' IN ('info', 'warning', 'critical')
   AND severity <> detail ->> 'severity';

-- mdraid is the one producer that never stated a severity, because the
-- judgement lived in the browser (mdraidCondition in ui/.../events/message.ts).
-- The rule ports here verbatim, and into the collector in the same change.
--
-- `state` is deliberately not consulted. It is sysfs array_state, whose
-- vocabulary -- clean, active, active-idle, write-pending, ... -- does not
-- contain the word "degraded": the kernel calls a raid1 with one disk left
-- `clean`, because clean is about consistency and not about how many disks are
-- left. The device count is the only honest source for "is this array in
-- trouble", and sync_action for "is it fixing itself".
UPDATE events
   SET severity = CASE
       WHEN (detail ->> 'sync_action') IN ('recover', 'resync', 'repair')
           THEN 'warning'
       ELSE 'critical'
   END
 WHERE type = 'mdraid'
   AND detail ->> 'severity' IS NULL
   AND coalesce((detail ->> 'degraded')::int, 0) > 0;

-- "What is critical since T", which is the whole of alerting's read pattern.
--
-- Partial, because most rows are info and an index over them would be mostly
-- dead weight on a table whose other index already answers the per-host log.
-- ts DESC leads because the driving predicate is always the time bound; the
-- severity filter narrows what the scan returns rather than how it starts.
CREATE INDEX IF NOT EXISTS events_notable_ts_idx
    ON events (ts DESC)
    WHERE severity <> 'info';
