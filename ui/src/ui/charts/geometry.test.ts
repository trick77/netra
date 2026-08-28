import { describe, expect, it } from "vitest";
import {
  areaPath,
  extent,
  linePath,
  mirrorPaths,
  mirrorStackBands,
  stackBands,
} from "./geometry";

describe("geometry", () => {
  describe("extent", () => {
    // The exact footgun this function exists to prevent: Math.min/Math.max
    // coerce null to 0 via Number(null), so Math.min(...[5, null, 3]) is 0 --
    // a null silently becomes the floor of the y-scale.
    it("ignores nulls rather than letting them coerce to 0", () => {
      expect(extent([5, null, 3])).toEqual({ min: 3, max: 5 });
    });

    it("returns a defined extent for an all-null array", () => {
      expect(extent([null, null])).toEqual({ min: 0, max: 0 });
    });

    it("returns a defined extent for an empty array", () => {
      expect(extent([])).toEqual({ min: 0, max: 0 });
    });

    it("finds the min and max of a normal series", () => {
      expect(extent([4, 1, 9, 2])).toEqual({ min: 1, max: 9 });
    });
  });

  describe("linePath", () => {
    // The single most important behaviour in the chart layer: a null splits
    // the line into separate subpaths so SVG draws a hole, not a bridge.
    it("splits the line at nulls instead of interpolating", () => {
      const { paths } = linePath([1, 2, null, 4, 5], 100, 20, 0, 5);
      expect(paths).toHaveLength(2);
      expect(paths[0]!.startsWith("M")).toBe(true);
      expect(paths[1]!.startsWith("M")).toBe(true);
    });

    it("emits one subpath when there are no gaps", () => {
      expect(linePath([1, 2, 3], 100, 20, 0, 3).paths).toHaveLength(1);
    });

    // A lone point between two gaps cannot be a line, but it is real data
    // and must not vanish -- it comes back in `points` so the component can
    // render it as a dot instead of losing it to an empty chart.
    it("returns a lone point as a point, not a dropped index", () => {
      const { paths, points } = linePath([null, 2, null], 100, 20, 0, 5);
      expect(paths).toHaveLength(0);
      expect(points).toHaveLength(1);
    });

    it("returns every isolated point of a flapping series, not just the first", () => {
      const { paths, points } = linePath([1, null, 2, null, 3], 100, 20, 0, 5);
      expect(paths).toHaveLength(0);
      expect(points).toHaveLength(3);
    });

    it("returns an empty array for empty input", () => {
      expect(linePath([], 100, 20, 0, 5)).toEqual({ paths: [], points: [] });
    });

    it("returns an empty array when every value is null", () => {
      expect(linePath([null, null, null], 100, 20, 0, 5)).toEqual({
        paths: [],
        points: [],
      });
    });

    it("returns a single point for one surviving value surrounded by leading and trailing nulls", () => {
      const { paths, points } = linePath(
        [null, null, 3, null, null],
        100,
        20,
        0,
        5,
      );
      expect(paths).toHaveLength(0);
      expect(points).toHaveLength(1);
    });

    it("keeps a leading run intact even when nulls trail it", () => {
      const { paths } = linePath([1, 2, 3, null, null], 100, 20, 0, 3);
      expect(paths).toHaveLength(1);
    });

    it("keeps a trailing run intact even when nulls lead it", () => {
      const { paths } = linePath([null, null, 1, 2, 3], 100, 20, 0, 3);
      expect(paths).toHaveLength(1);
    });

    it("splits into as many subpaths as unbroken runs of two or more, and reports the lone point separately", () => {
      // run [1,2] (len2, kept), lone 5 (dropped to points), run [9,9,9] (len3, kept)
      const { paths, points } = linePath(
        [1, 2, null, 5, null, 9, 9, 9],
        100,
        20,
        0,
        10,
      );
      expect(paths).toHaveLength(2);
      expect(points).toHaveLength(1);
    });

    // SVG's y-axis grows downward: a higher data value must map to a
    // *smaller* y coordinate, or every chart in the product renders upside
    // down.
    it("maps a higher value to a smaller y coordinate", () => {
      const [sub] = linePath([0, 10], 100, 20, 0, 10).paths;
      const points = sub!
        .slice(1)
        .trim()
        .split(/L|,/)
        .map((n) => parseFloat(n))
        .filter((n) => !Number.isNaN(n));
      const [, y0, , y1] = points;
      expect(y1).toBeLessThan(y0!);
    });

    // A literal 0 is a real, collected measurement -- distinct from `null`,
    // which means nothing was collected. A regression to `v ?? 0` or a
    // truthiness check would silently drop the 0 and no other test here
    // would notice, because most fixtures don't happen to include one.
    it("keeps a literal 0 in the middle of a run and plots it at the baseline", () => {
      const { paths } = linePath([0, 5, 10], 100, 20, 0, 10);
      expect(paths).toHaveLength(1);
      const y0 = paths[0]!.match(/-?\d+\.?\d*,(-?\d+\.?\d*)/)![1];
      // 0 is the minimum of the [0, 10] domain, so it maps to the bottom
      // edge of the chart (y = h = 20), not to the top or to being dropped.
      expect(parseFloat(y0!)).toBe(20);
    });

    it("rounds coordinates to one decimal place", () => {
      const [sub] = linePath([1, 2, 3], 33, 17, 0, 3).paths;
      const numbers = sub!.match(/-?\d+\.?\d*/g) ?? [];
      for (const n of numbers) {
        const decimals = n.includes(".") ? n.split(".")[1]!.length : 0;
        expect(decimals).toBeLessThanOrEqual(1);
      }
    });
  });

  describe("areaPath", () => {
    it("closes a line subpath down to the baseline", () => {
      const { paths } = linePath([1, 2, 3], 100, 20, 0, 3);
      const [area] = areaPath(paths, 100, 20);
      expect(area!.startsWith("M")).toBe(true);
      expect(area!.endsWith("Z")).toBe(true);
      // Closes down to the bottom edge of the chart.
      expect(area).toContain("20");
    });

    it("returns one fill per subpath rather than bridging the gap between them", () => {
      const { paths } = linePath([1, 2, null, 4, 5], 100, 20, 0, 5);
      const areas = areaPath(paths, 100, 20);
      expect(areas).toHaveLength(2);
      for (const area of areas) {
        expect(area.startsWith("M")).toBe(true);
        expect(area.endsWith("Z")).toBe(true);
      }
    });

    it("closes to h - pad, not h, so the fill does not overflow the plot box", () => {
      const { paths } = linePath([1, 2, 3], 100, 20, 0, 3, 5);
      const [area] = areaPath(paths, 100, 20, 5);
      // baseline is h - pad = 15, and must not also contain the unpadded 20.
      expect(area).toContain("15");
      expect(area).not.toMatch(/,20(\D|$)/);
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

    // Rows reach here ragged: querySeries returns one row per series and a
    // host that started reporting late, or a point-limit truncation that
    // lands mid-series, leaves the later series shorter. Reading the length
    // off series[0] made every index past the short series' end `undefined`
    // -- neither null nor a number -- which scaled to NaN and erased the
    // whole band from the chart, silently.
    it("treats a missing tail in a shorter series as a gap, not as NaN", () => {
      const bands = stackBands(
        [
          [1, 1, 1],
          [2, 2],
        ],
        100,
        20,
        10,
      );

      expect(bands).toHaveLength(2);
      for (const band of bands) {
        expect(band).not.toContain("NaN");
      }
    });

    // The opposite raggedness: a longer later series was truncated to
    // series[0]'s length and its tail simply never drew. The x domain is the
    // longest series, so index 1 of a four-point stack sits a third of the
    // way across -- the two trailing indices are gaps (the first series has
    // nothing there), and a gap still occupies its share of the axis.
    it("spans the longest series rather than truncating to the first", () => {
      const bands = stackBands(
        [
          [1, 1],
          [2, 2, 2, 2],
        ],
        100,
        20,
        10,
      );

      expect(bands).toHaveLength(2);
      for (const band of bands) {
        expect(band).not.toContain("NaN");
        expect(band).toContain("33.3");
        expect(band).not.toContain("L100,");
      }
    });

    // A null in ANY series at an index makes the running total undefined
    // for every band at that index -- v ?? 0 would fabricate that series as
    // reporting zero and draw a band as if it were really idle.
    it("breaks every band at an index where any series is null, instead of drawing a band at zero", () => {
      const bands = stackBands(
        [
          [1, 1, null, 1, 1],
          [2, 2, 2, 2, 2],
        ],
        100,
        20,
        10,
      );
      expect(bands).toHaveLength(2);
      for (const band of bands) {
        // Two disjoint M...Z segments -- the gap at index 1 split it, it
        // was not bridged.
        expect(band.match(/M/g)).toHaveLength(2);
        expect(band.match(/Z/g)).toHaveLength(2);
      }
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

    it("breaks the fill at a null instead of drawing it as zero", () => {
      const { up } = mirrorPaths(
        [5, 5, null, 5, 5],
        [1, 1, 1, 1, 1],
        100,
        20,
        10,
      );
      expect(up.match(/M/g)).toHaveLength(2);
    });

    it("lets an out-of-range value escape the plot box rather than clamping it, matching linePath", () => {
      const { up, mid } = mirrorPaths([20, 20], [], 100, 20, 10);
      const ys = [...up.matchAll(/-?\d+\.?\d*,(-?\d+\.?\d*)/g)].map((m) =>
        parseFloat(m[1]!),
      );
      // v = 20 against max = 10 is double scale: it must overshoot the
      // midline by more than the usable half-height, not clamp to its edge.
      const usable = mid;
      for (const y of ys) {
        if (y !== mid) expect(y).toBeLessThan(mid - usable * 0.99);
      }
    });
  });

  describe("mirrorStackBands", () => {
    // Every case below reads y off the path text. A band is
    // "M x,y Lx,y ... Lx,y Z", so the first coordinate pair of the TOP edge
    // is what says how tall the layer's running total is.
    const ys = (d: string) =>
      [...d.matchAll(/-?\d+\.?\d*,(-?\d+\.?\d*)/g)].map((m) =>
        parseFloat(m[1]!),
      );

    it("stacks each half away from the midline, in layer order", () => {
      // Given two layers of 10 either side, against a ceiling of 40 in a
      // 40-tall box: the half-height is 20, so 10 is a quarter of it.
      const { up, down, mid } = mirrorStackBands(
        [
          [10, 10],
          [10, 10],
        ],
        [
          [10, 10],
          [10, 10],
        ],
        100,
        40,
        40,
      );
      expect(mid).toBe(20);

      // Then the first layer sits between the midline and 5 units above it,
      // and the second between 5 and 10 -- the second stacked ON the first,
      // never overlapping it.
      expect(Math.min(...ys(up[0]!))).toBeCloseTo(15, 6);
      expect(Math.max(...ys(up[0]!))).toBeCloseTo(20, 6);
      expect(Math.min(...ys(up[1]!))).toBeCloseTo(10, 6);
      expect(Math.max(...ys(up[1]!))).toBeCloseTo(15, 6);

      // And the down half is the same distances on the other side.
      expect(Math.max(...ys(down[0]!))).toBeCloseTo(25, 6);
      expect(Math.max(...ys(down[1]!))).toBeCloseTo(30, 6);
    });

    it("scales each half against its own stack, not against both", () => {
      // One layer of 20 up and one of 20 down, ceiling 20: each half fills
      // its own side exactly. A ceiling read off in-plus-out would draw both
      // at half height and say the link was half loaded.
      const { up, mid } = mirrorStackBands([[20, 20]], [[20, 20]], 100, 40, 20);
      expect(Math.min(...ys(up[0]!))).toBeCloseTo(mid - 20, 6);
    });

    it("breaks a half at a null in ANY of its own layers", () => {
      // eth1 reported nothing in the middle bucket, so the running total is
      // undefined there for eth0 too -- both up bands break, and the DOWN
      // half, which has no hole, does not.
      const { up, down } = mirrorStackBands(
        [
          [5, 5, 5, 5, 5],
          [5, 5, null, 5, 5],
        ],
        [
          [1, 1, 1, 1, 1],
          [1, 1, 1, 1, 1],
        ],
        100,
        20,
        20,
      );
      expect(up[0]!.match(/M/g)).toHaveLength(2);
      expect(up[1]!.match(/M/g)).toHaveLength(2);
      expect(down[0]!.match(/M/g)).toHaveLength(1);
    });

    it("lets the stack escape the box rather than clamping it", () => {
      // Two layers of 20 against a ceiling of 20: the total is double the
      // half-height and must visibly overflow, as mirrorPaths and linePath
      // both do.
      const { up, mid } = mirrorStackBands(
        [
          [20, 20],
          [20, 20],
        ],
        [],
        100,
        40,
        20,
      );
      expect(Math.min(...ys(up[1]!))).toBeLessThan(mid - 20);
    });

    it("puts an all-zero stack on the midline rather than mid-half", () => {
      // max === 0 is the degenerate case stackY() exists for: a host that
      // genuinely moved nothing must draw nothing, not a band floating at
      // half height that reads as "about half loaded".
      const { up, mid } = mirrorStackBands([[0, 0]], [[0, 0]], 100, 40, 0);
      for (const y of ys(up[0]!)) expect(y).toBeCloseTo(mid, 6);
    });

    it("answers an empty half without inventing a band", () => {
      const { up, down } = mirrorStackBands([[1, 1]], [], 100, 20, 1);
      expect(up).toHaveLength(1);
      expect(down).toEqual([]);
    });

    it("measures off the longest layer, not the first", () => {
      // Rows arrive ragged. Measured off the first, a longer second layer
      // read `undefined` past its end -- neither null nor a number -- and
      // scaled to NaN, which erased the band. Same trap stackBands documents.
      const { up } = mirrorStackBands(
        [
          [5, 5],
          [5, 5, 5, 5],
        ],
        [],
        100,
        20,
        10,
      );
      // The short layer's own run ends where it ends, and nothing is drawn
      // past it: an index where any layer is missing is a gap for both.
      expect(up[0]).not.toContain("NaN");
      expect(up[1]).not.toContain("NaN");
    });
  });
});
