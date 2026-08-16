// The one chart renderer. Every non-sparkline chart in the app is this
// component with different props, and every sparkline is this component with
// all of the furniture switched off.
//
// The distinction that matters here is that SIZE and FURNITURE are separate
// knobs, not one preset. A fleet cell is 170x32 with nothing on it; a panel
// is 260x112 with a compact axis; a chart page is full width with everything.
// Those are three sizes and three furniture sets, but they are not a single
// enum -- hostColumns draws an Overlay at sparkline size, and a container
// list sparkline is itself enlargeable. So every piece of furniture below is
// an opt-in prop that defaults to absent, and a caller that asks for nothing
// gets exactly the mark it drew before.

import { useId } from "react";
import {
  areaPath,
  dotPath,
  extent,
  linePath,
  mirrorPaths,
  stackBands,
} from "./geometry";
import {
  AXIS_STROKE,
  AXIS_WIDTH,
  MIRROR_FILL_OPACITY,
  MIRROR_STROKE_WIDTH,
  REFERENCE_DASH,
  REFERENCE_STROKE,
  REFERENCE_WIDTH,
} from "./size";
import {
  layout,
  plotHeight,
  plotWidth,
  xAt,
  yAt,
  type PlotRect,
} from "./plot";
import { AxisLabels, Grid, Spine, ZeroRule } from "./Axis";
import type { Tick, TimeTick } from "./ticks";

export interface ChartSeries {
  name: string;
  /** A CSS variable string, e.g. "var(--s1)". Never a hex literal: colour
   * identity for a series is the caller's decision. */
  color: string;
  values: (number | null)[];
  /**
   * The bucket's PEAK, drawn as a pale envelope beneath the line.
   *
   * The rollups materialise avg and max per bucket and a chart has always
   * drawn one or the other. Drawing both is what lets a reader see the burst
   * and the typical level at once. Absent at the raw tier, where the sample
   * IS its own peak and there is no _max column to ask for.
   */
  band?: (number | null)[];
}

export type ChartMark = "line" | "area" | "stack" | "mirror";

export interface ChartProps {
  series: ChartSeries[];
  width: number;
  height: number;
  /** The ceiling the shape is scaled to. */
  max: number;
  /** The floor. Defaults to the data's own minimum, for a free-scaled chart. */
  min?: number;
  mark?: ChartMark;
  pad?: number;
  label?: string;
  /** Dim every series but this one, rather than hiding them. */
  highlight?: string;
  /** A value to mark with a dashed rule, e.g. a host's total memory. */
  reference?: number;

  // ---- furniture, all absent by default ----
  y?: readonly Tick[];
  x?: readonly TimeTick[];
  /** Formats a value tick. Without it, no value labels are drawn. */
  format?: (value: number) => string;
  grid?: boolean;
  spine?: boolean;
  labels?: boolean;
  /** The widest value label, so the left margin can be reserved for it. */
  widestYLabel?: string;
  /** The index the crosshair sits at, or null when nothing is hovered. */
  cursor?: number | null;
}

/**
 * Where the mark is drawn, and what furniture surrounds it.
 *
 * The rect is computed once and everything maps through it, so a gridline, a
 * label and the series they describe cannot disagree about where a value
 * sits however the image is scaled.
 */
export function Chart({
  series,
  width,
  height,
  max,
  min,
  mark = "line",
  pad = 2,
  label = "chart",
  highlight,
  reference,
  y,
  x,
  format,
  grid = false,
  spine = false,
  labels = false,
  widestYLabel,
  cursor = null,
}: ChartProps) {
  // React's useId, not a counter: two charts on one page must not mint the
  // same clip-path id, and a module-level counter is not stable across a
  // server render and a hydration.
  const clipId = useId();

  const rect = labels
    ? layout(width, height, {
        yLabel: widestYLabel,
        xLabel: x && x.length > 0 ? "00:00" : undefined,
      })
    : layout(width, height);

  const w = plotWidth(rect);
  const h = plotHeight(rect);

  const { min: autoMin } = extent(series.flatMap((s) => s.values));
  const floor = min ?? autoMin;

  const referenceY =
    reference === undefined || max <= floor
      ? null
      : h - pad - ((reference - floor) / (max - floor)) * (h - 2 * pad);

  const mirrored = mark === "mirror";
  const stacked = mark === "stack";

  return (
    <svg
      className="spark"
      viewBox={`0 0 ${width} ${height}`}
      width={width}
      height={height}
      role="img"
      aria-label={label}
    >
      {grid && <Grid rect={rect} y={y} x={x} />}

      {/* The series are clipped to the plot rect so an overflowing value
          cannot paint over the axis labels. This is NOT clamping, which
          mirrorPaths deliberately refuses to do: the value still visibly
          escapes the box, it just cannot reach the margins. */}
      <clipPath id={clipId}>
        <rect x={rect.left} y={rect.top} width={w} height={h} />
      </clipPath>

      <g
        clipPath={`url(#${clipId})`}
        transform={`translate(${rect.left},${rect.top})`}
      >
        {referenceY !== null && (
          <line
            data-reference
            x1={0}
            x2={w}
            y1={referenceY}
            y2={referenceY}
            stroke={REFERENCE_STROKE}
            strokeWidth={REFERENCE_WIDTH}
            strokeDasharray={REFERENCE_DASH}
          />
        )}

        {mirrored && <MirrorMarks {...{ series, w, h, max, pad }} />}
        {!mirrored && stacked && (
          <StackMarks {...{ series, w, h, max, pad, highlight }} />
        )}
        {!mirrored && !stacked && (
          <LineMarks
            {...{ series, w, h, max, pad, highlight, floor }}
            filled={mark === "area"}
          />
        )}
      </g>

      {spine && <Spine rect={rect} y={y} x={x} />}
      {mirrored && <ZeroRule rect={rect} at={0.5} />}
      {labels && <AxisLabels rect={rect} y={y} x={x} format={format} />}

      {cursor !== null && (
        <Crosshair
          rect={rect}
          index={cursor}
          count={longest(series)}
          series={series}
          max={max}
          min={floor}
          pad={pad}
          mirrored={mirrored}
        />
      )}
    </svg>
  );
}

function longest(series: readonly ChartSeries[]): number {
  return series.reduce((n, s) => Math.max(n, s.values.length), 0);
}

/**
 * Mirrored pairs about a midline: series arrive in twos, (0,1) being one
 * interface's in and out. Traffic has a direction, and drawing both as lines
 * climbing one axis makes a reader compare shapes to answer "which way".
 */
function MirrorMarks({
  series,
  w,
  h,
  max,
  pad,
}: {
  series: ChartSeries[];
  w: number;
  h: number;
  max: number;
  pad: number;
}) {
  return (
    <>
      {Array.from({ length: Math.ceil(series.length / 2) }, (_, p) => {
        const up = series[p * 2];
        const down = series[p * 2 + 1];
        if (up === undefined) return null;
        const paths = mirrorPaths(up.values, down?.values ?? [], w, h, max, pad);
        // The envelope, when the tier carries a peak column. Drawn first so
        // the mean sits over it, and with no stroke -- it is a region, not a
        // reading, and an edge on it would compete with the line that is.
        const bands =
          up.band || down?.band
            ? mirrorPaths(
                up.band ?? [],
                down?.band ?? [],
                w,
                h,
                max,
                pad,
              )
            : null;
        return (
          <g key={up.name} data-series={up.name} data-mirror>
            {bands && (
              <>
                <path
                  data-band-up
                  d={bands.up}
                  fill={up.color}
                  fillOpacity={0.18}
                  stroke="none"
                />
                {down !== undefined && (
                  <path
                    data-band-down
                    d={bands.down}
                    fill={down.color}
                    fillOpacity={0.18}
                    stroke="none"
                  />
                )}
              </>
            )}
            <path
              data-up
              d={paths.up}
              fill={up.color}
              fillOpacity={MIRROR_FILL_OPACITY}
              stroke={up.color}
              strokeWidth={MIRROR_STROKE_WIDTH}
            />
            {down !== undefined && (
              <path
                data-down
                d={paths.down}
                fill={down.color}
                fillOpacity={MIRROR_FILL_OPACITY}
                stroke={down.color}
                strokeWidth={MIRROR_STROKE_WIDTH}
              />
            )}
            <line
              data-mid
              x1={0}
              x2={w}
              y1={paths.mid}
              y2={paths.mid}
              stroke={AXIS_STROKE}
              strokeWidth={AXIS_WIDTH}
            />
          </g>
        );
      })}
    </>
  );
}

/** Cumulative bands, whose silhouette is the total rather than the top layer. */
function StackMarks({
  series,
  w,
  h,
  max,
  pad,
  highlight,
}: {
  series: ChartSeries[];
  w: number;
  h: number;
  max: number;
  pad: number;
  highlight?: string;
}) {
  const bands = stackBands(
    series.map((s) => s.values),
    w,
    h,
    max,
    pad,
  );
  return (
    <>
      {series.map((s, i) => (
        <path
          key={s.name}
          data-series={s.name}
          data-band
          d={bands[i] ?? ""}
          fill={s.color}
          fillOpacity={0.55}
          stroke={s.color}
          strokeWidth={1.25}
          strokeLinejoin="round"
          opacity={highlight !== undefined && highlight !== s.name ? 0.35 : 1}
        />
      ))}
    </>
  );
}

/** Independent lines, optionally filled to the baseline. */
function LineMarks({
  series,
  w,
  h,
  max,
  pad,
  highlight,
  floor,
  filled,
}: {
  series: ChartSeries[];
  w: number;
  h: number;
  max: number;
  pad: number;
  highlight?: string;
  floor: number;
  filled: boolean;
}) {
  return (
    <>
      {series.map((s) => {
        const { paths, points } = linePath(s.values, w, h, floor, max, pad);
        const areas = filled ? areaPath(paths, w, h, pad) : [];
        const dimmed = highlight !== undefined && highlight !== s.name;
        return (
          <g key={s.name} data-series={s.name} opacity={dimmed ? 0.35 : 1}>
            {areas.map((d, i) => (
              <path
                key={`area-${i}`}
                data-area
                d={d}
                fill={s.color}
                fillOpacity={0.15}
                stroke="none"
              />
            ))}
            {paths.map((d, i) => (
              <path
                key={`line-${i}`}
                data-line
                d={d}
                fill="none"
                stroke={s.color}
                strokeWidth={1.5}
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            ))}
            {points.map((p, i) => (
              <path
                key={`point-${i}`}
                data-line
                data-point
                d={dotPath(p.x, p.y)}
                fill={s.color}
                stroke="none"
              />
            ))}
          </g>
        );
      })}
    </>
  );
}

/**
 * The rule and dots marking the hovered bucket.
 *
 * A null bucket gets the rule but no dot: the rule says WHERE, the dot says
 * HOW MUCH, and at a hole there is no value to mark. Drawing one at zero
 * would state a reading the host never reported.
 */
function Crosshair({
  rect,
  index,
  count,
  series,
  max,
  min,
  pad,
  mirrored,
}: {
  rect: PlotRect;
  index: number;
  count: number;
  series: ChartSeries[];
  max: number;
  min: number;
  pad: number;
  mirrored: boolean;
}) {
  if (count <= 0) return null;
  const fraction = count <= 1 ? 0 : index / (count - 1);
  const cx = xAt(rect, fraction);
  const h = plotHeight(rect);

  return (
    <g data-crosshair>
      <line
        data-cursor
        x1={cx}
        x2={cx}
        y1={rect.top}
        y2={rect.bottom}
        stroke="var(--accent)"
        strokeWidth={1}
        strokeDasharray="3 2"
        opacity={0.75}
      />
      {series.map((s, i) => {
        const v = s.values[index];
        if (v === null || v === undefined) return null;
        const t = max === min ? 0.5 : (v - min) / (max - min);
        const cy = mirrored
          ? rect.top +
            h / 2 +
            (i % 2 === 0 ? -1 : 1) * (max === 0 ? 0 : v / max) * (h / 2 - pad)
          : yAt(rect, t);
        return (
          <circle
            key={s.name}
            data-cursor-dot
            cx={cx}
            cy={cy}
            r={3.5}
            fill={s.color}
            stroke="var(--surface)"
            strokeWidth={1.5}
          />
        );
      })}
    </g>
  );
}
