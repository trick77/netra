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
  mirrorStackBands,
  stackBands,
} from "./geometry";
import {
  AXIS_STROKE,
  AXIS_WIDTH,
  MIRROR_FILL_OPACITY,
  MIRROR_STROKE_WIDTH,
  mirrorEdge,
  REFERENCE_DASH,
  REFERENCE_STROKE,
  REFERENCE_WIDTH,
} from "./size";
import {
  contains,
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

export type ChartMark = "line" | "area" | "stack" | "mirror" | "mirrorStack";

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
  /**
   * Reports the bucket under the pointer, and null when it leaves.
   *
   * Given this, the chart listens for pointer movement itself. It has to:
   * the mapping from a screen position to a bucket runs through the PLOT
   * rect, and the rect is computed in here from margins the caller does not
   * know. A caller doing its own arithmetic would be reading the axis
   * margins as data -- brushing a value label would move the crosshair.
   */
  onCursorChange?: (index: number | null) => void;
  /**
   * Stroke width for a stacked band's edge.
   *
   * A prop rather than a constant because the two existing stacks disagree
   * and always have: Overlay draws 1.25, StackedSparkline draws 1. That is
   * pre-existing drift, not a decision -- but unifying it here would change
   * every fleet row's CPU and memory cell, and sparklines are not changing
   * in this work. Surfaced as a prop so the disagreement is visible in the
   * callers instead of hidden in two copies of the same component.
   */
  bandStroke?: number;
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
  onCursorChange,
  bandStroke = 1.25,
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

  const mirrorStacked = mark === "mirrorStack";
  // Both mirror marks share every piece of furniture that hangs off a
  // midline -- the zero rule, the crosshair's placement, the axis half-range
  // -- so `mirrored` stays the question "is there a midline", and the mark
  // dispatch below asks the narrower one.
  const mirrored = mark === "mirror" || mirrorStacked;
  const stacked = mark === "stack";

  // Where the DATA actually lives. geometry.ts insets every mark by `pad`
  // inside the box it is given (scaleX/scaleY map into [pad, w-pad]), so
  // furniture mapped across the full rect names heights the series was never
  // drawn at -- on a 112px panel the top gridline sat about two units above
  // its own value, and the crosshair rule never quite touched the point it
  // marked. The marks keep their coordinates; the axis moves onto them.
  const axisRect = {
    left: rect.left + pad,
    right: rect.right - pad,
    top: rect.top + pad,
    bottom: rect.bottom - pad,
  };

  // With no furniture the plot IS the image, and then the wrapper group and
  // the clip path are pure noise: nothing can overflow into margins that do
  // not exist. Omitting them keeps a sparkline's markup exactly what it was
  // before it moved onto this renderer, which is the guarantee that lets
  // every fleet cell migrate without being re-reviewed by eye.
  const inset =
    rect.left !== 0 ||
    rect.top !== 0 ||
    rect.right !== width ||
    rect.bottom !== height;

  const count = longest(series);

  // Screen position -> bucket, through the plot rect. Outside it -- over the
  // axis margins -- counts as not hovering, so brushing a label does not
  // leave a rule behind.
  const report = (e: React.PointerEvent<SVGSVGElement>) => {
    if (onCursorChange === undefined) return;
    const box = e.currentTarget.getBoundingClientRect();
    if (box.width === 0 || box.height === 0 || count <= 0) return;
    const vx = ((e.clientX - box.left) / box.width) * width;
    const vy = ((e.clientY - box.top) / box.height) * height;
    if (!contains(axisRect, vx, vy)) {
      if (cursor !== null) onCursorChange(null);
      return;
    }
    const span = plotWidth(axisRect);
    const fraction = span === 0 ? 0 : (vx - axisRect.left) / span;
    const next = Math.round(fraction * (count - 1));
    if (next !== cursor) onCursorChange(next);
  };

  return (
    <svg
      className="spark"
      viewBox={`0 0 ${width} ${height}`}
      width={width}
      height={height}
      role="img"
      aria-label={label}
      onPointerMove={onCursorChange ? report : undefined}
      onPointerLeave={
        onCursorChange
          ? () => cursor !== null && onCursorChange(null)
          : undefined
      }
    >
      {grid && <Grid rect={axisRect} y={y} x={x} />}

      {/* The series are clipped to the plot rect so an overflowing value
          cannot paint over the axis labels. This is NOT clamping, which
          mirrorPaths deliberately refuses to do: the value still visibly
          escapes the box, it just cannot reach the margins. */}
      {/* In the MARK GROUP's coordinates, not the image's. A userSpaceOnUse
          clip path is resolved in the user space established by the element
          that references it -- which includes that element's own transform --
          and MarkGroup translates by (rect.left, rect.top). Giving the rect
          image coordinates offset the clip window by the margins a second
          time: on a 260x112 panel the clip landed at x >= 92 while the plot
          starts at 48, so the oldest fifth of every axis-bearing chart was
          drawn and then hidden. */}
      {inset && (
        <clipPath id={clipId}>
          <rect x={0} y={0} width={w} height={h} />
        </clipPath>
      )}

      <MarkGroup inset={inset} clipId={clipId} rect={rect}>
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

        {mirrorStacked && (
          <MirrorStackMarks {...{ series, w, h, max, pad, highlight }} />
        )}
        {mirrored && !mirrorStacked && (
          // `independent` only where nothing on screen states a shared scale.
          // A chart given a tick ladder has its halves labelled off one
          // ceiling, and letting each half pick its own would put marks at
          // heights its own axis denies. A sparkline has no ladder, so it
          // takes RRDtool's scaling and the density that comes with it.
          <MirrorMarks
            {...{ series, w, h, max, pad }}
            independent={y === undefined}
          />
        )}
        {!mirrored && stacked && (
          <StackMarks {...{ series, w, h, max, pad, highlight, bandStroke }} />
        )}
        {!mirrored && !stacked && (
          <LineMarks
            {...{ series, w, h, max, pad, highlight, floor }}
            filled={mark === "area"}
          />
        )}
      </MarkGroup>

      {spine && <Spine rect={axisRect} y={y} x={x} />}
      {/* Only where there is furniture to outrank. A sparkline already draws
          its midline inside MirrorMarks at AXIS_STROKE, and size.ts is
          explicit that a sparkline's midline does not change; drawing this
          over it in the heavier ZERO_STROKE darkened every fleet traffic
          cell, the Overview traffic card and every range thumbnail. The
          sparkline's own test did not catch it because it asserts on
          [data-mid], which is the line underneath. */}
      {mirrored && (spine || grid || labels) && (
        <ZeroRule rect={axisRect} at={0.5} />
      )}
      {labels && <AxisLabels rect={axisRect} y={y} x={x} format={format} />}

      {cursor !== null && (
        <Crosshair
          rect={axisRect}
          index={cursor}
          count={count}
          series={series}
          max={max}
          min={floor}
          mirrored={mirrored}
          stacked={stacked}
          mirrorStacked={mirrorStacked}
        />
      )}
    </svg>
  );
}

/**
 * Wraps the marks only when there is something to wrap them for. An identity
 * translate is not free: it is one more node, and it reads in a diff as
 * though the mark had moved.
 */
function MarkGroup({
  inset,
  clipId,
  rect,
  children,
}: {
  inset: boolean;
  clipId: string;
  rect: PlotRect;
  children: React.ReactNode;
}) {
  if (!inset) return <>{children}</>;
  return (
    <g
      clipPath={`url(#${clipId})`}
      transform={`translate(${rect.left},${rect.top})`}
    >
      {children}
    </g>
  );
}

/**
 * The stack's height through series `upto` at `index`, or null at a hole.
 *
 * `step` is 2 for a mirrored stack, where a half is every OTHER series: band
 * 4 sits on top of bands 0 and 2 alone, and summing 0..4 would place its dot
 * above the outbound bytes it never stacked on.
 */
function runningTotal(
  series: readonly ChartSeries[],
  index: number,
  upto: number,
  step = 1,
): number | null {
  let sum = 0;
  for (let k = upto % step; k <= upto; k += step) {
    const v = series[k]?.values[index];
    if (v === null || v === undefined) return null;
    sum += v;
  }
  return sum;
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
  independent,
}: {
  series: ChartSeries[];
  w: number;
  h: number;
  max: number;
  pad: number;
  /** Let each half use its own ceiling and the whole half-height. See
   * mirrorPaths -- true only where no tick ladder claims a shared scale. */
  independent: boolean;
}) {
  return (
    <>
      {Array.from({ length: Math.ceil(series.length / 2) }, (_, p) => {
        const up = series[p * 2];
        const down = series[p * 2 + 1];
        if (up === undefined) return null;
        // Whether this chart has room for an edge at all. Derived from the
        // plot, not from the call site: the same component draws a 170px
        // fleet cell at one point per pixel and a 1000px dialog at three and
        // a half, and only one of those can carry a 1.25px outline without
        // the outline becoming the mark. See mirrorEdge().
        const edge = mirrorEdge(w, longest([up, ...(down ? [down] : [])]), pad);
        const paths = mirrorPaths(
          up.values,
          down?.values ?? [],
          w,
          h,
          max,
          pad,
          independent,
        );
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
                independent,
              )
            : null;
        return (
          <g key={up.name} data-series={up.name} data-mirror>
            {bands && (
              <>
                {bands.up !== "" && (
                  <path
                    data-band-up
                    d={bands.up}
                    fill={up.color}
                    fillOpacity={0.18}
                    stroke="none"
                  />
                )}
                {down !== undefined && bands.down !== "" && (
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
            {paths.up !== "" && (
              <path
                data-up
                d={paths.up}
                fill={up.color}
                fillOpacity={edge.fillOpacity}
                stroke={edge.strokeWidth === 0 ? "none" : up.color}
                strokeWidth={edge.strokeWidth}
              />
            )}
            {down !== undefined && paths.down !== "" && (
              <path
                data-down
                d={paths.down}
                fill={down.color}
                fillOpacity={edge.fillOpacity}
                stroke={edge.strokeWidth === 0 ? "none" : down.color}
                strokeWidth={edge.strokeWidth}
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

/**
 * Cumulative bands about a midline: the even series stack upward, the odd
 * ones downward, so the envelope of each half is the total in that direction.
 *
 * The even-up / odd-down convention is MirrorMarks', unchanged -- the bands
 * arrive as consecutive in/out pairs, one pair per interface, and layer order
 * is pair order so the same interface sits at the same depth on both sides.
 *
 * No peak envelope here, unlike MirrorMarks. A stacked band's height is a
 * running total, and the sum of each interface's peak is not the host's peak:
 * the interfaces do not peak in the same bucket. Drawing one would state a
 * number no bucket ever held.
 */
function MirrorStackMarks({
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
  const ups = series.filter((_, i) => i % 2 === 0);
  const downs = series.filter((_, i) => i % 2 === 1);
  const paths = mirrorStackBands(
    ups.map((s) => s.values),
    downs.map((s) => s.values),
    w,
    h,
    max,
    pad,
  );
  const dim = (s: ChartSeries): number =>
    highlight !== undefined && highlight !== s.name ? 0.35 : 1;
  return (
    <>
      {ups.map((s, i) =>
        (paths.up[i] ?? "") === "" ? null : (
          <path
            key={s.name}
            data-series={s.name}
            data-band
            data-up
            d={paths.up[i]!}
            fill={s.color}
            fillOpacity={MIRROR_FILL_OPACITY}
            stroke={s.color}
            strokeWidth={MIRROR_STROKE_WIDTH}
            opacity={dim(s)}
          />
        ),
      )}
      {downs.map((s, i) =>
        (paths.down[i] ?? "") === "" ? null : (
          <path
            key={s.name}
            data-series={s.name}
            data-band
            data-down
            d={paths.down[i]!}
            fill={s.color}
            fillOpacity={MIRROR_FILL_OPACITY}
            stroke={s.color}
            strokeWidth={MIRROR_STROKE_WIDTH}
            opacity={dim(s)}
          />
        ),
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
  bandStroke,
}: {
  series: ChartSeries[];
  w: number;
  h: number;
  max: number;
  pad: number;
  highlight?: string;
  bandStroke: number;
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
      {series.map((s, i) =>
        (bands[i] ?? "") === "" ? null : (
          <path
            key={s.name}
            data-series={s.name}
            data-band
            d={bands[i]!}
            fill={s.color}
            fillOpacity={0.55}
            stroke={s.color}
            strokeWidth={bandStroke}
            strokeLinejoin="round"
            opacity={highlight !== undefined && highlight !== s.name ? 0.35 : 1}
          />
        ),
      )}
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
  mirrored,
  stacked,
  mirrorStacked,
}: {
  rect: PlotRect;
  index: number;
  count: number;
  series: ChartSeries[];
  max: number;
  min: number;
  mirrored: boolean;
  stacked: boolean;
  mirrorStacked: boolean;
}) {
  if (count <= 0) return null;
  const fraction = count <= 1 ? 0 : index / (count - 1);
  const cx = xAt(rect, fraction);
  const h = plotHeight(rect);

  /**
   * Whether the stack band `i` belongs to is broken at this index.
   *
   * The same test the geometry makes, and it has to be: a mirrored stack's
   * halves are every other series, so a hole in the up half leaves the down
   * half whole. A non-stacked mark has no stack to break.
   */
  const stackBroken = (i: number): boolean => {
    if (!stacked && !mirrorStacked) return false;
    const step = mirrorStacked ? 2 : 1;
    for (let k = i % step; k < series.length; k += step) {
      const v = series[k]?.values[index];
      if (v === null || v === undefined) return true;
    }
    return false;
  };

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
        // A stack draws band k at the RUNNING TOTAL through k, so a dot at
        // the raw value points at whatever band happens to sit at that
        // height -- on a per-core stack every dot bunched near the baseline
        // while its own band was somewhere above.
        // A mirrored stack has the same problem on each half, one series in
        // two: band 4 is drawn on top of bands 0 and 2, never on top of the
        // outbound bands between them.
        const stackedTo = mirrorStacked
          ? runningTotal(series, index, i, 2)
          : stacked
            ? runningTotal(series, index, i)
            : null;
        // Both stack geometries break EVERY band of a stack at an index
        // where any of that stack's series is null -- the running total is
        // undefined there for the layers below the hole as much as for the
        // ones above it. So the whole stack loses its dots, not just the
        // layers over the gap: a dot at a raw value there would float over a
        // hole, stating a height nothing was drawn at. The rule the
        // docstring gives for a null bucket applies to the stack a band
        // belongs to, not only to the band itself.
        if (stackBroken(i)) return null;
        const shown = stackedTo ?? v;
        const t = max === min ? 0.5 : (shown - min) / (max - min);
        const cy = mirrored
          ? rect.top +
            h / 2 +
            ((i % 2 === 0 ? -1 : 1) * (max === 0 ? 0 : shown / max) * h) / 2
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
