import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChartPanel } from "./ChartPanel";
import { ABSENT } from "../../lib/format";

describe("ChartPanel", () => {
  it("draws one path per unbroken run, so a gap is a hole", () => {
    const { container } = render(
      <ChartPanel
        title="CPU"
        series={[{ name: "busy", color: "var(--s1)", values: [1, 2, null, 4] }]}
      />,
    );
    expect(container.querySelectorAll("path[data-line]")).toHaveLength(2);
  });

  it("renders the not-collected panel instead of an empty chart", () => {
    render(
      <ChartPanel
        title="ICMP statistics"
        unavailable="no ICMP columns in the schema"
      />,
    );
    expect(screen.getByText(/not collected/i)).toBeInTheDocument();
    expect(screen.getByText(/no ICMP columns/i)).toBeInTheDocument();
  });

  it("surfaces a window notice when the served range was clamped", () => {
    render(
      <ChartPanel title="Processes" series={[]} notice="48 hours retained" />,
    );
    expect(screen.getByText(/48 hours retained/)).toBeInTheDocument();
  });

  it("shows a legend once there are two or more series", () => {
    render(
      <ChartPanel
        title="Network"
        series={[
          { name: "rx", color: "var(--s1)", values: [1, 2, 3] },
          { name: "tx", color: "var(--s2)", values: [1, 2, 3] },
        ]}
      />,
    );
    expect(screen.getByText("rx")).toBeInTheDocument();
    expect(screen.getByText("tx")).toBeInTheDocument();
  });

  it("formats the latest value with the caller's formatter", () => {
    render(
      <ChartPanel
        title="Memory"
        unit="GB"
        fmt={(n) => (n === null ? "—" : `${n} GB`)}
        series={[{ name: "used", color: "var(--s1)", values: [1, 2, 3] }]}
      />,
    );
    expect(screen.getByText("3 GB")).toBeInTheDocument();
  });

  it("does not render the unavailable box when data is present", () => {
    render(
      <ChartPanel
        title="CPU"
        series={[{ name: "busy", color: "var(--s1)", values: [1, 2, 3] }]}
      />,
    );
    expect(screen.queryByText(/not collected/i)).not.toBeInTheDocument();
  });

  // A host that stopped reporting two buckets ago draws its hole correctly.
  // The headline used to filter the nulls out and print the last value that
  // ever arrived, in bold, beside that hole -- "the agent is down" rendered
  // as "CPU is at 43".
  it("reports the latest bucket as absent when the series ends in a gap", () => {
    render(
      <ChartPanel
        title="CPU"
        series={[
          { name: "busy", color: "var(--s1)", values: [42, 43, null, null] },
        ]}
      />,
    );

    expect(screen.queryByText("43")).toBeNull();
    expect(screen.getByText(ABSENT)).toBeInTheDocument();
  });

  // With one series the unit says everything; with several, series[0]'s
  // number under a bare unit reads as the panel's total.
  it("names the series the headline belongs to when there is more than one", () => {
    render(
      <ChartPanel
        title="Network"
        series={[
          { name: "rx", color: "var(--s1)", values: [10] },
          { name: "tx", color: "var(--s2)", values: [90] },
        ]}
      />,
    );

    // The legend names every series too, so this asserts on the headline
    // specifically rather than on the panel as a whole.
    expect(document.querySelector(".now")?.textContent).toContain("rx");
  });

  // percent(), bytes() and bitrate() all carry their own unit, so printing
  // the panel's unit after one of them rendered "12 % %".
  it("does not print the unit twice when the formatter carries one", () => {
    render(
      <ChartPanel
        title="CPU"
        unit="%"
        fmt={(v) => (v === null ? "none" : `${v} %`)}
        series={[{ name: "busy", color: "var(--s1)", values: [12] }]}
      />,
    );

    expect(document.querySelector(".now")?.textContent).toBe("12 %");
    expect(document.querySelector(".u")).toBeNull();
  });
});
