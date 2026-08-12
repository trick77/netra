// A mirrored up/down traffic chart (e.g. inbound above the midline,
// outbound below it). geometry.ts's mirrorPaths() already breaks each side
// independently at its own gaps and never lets one side's null force a gap
// on the other -- this component only supplies the shared max and colours.
import { extent, mirrorPaths } from "./geometry";
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

export function UpDownSparkline({
  up,
  down,
  max,
  width = SPARK_WIDTH,
  height = 32,
  pad = 2,
  // Green above the axis, purple below. Inbound is the green half in every
  // traffic graph an operator has already read, and these started out the
  // other way round. Purple rather than blue for the lower half: against the
  // green above it, blue-vs-green separates by CVD dE 9 and reads as one
  // mass at a glance, where purple is 20 -- and the two halves of this chart
  // are the one comparison it exists to make.
  upColor = "var(--s2)",
  downColor = "var(--s5)",
  label = "up/down traffic chart",
}: UpDownSparklineProps) {
  const effectiveMax = max ?? Math.max(extent(up).max, extent(down).max);
  const { up: upPath, down: downPath } = mirrorPaths(
    up,
    down,
    width,
    height,
    effectiveMax,
    pad,
  );

  return (
    <svg
      className="spark"
      viewBox={`0 0 ${width} ${height}`}
      width={width}
      height={height}
      role="img"
      aria-label={label}
    >
      {upPath !== "" && (
        <path data-up d={upPath} fill={upColor} stroke="none" />
      )}
      {downPath !== "" && (
        <path data-down d={downPath} fill={downColor} stroke="none" />
      )}
    </svg>
  );
}
