// The card that wraps a chart with a title, unit, latest value, legend and
// (when applicable) either a "not collected" state or a clamped-window
// notice. `unavailable` is a product requirement, not a nicety: netra's
// collector contract is that something which cannot run says why, and this
// panel is where that contract reaches the UI. It was built for the IP and
// ICMP families, which had no columns in the schema at all; those are
// collected now, and per-container networking is the remaining caller.
import type { ReactNode } from "react";
import { ABSENT } from "../../lib/format";
import { extent } from "./geometry";
import { mirroredTicks, niceTicks, timeTicks } from "./ticks";
import { widestLabel } from "./plot";
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
  /**
   * The ladder the value ticks step on: 1000 for a decimal quantity, 1024
   * for one formatted with binaryBytes.
   *
   * It has to be told rather than inferred, because a formatter is an opaque
   * function -- and getting it wrong is not subtle. A 16 GiB host ticked
   * decimally labels 1.9 / 3.7 / 5.6 / 7.5 GiB, every number ragged; in base
   * 1024 the same axis reads 2 / 4 / 6 / 8 / 12 / 16 GiB. Memory is the only
   * family that needs it.
   */
  tickBase?: 1000 | 1024;
  /** Where this chart lives as a page. Given it, the panel is a link and the
   * enlarge dialog is not built. See Enlargeable. */
  href?: string;
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
  /**
   * A reading that QUALIFIES this chart, drawn under it inside the panel.
   *
   * For the container memory meter, which has to sit with the memory chart it
   * is the ceiling for. Rendered outside the panel it landed after the whole
   * small-multiples grid closed -- so it read as a footnote to the last panel
   * in the grid (Disk I/O) rather than to Memory, at every viewport width.
   *
   * Deliberately NOT rendered by the `unavailable` branch: a panel that
   * collected nothing has no reading to qualify, and a meter under "Not
   * collected" would be the empty chart that branch exists to prevent.
   */
  footer?: ReactNode;
}

/**
 * The shortest panel that still reads as a chart with an axis.
 *
 * Below this the two value labels and the row of time labels crowd the plot
 * they describe. The panels this component actually draws are 112 tall; the
 * threshold exists so a caller passing an explicit small height gets the
 * bare shape it asked for rather than a collapsed plot.
 */
const AXIS_MIN_HEIGHT = 80;

export function ChartPanel({
  title,
  unit,
  series = [],
  max,
  fmt,
  notice,
  unavailable,
  width = 260,
  // Tall enough to carry an axis. A panel used to be a 64px shape whose only
  // number was the headline, so reading a magnitude off it meant enlarging
  // it; the extra 48px buy two value labels and three time labels, and the
  // panel answers "how much, and when" on its own.
  height = 112,
  highlight,
  stacked,
  legend,
  reference,
  nowFmt,
  nowValue,
  mirrored,
  hideAxis,
  tickBase = 1000,
  window: answered = null,
  range,
  ranges,
  fetchSeries,
  href,
  footer,
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

  // The axis, in two independent halves.
  //
  // A box shorter than AXIS_MIN_HEIGHT declines both. The axis costs a line
  // of text below the plot and half a line above it, so below that height
  // the furniture takes more of the box than the data does -- and a caller
  // drawing this card at sparkline height wants a sparkline.
  const axis = series.length > 0 && height >= AXIS_MIN_HEIGHT;

  // hideAxis suppresses the VALUE half only, which is what it has always
  // meant: "an axis would put a number on something that does not mean one".
  // Two panels need it and neither is measuring a quantity -- the per-core
  // stack, whose height is cores x 100, and a boolean series, whose 0 and 1
  // are states rather than magnitudes. Both still happened in TIME, so the
  // time axis stays: when a core spiked is a real question about a chart
  // that cannot answer how much.
  const valueAxis = axis && !hideAxis;

  const floor = stacked ? 0 : extent(series.flatMap((s) => s.values)).min;
  // ONE interval, not two or three. niceTicks rounds its step DOWN, so the
  // count it is given is a floor on how many labels come back, not a target:
  // asking for two over a 0-100 axis returns five (20/40/60/80/100), and
  // five labels at 12px do not fit a 58px plot -- they collided in the panel
  // before this was tuned. One interval yields the two or three that do.
  // The chart page, with room for eight, asks for more.
  const yTicks = !valueAxis
    ? undefined
    : mirrored
      ? mirroredTicks(effectiveMax, 1, tickBase)
      : niceTicks(floor, effectiveMax, 1, tickBase);
  const xTicks =
    !axis || answered === null
      ? undefined
      : timeTicks(
          Date.parse(answered.from),
          Date.parse(answered.to),
          // Three labels is what 260px holds once the value gutter is taken
          // out of it. timeTicks picks the coarsest step that yields at
          // least this many, so they cannot collide.
          3,
        );
  const tickText = (v: number) => (fmt ? fmt(v) : String(v));
  const widest =
    yTicks === undefined
      ? undefined
      : widestLabel(
          yTicks.filter((t) => t.major).map((t) => tickText(t.value)),
        );

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
        href={href}
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
          y={yTicks}
          x={xTicks}
          format={tickText}
          grid={axis}
          spine={axis}
          labels={axis}
          widestYLabel={widest}
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
      {/* Under the chart, inside the panel. The `unavailable` branch above
          returns before reaching this, which is the point: a meter beneath
          "Not collected" would state a reading for a panel that has none. */}
      {footer !== undefined && <div className="foot">{footer}</div>}
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
