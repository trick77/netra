// Fill colour comes from the series palette (--s1..--s4) or the status
// palette (--st-ok/--st-warn/--st-serious/--st-crit), NEVER --accent -- the
// accent is chrome (brand, current tab, ghost button, focus ring, primary
// button), not a data or severity fill. See index.css's comment above
// `.meter`. Links and the active nav entry rest in ink and are no longer on
// that list -- see the comment above `a` in index.css.
import type { ReactNode } from "react";
import { ABSENT, percent } from "../lib/format";
import type { Severity } from "./Badge";

export interface MeterThresholds {
  warning: number;
  serious: number;
  critical: number;
}

// beszel makes these user-configurable and netra may later, so they are a
// prop with a sensible default rather than a hardcoded cutoff.
export const DEFAULT_THRESHOLDS: MeterThresholds = {
  warning: 70,
  serious: 85,
  critical: 95,
};

// Exported so callers building pages against the shared `Severity` type
// (which also includes "neutral", for Badge's non-status case) have a
// concrete type to narrow to when they hand a severity to `Meter`.
export type FillSeverity = Exclude<Severity, "neutral">;

const STATUS_VAR: Record<FillSeverity, string> = {
  ok: "var(--st-ok)",
  warning: "var(--st-warn)",
  serious: "var(--st-serious)",
  critical: "var(--st-crit)",
};

const SERIES_VAR: Record<1 | 2 | 3 | 4, string> = {
  1: "var(--s1)",
  2: "var(--s2)",
  3: "var(--s3)",
  4: "var(--s4)",
};

function severityFromPercent(
  pct: number,
  thresholds: MeterThresholds,
): FillSeverity {
  if (pct >= thresholds.critical) return "critical";
  if (pct >= thresholds.serious) return "serious";
  if (pct >= thresholds.warning) return "warning";
  return "ok";
}

export interface MeterProps {
  /** Current reading. `null` means "not collected" -- distinct from 0. */
  value?: number | null;
  /** Denominator. `null` means "unknown", not "unlimited" -- use `noLimit` for that. */
  max?: number | null;
  /**
   * A container/host with no configured limit has nothing to be a
   * percentage of. Drawing it against the host total would invent a
   * denominator that was never set, so this renders the words "no limit"
   * instead of a bar.
   */
  noLimit?: boolean;
  label?: string;
  /**
   * Explicit severity override; otherwise derived from value/max vs.
   * `thresholds`. Deliberately `FillSeverity`, not the full `Severity`
   * union -- `Severity` also has "neutral" (Badge's non-status case), and
   * a meter has no genuinely neutral fill to give it. Silently mapping
   * "no opinion" to "ok" would assert a status the caller never claimed,
   * so this is a compile error instead: a caller with a `Severity` value
   * must narrow away "neutral" before handing it to `Meter`.
   */
  severity?: FillSeverity;
  thresholds?: MeterThresholds;
  /** Use the series palette instead of the status palette (non-severity meters). */
  series?: 1 | 2 | 3 | 4;
  formatValue?: (value: number, max: number, pct: number) => string;
}

function Row({
  label,
  bar,
  valueText,
}: {
  label?: string;
  bar: ReactNode;
  valueText: string;
}) {
  return (
    <div className="mrow">
      <div>
        {label !== undefined && <div className="lab">{label}</div>}
        {bar}
      </div>
      <div className="val">{valueText}</div>
    </div>
  );
}

export function Meter({
  value = null,
  max = null,
  noLimit = false,
  label,
  severity,
  thresholds = DEFAULT_THRESHOLDS,
  series,
  formatValue,
}: MeterProps) {
  if (noLimit) {
    return <Row label={label} bar={null} valueText="no limit" />;
  }

  // Absent is not zero, and it is not a guess either: with no value or no
  // max there is no percentage to draw, so render the absent marker
  // instead of dividing by an invented denominator.
  if (value === null || max === null || max === 0) {
    return <Row label={label} bar={null} valueText={ABSENT} />;
  }

  // `rawPct` is the true, unclamped reading -- a container 150% over its
  // memory limit is a real and interesting state, and the number beside
  // the bar must say so. `barPct` is clamped only because a bar cannot
  // physically be drawn wider than its track: at >100% the fill renders
  // full-width (a defensible choice -- there is no more "full" than full)
  // but the text next to it still reports the true percentage, never the
  // clamped one. Severity is likewise derived from the true value, so an
  // overage still reads as critical rather than merely "at the top".
  const rawPct = (value / max) * 100;
  const barPct = Math.max(0, Math.min(100, rawPct));
  const resolvedSeverity: FillSeverity =
    severity ?? severityFromPercent(rawPct, thresholds);
  const fillColor =
    series !== undefined ? SERIES_VAR[series] : STATUS_VAR[resolvedSeverity];
  const valueText = formatValue
    ? formatValue(value, max, rawPct)
    : percent(rawPct);

  return (
    <Row
      label={label}
      bar={
        <div className="meter">
          <i style={{ width: `${barPct}%`, background: fillColor }} />
        </div>
      }
      valueText={valueText}
    />
  );
}
