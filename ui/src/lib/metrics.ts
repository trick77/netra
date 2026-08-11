import type { MetricsResponse } from "./api";

// MetricsResponse (and its satellite MetricsWindow/MetricsSeries types) is
// owned by ./api.ts, which transcribes internal/hub/read/metrics.go's Result
// verbatim, including `Points [][]any` -- a point's cells are `unknown[]`,
// not `number[]`, because a column is not always numeric. family=collector
// is the case that proves it: at raw it yields `ok` as a boolean and
// `error_code` as a string beside a numeric column. This module used to
// carry its own copy of the response shape typed as
// `points: (number | null)[][]`, which made seriesValues() lie to the
// compiler for exactly that family. One definition of the wire shape lives
// in ./api.ts now; this module only interprets it.
//
// --- Error strategy ---
//
// Three different strategies coexist across this module and its neighbours;
// this is the one place stating which is which, for a Wave 2 author wiring
// up a chart or table without having read every function's doc comment:
//
//   - api.ts throws ApiError for a failed HTTP request/response -- a
//     network or server-side failure, always surfaced before any render
//     begins. Guard the fetch call itself (getMetrics, getHosts, ...).
//   - This module throws UnknownColumnError / SeriesIndexError /
//     NonNumericColumnError from column() / seriesCells() / seriesValues().
//     These fire DURING render, from a caller-supplied base name or
//     seriesIndex the response doesn't actually have. They are programmer
//     errors -- a typo'd column, a stale index, a numeric accessor pointed
//     at a non-numeric column -- so they belong behind a component-level
//     error boundary, not a try/catch a user-facing message is built from.
//   - format.ts never throws: null and unparseable input both become
//     ABSENT ("—"). A formatter call never needs a boundary.
//   - windowNotice() (below) never throws either: it returns null for
//     "nothing to say". Treat null as "no banner", not as an error.
//
// Rule of thumb: a throw means "put this behind an error boundary"; a
// null/ABSENT return means "render it as-is".

/**
 * Thrown by column() when a base name has no match in the response's tier.
 * Named so callers (and tests) can distinguish it from any other TypeError.
 */
export class UnknownColumnError extends Error {
  constructor(base: string, tier: string, available: readonly string[]) {
    super(
      `column '${base}' not in tier '${tier}'; has: ${available.join(", ")}`,
    );
    this.name = "UnknownColumnError";
  }
}

/**
 * Resolves a base metric name (e.g. "busy") to its index in the response's
 * columns for whichever tier actually answered the query.
 *
 * At raw resolution the column IS the base name. At 5m and 1h it is
 * suffixed -- busy_avg, busy_max -- because those are continuous-aggregate
 * columns, not the raw quantity. Trying the exact name first means a family
 * whose raw and rolled-up tiers happen to share a name (last()/max()
 * columns such as uptime_s) still resolves correctly.
 *
 * IMPORTANT: at 5m and 1h, nearly every rolled-up metric carries BOTH an
 * _avg and a _max column (0001_init.sql), and this function prefers _avg.
 * That means column(res, "cpu_total") -- and seriesValues() built on it --
 * silently resolves to the AVERAGE, never the peak, at every rolled-up
 * tier. This is deliberate and brief-mandated, not a bug: a caller who
 * actually wants the maximum must ask for it explicitly by passing the
 * fully-suffixed name as base, e.g. column(res, "cpu_total_max"), which
 * matches on the exact-name branch above.
 */
// The names a base column can answer to, in resolution order: the raw
// column first, then the rollup tiers' suffixed peers.
//
// _min belongs here because the schema's convention is wider than avg/max:
// 0001_init.sql rolls filesystem `free` up as min(free) AS free_min, and only
// as that. It is ONE list because it was briefly three -- column() learned
// _min while optionalValues and carriesColumn did not, so `free` resolved
// everywhere except where the chart actually read it, and every host's disk
// meter went blank while the code that decided whether to draw one said the
// column was there.
function candidates(base: string): string[] {
  return [base, `${base}_avg`, `${base}_max`, `${base}_min`];
}

export function column(res: MetricsResponse, base: string): number {
  for (const name of candidates(base)) {
    const idx = res.columns.indexOf(name);
    if (idx !== -1) return idx;
  }
  throw new UnknownColumnError(base, res.tier, res.columns);
}

/**
 * Thrown by seriesValues() when a resolved column holds a cell that is
 * neither a number nor null. Named so a caller who asked for numbers and
 * got, say, collector's boolean `ok` or string `error_code` sees exactly
 * which column and which type broke the claim, rather than a fabricated
 * NaN or a silently wrong chart.
 */
export class NonNumericColumnError extends Error {
  constructor(base: string, cell: unknown) {
    super(
      `column '${base}' is not numeric: found a ${typeof cell} (${JSON.stringify(cell)})`,
    );
    this.name = "NonNumericColumnError";
  }
}

/**
 * Thrown by seriesCells() / seriesTimestamps() when seriesIndex is out of
 * range for the response. Named so this is distinguishable from a bare
 * TypeError on `undefined.points`, matching UnknownColumnError and
 * NonNumericColumnError, its two neighbours in this file, which are both
 * named for the same reason: a caller catching by class or reading a stack
 * trace should not have to guess which invariant broke.
 */
export class SeriesIndexError extends Error {
  constructor(seriesIndex: number, length: number) {
    super(
      `series index ${seriesIndex} out of range: response has ${length} series`,
    );
    this.name = "SeriesIndexError";
  }
}

/**
 * Extracts one series' raw cells for a base column, resolved against the
 * response's tier, with no type claim about what a cell holds. This is the
 * accessor for columns that are legitimately not numbers -- family=collector
 * renders `ok` as a boolean and `error_code` as a string on purpose, and a
 * table cell for either wants the real value, not a number cast onto it.
 */
export function seriesCells(
  res: MetricsResponse,
  seriesIndex: number,
  base: string,
): unknown[] {
  const idx = column(res, base);
  const series = res.series[seriesIndex];
  if (series === undefined) {
    throw new SeriesIndexError(seriesIndex, res.series.length);
  }
  // +1: points[0] is the timestamp, values start at index 1.
  return series.points.map((point) => point[idx + 1]);
}

/**
 * Extracts one series' timestamps as epoch milliseconds. This is the one
 * timestamp shape in the whole read API that is NOT an ISO string:
 * internal/hub/read/metrics.go:198 emits `point[0]` as `ts.UnixMilli()`,
 * while every other timestamp field (Host.last_seen, Event.ts,
 * MetricsWindow.from/to) is RFC 3339. A caller building an x-axis wants
 * this function, not seriesCells(res, i, ...) reinterpreted as a date --
 * and must feed the result to format.ts's *Ms formatters (relativeMs,
 * absoluteMs), not relative()/absolute(), which parse ISO strings.
 */
export function seriesTimestamps(
  res: MetricsResponse,
  seriesIndex: number,
): number[] {
  const series = res.series[seriesIndex];
  if (series === undefined) {
    throw new SeriesIndexError(seriesIndex, res.series.length);
  }
  return series.points.map((point) => point[0] as number);
}

/**
 * Extracts one series' NUMERIC values for a base column, resolved against
 * the response's tier. Nulls pass through untouched -- a null means the
 * host reported nothing for that point, which must survive to the chart
 * layer so it can break the line rather than show a fabricated value.
 *
 * A cell that is neither a number nor null throws NonNumericColumnError
 * rather than being coerced or passed through as null. Returning null for
 * it would conflate two different facts -- "the host reported nothing" and
 * "this column is not a number" -- and refusing exactly that conflation is
 * why this module exists. A caller with a genuinely non-numeric column
 * (collector's `ok`/`error_code`) wants seriesCells(), not this function.
 */
export function seriesValues(
  res: MetricsResponse,
  seriesIndex: number,
  base: string,
): (number | null)[] {
  return seriesCells(res, seriesIndex, base).map((cell) => {
    if (cell === null) return null;
    if (typeof cell === "number") return cell;
    throw new NonNumericColumnError(base, cell);
  });
}

/**
 * Whether the answering tier carries this column at all.
 *
 * "The column is not in this tier" and "the host reported nothing" are
 * different facts that both arrive as an empty or null-filled series, and
 * telling them apart is the difference between a panel saying "not at this
 * resolution" and one asserting something about the host. swap_total is the
 * case that proves it: it exists only in the raw table, so at 5m a host with
 * 8 GB of swap in use rendered as "swap: none".
 *
 * The suffixes match column()'s resolution order, so this answers for the
 * same name column() would resolve.
 */
export function carriesColumn(
  res: MetricsResponse | null | undefined,
  base: string,
): boolean {
  // `== null`, covering undefined: these come from optional props, and a
  // caller that simply does not have this family yet passes nothing at all.
  // Reading .columns off it threw during render, with no error boundary
  // under it -- a blank page for a missing prop.
  if (res == null) return false;
  return candidates(base).some((name) => res.columns.includes(name));
}

/**
 * seriesValues for a column that may legitimately not be there.
 *
 * seriesValues throws when the answering tier has no such column, which is
 * the right default -- a page asking for a column it needs and getting
 * nothing back has a bug worth surfacing. But a page rendering N panels
 * across families does not know in advance which columns this tier answered
 * with, and one absent column would take the whole render down: there is no
 * error boundary in this app, so a thrown ColumnNotFoundError mid-render is
 * a blank page rather than one panel reading "no data".
 *
 * Returning [] rather than a series of nulls is deliberate. [] means "this
 * tier does not carry this column", which a panel renders as its
 * not-collected state; a series of nulls would mean "the host reported
 * nothing", which is a different fact and a different mark on the chart.
 *
 * The three names checked are the raw column and the rollup tiers'
 * suffixed peers, matching column()'s own resolution order.
 */
export function optionalValues(
  res: MetricsResponse | null | undefined,
  seriesIndex: number,
  base: string,
): (number | null)[] {
  if (res == null) return [];
  if (!carriesColumn(res, base)) return [];
  if (res.series[seriesIndex] === undefined) return [];
  return seriesValues(res, seriesIndex, base);
}

/**
 * Places a series on the window's own time grid, inserting a null for every
 * bucket the response has no row for.
 *
 * This is the difference between a chart that tells the truth about an
 * outage and one that lies about it. internal/hub/read/metrics.go emits only
 * the rows that exist: a bucket where the host reported nothing is a MISSING
 * POINT, not a null cell. The chart geometry breaks a line only on an
 * explicit null and spreads whatever array it is handed evenly across the
 * width -- so a host down from 03:00 to 06:00 inside a 24h window came back
 * as 216 points instead of 252 and drew one unbroken line straight across
 * the gap. "The agent was down" rendered as "CPU was steady", which is the
 * single failure the whole gap rule exists to prevent.
 *
 * Putting every series on the same grid also fixes a quieter one: two series
 * of different lengths in one panel (two filesystems, or rx with 200 points
 * and tx with 190) were each spread across their own length, so they were
 * misaligned in time against each other inside a single chart.
 *
 * The grid comes from the ANSWERED window and step, never the requested
 * ones: the hub clamps what it can serve, and drawing the requested span
 * would pad the difference with fabricated gaps.
 */
export function seriesOnGrid(
  res: MetricsResponse,
  values: readonly (number | null)[],
  timestamps: readonly number[],
): (number | null)[] {
  const stepMs = res.step_s * 1000;
  const from = Date.parse(res.window.from);
  const to = Date.parse(res.window.to);
  if (
    !Number.isFinite(stepMs) ||
    stepMs <= 0 ||
    !Number.isFinite(from) ||
    !Number.isFinite(to) ||
    to <= from
  ) {
    // Nothing to place them on. Returning the values untouched is the same
    // chart as before this function existed, which is better than an empty
    // one built from a window nobody could parse.
    return [...values];
  }

  const byBucket = new Map<number, number | null>();
  for (let i = 0; i < timestamps.length; i++) {
    // ROUND to the nearest bucket, not floor. Only the rollup tiers are
    // aligned -- read/metrics.go selects bare s.ts at the raw tier, so a
    // sample lands wherever the agent's clock and the collection latency put
    // it. Flooring sent a sample 50ms short of a boundary into the previous
    // bucket, where it overwrote the sample already there and left the
    // bucket it should have occupied empty: a one-minute hole drawn for a
    // host that never missed a scrape.
    const bucket = Math.round((timestamps[i]! - from) / stepMs);
    // First writer wins. Two samples genuinely inside one bucket means the
    // host reported faster than the step asked for; keeping the earlier one
    // matches the order the response arrived in, and neither is a gap.
    if (!byBucket.has(bucket)) byBucket.set(bucket, values[i] ?? null);
  }

  const count = Math.ceil((to - from) / stepMs);
  const out: (number | null)[] = new Array(count).fill(null);
  for (const [bucket, value] of byBucket) {
    if (bucket >= 0 && bucket < count) out[bucket] = value;
  }
  return out;
}

/**
 * seriesOnGrid for a base column: the common case, and the one that keeps a
 * caller from having to fetch values and timestamps separately just to line
 * them up again.
 */
export function griddedValues(
  res: MetricsResponse | null | undefined,
  seriesIndex: number,
  base: string,
): (number | null)[] {
  if (res == null) return [];
  const values = optionalValues(res, seriesIndex, base);
  if (values.length === 0) return [];
  return seriesOnGrid(res, values, seriesTimestamps(res, seriesIndex));
}

/** True when any value in the series is null -- the host reported nothing. */
export function hasGaps(vals: readonly (number | null)[]): boolean {
  return vals.some((v) => v === null);
}

/**
 * Turns a window/requested_window mismatch (and a truncated result) into a
 * sentence a human can act on. Returns null when the response is complete
 * and covers exactly what was asked.
 *
 * The server (internal/hub/read/tier.go's planQuery, metrics.go's Metrics)
 * already knows exactly which clamp fired and states it with real numbers
 * -- "from predates the 5m tier's 30 days retention", "the 5m tier
 * materialises 10 minutes behind now", "to was in the future and was
 * clamped to now". Those three clamps apply at DIFFERENT tiers and edges
 * (retention: leading edge, any tier; materialization lag: trailing edge,
 * 5m/1h only, never raw -- raw has lag 0 and no materialization step at
 * all; future-to: trailing edge, every tier including raw), so
 * res.warnings is surfaced verbatim rather than re-derived: re-deriving
 * risks folding the future-to clamp into materialization wording, which is
 * false at the raw tier.
 *
 * A derived sentence is used only as a fallback, for a window mismatch the
 * server did not explain with a warning (e.g. a caller-constructed response
 * in a test). That fallback is tier-aware: it never asserts materialization
 * for the raw tier, since raw has no such mechanism.
 *
 * truncated is folded in too, but NOT unconditionally: internal/hub/read/
 * metrics.go:146-150 already appends its own truncation warning ("the
 * result reached the N-point limit and is truncated; narrow the window or
 * ask for fewer columns") into res.warnings whenever it sets
 * res.truncated, and that append happens on every real code path that
 * produces a truncated response -- there is no way to observe
 * truncated: true from the actual hub without that warning also being
 * present. Appending our own sentence on top of the pass-through above
 * would print the same fact twice. The guard below only synthesizes a
 * truncation sentence when truncated is set but no warning mentions it --
 * a combination the real server cannot produce, kept purely as a defensive
 * fallback for a hand-built or malformed response (e.g. a test fixture, or
 * a future server bug) so truncation is never silently unreported. Do not
 * remove the guard and "helpfully" make this unconditional again: doing so
 * reintroduces the duplicate-sentence bug on every genuine truncated
 * response.
 */
export function windowNotice(res: MetricsResponse): string | null {
  const parts: string[] = [];

  if (res.warnings.length > 0) {
    parts.push(...res.warnings);
  } else {
    const { window, requested_window: requested } = res;
    const fromMoved = window.from !== requested.from;
    const toMoved = window.to !== requested.to;

    if (fromMoved) {
      parts.push(
        `data before ${window.from} is outside the ${res.tier} tier's retention and is not available`,
      );
    }
    if (toMoved) {
      if (res.tier === "raw") {
        parts.push(
          `the requested end time was after now and was clamped to ${window.to}`,
        );
      } else {
        parts.push(
          `data after ${window.to} has not materialized yet at the ${res.tier} tier`,
        );
      }
    }
  }

  const warningsMentionTruncation = res.warnings.some((w) =>
    /truncat/i.test(w),
  );
  if (res.truncated && !warningsMentionTruncation) {
    parts.push("the result was truncated at the point limit and is incomplete");
  }

  if (parts.length === 0) return null;
  return parts.join("; ") + ".";
}
