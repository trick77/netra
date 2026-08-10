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
 * Turns a window/requested_window mismatch into a sentence a human can act
 * on. Returns null when the response covers exactly what was asked.
 *
 * The two edges mean different things and must not be conflated:
 *  - leading edge (from moved later): retention -- the data simply does not
 *    exist that far back at this tier.
 *  - trailing edge (to moved earlier): every continuous aggregate is
 *    materialized_only, so the 5m/1h tiers are fresh only up to now minus
 *    their refresh lag.
 * Reporting this in words is what keeps retention from being misread as
 * data loss.
 */
export function windowNotice(res: MetricsResponse): string | null {
  const { window, requested_window: requested } = res;
  const fromMoved = window.from !== requested.from;
  const toMoved = window.to !== requested.to;

  if (!fromMoved && !toMoved) return null;

  const parts: string[] = [];
  if (fromMoved) {
    parts.push(
      `data before ${window.from} is outside the ${res.tier} tier's retention and is not available`,
    );
  }
  if (toMoved) {
    parts.push(
      `data after ${window.to} has not materialized yet at the ${res.tier} tier`,
    );
  }
  return parts.join("; ") + ".";
}
