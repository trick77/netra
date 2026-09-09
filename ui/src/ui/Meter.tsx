// Fill colour comes from the series palette (--s1..--s4) or the status
// palette (--st-ok/--st-warn/--st-crit), NEVER --accent -- the
// accent is chrome (brand, current tab, ghost button, focus ring, primary
// button), not a data or severity fill. See index.css's comment above
// `.segbar`. Links and the active nav entry rest in ink and are no longer on
// that list -- see the comment above `a` in index.css.
import { NEUTRAL_TREND_COLOR } from "./StatTile";
import { SegmentBar } from "./SegmentBar";
import type { ReactNode } from "react";
import { ABSENT, percent } from "../lib/format";
import type { Severity } from "./Badge";

export interface MeterThresholds {
  warning: number;
  critical: number;
}

// beszel makes these user-configurable and netra may later, so they are a
// prop with a sensible default rather than a hardcoded cutoff.
export const DEFAULT_THRESHOLDS: MeterThresholds = {
  warning: 70,
  critical: 95,
};

// Exported so callers building pages against the shared `Severity` type
// (which also includes "neutral", for Badge's non-status case) have a
// concrete type to narrow to when they hand a severity to `Meter`.
export type FillSeverity = Exclude<Severity, "neutral">;

/**
 * A severity as the colour it is FILLED with: the status palette's mark hue,
 * never its --st-*-text step.
 *
 * Exported for a caller that paints an SVG and so cannot reach the colour
 * through SEVERITY_CLASS and a stylesheet -- the fleet row hands this
 * straight to Sparkline's `color`. One map, so a silhouette and the bar
 * under it cannot draw the same severity in two different greens.
 */
export const SEVERITY_COLOR: Record<FillSeverity, string> = {
  ok: "var(--st-ok)",
  warning: "var(--st-warn)",
  critical: "var(--st-crit)",
};

/**
 * The colour a saturation silhouette is drawn in: the severity of
 * what it is reading NOW, from the same 70/95 the bar under it uses.
 *
 * ONE colour system in the row, and it means severity. The three cells were
 * --cpu-1, --mem-used and --s6, hues that answered "which column is this" --
 * which the header answers already -- and an amber memory silhouette sat
 * beside an amber warn bar meaning something else entirely. They were then
 * all --ink-2, which fixed the collision by giving the silhouette nothing to
 * say at all: grey is also how this table draws a host with no data and one
 * that stopped reporting, so a healthy row and an empty one read alike.
 *
 * So the whole cell agrees instead. Silhouette, bar and figure take one
 * severity, and a row that is fine is green in all three -- a statement that
 * netra measured this, not the absence of one. Green is also the colour the
 * eye skips, which is what a fleet list wants: the rows that went amber are
 * the only ones that break the field.
 *
 * The CURRENT severity over the whole shape, not a line that changes hue at
 * the point it crossed 70: the cell is a reading of now with its history
 * behind it, and a two-tone line would say "it started filling here", which
 * is not what a threshold means. The fill weight is unchanged
 * (AREA_FILL_OPACITY) -- the shade still says how high the line sits.
 *
 * NEUTRAL_TREND_COLOR when there is no current value to judge: a host that
 * stopped reporting still draws the history it has, and colouring that by a
 * severity nobody measured would be an invention. The now-bar is already
 * absent in that case, so the cell reads as history without a reading.
 *
 * NOT the traffic cell. A rate has no ceiling, so there is no percentage, no
 * threshold and no severity to draw -- it keeps --in-1/--out-1, where hue
 * separates in from out across the midline.
 *
 * Cell and the host page's tiles, which follow the same rule (see
 * overviewTiles.ts trendColor). The ENLARGED views and the host page's chart
 * panels keep the full palette: a dialog is a chart someone opened to read,
 * not a mark scanned down a column, and its stacks name their bands by colour.
 *
 * Lives here rather than in hostColumns because the CONTAINER list uses it
 * too now. Its cells were --s1 blue and --cmem-1 amber whatever they were
 * reading, so a container at 96 %% of its memory limit drew the same amber as
 * one at 4 %% -- the collision this function was written to end, left standing
 * one directory over.
 */
export function trendColor(pct: number | null): string {
  return pct === null
    ? NEUTRAL_TREND_COLOR
    : SEVERITY_COLOR[severityFromPercent(pct)];
}

// The series palette used to be reached from here, as an inline fill colour
// on a continuous bar. A row of cells cannot be painted that way, so the four
// hues live beside the status ones in index.css now (.segbar.s1 .. .s4) and
// SegmentBar picks between them by class. The `series` prop is unchanged.

/**
 * Where a percentage falls against the thresholds.
 *
 * Exported because the container list's row rail has to agree with the meter
 * drawn inside that same row: two readings of one number that disagreed --
 * an amber bar on a row with a red rail -- would be worse than either alone.
 * One function, one answer.
 */
export function severityFromPercent(
  pct: number,
  thresholds: MeterThresholds = DEFAULT_THRESHOLDS,
): FillSeverity {
  if (pct >= thresholds.critical) return "critical";
  if (pct >= thresholds.warning) return "warning";
  return "ok";
}

/**
 * The app's own spelling of a severity as a class: st-ok, st-warn,
 * st-crit (see the status pair in index.css). One map, beside
 * the function that decides the severity, so a bar and the figure printed
 * next to it -- SegmentBar and NowReading -- cannot spell it differently.
 */
export const SEVERITY_CLASS: Record<FillSeverity, string> = {
  ok: "st-ok",
  warning: "st-warn",
  critical: "st-crit",
};

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
  severity = null,
}: {
  label?: string;
  bar: ReactNode;
  valueText: string;
  /**
   * The reading's severity, or null for a row with nothing to say.
   *
   * `ok` is deliberately not a treatment here, the same rule StatTile states
   * at length: painting a healthy reading green would make a hue mean
   * "someone thought about it" rather than "look at this". The BAR still
   * takes its ok colour -- a bar's fill needs some colour to be a fill --
   * but the figure beside it stays plain ink until there is a reason.
   */
  severity?: FillSeverity | null;
}) {
  const valueClass =
    severity === null || severity === "ok"
      ? "val"
      : `val ${SEVERITY_CLASS[severity]}`;
  // The figure sits BESIDE the bar, not in a column of its own at the far
  // right of whatever the row happens to be wide. The old shape was a
  // 1fr/92px grid, which made sense while the bar stretched to fill the 1fr;
  // with the bar at one fixed width everywhere (see .segbar), that grid put
  // the Overview's Disk panel bar at the left edge and its percentage most of
  // a page away. The fleet row has always drawn the two together
  // (.metric-now) for the reason that they are one reading. The figures still
  // line up down the card, because every bar in the app is now the same
  // width.
  return (
    <div className="mrow">
      {label !== undefined && <div className="lab">{label}</div>}
      <div className="mrow-read">
        {bar}
        <div className={valueClass}>{valueText}</div>
      </div>
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
  // the bar must say so. `barPct` is clamped only because a bar has no cell
  // past its tenth: at >100% every cell lights (a defensible choice -- there
  // is no more "full" than full) but the text next to it still reports the
  // true percentage, never the clamped one. Severity is likewise derived from
  // the true value, so an
  // overage still reads as critical rather than merely "at the top".
  const rawPct = (value / max) * 100;
  const barPct = Math.max(0, Math.min(100, rawPct));
  const resolvedSeverity: FillSeverity =
    severity ?? severityFromPercent(rawPct, thresholds);
  const valueText = formatValue
    ? formatValue(value, max, rawPct)
    : percent(rawPct);

  return (
    <Row
      label={label}
      // The same ten-cell bar the fleet row draws, not a continuous fill of
      // this component's own. Two shapes for one job was the whole of the
      // difference: a filesystem at 62 % read as a proportion of a track here
      // and as six lit cells of ten in the fleet, and the track stretched to
      // whatever width the row had. litCells() also carries the floor of one
      // lit cell above zero, so a mount at 3 % says "barely used" rather than
      // drawing the empty row that means "nothing was measured".
      bar={
        <SegmentBar
          pct={barPct}
          severity={resolvedSeverity}
          series={series}
          label={label}
          // The unclamped reading, spoken. `pct` is clamped so the bar has a
          // cell to light and aria-valuenow stays inside its own max; this is
          // the figure the row prints, which for an overage is the only one
          // that is true.
          valueText={valueText}
        />
      }
      valueText={valueText}
      // The figure and the bar read the SAME severity. They did not before:
      // the bar was painted from resolvedSeverity while .val was pinned to
      // --muted in index.css, so a filesystem drew a red bar beside a grey
      // number at every percentage.
      severity={series !== undefined ? null : resolvedSeverity}
    />
  );
}
