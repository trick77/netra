// How an enlarged chart decides the box it is drawn in.
//
// Its own module because two things now ask the question and they must
// answer it identically: the big figure in the middle of the dialog, and
// every mini-chart in the rail beside it. A tile scaled differently from the
// chart it is a preview of is a different picture of the same data, and the
// tile is what the reader clicks to see the big one.
import type { OverlaySeries } from "./Overlay";
import { extent } from "./geometry";
import { peak } from "./ChartDetail";

/**
 * The extent to draw a free-scaled chart against, or nothing when the series
 * has no readings to scale to.
 *
 * The test is whether anything was REPORTED, not whether the extent has
 * width. A flat series is a real reading -- a fan pinned at one speed, a rail
 * that never moved -- and its degenerate span is what the sparkline above is
 * already drawn from: scaleY() centres a min === max series in the box rather
 * than dividing by zero. Rejecting it here would drop the dialog back to a
 * zero floor and draw the same fan at the top of a 0-1200 box, which is the
 * disagreement autoScale exists to prevent.
 *
 * An EMPTY series is the other case, and it is reachable rather than
 * theoretical: widen a sensor chart to 7d and the 1h tier materialises about
 * ninety minutes behind now, so a window with no rows comes back with
 * nothing. extent() answers {min: 0, max: 0} for it, and passing that 0
 * through would ALSO defeat ChartDetail's own `max || 1` guard, because
 * `0 ?? x` is 0 and the guard is only reached for an absent max.
 */
export function derived(series: readonly OverlaySeries[]): {
  min?: number;
  max?: number;
} {
  const values = series.flatMap((s) => s.values);
  return values.some((v) => v !== null) ? extent(values) : {};
}

/**
 * The given ceiling, raised if the data now on screen would not fit under it.
 *
 * A fixed `max` is a deliberate choice about the SMALL chart: the container
 * lists share one ceiling down the column so the rows can be compared, and
 * the fleet's CPU cell pins 100 so an idle host and a saturated one do not
 * draw the same silhouette. Opening the dialog keeps it, which is the point
 * -- a chart that rescaled itself on opening would redraw the shape the
 * reader just clicked.
 *
 * Widening the range is the other case. The refetched window is this one
 * chart's alone, and it is asked for precisely to find something the page's
 * window did not show. Held to the old ceiling, any bucket above it is drawn
 * OUTSIDE the plot -- linePath() deliberately never clamps (geometry.ts) --
 * so the spike someone widened the window to see is the one thing that
 * disappears off the top.
 *
 * Raised only, never lowered: a quieter wide window keeps the ceiling it was
 * opened with, so the shape stays comparable with the cell behind it.
 */
export function fitted(
  max: number | undefined,
  refetched: OverlaySeries[] | null,
  stacked: boolean | undefined,
  mirrored: boolean | undefined,
): number | undefined {
  if (max === undefined || refetched === null) return max;
  return Math.max(max, peak(refetched, stacked, mirrored));
}

/**
 * The floor and ceiling for one set of series, under the caller's policy.
 *
 * The one entry point, so the figure and the rail cannot disagree: pass the
 * series being drawn and get back the box to draw them in.
 */
export function scaleFor(
  shown: OverlaySeries[],
  refetched: OverlaySeries[] | null,
  opts: {
    autoScale?: boolean;
    min?: number;
    max?: number;
    stacked?: boolean;
    mirrored?: boolean;
  },
): { min?: number; max?: number } {
  return opts.autoScale
    ? derived(shown)
    : {
        min: opts.min,
        max: fitted(opts.max, refetched, opts.stacked, opts.mirrored),
      };
}
