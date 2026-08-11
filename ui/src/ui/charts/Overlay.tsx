// Multiple line series drawn on one shared axis (e.g. several hosts' CPU
// busy on the same chart). The whole point of "overlay" is comparability,
// so every series is scaled against ONE shared extent -- computing extent()
// per series independently would normalise a 2% series and a 200% series
// to look identical, which is exactly the comparison this component exists
// to make possible.
import { extent, linePath } from "./geometry";

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
  width?: number;
  height?: number;
  pad?: number;
  /** Name of a series to emphasise; other series are dimmed, not hidden --
   * dimming keeps every series' shape visible instead of removing data. */
  highlight?: string;
  label?: string;
}

// See Sparkline.tsx's dotPath() for why an isolated point is drawn as a
// two-arc circle path tagged data-line/data-point rather than a <circle>.
// Duplicated here (not extracted to a shared helper) because this task's
// file ownership is exactly the five chart components plus their tests --
// no new shared module is in scope.
function dotPath(x: number, y: number, r = 1.5): string {
  return `M${x - r},${y} A${r},${r} 0 1,0 ${x + r},${y} A${r},${r} 0 1,0 ${x - r},${y} Z`;
}

export function Overlay({
  series,
  max,
  width = 260,
  height = 64,
  pad = 2,
  highlight,
  label = "overlaid metrics chart",
}: OverlayProps) {
  const { min } = extent(series.flatMap((s) => s.values));

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
        {series.map((s) => {
          const { paths, points } = linePath(
            s.values,
            width,
            height,
            min,
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
      {series.length >= 2 && (
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
