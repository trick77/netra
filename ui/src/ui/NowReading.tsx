import { SegmentBar } from "./SegmentBar";
import { severityFromPercent, type FillSeverity } from "./Meter";

const SEVERITY_CLASS: Record<FillSeverity, string> = {
  ok: "",
  warning: "st-warn",
  serious: "st-serious",
  critical: "st-crit",
};

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
 * only half of it the second time.
 *
 * `Reading` (ui/Reading.tsx) is the figure-only form the container list
 * still draws beside its charts; this is not a replacement for it there.
 */
export function NowReading({
  pct,
  under,
  label,
  className,
}: {
  /** The percentage the bar shows and the figure prints, unrounded. */
  pct: number;
  /** What it is measured against: "of 8 cores", "of 14.9 GiB". */
  under?: string;
  /** Names the bar for assistive tech: "CPU now", "Disk /var/log". */
  label?: string;
  /** An extra class on the figure, for a caller that has to find it. */
  className?: string;
}) {
  const severity = SEVERITY_CLASS[severityFromPercent(pct)];
  const figureClass = [className, severity].filter((c) => c).join(" ");
  return (
    <div className="metric-now-wrap">
      <div className="metric-now">
        <SegmentBar pct={pct} label={label} />
        <span className={figureClass === "" ? "v" : `v ${figureClass}`}>
          {Math.round(pct)}
          <small>%</small>
        </span>
      </div>
      {under !== undefined && <span className="u">{under}</span>}
    </div>
  );
}
