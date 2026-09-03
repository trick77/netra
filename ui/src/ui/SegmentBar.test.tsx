import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { SEGMENT_CELLS, SegmentBar, litCells } from "./SegmentBar";

describe("litCells", () => {
  it("lights the nearest tenth", () => {
    expect(litCells(0)).toBe(0);
    expect(litCells(4)).toBe(0);
    expect(litCells(5)).toBe(1);
    expect(litCells(68)).toBe(7);
    expect(litCells(91)).toBe(9);
    expect(litCells(100)).toBe(10);
  });

  // A percentage past the ceiling -- a meter fed a value over its max --
  // fills the row rather than indexing past it, and a NaN lights nothing.
  it("clamps to the row", () => {
    expect(litCells(140)).toBe(SEGMENT_CELLS);
    expect(litCells(-3)).toBe(0);
    expect(litCells(Number.NaN)).toBe(0);
  });
});

describe("SegmentBar", () => {
  it("draws ten cells with the lit ones marked", () => {
    const { container } = render(<SegmentBar pct={68} />);
    const cells = container.querySelectorAll(".segbar i");
    expect(cells).toHaveLength(SEGMENT_CELLS);
    expect(container.querySelectorAll(".segbar i.on")).toHaveLength(7);
    // Lit from the left, not scattered.
    expect(cells[6]!.className).toBe("on");
    expect(cells[7]!.className).toBe("");
  });

  // The Meter's own thresholds: 70 warning, 85 serious, 95 critical. The
  // whole bar takes the class, so a host at 76 is an amber bar and not a
  // green one with an amber cell at the end.
  it("takes the meter's severity as a class on the whole bar", () => {
    const at = (pct: number) =>
      render(<SegmentBar pct={pct} />).container.querySelector(".segbar")!
        .className;
    expect(at(21)).toBe("segbar st-ok");
    expect(at(70)).toBe("segbar st-warn");
    expect(at(85)).toBe("segbar st-serious");
    expect(at(96)).toBe("segbar st-crit");
  });

  it("is a meter to assistive tech, with the rounded value", () => {
    const { container } = render(<SegmentBar pct={67.6} label="CPU now" />);
    const bar = container.querySelector(".segbar")!;
    expect(bar.getAttribute("role")).toBe("meter");
    expect(bar.getAttribute("aria-valuenow")).toBe("68");
    expect(bar.getAttribute("aria-label")).toBe("CPU now");
  });
});
