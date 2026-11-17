import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { areaPath, extent, linePath } from "./geometry";
import { Sparkline } from "./Sparkline";

describe("Sparkline", () => {
  it("draws one path per unbroken run, so a gap is a hole", () => {
    const { container } = render(<Sparkline values={[1, 2, null, 4]} />);
    expect(container.querySelectorAll("path[data-line]")).toHaveLength(2);
  });

  it("continues the line dotted at its last level when the host goes silent", () => {
    // Given a trend that stopped reporting halfway, last reading mid-box
    const { container } = render(
      <Sparkline
        values={[1, 3, 2, null, null]}
        width={100}
        height={20}
        pad={0}
      />,
    );

    // Then the silent half is one dotted rule from the last reading to the
    // box edge at that reading's height -- not dropped to the floor -- and
    // it is not a data line
    const gaps = Array.from(container.querySelectorAll("line[data-gap]"));
    expect(gaps).toHaveLength(1);
    expect(gaps[0]!.getAttribute("x1")).toBe("50");
    expect(gaps[0]!.getAttribute("x2")).toBe("100");
    expect(gaps[0]!.getAttribute("y1")).toBe("10");
    expect(gaps[0]!.getAttribute("y2")).toBe("10");
    expect(gaps[0]!.getAttribute("stroke-dasharray")).not.toBeNull();
    expect(container.querySelectorAll("path[data-line]")).toHaveLength(1);
  });

  it("bridges an interior gap from the last reading to the next one", () => {
    const { container } = render(
      <Sparkline
        values={[1, 3, null, null, 2]}
        width={100}
        height={20}
        pad={0}
      />,
    );
    const gaps = Array.from(container.querySelectorAll("line[data-gap]"));
    expect(
      gaps.map((g) => ["x1", "y1", "x2", "y2"].map((a) => g.getAttribute(a))),
      // 3 is the ceiling: inset half a dot so it is not clipped at pad 0.
    ).toEqual([["25", "0.6", "100", "10"]]);
  });

  it("rules the baseline when the window holds no reading at all", () => {
    const { container } = render(
      <Sparkline values={[null, null, null]} width={100} height={20} pad={0} />,
    );
    const gaps = Array.from(container.querySelectorAll("line[data-gap]"));
    expect(gaps).toHaveLength(1);
    expect(gaps[0]!.getAttribute("x1")).toBe("0");
    expect(gaps[0]!.getAttribute("x2")).toBe("100");
    expect(Number(gaps[0]!.getAttribute("y1"))).toBeGreaterThan(19);
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
