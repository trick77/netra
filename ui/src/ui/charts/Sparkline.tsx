// A single-series trend line. All the maths (where a gap breaks the line,
// where the fill closes, where the axis floor/ceiling sits) comes from
// geometry.ts verbatim -- this component only turns that output into SVG
// markup and never recomputes or "fixes up" a coordinate itself.
import { areaPath, dotPath, extent, linePath } from "./geometry";

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
}

export function Sparkline({
  values,
  width = 120,
  height = 32,
  color = "var(--s1)",
  label = "trend sparkline",
  pad = 2,
}: SparklineProps) {
  const { min, max } = extent(values);
  const { paths, points } = linePath(values, width, height, min, max, pad);
  const areas = areaPath(paths, width, height, pad).filter((d) => d !== "");

  return (
    <svg
      className="spark"
      viewBox={`0 0 ${width} ${height}`}
      width={width}
      height={height}
      role="img"
      aria-label={label}
    >
      {areas.map((d, i) => (
        <path
          key={`area-${i}`}
          data-area
          d={d}
          fill={color}
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
          stroke={color}
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
          fill={color}
          stroke="none"
        />
      ))}
    </svg>
  );
}
