// The click-to-enlarge affordance, on its own.
//
// It used to live inside ChartPanel, which is a whole CARD -- a section with
// a title, a unit and a headline value. That made enlarging a property of
// the card rather than of the chart, so every sparkline drawn somewhere a
// card does not fit -- a 32px table cell, a row in the sensor list -- was
// silently inert. This is that behaviour with the card taken off: whatever
// is handed as children becomes the button, and pressing it opens the same
// ChartDetail the panels open.
import { useEffect, useRef, useState, type ReactNode } from "react";
import type { OverlaySeries } from "./Overlay";
import { extent } from "./geometry";
import { ChartDetail, peak } from "./ChartDetail";
import type { Range } from "../../lib/range";

/** What fetchSeries answers: the bands to draw and the window they cover,
 * already shaped -- the caller owns the response-to-bands conversion because
 * only it knows which columns this chart plots. */
export interface DetailData {
  series: OverlaySeries[];
  window: { from: string; to: string } | null;
}

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
 * and closed again costs no request at all: the chart's own data already
 * covers the page's range.
 */
export function useDetailRange(
  pageRange: Range | undefined,
  fetchSeries: ((range: Range) => Promise<DetailData>) | undefined,
  open: boolean,
) {
  const [range, setRange] = useState<Range | undefined>(pageRange);
  const [data, setData] = useState<DetailData | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The latest fetcher, without making it a dependency of the effect below.
  // Callers build it inline -- Graphs closes over `spec`, ContainerPage over
  // which bands to pick, a table cell over its own row -- so it is a new
  // function on every render of the page, and depending on it would refire
  // the dialog's fetch on every poll of the page behind it. Same reason and
  // same shape as usePoll's fnRef in lib/poll.ts.
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
    //
    // `open` gates the whole hook, and that gate is load-bearing well beyond
    // tidiness: the fleet list mounts one of these per chart cell, so twenty
    // hosts is sixty of them. Fetching for a dialog nobody opened would turn
    // one page render into sixty requests.
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

/**
 * The extent to draw a free-scaled chart against, or nothing when the series
 * has no readings to scale to.
 *
 * The test is whether anything was REPORTED, not whether the extent has
 * width. A flat series is a real reading -- a fan pinned at one speed, a rail
 * that never moved -- and its degenerate span is what the sparkline above is
 * already drawn from: scaleY() centres a min === max series in the box rather
 * than dividing by zero. Rejecting it here would drop the dialog back to a
 * zero floor and draw the same fan at the top of a 0-1200 box, which is the
 * disagreement autoScale exists to prevent.
 *
 * An EMPTY series is the other case, and it is reachable rather than
 * theoretical: widen a sensor chart to 7d and the 1h tier materialises about
 * ninety minutes behind now, so a window with no rows comes back with
 * nothing. extent() answers {min: 0, max: 0} for it, and passing that 0
 * through would ALSO defeat ChartDetail's own `max || 1` guard, because
 * `0 ?? x` is 0 and the guard is only reached for an absent max.
 */
function derived(series: readonly OverlaySeries[]): {
  min?: number;
  max?: number;
} {
  const values = series.flatMap((s) => s.values);
  return values.some((v) => v !== null) ? extent(values) : {};
}

/**
 * The given ceiling, raised if the data now on screen would not fit under it.
 *
 * A fixed `max` is a deliberate choice about the SMALL chart: the container
 * lists share one ceiling down the column so the rows can be compared, and
 * the fleet's CPU cell pins 100 so an idle host and a saturated one do not
 * draw the same silhouette. Opening the dialog keeps it, which is the point
 * -- a chart that rescaled itself on opening would redraw the shape the
 * reader just clicked.
 *
 * Widening the range is the other case. The refetched window is this one
 * chart's alone, and it is asked for precisely to find something the page's
 * window did not show. Held to the old ceiling, any bucket above it is drawn
 * OUTSIDE the plot -- linePath() deliberately never clamps (geometry.ts) --
 * so the spike someone widened the window to see is the one thing that
 * disappears off the top.
 *
 * Raised only, never lowered: a quieter wide window keeps the ceiling it was
 * opened with, so the shape stays comparable with the cell behind it.
 */
function fitted(
  max: number | undefined,
  refetched: OverlaySeries[] | null,
  stacked: boolean | undefined,
): number | undefined {
  if (max === undefined || refetched === null) return max;
  return Math.max(max, peak(refetched, stacked));
}

export interface EnlargeableProps {
  /** Names the dialog, and the button unless `label` overrides it. */
  title: string;
  unit?: string;
  /** The bands the DIALOG draws. Usually the same data the child sparkline
   * is drawing, reshaped as bands -- the two must not disagree. */
  series: OverlaySeries[];
  max?: number;
  /** The floor the dialog scales from. ChartDetail's own default is 0, which
   * is right for anything measured from zero and wrong for a series that is
   * free-scaled to its own extent: a sensor sitting between 44 and 47 degrees
   * draws as a flat line against a 0-47 box. Sites whose sparkline scales to
   * its own extent pass `autoScale` instead of a fixed floor. */
  min?: number;
  /**
   * Scale the dialog to the extent of whatever it is currently showing,
   * floor included, the way Sparkline scales itself.
   *
   * A fixed `min`/`max` would be the window's extent frozen at the moment the
   * dialog was opened, so widening the range would draw the new data against
   * the old window's floor -- a spike outside it clipped, and the axis
   * labelling heights the shape was never scaled to. Only for charts whose
   * small version free-scales; anything measured from zero must stay at zero.
   */
  autoScale?: boolean;
  /**
   * What the ENLARGED view draws, when that is not what the small one draws.
   *
   * The panel and the dialog are the same chart at two sizes, and for almost
   * every caller that means the same series. The exception is the pair: a
   * mean line with the bucket peak as a pale envelope under it. There is
   * room for both at 1000px and not at 260, where two marks are a smear --
   * so the panel keeps the single mark and the dialog gets the pair.
   *
   * Absent, the dialog draws `series`, which is what every other caller
   * wants and what all of them did before this existed.
   */
  detailSeries?: OverlaySeries[];
  fmt?: (n: number | null) => string;
  stacked?: boolean;
  reference?: number;
  mirrored?: boolean;
  /** The ladder the enlarged view's value ticks step on: 1024 for a byte
   * quantity, 1000 otherwise. See ChartPanel's prop of the same name. */
  tickBase?: 1000 | 1024;
  hideAxis?: boolean;
  /** The answered window, for the dialog's time axis. */
  window?: { from: string; to: string } | null;
  /** The range the PAGE is showing. Seeds the dialog's own picker and is
   * what that picker returns to when the dialog is closed and reopened. It
   * is never written back. */
  range?: Range;
  /** The ranges to offer in the dialog. Defaults to every range; pages that
   * resolve a narrower set pass it so the dialog cannot ask for a window the
   * page's fetcher will not serve. */
  ranges?: readonly Range[];
  /** Loads these series at another range, for the dialog alone. Without it
   * the dialog carries no picker. */
  fetchSeries?: (range: Range) => Promise<DetailData>;
  /**
   * The button's accessible name. Defaults to `Enlarge {title}`, which is
   * enough for a titled card and not enough in a list: twenty rows of
   * "Enlarge CPU" name twenty different charts identically. List sites pass
   * the row's own name.
   */
  label?: string;
  /** Extra classes on the button. `inline` is the compact variant, for a
   * sparkline in a table cell or a flex row. */
  className?: string;
  /** The small chart. It IS the button. */
  children: ReactNode;
}

export function Enlargeable({
  title,
  unit,
  series,
  detailSeries,
  max,
  min,
  autoScale,
  fmt,
  stacked,
  reference,
  mirrored,
  tickBase,
  hideAxis,
  window: answered = null,
  range,
  ranges,
  fetchSeries,
  label,
  className,
  children,
}: EnlargeableProps) {
  const [enlarged, setEnlarged] = useState(false);
  // Gated on `enlarged` alone. It used to be gated on href being absent too,
  // because an href meant navigation and the enlarged view never opened;
  // there are no chart pages any more, so every chart opens the dialog and
  // the range fetch belongs to "is it open", full stop. The gate still
  // matters as much as it ever did: the fleet list mounts one of these per
  // chart cell, so twenty hosts is sixty of them, and fetching for a view
  // nobody opened would turn one page render into sixty requests.
  const detail = useDetailRange(range, fetchSeries, enlarged);

  // What the dialog is actually showing: its own data once the range has been
  // changed, the caller's otherwise. Keeping the previous series while a
  // fetch is in flight is deliberate: blanking the chart to redraw it a
  // moment later is a flicker, and an empty chart in netra asserts "the host
  // reported nothing".
  const shown = detail.series ?? detailSeries ?? series;
  const span = autoScale
    ? derived(shown)
    : { min, max: fitted(max, detail.series, stacked) };

  return (
    <>
      {/* A button, not a div with a click handler: opening the enlarged view
          has to work from the keyboard, and the chart IS the affordance --
          the zoom-in pointer and the focus ring say so, and nothing is drawn
          on top of it. A (+) badge was tried here and taken off again: in
          the corner of a 170x32 table cell it read as clutter in the row,
          not as an invitation, and a fleet page carries sixty of them. */}
      <button
        type="button"
        className={`chartwrap as-button${className ? ` ${className}` : ""}`}
        onClick={() => setEnlarged(true)}
        aria-label={label ?? `Enlarge ${title}`}
      >
        {children}
      </button>
      {enlarged && (
        <ChartDetail
          title={title}
          unit={unit}
          series={shown}
          max={span.max}
          min={span.min}
          fmt={fmt}
          stacked={stacked}
          reference={reference}
          mirrored={mirrored}
          tickBase={tickBase}
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
    </>
  );
}
