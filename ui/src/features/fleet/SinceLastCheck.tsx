// The age of the last poll, and the one figure on the rail that has to move
// on its own.
//
// It used to be computed in FleetPage's render from a `now` created there,
// against a `checkedAt` stamped the instant the fetch resolved. Those are the
// same clock read microseconds apart, and nothing re-rendered the page
// between polls -- so the figure read "0 s" at every paint, reset to 0 by the
// only event that could have moved it. The fact it exists to carry is
// precisely the one it could not: a poll that has stalled, or a tab that was
// in the background, shows its age climbing past the interval.
//
// A component with its own interval rather than a ticking `now` on the page.
// FleetPage's `now` also feeds isReporting, fleetTraffic and fleetConditions,
// so ticking it would re-render the whole host table once a second to move
// three characters.
import { useEffect, useRef, useState } from "react";
import { duration } from "../../lib/format";
import { StatFigure } from "../../ui/StatRail";

export interface SinceLastCheckProps {
  /**
   * When this page last read the fleet, as an ISO instant.
   *
   * The browser's own clock, both where it is set (App's poll, and
   * FleetPage's un-injected fallback) -- the hub does not send one. That is
   * what the figure claims: the age of THIS page's last poll, which is the
   * question "did the check run" actually asks.
   */
  checkedAt: string | null;
  /** Injectable so tests are deterministic instead of racing the clock. */
  now?: Date;
}

const TICK_MS = 1000;

export function SinceLastCheck({ checkedAt, now }: SinceLastCheckProps) {
  // The AGE is what decides everything below, not the timestamp: null and an
  // unparseable string are the same fact here, and guarding on
  // `checkedAt !== null` alone let a malformed one through.
  const stamp = parseStamp(checkedAt);

  const [clock, setClock] = useState(() => (now ?? new Date()).getTime());
  // Where the clock was seeded, and the real instant it was seeded at. The
  // tick advances the seed by the elapsed REAL time rather than reading the
  // wall clock, so an injected `now` stays authoritative for the life of the
  // component instead of holding for one second -- and, unlike adding
  // TICK_MS per tick, it does not drift when the browser throttles timers in
  // a background tab, which is exactly when this figure has something to say.
  const seed = useRef({ at: 0, real: 0 });

  useEffect(() => {
    // Re-seeded here, not only advanced: a poll that just landed has to snap
    // the figure back to zero at once rather than at the next tick.
    const at = (now ?? new Date()).getTime();
    seed.current = { at, real: Date.now() };
    setClock(at);
    // No interval when there is nothing to count. Left unconditional, a page
    // whose hub never said when it looked re-rendered once a second forever
    // to render nothing.
    if (stamp === null) return;
    const timer = setInterval(() => {
      setClock(seed.current.at + (Date.now() - seed.current.real));
    }, TICK_MS);
    return () => clearInterval(timer);
    // `checkedAt` only -- `stamp` is derived from it, and `now` must stay
    // out: FleetPage passes `now = new Date()`, a fresh value on every one of
    // its renders, so listing it would re-seed the clock whenever the parent
    // painted for any reason, which is the original bug rebuilt one level
    // down.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [checkedAt]);

  // Omitted entirely rather than shown as ABSENT -- an em dash under "since
  // last check" reads as "the check failed", which is a claim this page has
  // no basis for.
  if (stamp === null) return null;

  // Clamped at zero like relative()'s age is, for the same reason: a clock a
  // moment ahead of this one must not produce a negative age.
  const age = Math.max(0, Math.round((clock - stamp) / 1000));

  // `duration`, not `relative`: the rail's shape is a figure and a phrase
  // finishing it, and "0 s ago since last check" is not a sentence. No href
  // -- there is no list of "when we last looked" to go to, and a figure that
  // looks clickable and does nothing is worse than one that plainly is not.
  return <StatFigure value={duration(age)} label="since last check" />;
}

function parseStamp(iso: string | null): number | null {
  if (iso === null) return null;
  const ms = new Date(iso).getTime();
  return Number.isNaN(ms) ? null : ms;
}
