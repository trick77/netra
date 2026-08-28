import { describe, expect, it } from "vitest";
import {
  AREA_FILL_OPACITY,
  areaFillOpacity,
  MIRROR_FILL_OPACITY,
  MIRROR_STROKE_WIDTH,
  mirrorEdge,
  SPARK_WIDTH,
} from "./size";

describe("areaFillOpacity", () => {
  it("draws a lone series at the weight a single fill has always had", () => {
    expect(areaFillOpacity(1)).toBe(AREA_FILL_OPACITY);
  });

  // The property that matters, stated as a property: compounding is what
  // makes overlapping fills read as a stack, so the weight has to fall as
  // the count rises. Every step, not just the first.
  it("thins the fill as more areas share the baseline", () => {
    const counts = [1, 2, 3, 4, 6, 8, 12];
    const weights = counts.map(areaFillOpacity);
    for (let i = 1; i < weights.length; i++) {
      expect(weights[i]!).toBeLessThan(weights[i - 1]!);
    }
  });

  // Thinned, not conceded. 1/count would put the six-series fragmentation
  // panel at 0.025, which is a fill nobody can see; sqrt keeps a mark there.
  it("keeps the worst declared panel visible", () => {
    expect(areaFillOpacity(6)).toBeGreaterThan(0.05);
  });

  // The deepest overlap a panel can produce is every area covering every
  // other, which composites to 1 - (1 - a)^count. Undivided that runs to 0.60
  // at the six bases a fragmentation panel declares and 0.86 at the twelve
  // Filesystem space draws on a six-mount host, both of which read as a
  // cumulative stack. Divided it stays under half: 0.31 at six, 0.41 at
  // twelve. The bound is what the rule promises -- the ground under a panel
  // never goes darker than the data drawn on it.
  it("holds the deepest overlap under half, however many series share it", () => {
    for (const count of [2, 4, 6, 8, 12]) {
      const deepest = 1 - (1 - areaFillOpacity(count)) ** count;
      expect(deepest).toBeLessThan(0.45);
    }
  });

  // A panel with no series has no fill to weight, and a caller that hands
  // one over must not get NaN or a division by zero into the markup.
  it("answers the single-series weight for an empty panel", () => {
    expect(areaFillOpacity(0)).toBe(AREA_FILL_OPACITY);
  });
});

describe("mirrorEdge", () => {
  // The threshold is a relation between the ink and the data: an edge is only
  // an edge while it is narrower than the thing it outlines.
  it("keeps the edge while a data column is wider than it", () => {
    // A dialog: 285 five-minute buckets across 1000px is 3.5px a column, and
    // a 1.25px edge sits comfortably inside one.
    expect(mirrorEdge(1000, 285)).toEqual({
      fillOpacity: MIRROR_FILL_OPACITY,
      strokeWidth: MIRROR_STROKE_WIDTH,
    });
  });

  it("drops the edge once a column is no wider than it", () => {
    // The fleet cell: one point per pixel, so the edge would run down both
    // sides of a two-pixel spike and be the mark rather than its outline.
    expect(mirrorEdge(SPARK_WIDTH, SPARK_WIDTH)).toEqual({
      fillOpacity: 1,
      strokeWidth: 0,
    });
  });

  it("takes the fill to opaque when it drops the edge", () => {
    // The fill is dimmed only because an edge sits over it. Without one it
    // has to carry the shape alone, which is also what lets it taper: a
    // translucent area with no edge reads as a smudge rather than a spike.
    expect(mirrorEdge(170, 400).fillOpacity).toBe(1);
  });

  // Exactly at the threshold the edge goes. A column and an edge of the same
  // width means the outline covers the whole column, which is the failure
  // this exists to prevent rather than a borderline case worth keeping.
  it("drops the edge at exactly one edge-width per column", () => {
    // Ten gaps between eleven points across ten edge-widths.
    expect(mirrorEdge(MIRROR_STROKE_WIDTH * 10, 11).strokeWidth).toBe(0);
  });

  // The column has to be measured the way scaleX() actually spaces points:
  // inset by `pad` at both ends, divided by the gaps rather than the points.
  // 170px with 134 points is 1.269 measured carelessly and 1.248 measured
  // properly, and the edge is 1.25 -- so the careless reading keeps an
  // outline on a column narrower than the outline, which is the one case
  // this function exists to catch.
  it("measures a column the way the geometry spaces one", () => {
    expect(mirrorEdge(SPARK_WIDTH, 134, 2).strokeWidth).toBe(0);
    // Without the inset the same chart reads as having room, which is the
    // bug. Pinned so the two cannot drift apart again.
    expect(mirrorEdge(SPARK_WIDTH, 134, 0).strokeWidth).toBe(
      MIRROR_STROKE_WIDTH,
    );
  });

  it("keeps the edge for a chart of one point", () => {
    // A single point has no gap to measure, and draws no run anyway --
    // mirrorPaths drops a run of one. Answering "no edge" would make an
    // empty-looking chart disagree with every other one for no visible
    // reason.
    expect(mirrorEdge(170, 1, 2).strokeWidth).toBe(MIRROR_STROKE_WIDTH);
  });

  it("keeps the edge for a chart with no points to measure", () => {
    // An empty series draws nothing; answering "no edge" would leave the
    // weights of an empty chart disagreeing with every other one for no
    // reason a reader could see.
    expect(mirrorEdge(170, 0).strokeWidth).toBe(MIRROR_STROKE_WIDTH);
  });
});
