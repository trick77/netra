-- Drop process_samples: the per-process-name table nothing rendered.
--
-- The agent filled it by reading /proc/<pid>/stat for every process on the
-- host, once a minute. The kernel ptrace-checks each of those reads
-- (do_task_stat, to decide whether to expose the wchan/address fields), and a
-- docker-default container fails that check against every unconfined host
-- process -- one apparmor="DENIED" ptrace line a minute in the host's kernel
-- log, which is the log netra exists to help the operator read. The collector
-- is gone, so nothing writes here any more.
--
-- Forward-only: 0001_init.sql still creates the table, and a fresh install
-- creates it and then drops it here. Editing 0001 in place would diverge from
-- every database already running it.
--
-- Guarded on the table existing rather than on the policy: remove_retention_policy
-- raises on a missing relation even with if_exists, and this migration must be
-- re-runnable against a database that has already dropped the table by hand.
DO $$
BEGIN
    IF to_regclass('public.process_samples') IS NOT NULL THEN
        PERFORM remove_retention_policy('process_samples', if_exists => TRUE);
    END IF;
END
$$;

DROP TABLE IF EXISTS process_samples;
