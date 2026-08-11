import { useCallback, useEffect, useRef, useState } from "react";

/**
 * Polls an async function on an interval, and stops while nobody is looking.
 *
 * The pause on document.hidden is the point. A fleet overview open in a
 * background tab for a weekend is a request every minute for two days, per
 * tab, against a hub that is also ingesting from every agent it owns. The
 * work is invisible to whoever left the tab open and indistinguishable, at
 * the hub, from load that matters.
 *
 * A 401 lands in `error` like any other failure, as an ApiError carrying its
 * status: it is a routing decision (the session expired, show the login
 * page) and the caller distinguishes it BY TYPE, never by parsing a message.
 * It is deliberately not thrown -- the poll runs inside a timer callback,
 * where a throw is an unhandled rejection nobody can catch, and the
 * component that would route on it is not on that stack.
 *
 * A failed poll -- deliberately -- LEAVES the last good data in place: a
 * chart that empties itself because one request timed out tells the reader
 * the host stopped reporting, which is a different and far more alarming
 * fact than "the last refresh did not land".
 */
export interface Poll<T> {
  data: T | null;
  error: Error | null;
  loading: boolean;
  refresh: () => void;
}

export function usePoll<T>(
  fn: () => Promise<T>,
  intervalMs: number,
  deps: readonly unknown[] = [],
): Poll<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);

  // The latest fn, without making it a dependency: callers pass an inline
  // arrow, which is a new function every render, and depending on it would
  // restart the interval on every render -- a poll that fires continuously
  // rather than on its interval.
  const fnRef = useRef(fn);
  fnRef.current = fn;

  const refresh = useCallback(() => setTick((t) => t + 1), []);

  useEffect(() => {
    // Not cancelled-as-a-ref-on-the-hook: one per effect run, so a run
    // superseded by a dependency change cannot write its late answer over
    // the newer one's. Without it, switching hosts twice quickly leaves the
    // first host's data on the second host's page.
    let cancelled = false;

    async function run() {
      try {
        const next = await fnRef.current();
        if (!cancelled) {
          setData(next);
          setError(null);
        }
      } catch (err) {
        if (cancelled) return;
        setError(err instanceof Error ? err : new Error(String(err)));
      } finally {
        if (!cancelled) setLoading(false);
      }
    }

    void run();

    let timer: ReturnType<typeof setInterval> | undefined;
    const start = () => {
      if (timer === undefined)
        timer = setInterval(() => void run(), intervalMs);
    };
    const stop = () => {
      if (timer !== undefined) {
        clearInterval(timer);
        timer = undefined;
      }
    };

    // A tab hidden at mount must not start a timer at all, and a tab
    // revealed after an hour refreshes immediately rather than waiting out
    // the interval with stale numbers on screen.
    const onVisibility = () => {
      if (document.hidden) {
        stop();
      } else {
        void run();
        start();
      }
    };
    if (!document.hidden) start();
    document.addEventListener("visibilitychange", onVisibility);

    return () => {
      cancelled = true;
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intervalMs, tick, ...deps]);

  return { data, error, loading, refresh };
}

/** The overview's cadence. The agent reports every 60s, so anything faster
 * asks the hub for numbers that cannot have changed. */
export const POLL_MS = 60_000;
