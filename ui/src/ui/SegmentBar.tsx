import { severityFromPercent, type FillSeverity } from "./Meter";

/**
 * The current value of a percentage as a row of ten cells, lit from the left.
 *
 * What the fleet's CPU, Memory and Disk cells draw under their reading. A
 * sparkline answers "what has this host been doing"; this answers "where is
 * it NOW, and is that a problem" in a form that reads at a glance across a
 * column of rows -- the way Zabbix's Top hosts widget does it. Ten cells
 * rather than a continuous bar because ten steps is what an eye can count
 * without reading a number, and because a lit cell is a mark the way a bar
 * segment is not: 7 of 10 lit is a reading, 68% of a track is a proportion.
 *
 * The severity is the Meter's own -- same thresholds, same colour tokens --
 * so the disk cell's bar and this bar can never disagree about what 91% means.
 * Every cell takes the severity, not just the ones past the threshold: a bar
 * that turns amber at the seventh cell alone reads as a marker at 70, not as
 * a host at 76.
 */
export const SEGMENT_CELLS = 10;

const SEVERITY_CLASS: Record<FillSeverity, string> = {
  ok: "st-ok",
  warning: "st-warn",
  serious: "st-serious",
  critical: "st-crit",
};

/** Cells lit for a percentage: the nearest tenth, clamped to the row. */
export function litCells(pct: number): number {
  if (!Number.isFinite(pct)) return 0;
  return Math.max(0, Math.min(SEGMENT_CELLS, Math.round(pct / 10)));
}

export function SegmentBar({ pct, label }: { pct: number; label?: string }) {
  const lit = litCells(pct);
  const severity = SEVERITY_CLASS[severityFromPercent(pct)];
  return (
    <div
      className={`segbar ${severity}`}
      role="meter"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(pct)}
      aria-label={label}
    >
      {Array.from({ length: SEGMENT_CELLS }, (_, i) => (
        <i key={i} className={i < lit ? "on" : undefined} />
      ))}
    </div>
  );
}
