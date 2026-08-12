import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Overlay } from "./Overlay";
import { ChartDetail } from "./ChartDetail";
import { stackBands } from "./geometry";

// The stacked mark exists so the host page can draw the same chart the fleet
// columns do. The thing worth pinning is that it really is the SAME maths --
// a second implementation that merely looks similar would drift, and the two
// views of one host would quietly disagree.
describe("Overlay, stacked", () => {
  const series = [
    { name: "used", color: "var(--s1)", values: [10, 20, 30] },
    { name: "cached", color: "var(--s2)", values: [5, 5, 5] },
  ];

  it("draws one filled band per series instead of lines", () => {
    const { container } = render(<Overlay series={series} max={100} stacked />);

    expect(container.querySelectorAll("path[data-band]")).toHaveLength(2);
    expect(container.querySelectorAll("path[data-line]")).toHaveLength(0);
  });

  it("draws exactly what stackBands would, so a panel and its enlarged view cannot diverge", () => {
    const { container } = render(
      <Overlay
        series={series}
        max={100}
        width={120}
        height={32}
        pad={2}
        stacked
      />,
    );

    const want = stackBands(
      series.map((s) => s.values),
      120,
      32,
      100,
      2,
    );
    const got = [...container.querySelectorAll("path[data-band]")].map((p) =>
      p.getAttribute("d"),
    );
    expect(got).toEqual(want);
  });

  // A stack scaled to its own running total always touches the top of the
  // box, which is the always-full reading the fleet columns carry explicit
  // ceilings to avoid. `max` must be used verbatim.
  it("honours the ceiling it is given rather than the data's own peak", () => {
    const { container } = render(
      <Overlay
        series={series}
        max={1000}
        width={120}
        height={32}
        pad={0}
        stacked
      />,
    );

    const want = stackBands(
      series.map((s) => s.values),
      120,
      32,
      1000,
      0,
    );
    const got = [...container.querySelectorAll("path[data-band]")].map((p) =>
      p.getAttribute("d"),
    );
    expect(got).toEqual(want);
  });

  it("keeps drawing lines when it is not told to stack", () => {
    const { container } = render(<Overlay series={series} max={100} />);

    expect(container.querySelectorAll("path[data-band]")).toHaveLength(0);
    expect(
      container.querySelectorAll("path[data-line]").length,
    ).toBeGreaterThan(0);
  });
});

describe("ChartDetail, stacked", () => {
  const series = [
    { name: "used", color: "var(--s1)", values: [10, 20, 30] },
    { name: "cached", color: "var(--s2)", values: [5, 5, 5] },
  ];

  // Enlarging a chart must not change what it is. A stacked panel that opened
  // into overlaid lines would be a different claim about the same data.
  it("keeps the stacked mark when a panel is enlarged", () => {
    const { container } = render(
      <ChartDetail
        title="Memory"
        series={series}
        max={100}
        stacked
        onClose={() => {}}
      />,
    );

    expect(container.querySelectorAll("path[data-band]")).toHaveLength(2);
  });

  // Without an explicit max the ceiling is derived, and for a stack the
  // largest single value is the wrong question: the stack is as tall as the
  // running TOTAL, so a peak of 30 would draw a 35-tall stack out of the box.
  it("derives a ceiling from the running total, not the largest single value", () => {
    const { container } = render(
      <ChartDetail title="Memory" series={series} stacked onClose={() => {}} />,
    );

    // The y-axis labels name the ceiling the geometry used.
    const top = container.querySelector(".cd-y span");
    expect(top?.textContent).toBe("35");
  });
});
