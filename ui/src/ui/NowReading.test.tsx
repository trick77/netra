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

  // Judged once: the bar is handed the severity the figure was coloured
  // with, so the two cannot disagree at a threshold.
  it("gives the bar the same severity as the figure", () => {
    const { container } = render(<NowReading pct={86} />);
    expect(container.querySelector(".segbar")?.className).toBe(
      "segbar st-serious",
    );
    expect(container.querySelector(".metric-now .v")?.className).toBe(
      "v st-serious",
    );
  });

  // The disk cell hands over markup rather than a string: the mount and the
  // bytes left are two spans the stylesheet lays out as one line.
  it("accepts markup under the bar", () => {
    const { container } = render(
      <NowReading
        pct={40}
        under={
          <>
            <span className="dmount">/</span>
            <span className="dfree">14 GB left</span>
          </>
        }
      />,
    );
    expect(container.querySelector(".u .dmount")?.textContent).toBe("/");
    expect(container.querySelector(".u .dfree")?.textContent).toBe(
      "14 GB left",
    );
  });
});
