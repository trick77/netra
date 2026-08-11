/**
 * The one time range.
 *
 * Wave 4's five pages each needed a range before anything shared one, so
 * four incompatible types grew in parallel: the fleet's 1h/6h/24h, the host
 * page's 1h/6h/24h/7d, the events page's 1h/24h/7d/30d, and Settings'
 * 1h/6h/24h/7d/30d for the stored default. They agreed on the strings and
 * disagreed on the set, which meant a range chosen in Settings could not be
 * handed to a page that had never heard of it -- and the stored default is
 * exactly a value that crosses pages.
 *
 * This is the union of all of them. A page still chooses which options to
 * OFFER (a metrics chart over 30 days is a rollup nobody asked for; the
 * events log over an hour is usually empty), but every page can now be
 * HANDED any of them, which is what a shared default requires.
 */
export type Range = "1h" | "6h" | "24h" | "7d" | "30d";

/** Every range, in ascending order — the order a picker should show them. */
export const RANGES: readonly Range[] = ["1h", "6h", "24h", "7d", "30d"];

export function isRange(value: unknown): value is Range {
  return (
    typeof value === "string" && (RANGES as readonly string[]).includes(value)
  );
}

/**
 * `seconds` is how far back the range reaches; `step` is the bucket the hub
 * should aggregate to, as a Go duration matching parseStep.
 *
 * The steps are chosen so a chart is roughly 60-300 points wide whatever the
 * range: fine enough that a spike survives, coarse enough that the response
 * is not truncated. Which rollup tier the hub picks is its decision, not
 * this table's -- selectTier reads the step and answers from raw, 5m or 1h.
 */
const SPEC: Record<Range, { seconds: number; step: string }> = {
  "1h": { seconds: 3600, step: "60s" },
  "6h": { seconds: 6 * 3600, step: "5m" },
  "24h": { seconds: 24 * 3600, step: "5m" },
  "7d": { seconds: 7 * 24 * 3600, step: "1h" },
  "30d": { seconds: 30 * 24 * 3600, step: "1h" },
};

/** Human label for a range, for a picker or a chart's accessible name. */
export function rangeLabel(range: Range): string {
  return `last ${range}`;
}

/**
 * Resolves a relative range into the absolute window the hub demands.
 *
 * The read API rejects relative times outright -- `from=-24h` answers
 * "from must be RFC 3339 or unix milliseconds" -- so the conversion happens
 * here, once, rather than at each call site. `now` is a parameter so a test
 * can pin it; nothing else should pass it.
 */
export function rangeWindow(
  range: Range,
  now: Date = new Date(),
): { from: string; to: string; step: string } {
  const { seconds, step } = SPEC[range];
  return {
    from: new Date(now.getTime() - seconds * 1000).toISOString(),
    to: now.toISOString(),
    step,
  };
}

/** Milliseconds a range spans — for a filter that works client-side. */
export function rangeMs(range: Range): number {
  return SPEC[range].seconds * 1000;
}
