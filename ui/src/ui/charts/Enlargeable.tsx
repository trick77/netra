// The click-to-enlarge affordance, on its own.
//
// It used to live inside ChartPanel, which is a whole CARD -- a section with
// a title, a unit and a headline value. That made enlarging a property of
// the card rather than of the chart, so every sparkline drawn somewhere a
// card does not fit -- a 32px table cell, a row in the sensor list -- was
// silently inert. This is that behaviour with the card taken off: whatever
// is handed as children becomes the button, and pressing it opens the same
// ChartDetail the panels open.
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { OverlaySeries } from "./Overlay";
import { ChartDetail } from "./ChartDetail";
import { scaleFor } from "./scale";
import type { RailEntry } from "./RangeRail";
import { RAIL_RANGES, type Range } from "../../lib/range";

/** What fetchSeries answers: the bands to draw and the window they cover,
 * already shaped -- the caller owns the response-to-bands conversion because
 * only it knows which columns this chart plots. */
export interface DetailData {
  series: OverlaySeries[];
  window: { from: string; to: string } | null;
}

/**
 * The enlarged view's own range, the data for it, and the data for every
 * OTHER range on the rail.
 *
 * The whole point of this hook is that nothing it returns reaches the page.
 * A reader who enlarges one chart and widens it is asking a question about
 * that chart; answering it by re-ranging the twenty panels behind the dialog
 * -- which is what wiring the dialog to the page's setter did -- refetches
 * the entire tab and moves the toolbar under the thing being read.
 *
 * It fetches the WHOLE LADDER on open, not just the range being shown,
 * because the rail is not a set of buttons: each tile is a drawn preview of
 * its window, and a tile that has not been fetched has nothing to draw. That
 * is seven requests per opening -- the page's own range is already on screen
 * and is seeded rather than asked for -- and it stays seven however many
 * charts the page carries, because only one dialog is ever open.
 *
 * The `open` gate is what makes that affordable, and it is load-bearing well
 * beyond tidiness: the fleet list mounts one Enlargeable per chart cell, so
 * twenty hosts is sixty of them. Fetching for a dialog nobody opened would
 * turn one page render into four hundred requests.
 *
 * `series` is null while the shown range is the page's own, so a dialog
 * opened and closed again costs the rail's requests and nothing for the
 * chart itself: the data already on screen covers that window.
 */
export function useDetailRange(
  pageRange: Range | undefined,
  fetchSeries: ((range: Range) => Promise<DetailData>) | undefined,
  open: boolean,
  ranges: readonly Range[] = RAIL_RANGES,
  /** The page's own series, seeding the tile for the page's range so it is
   * not fetched a second time. */
  pageData?: DetailData,
) {
  const [range, setRange] = useState<Range | undefined>(pageRange);
  const [entries, setEntries] = useState<Partial<Record<Range, RailEntry>>>({});

  // The latest fetcher, without making it a dependency of the effect below.
  // Callers build it inline -- Graphs closes over `spec`, ContainerPage over
  // which bands to pick, a table cell over its own row -- so it is a new
  // function on every render of the page, and depending on it would refire
  // every one of the dialog's fetches on every poll of the page behind it.
  // Same reason and same shape as usePoll's fnRef in lib/poll.ts.
  const fetchRef = useRef(fetchSeries);
  fetchRef.current = fetchSeries;

  // Same treatment, same reason: the page's series are a new array on every
  // poll, and the seed below must not refire the ladder because of it.
  const pageDataRef = useRef(pageData);
  pageDataRef.current = pageData;

  // A stable identity for the ladder, so a caller passing an inline array
  // literal does not refetch eight windows on every render of the page.
  const ladder = useMemo(() => ranges.join(","), [ranges]);

  // Closing resets, so reopening starts from the page again rather than from
  // wherever the last look happened to end. The dialog is a question asked
  // and answered, not a second piece of page state to keep track of.
  useEffect(() => {
    if (open) return;
    setRange(pageRange);
    setEntries({});
  }, [open, pageRange]);

  useEffect(() => {
    if (!open) return;
    const fetchSeries = fetchRef.current;
    if (fetchSeries === undefined) return;

    let cancelled = false;
    const wanted = ladder.split(",") as Range[];

    // The page's range is already drawn on the page behind this dialog.
    // Seeding it costs nothing and removes one request from every opening.
    const seed: Partial<Record<Range, RailEntry>> = {};
    if (pageRange !== undefined && pageDataRef.current !== undefined) {
      seed[pageRange] = {
        data: pageDataRef.current,
        loading: false,
        error: null,
      };
    }
    setEntries({
      ...seed,
      ...Object.fromEntries(
        wanted
          .filter((r) => seed[r] === undefined)
          .map((r) => [r, { data: null, loading: true, error: null }]),
      ),
    });

    // All at once rather than in sequence. They are independent reads of one
    // host, the rail is only useful once several tiles are drawn, and
    // widening the window does not make the query meaningfully slower -- the
    // hub answers a year from the daily tier, which is 365 rows.
    for (const r of wanted) {
      if (seed[r] !== undefined) continue;
      fetchSeries(r).then(
        (next) => {
          if (cancelled) return;
          setEntries((prev) => ({
            ...prev,
            [r]: { data: next, loading: false, error: null },
          }));
        },
        (e: unknown) => {
          if (cancelled) return;
          // The tile says so and the chart keeps whatever it had. An empty
          // chart would claim the host reported nothing over that window,
          // which is a different and much worse statement.
          setEntries((prev) => ({
            ...prev,
            [r]: {
              data: null,
              loading: false,
              error: e instanceof Error ? e.message : String(e),
            },
          }));
        },
      );
    }
    return () => {
      cancelled = true;
    };
  }, [open, ladder, pageRange]);

  const shown = range === undefined ? undefined : entries[range];

  return {
    range,
    // No picker without a fetcher: tiles that cannot change anything, and
    // have nothing to draw, are worse than no tiles.
    setRange: fetchSeries === undefined ? undefined : setRange,
    entries,
    series: range === pageRange ? null : (shown?.data?.series ?? null),
    window: range === pageRange ? null : (shown?.data?.window ?? null),
    loading: shown?.loading ?? false,
    error: shown?.error ?? null,
  };
}

export interface EnlargeableProps {
  /** Names the dialog, and the button unless `label` overrides it. */
  title: string;
  /** The panel's explanation, carried into the dialog header so that
   * enlarging a chart does not lose the sentence that made it readable. See
   * ChartPanel's prop of the same name. */
  about?: string;
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
  /** Fill the area under a line. Forwarded so the enlarged view draws the
   * same mark as the panel it was opened from; see Overlay's prop. */
  filled?: boolean;
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
  /** The ladder the dialog's rail draws. Defaults to RAIL_RANGES, which is
   * what every caller wants: the rail is one chart's own question, so the
   * reasons a PAGE narrows its picker do not apply to it. See RAIL_RANGES in
   * lib/range. */
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
  about,
  unit,
  series,
  detailSeries,
  max,
  min,
  autoScale,
  fmt,
  filled,
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
  // What the page's own range already has on screen. Handed to the hook so
  // the rail's tile for that window is seeded rather than fetched -- it is
  // the same window, drawn from the same data, one panel away.
  const pageData = useMemo(
    () => ({ series: detailSeries ?? series, window: answered }),
    [detailSeries, series, answered],
  );
  const detail = useDetailRange(range, fetchSeries, enlarged, ranges, pageData);

  // What the dialog is actually showing: its own data once the range has been
  // changed, the caller's otherwise. Keeping the previous series while a
  // fetch is in flight is deliberate: blanking the chart to redraw it a
  // moment later is a flicker, and an empty chart in netra asserts "the host
  // reported nothing".
  const shown = detail.series ?? detailSeries ?? series;
  const span = scaleFor(shown, detail.series, {
    autoScale,
    min,
    max,
    stacked,
    mirrored,
  });

  return (
    <>
      {/* A button, not a div with a click handler: opening the enlarged view
          has to work from the keyboard, and the chart IS the affordance --
          the zoom-in pointer and the focus ring say so, and nothing is drawn
          on top of it. A (+) badge was tried here and taken off again: in
          the corner of a 150x45 table cell it read as clutter in the row,
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
          about={about}
          unit={unit}
          series={shown}
          max={span.max}
          min={span.min}
          fmt={fmt}
          filled={filled}
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
          entries={detail.entries}
          // The rail draws the same mark and scales by the same policy as
          // the figure it sits beside; both come from these.
          autoScale={autoScale}
          railMin={min}
          railMax={max}
          loading={detail.loading}
          error={detail.error}
          onClose={() => setEnlarged(false)}
        />
      )}
    </>
  );
}
