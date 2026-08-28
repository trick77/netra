// Pure chart-geometry math, with no React import and no DOM access, so the
// one behaviour that matters most here -- a null in the data producing a
// hole in the line rather than a bridge across it -- can be pinned by a
// fast unit test instead of discovered later through a rendered chart.
//
// SVG's y-axis grows downward, so every value-to-y mapping below inverts
// the value: a higher value must land at a smaller y.

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
  pad = 0,
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
        point(scaleX(run[j]!, n, w, pad), stackY(v, max, h, pad)),
      );
      const bottomPts = bottom.map((v, j) =>
        point(scaleX(run[j]!, n, w, pad), stackY(v, max, h, pad)),
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
export function mirrorPaths(
  up: (number | null)[],
  down: (number | null)[],
  w: number,
  h: number,
  max: number,
  pad = 0,
): { up: string; down: string; mid: number } {
  const mid = h / 2;
  const n = Math.max(up.length, down.length);
  const baseline = round1(mid);

  const build = (vals: (number | null)[], direction: 1 | -1): string => {
    const usable = h / 2 - pad;
    const runs = splitRuns(vals.length, (i) => vals[i] === null).filter(
      (run) => run.length >= 2,
    );

    return runs
      .map((run) => {
        const pts = run.map((i) => {
          const x = scaleX(i, n, w, pad);
          const v = vals[i] as number;
          const t = max === 0 ? 0 : v / max;
          const y = mid + direction * t * usable;
          return point(x, y);
        });
        const first = pts[0]!.split(",")[0];
        const last = pts[pts.length - 1]!.split(",")[0];
        return (
          `M${first},${baseline} ` +
          pts.map((p) => `L${p}`).join(" ") +
          ` L${last},${baseline} Z`
        );
      })
      .join(" ");
  };

  return {
    up: build(up, -1),
    down: build(down, 1),
    mid,
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
  max: number,
  pad = 0,
): { up: string[]; down: string[]; mid: number } {
  const mid = h / 2;
  const usable = h / 2 - pad;

  const half = (series: (number | null)[][], direction: 1 | -1): string[] => {
    if (series.length === 0) return [];
    // The longest series, not series[0]'s -- rows arrive ragged, and the same
    // trap stackBands documents applies here.
    const n = series.reduce((longest, s) => Math.max(longest, s.length), 0);
    if (n === 0) return series.map(() => "");

    const runs = splitRuns(n, (i) => series.some((s) => s[i] == null)).filter(
      (run) => run.length >= 2,
    );

    const y = (v: number): number =>
      mid + direction * (max === 0 ? 0 : v / max) * usable;

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
