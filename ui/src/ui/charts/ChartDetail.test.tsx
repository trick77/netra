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
    render(<ChartPanel title="ICMP statistics" unavailable="no columns" />);

    expect(screen.queryByRole("button", { name: /enlarge/i })).toBeNull();
  });
});
