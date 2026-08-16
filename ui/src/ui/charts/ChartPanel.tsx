// The card that wraps a chart with a title, unit, latest value, legend and
// (when applicable) either a "not collected" state or a clamped-window
// notice. `unavailable` is a product requirement, not a nicety: three
// metric families (IP statistics, ICMP statistics, ICMP informational) have
// no columns in the schema at all, and netra's collector contract is that
// something which cannot run says why -- this panel is where that
// contract reaches the UI.
import { ABSENT } from "../../lib/format";
import { extent } from "./geometry";
import type { OverlaySeries } from "./Overlay";
import { Overlay } from "./Overlay";
import { Enlargeable, type DetailData } from "./Enlargeable";
import type { Range } from "../../lib/range";

/** A single plotted series. Re-exported as `Band` because that is the name
 * the task brief uses for ChartPanel's `series` prop. */
export type Band = OverlaySeries;

// The click-to-enlarge behaviour, the dialog and the dialog's own range now
// live in Enlargeable, so that a sparkline drawn without this card around it
// -- in a table cell, in the sensor list -- can carry the same affordance.
// Re-exported from here because this is where they were, and every existing
// import says so.
export type { DetailData } from "./Enlargeable";
export { useDetailRange } from "./Enlargeable";

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
  /** A value to mark with a dashed rule, e.g. a host's total memory. */
  reference?: number;
  /** Draw the series as mirrored in/out pairs about a midline. */
  mirrored?: boolean;
  /** Formats the HEADLINE only, defaulting to `fmt`. The two are separate
   * because they answer to different neighbours: `fmt` also renders the
   * enlarged view's axis, its tooltips and its stats table, where every
   * number stands alone, while the headline sits beside the panel's title
   * and can afford to carry the ceiling with it -- "used 20.4 · 31 GiB".
   * An axis whose every tick repeated the ceiling would be unreadable. */
  nowFmt?: (n: number | null) => string;
  /** A headline value that is NOT series[0]'s latest.
   *
   * Giving it also suppresses the series name, and that is the point rather
   * than a side effect: the label exists to say WHOSE number the headline is,
   * so a number belonging to no single series must not wear one. The
   * per-core CPU stack is the case -- series[0] is core 0, and headlining one
   * arbitrary core beside a shape that is the whole machine states a fact
   * about the host that is not true of it. */
  nowValue?: number | null;
  /** Hide the enlarged view's y axis. For a stack whose height is a shape
   * rather than a quantity -- unnormalised per-core CPU runs to N x 100 --
   * an axis would put a number on something that does not mean one. */
  hideAxis?: boolean;
  /** The answered window, passed through to the enlarged view's time axis. */
  window?: { from: string; to: string } | null;
  /** The range the PAGE is showing. It seeds the enlarged view's own picker
   * and is what that picker returns to when the dialog is closed and
   * reopened. It is never written back: enlarging a chart to look at a
   * longer window is a question about that chart, not a decision about the
   * page. */
  range?: Range;
  /** The ranges to offer in the enlarged view. Defaults to every range;
   * pages that resolve a narrower set pass it so the dialog cannot ask for a
   * window the page's fetcher will not serve. */
  ranges?: readonly Range[];
  /**
   * Loads this panel's series at another range, for the enlarged view alone.
   *
   * The picker in the dialog used to be wired to the PAGE's setter, so
   * widening one chart refetched all twenty panels beside it and moved the
   * toolbar behind the dialog -- the opposite of what enlarging one chart
   * asks for. The dialog owns its range now, and this is how it gets data
   * for it.
   *
   * Without this the dialog carries no picker at all, which is right for a
   * panel whose page cannot refetch one family on its own.
   */
  fetchSeries?: (range: Range) => Promise<DetailData>;
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
  reference,
  nowFmt,
  nowValue,
  mirrored,
  hideAxis,
  window: answered = null,
  range,
  ranges,
  fetchSeries,
}: ChartPanelProps) {
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
  // `nowValue` is already reduced by the caller and obeys the same rule: it
  // is the latest BUCKET's value, nulls included, never the last one that
  // happened to arrive.
  const latest =
    nowValue !== undefined ? nowValue : (series[0]?.values.at(-1) ?? null);
  const format = nowFmt ?? fmt;
  const nowText = format ? format(latest) : (latest?.toString() ?? ABSENT);
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
  // ...and only when the headline IS a series'. See `nowValue`.
  const nowLabel =
    nowValue === undefined && series.length > 1 ? series[0]?.name : undefined;

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
      {/* The chart is the affordance -- see Enlargeable, which owns the
          button and the dialog so that a sparkline drawn without this card
          around it can carry the same one. */}
      <Enlargeable
        title={title}
        unit={unit}
        // The panel's own series, and its own `max` rather than
        // `effectiveMax`: the dialog derives its ceiling from the data when
        // no explicit one is given, which is what it has always done.
        series={series}
        max={max}
        fmt={fmt}
        stacked={stacked}
        legend={legend}
        // The rule is unlabelled in both views: the enlarged view's y axis
        // already names it at the height it sits, and the small panel names
        // it in its header rather than inside the plot.
        reference={reference}
        mirrored={mirrored}
        hideAxis={hideAxis}
        window={answered}
        range={range}
        ranges={ranges}
        fetchSeries={fetchSeries}
      >
        <Overlay
          series={series}
          max={effectiveMax}
          // A stack is scaled from ZERO -- stackBands() has no min at all,
          // it divides a running total by `max` -- so the reference rule has
          // to be placed against the same floor. Without this, Overlay fell
          // back to the data's own derived minimum: the dashed mem_total rule
          // landed at the wrong height and the gap above the stack, which is
          // the whole reading ("how much memory is free"), misstated it. The
          // enlarged view has always passed min={0} and was already right,
          // so the small panel and the chart it opened disagreed.
          //
          // Scoped to the stacked case rather than passed unconditionally:
          // effectiveMin also feeds linePath() for the non-stacked panels,
          // where auto-scaling off the data's floor is deliberate -- a
          // temperature series between 44 and 47 degrees must not be drawn
          // as a flat line pinned to the bottom of a 0-47 box.
          min={stacked ? 0 : undefined}
          width={width}
          height={height}
          highlight={highlight}
          stacked={stacked}
          legend={legend}
          reference={reference}
          mirrored={mirrored}
          label={`${title} over time`}
        />
      </Enlargeable>
      {notice && <p className="note">{notice}</p>}
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
