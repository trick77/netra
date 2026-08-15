// Multiple line series drawn on one shared axis (e.g. several hosts' CPU
// busy on the same chart). The whole point of "overlay" is comparability,
// so every series is scaled against ONE shared extent -- computing extent()
// per series independently would normalise a 2% series and a 200% series
// to look identical, which is exactly the comparison this component exists
// to make possible.
import { dotPath, extent, linePath, mirrorPaths, stackBands } from "./geometry";
import {
  AXIS_STROKE,
  AXIS_WIDTH,
  MIRROR_FILL_OPACITY,
  MIRROR_STROKE_WIDTH,
  REFERENCE_DASH,
  REFERENCE_STROKE,
  REFERENCE_WIDTH,
} from "./size";

export interface OverlaySeries {
  name: string;
  /** CSS variable string, e.g. "var(--s1)". Never a hex literal -- colour
   * identity for a series is the caller's decision, and with 2+ series a
   * legend is required below since colour alone can't carry identity. */
  color: string;
  values: (number | null)[];
}

export interface OverlayProps {
  series: OverlaySeries[];
  max: number;
  /** Axis floor. Optional, and it exists because `max` alone is half an
   * axis: a caller declaring a ceiling of 100 for a panel whose data sits
   * at 88-92 got a derived floor of 88, so a four-point swing filled a
   * third of the box and two panels sharing `max` still could not be
   * compared. Omitted, the floor is still derived from the data, which is
   * right for a free-scaled chart. */
  min?: number;
  width?: number;
  height?: number;
  pad?: number;
  /** Name of a series to emphasise; other series are dimmed, not hidden --
   * dimming keeps every series' shape visible instead of removing data. */
  highlight?: string;
  label?: string;
  /**
   * Draw the series as a cumulative stack rather than as independent lines.
   *
   * The maths is stackBands() from geometry.ts, the same function the fleet
   * columns' StackedSparkline uses -- it is size-independent, so a panel and
   * its enlarged view draw the identical mark rather than two components
   * that have to be kept agreeing by hand. That is the property ChartDetail
   * exists to preserve.
   *
   * A stack needs a real ceiling, so `max` is used verbatim and never
   * widened to the data's own extent: a stack scaled to its own running
   * total always touches the top, which is the always-full reading the fleet
   * columns already carry their own ceilings to avoid.
   */
  stacked?: boolean;
  /**
   * Whether to name every series underneath the chart.
   *
   * On by default -- with two or more series colour alone cannot carry
   * identity. Off for the per-core stack, where a 32-entry legend is taller
   * than the chart and crushes it. Suppressing it via `highlight` was the
   * first attempt and was worse than the legend: highlight DIMS every other
   * series to 35%, so the whole stack went pale to hide a list.
   */
  legend?: boolean;
  /** A value to mark with a dashed rule -- for memory, the host's total RAM.
   * Without it a stack scaled to a ceiling cannot answer "is this host
   * nearly full", because nothing says what the top of the box means. */
  reference?: number;
  /** What the reference rule is. Drawn at the line, because a dashed rule
   * with no name is a mystery: a reader has to guess whether it is a
   * ceiling, a threshold or a mean. */
  referenceLabel?: string;
  /**
   * Draw the series as mirrored pairs about a midline -- ingress above,
   * egress below -- rather than as independent lines.
   *
   * Series arrive in pairs: (0,1) is one interface's in and out, (2,3) the
   * next. Traffic has a direction, and a chart that draws both directions as
   * two lines climbing the same axis makes a reader compare shapes to answer
   * "which way is this going", which the midline answers at a glance. The
   * fleet row has always drawn it this way; this is how everything else
   * catches up.
   */
  mirrored?: boolean;
}

export function Overlay({
  series,
  max,
  min,
  width = 260,
  height = 64,
  pad = 2,
  highlight,
  label = "overlaid metrics chart",
  stacked = false,
  legend = true,
  reference,
  referenceLabel,
  mirrored = false,
}: OverlayProps) {
  const { min: autoMin } = extent(series.flatMap((s) => s.values));
  const effectiveMin = min ?? autoMin;
  // stackBands breaks every band at any index where ANY series is null: a
  // running total is undefined there, not just the one series' value.
  const referenceY =
    reference === undefined || max <= effectiveMin
      ? null
      : height -
        pad -
        ((reference - effectiveMin) / (max - effectiveMin)) *
          (height - 2 * pad);

  const bands = stacked
    ? stackBands(
        series.map((s) => s.values),
        width,
        height,
        max,
        pad,
      )
    : [];

  return (
    <>
      <svg
        className="spark"
        viewBox={`0 0 ${width} ${height}`}
        width={width}
        height={height}
        role="img"
        aria-label={label}
      >
        {referenceY !== null && (
          <>
            <line
              data-reference
              x1={0}
              x2={width}
              y1={referenceY}
              y2={referenceY}
              stroke={REFERENCE_STROKE}
              strokeWidth={REFERENCE_WIDTH}
              strokeDasharray={REFERENCE_DASH}
            />
            {referenceLabel !== undefined && (
              <text
                data-reference-label
                className="ref-label"
                x={width - 4}
                y={referenceY - 5}
                textAnchor="end"
              >
                {referenceLabel}
              </text>
            )}
          </>
        )}
        {mirrored &&
          Array.from({ length: Math.ceil(series.length / 2) }, (_, p) => {
            const up = series[p * 2];
            const down = series[p * 2 + 1];
            if (up === undefined) return null;
            const paths = mirrorPaths(
              up.values,
              down?.values ?? [],
              width,
              height,
              max,
              pad,
            );
            return (
              <g key={up.name} data-series={up.name} data-mirror>
                {/* Weights from size.ts, shared with UpDownSparkline: the
                    fleet row's traffic cell draws this same rx/tx pair and
                    the two are read on the same screen. */}
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
                  x2={width}
                  y1={paths.mid}
                  y2={paths.mid}
                  stroke={AXIS_STROKE}
                  strokeWidth={AXIS_WIDTH}
                />
              </g>
            );
          })}
        {!mirrored &&
          stacked &&
          series.map((s, i) => {
            const dimmed = highlight !== undefined && highlight !== s.name;
            return (
              <path
                key={s.name}
                data-series={s.name}
                data-band
                d={bands[i] ?? ""}
                fill={s.color}
                // Dimmed fill with a solid edge, matching StackedSparkline so
                // a fleet row and the host page draw the same mark. The edge
                // is what keeps a many-band stack legible: a band's floor is
                // the band below it, so stroking each polygon draws every
                // separator in the stack.
                fillOpacity={0.55}
                stroke={s.color}
                strokeWidth={1.25}
                strokeLinejoin="round"
                opacity={dimmed ? 0.35 : 1}
              />
            );
          })}
        {!mirrored &&
          !stacked &&
          series.map((s) => {
            const { paths, points } = linePath(
              s.values,
              width,
              height,
              effectiveMin,
              max,
              pad,
            );
            const dimmed = highlight !== undefined && highlight !== s.name;
            return (
              <g key={s.name} data-series={s.name} opacity={dimmed ? 0.35 : 1}>
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
      </svg>
      {/* A legend names series; on a fleet overlay it would name all
          nineteen of them, which is the opposite of "every host
          de-emphasised, only the outlier labelled". `highlight` is exactly
          the caller saying one series carries the identity, so it also says
          the legend has no work to do. */}
      {legend && series.length >= 2 && highlight === undefined && (
        <div className="legend">
          {series.map((s) => (
            <span key={s.name}>
              <i style={{ background: s.color }} />
              {s.name}
            </span>
          ))}
        </div>
      )}
    </>
  );
}
