import { describe, expect, it } from "vitest";
import { areaPath, linePath, mirrorPaths, stackBands } from "./geometry";

describe("geometry", () => {
  describe("linePath", () => {
    // The single most important behaviour in the chart layer: a null splits
    // the line into separate subpaths so SVG draws a hole, not a bridge.
    it("splits the line at nulls instead of interpolating", () => {
      const subs = linePath([1, 2, null, 4, 5], 100, 20, 0, 5);
      expect(subs).toHaveLength(2);
      expect(subs[0].startsWith("M")).toBe(true);
      expect(subs[1].startsWith("M")).toBe(true);
    });

    it("emits one subpath when there are no gaps", () => {
      expect(linePath([1, 2, 3], 100, 20, 0, 3)).toHaveLength(1);
    });

    // A lone point between two gaps cannot be a line. It is rendered as a dot
    // by the component, not as an empty <path>.
    it("drops runs of a single point rather than emitting a zero-length path", () => {
      expect(linePath([null, 2, null], 100, 20, 0, 5)).toHaveLength(0);
    });

    it("returns an empty array for empty input", () => {
      expect(linePath([], 100, 20, 0, 5)).toEqual([]);
    });

    it("returns an empty array when every value is null", () => {
      expect(linePath([null, null, null], 100, 20, 0, 5)).toEqual([]);
    });

    it("drops a single surviving point surrounded by leading and trailing nulls", () => {
      expect(linePath([null, null, 3, null, null], 100, 20, 0, 5)).toHaveLength(
        0,
      );
    });

    it("keeps a leading run intact even when nulls trail it", () => {
      const subs = linePath([1, 2, 3, null, null], 100, 20, 0, 3);
      expect(subs).toHaveLength(1);
    });

    it("keeps a trailing run intact even when nulls lead it", () => {
      const subs = linePath([null, null, 1, 2, 3], 100, 20, 0, 3);
      expect(subs).toHaveLength(1);
    });

    it("splits into as many subpaths as unbroken runs of two or more", () => {
      // run [1,2] (len2, kept), lone 5 (dropped), run [9,9,9] (len3, kept)
      const subs = linePath([1, 2, null, 5, null, 9, 9, 9], 100, 20, 0, 10);
      expect(subs).toHaveLength(2);
    });

    // SVG's y-axis grows downward: a higher data value must map to a
    // *smaller* y coordinate, or every chart in the product renders upside
    // down.
    it("maps a higher value to a smaller y coordinate", () => {
      const [sub] = linePath([0, 10], 100, 20, 0, 10);
      const points = sub
        .slice(1)
        .trim()
        .split(/L|,/)
        .map((n) => parseFloat(n))
        .filter((n) => !Number.isNaN(n));
      const [, y0, , y1] = points;
      expect(y1).toBeLessThan(y0);
    });

    // A literal 0 is a real, collected measurement -- distinct from `null`,
    // which means nothing was collected. A regression to `v ?? 0` or a
    // truthiness check would silently drop the 0 and no other test here
    // would notice, because most fixtures don't happen to include one.
    it("keeps a literal 0 in the middle of a run and plots it at the baseline", () => {
      const subs = linePath([0, 5, 10], 100, 20, 0, 10);
      expect(subs).toHaveLength(1);
      const y0 = subs[0]!.match(/-?\d+\.?\d*,(-?\d+\.?\d*)/)![1];
      // 0 is the minimum of the [0, 10] domain, so it maps to the bottom
      // edge of the chart (y = h = 20), not to the top or to being dropped.
      expect(parseFloat(y0!)).toBe(20);
    });

    it("rounds coordinates to one decimal place", () => {
      const [sub] = linePath([1, 2, 3], 33, 17, 0, 3);
      const numbers = sub.match(/-?\d+\.?\d*/g) ?? [];
      for (const n of numbers) {
        const decimals = n.includes(".") ? n.split(".")[1].length : 0;
        expect(decimals).toBeLessThanOrEqual(1);
      }
    });
  });

  describe("areaPath", () => {
    it("closes a line subpath down to the baseline", () => {
      const [sub] = linePath([1, 2, 3], 100, 20, 0, 3);
      const area = areaPath(sub, 100, 20);
      expect(area.startsWith("M")).toBe(true);
      expect(area.endsWith("Z")).toBe(true);
      // Closes down to the bottom edge of the chart.
      expect(area).toContain("20");
    });
  });

  describe("stackBands", () => {
    it("stacks bands cumulatively so the silhouette is the total", () => {
      const bands = stackBands(
        [
          [1, 1],
          [2, 2],
        ],
        100,
        20,
        10,
      );
      expect(bands).toHaveLength(2);
    });

    it("returns closed paths for every band", () => {
      const bands = stackBands(
        [
          [1, 1],
          [2, 2],
        ],
        100,
        20,
        10,
      );
      for (const band of bands) {
        expect(band.startsWith("M")).toBe(true);
        expect(band.endsWith("Z")).toBe(true);
      }
    });

    it("returns an empty array for empty series", () => {
      expect(stackBands([], 100, 20, 10)).toEqual([]);
    });

    it("returns an empty array when the series contain no points", () => {
      expect(stackBands([[]], 100, 20, 10)).toEqual([]);
    });

    // A host genuinely idling at 0 across every series must draw a band
    // flat on the baseline, not floating at mid-chart -- mid-chart reads as
    // "about half loaded", which is a worse lie than drawing nothing.
    it("draws an all-zero stack on the baseline, not at mid-chart", () => {
      const bands = stackBands(
        [
          [0, 0],
          [0, 0],
        ],
        100,
        20,
        0,
      );
      expect(bands).toHaveLength(2);
      for (const band of bands) {
        const ys = [...band.matchAll(/-?\d+\.?\d*,(-?\d+\.?\d*)/g)].map((m) =>
          parseFloat(m[1]!),
        );
        for (const y of ys) {
          expect(y).toBe(20);
          expect(y).not.toBe(10); // h / 2 -- the bug this guards against
        }
      }
    });

    // A non-zero flat series (every point equal to `max`) still leaves `t`
    // constant across the band, but it must not collapse the whole band
    // onto the baseline the way an all-zero stack does -- that would draw
    // "fully loaded" and "idle" identically. Its top edge reaches the top
    // of the chart while its bottom edge (zero, the implicit floor) still
    // sits on the baseline: a full-height band, not a flat one.
    it("draws a non-zero flat stack as a full-height band, not collapsed onto the baseline", () => {
      const bands = stackBands([[5, 5]], 100, 20, 5);
      expect(bands).toHaveLength(1);
      const ys = [...bands[0]!.matchAll(/-?\d+\.?\d*,(-?\d+\.?\d*)/g)].map(
        (m) => parseFloat(m[1]!),
      );
      expect(Math.min(...ys)).toBe(0);
      expect(Math.max(...ys)).toBe(20);
    });
  });

  describe("mirrorPaths", () => {
    it("draws up above the midline and down below it, mirrored", () => {
      const { up, down, mid } = mirrorPaths([5, 5], [5, 5], 100, 20, 10);
      expect(mid).toBe(10);
      expect(up.startsWith("M")).toBe(true);
      expect(down.startsWith("M")).toBe(true);
    });

    it("places the midline at half the chart height", () => {
      const { mid } = mirrorPaths([1, 1], [1, 1], 100, 40, 10);
      expect(mid).toBe(20);
    });
  });
});
