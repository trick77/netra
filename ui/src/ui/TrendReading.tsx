import type { ReactNode } from "react";
import { SEVERITY_CLASS, severityFromPercent } from "./Meter";
import { useMeasuredWidth } from "./charts/useMeasuredWidth";
import {
  READING_CARD_HEIGHT,
  READING_SPARK_HEIGHT,
  READING_SPARK_WIDTH,
} from "./charts/size";

/**
 * One reading in a list: the figure, what it is measured against, and its
 * trend. The fleet's CPU, Memory, Filesystem and Traffic cells draw every
 * reading with this, so a host card and a host row say the same thing the
 * same way.
 *
 * ONE mark per quantity. The cell it replaces drew each reading three times
 * -- a sparkline, a ten-cell bar and the figure, in three weights -- and a
 * row of twelve such cells was noise. The figure carries the value and its
 * severity; the trend carries the history, on a fixed scale wherever the
 * quantity has one, so how full it is still shows as how high it sits.
 *
 * Two layouts of the same parts:
 *  - "cell": a table cell. Figure left, a READING_SPARK_WIDTH trend right,
 *    the measured-against line under both.
 *  - "card": a host card. Label, measured-against and figure on one line,
 *    the trend under it across the card's whole width (measured, since a
 *    card's width is the grid's to decide).
 */
export type ReadingLayout = "cell" | "card";

/** The trend at the size the layout gives it, or null for none. */
export type TrendAt = (width: number, height: number) => ReactNode;

// What a card's trend is drawn at before its first measurement (and in
// jsdom, which never lays anything out).
const CARD_FALLBACK_WIDTH = 340;

export function TrendReading({
  figure,
  under,
  trend,
  layout = "cell",
  fluid = false,
  label,
}: {
  /** The value: a <Figure> for a percentage, the rates for traffic. Null
   * prints nothing -- a cell with no reading never shows a dash. */
  figure: ReactNode;
  /** What the figure is measured against: "of 8 cores", a mount. */
  under?: ReactNode;
  trend: TrendAt;
  layout?: ReadingLayout;
  /** In a cell, let the trend take whatever width the column gives it
   * instead of READING_SPARK_WIDTH -- for the one column a table hands its
   * spare width to. Never narrower than READING_SPARK_WIDTH. */
  fluid?: boolean;
  /** The quantity's name. Only a card prints it: a table cell has its
   * column header. */
  label?: string;
}) {
  const { ref, width } = useMeasuredWidth<HTMLDivElement>(CARD_FALLBACK_WIDTH);
  // Classes are tr-card / tr-cell, never card / cell: .card is the app's
  // panel component and would box every reading in its own frame.
  if (layout === "card") {
    return (
      <div className="treading tr-card">
        <div className="treading-head">
          {label !== undefined && <span className="k">{label}</span>}
          {under !== undefined && <span className="u">{under}</span>}
          <span className="treading-fig">{figure}</span>
        </div>
        <div className="treading-trend" ref={ref}>
          {trend(width, READING_CARD_HEIGHT)}
        </div>
      </div>
    );
  }
  return (
    <div
      className={fluid ? "treading tr-cell tr-fluid" : "treading tr-cell"}
      // A fluid trend's track never goes under the fixed width: narrower
      // than that, the column holds and the table scrolls (.tablewrap)
      // rather than cutting off the newest data at the right edge. Inline,
      // so the floor is READING_SPARK_WIDTH itself and not a CSS copy of it.
      style={
        fluid
          ? {
              gridTemplateColumns: `auto minmax(${READING_SPARK_WIDTH}px, 1fr)`,
            }
          : undefined
      }
    >
      <span className="treading-fig">{figure}</span>
      <span className="treading-trend" ref={fluid ? ref : undefined}>
        {trend(
          fluid ? Math.max(READING_SPARK_WIDTH, width) : READING_SPARK_WIDTH,
          READING_SPARK_HEIGHT,
        )}
      </span>
      {under !== undefined && <span className="u">{under}</span>}
    </div>
  );
}

/**
 * A percentage as a reading's figure: rounded, the unit a step smaller, and
 * in its severity's text ink when it has one. ok is plain ink -- colour on a
 * reading means "look here".
 *
 * `unjudged` for a percentage with no ceiling to judge it by (a container's
 * raw percent of one core, which can read 300): printed the same way, with
 * no severity at all rather than an invented one.
 */
export function Figure({
  pct,
  label,
  unjudged = false,
}: {
  pct: number;
  label?: string;
  unjudged?: boolean;
}) {
  const className = unjudged
    ? "fig"
    : `fig ${SEVERITY_CLASS[severityFromPercent(pct)]}`;
  return (
    // role="img" with the name, when there is one: aria-label on a bare span
    // is ignored by screen readers, and the figure replaced a meter that
    // announced "CPU now, 40" -- without a role it read a bare "40%".
    <span
      className={className}
      role={label === undefined ? undefined : "img"}
      aria-label={
        label === undefined ? undefined : `${label} ${Math.round(pct)} %`
      }
    >
      {Math.round(pct)}
      <small>%</small>
    </span>
  );
}
