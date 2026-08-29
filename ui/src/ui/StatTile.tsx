import type { MouseEvent } from "react";
import { ABSENT } from "../lib/format";
import { Sparkline } from "./charts/Sparkline";
import { useMeasuredWidth } from "./charts/useMeasuredWidth";
import type { FillSeverity } from "./Meter";

/**
 * One figure, and the shape of how it got there.
 *
 * The host Overview used to state none of its numbers: what the CPU was at,
 * how full the busiest disk was and how much traffic was moving all had to be
 * READ OFF A CHART, because the page was ten charts and nothing else. A tile
 * is the other half of that -- the reading printed large, with the same
 * window's trend behind it so a figure that is high-and-falling cannot be
 * mistaken for one that is high-and-climbing.
 *
 * The sparkline is full-bleed along the bottom rather than a boxed chart
 * beside the number, and that is the whole layout: at 92px tall there is room
 * for a label, a figure and a silhouette, and a framed chart would spend a
 * third of the tile on its own frame. There is no axis for the same reason --
 * a tile is a reading and a direction, and the panel it links to is where the
 * numbers on the axis live.
 */
export interface StatTileProps {
  /** What the figure is. A noun, not a sentence: "Context switches". */
  label: string;
  /**
   * The figure itself, ALREADY FORMATTED -- "37", "51K", ABSENT. Formatting
   * here would need a unit registry this component has no business owning,
   * and every caller already holds one (lib/format.ts). A null reading is the
   * caller's ABSENT, never a 0 invented at the last moment.
   */
  value: string;
  /** "%", "K/s", "Mb/s". Drawn one step quieter and smaller, so the figure
   * still reads as the figure. */
  unit?: string;
  /** One line under the figure for the fact that qualifies it -- the
   * denominator, the mount point, the total. Omitted when there is none;
   * never a dash. */
  sub?: string;
  /** The series behind the figure, over the page's window. Nulls are gaps,
   * and Sparkline draws them as gaps. */
  values: (number | null)[];
  /** A series token, never a hex literal -- the palette lives in index.css. */
  color?: string;
  /**
   * The status treatment, or null for none.
   *
   * `null` rather than "ok" on purpose, and it is the one thing this
   * component insists on: severityFromPercent() answers "ok" for a healthy
   * reading, and painting that green would make a hue on this page mean
   * "someone thought about it" instead of "look at this". A tile wears the
   * status palette only when something is actually off, which is the same
   * rule the attention band and the fleet list already follow. Callers map
   * "ok" to null.
   */
  severity?: FillSeverity | null;
  /** The chart this tile is a summary of. Given one, the tile is a real link
   * -- see the click handler below. */
  href?: string;
  /** Client-side navigation, matching StatFigure and Tabs. */
  onSelect?: () => void;
}

/** Wide enough to have a shape before ResizeObserver answers, narrow enough
 * that it never forces the grid track wider than the tile. The tile is fluid;
 * this is only what the first paint draws at. */
const FALLBACK_WIDTH = 150;
/** The strip the sparkline gets along the bottom.
 *
 * Not SPARK_HEIGHT (45), and not the 38 it started at either: the label, the
 * figure and the sub-line stack to ~62px inside a 100px tile, and a 38px
 * strip put the trend line straight through "24 cores". 32 clears it. The
 * text is also lifted above the svg in index.css, so a longer sub-line on
 * some future host wraps over the chart rather than under it. */
const SPARK_STRIP = 32;

export function StatTile({
  label,
  value,
  unit,
  sub,
  values,
  color = "var(--s1)",
  severity = null,
  href,
  onSelect,
}: StatTileProps) {
  // MEASURED, not scaled. The tile is a fluid grid track, and a sparkline
  // stretched with width:100% would take its stroke width with it -- see the
  // docstring on useMeasuredWidth, which is the argument in full.
  const { ref, width } = useMeasuredWidth<HTMLElement>(FALLBACK_WIDTH);

  const body = (
    <>
      <span className="k">{label}</span>
      <span className="v">
        {value}
        {/* No unit beside an absent reading. "– /s" reads as a rate that
            happens to be missing its number; the dash on its own is the
            whole answer, which is how every other absent value in the app
            is written. */}
        {unit !== undefined && value !== ABSENT && (
          <span className="u">{unit}</span>
        )}
      </span>
      {sub !== undefined && <span className="sub">{sub}</span>}
      {/* pad=0, unlike every other sparkline in the app: this one is bled to
          the tile's edges, so a 2px inset would draw a hairline of surface
          under it that reads as a gap rather than as breathing room. */}
      <Sparkline
        values={values}
        width={width}
        height={SPARK_STRIP}
        pad={0}
        color={color}
        label={`${label} trend`}
      />
    </>
  );

  const className = `tile${severity === null ? "" : ` sev-${severity}`}`;

  if (href === undefined) {
    return (
      <div className={className} ref={ref}>
        {body}
      </div>
    );
  }

  // Lifted from StatFigure in StatRail.tsx, which states the reasoning: a
  // real <a> so middle-click, cmd-click, copy-link and keyboard focus all
  // work without this component reimplementing any of them, and only a plain
  // primary click is intercepted.
  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    if (!onSelect) return;
    if (event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey)
      return;
    event.preventDefault();
    onSelect();
  };

  return (
    <a className={className} href={href} onClick={handleClick} ref={ref}>
      {body}
    </a>
  );
}
