import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Overlay } from "./Overlay";

describe("Overlay", () => {
  const series = [
    { name: "Host A", color: "var(--s1)", values: [1, 2, null, 4] },
    { name: "Host B", color: "var(--s2)", values: [1, 2, 3, 4] },
  ];

  it("splits only the series that actually has a gap", () => {
    const { container } = render(<Overlay series={series} max={4} />);
    const a = container.querySelector('g[data-series="Host A"]');
    const b = container.querySelector('g[data-series="Host B"]');
    expect(
      a?.querySelectorAll("path[data-line]:not([data-point])"),
    ).toHaveLength(1);
    expect(a?.querySelectorAll("path[data-line][data-point]")).toHaveLength(1);
    expect(b?.querySelectorAll("path[data-line]")).toHaveLength(1);
  });

  // The Memory panel scales its max to mem_total * 1.08, which puts the
  // ceiling rule at ~7% of the height -- about 4.7px on a 64px chart. The
  // label sits 5px above the line, so its baseline landed outside the
  // viewBox and the text rendered cut in half.
  it("keeps a reference label inside the chart when the rule is near the top", () => {
    const { container } = render(
      <Overlay
        series={series}
        max={108}
        reference={100}
        referenceLabel="31.0 GiB"
        min={0}
        height={64}
        pad={0}
      />,
    );
    const label = container.querySelector("text[data-reference-label]");
    const rule = container.querySelector("line[data-reference]");
    const y = Number(label?.getAttribute("y"));
    const ruleY = Number(rule?.getAttribute("y1"));
    expect(ruleY).toBeLessThan(14);
    // Flipped below the rule, and inside the box.
    expect(y).toBeGreaterThan(ruleY);
    expect(y).toBeLessThanOrEqual(64);
  });

  it("keeps a reference label above the rule when there is room", () => {
    const { container } = render(
      <Overlay
        series={series}
        max={100}
        reference={20}
        referenceLabel="31.0 GiB"
        min={0}
        height={64}
        pad={0}
      />,
    );
    const y = Number(
      container.querySelector("text[data-reference-label]")?.getAttribute("y"),
    );
    const ruleY = Number(
      container.querySelector("line[data-reference]")?.getAttribute("y1"),
    );
    expect(y).toBeLessThan(ruleY);
    expect(y).toBeGreaterThan(0);
  });

  it("scales every series against one shared extent", () => {
    const wide = [
      { name: "Small", color: "var(--s1)", values: [1, 1, 1] },
      { name: "Big", color: "var(--s2)", values: [100, 100, 100] },
    ];
    const { container } = render(
      <Overlay series={wide} max={100} width={100} height={20} pad={0} />,
    );
    const small = container.querySelector(
      'g[data-series="Small"] path[data-line]',
    );
    const big = container.querySelector('g[data-series="Big"] path[data-line]');
    const smallY = small?.getAttribute("d")?.split(/[ML,]/).filter(Boolean)[1];
    const bigY = big?.getAttribute("d")?.split(/[ML,]/).filter(Boolean)[1];
    expect(Number(bigY)).toBeLessThan(Number(smallY));
  });

  it("requires a legend for two or more series, since identity can't rest on colour alone", () => {
    render(<Overlay series={series} max={4} />);
    expect(screen.getByText("Host A")).toBeInTheDocument();
    expect(screen.getByText("Host B")).toBeInTheDocument();
  });

  it("gives the chart an accessible name", () => {
    render(<Overlay series={series} max={4} label="Two hosts, CPU busy" />);
    expect(
      screen.getByRole("img", { name: "Two hosts, CPU busy" }),
    ).toBeInTheDocument();
  });

  // max alone is half an axis. A panel declaring a ceiling of 100 whose data
  // sits at 88-92 got a floor derived from that data, so a four-point swing
  // filled a third of the box and two panels sharing max still could not be
  // compared -- which is the whole reason to declare one.
  it("honours a declared floor instead of deriving one from the data", () => {
    const { container } = render(
      <Overlay
        series={[{ name: "mem", color: "var(--s1)", values: [88, 90, 92] }]}
        min={0}
        max={100}
        width={100}
        height={100}
        pad={0}
      />,
    );

    const ys = [
      ...container.innerHTML.matchAll(/-?\d+\.?\d*,(-?\d+\.?\d*)/g),
    ].map((m) => parseFloat(m[1]!));
    // 88..92 of a 0..100 axis occupies the top eighth of the box, not a
    // third of it: every y sits between 8 and 12.
    expect(Math.min(...ys)).toBeGreaterThanOrEqual(8);
    expect(Math.max(...ys)).toBeLessThanOrEqual(12);
  });

  // §4.4's fleet overlay is nineteen hosts with one outlier named. A legend
  // there names all nineteen, which is the thing the de-emphasis exists to
  // avoid; highlight is the caller saying identity is carried by the
  // emphasis, not by a colour key.
  it("drops the legend when one series is highlighted", () => {
    const series = [
      { name: "web-01", color: "var(--s1)", values: [1, 2] },
      { name: "web-02", color: "var(--s1)", values: [3, 4] },
    ];

    const { container, rerender } = render(
      <Overlay series={series} max={10} />,
    );
    expect(container.querySelector(".legend")).toBeInTheDocument();

    rerender(<Overlay series={series} max={10} highlight="web-02" />);
    expect(container.querySelector(".legend")).not.toBeInTheDocument();
  });
});
