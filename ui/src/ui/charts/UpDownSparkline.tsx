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
import { trafficScale } from "./scale";
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

  /* Non-linear, and this is the whole reason scale.ts exists. Traffic has no
     ceiling and a very heavy tail: measured on ark.o11.net over 24 h, the
     typical five-minute bucket is 29 kB/s and the day's peak is 101 MB/s,
     ~3500:1. Against a proportional axis the typical bucket drew 0.004 px of
     this chart's 14 px half-height, so the cell was a hairline on the midline
     with three spikes -- the whole day unreadable, which is the bug this
     replaced. Through asinh the same bucket draws 5.03 px and the spikes
     still reach the top. See scale.ts for why asinh and not log, and why the
     knee is a constant. */
  const toFraction = trafficScale(effectiveMax);

  return (
    <Chart
      series={[
        { name: "up", color: upColor, values: up },
        { name: "down", color: downColor, values: down },
      ]}
      width={width}
      height={height}
      max={effectiveMax}
      scale={toFraction}
      pad={pad}
      mark="mirror"
      label={label}
    />
  );
}
