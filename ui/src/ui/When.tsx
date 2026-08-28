import { ABSENT, absolute, relative } from "../lib/format";

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
