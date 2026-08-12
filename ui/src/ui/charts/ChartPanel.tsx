// The card that wraps a chart with a title, unit, latest value, legend and
// (when applicable) either a "not collected" state or a clamped-window
// notice. `unavailable` is a product requirement, not a nicety: three
// metric families (IP statistics, ICMP statistics, ICMP informational) have
// no columns in the schema at all, and netra's collector contract is that
// something which cannot run says why -- this panel is where that
// contract reaches the UI.
import { useState } from "react";
import { ABSENT } from "../../lib/format";
import { extent } from "./geometry";
import type { OverlaySeries } from "./Overlay";
import { Overlay } from "./Overlay";
import { ChartDetail } from "./ChartDetail";
import type { Range } from "../../lib/range";

/** A single plotted series. Re-exported as `Band` because that is the name
 * the task brief uses for ChartPanel's `series` prop. */
export type Band = OverlaySeries;

export interface ChartPanelProps {
  title: string;
  /** Printed after the value. Give one only when `fmt` does NOT already
   * carry it: percent() and bytes() name their own units, and a panel that
   * passes both renders "12 % %". A rate is the case that needs it -- bytes()
   * formats the magnitude, and only "B/s" says it is per second. */
  unit?: string;
  series?: Band[];
  max?: number;
  fmt?: (n: number | null) => string;
  /** The sentence from windowNotice() (lib/metrics.ts) explaining a served
   * window clamped by retention or materialisation lag. Rendered verbatim
   * so a clamped range never reads as missing data. */
  notice?: string | null;
  /** The reason this metric family has no data at all, e.g. "no ICMP
   * columns in the schema". Presence of this prop switches the whole panel
   * to the dashed not-collected state instead of drawing an empty chart. */
  unavailable?: string;
  width?: number;
  height?: number;
  highlight?: string;
  /** Draw the series as a cumulative stack instead of independent lines.
   * Passed through to Overlay AND to the enlarged view, so clicking a
   * stacked panel open does not silently change the mark. */
  stacked?: boolean;
  /** Whether the chart names its series underneath. Off for the per-core
   * stack, where the list is longer than the chart. */
  legend?: boolean;
  /** Hide the enlarged view's y axis. For a stack whose height is a shape
   * rather than a quantity -- unnormalised per-core CPU runs to N x 100 --
   * an axis would put a number on something that does not mean one. */
  hideAxis?: boolean;
  /** The answered window, passed through to the enlarged view's time axis. */
  window?: { from: string; to: string } | null;
  /** The page's range and setter, so the enlarged view can carry the same
   * control. Without them it simply shows the window it was given. */
  range?: Range;
  onRangeChange?: (range: Range) => void;
}

export function ChartPanel({
  title,
  unit,
  series = [],
  max,
  fmt,
  notice,
  unavailable,
  width = 260,
  height = 64,
  highlight,
  stacked,
  legend,
  hideAxis,
  window: answered = null,
  range,
  onRangeChange,
}: ChartPanelProps) {
  const [enlarged, setEnlarged] = useState(false);
  if (unavailable !== undefined) {
    return (
      <section className="smp na" aria-label={`${title}, not collected`}>
        <div className="t">
          <h4>{title}</h4>
        </div>
        <div className="box">
          <span>Not collected</span>
          <span>{unavailable}</span>
        </div>
      </section>
    );
  }

  // A stack is as tall as the running TOTAL at an index, so the largest
  // single value understates it and the top of the stack would be drawn
  // outside the box. Only matters when no explicit ceiling is given, which
  // is the unnormalised per-core chart: N cores stack to N x 100.
  const autoMax = stacked
    ? runningTotalMax(series)
    : extent(series.flatMap((s) => s.values)).max;
  const effectiveMax = max ?? autoMax;

  // The value at the LATEST bucket, trailing nulls included. Filtering the
  // nulls out first and taking the last survivor reported the last value
  // that ever arrived: a host that stopped reporting two buckets ago drew
  // its hole correctly and then printed "43" in bold beside it as the
  // current reading. "The agent is down" must never render as "CPU is at
  // 43" -- absent is absent, never the last number we happen to have.
  const latest = series[0]?.values.at(-1) ?? null;
  const nowText = fmt ? fmt(latest) : (latest?.toString() ?? ABSENT);
  // `unit` is printed whenever it is given, formatter or not. Suppressing it
  // for every formatted panel was an over-correction: it fixed percent()
  // with unit="%" printing "12 % %" and broke five panels whose formatter
  // carries a magnitude but not a rate -- "Interface throughput" showed
  // 1.2 MB for a 1.2 MB/s link, a rate reading as a total. A formatter that
  // already carries its own unit must simply not be given one; that is the
  // caller's contract, and it is stated on the prop.
  // With more than one series the headline is series[0]'s alone, so it says
  // whose it is. A Network panel printing rx's number under a bare unit
  // reads as the panel's total.
  const nowLabel = series.length > 1 ? series[0]?.name : undefined;

  return (
    <section className="smp" aria-label={`${title} chart`}>
      <div className="t">
        <h4>{title}</h4>
        <span className="now">
          {nowLabel ? `${nowLabel} ` : ""}
          {nowText}
        </span>
        {unit !== undefined && <span className="u">{unit}</span>}
      </div>
      {/* A button, not a div with a click handler: opening the enlarged view
          has to work from the keyboard, and the chart is the affordance --
          a separate "expand" icon would be a second thing to find. */}
      <button
        type="button"
        className="chartwrap as-button"
        onClick={() => setEnlarged(true)}
        aria-label={`Enlarge ${title}`}
      >
        <Overlay
          series={series}
          max={effectiveMax}
          width={width}
          height={height}
          highlight={highlight}
          stacked={stacked}
          legend={legend}
          label={`${title} over time`}
        />
      </button>
      {notice && <p className="note">{notice}</p>}
      {enlarged && (
        <ChartDetail
          title={title}
          unit={unit}
          series={series}
          max={max}
          fmt={fmt}
          stacked={stacked}
          legend={legend}
          hideAxis={hideAxis}
          window={answered}
          range={range}
          onRangeChange={onRangeChange}
          onClose={() => setEnlarged(false)}
        />
      )}
    </section>
  );
}

/**
 * The largest running total across a stack's series -- what stackBands()
 * scales against. Indices where any series is null are skipped, matching
 * stackBands' own gap rule: a running total is undefined there.
 */
function runningTotalMax(series: readonly Band[]): number {
  const n = series.reduce(
    (longest, s) => Math.max(longest, s.values.length),
    0,
  );
  let best = 0;
  for (let i = 0; i < n; i++) {
    if (series.some((s) => s.values[i] == null)) continue;
    let sum = 0;
    for (const s of series) sum += s.values[i] as number;
    if (sum > best) best = sum;
  }
  return best;
}
