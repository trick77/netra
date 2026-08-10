// TODO(task-4-types): replace this structural type with the shared one from
// ui/src/lib/api.ts once that module lands. Field shapes are taken verbatim
// from internal/hub/read/metrics.go's Result and tier.go's Window, which is
// what the /metrics endpoint actually serializes.
export interface MetricsWindow {
  from: string;
  to: string;
}

export interface MetricsSeries {
  key: Record<string, string>;
  // Each point is [unixMillis, ...values], one value per entry of
  // MetricsResponse.columns, in that order. A value can be null -- the
  // host reported nothing for that column at that timestamp -- and must
  // never be coerced to 0 or dropped.
  points: (number | null)[][];
}

export interface MetricsResponse {
  family: string;
  tier: string;
  step_s: number;
  // window is what the response actually covers; requested_window is the
  // echo of what was asked. They differ at the leading edge (retention) and
  // the trailing edge (materialization lag of the 5m/1h continuous
  // aggregates).
  window: MetricsWindow;
  requested_window: MetricsWindow;
  warnings: string[];
  key_columns: string[];
  // Column names differ per tier BY CONSTRUCTION: a base name like "busy"
  // becomes "busy_avg" at the 5m tier and "busy_max" at the 1h tier. This is
  // deliberate -- a client that ignores tier gets a key it does not
  // recognise rather than a number that looks fine and is wrong.
  columns: string[];
  series: MetricsSeries[];
  truncated: boolean;
}

/**
 * Thrown by column() when a base name has no match in the response's tier.
 * Named so callers (and tests) can distinguish it from any other TypeError.
 */
export class UnknownColumnError extends Error {
  constructor(base: string, tier: string, available: readonly string[]) {
    super(`column '${base}' not in tier '${tier}'; has: ${available.join(", ")}`);
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
export function column(res: MetricsResponse, base: string): number {
  const candidates = [base, `${base}_avg`, `${base}_max`];
  for (const name of candidates) {
    const idx = res.columns.indexOf(name);
    if (idx !== -1) return idx;
  }
  throw new UnknownColumnError(base, res.tier, res.columns);
}

/**
 * Extracts one series' values for a base column, resolved against the
 * response's tier. Nulls pass through untouched -- a null means the host
 * reported nothing for that point, which must survive to the chart layer so
 * it can break the line rather than show a fabricated value.
 */
export function seriesValues(
  res: MetricsResponse,
  seriesIndex: number,
  base: string,
): (number | null)[] {
  const idx = column(res, base);
  const series = res.series[seriesIndex];
  // +1: points[0] is the timestamp, values start at index 1.
  return series.points.map((point) => point[idx + 1] as number | null);
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

  const warningsMentionTruncation = res.warnings.some((w) => /truncat/i.test(w));
  if (res.truncated && !warningsMentionTruncation) {
    parts.push("the result was truncated at the point limit and is incomplete");
  }

  if (parts.length === 0) return null;
  return parts.join("; ") + ".";
}
