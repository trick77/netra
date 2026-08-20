import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Overlay } from "./Overlay";
import { ChartDetail } from "./ChartDetail";
import { ChartPanel } from "./ChartPanel";
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

    // The y-axis labels name the ceiling the geometry used. They are SVG
    // text inside the plot, so the topmost is the one with the smallest y.
    const labels = Array.from(
      container.querySelectorAll('[data-axis-label="y"]'),
    ).sort((a, b) => Number(a.getAttribute("y")) - Number(b.getAttribute("y")));
    // The tick chooser rounds to a readable step, so the top label is the
    // highest nice value UNDER the ceiling rather than the ceiling itself.
    // What matters is that it is above the largest single band: a ceiling
    // taken from that instead of the running total would top the axis out at
    // 20 and draw the stack straight out of the box.
    expect(Number(labels[0]?.textContent)).toBeGreaterThan(20);
  });
});

describe("ChartPanel, stacked", () => {
  const series = [
    { name: "a", color: "var(--s1)", values: [10, 20] },
    { name: "b", color: "var(--s2)", values: [30, 30] },
  ];

  // Without an explicit ceiling the panel derives one, and for a stack the
  // largest single value is the wrong question: the stack is as tall as the
  // running TOTAL, so a peak of 30 would draw a 50-tall stack outside the
  // box. This is what the unnormalised per-core chart relies on -- N cores
  // stack to N x 100 and nothing declares that number in advance.
  it("derives its ceiling from the running total, not the largest value", () => {
    const { container } = render(
      <ChartPanel
        title="Stacked"
        series={series}
        stacked
        width={100}
        height={20}
      />,
    );

    const want = stackBands(
      series.map((s) => s.values),
      100,
      20,
      50,
      2,
    );
    const got = [...container.querySelectorAll("path[data-band]")].map((p) =>
      p.getAttribute("d"),
    );
    expect(got).toEqual(want);
  });

  // An explicit ceiling still wins: the memory chart passes mem_total plus
  // headroom, and deriving one from the data would lose the free gap.
  it("prefers the ceiling it is given", () => {
    const { container } = render(
      <ChartPanel
        title="Stacked"
        series={series}
        stacked
        max={200}
        width={100}
        height={20}
      />,
    );

    const want = stackBands(
      series.map((s) => s.values),
      100,
      20,
      200,
      2,
    );
    const got = [...container.querySelectorAll("path[data-band]")].map((p) =>
      p.getAttribute("d"),
    );
    expect(got).toEqual(want);
  });
});
