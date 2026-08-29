// The enlarged chart, and the ONE place its furniture is decided.
//
// There used to be two of these. The chart page computed ticks, drew the
// axis labels inside the SVG, and carried a crosshair; the enlarged view
// beside it drew the same series with an HTML gutter of absolutely
// positioned labels, no gridlines, no spine and no time axis. Same metric,
// same window, two pictures -- and the gutter was the older, broken one: it
// is a fixed pixel height beside an SVG that scales to its container, so
// every label drifts from the line it names as soon as the panel is narrower
// than the width the chart was drawn at.
//
// Everything above the small-panel size is this component now. A caller
// brings the series and the scale it already decided; what surrounds them is
// not the caller's business.

import { useState } from "react";
import { Chart, markFor, type ChartSeries } from "./Chart";
import { mirrorPeaks } from "./geometry";
import { mirroredTicks, niceTicks, timeLabel, timeTicks } from "./ticks";
import { widestLabel } from "./plot";
import { ABSENT, absolute } from "../../lib/format";

export interface ChartFigureProps {
  series: ChartSeries[];
  width: number;
  height: number;
  /** The floor and ceiling the mark is scaled to. Both resolved by the
   * caller: a spec's declared max, a host's mem_total, or the data's own
   * extent are scaling POLICY, and this component only draws what it is
   * handed. Passing them in is also what keeps the axis honest -- the ticks
   * below step from the same two numbers the shape does. */
  min: number;
  max: number;
  /** A value to mark with a dashed rule, e.g. a host's total memory. */
  reference?: number;
  /**
   * Fill the area under a line, rather than drawing the line alone.
   *
   * For a FREE-SCALED chart only, and that restriction is the whole design.
   * A filled area reads as a mass, and a mass is only honest when its bottom
   * edge means something: on a free-scaled chart the floor is the quietest
   * reading in the window, so the fill is the band the series actually moved
   * through. On a chart pinned to a declared floor it is not -- filesystem
   * usage between 40 % and 95 % against a fixed 0-100 draws four hosts as
   * four solid blocks differing only along their top edge, which is the
   * argument Sparkline.tsx has always made for turning its own fill off
   * there.
   *
   * Says nothing about how MANY series may be filled: areaFillOpacity() in
   * size.ts thins each area by the count sharing the baseline, so a panel of
   * six independent lines cannot compound into something stack-shaped.
   *
   * Ignored by the stack and mirror marks, which are filled by construction.
   */
  filled?: boolean;
  stacked?: boolean;
  mirrored?: boolean;
  /**
   * Drop the VALUE axis -- and only that.
   *
   * A boolean series, or an unnormalised per-core stack running to N x 100,
   * has a height that is a shape rather than a quantity: labelling it puts a
   * number on screen that means nothing. Gridlines, the spine and the time
   * axis stay, because "when" is a real question about every chart in the
   * app.
   */
  hideAxis?: boolean;
  format: (v: number) => string;
  /** 1024 for byte quantities, so the ticks land on 512 MB rather than
   * 500 MB. */
  tickBase?: 1000 | 1024;
  /** The answered window, for the time axis. Absent, the time axis is
   * omitted rather than guessed -- a chart with invented times is worse than
   * one with none. */
  window?: { from: string; to: string } | null;
  label: string;
}

export function ChartFigure({
  series,
  width,
  height,
  min,
  max,
  reference,
  filled,
  stacked,
  mirrored,
  hideAxis,
  format,
  tickBase,
  window: answered = null,
  label,
}: ChartFigureProps) {
  // null until the pointer is over the plot. The crosshair is a reading of
  // where the pointer IS; with no pointer there is no such reading, and a
  // rule frozen at some arbitrary bucket states one nobody asked for. The
  // "at cursor" COLUMN is drawn either way and reads as absent -- see the
  // table below for why it does not come and go.
  const [cursor, setCursor] = useState<number | null>(null);

  const valueAxis = !hideAxis;
  // Same two peaks the marks use -- see ChartPanel.
  const figureHalves = mirrorPeaks(
    // The BAND where a series carries one: it is the peak envelope drawn
    // behind the mean and is always the taller of the two, so a ceiling
    // taken from the mean alone would draw the envelope outside the plot.
    series.map((s) => (s.band && s.band.length > 0 ? s.band : s.values)),
    stacked ?? false,
  );
  const yTicks = !valueAxis
    ? undefined
    : mirrored
      ? mirroredTicks(figureHalves.up, figureHalves.down, 3, tickBase)
      : niceTicks(min, max, 3, tickBase);

  const xTicks = answered
    ? timeTicks(
        Date.parse(answered.from),
        Date.parse(answered.to),
        timeTickTarget(width),
      )
    : undefined;

  const widest = yTicks
    ? widestLabel(yTicks.filter((t) => t.major).map((t) => format(t.value)))
    : undefined;

  const buckets = series.reduce((n, s) => Math.max(n, s.values.length), 0);
  const at = cursorTime(answered, cursor, buckets);

  return (
    <>
      <Chart
        series={series}
        width={width}
        height={height}
        min={min}
        max={max}
        reference={reference}
        // The same four-way choice Overlay makes, and it has to stay the
        // same one: this is the ENLARGED view of a panel Overlay drew, and a
        // mark that differs here is a different chart on click.
        mark={markFor({ filled, stacked, mirrored })}
        label={label}
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

      {answered && (
        <div className="cd-x">
          <span>{absolute(answered.from)}</span>
          <span>{absolute(answered.to)}</span>
        </div>
      )}

      {/* What the small panel has no room for: every series named, with the
          numbers a reader would otherwise have to eyeball. This is also why
          the figure carries no legend -- the swatch in the first column
          names every series already, and a second naming of the same six
          things above the chart is furniture. */}
      <table className="cd-stats">
        <thead>
          <tr>
            <th scope="col">Series</th>
            {/* Always here, empty until the pointer is over the plot. It was
                mounted on hover and unmounted on leave, which meant every
                statistic in the table jumped sideways the moment the pointer
                crossed the chart and jumped back when it left -- the reading
                moved under the eye that was going to read it. An absent
                marker holds the space and says the same thing. */}
            <th scope="col" className="cursor">{`At ${at ?? ABSENT}`}</th>
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
                <td className="cursor">
                  {here === null ? ABSENT : format(here)}
                </td>
                <td>{stats.latest === null ? ABSENT : format(stats.latest)}</td>
                <td>{stats.min === null ? ABSENT : format(stats.min)}</td>
                <td>{stats.max === null ? ABSENT : format(stats.max)}</td>
                <td>{stats.mean === null ? ABSENT : format(stats.mean)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </>
  );
}

/**
 * How many time labels to aim for at a given plot width.
 *
 * A 1000-unit plot fits eight comfortably and a 260px panel fits three, so
 * this is that ratio rather than a constant: the enlarged view is not one
 * fixed size any more, and a narrow one asking for eight labels overlaps
 * them into a smear. timeTicks treats it as a target and picks the coarsest
 * step that clears it, so an off-by-one here costs nothing.
 */
function timeTickTarget(width: number): number {
  return Math.max(3, Math.min(8, Math.round(width / 125)));
}

/** The moment the cursor is over, formatted for the stats column header. */
function cursorTime(
  answered: { from: string; to: string } | null,
  cursor: number | null,
  buckets: number,
): string | null {
  if (answered === null || cursor === null || buckets <= 1) return null;
  const from = Date.parse(answered.from);
  const to = Date.parse(answered.to);
  if (!Number.isFinite(from) || !Number.isFinite(to)) return null;
  const at = new Date(from + (cursor / (buckets - 1)) * (to - from));
  // timeLabel, not a bare clock: it widens to a weekday and then to a date
  // as the window does. Formatting HH:MM regardless put "At 09:00" in the
  // header for one of thirty days while the axis below it, correctly, said
  // "16 Aug" -- two readings of the same instant disagreeing in one view.
  return timeLabel(at, to - from);
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
