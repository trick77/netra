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
export type Range = "1h" | "6h" | "12h" | "24h" | "7d" | "30d" | "3mo" | "12mo";

/** Every range, in ascending order — the order a picker should show them. */
export const RANGES: readonly Range[] = [
  "1h",
  "6h",
  "12h",
  "24h",
  "7d",
  "30d",
  "3mo",
  "12mo",
];

/**
 * The ranges a PAGE can be set to, in ascending order.
 *
 * Everything past 30d belongs to one enlarged chart and not to a page: a
 * fleet list drawn over a year is twenty rows of the same flat smear, and no
 * page offers a window that wide in its own picker. Offering one as the
 * stored default would put two buttons in Settings that every page then
 * widens back down -- a setting that reads as ignored.
 */
export const PAGE_RANGES: readonly Range[] = [
  "1h",
  "6h",
  "12h",
  "24h",
  "7d",
  "30d",
];

/**
 * The ladder the enlarged view's rail of mini-charts draws, in ascending
 * order.
 *
 * Fixed, and deliberately not the page's offered set. The rail is a property
 * of looking at ONE chart closely: every tile is a separate request for one
 * series, so the reason a page narrows its own picker -- the fleet list
 * refusing a 30-day fan-out across every host -- does not apply to it. What a
 * reader wants from an enlarged chart is the whole span of the question, from
 * "what is happening right now" to "has this disk always been this full".
 *
 * It stops at a year. Two years was on it and came off: the second year is
 * one more nearly identical smear on a tile 45px tall, and it is the rung
 * that costs the most to keep -- the daily tier's retention is sized by the
 * widest window on this list.
 *
 * 12h is the one narrow range missing from it. It exists for the fleet
 * list's fixed window and nothing else; on this ladder it would sit between
 * 6h and 24h saying almost nothing neither of its neighbours says. A dialog
 * opened from the fleet still opens AT 12h -- the rail simply has no tile
 * pressed until the reader picks one.
 */
export const RAIL_RANGES: readonly Range[] = [
  "1h",
  "6h",
  "24h",
  "7d",
  "30d",
  "3mo",
  "12mo",
];

export function isRange(value: unknown): value is Range {
  return (
    typeof value === "string" && (RANGES as readonly string[]).includes(value)
  );
}

/**
 * Resolves a range against the set a page actually OFFERS.
 *
 * The stored default crosses pages, and the pages disagree about which
 * windows make sense: the fleet stops at 24h, the events log starts there.
 * So a remembered 7d arriving at the fleet has to become something the
 * fleet can both fetch and show as pressed -- a Segmented handed a value
 * outside its options renders every button unpressed, which reads as "no
 * range selected".
 *
 * It WIDENS: the narrowest offered range at least as wide as the one asked
 * for, and the widest offered when the ask is wider than anything on the
 * page. Narrowing would send a remembered 6h to the events page's 1h, and
 * an hour of events is usually empty -- which is exactly why that page does
 * not offer 6h in the first place. Widening lands on 24h instead, which is
 * the answer someone asking for 6h of events wanted.
 *
 * The stored preference is NOT rewritten by this. Clamping is what one page
 * displays, not a change of mind: going back to the host page must still
 * show the 7d that was picked there.
 */
export function clampRange(requested: Range, offered: readonly Range[]): Range {
  if (offered.includes(requested)) return requested;
  // RANGES is ascending, so the first offered range at or past the asked-for
  // one is the narrowest that is still at least as wide.
  const asked = RANGES.indexOf(requested);
  let widest: Range | undefined;
  for (const range of RANGES) {
    if (!offered.includes(range)) continue;
    widest = range;
    if (RANGES.indexOf(range) >= asked) return range;
  }
  // Nothing offered is as wide: the widest there is. `offered` empty is not
  // a case any page produces, and falling back to the request keeps this
  // total rather than throwing at a caller that cannot act on it.
  return widest ?? requested;
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
  // The fleet's fixed window. 144 five-minute buckets, which is close enough
  // to the 150px the row's sparkline is drawn in that a bucket is about a
  // pixel and the fold has almost nothing to throw away.
  "12h": { seconds: 12 * 3600, step: "5m" },
  "24h": { seconds: 24 * 3600, step: "5m" },
  "7d": { seconds: 7 * 24 * 3600, step: "1h" },
  "30d": { seconds: 30 * 24 * 3600, step: "1h" },
  // The daily tier, from here up. Three months of hourly buckets is 2160
  // points -- past the 60-300 rule by an order of magnitude, and a response
  // nobody wants to send -- so the step steps too. "24h" rather than "1d"
  // because the hub parses this with time.ParseDuration, which has no day
  // unit; selectTier reads it and answers from the _1d aggregate.
  "3mo": { seconds: 90 * 24 * 3600, step: "24h" },
  "12mo": { seconds: 365 * 24 * 3600, step: "24h" },
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

/**
 * How long a chart drawn over this range stays true: the width of its
 * newest bucket, in milliseconds.
 *
 * A poller reads it to stop asking for a picture that cannot have changed. A
 * 7d view is drawn from hourly buckets, so refetching twelve metric families
 * every minute redraws the same 168 points sixty times an hour -- the hub
 * does the work, the browser does the parsing, and the line moves once.
 *
 * Read off the same SPEC the request's own `step` comes from, so a range
 * whose step is retuned cannot end up polled at a cadence chosen for the old
 * one.
 */
export function rangeStepMs(range: Range): number {
  const step = SPEC[range].step;
  const value = Number.parseInt(step, 10);
  // The suffixes SPEC actually uses. Go's time.ParseDuration has no day unit,
  // which is why the longest ranges say "24h" rather than "1d" -- so hours
  // are the coarsest thing that can appear here.
  const unit = step.endsWith("s")
    ? 1000
    : step.endsWith("m")
      ? 60_000
      : 3_600_000;
  return value * unit;
}

/**
 * How many rows an events request should ask for, per window.
 *
 * A flat limit makes the wide buttons a lie: events accumulate with the
 * window, so 500 rows over 30 days is the newest few days and a silent cut
 * everywhere else -- the reader widens the range and sees the same page.
 * These scale with the span so a wide window returns a wide answer.
 *
 * 5000 is maxEventLimit in internal/hub/read/events.go; the hub clamps rather
 * than rejects, but asking for more than it will give is a lie in the other
 * direction. Covers every Range, not just the four the events page offers, so
 * the record stays total if the offered set changes.
 *
 * Here rather than in EventsPage because both the fleet log and the host
 * page's Events tab read it and neither owns it -- the same reason messageOf
 * and PackageRunFold are their own modules. A host page reaching into a page
 * component for a constant is the import that gets awkward later, and a table
 * keyed by Range belongs beside Range.
 */
export const EVENT_LIMITS: Record<Range, number> = {
  "1h": 500,
  "6h": 500,
  "12h": 500,
  "24h": 500,
  "7d": 2000,
  "30d": 5000,
  // 5000 is maxEventLimit: everything past a month asks for all of it. The
  // events page offers none of these, but the record is total by
  // construction, so a range added to the type has to answer here too.
  "3mo": 5000,
  "12mo": 5000,
};
