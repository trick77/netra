import { ABSENT, absolute, instant, relative } from "../lib/format";

/**
 * A timestamp reads relative, with the absolute time on hover (spec §9).
 *
 * Lives here rather than beside one table because three of them now render a
 * seen-at column -- drives, packages and containers -- and a second copy is
 * how two columns headed the same thing come to format differently.
 */
export function When({ iso }: { iso: string | null }) {
  // Wrapped rather than bare, so a column of never-reported timestamps dims
  // the same way every other absent cell does -- Table only dims a cell whose
  // own output IS the string, and this component's is an element.
  if (iso === null) return <span className="absent">{ABSENT}</span>;
  return <span title={absolute(iso)}>{relative(iso)}</span>;
}

/**
 * An EVENT's timestamp, which reads the other way round: the exact local
 * wall-clock instant, with the age on hover.
 *
 * `When` is right for a "last seen" column, where the question is how stale a
 * reading is and "4 h ago" answers it directly. An event is a thing that
 * HAPPENED, and the question asked of the log is when -- to line a row up
 * against a deploy, a reboot, or someone else's incident timeline. "1 d 7 h
 * ago" cannot be lined up against anything without arithmetic, and it drifts
 * under the reader while they do it.
 *
 * The zone is the browser's: `instant` passes no timeZone, so the value comes
 * from the reader's own locale settings and matches what every other clock on
 * their machine says.
 *
 * `instant` rather than `absolute`, which is what a hover title uses: down a
 * column the month NAME is most of the width and is a word among digits. See
 * its doc comment.
 *
 * `now` is threaded rather than defaulted so the hover age matches the page's
 * single clock instead of the instant this cell happened to render.
 */
export function EventTime({ iso, now }: { iso: string; now?: Date }) {
  return (
    <time className="evtime" dateTime={iso} title={relative(iso, now)}>
      {instant(iso)}
    </time>
  );
}
