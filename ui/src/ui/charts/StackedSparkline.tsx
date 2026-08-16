// A cumulative stacked-area chart. geometry.ts's stackBands() already
// breaks every band at any index where ANY series is null (a running total
// is undefined for every band there, not just the null one) -- this
// component's only job is to hand it the right `max` and turn its output
// into SVG paths.
import { stackBands } from "./geometry";
import {
  REFERENCE_DASH,
  REFERENCE_STROKE,
  REFERENCE_WIDTH,
  SPARK_WIDTH,
} from "./size";

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
   * A value to mark with a dashed rule across the chart -- for memory, the
   * host's total RAM.
   *
   * Without it a stack scaled to a ceiling is uninterpretable: the shape
   * says how the parts move but not whether the host is nearly full or
   * barely touched, because nothing on screen says what the top of the box
   * means. The caller is expected to leave headroom (a max slightly above
   * the reference) so the rule lands inside the plot rather than on its
   * edge, where it would be indistinguishable from a border.
   */
  reference?: number;
  /**
   * Whether to name every band underneath the chart.
   *
   * OFF by default, and that default is the 32-core CPU cell: a 32-thread
   * host produced a legend five times taller than the chart it explained and
   * pushed every other column of the fleet row off the screen. Thirty-two
   * cores cannot each own a hue anyway, so there is no identity there for a
   * legend to carry -- the shape is the whole message.
   *
   * But it is a per-CALLER decision, not a property of sparklines. Removing
   * the legend outright to fix the CPU cell also stripped the five-band
   * memory cell in the same row, where used/shared/ARC/buffers/cached are
   * five distinct hues that DO carry identity -- and identity on colour alone
   * is exactly what a legend exists to prevent. Five nameable bands opt in;
   * thirty-two unnameable ones do not.
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
  width = SPARK_WIDTH,
  height = 32,
  pad = 2,
  label = "stacked chart",
  reference,
  legend = false,
}: StackedSparklineProps) {
  const effectiveMax = max ?? maxRunningTotal(bands);
  // Where the reference sits in the plot box. Null when there is nothing to
  // mark, or when it would land outside -- a rule drawn off the edge is a
  // rule nobody can read.
  const referenceY =
    reference === undefined || effectiveMax <= 0
      ? null
      : height - pad - (reference / effectiveMax) * (height - 2 * pad);

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
        {referenceY !== null && (
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
        )}
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
      {/* Off unless the caller asks. A sparkline is a shape in a table cell,
          read at a glance alongside four other columns, and at 32 cores a
          list of band names under it was taller than the row itself. Where
          the bands are few and separately coloured -- the memory cell's five
          -- the names are the only thing keeping identity off colour alone,
          so that caller opts in. `bands.length >= 2` because a single band
          has nothing to distinguish it from. */}
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
