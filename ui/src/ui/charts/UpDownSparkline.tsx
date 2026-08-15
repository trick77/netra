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
import { extent, mirrorPaths } from "./geometry";
import {
  AXIS_STROKE,
  AXIS_WIDTH,
  MIRROR_FILL_OPACITY,
  MIRROR_STROKE_WIDTH,
  SPARK_WIDTH,
} from "./size";

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
  const {
    up: upPath,
    down: downPath,
    mid,
  } = mirrorPaths(up, down, width, height, effectiveMax, pad);

  return (
    <svg
      className="spark"
      viewBox={`0 0 ${width} ${height}`}
      width={width}
      height={height}
      role="img"
      aria-label={label}
    >
      {/* One closed polygon carrying both a dimmed fill and an opaque stroke
          of the same token -- the house pattern for every other chart here,
          and the reason a fully opaque fill read as two blocks of colour
          rather than a silhouette. Because mirrorPaths() closes each run to
          the midline, the stroke also traces the drop at each run's ends;
          that is true of the throughput panel too, and the axis rule below
          covers it. */}
      {upPath !== "" && (
        <path
          data-up
          d={upPath}
          fill={upColor}
          fillOpacity={MIRROR_FILL_OPACITY}
          stroke={upColor}
          strokeWidth={MIRROR_STROKE_WIDTH}
        />
      )}
      {downPath !== "" && (
        <path
          data-down
          d={downPath}
          fill={downColor}
          fillOpacity={MIRROR_FILL_OPACITY}
          stroke={downColor}
          strokeWidth={MIRROR_STROKE_WIDTH}
        />
      )}
      {/* Drawn last, on top, and unconditionally. Once the fills are dimmed
          the mirror axis is what says where zero is, and a host reporting
          nothing shows a bare rule rather than an empty box -- "axis, no
          data" instead of "the chart failed to render". */}
      <line
        data-mid
        x1={0}
        x2={width}
        y1={mid}
        y2={mid}
        stroke={AXIS_STROKE}
        strokeWidth={AXIS_WIDTH}
      />
    </svg>
  );
}
