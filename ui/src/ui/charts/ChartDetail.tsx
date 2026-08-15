import { useEffect, useRef } from "react";
import { Overlay, type OverlaySeries } from "./Overlay";
import { Segmented } from "../Segmented";
import { ABSENT, absolute } from "../../lib/format";
import { RANGES, type Range } from "../../lib/range";

export interface ChartDetailProps {
  title: string;
  unit?: string;
  series: OverlaySeries[];
  max?: number;
  fmt?: (v: number | null) => string;
  /** The answered window, for the time axis. Absent, the axis is omitted
   * rather than guessed -- a chart with invented times is worse than one
   * with none. */
  window?: { from: string; to: string } | null;
  /** THIS DIALOG's range and its setter, owned by the panel that opened it.
   * Given both, the dialog carries a picker, so a reader can widen the
   * window without closing what they opened to look at -- and without
   * re-ranging the page behind them, which is what this control used to do.
   * Closing the dialog returns it to the page's range. */
  range?: Range;
  onRangeChange?: (range: Range) => void;
  /** A fetch for another range is in flight. The chart keeps showing the
   * range it already had while this is true. */
  loading?: boolean;
  /** Why the range just asked for could not be loaded. The chart keeps the
   * range it had rather than blanking, so this is a line of text beside a
   * real chart, not an error state replacing one. */
  error?: string | null;
  /** The ranges the PAGE behind this dialog offers. It used to show all
   * five regardless, so picking 30d over a fleet chart handed the page a
   * range its own picker could not express -- every button underneath came
   * back unpressed, which reads as "no range selected". Absent, all five
   * are offered, which is right only for a page that offers all five. */
  ranges?: readonly Range[];
  onClose: () => void;
  /** Draw the series as a cumulative stack, matching the small panel that
   * opened this. The mark must not change when a chart is enlarged. */
  stacked?: boolean;
  /** Whether the chart names its series above it. Off for the per-core
   * stack: 32 entries squeezed the 900px plot into a corner, and the stats
   * table below already names every series beside its colour. */
  legend?: boolean;
  /** A value to mark with a dashed rule, e.g. a host's total memory. */
  reference?: number;
  /** Draw the series as mirrored in/out pairs about a midline. */
  mirrored?: boolean;
  /** What the reference rule is, drawn at the line. The small panel has no
   * axis, so without this the rule is unnamed there. */
  referenceLabel?: string;
  /** Hide the y axis. A stack whose height is a shape rather than a
   * quantity -- unnormalised per-core CPU runs to N x 100 -- must not carry
   * an axis putting a number on it. */
  hideAxis?: boolean;
}

/**
 * The enlarged view of a chart.
 *
 * A 260px sparkline answers "is anything happening"; it cannot answer "how
 * much, and when". This is the same series drawn large enough to read, with
 * the axis and the per-series numbers the small one has no room for.
 *
 * It is a modal dialog rather than an expanding panel because the point is
 * to look at ONE thing closely -- the surrounding grid of twenty panels is
 * exactly the noise being escaped.
 */
export function ChartDetail({
  title,
  unit,
  series,
  max,
  fmt,
  window: answered = null,
  range,
  onRangeChange,
  ranges = RANGES,
  loading = false,
  error = null,
  onClose,
  stacked,
  legend,
  reference,
  referenceLabel,
  mirrored,
  hideAxis,
}: ChartDetailProps) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    // Escape closes, and focus moves into the dialog so the next Tab lands
    // inside it rather than back in the page behind.
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    ref.current?.focus();
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  const ceiling = max ?? peak(series, stacked);
  const format = (v: number | null) => (fmt ? fmt(v) : formatNumber(v));

  return (
    // The backdrop closes on click; the dialog stops the click so a stray
    // press inside it does not dismiss what you are reading.
    <div className="cd-back" onClick={onClose}>
      <div
        ref={ref}
        className="cd"
        role="dialog"
        aria-modal="true"
        aria-label={`${title}, enlarged`}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        <header>
          <h3>{title}</h3>
          {unit && <span className="u">{unit}</span>}
          <div className="spacer" />
          {range !== undefined && onRangeChange !== undefined && (
            <Segmented
              options={ranges.map((r) => ({ value: r, label: r }))}
              value={range}
              onChange={onRangeChange}
            />
          )}
          {/* Beside the picker rather than over the chart: the chart is
              still showing real data for the range it had, and covering it
              would hide the thing the dialog was opened to read. */}
          {loading && (
            <span className="note" role="status">
              Loading…
            </span>
          )}
          {error !== null && !loading && (
            <span className="note" role="status">
              Could not load that range.
            </span>
          )}
          <button className="btn ghost" onClick={onClose} aria-label="Close">
            Close
          </button>
        </header>

        <div className="cd-chart">
          {/* The axis is drawn from the same ceiling the shape is scaled to,
              so the labels cannot disagree with it -- EXCEPT when a
              reference is given. Then the ceiling carries headroom above the
              reference so the rule is visible, and labelling that padded
              number puts a quantity on screen that does not exist: a 137.4 GB
              host read "148.4 GB". The labels then step from the reference,
              positioned where they actually fall. */}
          {!hideAxis && (
            <div className="cd-y">
              {axisLabels(ceiling, reference, mirrored).map((label, i) => (
                <span key={i} style={{ top: `${(1 - label.fraction) * 100}%` }}>
                  {format(label.value)}
                </span>
              ))}
            </div>
          )}
          <Overlay
            series={series}
            min={0}
            max={ceiling}
            width={900}
            height={320}
            stacked={stacked}
            legend={legend}
            reference={reference}
            referenceLabel={referenceLabel}
            mirrored={mirrored}
            label={`${title}, enlarged`}
          />
        </div>

        {answered && (
          <div className="cd-x">
            <span>{absolute(answered.from)}</span>
            <span>{absolute(answered.to)}</span>
          </div>
        )}

        {/* What the small panel has no room for: every series named, with
            the numbers a reader would otherwise have to eyeball. */}
        <table className="cd-stats">
          <thead>
            <tr>
              <th scope="col">Series</th>
              <th scope="col">Latest</th>
              <th scope="col">Min</th>
              <th scope="col">Max</th>
              <th scope="col">Mean</th>
            </tr>
          </thead>
          <tbody>
            {series.map((s) => {
              const stats = summarise(s.values);
              return (
                <tr key={s.name}>
                  <th scope="row">
                    <i style={{ background: s.color }} />
                    {s.name}
                  </th>
                  <td>{format(stats.latest)}</td>
                  <td>{format(stats.min)}</td>
                  <td>{format(stats.max)}</td>
                  <td>{format(stats.mean)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

/**
 * The three y-axis labels, as a position in the plot box and the value that
 * belongs at it.
 *
 * The mirrored case gets its own axis rather than `hideAxis`, and the reason
 * is what the chart is for: both mirrored callers -- the Graphs tab's
 * "Interface throughput" and the container page's "Network" -- are rate
 * charts carrying unit="B/s", so "how much" is exactly the question a reader
 * enlarged them to answer. Dropping the axis answers less than labelling it
 * correctly does.
 *
 * What it must say is set by mirrorPaths() (geometry.ts): the baseline is
 * h/2, ingress is drawn UPWARD from it and egress DOWNWARD, both scaled
 * against the same max. So the midline is zero and BOTH edges are a peak of
 * `ceiling`. The shared [ceiling, ceiling/2, 0] axis claimed the opposite --
 * zero at the bottom, where the largest egress actually sits -- which read a
 * saturated downlink as an idle one.
 *
 * `reference` is not combined with `mirrored`: no caller passes both, and a
 * ceiling padded for a reference rule means something different on each of
 * the two half-axes. The mirrored case is taken first rather than guessed at.
 */
function axisLabels(
  ceiling: number,
  reference: number | undefined,
  mirrored: boolean | undefined,
): { fraction: number; value: number }[] {
  if (mirrored) {
    // Top and bottom are both +ceiling -- a magnitude away from the midline
    // in either direction -- and the midline is zero.
    return [
      { fraction: 1, value: ceiling },
      { fraction: 0.5, value: 0 },
      { fraction: 0, value: ceiling },
    ];
  }
  const fractions =
    reference === undefined
      ? [1, 0.5, 0]
      : [reference / ceiling, reference / ceiling / 2, 0];
  return fractions.map((fraction) => ({
    fraction,
    value: fraction * ceiling,
  }));
}

function formatNumber(v: number | null): string {
  if (v === null) return ABSENT;
  return Number.isInteger(v) ? String(v) : v.toFixed(1);
}

// `stacked` is not a detail this can ignore: a stack's height at an index is
// the SUM over the series there, so the largest single value understates it
// and the top of the stack would be drawn outside the box. Callers here all
// pass an explicit max today, but a derived ceiling that answers the wrong
// question is a trap rather than a fallback.
function peak(series: readonly OverlaySeries[], stacked = false): number {
  let max = 0;
  if (stacked) {
    const n = series.reduce(
      (longest, s) => Math.max(longest, s.values.length),
      0,
    );
    for (let i = 0; i < n; i++) {
      // Skipping any index where a series is null, exactly as stackBands
      // does: a running total is undefined there rather than smaller.
      if (series.some((s) => s.values[i] == null)) continue;
      let sum = 0;
      for (const s of series) sum += s.values[i] as number;
      if (sum > max) max = sum;
    }
  } else {
    for (const s of series) {
      for (const v of s.values) if (v !== null && v > max) max = v;
    }
  }
  // A zero ceiling would divide by zero in the geometry.
  return max || 1;
}

/**
 * Latest, min, max and mean over the non-null values.
 *
 * Nulls are skipped rather than counted as zero: a host that reported
 * nothing for an hour did not report an hour of zeroes, and averaging them
 * in would drag every mean toward a number nobody measured. A series with no
 * values at all reports null for each, which renders as the absent marker.
 */
export function summarise(values: readonly (number | null)[]): {
  latest: number | null;
  min: number | null;
  max: number | null;
  mean: number | null;
} {
  let min: number | null = null;
  let max: number | null = null;
  let sum = 0;
  let count = 0;
  for (const v of values) {
    if (v === null) continue;
    if (min === null || v < min) min = v;
    if (max === null || v > max) max = v;
    sum += v;
    count++;
  }
  // The LATEST bucket, trailing nulls included: a series that has gone quiet
  // reads as absent rather than as its last known value.
  const latest = values.length > 0 ? (values[values.length - 1] ?? null) : null;
  return { latest, min, max, mean: count === 0 ? null : sum / count };
}
