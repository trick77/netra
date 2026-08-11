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
});
