// The card that wraps a chart with a title, unit, latest value, legend and
// (when applicable) either a "not collected" state or a clamped-window
// notice. `unavailable` is a product requirement, not a nicety: three
// metric families (IP statistics, ICMP statistics, ICMP informational) have
// no columns in the schema at all, and netra's collector contract is that
// something which cannot run says why -- this panel is where that
// contract reaches the UI.
import { useEffect, useRef, useState } from "react";
import { ABSENT } from "../../lib/format";
import { extent } from "./geometry";
import type { OverlaySeries } from "./Overlay";
import { Overlay } from "./Overlay";
import { ChartDetail } from "./ChartDetail";
import type { Range } from "../../lib/range";

/** A single plotted series. Re-exported as `Band` because that is the name
 * the task brief uses for ChartPanel's `series` prop. */
export type Band = OverlaySeries;

/**
 * The enlarged view's own range, and the data for it.
 *
 * The whole point of this hook is that nothing it returns reaches the page.
 * A reader who enlarges one chart and widens it is asking a question about
 * that chart; answering it by re-ranging the twenty panels behind the dialog
 * -- which is what wiring the dialog to the page's setter did -- refetches
 * the entire tab and moves the toolbar under the thing being read.
 *
 * `series` is null until the range is actually changed, so a dialog opened
 * and closed again costs no request at all: the panel's own data already
 * covers the page's range.
 */
function useDetailRange(
  pageRange: Range | undefined,
  fetchSeries: ((range: Range) => Promise<DetailData>) | undefined,
  open: boolean,
) {
  const [range, setRange] = useState<Range | undefined>(pageRange);
  const [data, setData] = useState<DetailData | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The latest fetcher, without making it a dependency of the effect below.
  // Both callers build it inline -- Graphs closes over `spec`, ContainerPage
  // over which bands to pick -- so it is a new function on every render of
  // the page, and depending on it would refire the dialog's fetch on every
  // poll of the page behind it. Same reason and same shape as usePoll's
  // fnRef in lib/poll.ts.
  const fetchRef = useRef(fetchSeries);
  fetchRef.current = fetchSeries;

  // Closing resets, so reopening starts from the page again rather than from
  // wherever the last look happened to end. The dialog is a question asked
  // and answered, not a second piece of page state to keep track of.
  useEffect(() => {
    if (open) return;
    setRange(pageRange);
    setData(null);
    setError(null);
    // Closed while a fetch was in flight: its resolver bails on `cancelled`
    // below, so without this the header would still say "Loading…" the next
    // time the dialog is opened.
    setLoading(false);
  }, [open, pageRange]);

  useEffect(() => {
    // Nothing to do at the page's own range: the series already on screen
    // are that range's, which is what makes opening the dialog free. Coming
    // BACK to it also ends whatever the last range said: a fetch cancelled
    // on the way here never settles, and "could not load that range" is
    // about a range no longer being shown.
    if (!open || range === undefined || range === pageRange) {
      setLoading(false);
      setError(null);
      return;
    }
    const fetchSeries = fetchRef.current;
    if (fetchSeries === undefined) return;

    let cancelled = false;
    setLoading(true);
    setError(null);
    fetchSeries(range).then(
      (next) => {
        if (cancelled) return;
        setData(next);
        setLoading(false);
      },
      (e: unknown) => {
        if (cancelled) return;
        // The previous series stay on screen (see the series prop below), so
        // this reports the failure without also blanking the chart -- an
        // empty chart would claim the host reported nothing over the window
        // just asked for, which is a different and much worse statement.
        setError(e instanceof Error ? e.message : String(e));
        setLoading(false);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [open, range, pageRange]);

  return {
    range,
    // No picker without a fetcher: buttons that cannot change anything are
    // worse than no buttons.
    setRange: fetchSeries === undefined ? undefined : setRange,
    series: range === pageRange ? null : (data?.series ?? null),
    window: range === pageRange ? null : (data?.window ?? null),
    loading,
    error,
  };
}

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
  /** What the reference rule is, drawn at the line. The small panel has no
   * axis, so without this the rule is unnamed there. */
  referenceLabel?: string;
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

/** What fetchSeries answers: the bands to draw and the window they cover,
 * already shaped -- the caller owns the response-to-bands conversion because
 * only it knows which columns this panel plots. */
export interface DetailData {
  series: Band[];
  window: { from: string; to: string } | null;
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
  referenceLabel,
  mirrored,
  hideAxis,
  window: answered = null,
  range,
  ranges,
  fetchSeries,
}: ChartPanelProps) {
  const [enlarged, setEnlarged] = useState(false);
  const detail = useDetailRange(range, fetchSeries, enlarged);
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
          referenceLabel={referenceLabel}
          mirrored={mirrored}
          label={`${title} over time`}
        />
      </button>
      {notice && <p className="note">{notice}</p>}
      {enlarged && (
        <ChartDetail
          title={title}
          unit={unit}
          // The dialog's own data once its range has been changed, and the
          // panel's otherwise. Keeping the previous series while a fetch is
          // in flight is deliberate: blanking the chart to redraw it a
          // moment later is a flicker, and an empty chart in netra asserts
          // "the host reported nothing".
          series={detail.series ?? series}
          max={max}
          fmt={fmt}
          stacked={stacked}
          legend={legend}
          reference={reference}
          // No label in the enlarged view: it has a y axis, and that axis
          // already names the reference at the height it sits. Only the
          // small panel, which has no axis, needs the rule to say what it is.
          mirrored={mirrored}
          hideAxis={hideAxis}
          window={detail.window ?? answered}
          // The dialog's range and the dialog's setter. The page's setter is
          // deliberately not reachable from here.
          range={detail.range}
          onRangeChange={detail.setRange}
          ranges={ranges}
          loading={detail.loading}
          error={detail.error}
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
