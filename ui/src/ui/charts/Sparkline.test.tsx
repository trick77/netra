import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { areaPath, extent, linePath } from "./geometry";
import { Sparkline } from "./Sparkline";

describe("Sparkline", () => {
  it("draws one path per unbroken run, so a gap is a hole", () => {
    const { container } = render(<Sparkline values={[1, 2, null, 4]} />);
    expect(container.querySelectorAll("path[data-line]")).toHaveLength(2);
  });

  it("marks the isolated point as a dot, not a joined segment", () => {
    const { container } = render(<Sparkline values={[1, 2, null, 4]} />);
    expect(
      container.querySelectorAll("path[data-line][data-point]"),
    ).toHaveLength(1);
  });

  it("never joins two subpaths into a single d, so a gap can't be bridged", () => {
    const { container } = render(<Sparkline values={[1, 2, null, 4]} />);
    container
      .querySelectorAll("path[data-line]:not([data-point])")
      .forEach((p) => {
        const d = p.getAttribute("d") ?? "";
        expect(d.match(/M/g)?.length ?? 0).toBe(1);
      });
  });

  it("renders the exact geometry output with no post-processing", () => {
    const values = [1, 2, null, 4];
    const { min, max } = extent(values);
    const { paths } = linePath(values, 120, 32, min, max, 2);
    const areas = areaPath(paths, 120, 32, 2).filter((d) => d !== "");
    const { container } = render(
      <Sparkline values={values} width={120} height={32} pad={2} />,
    );
    const lineEls = Array.from(
      container.querySelectorAll("path[data-line]:not([data-point])"),
    );
    expect(lineEls.map((p) => p.getAttribute("d"))).toEqual(paths);
    const areaEls = Array.from(container.querySelectorAll("path[data-area]"));
    expect(areaEls.map((p) => p.getAttribute("d"))).toEqual(areas);
  });

  it("gives the chart an accessible name", () => {
    render(<Sparkline values={[1, 2, 3]} label="CPU busy trend" />);
    expect(
      screen.getByRole("img", { name: "CPU busy trend" }),
    ).toBeInTheDocument();
  });

  it("takes its colour from the caller, never chooses one itself", () => {
    const { container } = render(
      <Sparkline values={[1, 2, 3]} color="var(--s3)" />,
    );
    const line = container.querySelector("path[data-line]:not([data-point])");
    expect(line?.getAttribute("stroke")).toBe("var(--s3)");
  });
});
