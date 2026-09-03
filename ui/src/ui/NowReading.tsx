import type { ReactNode } from "react";
import { SegmentBar } from "./SegmentBar";
import { SEVERITY_CLASS, severityFromPercent } from "./Meter";

/**
 * The fleet row's "now" line: a segmented bar for a percentage, the figure
 * beside it in the bar's own severity colour, and what it is measured
 * against on a quiet line underneath.
 *
 * Sits under a sparkline in the CPU and Memory cells and stands alone in the
 * Disk cell, so the three saturation metrics in a row read alike: history
 * on top where there is one, the current value as the same bar-and-figure
 * in every column. The figure takes the bar's colour because they are one
 * reading -- an amber bar beside a plain "76 %" said one thing twice and
 * only half of it the second time. The severity is judged once, here, and
 * handed to the bar.
 *
 * `Reading` (ui/Reading.tsx) is the figure-only form the container list
 * still draws beside its charts; this is not a replacement for it there.
 */
export function NowReading({
  pct,
  under,
  label,
}: {
  /** The percentage the bar shows and the figure prints, unrounded. */
  pct: number;
  /** What it is measured against: "of 8 cores", "of 14.9 GiB", or the disk
   * cell's mount and bytes left. */
  under?: ReactNode;
  /** Names the bar for assistive tech: "CPU now", "Disk /var/log". */
  label?: string;
}) {
  const severity = severityFromPercent(pct);
  // No class at all when there is nothing to say: a calm figure is plain
  // ink, not green. The bar keeps st-ok, because its lit cells need a colour.
  const figureClass = severity === "ok" ? "v" : `v ${SEVERITY_CLASS[severity]}`;
  return (
    <div className="metric-now-wrap">
      <div className="metric-now">
        <SegmentBar pct={pct} severity={severity} label={label} />
        <span className={figureClass}>
          {Math.round(pct)}
          <small>%</small>
        </span>
      </div>
      {under !== undefined && <span className="u">{under}</span>}
    </div>
  );
}
