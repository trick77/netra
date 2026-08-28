import { describe, expect, it } from "vitest";
import {
  MIRROR_FILL_OPACITY,
  BAND_STROKE_WIDTH,
  mirrorEdge,
  SPARK_WIDTH,
} from "./size";

describe("mirrorEdge", () => {
  // The threshold is a relation between the ink and the data: an edge is only
  // an edge while it is narrower than the thing it outlines.
  //
  // Collapsed to one answer once, on the reasoning that rrdtool fills an AREA
  // solid at every size and a threshold was therefore an invention. True of
  // rrdtool, and it made the charts worse: a translucent fill over a dark
  // ground with nothing behind it is not translucent to look at, every row of
  // the band renders the same #5d5e75, and an operator reported the result as
  // a solid fill. The edge is what makes an area read as a shape.
  it("keeps the edge while a data column is wider than it", () => {
    // A dialog: 285 five-minute buckets across 1000px is 3.5px a column, and
    // a 1.25px edge sits comfortably inside one.
    expect(mirrorEdge(1000, 285)).toEqual({
      fillOpacity: MIRROR_FILL_OPACITY,
      strokeWidth: BAND_STROKE_WIDTH,
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
    expect(mirrorEdge(BAND_STROKE_WIDTH * 10, 11).strokeWidth).toBe(0);
  });

  // The column has to be measured the way scaleX() actually spaces points:
  // inset by `pad` at both ends, divided by the gaps rather than the points.
  it("measures a column the way the geometry spaces one", () => {
    expect(mirrorEdge(SPARK_WIDTH, 134, 2).strokeWidth).toBe(0);
    expect(mirrorEdge(SPARK_WIDTH, 134, 0).strokeWidth).toBe(BAND_STROKE_WIDTH);
  });

  it("keeps the edge for a chart of one point", () => {
    expect(mirrorEdge(170, 1, 2).strokeWidth).toBe(BAND_STROKE_WIDTH);
  });

  it("keeps the edge for a chart with no points to measure", () => {
    expect(mirrorEdge(170, 0).strokeWidth).toBe(BAND_STROKE_WIDTH);
  });
});
