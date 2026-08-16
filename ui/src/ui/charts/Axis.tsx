// Everything in a chart that is not the data: the grid behind it, the spine
// framing it, the tick marks on that spine and the labels naming them.
//
// Drawn INSIDE the chart's own SVG, in viewBox units, which is the whole
// point. The axis this replaces was HTML positioned beside the SVG, and the
// two could not agree on a height: .cd-y was a hardcoded 320px while
// svg.spark is responsive, so at a 700px viewport the plot rendered 216px
// tall and every label sat up to 104px from the gridline it named. In the
// viewBox they scale together and cannot drift.

import {
  AXIS_STROKE,
  AXIS_WIDTH,
  GRID_DASH,
  GRID_MAJOR_STROKE,
  GRID_MINOR_STROKE,
  GRID_WIDTH,
  ZERO_STROKE,
  ZERO_WIDTH,
} from "./size";
import {
  AXIS_FONT_PX,
  TICK_MAJOR,
  TICK_MINOR,
  labelWidth,
  xAt,
  yAt,
  type PlotRect,
} from "./plot";
import type { Tick, TimeTick } from "./ticks";

export interface AxisProps {
  rect: PlotRect;
  /** Value-axis ticks, bottom to top. */
  y?: readonly Tick[];
  /** Time-axis ticks, left to right. */
  x?: readonly TimeTick[];
  /** Formats a value tick for display. Without it no value labels are drawn. */
  format?: (value: number) => string;
  /** Draw the gridlines behind the series. */
  grid?: boolean;
  /** Draw the L-shaped spine and its tick marks. */
  spine?: boolean;
  /** Draw the tick labels. Implies room was reserved for them in `rect`. */
  labels?: boolean;
  /** Rule a stronger line at this fraction -- zero on a mirrored chart. */
  zeroAt?: number;
}

/**
 * The grid, drawn behind every series.
 *
 * Minor lines are laid down before major ones so that where the two coincide
 * the heavier line wins. Both use the same dash: the lattice should read as
 * one grid at two weights, not as two kinds of mark.
 */
export function Grid({
  rect,
  y = [],
  x = [],
}: Pick<AxisProps, "rect" | "y" | "x">) {
  const line = (key: string, a: number[], major: boolean) => (
    <line
      key={key}
      data-grid
      data-major={major || undefined}
      x1={a[0]}
      y1={a[1]}
      x2={a[2]}
      y2={a[3]}
      stroke={major ? GRID_MAJOR_STROKE : GRID_MINOR_STROKE}
      strokeWidth={GRID_WIDTH}
      strokeDasharray={GRID_DASH}
    />
  );

  const rows = (major: boolean) =>
    y
      .filter((t) => t.major === major)
      .map((t, i) =>
        line(`y${major}${i}`, [
          rect.left,
          yAt(rect, t.fraction),
          rect.right,
          yAt(rect, t.fraction),
        ], major),
      );

  const cols = (major: boolean) =>
    x
      .filter((t) => t.major === major)
      .map((t, i) =>
        line(`x${major}${i}`, [
          xAt(rect, t.fraction),
          rect.top,
          xAt(rect, t.fraction),
          rect.bottom,
        ], major),
      );

  return (
    <g data-grid-group>
      {rows(false)}
      {cols(false)}
      {rows(true)}
      {cols(true)}
    </g>
  );
}

/**
 * The spine: a solid L down the left of the plot and along its bottom, with
 * a tick mark at every tick.
 *
 * Without it the dotted grid floats -- nothing says where the plot box ends
 * and the card begins, and the leftmost gridline has to double as an axis it
 * is not. Half-unit offsets so a 1-unit stroke lands on a pixel rather than
 * straddling two and rendering as a 2px blur.
 *
 * Ticks come in two lengths. The long ones sit under a label and anchor it;
 * the short unlabelled ones are helpers, and unlike the minor gridlines they
 * survive where a dense series has painted over the lattice.
 */
export function Spine({
  rect,
  y = [],
  x = [],
}: Pick<AxisProps, "rect" | "y" | "x">) {
  const left = rect.left + 0.5;
  const bottom = rect.bottom - 0.5;

  return (
    <g data-spine>
      <path
        d={`M${left},${rect.top} L${left},${bottom} L${rect.right},${bottom}`}
        fill="none"
        stroke={AXIS_STROKE}
        strokeWidth={AXIS_WIDTH}
      />
      {y.map((t, i) => (
        <line
          key={`yt${i}`}
          data-tick
          x1={left}
          x2={left - (t.major ? TICK_MAJOR : TICK_MINOR)}
          y1={yAt(rect, t.fraction)}
          y2={yAt(rect, t.fraction)}
          stroke={AXIS_STROKE}
          strokeWidth={AXIS_WIDTH}
        />
      ))}
      {x.map((t, i) => (
        <line
          key={`xt${i}`}
          data-tick
          x1={xAt(rect, t.fraction)}
          x2={xAt(rect, t.fraction)}
          y1={bottom}
          y2={bottom + (t.major ? TICK_MAJOR : TICK_MINOR)}
          stroke={AXIS_STROKE}
          strokeWidth={AXIS_WIDTH}
        />
      ))}
    </g>
  );
}

/**
 * The tick labels.
 *
 * Value labels are right-anchored in the left margin, their baseline centred
 * on the gridline they name. Time labels sit below the spine, centred on
 * their tick -- except at the two ends, where a centred label would hang
 * outside the image and be clipped, so the first and last anchor their near
 * edge to the tick instead. They stay attached to their own gridline either
 * way.
 *
 * font-size comes from AXIS_FONT_PX rather than from CSS: plot.ts computed
 * this rect's margins from character widths measured at that size, and if
 * the two could drift the margin would be silently wrong.
 */
export function AxisLabels({
  rect,
  y = [],
  x = [],
  format,
}: Pick<AxisProps, "rect" | "y" | "x" | "format">) {
  return (
    <g data-axis-labels>
      {format &&
        y
          .filter((t) => t.major)
          .map((t, i) => (
            <text
              key={`yl${i}`}
              className="axislab"
              data-axis-label="y"
              x={rect.left - TICK_MAJOR - 4}
              y={yAt(rect, t.fraction)}
              fontSize={AXIS_FONT_PX}
              textAnchor="end"
              dominantBaseline="middle"
            >
              {format(t.value)}
            </text>
          ))}
      {x
        // A type predicate rather than a plain filter: the labels below are
        // measured to place them, and measuring needs the narrowing this
        // filter already performs at runtime.
        .filter((t): t is TimeTick & { label: string } =>
          Boolean(t.major && t.label !== null),
        )
        .map((t, i) => (
          <text
            key={`xl${i}`}
            className="axislab"
            data-axis-label="x"
            x={xAt(rect, t.fraction)}
            y={rect.bottom + TICK_MINOR + AXIS_FONT_PX}
            fontSize={AXIS_FONT_PX}
            textAnchor={anchorFor(xAt(rect, t.fraction), t.label, rect)}
          >
            {t.label}
          </text>
        ))}
    </g>
  );
}

/**
 * Centred, unless centring would push the label outside the image.
 *
 * Measured rather than guessed at from the fraction. A fraction threshold
 * cannot know how wide the text is, and the last tick of a 24h axis sits at
 * about 0.95 -- inside any sane threshold, and still wide enough to run off
 * the edge. "12:00" rendered as "12:0" with the rest clipped.
 *
 * The left bound is the plot's own left edge rather than zero: the value
 * labels live in that margin, and a time label reaching into it collides
 * with them rather than with the image boundary.
 */
function anchorFor(
  x: number,
  label: string,
  rect: PlotRect,
): "start" | "middle" | "end" {
  const half = labelWidth(label, AXIS_FONT_PX) / 2;
  if (x - half < rect.left) return "start";
  if (x + half > rect.right) return "end";
  return "middle";
}

/**
 * Zero on a mirrored chart: the line every reading is measured from, so it
 * outranks both the grid and the spine.
 */
export function ZeroRule({
  rect,
  at,
}: {
  rect: PlotRect;
  at: number;
}) {
  return (
    <line
      data-zero
      x1={rect.left}
      x2={rect.right}
      y1={yAt(rect, at)}
      y2={yAt(rect, at)}
      stroke={ZERO_STROKE}
      strokeWidth={ZERO_WIDTH}
    />
  );
}
