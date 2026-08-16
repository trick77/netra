import { describe, expect, it } from "vitest";
import {
  AXIS_FONT_PX,
  contains,
  labelWidth,
  layout,
  plotHeight,
  plotWidth,
  widestLabel,
  xAt,
  yAt,
} from "./plot";

describe("plot", () => {
  describe("labelWidth", () => {
    // The advance table is only worth anything if it agrees with what a
    // browser actually renders. These are getBBox() measurements taken in
    // Chrome against the app's font stack at AXIS_FONT_PX with tabular-nums
    // on. There is no way to measure text in the test environment, so this
    // pinned comparison IS the check that the table has not drifted.
    const MEASURED_IN_CHROME: Array<[string, number]> = [
      ["500 MB/s", 54.38],
      ["1.0 GB/s", 48.84],
      ["16 GiB", 38.31],
      ["100%", 33.78],
      ["0%", 18.67],
      ["4 GiB", 30.75],
      ["0", 7.56],
    ];

    // A fifth of a pixel. The advances are themselves rounded measurements,
    // so demanding exactness would fail on rounding rather than on drift;
    // a fifth of a pixel is far tighter than anything that could clip a
    // label and still catches a genuinely wrong entry in the table.
    const TOLERANCE_PX = 0.2;

    for (const [text, measured] of MEASURED_IN_CHROME) {
      it(`matches the rendered width of "${text}"`, () => {
        expect(Math.abs(labelWidth(text) - measured)).toBeLessThan(
          TOLERANCE_PX,
        );
      });
    }

    // Measured at AXIS_FONT_PX, and that is the size the app draws an axis
    // at. Scaling to another size is deliberately only an approximation:
    // advances are hinted per size and do not scale linearly -- deriving
    // 12px from an 11px measurement put "500 MB/s" 0.56 units out, enough to
    // clip. A different axis size needs its own measured table.
    it("computes at the calibrated size by default", () => {
      expect(labelWidth("100%")).toBe(labelWidth("100%", AXIS_FONT_PX));
    });

    // An over-wide margin costs a few pixels of plot; an under-wide one
    // clips the label. Unknown glyphs must therefore err wide.
    it("assumes a wide glyph for a character it does not know", () => {
      expect(labelWidth("☃")).toBeGreaterThanOrEqual(labelWidth("i"));
    });

    it("is zero for an empty label", () => {
      expect(labelWidth("")).toBe(0);
    });
  });

  describe("widestLabel", () => {
    // Length is not the ordering: "1.0 GB/s" is 8 characters and "16 GiB" is
    // 6, but the wide M/G glyphs mean the shorter string can be the wider
    // one. Comparing lengths put the wrong label in the margin.
    it("picks the widest rendering, not the longest string", () => {
      expect(widestLabel(["1.0 GB/s", "500 MB/s"])).toBe("500 MB/s");
      expect(labelWidth("500 MB/s")).toBeGreaterThan(labelWidth("1.0 GB/s"));
    });

    it("is total for an empty list", () => {
      expect(widestLabel([])).toBe("");
    });
  });

  describe("layout", () => {
    it("gives the whole image to the series when there are no labels", () => {
      const rect = layout(260, 64);
      expect(rect).toEqual({ left: 0, right: 260, top: 0, bottom: 64 });
    });

    // The margin has to fit the text that will actually be drawn. This is
    // the regression that produced "00 MB/s" against a guessed constant.
    it("reserves a left margin wide enough for the value label", () => {
      const rect = layout(260, 112, { yLabel: "500 MB/s" });
      expect(rect.left).toBeGreaterThan(labelWidth("500 MB/s"));
    });

    it("reserves less for a narrow label than for a wide one", () => {
      const narrow = layout(260, 112, { yLabel: "0%" });
      const wide = layout(260, 112, { yLabel: "500 MB/s" });
      expect(narrow.left).toBeLessThan(wide.left);
    });

    // The topmost label's baseline is centred on the topmost gridline, so
    // half a line of it sits above that line and would be clipped by the
    // top of the image.
    it("reserves room above the plot for the topmost label", () => {
      expect(layout(260, 112, { yLabel: "100%" }).top).toBeGreaterThan(0);
    });

    it("reserves room below the plot for the time labels", () => {
      const rect = layout(260, 112, { xLabel: "14:00" });
      expect(rect.bottom).toBeLessThan(112);
    });

    it("keeps the plot rect inside the image", () => {
      const rect = layout(260, 112, { yLabel: "500 MB/s", xLabel: "Sat 18:00" });
      expect(rect.left).toBeGreaterThanOrEqual(0);
      expect(rect.right).toBeLessThanOrEqual(260);
      expect(rect.top).toBeGreaterThanOrEqual(0);
      expect(rect.bottom).toBeLessThanOrEqual(112);
      expect(plotWidth(rect)).toBeGreaterThan(0);
      expect(plotHeight(rect)).toBeGreaterThan(0);
    });
  });

  describe("xAt / yAt", () => {
    const rect = layout(200, 100, { yLabel: "0%" });

    it("maps fraction 0 to the left edge and 1 to the right", () => {
      expect(xAt(rect, 0)).toBe(rect.left);
      expect(xAt(rect, 1)).toBe(rect.right);
    });

    // SVG's y grows downward and a value grows upward, so the mapping is
    // inverted: the largest value sits at the smallest y.
    it("maps fraction 1 to the TOP, not the bottom", () => {
      expect(yAt(rect, 1)).toBe(rect.top);
      expect(yAt(rect, 0)).toBe(rect.bottom);
    });

    it("puts a half fraction at the middle", () => {
      expect(yAt(rect, 0.5)).toBeCloseTo((rect.top + rect.bottom) / 2, 10);
    });
  });

  describe("contains", () => {
    const rect = layout(260, 112, { yLabel: "500 MB/s", xLabel: "14:00" });

    it("accepts a point inside the plot", () => {
      expect(contains(rect, rect.left + 10, rect.top + 10)).toBe(true);
    });

    // Brushing the axis labels is not hovering the chart, and must not leave
    // a crosshair rule behind.
    it("rejects the axis margins", () => {
      expect(contains(rect, rect.left - 5, rect.top + 10)).toBe(false);
      expect(contains(rect, rect.left + 10, rect.bottom + 5)).toBe(false);
    });
  });
});
