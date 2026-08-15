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

-- A host that already has a row under the stripped name -- an agent that was
-- reinstalled without the marker scheme, or a hand-inserted row -- would make
-- the UPDATE below violate the unique index. Drop the prefixed duplicate first.
--
-- Deleted, NOT merged onto the survivor. filesystem_samples is
-- PRIMARY KEY (host_id, ts, fs_id), so repointing fs_id collides on every
-- timestamp the two rows share -- and they overlap by construction, since both
-- were being written during the same window. The FK is ON DELETE CASCADE, so
-- the duplicate's samples go with it.
DELETE FROM filesystems f
 WHERE f.label LIKE '/netra/fs/%'
   AND EXISTS (
       SELECT 1
         FROM filesystems g
        WHERE g.host_id = f.host_id
          AND g.label = regexp_replace(f.label, '^/netra/fs/', '')
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
