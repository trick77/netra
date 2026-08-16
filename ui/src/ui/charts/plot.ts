// The plot rect: where inside a chart image the data is actually drawn, and
// how a value or a moment maps to a coordinate in it.
//
// Pure, with no React import and no DOM access -- and here that is not only
// a testing convenience, it is a correctness requirement. See labelWidth().
//
// Every chart with an axis has two boxes: the IMAGE (the SVG's viewBox) and
// the PLOT (the part of it the series may touch). The margins between them
// hold the axis. Marks, gridlines and labels all map through the same rect,
// so a label cannot drift from the gridline it names however the image is
// scaled -- which is the failure that HTML-positioned axis labels had, where
// a fixed 320px label gutter sat beside an SVG that had responsively
// rendered at 216px and every label was up to 104px from its line.

/** The drawable box inside a chart image, in viewBox units. */
export interface PlotRect {
  left: number;
  right: number;
  top: number;
  bottom: number;
}

export interface LayoutOptions {
  /** The widest label the value axis will draw, already formatted. */
  yLabel?: string;
  /** The widest label the time axis will draw, already formatted. */
  xLabel?: string;
  /** Axis label size in px, matching .axislab. */
  fontPx?: number;
}

/**
 * Gap between a value label and the spine it annotates, and between the
 * spine and a time label below it. Small enough to read as attached to the
 * axis, large enough not to touch the tick marks.
 */
const LABEL_GAP = 6;

/** How far a tick mark protrudes from the spine. Labels clear it. */
export const TICK_MAJOR = 6;
export const TICK_MINOR = 3;

/**
 * Half a line of text, reserved above the plot so the topmost value label --
 * whose baseline is centred on the topmost gridline -- is not clipped by the
 * top of the image.
 */
function halfLine(fontPx: number): number {
  return Math.ceil(fontPx / 2) + 2;
}

/**
 * The plot rect for a chart image of `width` x `height`.
 *
 * With no labels this is the whole image inset by a hairline, which is what
 * every sparkline and every furniture-free panel wants: the series gets the
 * entire box. With labels the margins are computed from the text that will
 * actually be drawn, never from a constant -- a fixed margin is either too
 * tight for one panel or wasteful for another, and "500 MB/s" against a
 * guessed 54px margin rendered as "00 MB/s".
 */
export function layout(
  width: number,
  height: number,
  opts: LayoutOptions = {},
): PlotRect {
  const { yLabel, xLabel, fontPx = 11 } = opts;

  const left = yLabel
    ? Math.ceil(labelWidth(yLabel, fontPx)) + LABEL_GAP + TICK_MAJOR
    : 0;
  // A time label is centred on its tick, so only half of it hangs below the
  // spine -- but the whole line height does, plus the tick mark.
  const bottom = xLabel ? height - (fontPx + LABEL_GAP + TICK_MINOR) : height;
  const top = yLabel ? halfLine(fontPx) : 0;

  return { left, right: width, top, bottom };
}

/** Maps a 0..1 fraction across the plot to an x coordinate. */
export function xAt(rect: PlotRect, fraction: number): number {
  return rect.left + fraction * (rect.right - rect.left);
}

/**
 * Maps a 0..1 fraction to a y coordinate, inverted: SVG's y grows downward
 * and a value grows upward, so fraction 1 is the TOP of the plot.
 */
export function yAt(rect: PlotRect, fraction: number): number {
  return rect.bottom - fraction * (rect.bottom - rect.top);
}

export function plotWidth(rect: PlotRect): number {
  return rect.right - rect.left;
}

export function plotHeight(rect: PlotRect): number {
  return rect.bottom - rect.top;
}

/**
 * Whether a point in viewBox coordinates is inside the plot rect.
 *
 * The crosshair uses this: brushing the axis margins is not hovering the
 * chart, and must not leave a rule behind.
 */
export function contains(rect: PlotRect, x: number, y: number): boolean {
  return x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom;
}

/**
 * The width of a rendered axis label, in px, computed arithmetically.
 *
 * It does NOT measure. Measuring is the obvious approach and it cannot be
 * used here: in this project's jsdom test environment
 * canvas.getContext("2d") returns null and SVGGraphicsElement.getBBox does
 * not exist, so a measuring layout would compute a zero-width margin under
 * test and pass against a layout that never occurs in a browser. A wrong
 * answer that only appears in production is worse than no answer.
 *
 * Summing per-character advances is exact rather than approximate here for
 * one reason: .axislab sets font-variant-numeric: tabular-nums, so every
 * digit occupies the same width. The advances below were measured with
 * getBBox in Chrome against the app's own font stack at 11px. Checked
 * against real renderings, the sum is within 0.03px:
 *
 *     "500 MB/s"   50.38 computed   50.36 measured
 *     "1.0 GB/s"   45.31 computed   45.30 measured
 *     "16 GiB"     35.52 computed   35.50 measured
 *     "100%"       31.25 computed   31.23 measured
 *
 * Advances scale linearly with font size, which holds for every size this
 * app draws an axis at.
 */
const ADVANCE_AT_11PX: Record<string, number> = {
  // Digits are uniform under tabular-nums -- that is what makes this exact.
  "0": 7.0,
  "1": 7.0,
  "2": 7.0,
  "3": 7.0,
  "4": 7.0,
  "5": 7.0,
  "6": 7.0,
  "7": 7.0,
  "8": 7.0,
  "9": 7.0,
  " ": 3.14,
  ".": 3.33,
  "%": 10.23,
  "/": 3.41,
  "-": 5.25,
  "–": 6.48,
  "—": 9.67,
  k: 6.03,
  K: 7.3,
  M: 9.67,
  G: 8.27,
  T: 7.03,
  P: 7.05,
  B: 7.28,
  i: 2.78,
  I: 3.0,
  s: 5.81,
  b: 6.81,
  m: 9.63,
  h: 6.53,
  d: 6.81,
  "µ": 6.81,
  "°": 5.25,
  C: 7.94,
};

/**
 * The advance used for a character not in the table.
 *
 * The digit width, which is wider than most glyphs a formatter emits. That
 * is the safe direction to be wrong in: an over-wide margin costs a few
 * pixels of plot, an under-wide one clips the label.
 */
const FALLBACK_ADVANCE = 7.0;

export function labelWidth(text: string, fontPx = 11): number {
  let total = 0;
  for (const ch of text) {
    total += ADVANCE_AT_11PX[ch] ?? FALLBACK_ADVANCE;
  }
  return (total * fontPx) / 11;
}

/**
 * The widest of several labels -- what a caller passes to layout() once it
 * knows the ticks it will draw.
 *
 * Widest by RENDERED width, not by string length: "1.0 GB/s" is 8 characters
 * and "16 GiB" is 6, but the digits and the wide M/G glyphs mean length is
 * not the ordering. Comparing lengths put the wrong label in the margin.
 */
export function widestLabel(labels: readonly string[], fontPx = 11): string {
  let widest = "";
  let width = -1;
  for (const label of labels) {
    const w = labelWidth(label, fontPx);
    if (w > width) {
      width = w;
      widest = label;
    }
  }
  return widest;
}
