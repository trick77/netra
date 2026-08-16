import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChartDetail, summarise } from "./ChartDetail";
import { ChartPanel } from "./ChartPanel";

const series = [
  { name: "user", color: "var(--s1)", values: [10, 20, 30] },
  { name: "system", color: "var(--s2)", values: [1, null, 3] },
];

describe("summarise", () => {
  // A host that reported nothing for an hour did not report an hour of
  // zeroes. Counting the gaps would drag every mean toward a number nobody
  // measured.
  it("skips gaps rather than counting them as zero", () => {
    expect(summarise([10, null, 20])).toEqual({
      latest: 20,
      min: 10,
      max: 20,
      mean: 15,
    });
  });

  // The LATEST bucket, trailing nulls included: a series that has gone
  // quiet reads as absent, not as its last known value.
  it("reports a trailing gap as absent rather than the last number", () => {
    expect(summarise([10, 20, null]).latest).toBeNull();
  });

  it("reports every statistic as absent for a series with no values", () => {
    expect(summarise([null, null])).toEqual({
      latest: null,
      min: null,
      max: null,
      mean: null,
    });
  });
});

describe("ChartDetail", () => {
  it("names every series with its numbers, which the small panel has no room for", () => {
    render(
      <ChartDetail title="Processor" series={series} onClose={() => {}} />,
    );

    const row = screen.getByRole("row", { name: /user/ });
    expect(row).toHaveTextContent("30"); // latest
    expect(row).toHaveTextContent("10"); // min
  });

  it("closes on Escape", async () => {
    const onClose = vi.fn();
    render(<ChartDetail title="Processor" series={series} onClose={onClose} />);

    await userEvent.keyboard("{Escape}");

    expect(onClose).toHaveBeenCalled();
  });

  // A chart with invented times is worse than one with none.
  it("omits the time axis when the window is unknown", () => {
    render(
      <ChartDetail title="Processor" series={series} onClose={() => {}} />,
    );

    expect(document.querySelector(".cd-x")).toBeNull();
  });

  it("carries the range control when the page supplies one", async () => {
    const onRangeChange = vi.fn();
    render(
      <ChartDetail
        title="Processor"
        series={series}
        range="6h"
        onRangeChange={onRangeChange}
        onClose={() => {}}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "24h" }));

    expect(onRangeChange).toHaveBeenCalledWith("24h");
  });
});

describe("ChartDetail y axis", () => {
  const axisLabels = () =>
    Array.from(document.querySelectorAll(".cd-y span")).map((el) => ({
      top: (el as HTMLElement).style.top,
      text: el.textContent,
    }));

  // mirrorPaths() puts the baseline at h/2 and draws egress DOWNWARD from
  // it, so the midline is ZERO and both edges are a peak. The shared
  // [ceiling, ceiling/2, 0] axis said the opposite -- zero at the bottom,
  // where the largest egress actually sits -- so a saturated downlink read
  // as an idle one. Both mirrored callers ("Interface throughput" and the
  // container page's "Network") left the axis on.
  it("labels a mirrored chart with zero at the midline and a peak at each edge", () => {
    render(
      <ChartDetail
        title="Interface throughput"
        series={[
          { name: "ingress", color: "var(--s2)", values: [10, 40] },
          { name: "egress", color: "var(--s5)", values: [5, 20] },
        ]}
        max={100}
        mirrored
        onClose={() => {}}
      />,
    );

    expect(axisLabels()).toEqual([
      { top: "0%", text: "100" },
      { top: "50%", text: "0" },
      { top: "100%", text: "100" },
    ]);
  });

  // The axis is kept rather than hidden because both mirrored callers are
  // rate charts carrying unit="B/s": "how much" is the question a reader
  // enlarged them to answer, and dropping the axis answers less than
  // labelling it correctly does.
  it("still draws an axis for a mirrored chart rather than hiding it", () => {
    render(
      <ChartDetail
        title="Network"
        series={[{ name: "ingress", color: "var(--s2)", values: [1, 2] }]}
        max={10}
        mirrored
        onClose={() => {}}
      />,
    );

    expect(document.querySelector(".cd-y")).not.toBeNull();
  });

  // The unmirrored axis is unchanged: top is the ceiling, bottom is zero.
  it("leaves the ordinary axis running from the ceiling down to zero", () => {
    render(
      <ChartDetail
        title="Processor"
        series={series}
        max={100}
        onClose={() => {}}
      />,
    );

    expect(axisLabels()).toEqual([
      { top: "0%", text: "100" },
      { top: "50%", text: "50" },
      { top: "100%", text: "0" },
    ]);
  });

  // hideAxis still wins: an unnormalised per-core stack runs to N x 100 and
  // its height is a shape, not a quantity.
  it("draws no axis at all when the caller hides it", () => {
    render(
      <ChartDetail
        title="Per-core"
        series={series}
        max={400}
        stacked
        hideAxis
        onClose={() => {}}
      />,
    );

    expect(document.querySelector(".cd-y")).toBeNull();
  });
});

describe("ChartPanel enlargement", () => {
  // The chart is the affordance, and it has to work from the keyboard: a
  // div with a click handler would be neither.
  it("opens the enlarged view from the chart itself", async () => {
    render(<ChartPanel title="Processor" series={series} />);

    await userEvent.click(
      screen.getByRole("button", { name: /enlarge processor/i }),
    );

    expect(
      screen.getByRole("dialog", { name: /processor, enlarged/i }),
    ).toBeInTheDocument();
  });

  it("does not open one for a panel that has nothing to show", () => {
    render(<ChartPanel title="Container health" unavailable="no columns" />);

    expect(screen.queryByRole("button", { name: /enlarge/i })).toBeNull();
  });
});
