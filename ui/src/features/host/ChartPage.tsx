// One chart, on its own page.
//
// The enlarged view used to be a modal, and the comment defending that
// choice argued modal-versus-expanding-panel: "the surrounding grid of
// twenty panels is exactly the noise being escaped". A page escapes it more
// completely than a scrim does, and it answers something a dialog never
// could -- spec 9's rule that a view someone is looking at should be a link
// they can send. The chart an operator is staring at was the deepest, most
// specific view in the app and the only one with no URL.
import { useCallback, useEffect, useMemo, useState } from "react";
import { ChevronLeft } from "lucide-react";
import { getMetrics, type MetricsResponse } from "../../lib/api";
import { rangeWindow, type Range } from "../../lib/range";
import { RANGE_VALUES } from "./ranges";
import {
  REFERENCE_HEADROOM,
  bandsFor,
  ceilingOf,
  familyFor,
  noCeilingReason,
  specForSlug,
  type Family,
} from "./chartSpecs";
import { Chart, type ChartSeries } from "../../ui/charts/Chart";
import {
  mirroredTicks,
  niceTicks,
  timeLabel,
  timeTicks,
} from "../../ui/charts/ticks";
import { widestLabel } from "../../ui/charts/plot";
import { extent } from "../../ui/charts/geometry";
import { summarise } from "../../ui/charts/ChartDetail";
import { ABSENT, absolute } from "../../lib/format";
import { windowNotice } from "../../lib/metrics";
import { EmptyState } from "../../ui/EmptyState";
import { CircleSlash } from "lucide-react";

const CHART_WIDTH = 1000;
const CHART_HEIGHT = 380;
/** The thumbnails are read as shapes, not values, so they carry no axis. */
const THUMB_WIDTH = 150;
const THUMB_HEIGHT = 34;

export interface ChartPageProps {
  hostId: string;
  slug: string;
  range: Range;
  onRangeChange: (range: Range) => void;
  onBack: () => void;
  /**
   * What Back says it goes back to. The chart page has more than one way in
   * -- the Graphs tab and, since traffic got its own page, the fleet row --
   * and a button reading "Back to graphs" on a page reached from the fleet
   * names somewhere the reader has never been.
   */
  backLabel: string;
  /** Injectable for tests and for the harness; defaults to the real API. */
  fetchFamily?: (family: Family, range: Range) => Promise<MetricsResponse>;
}

export function ChartPage({
  hostId,
  slug,
  range,
  onRangeChange,
  onBack,
  backLabel,
  fetchFamily,
}: ChartPageProps) {
  const spec = specForSlug(slug);

  const load = useCallback(
    (family: Family, next: Range) => {
      if (fetchFamily) return fetchFamily(family, next);
      const window = rangeWindow(next);
      return getMetrics(hostId, {
        family,
        from: window.from,
        to: window.to,
        step: window.step,
      });
    },
    [fetchFamily, hostId],
  );

  if (spec === undefined) {
    return (
      <EmptyState
        icon={CircleSlash}
        title="No such chart"
        body={`This host has no chart called "${slug}".`}
      />
    );
  }

  return (
    <ChartView
      key={slug}
      spec={spec}
      range={range}
      onRangeChange={onRangeChange}
      onBack={onBack}
      backLabel={backLabel}
      load={load}
    />
  );
}

type Spec = NonNullable<ReturnType<typeof specForSlug>>;

function ChartView({
  spec,
  range,
  onRangeChange,
  onBack,
  backLabel,
  load,
}: {
  spec: Spec;
  range: Range;
  onRangeChange: (range: Range) => void;
  onBack: () => void;
  backLabel: string;
  load: (family: Family, range: Range) => Promise<MetricsResponse>;
}) {
  const [res, setRes] = useState<MetricsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  // null until the pointer is over the plot. The crosshair and the "at
  // cursor" column are a reading of where the pointer IS; with no pointer
  // there is no such reading, and a rule frozen at some arbitrary bucket
  // states one nobody asked for.
  const [cursor, setCursor] = useState<number | null>(null);

  useEffect(() => {
    let live = true;
    setLoading(true);
    load(familyFor(spec), range)
      .then((next) => {
        if (!live) return;
        setRes(next);
        setError(null);
      })
      .catch(() => live && setError("Could not load this chart."))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [load, spec, range]);

  // The page has room for the pair: mean as the line, peak as the envelope.
  // The panel this was opened from draws the peak alone, which is right at
  // 260px -- two marks in that space is a smear.
  const series: ChartSeries[] = useMemo(
    () => (res ? bandsFor(spec, res, { withPeakBand: true }) : []),
    [spec, res],
  );

  // A ceiling the DATA carries -- memory's mem_total -- and the dashed rule
  // marking it. Headroom above so the rule lands inside the plot instead of
  // on its border, where it reads as the edge of the box rather than as the
  // host's limit; the same 1.08 the fleet cell uses.
  const reference = useMemo(
    () => (res && spec.ceiling ? ceilingOf(spec.ceiling(res)) : undefined),
    [spec, res],
  );

  // A spec that declares a ceiling and cannot find one MUST NOT fall through
  // to the auto-scale below. That fallback is the always-full bug: a memory
  // stack scaled to its own running total draws every host as nearly out,
  // whatever its headroom. The fleet cell refuses to draw a memory chart with
  // no mem_total (hostColumns.tsx) and says the reading is absent; this is
  // that same rule on the page, rather than the page drawing the lie the cell
  // declines to.
  const noCeiling =
    res !== null && spec.ceiling !== undefined && reference === undefined;

  const ceiling = useMemo(() => {
    if (reference !== undefined) return reference * REFERENCE_HEADROOM;
    if (spec.max !== undefined) return spec.max;
    if (spec.stacked) return runningTotalMax(series);
    // peakOf scans the BAND too, not just the line. The envelope is the
    // bucket's peak and therefore always the taller of the two; scaling to
    // the mean alone would draw it straight out of the plot box.
    return peakOf(series);
  }, [spec, series, reference]);

  const format = (v: number) => (spec.fmt ? spec.fmt(v) : String(v));
  const valueAxis = !spec.hideAxis && !spec.boolean;

  // ONE floor, used both to scale the mark and to label the axis. They were
  // computed separately and disagreed: the ticks stepped from 0 while Chart
  // fell back to the data's own minimum, so the labels named heights the
  // series had never been drawn against. Uptime showed it worst -- a window
  // from 2.6M to 3.2M seconds filled the box top to bottom under an axis
  // reading 0 / 1M / 2M / 3M.
  // spec.min before the data's own minimum: a panel that names its floor is
  // naming its SCALE -- filesystem usage is read against 0-100 or it is not
  // read at all -- and deriving one from the window would draw a different
  // picture from the cell this page was opened from.
  const floor = spec.stacked
    ? 0
    : (spec.min ?? extent(series.flatMap((s) => s.values)).min);

  const yTicks = !valueAxis
    ? undefined
    : spec.mirrored
      ? mirroredTicks(ceiling, 3, spec.tickBase)
      : niceTicks(floor, ceiling, 3, spec.tickBase);
  const xTicks = res
    ? timeTicks(
        Date.parse(res.window.from),
        Date.parse(res.window.to),
        // A 1000-unit plot fits eight labels comfortably; the panel it was
        // opened from fits three.
        8,
      )
    : undefined;
  const widest = yTicks
    ? widestLabel(yTicks.filter((t) => t.major).map((t) => format(t.value)))
    : undefined;

  const buckets = series.reduce((n, s) => Math.max(n, s.values.length), 0);
  const notice = res ? windowNotice(res) : null;
  const at = cursorTime(res, cursor, buckets);

  return (
    <section className="chartpage">
      <div className="crumb">
        <button type="button" className="btn ghost" onClick={onBack}>
          <ChevronLeft size={15} aria-hidden="true" />
          {backLabel}
        </button>
      </div>

      <header>
        <h2>{spec.title}</h2>
        {spec.unit !== undefined && <span className="u">{spec.unit}</span>}
        <div className="spacer" />
        {loading && (
          <span className="note" role="status">
            Loading…
          </span>
        )}
        {error !== null && !loading && (
          <span className="note" role="status">
            {error}
          </span>
        )}
      </header>

      <RangeStrip
        spec={spec}
        active={range}
        onPick={onRangeChange}
        load={load}
      />

      {notice && <p className="note">{notice}</p>}

      {noCeiling ? (
        <p className="note">{noCeilingReason(spec)}</p>
      ) : (
        <Chart
          series={series}
          width={CHART_WIDTH}
          height={CHART_HEIGHT}
          max={ceiling}
          min={floor}
          reference={reference}
          mark={spec.mirrored ? "mirror" : spec.stacked ? "stack" : "line"}
          label={`${spec.title} over time`}
          y={yTicks}
          x={xTicks}
          format={format}
          grid
          spine
          labels
          widestYLabel={widest}
          cursor={cursor}
          onCursorChange={setCursor}
        />
      )}

      {res && !noCeiling && (
        <div className="cd-x">
          <span>{absolute(res.window.from)}</span>
          <span>{absolute(res.window.to)}</span>
        </div>
      )}

      <table className="cd-stats">
        <thead>
          <tr>
            <th scope="col">Series</th>
            {/* The column exists only while the pointer is over the plot. A
                permanently empty one is furniture for a measurement nobody
                is taking, and it pushes every real statistic sideways. */}
            {at !== null && (
              <th scope="col" className="cursor">{`At ${at}`}</th>
            )}
            <th scope="col">Latest</th>
            <th scope="col">Min</th>
            <th scope="col">Max</th>
            <th scope="col">Mean</th>
          </tr>
        </thead>
        <tbody>
          {series.map((s) => {
            const stats = summarise(s.values);
            const here = cursor === null ? null : (s.values[cursor] ?? null);
            return (
              <tr key={s.name}>
                <th scope="row">
                  <i style={{ background: s.color }} />
                  {s.name}
                </th>
                {at !== null && (
                  <td className="cursor">
                    {here === null ? ABSENT : format(here)}
                  </td>
                )}
                <td>{stats.latest === null ? ABSENT : format(stats.latest)}</td>
                <td>{stats.min === null ? ABSENT : format(stats.min)}</td>
                <td>{stats.max === null ? ABSENT : format(stats.max)}</td>
                <td>{stats.mean === null ? ABSENT : format(stats.mean)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </section>
  );
}

/** The moment the cursor is over, formatted for the stats column header. */
function cursorTime(
  res: MetricsResponse | null,
  cursor: number | null,
  buckets: number,
): string | null {
  if (res === null || cursor === null || buckets <= 1) return null;
  const from = Date.parse(res.window.from);
  const to = Date.parse(res.window.to);
  const at = new Date(from + (cursor / (buckets - 1)) * (to - from));
  // timeLabel, not a bare clock: it widens to a weekday and then to a date
  // as the window does. Formatting HH:MM regardless put "At 09:00" in the
  // header for one of thirty days while the axis below it, correctly, said
  // "16 Aug" -- two readings of the same instant disagreeing in one view.
  return timeLabel(at, to - from);
}

/** The largest value across every series, bands included. */
function peakOf(series: readonly ChartSeries[]): number {
  let peak = 0;
  for (const s of series) {
    for (const v of s.values) if (v !== null && v > peak) peak = v;
    for (const v of s.band ?? []) if (v !== null && v > peak) peak = v;
  }
  return peak || 1;
}

function runningTotalMax(series: readonly ChartSeries[]): number {
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
  return best || 1;
}

/**
 * The ranges, drawn as the chart itself rather than listed as buttons.
 *
 * Observium's strongest navigation idea: you see WHERE an anomaly lives
 * before you zoom to it. The active one is marked by dimming the others and
 * by nothing else -- no border, no fill. A thumbnail contains a chart, so
 * any fill tints the ground its series colour sits on and the thumbnail
 * stops matching the chart it opens.
 */
function RangeStrip({
  spec,
  active,
  onPick,
  load,
}: {
  spec: Spec;
  active: Range;
  onPick: (range: Range) => void;
  load: (family: Family, range: Range) => Promise<MetricsResponse>;
}) {
  const [byRange, setByRange] = useState<Record<string, MetricsResponse>>({});

  useEffect(() => {
    let live = true;
    // One request per range, all at once. Affordable only because this page
    // draws ONE chart: the tab it was opened from mounts twenty-odd panels,
    // and five requests each would be a hundred.
    RANGE_VALUES.forEach((r) => {
      load(familyFor(spec), r)
        .then((res) => {
          if (live) setByRange((prev) => ({ ...prev, [r]: res }));
        })
        .catch(() => {
          /* a thumbnail that will not load simply stays empty */
        });
    });
    return () => {
      live = false;
    };
  }, [load, spec]);

  return (
    <div className="strip" role="group" aria-label="Range">
      {RANGE_VALUES.map((r) => {
        const res = byRange[r] ?? null;
        // withPeakBand, same as the chart: without it the thumbnail's line
        // is the bucket PEAK while the chart's is the mean, so the strip
        // would show a different reading of the same window.
        const bands = res ? bandsFor(spec, res, { withPeakBand: true }) : [];
        // Read from THIS range's own response: mem_total is a constant per
        // boot, but the range whose window the host was rebooted in is the
        // one that has to answer for itself.
        const reference =
          res && spec.ceiling ? ceilingOf(spec.ceiling(res)) : undefined;
        return (
          <button
            key={r}
            type="button"
            className="thumb"
            aria-pressed={r === active}
            aria-label={`last ${r}`}
            onClick={() => onPick(r)}
          >
            <Thumb spec={spec} bands={bands} reference={reference} />
            <span className="lab">{r}</span>
          </button>
        );
      })}
    </div>
  );
}

/**
 * A thumbnail draws the SAME mark as the chart it opens, through the same
 * renderer with all furniture off.
 *
 * It used to special-case each mark and hand a sparkline component one
 * series, which meant Load averages, TCP statistics and Hub latency showed
 * ONE of their lines in the strip and all of them in the chart -- so the
 * strip was not five views of one chart for exactly the specs where seeing
 * the shape matters most.
 */
function Thumb({
  spec,
  bands,
  reference,
}: {
  spec: Spec;
  bands: ChartSeries[];
  reference: number | undefined;
}) {
  // A thumbnail for a spec that declares a ceiling and has none is EMPTY,
  // not auto-scaled -- the same refusal the chart above it makes, and for
  // the same reason: a memory stack scaled to its own running total draws
  // every host as nearly full.
  if (
    bands.length === 0 ||
    (spec.ceiling !== undefined && reference === undefined)
  ) {
    return <svg className="spark" width={THUMB_WIDTH} height={THUMB_HEIGHT} />;
  }
  const ceiling = spec.stacked ? runningTotalMax(bands) : peakOf(bands);

  return (
    <Chart
      series={bands}
      width={THUMB_WIDTH}
      height={THUMB_HEIGHT}
      // The data's ceiling first, then the fixed one, then the auto-scale.
      // Without it the five memory thumbnails were each scaled to their own
      // running total and drew a full box, while the chart they open sits
      // under the mem_total rule -- so the strip stopped being five views of
      // one chart for exactly the panel that needs a real scale most.
      max={
        reference !== undefined
          ? reference * REFERENCE_HEADROOM
          : (spec.max ?? ceiling)
      }
      min={spec.stacked ? 0 : spec.min}
      mark={spec.mirrored ? "mirror" : spec.stacked ? "stack" : "line"}
      label=""
    />
  );
}
