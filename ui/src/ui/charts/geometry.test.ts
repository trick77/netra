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
      // yPad 0: this pins the BASELINE rule, not the band's headroom.
      const bands = stackBands(
        [
          [0, 0],
          [0, 0],
        ],
        100,
        20,
        0,
        0,
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
      const bands = stackBands([[5, 5]], 100, 20, 5, 0, 0);
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

    // A traffic floor is routinely a thousandth of its ceiling, and a fleet
    // cell has fourteen pixels either side of the midline. Straight
    // arithmetic puts that reading 0.014px off the line, round1 snaps it to
    // zero, and the polygon has no area -- a host moving 26 kB/s all day drew
    // exactly as much ink as a host moving nothing. RRDtool keeps full
    // precision and cairo paints the same value as a dim continuous row,
    // which is why an Observium sparkline has a floor where ours had bare
    // background.
    // The area has NO floor, deliberately. A floor sounds like it protects a
    // small reading and what it actually does is make every small reading
    // identical: on a host whose median is a thousandth of its burst, every
    // quiet column clamps to the same one pixel and the cell draws a dead
    // straight line. rrdtool lets those columns fall to nothing, and the
    // variation that survives is the detail. Nothing is drawn in its place:
    // the reference draws nothing there either -- in the operator's own
    // Observium sparkline the inbound series is absent from 111 of 173
    // columns -- and a line laid over the area to keep it visible put a
    // saturated rule across every column instead.
    it("lets a sub-pixel reading round away, as rrdtool does", () => {
      // Given a floor a thousandth of the ceiling, drawn in a fleet cell
      const { up, mid } = mirrorPaths([1, 1, 1000], [], 170, 32, 1000, 2);
      const ys = [...up.matchAll(/-?\d+\.?\d*,(-?\d+\.?\d*)/g)].map((m) =>
        parseFloat(m[1]!),
      );

      // Then the quiet columns sit ON the midline: 1/1000 of 16px is 0.016,
      // which is not a pixel and is not drawn as one.
      const quiet = ys.filter((y) => mid - y < 1);
      expect(quiet.length).toBeGreaterThan(0);
      for (const y of quiet) expect(y).toBeCloseTo(mid, 6);

      // And the loud one reaches the far edge. There is no down half here,
      // so the axis is [0, +1000] and the whole box is the up half: `pad` is
      // not spent, because a bar has no stroke to keep off the edge.
      expect(mid - Math.min(...ys)).toBeCloseTo(32, 6);
    });

    // RRDtool's scaling, and the reason its peaks reach the edge of the cell
    // where ours stopped short. One scale for BOTH halves -- so a taller bar
    // still means more bytes, whichever direction it points -- with the zero
    // line placed where the data puts it, so the combined range fills the box
    // instead of each half being given exactly half of it.
    it("shares one scale and places zero where the data puts it", () => {
      // Given a pair whose halves peak at 5 and 10 in a 32px box
      const { up, down, mid } = mirrorPaths([5, 5], [10, 10], 100, 32, 10, 2);
      const ys = (d: string) =>
        [...d.matchAll(/-?\d+\.?\d*,(-?\d+\.?\d*)/g)].map((m) =>
          parseFloat(m[1]!),
        );

      // Then zero sits above the middle, not at it: the up half needs 5 of
      // the combined 15 and the down half needs 10, so zero lands a third of
      // the way down -- on the whole pixel nearest it, which keeps the bars
      // measured from it inside their own rows.
      const span = 5 + 10;
      expect(mid).toBe(Math.round((32 * 5) / span));

      // And each peak reaches its OWN edge. No headroom: the reference
      // passes --rigid, which makes rrdtool skip expand_range() altogether
      // (rrd_graph.c:4042), so the window's peak is the ceiling.
      expect(Math.min(...ys(up))).toBeCloseTo(0, 0);
      expect(Math.max(...ys(down))).toBeCloseTo(32, 0);
      expect(Math.max(...ys(down))).toBeGreaterThan(32 * 0.8);

      // ...while staying on ONE scale: the down half is twice the up half,
      // exactly as 10 is twice 5. Given each half its own ceiling both would
      // be full height and the cell would claim they were equal.
      // Within the rounding, since bar heights are whole pixels: 10.7 and
      // 21.3 land on 11 and 21, which is 1.91 rather than a clean 2.
      const upHeight = mid - Math.min(...ys(up));
      const downHeight = Math.max(...ys(down)) - mid;
      expect(downHeight / upHeight).toBeCloseTo(2, 0);
    });

    it("centres zero when nothing is drawn", () => {
      // An all-zero pair has no range to place a line by, and a midline that
      // jumped to the top of an empty cell would be a statement about a host
      // that reported nothing.
      expect(mirrorPaths([0, 0], [0, 0], 100, 32, 10, 2).mid).toBe(16);
    });

    it("leaves a genuine zero on the midline", () => {
      // "Nothing moved" and "almost nothing moved" are different answers, and
      // this mark is what distinguishes them. Lifting a zero would make every
      // idle interface claim a reading it never had.
      const { up, mid } = mirrorPaths([0, 0, 1000], [], 170, 32, 1000, 2);
      const ys = [...up.matchAll(/-?\d+\.?\d*,(-?\d+\.?\d*)/g)].map((m) =>
        parseFloat(m[1]!),
      );
      expect(ys.filter((y) => y === mid).length).toBeGreaterThan(0);
    });

    it("keeps the quieter half inside the box, however lopsided the pair", () => {
      // Given a host pulling a hundredth of what it pushes -- a backup target,
      // a mirror, any box whose traffic is one-way
      const { up, down, mid } = mirrorPaths(
        [10_000, 10_000],
        [1_000_000, 1_000_000],
        170,
        32,
        1e6,
        2,
      );
      const ys = (d: string) =>
        [...d.matchAll(/-?\d+\.?\d*,(-?\d+\.?\d*)/g)].map((m) =>
          parseFloat(m[1]!),
        );

      // Then the zero line is off the edge, keeping a row for the half that
      // has readings: pinned to 0, any inbound bar reaching a whole pixel
      // would be drawn from row 0 to row -1 and clipped away. The padding
      // lifts it further off on its own -- a hundredth of the other half is
      // 0.3 of a row unpadded, and about 3 once both ends are widened.
      expect(mid).toBeGreaterThanOrEqual(1);
      expect(mid).toBeLessThan(32 / 4);

      // This half's own readings are a hundredth of the other's, which at 32
      // rows is a third of a pixel. It is DRAWN at a third of a pixel rather
      // than rounded away: heights are sub-pixel, so the renderer lays down a
      // partly-covered row and the reading survives as a tint. Rounding it to
      // nothing -- or up to a whole row -- is what flattened a bursty host's
      // quiet stretch into a dead line.
      const top = Math.min(...ys(up));
      expect(top).toBeLessThan(mid);
      expect(mid - top).toBeLessThan(1);

      // And the loud half stops inside the box rather than a row past it --
      // short of the edge, because the range is padded before it is drawn.
      expect(Math.max(...ys(down))).toBeLessThanOrEqual(32);
      expect(Math.max(...ys(down))).toBeGreaterThan(32 * 0.8);
    });

    it("draws a reading that stands alone between two holes", () => {
      // A bar is a rectangle over its own column, so one is drawable where a
      // polyline needed two points to be a line. A host that reported in a
      // single column of the window drew an empty cell, which reads as a host
      // that reported nothing at all.
      const { up, mid } = mirrorPaths([null, 500, null], [], 100, 32, 500, 2);
      expect(up).not.toBe("");
      const ys = [...up.matchAll(/-?\d+\.?\d*,(-?\d+\.?\d*)/g)].map((m) =>
        parseFloat(m[1]!),
      );
      expect(Math.min(...ys)).toBeLessThan(mid);
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
      // Both halves total 20, so the axis is [-20, +20], zero is the middle
      // of a 40-tall box and each half spends its whole 20 rows.
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
      );
      expect(mid).toBe(20);

      // Then the first layer sits between the midline and half way up it,
      // and the second between there and the top -- the second stacked ON
      // the first, never overlapping it.
      expect(Math.min(...ys(up[0]!))).toBeCloseTo(10, 6);
      expect(Math.max(...ys(up[0]!))).toBeCloseTo(20, 6);
      expect(Math.min(...ys(up[1]!))).toBeCloseTo(0, 6);
      expect(Math.max(...ys(up[1]!))).toBeCloseTo(10, 6);

      // And the down half is the same distances on the other side.
      expect(Math.max(...ys(down[0]!))).toBeCloseTo(30, 6);
      expect(Math.max(...ys(down[1]!))).toBeCloseTo(40, 6);
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
