import { ABSENT, absolute, relative } from "../lib/format";

/**
 * A timestamp reads relative, with the absolute time on hover (spec §9).
 *
 * Lives here rather than beside one table because three of them now render a
 * seen-at column -- drives, packages and containers -- and a second copy is
 * how two columns headed the same thing come to format differently.
 */
export function When({ iso }: { iso: string | null }) {
  if (iso === null) return <>{ABSENT}</>;
  return <span title={absolute(iso)}>{relative(iso)}</span>;
}
