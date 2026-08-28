// A mirrored up/down traffic chart (e.g. inbound above the midline,
// outbound below it). geometry.ts's mirrorPaths() already breaks each side
// independently at its own gaps and never lets one side's null force a gap
// on the other -- this component supplies the shared max and the colours.
//
// The mark weights come from size.ts, not from here. Interface throughput
// plots the same rx/tx pair through the same mirrorPaths() geometry, and the
// two are read on the same screen: a fleet row's traffic cell and the
// throughput panel must be the same mark at two sizes, or the operator has
// to learn the chart twice. Sharing the constants is what makes that true by
// construction rather than by everyone remembering to edit both files.
import { extent } from "./geometry";
import { Chart } from "./Chart";
// The mirror weights and the midline stroke are Chart's now; only the shared
// sparkline width is still read here.
import { SPARK_WIDTH } from "./size";

export interface UpDownSparklineProps {
  up: (number | null)[];
  down: (number | null)[];
  /** Shared scale for both sides. Auto-computed from up/down when omitted. */
  max?: number;
  width?: number;
  height?: number;
  pad?: number;
  /** CSS variable strings. The brief's signature for this component omits
   * colour props entirely; these default to series tokens (never a hex
   * literal) so the component still never invents a hue, while a caller
   * that does want a specific pair of series colours can override them. */
  upColor?: string;
  downColor?: string;
  label?: string;
}

/**
 * Green above the axis, purple below. Inbound is the green half in every
 * traffic graph an operator has already read, and these started out the
 * other way round. Purple rather than blue for the lower half: against the
 * green above it, blue-vs-green separates by CVD dE 9 and reads as one mass
 * at a glance, where purple is 20 -- and the two halves of this chart are the
 * one comparison it exists to make.
 *
 * Exported because the enlarged view of a traffic sparkline is drawn by
 * Overlay rather than by this component, and a chart that changed colour on
 * being clicked open would be a different chart.
 */
export const UP_COLOR = "var(--s2)";
export const DOWN_COLOR = "var(--s5)";

/**
 * The same pair, one lightness step per interface, for the panel that stacks
 * traffic per interface rather than summing it.
 *
 * Index 0 IS the pair above, so a one-NIC host draws exactly what its fleet
 * cell draws -- which is the property that lets the cell open into the panel
 * without the chart changing under the click. The walk wraps; see index.css
 * for why three steps and not eight.
 */
export const UP_SHADES = [
  UP_COLOR,
  "var(--in-2)",
  "var(--in-3)",
  "var(--in-4)",
];
export const DOWN_SHADES = [
  DOWN_COLOR,
  "var(--out-2)",
  "var(--out-3)",
  "var(--out-4)",
];

export function UpDownSparkline({
  up,
  down,
  max,
  width = SPARK_WIDTH,
  height = 32,
  pad = 2,
  upColor = UP_COLOR,
  downColor = DOWN_COLOR,
  label = "up/down traffic chart",
}: UpDownSparklineProps) {
  // Both directions share one ceiling, which is what makes the two halves
  // comparable -- scaling each to its own extent would draw a trickle of
  // egress the same size as a saturated ingress.
  const effectiveMax = max ?? Math.max(extent(up).max, extent(down).max);

  /* Proportional, and the ceiling is the window's own peak -- what RRDtool
     and the graphs an operator already reads draw. A bursty host's quiet
     baseline goes flat next to its spikes, and that is the reading: heights
     mean the same thing in every cell of the column, which a bent axis
     cannot promise. */

  return (
    <Chart
      series={[
        { name: "up", color: upColor, values: up },
        { name: "down", color: downColor, values: down },
      ]}
      width={width}
      height={height}
      max={effectiveMax}
      pad={pad}
      mark="mirror"
      label={label}
    />
  );
}
