// Multiple line series drawn on one shared axis (e.g. several hosts' CPU
// busy on the same chart). The whole point of "overlay" is comparability,
// so every series is scaled against ONE shared extent -- computing extent()
// per series independently would normalise a 2% series and a 200% series
// to look identical, which is exactly the comparison this component exists
// to make possible.
import { dotPath, extent, linePath } from "./geometry";

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
}: OverlayProps) {
  const { min: autoMin } = extent(series.flatMap((s) => s.values));
  const effectiveMin = min ?? autoMin;

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
      {series.length >= 2 && highlight === undefined && (
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
