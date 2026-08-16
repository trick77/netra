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
  /** Axis label size in viewBox units. Defaults to AXIS_FONT_PX. */
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
  const { yLabel, xLabel, fontPx = AXIS_FONT_PX } = opts;

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
 * The size an axis label is drawn at, in viewBox units.
 *
 * The advance table below is measured AT this size, and the component sets
 * font-size from this constant rather than leaving it to CSS. That coupling
 * is deliberate: the left margin is computed from these numbers, so if the
 * rendered size and the measured size could drift apart the margin would be
 * silently wrong. Matches --text-micro, which is what the rest of the app
 * uses for a label this small.
 */
export const AXIS_FONT_PX = 12;

/**
 * The width of a rendered axis label, in viewBox units, computed
 * arithmetically.
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
 * digit occupies the same width. Without that the table would be a guess --
 * proportional digits differ by glyph, and the same label would measure
 * differently depending on which numbers were in it.
 *
 * The advances were measured with getBBox in Chrome against the app's own
 * font stack at AXIS_FONT_PX. Checked against real renderings the sum lands
 * within 0.03 units:
 *
 *     "500 MB/s"   54.35 computed   54.38 measured
 *     "1.0 GB/s"   48.82 computed   48.84 measured
 *     "16 GiB"     38.29 computed   38.31 measured
 *     "100%"       33.78 computed   33.78 measured
 *
 * They are NOT scaled from another size. Advances do not scale linearly --
 * hinting rounds them per size, and deriving 12px from an 11px measurement
 * put "500 MB/s" 0.56 units out, an error large enough to clip. A different
 * axis size would need its own measured table.
 */
const ADVANCE: Record<string, number> = {
  // Digits are uniform under tabular-nums -- that is what makes this exact.
  "0": 7.56,
  "1": 7.56,
  "2": 7.56,
  "3": 7.56,
  "4": 7.56,
  "5": 7.56,
  "6": 7.56,
  "7": 7.56,
  "8": 7.56,
  "9": 7.56,
  " ": 3.37,
  ".": 3.56,
  "%": 11.1,
  "/": 3.65,
  "-": 5.65,
  "\u2013": 7.0,
  "\u2014": 10.48,
  k: 6.51,
  K: 7.9,
  M: 10.48,
  G: 8.95,
  T: 7.6,
  P: 7.62,
  B: 7.89,
  i: 2.96,
  I: 3.2,
  s: 6.28,
  b: 7.37,
  m: 10.43,
  h: 7.06,
  d: 7.37,
  "\u00b5": 7.37,
  "\u00b0": 5.65,
  C: 8.59,
};

/**
 * The advance used for a character not in the table.
 *
 * The digit width, which is wider than most glyphs a value formatter emits.
 * That is the safe direction to be wrong in: an over-wide margin costs a few
 * units of plot, an under-wide one clips the label.
 *
 * Time labels ("Sat 18:00") contain letters that are deliberately absent
 * here. They never need measuring: the bottom margin is a line height rather
 * than a text width, and the labels at the two ends are anchored to their
 * near edge instead of centred, so none of them can overflow the image.
 */
const FALLBACK_ADVANCE = 7.56;

export function labelWidth(text: string, fontPx = AXIS_FONT_PX): number {
  let total = 0;
  for (const ch of text) {
    total += ADVANCE[ch] ?? FALLBACK_ADVANCE;
  }
  return (total * fontPx) / AXIS_FONT_PX;
}

/**
 * The widest of several labels -- what a caller passes to layout() once it
 * knows the ticks it will draw.
 *
 * Widest by RENDERED width, not by string length: "1.0 GB/s" is 8 characters
 * and "16 GiB" is 6, but the digits and the wide M/G glyphs mean length is
 * not the ordering. Comparing lengths put the wrong label in the margin.
 */
export function widestLabel(
  labels: readonly string[],
  fontPx = AXIS_FONT_PX,
): string {
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
