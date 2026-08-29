// The one chart renderer. Every non-sparkline chart in the app is this
// component with different props, and every sparkline is this component with
// all of the furniture switched off.
//
// The distinction that matters here is that SIZE and FURNITURE are separate
// knobs, not one preset. A fleet cell is 150x45 with nothing on it; a panel
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
  mirrorCeilings,
  mirrorPaths,
  mirrorPeaks,
  mirrorStackBands,
  stackBands,
} from "./geometry";
import {
  areaFillOpacity,
  AXIS_STROKE,
  AXIS_WIDTH,
  MIRROR_FILL_OPACITY,
  BAND_STROKE_WIDTH,
  BAND_Y_PAD,
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
  bandStroke = BAND_STROKE_WIDTH,
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

  const mirrorStacked = mark === "mirrorStack";
  const stacked = mark === "stack";

  /* The vertical inset a STACK spends, against `pad` for everything else.
   *
   * A stacked band is a filled region with a BAND_STROKE_WIDTH edge, so half
   * a stroke is the only headroom it can use. `pad` is two pixels because
   * that is what a LINE's stroke needs, and on a 45px fleet cell spending it
   * at both ends costs an eighth of the chart -- see stackBands' `yPad`.
   *
   * Everything that has to agree with the bands reads this: the marks, the
   * furniture below, and the reference rule, which on the memory cell is the
   * host's total RAM and has to land on the band edge that reaches it. */
  const markYPad = stacked ? BAND_Y_PAD : pad;

  const referenceY =
    reference === undefined || max <= floor
      ? null
      : h -
        markYPad -
        ((reference - floor) / (max - floor)) * (h - 2 * markYPad);

  // Both mirror marks share every piece of furniture that hangs off a
  // midline -- the zero rule, the crosshair's placement, the axis half-range
  // -- so `mirrored` stays the question "is there a midline", and the mark
  // dispatch below asks the narrower one.
  const mirrored = mark === "mirror" || mirrorStacked;
  /* The ceilings the mirrored marks are drawn against, and where zero falls
     between them as a fraction measured from the TOP -- the same numbers
     mirrorPaths and mirrorStackBands derive, so the axis furniture, the
     crosshair and the marks cannot disagree about where the line is. */
  const mirrorScale = mirrored
    ? mirrorCeilings(
        ...(({ up, down }) => [up, down] as const)(
          mirrorPeaks(
            series.map((s) =>
              s.band && s.band.length > 0 ? s.band : s.values,
            ),
            mirrorStacked,
          ),
        ),
      )
    : { up: 0, down: 0, zero: 0.5 };
  const mirrorZero = mirrorScale.zero;

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

  /* The rect the FURNITURE hangs on.
   *
   * axisRect for every other mark, because scaleY insets a mark by `pad` and
   * furniture mapped across the full rect names heights the series was never
   * drawn at. A mirrored mark is the exception: it spends the whole height
   * deliberately -- a bar has no stroke to clip at the box edge, so
   * mirrorPaths and mirrorStackBands measure their halves over `h` rather
   * than `h - 2 * pad`. Mapped through the inset rect the whole ladder was
   * compressed against marks that were not: the "0" row sat over a pixel off
   * the midline it names, and the top label about `pad` below the peak it
   * names.
   *
   * Vertically only. mirrorStackBands still places its columns through
   * scaleX, which IS inset by `pad`, so the time axis stays where it was. */
  const furnitureRect = mirrored
    ? { ...axisRect, top: rect.top, bottom: rect.bottom }
    : stacked
      ? {
          ...axisRect,
          top: rect.top + markYPad,
          bottom: rect.bottom - markYPad,
        }
      : axisRect;

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
      {grid && <Grid rect={furnitureRect} y={y} x={x} />}

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
        {mirrorStacked && (
          <MirrorStackMarks {...{ series, w, h, max, pad, highlight }} />
        )}
        {mirrored && !mirrorStacked && (
          <MirrorMarks {...{ series, w, h, max, pad }} />
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

        {/* Over the marks, not under them.
            A threshold is not furniture: the grid says where the values are,
            this says which value MATTERS -- a memory limit, a disk ceiling --
            and it is only worth drawing where the series has reached it,
            which is exactly where an opaque band would bury it. It stays
            inside the clip so it cannot paint over the axis labels, and it
            keeps its dashes so it never reads as data. */}
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
      </MarkGroup>

      {spine && <Spine rect={furnitureRect} y={y} x={x} />}
      {/* Only where there is furniture to outrank. A sparkline already draws
          its midline inside MirrorMarks at AXIS_STROKE, and size.ts is
          explicit that a sparkline's midline does not change; drawing this
          over it in the heavier ZERO_STROKE darkened every fleet traffic
          cell, the Overview traffic card and every range thumbnail. The
          sparkline's own test did not catch it because it asserts on
          [data-mid], which is the line underneath. */}
      {/* At the height the DATA puts zero, not at 0.5.
          
          It was hard-coded to the midline, which was true only while both
          halves shared one ceiling. They no longer do -- a host that pulls
          four times what it pushes puts zero four fifths of the way down --
          and a rule pinned to 0.5 then ran across the middle of the box, over
          the marks, straight through the spikes. Same fraction the marks
          measure from, so it lands on the edge of the band rather than
          through it.

          Through furnitureRect, which on a mirrored chart is the mark's own
          height -- see its definition. On the inset rect this rule landed
          `pad * (2 * mirrorZero - 1)` off the midline it names, 1.5 px on a
          lopsided 112 px panel: a visible second line beside the mark's
          own. */}
      {mirrored && (spine || grid || labels) && (
        <ZeroRule rect={furnitureRect} at={1 - mirrorZero} />
      )}
      {labels && (
        <AxisLabels rect={furnitureRect} y={y} x={x} format={format} />
      )}

      {cursor !== null && (
        <Crosshair
          rect={axisRect}
          /* The mark rect, for the mirrored branch only: a mirror places its
             zero line and measures its bars over the full height, so a dot
             positioned in the inset rect floats off the band it marks. */
          plot={rect}
          mirror={mirrorScale}
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

/**
 * The two ceilings a mirrored PAIR is drawn against -- the peak envelope
 * where the tier carries one, the mean otherwise.
 *
 * The same rule mirrorPeaks() applies to a whole chart, for the one-pair
 * case. Exists so the marks, the midline and the tick ladder derive their
 * scale from one place; they disagreed about where zero was when they did
 * not.
 */
function mirrorHalves(
  up: ChartSeries,
  down: ChartSeries | undefined,
): { up: number; down: number } {
  const peak = (s: ChartSeries | undefined): number => {
    if (s === undefined) return 0;
    const vals = s.band && s.band.length > 0 ? s.band : s.values;
    let m = 0;
    for (const v of vals) if (v !== null && v > m) m = v;
    return m;
  };
  return { up: peak(up), down: peak(down) };
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
  /** Let each half use its own ceiling and the whole half-height. See
   * mirrorPaths -- true only where no tick ladder claims a shared scale. */
}) {
  return (
    <>
      {Array.from({ length: Math.ceil(series.length / 2) }, (_, p) => {
        const up = series[p * 2];
        const down = series[p * 2 + 1];
        if (up === undefined) return null;
        // Whether this chart has room for an edge at all. Derived from the
        // plot, not from the call site: the same component draws a 150px
        // fleet cell at more than one point per pixel and a 1000px dialog at
        // three and a half, and only one of those can carry a 1.25px outline without
        // the outline becoming the mark. See mirrorEdge().
        const edge = mirrorEdge(w, longest([up, ...(down ? [down] : [])]), pad);
        // Both calls on ONE pair of ceilings, derived from the peak where
        // there is one. The envelope and the mean inside it are handed
        // different values, so left to derive their own they would place
        // their zero lines at different heights and the envelope would
        // neither contain the mean nor share its midline.
        //
        // This used to be a guard that simply turned the derived scale off
        // for a banded pair, on the reasoning that such a chart always
        // carries a tick ladder and the ladder turned it off anyway. The
        // ladder no longer does -- it is built from these same ceilings now
        // -- so the guard would have left the marks centred while the axis
        // furniture sat where the data puts zero, and ruled a line through
        // the chart.
        const ceiling = mirrorHalves(up, down);
        const paths = mirrorPaths(
          up.values,
          down?.values ?? [],
          w,
          h,
          max,
          pad,
          ceiling,
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
                ceiling,
              )
            : null;
        return (
          <g key={up.name} data-series={up.name} data-mirror>
            {/* BEHIND the series, and half a row down from `mid`.

                Behind, because it is the axis the data is measured from and
                not an annotation over it: drawn last it ran a grey rule
                straight through the band, striking it out. rrdtool draws its
                grid first and fills the AREA on top for the same reason.

                Half a row down, because a 1px stroke is centred on its
                coordinate: at an integer y it covers half the row above and
                half the row below, rendering as a two-row smear that the
                marks stand off rather than on. Offset, it fills exactly the
                row the down half begins in. */}
            <line
              data-mid
              x1={0}
              x2={w}
              y1={paths.mid + AXIS_WIDTH / 2}
              y2={paths.mid + AXIS_WIDTH / 2}
              stroke={AXIS_STROKE}
              strokeWidth={AXIS_WIDTH}
              shapeRendering="crispEdges"
            />
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
                strokeLinejoin="round"
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
                strokeLinejoin="round"
              />
            )}
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
  // The same weights the plain mirror uses, at this chart's own density.
  // This half used to hard-code the roomy pair whatever the density, so a
  // mirrored STACK folded to one point per pixel drew an outline where the
  // plain mirror had already learned not to.
  const edge = mirrorEdge(w, longest(series), pad);
  const paths = mirrorStackBands(
    ups.map((s) => s.values),
    downs.map((s) => s.values),
    w,
    h,
    pad,
  );
  const dim = (s: ChartSeries): number =>
    highlight !== undefined && highlight !== s.name ? 0.35 : 1;
  return (
    <>
      {/* Behind the bands, and half a row down -- see MirrorMarks for both
          reasons. Drawn last it struck a grey rule through the band. */}
      <line
        data-mid
        x1={0}
        x2={w}
        y1={paths.mid + AXIS_WIDTH / 2}
        y2={paths.mid + AXIS_WIDTH / 2}
        stroke={AXIS_STROKE}
        strokeWidth={AXIS_WIDTH}
        shapeRendering="crispEdges"
      />
      {ups.map((s, i) =>
        (paths.up[i] ?? "") === "" ? null : (
          <path
            key={s.name}
            data-series={s.name}
            data-band
            data-up
            d={paths.up[i]!}
            fill={s.color}
            fillOpacity={edge.fillOpacity}
            stroke={edge.strokeWidth === 0 ? "none" : s.color}
            strokeWidth={edge.strokeWidth}
            strokeLinejoin="round"
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
            fillOpacity={edge.fillOpacity}
            stroke={edge.strokeWidth === 0 ? "none" : s.color}
            strokeWidth={edge.strokeWidth}
            strokeLinejoin="round"
            opacity={dim(s)}
          />
        ),
      )}
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
            // Opaque. A band used to be drawn at 0.55 "so it stays readable
            // through the one above it", which is not a thing that happens:
            // stackBands emits band k as the ribbon between running total
            // k-1 and running total k, so the bands are disjoint and there
            // is nothing behind any of them except the grid. All the
            // translucency bought was the dotted gridline showing THROUGH
            // the data -- and where a gridline crossed a band's own edge
            // stroke, that edge read as broken. A grid is furniture; it
            // belongs behind what it measures, which means being hidden by
            // it.
            fillOpacity={1}
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
  // One weight for the whole panel, read off the series count rather than
  // per series: every fill on this chart shares the baseline, so what makes
  // an overlap dark is how many of them there are.
  const fillOpacity = areaFillOpacity(series.length);
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
                fillOpacity={fillOpacity}
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
  plot,
  mirror,
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
  /** The MARK rect -- see the mirrored branch of `cy`. */
  plot: PlotRect;
  /** The ceilings the mirrored marks were drawn against, from
   * mirrorCeilings(). Ignored by every other mark. */
  mirror: { up: number; down: number; zero: number };
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

  /* Where a plain STACK's bands actually are, vertically.
   *
   * Same reason the mirrored branch reads `plot`: the bands are inset by
   * BAND_Y_PAD (half their own edge) rather than by `pad`, so a dot placed
   * in the axis rect sits `pad - BAND_Y_PAD` off the band it names -- at the
   * ceiling and at the baseline both, and only at mid-scale not at all.
   * Horizontally unchanged, so the rule and the dot stay on one column. */
  const stackRect = {
    ...rect,
    top: plot.top + BAND_Y_PAD,
    bottom: plot.bottom - BAND_Y_PAD,
  };

  /**
   * Where the mirrored marks put their zero line, in the MARK rect.
   *
   * The same arithmetic mirrorPaths' and mirrorStackBands' placeZero() do,
   * because a dot has to land on the band it names. It used to assume zero
   * at mid-height and both halves divided by `max`, which was true only
   * while a mirror was drawn on one shared ceiling: on a host pulling four
   * times what it pushes every dot sat a quarter of the box from its own
   * band.
   */
  const mirrorH = plotHeight(plot);
  const mirrorSpan = mirror.up + mirror.down;
  const mirrorMid = ((): number => {
    if (mirrorSpan === 0) return mirrorH / 2;
    let z = Math.round((mirrorH * mirror.up) / mirrorSpan);
    if (mirror.up > 0) z = Math.max(z, 1);
    if (mirror.down > 0) z = Math.min(z, mirrorH - 1);
    return z;
  })();

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
        const direction = i % 2 === 0 ? -1 : 1;
        // Clamped to the room the half actually has, exactly as the geometry
        // clamps its bars: a reading at the peak is a whole pixel of
        // rounding away from the edge of the box.
        const room = direction === -1 ? mirrorMid : mirrorH - mirrorMid;
        // The two mirrored geometries scale a half differently by a fraction
        // of a pixel -- mirrorPaths divides the combined span into the whole
        // box, mirrorStackBands divides one half's ceiling into the room
        // that half was given -- and the difference is the rounding of the
        // midline. Each dot follows the one that drew its own band.
        const halfCeiling = direction === -1 ? mirror.up : mirror.down;
        const reach = mirrorStacked
          ? halfCeiling === 0
            ? 0
            : (shown / halfCeiling) * room
          : mirrorSpan === 0
            ? 0
            : (shown / mirrorSpan) * mirrorH;
        const cy = mirrored
          ? plot.top + mirrorMid + direction * Math.min(reach, room)
          : yAt(stacked ? stackRect : rect, t);
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
