import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { areaPath, extent, linePath } from "./geometry";
import { Sparkline } from "./Sparkline";

describe("Sparkline", () => {
  it("draws one path per unbroken run, so a gap is a hole", () => {
    const { container } = render(<Sparkline values={[1, 2, null, 4]} />);
    expect(container.querySelectorAll("path[data-line]")).toHaveLength(2);
  });

  it("rules a dotted baseline across a stretch with no reading", () => {
    // Given a trend that stopped reporting halfway
    const { container } = render(
      <Sparkline
        values={[1, 2, 3, null, null]}
        width={100}
        height={20}
        pad={0}
      />,
    );

    // Then the silent half is one dotted rule on the baseline, from the
    // last reading to the box edge, and it is not a data line
    const gaps = Array.from(container.querySelectorAll("line[data-gap]"));
    expect(gaps).toHaveLength(1);
    expect(gaps[0]!.getAttribute("x1")).toBe("50");
    expect(gaps[0]!.getAttribute("x2")).toBe("100");
    expect(Number(gaps[0]!.getAttribute("y1"))).toBeGreaterThan(19);
    expect(gaps[0]!.getAttribute("stroke-dasharray")).not.toBeNull();
    expect(container.querySelectorAll("path[data-line]")).toHaveLength(1);
  });

  it("draws no rule for a trend with no slots at all", () => {
    const { container } = render(<Sparkline values={[]} />);
    expect(container.querySelector("line[data-gap]")).toBeNull();
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
