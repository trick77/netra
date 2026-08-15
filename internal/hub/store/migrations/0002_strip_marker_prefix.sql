-- Rename the filesystems an older agent named after its own container.
--
-- The agent measures a host filesystem through an empty .netra marker file
-- bind-mounted to /netra/fs/<label> inside its container. statfs on that target
-- reads the real host filesystem, so every number stored against these rows is
-- correct. The NAME was not: the agent reported the bind target verbatim, so a
-- host with no netra anywhere was told "/netra/fs/ark is 94 % full".
--
-- Agents from this release onward send the label (`ark`) and the host mount
-- point (`/mnt/ark`). Without this migration those arrive as a NEW row in
-- filesystems -- the table is unique on (host_id, label) -- and every existing
-- graph restarts from empty. Renaming in place keeps fs_id, and fs_id is what
-- the samples, the 5m rollup and the 1h rollup are all keyed by, so the whole
-- history follows the rename with no reparenting and no refresh.

-- A host that already has a row under the stripped name would make the UPDATE
-- below violate the unique index, so one of the two has to go.
--
-- The STRIPPED one goes, never the prefixed one. A marker-less agent writes the
-- whole mount point as the label (`/mnt/ark`), so it cannot produce `ark`; the
-- only thing that does is an agent from THIS release that was upgraded ahead of
-- its hub. That makes the prefixed row the one carrying the history -- months of
-- it -- and the stripped row the one holding whatever landed since that agent
-- restarted. Dropping the prefixed row would destroy exactly what this migration
-- exists to preserve.
--
-- Deleted, NOT merged. filesystem_samples is PRIMARY KEY (host_id, ts, fs_id),
-- so repointing fs_id collides on every timestamp the two rows share -- and they
-- overlap by construction, since both were being written during the same window.
-- The FK is ON DELETE CASCADE, so the loser's samples go with it.

-- The stripped twin knows the real mount point, because the new agent sent it.
-- Carry it over before deleting it: the survivor otherwise displays the bare
-- label until the agent's next scrape, and this costs one statement.
UPDATE filesystems f
   SET mountpoint = g.mountpoint
  FROM filesystems g
 WHERE f.label LIKE '/netra/fs/%'
   AND g.host_id = f.host_id
   AND g.label = regexp_replace(f.label, '^/netra/fs/', '')
   AND g.mountpoint IS NOT NULL
   AND g.mountpoint NOT LIKE '/netra/fs/%';

DELETE FROM filesystems g
 WHERE g.label NOT LIKE '/netra/fs/%'
   AND EXISTS (
       SELECT 1
         FROM filesystems f
        WHERE f.host_id = g.host_id
          AND f.label LIKE '/netra/fs/%'
          AND regexp_replace(f.label, '^/netra/fs/', '') = g.label
   );

-- regexp_replace with an anchor, never a character offset: /netra/fs/ is ten
-- characters, and an off-by-one here turns `ark` into `rk`, which is wrong in a
-- way that still looks like a plausible filesystem name in the UI.
--
-- mountpoint is corrected properly by the agent on its next scrape, once its
-- .env carries NETRA_FS_MOUNTS. Stripping it here only stops the prefix being
-- displayed in the meantime.
UPDATE filesystems
   SET label      = regexp_replace(label, '^/netra/fs/', ''),
       mountpoint = regexp_replace(mountpoint, '^/netra/fs/', '')
 WHERE label LIKE '/netra/fs/%'
    OR mountpoint LIKE '/netra/fs/%';
