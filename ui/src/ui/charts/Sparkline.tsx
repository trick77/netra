// A single-series trend line. All the maths (where a gap breaks the line,
// where the fill closes, where the axis floor/ceiling sits) comes from
// geometry.ts verbatim -- this component only turns that output into SVG
// markup and never recomputes or "fixes up" a coordinate itself.
import { extent } from "./geometry";
import { Chart } from "./Chart";
import { SPARK_WIDTH } from "./size";

export interface SparklineProps {
  values: (number | null)[];
  width?: number;
  height?: number;
  /** CSS variable string, e.g. "var(--s1)". Never a hex literal -- colour
   * choice belongs to the caller, not this component. */
  color?: string;
  /** Accessible name for the chart; a chart is an image and needs one. */
  label?: string;
  pad?: number;
  /**
   * Axis floor and ceiling. Omitted, the line is scaled to its own extent,
   * which is right for a lone chart and wrong for a column of them: every
   * row would fill its box regardless of magnitude, so an idle container and
   * a saturated one would draw the identical silhouette. A caller rendering
   * a LIST passes a shared pair; the host list's CPU column does the same
   * thing with its fixed 100.
   */
  min?: number;
  max?: number;
  /**
   * Whether to fill the area under the line.
   *
   * On by default -- for a rate that rests at zero the filled mass IS the
   * reading. Off for a series that never goes near its floor: filesystem
   * usage lives between 40% and 95%, so an area anchored at zero floods the
   * cell and four hosts 40 points apart draw the same solid block. The
   * line's height is the information there.
   */
  fill?: boolean;
}

export function Sparkline({
  values,
  width = SPARK_WIDTH,
  height = 32,
  color = "var(--s1)",
  label = "trend sparkline",
  pad = 2,
  min,
  max,
  fill = true,
}: SparklineProps) {
  // Free-scaled by default: a sparkline with no declared extent is drawn
  // between its own, so a flat-but-not-zero series still has a shape. Chart
  // derives the floor the same way, but it takes an explicit ceiling, so the
  // data's own max is resolved here.
  const auto = extent(values);

  return (
    <Chart
      series={[{ name: label, color, values }]}
      width={width}
      height={height}
      min={min ?? auto.min}
      max={max ?? auto.max}
      pad={pad}
      mark={fill ? "area" : "line"}
      label={label}
    />
  );
}
