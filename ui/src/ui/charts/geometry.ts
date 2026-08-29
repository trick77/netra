// Pure chart-geometry math, with no React import and no DOM access, so the
// one behaviour that matters most here -- a null in the data producing a
// hole in the line rather than a bridge across it -- can be pinned by a
// fast unit test instead of discovered later through a rendered chart.
//
// SVG's y-axis grows downward, so every value-to-y mapping below inverts
// the value: a higher value must land at a smaller y.

// The one import here: a stacked band's vertical inset is a property of the
// MARK -- half the edge it draws -- rather than a number a caller picks, so
// it lives with the other mark weights. size.ts imports nothing, no cycle.
import { BAND_Y_PAD } from "./size";

function round1(n: number): number {
  return Math.round(n * 10) / 10;
}

function scaleX(i: number, n: number, w: number, pad: number): number {
  if (n <= 1) return pad;
  return pad + (i / (n - 1)) * (w - 2 * pad);
}

// Inverted on purpose: SVG y grows downward, data value grows upward.
//
// The `max === min` guard centers a degenerate range at mid-height. That
// is a defensible choice for `linePath`, where a flat *line* of identical
// non-zero values reads fine centred. It is the wrong choice for a
// *stack* (see `stackY` below): a stack's zero has a fixed, meaningful
// position -- the baseline -- so `stackBands` deliberately does not route
// through this function's degenerate branch.
function scaleY(
  v: number,
  min: number,
  max: number,
  h: number,
  pad: number,
): number {
  if (max === min) return h / 2;
  const t = (v - min) / (max - min);
  return pad + (1 - t) * (h - 2 * pad);
}

// Value-to-y mapping for stacked bands only. An all-zero stack (`max === 0`,
// since `max` is the largest running total across all points) must sit on
// the baseline, not float at mid-chart -- a host genuinely idling at 0 %
// drawing a band at h/2 would read as "about half loaded", which is worse
// than drawing nothing. When `max` is positive, `v = 0` already maps to the
// baseline through the normal formula (t = 0 -> y = h - pad), so this only
// special-cases the fully-degenerate case that `scaleY` would otherwise
// mishandle.
function stackY(v: number, max: number, h: number, pad: number): number {
  if (max === 0) return h - pad;
  return pad + (1 - v / max) * (h - 2 * pad);
}

function point(x: number, y: number): string {
  return `${round1(x)},${round1(y)}`;
}

/**
 * Splits indices `0..n-1` into runs of consecutive indices for which
 * `isGap(i)` is false, breaking a run wherever `isGap(i)` is true. Every
 * function below that must render a null (or, for `stackBands`, an index
 * where ANY series is null) as a break rather than a bridge shares this one
 * run-splitting rule instead of each re-implementing its own.
 */
function splitRuns(n: number, isGap: (i: number) => boolean): number[][] {
  const runs: number[][] = [];
  let run: number[] = [];
  for (let i = 0; i < n; i++) {
    if (isGap(i)) {
      if (run.length > 0) runs.push(run);
      run = [];
    } else {
      run.push(i);
    }
  }
  if (run.length > 0) runs.push(run);
  return runs;
}

/**
 * The min/max of a value series, ignoring nulls -- the extent `linePath`'s
 * `min`/`max` parameters need. Exists because the obvious inline
 * alternative, `Math.min(...vals)` / `Math.max(...vals)`, is a footgun:
 * `Math.min` and `Math.max` coerce every argument with `Number()`, and
 * `Number(null) === 0`, so `Math.min(...[5, null, 3])` silently returns `0`
 * -- a null becomes the floor of the y-scale instead of being ignored.
 *
 * An empty array or an all-null array has no real extent to report; both
 * return `{ min: 0, max: 0 }` rather than `{ Infinity, -Infinity }` (which
 * would poison any arithmetic a caller does with the result) or throwing
 * (which would make every chart's empty/loading state a special case for
 * its caller). `{ min: 0, max: 0 }` feeds straight into `scaleY`'s
 * `max === min` branch, which centers the (nonexistent) line at mid-height
 * -- the same degenerate handling a genuinely flat series gets.
 */
export function extent(vals: (number | null)[]): { min: number; max: number } {
  let min = Infinity;
  let max = -Infinity;
  for (const v of vals) {
    if (v === null) continue;
    if (v < min) min = v;
    if (v > max) max = v;
  }
  if (!Number.isFinite(min)) return { min: 0, max: 0 };
  return { min, max };
}

/**
 * Builds one subpath per unbroken run of two or more non-null values, plus
 * the coordinates of every run of exactly one non-null value surrounded by
 * nulls (or by the ends of the array). A `null` means the monitored host
 * reported nothing at that moment -- the chart must show a hole there, not
 * interpolate across it, or "the agent was down" silently becomes "the
 * metric was steady".
 *
 * A run of length one cannot be drawn as a line segment, but it is real
 * data and must not vanish: it is returned in `points` so the component can
 * render it as a dot. A host reporting on a 60 s cadence that flaps -- up,
 * down, up, down -- would otherwise produce all-length-one runs and an
 * empty `paths` array: data present, nothing drawn.
 */
export function linePath(
  vals: (number | null)[],
  w: number,
  h: number,
  min: number,
  max: number,
  pad = 0,
): { paths: string[]; points: { x: number; y: number }[] } {
  const n = vals.length;
  const runs = splitRuns(n, (i) => vals[i] === null);
  const paths: string[] = [];
  const points: { x: number; y: number }[] = [];

  for (const run of runs) {
    if (run.length >= 2) {
      const d = run
        .map((i, k) => {
          const x = scaleX(i, n, w, pad);
          const y = scaleY(vals[i] as number, min, max, h, pad);
          return `${k === 0 ? "M" : "L"}${point(x, y)}`;
        })
        .join(" ");
      paths.push(d);
    } else {
      const i = run[0]!;
      points.push({
        x: round1(scaleX(i, n, w, pad)),
        y: round1(scaleY(vals[i] as number, min, max, h, pad)),
      });
    }
  }

  return { paths, points };
}

/**
 * Closes each `linePath` subpath into a filled area by dropping straight
 * down to the chart's baseline at both ends and closing the shape. Takes
 * (and returns) one entry per subpath rather than a single string: the
 * obvious alternative, joining `linePath`'s subpaths into one string first
 * (`areaPath(linePath(...).paths.join(" "), ...)`), makes the coordinate
 * scan below see one continuous run of points spanning the gap and fill
 * straight across it -- exactly the hole `linePath` splits the data to
 * preserve. One fill per subpath keeps each gap a gap.
 *
 * The baseline is `h - pad`, matching the bottom of the plot box that
 * `scaleY`/`stackY` actually draw within (`pad` to `h - pad`), not `h`
 * itself -- closing to `h` would overflow the plot box by `pad` for any
 * `pad > 0`.
 *
 * `w` is unused and kept only to match the brief's signature that later
 * tasks build against. Do not "fix" that by closing the area to `0`/`w`:
 * this function closes to each subpath's own first/last x on purpose, so a
 * gap between subpaths stays a gap instead of being bridged by the fill.
 */
export function areaPath(
  subs: string[],
  w: number,
  h: number,
  pad = 0,
): string[] {
  const base = round1(h - pad);
  return subs.map((sub) => {
    const coords = sub.match(/-?\d+\.?\d*,-?\d+\.?\d*/g) ?? [];
    if (coords.length === 0) return "";

    const first = coords[0]!.split(",");
    const last = coords[coords.length - 1]!.split(",");
    const x0 = first[0];
    const xN = last[0];

    return `${sub} L${xN},${base} L${x0},${base} Z`;
  });
}

/**
 * Cumulative stacked bands: band `k` is the ribbon between the running
 * total through series `k - 1` and the running total through series `k`,
 * so the topmost band's upper edge traces the sum of every series -- the
 * silhouette reads as the total, not just the top layer.
 *
 * `series[s][i]` may be `null` -- one series' host reported nothing at
 * index `i`. When that happens the running total at `i` is undefined for
 * EVERY band, not just series `s`'s own, so the break applies to all of
 * them: an index is a gap if ANY series is null there. The obvious
 * alternative, `v ?? 0`, would fabricate that series as "reporting zero"
 * and draw a band as if it were really idle. A band's path is built as one
 * `M...Z` segment per unbroken run and the segments are concatenated with a
 * space, which SVG renders as visually disconnected regions within a
 * single `<path>` -- the standard way to put more than one hole in one
 * fill. A run of exactly one point is dropped, matching `linePath`: a
 * single point cannot bound a filled polygon.
 */
export function stackBands(
  series: (number | null)[][],
  w: number,
  h: number,
  max: number,
  /* Horizontal inset only; the vertical one is `yPad` below. */
  pad = 0,
  /* The VERTICAL inset, defaulting to half a band's own edge.
   *
   * `pad` exists so a LINE's stroke does not clip at the box edge, and it is
   * two pixels because that is what a line needs. A stacked band is a filled
   * region carrying a BAND_STROKE_WIDTH outline, so half that stroke is all
   * the headroom it can use; the rest is box the data can never reach. Spent
   * at both ends of a 32px fleet cell it was an eighth of the chart -- on a
   * CPU column whose hosts idle in single-digit percent, an eighth of what
   * little signal there is.
   *
   * The same reasoning mirrorPaths already applies by spending the whole
   * height: its mark carries no stroke at cell density, so it spends
   * everything. Horizontal padding is untouched; the time axis is unmoved.
   *
   * Overridable so a caller that draws no edge, and the tests that pin the
   * degenerate baseline cases, can ask for none. */
  yPad = BAND_Y_PAD,
): string[] {
  if (series.length === 0) return [];
  // The longest series, not series[0]'s: rows arrive ragged. querySeries
  // returns one row per series, and a host that started reporting late -- or
  // a point-limit truncation landing mid-series -- leaves some of them
  // shorter. Measuring off the first row alone read `undefined` past a short
  // series' end, which is neither null nor a number: it slipped through the
  // gap test, scaled to NaN, and erased that band from the chart entirely.
  const n = series.reduce((longest, s) => Math.max(longest, s.length), 0);
  if (n === 0) return [];

  // `== null` and not `=== null`, for the same reason: past a shorter
  // series' end there is no value at all, and a missing value is exactly as
  // unstackable as an explicit null.
  const runs = splitRuns(n, (i) => series.some((s) => s[i] == null)).filter(
    (run) => run.length >= 2,
  );

  const bands: string[] = series.map(() => "");

  for (const run of runs) {
    const prefix: number[][] = [];
    let running: number[] = new Array(run.length).fill(0);
    prefix.push([...running]);
    for (const s of series) {
      running = running.map((v, j) => v + (s[run[j]!] as number));
      prefix.push([...running]);
    }

    for (let k = 0; k < series.length; k++) {
      const bottom = prefix[k]!;
      const top = prefix[k + 1]!;

      const topPts = top.map((v, j) =>
        point(scaleX(run[j]!, n, w, pad), stackY(v, max, h, yPad)),
      );
      const bottomPts = bottom.map((v, j) =>
        point(scaleX(run[j]!, n, w, pad), stackY(v, max, h, yPad)),
      );

      const d =
        `M${topPts[0]} ` +
        topPts
          .slice(1)
          .map((p) => `L${p}`)
          .join(" ") +
        ` ${bottomPts
          .slice()
          .reverse()
          .map((p) => `L${p}`)
          .join(" ")} Z`;

      bands[k] = bands[k] ? `${bands[k]} ${d}` : d;
    }
  }

  return bands;
}

/**
 * Mirrors two series around a shared midline -- typical for paired up/down
 * traffic sparklines -- so `up` fills the top half growing upward from
 * `mid` and `down` fills the bottom half growing downward from `mid`, both
 * scaled against the same `max` for a visually comparable pair.
 *
 * `up`/`down` may contain `null` -- widened to match `seriesValues()`'s
 * return type, since a mirrored traffic chart is fed by the same
 * null-bearing wire data as every other chart. A null breaks that side's
 * fill the same way `stackBands` breaks a band: one `M...Z` segment per
 * unbroken run of two or more points, concatenated, so a gap on one side
 * does not force a gap on the other. A run of exactly one point is dropped;
 * a single point cannot bound a filled area.
 *
 * Out-of-range values (`v > max`) are left to escape the plot box rather
 * than being clamped to it with `Math.min(v, max)`. This intentionally
 * matches `linePath`, which never clamps: silently capping a value at the
 * plot edge hides the fact that it exceeded what the axis was scaled for,
 * which is worse than a line that visibly overflows the box -- the overflow
 * is itself informative. (Previously this function clamped and `linePath`
 * did not; that inconsistency is resolved here in `linePath`'s favour.)
 */
/**
 * A bar's height, at SUB-PIXEL precision.
 *
 * Not rounded to a whole pixel. Whole pixels were argued for here on an ink
 * measurement "against rrdtool rendering the identical numbers" -- but that
 * rrdtool render was one this repo built for itself, not the reference. The
 * operator's actual Observium sparkline carries 110 distinct series colours
 * over 173 columns; this cell, rounding, carried 24. Those extra hundred are
 * partial-coverage tops: a 3.4px reading draws three solid rows and a fourth
 * at 40 %.
 *
 * That is the whole difference between the two pictures. Rounding leaves a
 * 14px half exactly fifteen possible heights, so neighbouring buckets that
 * differ by a fifth of a pixel draw identically and every bar ends in a hard
 * flat edge -- the "stubby tower". A fractional height ends in an antialiased
 * row instead, which both restores the variation and lets a spike taper to a
 * point.
 *
 * There is deliberately NO floor either. It used to read `Math.max(1, ...)`,
 * so that a reading which exists is a reading you can see -- which sounds
 * right and is what makes the quiet stretch of a bursty host a dead straight
 * line. A traffic floor is routinely a thousandth of its ceiling, so on such
 * a host EVERY quiet column clamped to exactly one pixel and the cell said
 * the same thing 149 times. rrdtool lets those columns fall where they land,
 * and the variation IS the detail.
 */
function barHeight(px: number, value: number): number {
  if (value === 0) return 0;
  return px;
}

/**
 * The two halves' peaks, by the same even-up / odd-down convention the
 * mirrored marks use and with the same stacking rule.
 *
 * Exported so a panel can build its tick ladder from the numbers its marks
 * will actually be drawn against, rather than from a single shared `max`.
 */
export function mirrorPeaks(
  seriesValues: readonly (number | null)[][],
  stacked: boolean,
): { up: number; down: number } {
  const peak = (rows: readonly (number | null)[][]): number => {
    if (rows.length === 0) return 0;
    const n = rows.reduce((longest, r) => Math.max(longest, r.length), 0);
    let m = 0;
    for (let i = 0; i < n; i++) {
      // Stacked, the outer edge is the running total; overlaid, it is the
      // tallest single layer.
      let v = 0;
      for (const row of rows) {
        const x = row[i];
        if (x == null) continue;
        if (stacked) v += x;
        else if (x > v) v = x;
      }
      if (v > m) m = v;
    }
    return m;
  };
  return {
    up: peak(seriesValues.filter((_, i) => i % 2 === 0)),
    down: peak(seriesValues.filter((_, i) => i % 2 === 1)),
  };
}

/**
 * The ceilings a mirrored chart's two halves are drawn against, and where
 * zero falls between them.
 *
 * ONE computation, exported because three things have to agree about it: the
 * plain mirror, the stacked mirror, and the tick ladder a panel hangs beside
 * them. They did not agree before. `mirrorPaths` derived an asymmetric scale
 * from the data whenever no ladder was present, and a panel WITH a ladder
 * got the symmetric fallback instead -- both halves measured against the
 * larger peak, zero pinned to mid-height. On a host pulling four times what
 * it pushes that spends four fifths of the outbound range on empty box, and
 * it is why the fleet cell and the Traffic panel of the same host disagreed
 * about how thick a quiet band is.
 *
 * RRDtool has no such split: one linear axis over [-down, +up], with zero
 * wherever that puts it and the labels generated from the same range. An
 * operator's own graph reads 500k at the top and -100k at the bottom for
 * exactly this reason.
 *
 * There is no opting out, and there used to be. A flag let a caller ask for
 * both halves against one ceiling with zero at mid-height, and it was wired
 * to `y === undefined` (a chart with a tick ladder) and then to
 * `min === undefined` (a caller who pinned the range). Both were wrong, the
 * second invisibly: ChartPanel passes min 0 for a stack and ChartFigure's
 * min defaults to 0, so EVERY mirrored panel and enlarged view in the app
 * took the shared ceiling while its ladder -- built from this function, with
 * no such condition -- was asymmetric. They drew a centred midline under an
 * axis whose "0" sat a fifth of the way up the box. Nothing wants a mirror
 * scaled any other way, so the choice is gone rather than corrected.
 */
export function mirrorCeilings(
  upPeak: number,
  downPeak: number,
): { up: number; down: number; zero: number } {
  /* NO headroom. The range is the data's own, and the peak touches the edge.
   *
   * This used to add rrdtool's `expand_range()` padding -- a tenth of the
   * combined range at each end -- ported from the ALTAUTOSCALE branch after
   * reading it. Reading the branch was not enough; the CALL had to be read
   * too. rrd_graph.c:4042:
   *
   *     if ((!im->rigid || im->allow_shrink) && !im->logarithmic)
   *         expand_range(im);
   *
   * Observium's common.inc.php appends `--alt-autoscale --rigid` and never
   * `--allow-shrink`, so the reference never calls expand_range at all. The
   * padding widened the span by a fifth and so drew every mark 20 % shorter
   * than the graph it was copying -- which is what an operator reported by
   * eye, independently, as "still missing about 20 %".
   */
  const span = upPeak + downPeak;
  return {
    up: upPeak,
    down: downPeak,
    zero: span === 0 ? 0.5 : upPeak / span,
  };
}

export function mirrorPaths(
  up: (number | null)[],
  down: (number | null)[],
  w: number,
  h: number,
  max: number,
  pad = 0,
  /* The ceilings to scale against, when the caller has already derived them.
     A pair drawn WITH a peak envelope is two calls -- the envelope and the
     mean inside it -- handed different values, so left to derive their own
     they would place their zero lines at different heights and the envelope
     would neither contain the mean nor share its midline. The caller derives
     one pair from the peak, the tallest thing on the chart, and hands the
     same pair to both. It is also what the tick ladder is built from, so the
     labels name the heights the marks are actually drawn at. */
  ceiling?: { up: number; down: number },
): { up: string; down: string; mid: number } {
  const n = Math.max(up.length, down.length);

  /**
   * The ceiling each half is drawn against, and how much room it gets.
   *
   * This is RRDtool's model, and it is why an Observium sparkline's peaks
   * reach the edge of the cell where ours stopped well short of it.
   * Two things were costing height:
   *
   *  - `pad`. It exists so a LINE's stroke does not clip at the box edge. A
   *    bar has no stroke, so on this mark it is four pixels of a
   *    thirty-two-pixel cell thrown away for nothing.
   *  - one ceiling for both halves. Measured on a normal host, in peaks at
   *    5.9 MB/s against out's 7.3: dividing both by 7.3 means the inbound
   *    half can never reach higher than eleven of its fourteen pixels, no
   *    matter what the host does.
   *
   * Drawn against the COMBINED range with the zero line placed inside the box
   * rather than pinned to its middle, each direction fills the cell -- 14 and
   * 18 px of a 32 px cell for a host peaking at 5.9 in against 7.3 out,
   * instead of 11.3 and 14. Both halves still divide by the same number, so a
   * taller bar means more bytes whichever way it points.
   *
   * What it costs is the comparison BETWEEN CELLS: the midline sits at a
   * different height in each one, so a reader cannot lay two rows side by
   * side and read the zero off the same row. RRDtool accepts that trade on a
   * sparkline and so do we, but only where nothing on screen claims
   * otherwise, which is why Chart turns this on for a chart with no value
   * ladder and leaves a chart that has one on the shared ceiling its ticks
   * are labelled from.
   */
  // The ceiling a half is drawn against: the window's own peak.
  //
  // A percentile ceiling was tried three times and is not here, which is
  // worth recording so it is not tried a fourth. The reference does not clip:
  // Observium runs rrdtool with `--alt-autoscale --rigid` (common.inc.php,
  // the branch taken when no explicit scale is set), which pads the data's
  // own range and never truncates it.
  //
  // It does not need to, and netra arguably does: measured on one real host,
  // inbound median 545 B/s with three isolated FIVE-MINUTE buckets at about
  // 100 kB/s -- 190x the median, on round hours, ordinary neighbours either
  // side. Scaled to one of those the rest of the day shares a pixel. But the
  // clip that fixes it is a dial with no defensible setting: p98 leaves the
  // cell nearly as flat, p90 fills it with a solid block, and the right value
  // differs per host. Picking one by eye is how this file acquired, and then
  // lost, a one-pixel floor.
  const halfMax = (vals: (number | null)[]): number => {
    let m = 0;
    for (const v of vals) if (v !== null && v > m) m = v;
    return m;
  };

  // Padded the way `--alt-autoscale` pads, but only where the scale is
  // derived here. A chart with a tick ladder gets `max` from its caller and
  // its labels are drawn from that number: widening it underneath would put
  // every mark at a height its own axis denies.
  //
  // The padding is shared between the halves rather than added to each, which
  // is what rrdtool does -- it moves minval and maxval apart by `adj` each,
  // and both ends of one range.
  const raw = ceiling ?? { up: halfMax(up), down: halfMax(down) };
  const scale = mirrorCeilings(raw.up, raw.down);
  const upMax = scale.up;
  const downMax = scale.down;
  const span = upMax + downMax;
  // Where zero sits. Centred when nothing is drawn, or when the caller is on
  // the shared ceiling; otherwise placed so the combined range fills the box.
  //
  // Snapped to a whole pixel, because the bars measured from it are a whole
  // number of pixels tall. Left at its exact position -- 19.3 on a real host
  // -- every bar in the cell would begin and end on a third of a pixel, and
  // each one would be antialiased across two rows instead of filling one.
  //
  // And never on the box edge while the half above or below it has something
  // to draw. A host pulling a hundredth of what it pushes puts the exact zero
  // at 0.3, which rounds to 0 -- and any inbound bar that does reach a whole
  // pixel is then drawn from row 0 to row -1, outside the viewport. Each
  // direction that has a reading keeps one row to draw it in.
  const placeZero = (): number => {
    if (span === 0) return h / 2;
    let z = Math.round((h * upMax) / span);
    if (upMax > 0) z = Math.max(z, 1);
    if (downMax > 0) z = Math.min(z, h - 1);
    return z;
  };
  const zero = placeZero();
  const baseline = round1(zero);
  // `room` is how many rows the direction actually has between the zero line
  // and the edge of the box. A bar longer than its room is pure rounding:
  // h*up/span at exactly x.5 rounds the zero line one way and the opposite
  // half's height the other, and the tallest bar loses its last row off the
  // bottom of the cell.
  const scaleOf = (direction: 1 | -1) => ({
    // One linear axis: a value maps through the COMBINED span over the whole
    // box, so the same magnitude is the same height above the line as below
    // it. `room` is only how far each half can reach before it runs out.
    ceiling: span,
    usable: h,
    room: direction === -1 ? zero : h - zero,
  });

  // Columns TILED across the full width, edge to edge, rather than centred on
  // scaleX's positions.
  //
  // scaleX insets by `pad` at both ends, so 150 readings in a 150px cell get
  // 166px of span and every bar straddles two pixel columns -- a smear at
  // partial alpha instead of a mark. Tiled, a fold to the plot's own width
  // lands each bar on an exact pixel boundary, which is what lets it be
  // saturated rather than grey.
  //
  // The cost is that a bar's centre is up to half a column from where the
  // crosshair puts its dot. Half a column is half a pixel on a fleet cell,
  // and on a chart wide enough for that to be visible the bars are wide
  // enough that the dot still lands inside its own bar.
  const columnWidth = n > 0 ? w / n : w;

  const build = (vals: (number | null)[], direction: 1 | -1): string => {
    const { ceiling, usable, room } = scaleOf(direction);
    // A run of ONE is a run. The polyline this used to draw needed two points
    // to be a line and a lone reading between two holes was dropped; a bar is
    // a rectangle over its own column and draws perfectly well alone. A host
    // that reported in a single column of the window -- one bucket surviving
    // reduceToColumns' fold, the rest of the day silent -- drew an empty cell
    // that said it had reported nothing at all.
    const runs = splitRuns(vals.length, (i) => vals[i] === null);

    return runs
      .map((run) => {
        // One BAR per reading, midline to value, rather than a polyline
        // through the readings with the area filled under it.
        //
        // This is the whole difference between our sparkline and the RRDtool
        // graph it is meant to look like, and it is not a small one. A
        // polyline joins the top of each column to the top of its neighbours
        // with a diagonal, so two adjacent buckets merge into one slope and a
        // run of them reads as a single smooth mass. Per-column bars keep
        // every bucket separate: the picket fence an operator can count. Same
        // numbers, same size, same colours -- the readings are simply legible
        // in one and not the other. rrd_graph.c draws AREA exactly this way.
        //
        // Measured the wrong way first: bars lay down about 7 % LESS ink than
        // the polyline, and that was taken as evidence against them. Ink is
        // not detail. Nobody was asking for more colour on the cell, they
        // were asking to be able to tell one five-minute bucket from the
        // next.
        const edges: string[] = [];
        for (const i of run) {
          const v = vals[i] as number;
          const t = ceiling === 0 ? 0 : v / ceiling;
          const y = zero + direction * Math.min(barHeight(t * usable, v), room);
          edges.push(
            point(i * columnWidth, y),
            point((i + 1) * columnWidth, y),
          );
        }
        const first = edges[0]!.split(",")[0];
        const last = edges[edges.length - 1]!.split(",")[0];
        return (
          `M${first},${baseline} ` +
          edges.map((p) => `L${p}`).join(" ") +
          ` L${last},${baseline} Z`
        );
      })
      .join(" ");
  };

  return {
    up: build(up, -1),
    down: build(down, 1),
    mid: zero,
  };
}

/**
 * A mirrored STACK: `up` layers accumulate upward from the midline, `down`
 * layers accumulate downward from it, both against one shared `max`.
 *
 * This is the shape a per-interface traffic chart wants and neither
 * `stackBands` nor `mirrorPaths` can draw. `mirrorPaths` overlays its pairs,
 * so four interfaces hide each other and the silhouette is whichever one is
 * loudest rather than the host's total; `stackBands` totals correctly but has
 * one baseline, so in and out would share a direction and the reader loses
 * the one comparison a traffic chart exists to make. Stacked about a midline,
 * the outer envelope IS the host's total in each direction -- which is what
 * the fleet row cell draws as a single summed pair, so the cell and the chart
 * it opens agree about the same host.
 *
 * The two halves keep their own gaps. Within a half an index is a gap if ANY
 * of that half's series is null there, exactly as `stackBands` argues: the
 * running total at that index is undefined for every layer, and `v ?? 0`
 * would draw an interface that reported nothing as one reporting zero. A gap
 * on the up side does not force one on the down side.
 *
 * Values above `max` are left to overflow, matching `mirrorPaths` and
 * `linePath`: the overflow is itself informative.
 */
export function mirrorStackBands(
  up: (number | null)[][],
  down: (number | null)[][],
  w: number,
  h: number,
  pad = 0,
): { up: string[]; down: string[]; mid: number } {
  // The stack's own totals, which is what the outer edge of each half is
  // drawn at -- not any one layer's peak.
  const stackMax = (series: (number | null)[][]): number => {
    const n = series.reduce((longest, s) => Math.max(longest, s.length), 0);
    let m = 0;
    for (let i = 0; i < n; i++) {
      let sum = 0;
      for (const s of series) {
        const v = s[i];
        if (v != null) sum += v;
      }
      if (sum > m) m = sum;
    }
    return m;
  };

  /* One linear axis over [-out_max, +in_max], with zero wherever that puts
     it -- rrdtool's `--alt-autoscale`, and what the fleet cell already does
     through mirrorPaths.

     Centring zero and measuring BOTH halves against the larger one is what
     this did before, and on a host that pulls four times what it pushes it
     throws away four fifths of the outbound range: zen's out peak is 24.6k
     against an in peak of 101.6k, so the out half got 105 rows to draw 24.6k
     in and its 4k baseline landed on 4 pixels. On the reference's own axis
     the same baseline is 11. The band was not thin because of the mark, it
     was thin because three quarters of the box below the midline could never
     be reached.

     This is also what makes the panel and the fleet cell the same chart at
     two sizes rather than two charts that happen to share colours. */
  const raw = { up: stackMax(up), down: stackMax(down) };
  const scale = mirrorCeilings(raw.up, raw.down);
  const upMax = scale.up;
  const downMax = scale.down;
  const span = upMax + downMax;

  // Whole-pixel, and never on the box edge while the half beyond it has
  // something to draw -- the same rule mirrorPaths' placeZero() follows.
  const placeZero = (): number => {
    if (span === 0) return h / 2;
    let z = Math.round((h * upMax) / span);
    if (upMax > 0) z = Math.max(z, 1);
    if (downMax > 0) z = Math.min(z, h - 1);
    return z;
  };
  const mid = placeZero();

  const half = (series: (number | null)[][], direction: 1 | -1): string[] => {
    if (series.length === 0) return [];
    // The longest series, not series[0]'s -- rows arrive ragged, and the same
    // trap stackBands documents applies here.
    const n = series.reduce((longest, s) => Math.max(longest, s.length), 0);
    if (n === 0) return series.map(() => "");

    const runs = splitRuns(n, (i) => series.some((s) => s[i] == null)).filter(
      (run) => run.length >= 2,
    );

    // Each half against its OWN ceiling and its own room, which is the whole
    // point of the placement above. Applied to the running TOTAL rather than
    // to each layer's own value: the total is what the edge is drawn at.
    const ceiling = direction === -1 ? upMax : downMax;
    const room = direction === -1 ? mid : h - mid;
    const usable = room;
    // Clamped only where the scale is DERIVED here, exactly as mirrorPaths
    // does: there a bar longer than its room is pure rounding. On a
    // caller-supplied ceiling a total above `max` is a real reading and is
    // left to escape the box.
    const y = (v: number): number => {
      const px = barHeight((ceiling === 0 ? 0 : v / ceiling) * usable, v);
      return mid + direction * Math.min(px, room);
    };

    const bands: string[] = series.map(() => "");
    for (const run of runs) {
      const prefix: number[][] = [];
      let running: number[] = new Array(run.length).fill(0);
      prefix.push([...running]);
      for (const s of series) {
        running = running.map((v, j) => v + (s[run[j]!] as number));
        prefix.push([...running]);
      }

      // A POLYLINE through the bucket tops, deliberately, and NOT the
      // column-per-bucket staircase mirrorPaths' build() draws.
      //
      // Tried the staircase here on the theory that it was why the panel
      // inked less of the box than the reference. It was not: at the
      // reference's own plot size the ink went from 5.4 % to 5.5 % against
      // its 8.4 %, so the mark was never the cause -- the scale was, see
      // mirrorCeilings and the ceiling each half is given. What the staircase
      // did cost is the shape of a spike. At roughly four pixels per bucket a
      // column is a flat-topped rectangle four wide, where a polyline rises
      // and falls across its neighbours and tapers to a point. The reference
      // draws needles, and so does this.
      //
      // The cell is the other way round for a reason that does not apply
      // here: at 170 px it folds many buckets into one pixel column, and
      // there a diagonal between two columns merges adjacent buckets into a
      // single smooth mass. It has no room to taper; this chart does.
      for (let k = 0; k < series.length; k++) {
        const bottom = prefix[k]!;
        const top = prefix[k + 1]!;
        const topPts = top.map((v, j) =>
          point(scaleX(run[j]!, n, w, pad), y(v)),
        );
        const bottomPts = bottom.map((v, j) =>
          point(scaleX(run[j]!, n, w, pad), y(v)),
        );
        const d =
          `M${topPts[0]} ` +
          topPts
            .slice(1)
            .map((pt) => `L${pt}`)
            .join(" ") +
          ` ${bottomPts
            .slice()
            .reverse()
            .map((pt) => `L${pt}`)
            .join(" ")} Z`;
        bands[k] = bands[k] ? `${bands[k]} ${d}` : d;
      }
    }
    return bands;
  };

  return { up: half(up, -1), down: half(down, 1), mid };
}

/**
 * An isolated point -- a run of exactly one non-null value, surrounded by
 * gaps -- drawn as a two-arc circle PATH rather than an SVG <circle>. Both
 * chart components render these, and both tag them `data-line data-point`,
 * so a caller counting "how many runs did this gap produce" via
 * `path[data-line]` sees the isolated point counted alongside real segments,
 * the way the run-splitting docs above describe.
 *
 * It lives here rather than in either component because it produces
 * coordinates, which is what this module is: two byte-identical copies in
 * Sparkline and Overlay would drift the first time the radius or the
 * isolated-point strategy changes.
 */
export function dotPath(x: number, y: number, r = 1.5): string {
  return `M${x - r},${y} A${r},${r} 0 1,0 ${x + r},${y} A${r},${r} 0 1,0 ${x - r},${y} Z`;
}
