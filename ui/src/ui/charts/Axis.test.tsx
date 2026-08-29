import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { AxisLabels, Grid, Spine, ZeroRule } from "./Axis";
import { layout, yAt } from "./plot";
import { mirroredTicks, niceTicks, timeTicks } from "./ticks";

/** Renders SVG children inside a host <svg>, which they all require. */
function draw(children: React.ReactNode) {
  const { container } = render(<svg>{children}</svg>);
  return container;
}

const RECT = layout(900, 320, { yLabel: "500 MB/s", xLabel: "14:00" });
const Y = niceTicks(0, 800);
const FROM = new Date(2026, 7, 15, 13, 18).getTime();
const X = timeTicks(FROM, FROM + 24 * 3600_000, 8);

describe("Axis", () => {
  describe("Grid", () => {
    it("draws a line for every tick, on both axes", () => {
      const c = draw(<Grid rect={RECT} y={Y} x={X} />);
      expect(c.querySelectorAll("[data-grid]").length).toBe(
        Y.length + X.length,
      );
    });

    // Where a minor and a major line coincide the heavier one has to win, so
    // the majors are painted last. In SVG that is document order.
    it("paints major lines after minor ones so the heavier wins", () => {
      const c = draw(<Grid rect={RECT} y={Y} x={X} />);
      const lines = [...c.querySelectorAll("[data-grid]")];
      const firstMajor = lines.findIndex((l) => l.hasAttribute("data-major"));
      const lastMinor = lines.reduce(
        (last, l, i) => (l.hasAttribute("data-major") ? last : i),
        -1,
      );
      expect(firstMajor).toBeGreaterThan(lastMinor);
    });

    it("uses the lighter ink for the unlabelled helper lines", () => {
      const c = draw(<Grid rect={RECT} y={Y} x={X} />);
      const minor = c.querySelector("[data-grid]:not([data-major])");
      const major = c.querySelector("[data-grid][data-major]");
      expect(minor?.getAttribute("stroke")).toBe("var(--grid-minor)");
      expect(major?.getAttribute("stroke")).toBe("var(--grid)");
    });

    it("spans the plot rect, not the whole image", () => {
      const c = draw(<Grid rect={RECT} y={Y} x={[]} />);
      const row = c.querySelector("[data-grid]");
      expect(row?.getAttribute("x1")).toBe(String(RECT.left));
      expect(row?.getAttribute("x2")).toBe(String(RECT.right));
    });

    it("draws nothing when there are no ticks", () => {
      const c = draw(<Grid rect={RECT} />);
      expect(c.querySelectorAll("[data-grid]").length).toBe(0);
    });
  });

  describe("Spine", () => {
    it("frames the plot with one L-shaped path", () => {
      const c = draw(<Spine rect={RECT} y={Y} x={X} />);
      const path = c.querySelector("[data-spine] path");
      // Down the left, then along the bottom.
      expect(path?.getAttribute("d")).toBe(
        `M${RECT.left + 0.5},${RECT.top} L${RECT.left + 0.5},${RECT.bottom - 0.5} L${RECT.right},${RECT.bottom - 0.5}`,
      );
    });

    // Unlabelled helper marks are the point: they survive where a dense
    // series has painted over the grid.
    it("marks every tick, not only the labelled ones", () => {
      const c = draw(<Spine rect={RECT} y={Y} x={X} />);
      expect(c.querySelectorAll("[data-tick]").length).toBe(
        Y.length + X.length,
      );
    });

    it("draws a longer mark at a labelled tick than at a helper", () => {
      const c = draw(<Spine rect={RECT} y={Y} x={[]} />);
      const ticks = [...c.querySelectorAll("[data-tick]")];
      const lengthOf = (el: Element) =>
        Math.abs(Number(el.getAttribute("x1")) - Number(el.getAttribute("x2")));
      const majorIndex = Y.findIndex((t) => t.major);
      const minorIndex = Y.findIndex((t) => !t.major);
      expect(lengthOf(ticks[majorIndex]!)).toBeGreaterThan(
        lengthOf(ticks[minorIndex]!),
      );
    });
  });

  describe("AxisLabels", () => {
    const format = (v: number) => `${v / 1e6} MB/s`;

    it("labels the major ticks and leaves the helpers unlabelled", () => {
      const c = draw(<AxisLabels rect={RECT} y={Y} format={format} />);
      expect(c.querySelectorAll('[data-axis-label="y"]').length).toBe(
        Y.filter((t) => t.major).length,
      );
    });

    it("puts a label at the height of the tick it names", () => {
      const c = draw(<AxisLabels rect={RECT} y={Y} format={format} />);
      const first = Y.filter((t) => t.major)[0]!;
      const label = c.querySelector('[data-axis-label="y"]');
      expect(Number(label?.getAttribute("y"))).toBeCloseTo(
        yAt(RECT, first.fraction),
        6,
      );
    });

    it("draws no value labels without a formatter", () => {
      const c = draw(<AxisLabels rect={RECT} y={Y} />);
      expect(c.querySelectorAll('[data-axis-label="y"]').length).toBe(0);
    });

    // A label centred on fraction 0 hangs half its width into the gutter
    // beside it, and one centred on fraction 1 hangs off the image.
    it("anchors the end labels inward so they cannot be clipped", () => {
      const ends = [
        { fraction: 0, major: true, label: "start" },
        { fraction: 0.5, major: true, label: "middle" },
        { fraction: 1, major: true, label: "end" },
      ];
      const c = draw(<AxisLabels rect={RECT} x={ends} />);
      const anchors = [...c.querySelectorAll('[data-axis-label="x"]')].map(
        (l) => l.getAttribute("text-anchor"),
      );
      expect(anchors).toEqual(["start", "middle", "end"]);
    });

    it("sets the size the margins were computed for", () => {
      const c = draw(<AxisLabels rect={RECT} y={Y} format={format} />);
      const label = c.querySelector('[data-axis-label="y"]');
      expect(label?.getAttribute("font-size")).toBe("12");
    });
  });

  describe("ZeroRule", () => {
    // On a mirrored chart zero is the line every reading is measured from,
    // so it must outrank both the grid and the spine.
    it("is drawn in stronger ink than any gridline", () => {
      const c = draw(<ZeroRule rect={RECT} at={0.5} />);
      const zero = c.querySelector("[data-zero]");
      expect(zero?.getAttribute("stroke")).toBe("var(--axis)");
    });

    it("sits at the fraction it is given", () => {
      const ticks = mirroredTicks(800);
      const middle = ticks.find((t) => t.value === 0)!;
      const c = draw(<ZeroRule rect={RECT} at={middle.fraction} />);
      expect(Number(c.querySelector("[data-zero]")?.getAttribute("y1"))).toBe(
        yAt(RECT, 0.5),
      );
    });
  });
});
