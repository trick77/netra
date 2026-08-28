import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { mirrorPaths } from "./geometry";
import { UpDownSparkline } from "./UpDownSparkline";

describe("UpDownSparkline", () => {
  // 5 points, gap at index 2: two surviving runs of length >= 2 each
  // ([0,1] and [3,4]) -- mirrorPaths() drops any run of length 1, so a gap
  // too close to an edge would leave only one run and not actually prove
  // "the up side broke into two fills".
  it("breaks the up fill at a gap while the down fill stays whole", () => {
    const up = [1, 2, null, 4, 3];
    const down = [1, 1, 1, 1, 1];
    const { container } = render(
      <UpDownSparkline up={up} down={down} max={4} />,
    );
    const upPath = container.querySelector("path[data-up]");
    const downPath = container.querySelector("path[data-down]");
    expect(upPath?.getAttribute("d")?.match(/M/g)?.length).toBe(2);
    expect(downPath?.getAttribute("d")?.match(/M/g)?.length).toBe(1);
  });

  it("renders the exact geometry output with no post-processing", () => {
    const up = [1, 2, null, 4, 3];
    const down = [1, 1, 1, 1, 1];
    const width = 100;
    const height = 20;
    const pad = 2;
    const max = 4;
    const expected = mirrorPaths(up, down, width, height, max, pad);
    const { container } = render(
      <UpDownSparkline
        up={up}
        down={down}
        max={max}
        width={width}
        height={height}
        pad={pad}
      />,
    );
    expect(container.querySelector("path[data-up]")?.getAttribute("d")).toBe(
      expected.up,
    );
    expect(container.querySelector("path[data-down]")?.getAttribute("d")).toBe(
      expected.down,
    );
  });

  // The trade this component makes, pinned so it cannot be un-made by
  // accident. A heavy-tailed day drawn proportionally IS a flat line with a
  // spike on it -- that is what RRDtool draws and what an operator reads
  // traffic as -- and the alternative, a bent axis, buys the baseline back
  // by making two cells in one column mean different heights.
  it("draws a heavy-tailed day proportionally, spike and all", () => {
    // Given a day like ark's: a quiet baseline and one transfer three orders
    // of magnitude above it
    const up = [29_000, 31_000, 37_040_000, 30_000, 28_000];
    const down = [39_000, 40_000, 24_790_000, 38_000, 41_000];
    const height = 32;
    const pad = 2;
    const { container } = render(
      <UpDownSparkline up={up} down={down} height={height} pad={pad} />,
    );

    // Then the mark is exactly the proportional one, against the window's
    // own peak
    const max = 37_040_000;
    const proportional = mirrorPaths(up, down, 170, height, max, pad);
    const drawn = container.querySelector("path[data-up]")?.getAttribute("d");
    expect(drawn).toBe(proportional.up);

    // And the spike reaches the top of the half it is drawn in, which is the
    // reading a proportional axis is chosen for. y grows downward and `up` is
    // drawn above the midline, so a taller reading is a SMALLER y.
    const midline = height / 2;
    const ys = drawn!
      .split(" ")
      .map((p) => Number(p.split(",")[1]))
      .filter((y) => Number.isFinite(y));
    expect(midline - Math.min(...ys)).toBeCloseTo(height / 2 - pad, 6);
  });

  it("takes colours from the caller, defaulting to series tokens not hex", () => {
    const { container } = render(
      <UpDownSparkline
        up={[1, 2]}
        down={[1, 2]}
        max={4}
        upColor="var(--s3)"
        downColor="var(--s4)"
      />,
    );
    expect(container.querySelector("path[data-up]")?.getAttribute("fill")).toBe(
      "var(--s3)",
    );
    expect(
      container.querySelector("path[data-down]")?.getAttribute("fill"),
    ).toBe("var(--s4)");
  });

  // The mark itself: a dimmed fill with a solid edge of the same token, and
  // the mirror axis. Every number here is Overlay's mirrored branch verbatim
  // -- the host page's Traffic panel draws the same rx/tx pair, and the two
  // are read on the same screen. These assertions exist so a divergence fails here
  // rather than being noticed as "the fleet row looks a bit off".
  describe("mark weights", () => {
    it("dims the fill and strokes the same token on both sides, given room", () => {
      // Given a chart with an explicit colour per side and two points across
      // 170px -- 85px per point, room for an edge many times over
      const { container } = render(
        <UpDownSparkline
          up={[1, 2]}
          down={[1, 2]}
          max={4}
          upColor="var(--s3)"
          downColor="var(--s4)"
        />,
      );

      // When each side's path is read
      const upPath = container.querySelector("path[data-up]");
      const downPath = container.querySelector("path[data-down]");

      // Then the fill is translucent and the edge is that same colour, solid
      expect(upPath?.getAttribute("fill-opacity")).toBe("0.45");
      expect(upPath?.getAttribute("stroke")).toBe("var(--s3)");
      expect(upPath?.getAttribute("stroke-width")).toBe("1.25");
      expect(downPath?.getAttribute("fill-opacity")).toBe("0.45");
      expect(downPath?.getAttribute("stroke")).toBe("var(--s4)");
      expect(downPath?.getAttribute("stroke-width")).toBe("1.25");
    });

    // The case the fleet actually draws, and the one an operator complained
    // about: a 24h window folded to one point per pixel. A 1.25px edge on a
    // spike whose whole base is two pixels runs down both of its sides and
    // has its apex chopped square by the miter limit, so the mark becomes a
    // three-pixel block made of stroke. RRDtool's answer, which Observium
    // inherits, is to draw the area and no line at all.
    it("drops the edge once a point is narrower than the edge", () => {
      // Given a series with one point per pixel of the 170px cell
      const dense = Array.from({ length: 170 }, (_, i) => (i === 80 ? 100 : 1));
      const { container } = render(
        <UpDownSparkline up={dense} down={dense} max={100} />,
      );

      // Then there is no edge, and the fill carries the shape on its own --
      // which is what lets it taper: antialiasing softens the edge of a fill,
      // and can do nothing about the opaque middle of a stroke.
      const upPath = container.querySelector("path[data-up]");
      expect(upPath?.getAttribute("stroke")).toBe("none");
      expect(upPath?.getAttribute("stroke-width")).toBe("0");
      expect(upPath?.getAttribute("fill-opacity")).toBe("1");
    });

    it("rules the mirror axis across the full width at mid-height", () => {
      // Given a chart of a known box
      const width = 100;
      const height = 20;
      const { container } = render(
        <UpDownSparkline
          up={[1, 2]}
          down={[1, 2]}
          max={4}
          width={width}
          height={height}
        />,
      );

      // When the axis rule is read
      const mid = container.querySelector("line[data-mid]");

      // Then it spans the box at the midline mirrorPaths() anchors to
      expect(mid?.getAttribute("x1")).toBe("0");
      expect(mid?.getAttribute("x2")).toBe(String(width));
      expect(mid?.getAttribute("y1")).toBe(String(height / 2));
      expect(mid?.getAttribute("y2")).toBe(String(height / 2));
      expect(mid?.getAttribute("stroke")).toBe("var(--border)");
    });

    it("still rules the axis when the host reported nothing", () => {
      // Given a host that reported no traffic at all
      const { container } = render(
        <UpDownSparkline up={[null, null]} down={[null, null]} max={4} />,
      );

      // When the chart is read
      // Then neither fill is drawn, but the axis still says where zero was
      expect(container.querySelector("path[data-up]")).toBeNull();
      expect(container.querySelector("path[data-down]")).toBeNull();
      expect(container.querySelector("line[data-mid]")).not.toBeNull();
    });
  });

  it("gives the chart an accessible name", () => {
    render(
      <UpDownSparkline
        up={[1, 2]}
        down={[1, 2]}
        max={4}
        label="Inbound/outbound traffic"
      />,
    );
    expect(
      screen.getByRole("img", { name: "Inbound/outbound traffic" }),
    ).toBeInTheDocument();
  });
});
