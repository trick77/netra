// Multiple line series drawn on one shared axis (e.g. several hosts' CPU
// busy on the same chart).
//
// A thin wrapper over Chart now, carrying its own prop names and its legend.
// It keeps its identity because the callers speak in its terms -- `stacked`
// and `mirrored` rather than a mark -- and because the legend is HTML beside
// the SVG rather than part of it. Everything inside the image is Chart's.
//
// The whole point of "overlay" is comparability,
// so every series is scaled against ONE shared extent -- computing extent()
// per series independently would normalise a 2% series and a 200% series
// to look identical, which is exactly the comparison this component exists
// to make possible.
import { Chart } from "./Chart";
import type { Tick, TimeTick } from "./ticks";

export interface OverlaySeries {
  name: string;
  /** CSS variable string, e.g. "var(--s1)". Never a hex literal -- colour
   * identity for a series is the caller's decision, and with 2+ series a
   * legend is required below since colour alone can't carry identity. */
  color: string;
  values: (number | null)[];
  /**
   * The bucket's PEAK, drawn as a pale envelope beneath the line.
   *
   * Declared here rather than only on ChartSeries because bandsFor() has
   * always attached one and this type has always carried it -- as an
   * untyped spread, which type-checked by accident and meant no caller
   * could pass it deliberately. See ChartSeries.band in Chart.tsx.
   */
  band?: (number | null)[];
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
   * Ignored by the stack and mirror marks, which are filled by construction.
   */
  filled?: boolean;

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
   * nearly full", because nothing says what the top of the box means.
   *
   * The rule is drawn unlabelled. What it is belongs in the panel header,
   * beside the reading it is the ceiling for -- "used 20.4 · 31 GiB" -- not
   * floating over the plot: this is the only chart in the app that ever drew
   * text inside its own box, and a magnitude reads as a magnitude in text. */
  reference?: number;
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

  /**
   * Axis furniture, forwarded verbatim to Chart.
   *
   * Overlay does not compute any of it. Ticks depend on the formatter and
   * the answered window, which are the PANEL's knowledge -- Overlay is handed
   * series and a ceiling and knows nothing about time or units. Forwarding
   * rather than deriving is what keeps a fleet-row overlay, which passes
   * none of these, drawing exactly what it drew before.
   */
  y?: readonly Tick[];
  x?: readonly TimeTick[];
  format?: (value: number) => string;
  grid?: boolean;
  spine?: boolean;
  labels?: boolean;
  widestYLabel?: string;
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
  filled = false,
  stacked = false,
  legend = true,
  reference,
  mirrored = false,
  y,
  x,
  format,
  grid,
  spine,
  labels,
  widestYLabel,
}: OverlayProps) {
  return (
    <>
      <Chart
        series={series}
        width={width}
        height={height}
        max={max}
        min={min}
        pad={pad}
        mark={
          mirrored
            ? stacked
              ? "mirrorStack"
              : "mirror"
            : stacked
              ? "stack"
              : filled
                ? "area"
                : "line"
        }
        highlight={highlight}
        reference={reference}
        label={label}
        y={y}
        x={x}
        format={format}
        grid={grid}
        spine={spine}
        labels={labels}
        widestYLabel={widestYLabel}
      />
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
