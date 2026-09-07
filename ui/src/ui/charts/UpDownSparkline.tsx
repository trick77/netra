// A mirrored up/down traffic chart (e.g. inbound above the midline,
// outbound below it). geometry.ts's mirrorPaths() already breaks each side
// independently at its own gaps and never lets one side's null force a gap
// on the other -- this component supplies the shared max and the colours.
//
// The mark weights come from size.ts, not from here. The host page's Traffic
// panel plots the same rx/tx pair through the same geometry, and the two are
// read on the same screen: a fleet row's traffic cell and the panel it opens
// must be the same mark at two sizes, or the operator has to learn the chart
// twice. Sharing the constants is what makes that true by construction rather
// than by everyone remembering to edit both files.
import { extent } from "./geometry";
import { Chart } from "./Chart";
// The mirror weights and the midline stroke are Chart's now; only the shared
// sparkline width is still read here.
import { SPARK_HEIGHT, SPARK_WIDTH } from "./size";

export interface UpDownSparklineProps {
  up: (number | null)[];
  down: (number | null)[];
  /**
   * The bucket PEAK behind each mean, drawn as a pale envelope.
   *
   * The reason the cell has them at all: past the 1h range a point is a
   * five-minute or hourly AVERAGE, and a saturation that lasted three minutes
   * is a fifth of its own height by the time it reaches the mean -- then half
   * that again where reduceToColumns folds two buckets into one pixel. The
   * hub materialises max(rx_bytes) beside the average at every rolled-up tier
   * and the cell simply never asked for it, so the one reading an operator
   * scans this column FOR was the one it could not show.
   *
   * The mean stays the line. Drawing the peak ALONE was tried and reverted:
   * taking the bucket peak and then the peak of each pixel column compounds,
   * and the quiet body of a real host's chart -- which is most of it -- drops
   * under one pixel. The envelope carries the burst, the line carries the
   * number.
   *
   * Empty at the raw tier, where the sample IS its own peak and an envelope
   * drawn exactly on its own line would be ink for nothing. Chart handles
   * that: an empty band draws no path.
   */
  upBand?: (number | null)[];
  downBand?: (number | null)[];
  /** Shared scale for both sides. Auto-computed from up/down when omitted.
   *
   * Read only by furniture that names a value: a sparkline carries no tick
   * ladder, so mirrorPaths scales it against the combined range of the pair
   * it was handed (see `independent` there) and a caller's ceiling does not
   * reach the mark. Passing one to force two cells onto the same scale will
   * not do that. */
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
export const UP_COLOR = "var(--in-1)";
export const DOWN_COLOR = "var(--out-1)";

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
  upBand,
  downBand,
  max,
  width = SPARK_WIDTH,
  height = SPARK_HEIGHT,
  pad = 2,
  upColor = UP_COLOR,
  downColor = DOWN_COLOR,
  label = "up/down traffic chart",
}: UpDownSparklineProps) {
  // Both directions share one ceiling, which is what makes the two halves
  // comparable -- scaling each to its own extent would draw a trickle of
  // egress the same size as a saturated ingress. The mark derives that
  // ceiling from the pair itself now (mirrorPaths' `independent`); this is
  // still computed for the furniture a caller may hang on the same Chart.
  // Taken from the ENVELOPE where there is one, not from the mean: the peak
  // is the tallest thing on the chart, and a ceiling derived from the mean
  // would let the envelope run off the top of the cell.
  const effectiveMax =
    max ??
    Math.max(
      extent(upBand && upBand.length > 0 ? upBand : up).max,
      extent(downBand && downBand.length > 0 ? downBand : down).max,
    );

  /* Proportional, and the ceiling is the window's own peak -- what RRDtool
     and the graphs an operator already reads draw. A bursty host's quiet
     baseline goes flat next to its spikes, and that is the reading: heights
     mean the same thing in every cell of the column, which a bent axis
     cannot promise. */

  return (
    <Chart
      series={[
        { name: "up", color: upColor, values: up, band: upBand },
        { name: "down", color: downColor, values: down, band: downBand },
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
