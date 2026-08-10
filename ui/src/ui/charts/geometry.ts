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

function point(x: number, y: number): string {
  return `${round1(x)},${round1(y)}`;
}

/**
 * Builds one subpath per unbroken run of non-null values. A `null` means
 * the monitored host reported nothing at that moment -- the chart must show
 * a hole there, not interpolate across it, or "the agent was down" silently
 * becomes "the metric was steady". A run of exactly one point is dropped
 * rather than emitted as a zero-length path; the component renders those as
 * a dot instead.
 */
export function linePath(
  vals: (number | null)[],
  w: number,
  h: number,
  min: number,
  max: number,
  pad = 0,
): string[] {
  const n = vals.length;
  const subs: string[] = [];
  let run: { i: number; v: number }[] = [];

  const flush = () => {
    if (run.length >= 2) {
      const d = run
        .map(({ i, v }, k) => {
          const x = scaleX(i, n, w, pad);
          const y = scaleY(v, min, max, h, pad);
          return `${k === 0 ? "M" : "L"}${point(x, y)}`;
        })
        .join(" ");
      subs.push(d);
    }
    run = [];
  };

  for (let i = 0; i < n; i++) {
    const v = vals[i];
    if (v === null) {
      flush();
    } else {
      run.push({ i, v });
    }
  }
  flush();

  return subs;
}

/**
 * Closes a `linePath` subpath into a filled area by dropping straight down
 * to the chart's bottom edge at both ends and closing the shape.
 */
export function areaPath(sub: string, w: number, h: number): string {
  const coords = sub.match(/-?\d+\.?\d*,-?\d+\.?\d*/g) ?? [];
  if (coords.length === 0) return "";

  const first = coords[0]!.split(",");
  const last = coords[coords.length - 1]!.split(",");
  const x0 = first[0];
  const xN = last[0];
  const base = round1(h);

  return `${sub} L${xN},${base} L${x0},${base} Z`;
}

/**
 * Cumulative stacked bands: band `k` is the ribbon between the running
 * total through series `k - 1` and the running total through series `k`,
 * so the topmost band's upper edge traces the sum of every series -- the
 * silhouette reads as the total, not just the top layer.
 */
export function stackBands(
  series: number[][],
  w: number,
  h: number,
  max: number,
  pad = 0,
): string[] {
  if (series.length === 0) return [];
  const n = series[0]?.length ?? 0;
  if (n === 0) return [];

  const prefix: number[][] = [];
  let running = new Array(n).fill(0);
  prefix.push([...running]);
  for (const s of series) {
    running = running.map((v, i) => v + (s[i] ?? 0));
    prefix.push([...running]);
  }

  const bands: string[] = [];
  for (let k = 0; k < series.length; k++) {
    const bottom = prefix[k];
    const top = prefix[k + 1];

    const topPts = top.map((v, i) =>
      point(scaleX(i, n, w, pad), scaleY(v, 0, max, h, pad)),
    );
    const bottomPts = bottom.map((v, i) =>
      point(scaleX(i, n, w, pad), scaleY(v, 0, max, h, pad)),
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
    bands.push(d);
  }

  return bands;
}

/**
 * Mirrors two non-negative series around a shared midline -- typical for
 * paired up/down traffic sparklines -- so `up` fills the top half growing
 * upward from `mid` and `down` fills the bottom half growing downward from
 * `mid`, both scaled against the same `max` for a visually comparable pair.
 */
export function mirrorPaths(
  up: number[],
  down: number[],
  w: number,
  h: number,
  max: number,
  pad = 0,
): { up: string; down: string; mid: number } {
  const mid = h / 2;
  const n = Math.max(up.length, down.length);

  const build = (vals: number[], direction: 1 | -1): string => {
    const usable = h / 2 - pad;
    const pts = vals.map((v, i) => {
      const x = scaleX(i, n, w, pad);
      const t = max === 0 ? 0 : Math.min(v, max) / max;
      const y = mid + direction * t * usable;
      return point(x, y);
    });
    if (pts.length === 0) return "";

    const first = pts[0].split(",")[0];
    const last = pts[pts.length - 1].split(",")[0];
    const baseline = round1(mid);

    return (
      `M${first},${baseline} ` +
      pts.map((p) => `L${p}`).join(" ") +
      ` L${last},${baseline} Z`
    );
  };

  return {
    up: build(up, -1),
    down: build(down, 1),
    mid,
  };
}
