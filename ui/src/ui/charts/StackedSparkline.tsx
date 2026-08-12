// A cumulative stacked-area chart. geometry.ts's stackBands() already
// breaks every band at any index where ANY series is null (a running total
// is undefined for every band there, not just the null one) -- this
// component's only job is to hand it the right `max` and turn its output
// into SVG paths.
import { stackBands } from "./geometry";

export interface Band {
  name: string;
  /** CSS variable string, e.g. "var(--s2)". Never a hex literal. */
  color: string;
  values: (number | null)[];
}

export interface StackedSparklineProps {
  bands: Band[];
  /** Largest running total to scale against. Auto-computed from `bands`
   * when omitted -- see maxRunningTotal() below. */
  max?: number;
  width?: number;
  height?: number;
  pad?: number;
  label?: string;
  /**
   * Whether to name every band underneath the chart.
   *
   * On by default, because with two or more bands colour alone cannot carry
   * identity. Off for the per-core CPU stack: a 32-thread host produced a
   * 32-entry legend that was five times taller than the chart it explained
   * and pushed every other column of the fleet row off the screen. Thirty-two
   * cores cannot each own a hue anyway, so there is no identity for a legend
   * to carry -- the shape is the whole message.
   */
  legend?: boolean;
}

// stackBands() needs the largest RUNNING TOTAL across bands (sum over all
// series at an index), not the largest single value in any one series.
// extent() on the flattened values would answer the wrong question here --
// e.g. two bands of [1,2,3,4] and [2,3,null,1] have a max single value of
// 4, but the tallest actual stack is 5 (1+2 at i=1... no, at i=1: 2+3=5).
// Using 4 would let the real (5-tall) stack overflow the plot box.
// Indices where any band is null are skipped, matching stackBands' own gap
// rule, since a running total is undefined there.
function maxRunningTotal(bands: Band[]): number {
  // Bands arrive ragged (one started reporting later, a point-limit
  // truncation landing mid-series), so the scan runs to the LONGEST band and
  // treats a missing value as a gap, exactly as stackBands does. Today this
  // changes no pixel: stackBands makes every index past a short band a gap,
  // so a stack the old bands[0]-length scan never saw is also a stack it
  // never draws, and `sum += undefined` produced NaN, which loses every
  // `>` comparison anyway. It is written this way so the ceiling and the
  // geometry cannot answer "how long is this stack" differently -- there is
  // no test below distinguishing the two, because at present none can.
  const n = bands.reduce((longest, b) => Math.max(longest, b.values.length), 0);
  let best = 0;
  for (let i = 0; i < n; i++) {
    if (bands.some((b) => b.values[i] == null)) continue;
    let sum = 0;
    for (const b of bands) sum += b.values[i] as number;
    if (sum > best) best = sum;
  }
  return best;
}

export function StackedSparkline({
  bands,
  max,
  width = 120,
  height = 32,
  pad = 2,
  label = "stacked chart",
  legend = true,
}: StackedSparklineProps) {
  const effectiveMax = max ?? maxRunningTotal(bands);
  const paths = stackBands(
    bands.map((b) => b.values),
    width,
    height,
    effectiveMax,
    pad,
  );

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
        {paths.map(
          (d, i) =>
            d !== "" && (
              <path
                key={bands[i]!.name}
                data-band
                d={d}
                fill={bands[i]!.color}
                // Dimmed fill, solid edge -- the pattern every tool that draws
                // a many-band stack well uses. Without it thirty-two bands are
                // one mass of colour: the fill says how much, and the crisp
                // edge is what separates each band from its neighbour. The
                // stroke outlines the whole polygon, and since a band's floor
                // IS the band below it, that single attribute draws every
                // separator in the stack.
                fillOpacity={0.55}
                stroke={bands[i]!.color}
                strokeWidth={1}
                strokeLinejoin="round"
              />
            ),
        )}
      </svg>
      {legend && bands.length >= 2 && (
        <div className="legend">
          {bands.map((b) => (
            <span key={b.name}>
              <i style={{ background: b.color }} />
              {b.name}
            </span>
          ))}
        </div>
      )}
    </>
  );
}
