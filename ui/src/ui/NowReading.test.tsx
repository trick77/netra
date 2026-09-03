import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { NowReading } from "./NowReading";

describe("NowReading", () => {
  it("prints the rounded figure beside the bar and the unit line under it", () => {
    const { container } = render(
      <NowReading pct={68.4} under="of 8 cores" label="CPU now" />,
    );
    expect(container.querySelector(".metric-now .segbar")).toBeInTheDocument();
    expect(container.querySelector(".metric-now .v")?.textContent).toBe("68%");
    expect(container.querySelector(".u")?.textContent).toBe("of 8 cores");
  });

  // The figure and the bar are one reading, so the figure takes the bar's
  // severity -- and no class at all when there is nothing to say, so a calm
  // row is plain ink rather than green.
  it("colours the figure with the bar's severity, and only then", () => {
    const at = (pct: number) =>
      render(<NowReading pct={pct} />).container.querySelector(
        ".metric-now .v",
      )!.className;
    expect(at(21)).toBe("v");
    expect(at(76)).toBe("v st-warn");
    expect(at(96)).toBe("v st-crit");
  });

  it("omits the unit line when there is nothing to measure against", () => {
    const { container } = render(<NowReading pct={12} />);
    expect(container.querySelector(".u")).toBeNull();
  });

  // A caller that has to find its own figure -- the disk cell's tests look
  // for .dpct -- can hang a class on it without losing the severity.
  it("keeps a caller's class on the figure alongside the severity", () => {
    const { container } = render(<NowReading pct={76} className="dpct" />);
    expect(container.querySelector(".dpct.st-warn")).toBeInTheDocument();
  });
});
