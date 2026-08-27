import { Fragment, type ReactNode } from "react";

/** `wide` lays the pairs out two across instead of one, for a card that spans
 * the page rather than half of it. See .kv.wide.
 *
 * Its own file since the Collectors tab left Overview.tsx: the capability
 * list and the cards that stayed behind must draw the same pairs, and two
 * copies of six lines of markup is how one of them comes to lose its
 * separators. */
export function Facts({
  rows,
  wide = false,
}: {
  rows: [string, ReactNode][];
  wide?: boolean;
}) {
  return (
    <dl className={wide ? "kv wide" : "kv"}>
      {/* Fragment, not a `display: contents` div. The div was invisible in
        every sense but the one that mattered: display affects box generation,
        not selector matching, so each dt was the only dt under its own
        wrapper and `.kv dt:last-of-type` matched EVERY row -- no card built
        here drew the separators .kv specifies, while ContainerPage, which
        writes its dt/dd out by hand as direct children, drew them normally.
        The same class, two appearances, decided by which file you were in. */}
      {rows.map(([key, value]) => (
        <Fragment key={key}>
          <dt>{key}</dt>
          <dd>{value}</dd>
        </Fragment>
      ))}
    </dl>
  );
}
