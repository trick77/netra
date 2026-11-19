// The mutes a page shows, polled, with the one action every list of them
// offers: lift. Shared by the host page and the admin page's fleet list so a
// fix to either is a fix to both.
import { useState } from "react";

import {
  ApiError,
  deleteMute,
  errorText,
  getMutes,
  type Mute,
} from "../../lib/api";
import { POLL_MS, usePoll } from "../../lib/poll";

export interface Mutes {
  mutes: Mute[];
  // A failed read. The caller must say so rather than show "nothing muted":
  // a list that silently empties is how a forgotten mute stays forgotten.
  loadError: string | null;
  // Failed lifts, one per mute still listed. A lift that fails on one row
  // is not erased by another row's success, and a row that has since gone
  // takes its error with it.
  liftErrors: string[];
  // Every lift in flight, so two clicks in a row each keep their spinner.
  lifting: ReadonlySet<number>;
  lift: (id: number) => Promise<void>;
  /** A mute just created here: listed at once, then re-read. */
  add: (m: Mute) => void;
  refresh: () => void;
}

/**
 * hostId narrows the read to one host; `deps` re-reads when something else
 * on the page changed the mutes (a deleted host takes its mutes with it).
 */
export function useMutes(
  hostId?: number,
  deps: readonly unknown[] = [],
): Mutes {
  const poll = usePoll(() => getMutes(hostId), POLL_MS, [hostId, ...deps]);
  const [lifting, setLifting] = useState<ReadonlySet<number>>(new Set());
  // Lifted here, hidden at once rather than after the re-read lands: a row
  // whose Unmute is clickable again for the length of a poll invites a
  // second click that can only answer 404.
  const [gone, setGone] = useState<ReadonlySet<number>>(new Set());
  const [errors, setErrors] = useState<ReadonlyMap<number, string>>(new Map());
  // Created here and not yet read back, each with the read it was added
  // over. Kept until a newer read lands: the alert line it was set on would
  // otherwise offer Mute again for the length of a poll.
  const [added, setAdded] = useState<
    readonly { mute: Mute; over: Mute[] | null }[]
  >([]);

  function setError(id: number, text: string | null) {
    setErrors((m) => {
      const next = new Map(m);
      if (text === null) next.delete(id);
      else next.set(id, text);
      return next;
    });
  }

  async function lift(id: number) {
    setLifting((s) => new Set(s).add(id));
    try {
      await deleteMute(id);
      setGone((s) => new Set(s).add(id));
      setError(id, null);
    } catch (e) {
      // Already gone -- expired, or lifted in another tab: what the user
      // asked for is true, so it is not an error to show.
      if (e instanceof ApiError && e.status === 404) {
        setGone((s) => new Set(s).add(id));
        setError(id, null);
      } else {
        setError(id, errorText(e));
      }
    } finally {
      setLifting((s) => {
        const next = new Set(s);
        next.delete(id);
        return next;
      });
      // Either way the list may be stale; read it again.
      poll.refresh();
    }
  }

  function add(m: Mute) {
    setAdded((a) => [...a, { mute: m, over: poll.data }]);
    poll.refresh();
  }

  const read = poll.data ?? [];
  const fresh = added
    .filter(
      (a) => a.over === poll.data && !read.some((m) => m.id === a.mute.id),
    )
    .map((a) => a.mute);
  const mutes = [...fresh, ...read].filter((m) => !gone.has(m.id));
  const listed = new Set(mutes.map((m) => m.id));
  const liftErrors = [...errors]
    .filter(([id]) => listed.has(id))
    .map(([, text]) => text);

  return {
    mutes,
    loadError: poll.error === null ? null : poll.error.message,
    liftErrors,
    lifting,
    lift,
    add,
    refresh: poll.refresh,
  };
}
