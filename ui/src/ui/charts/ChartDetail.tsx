import { useEffect, useRef } from "react";
import type { OverlaySeries } from "./Overlay";
import { ChartFigure } from "./ChartFigure";
import { Segmented } from "../Segmented";
import { InfoTip } from "../InfoTip";
import { ABSENT } from "../../lib/format";
import { RANGES, type Range } from "../../lib/range";
import type { ScaleFactory } from "./scale";

// Where it lives now. Re-exported because this is where it was.
export { summarise } from "./ChartFigure";

// The size the enlarged chart is DRAWN at. It scales down to whatever the
// panel gives it -- svg.spark carries max-width:100% and a viewBox -- so
// this is the shape of the image, not a minimum width the dialog demands.
const CHART_WIDTH = 1000;
const CHART_HEIGHT = 380;

export interface ChartDetailProps {
  title: string;
  /** The panel's explanation, shown behind an (i) beside the dialog title --
   * the same text and the same affordance as the 260px panel this was opened
   * from. See ChartPanel's prop of the same name. */
  about?: string;
  unit?: string;
  series: OverlaySeries[];
  max?: number;
  /** The floor the plot scales from. Zero by default, which is right for
   * every quantity measured from nothing and wrong for a series drawn
   * against its own extent: the sensor list free-scales deliberately, and a
   * package sitting between 44 and 47 degrees pinned to a 0-47 box is a flat
   * line at the bottom -- the enlarged view would say LESS than the 110px
   * sparkline it was opened from. Callers whose small chart free-scales pass
   * that chart's floor. */
  min?: number;
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
  /** A value to mark with a dashed rule, e.g. a host's total memory. */
  reference?: number;
  /** Draw the series as mirrored in/out pairs about a midline. */
  mirrored?: boolean;
  /** A non-proportional value axis, forwarded so the enlarged chart draws the
   * same curve as the small one it was opened from. See scale.ts. */
  scaleFor?: ScaleFactory;
  /** 1024 for byte quantities, so the axis steps 512 MB rather than 500 MB.
   * The small panel has always passed this; the enlarged view ignored it,
   * because its HTML gutter stepped the axis itself and knew nothing about
   * a base. Same chart, two different sets of numbers. */
  tickBase?: 1000 | 1024;
  /** Hide the VALUE axis, and only that. A stack whose height is a shape
   * rather than a quantity -- unnormalised per-core CPU runs to N x 100 --
   * must not carry an axis putting a number on it. Gridlines, the spine and
   * the time axis stay: "when" is a real question about every chart. */
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
  about,
  unit,
  series,
  max,
  min = 0,
  fmt,
  window: answered = null,
  range,
  onRangeChange,
  ranges = RANGES,
  loading = false,
  error = null,
  onClose,
  stacked,
  reference,
  mirrored,
  scaleFor,
  tickBase,
  hideAxis,
}: ChartDetailProps) {
  const ref = useRef<HTMLDivElement>(null);

  // The latest onClose, without making it a dependency of the effects below.
  // Callers build it inline (`onClose={() => setEnlarged(false)}`), so it is a
  // new function on every render of the page behind this -- and the page
  // behind this polls. Depending on it re-ran the focus effect on every tick:
  // cleanup put focus back on the opener and setup pulled it into the panel
  // again, so a reader who had just clicked "6h" in the picker, or who was
  // typing in the picker, lost
  // the caret to the panel root. Same reason and same shape as useDetailRange's
  // fetchRef and usePoll's fnRef.
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    // Escape closes, and focus moves into the dialog so the next Tab lands
    // inside it rather than back in the page behind.
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCloseRef.current();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    // Focus goes into the panel on open, and back to whatever opened this
    // when it closes. Without the second half, focus lands on document.body
    // and the next Tab starts from the top of the page: a reader who opened
    // the chart on row 14 of a twenty-host table is returned to the masthead.
    // It matters more now than it did -- every chart is one of these buttons,
    // so a fleet page carries sixty of them rather than a page's worth of
    // panels.
    //
    // Runs ONCE, on mount: this is what opening and closing does, not
    // something a re-render of the page behind gets to redo.
    const opener = document.activeElement;
    ref.current?.focus();
    return () => {
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
    };
  }, []);

  const ceiling = max ?? peak(series, stacked);
  // The figure only ever formats a real number -- a tick, or a value under
  // the cursor. The null case belongs to the caller's fmt, which the stats
  // table used to reach through for its absent cells and no longer does.
  const format = (v: number) => (fmt ? fmt(v) : formatNumber(v));

  const panel = (
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
        {about !== undefined && <InfoTip text={about} label={title} />}
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

      <ChartFigure
        series={series}
        min={min}
        max={ceiling}
        width={CHART_WIDTH}
        height={CHART_HEIGHT}
        stacked={stacked}
        reference={reference}
        mirrored={mirrored}
        scaleFor={scaleFor}
        hideAxis={hideAxis}
        format={format}
        tickBase={tickBase}
        window={answered}
        label={`${title}, enlarged`}
      />
    </div>
  );

  // The backdrop closes on a click, and the panel stops the click inside
  // itself, so a stray press on what you are reading does not dismiss it.
  return (
    <div className="cd-back" onClick={onClose}>
      {panel}
    </div>
  );
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
export function peak(
  series: readonly OverlaySeries[],
  stacked = false,
): number {
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
      // The BAND too, not just the line. With the mean-plus-peak pair the
      // line is the bucket's mean and the band is its peak, so the band is
      // always the taller of the two: a ceiling taken from the line alone
      // draws the envelope outside the plot -- linePath() never clamps --
      // and the burst someone enlarged the chart to see is the one thing
      // that disappears off the top. This is what the chart page's peakOf()
      // did before this component absorbed it.
      for (const v of s.band ?? []) if (v !== null && v > max) max = v;
    }
  }
  // A zero ceiling would divide by zero in the geometry.
  return max || 1;
}
