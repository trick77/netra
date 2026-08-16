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
        title="Container health"
        unavailable="the agent reads health from the Docker socket, but it reaches neither the wire nor the schema"
      />,
    );
    expect(screen.getByText(/not collected/i)).toBeInTheDocument();
    expect(
      screen.getByText(/neither the wire nor the schema/i),
    ).toBeInTheDocument();
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

  // The header can afford to carry the ceiling with the reading; the axis,
  // the tooltips and the stats table cannot, and they all read `fmt`.
  it("formats the headline with nowFmt, leaving fmt for the chart", () => {
    render(
      <ChartPanel
        title="Memory"
        fmt={(v) => `${v} GiB`}
        nowFmt={(v) => `${v} · 31 GiB`}
        series={[
          { name: "used", color: "var(--s1)", values: [20] },
          { name: "cached", color: "var(--s2)", values: [4] },
        ]}
      />,
    );

    expect(document.querySelector(".now")?.textContent).toBe(
      "used 20 · 31 GiB",
    );
  });

  // The series name says WHOSE number the headline is. A headline belonging
  // to no single series must not wear one -- the per-core CPU stack read
  // "core 0 6 %" for a number that is the whole host's.
  it("drops the series name when the headline is not a series'", () => {
    render(
      <ChartPanel
        title="Processor"
        fmt={(v) => `${v}%`}
        nowValue={6}
        series={[
          { name: "core 0", color: "var(--s1)", values: [43] },
          { name: "core 1", color: "var(--s2)", values: [12] },
        ]}
      />,
    );

    const now = document.querySelector(".now")?.textContent;
    expect(now).toBe("6%");
    expect(now).not.toContain("core");
  });

  // Absent is absent here too: a caller handing in null for the latest
  // bucket must not fall back to series[0].
  it("renders a null nowValue as absent, not series[0]'s number", () => {
    render(
      <ChartPanel
        title="Processor"
        fmt={(v) => (v === null ? ABSENT : `${v}%`)}
        nowValue={null}
        series={[{ name: "core 0", color: "var(--s1)", values: [43] }]}
      />,
    );

    expect(document.querySelector(".now")?.textContent).toBe(ABSENT);
  });

  // A formatter carrying a magnitude still needs the panel's unit to say
  // what KIND of quantity it is: bytes() renders "1.2 MB" for a link doing
  // 1.2 MB/s, and only "B/s" makes that a rate rather than a total.
  // (A formatter that prints its own unit must simply not be given one --
  // that contract is on the prop, not enforced here.)
  it("prints the unit beside a formatted value", () => {
    render(
      <ChartPanel
        title="Interface throughput"
        unit="B/s"
        fmt={(v) => (v === null ? "none" : `${v} MB`)}
        series={[{ name: "rx", color: "var(--s1)", values: [12] }]}
      />,
    );

    expect(document.querySelector(".now")?.textContent).toBe("12 MB");
    expect(document.querySelector(".u")?.textContent).toBe("B/s");
  });

  // stackBands() scales from ZERO -- it divides a running total by `max`,
  // with no min at all -- so the reference rule has to be placed against the
  // same floor. Without min={0} the small panel used the data's own derived
  // minimum, and the dashed mem_total rule landed at the wrong height: the
  // gap between the stack and the rule IS the free-memory reading, so it
  // misstated exactly what the chart is for. The enlarged view always passed
  // min={0}, so a panel and the chart it opened disagreed.
  it("places a stacked panel's reference rule against the same zero floor the stack is scaled from", () => {
    const { container } = render(
      <ChartPanel
        title="Memory"
        stacked
        // A floor well above zero is what made the bug visible: with the
        // data's own minimum the rule moved, with zero it does not.
        series={[{ name: "used", color: "var(--s1)", values: [800, 900] }]}
        max={1000}
        reference={1000}
        height={64}
      />,
    );

    const rule = container.querySelector("svg [data-reference]")!;
    // height - pad - (reference / max) * (height - 2 * pad), pad = 2:
    // 64 - 2 - 1 * 60 = 2. A derived floor of 800 would put it at 2 as well
    // only by coincidence, so the mid-scale case below is the real check.
    expect(Number(rule.getAttribute("y1"))).toBeCloseTo(2, 5);
  });

  it("keeps the rule where zero-scaling puts it, not where the data floor would", () => {
    const { container } = render(
      <ChartPanel
        title="Memory"
        stacked
        series={[{ name: "used", color: "var(--s1)", values: [800, 900] }]}
        max={1000}
        reference={500}
        height={64}
      />,
    );

    const rule = container.querySelector("svg [data-reference]")!;
    // Scaled from zero: 64 - 2 - 0.5 * 60 = 32, the midline.
    // Scaled from the data's floor of 800 the fraction would be negative and
    // the rule would fall off the bottom of the box entirely.
    expect(Number(rule.getAttribute("y1"))).toBeCloseTo(32, 5);
  });

  // Scoped to the stacked case on purpose: effectiveMin also feeds
  // linePath(), where auto-scaling off the data's floor is deliberate. A
  // temperature series between 44 and 47 degrees must not be redrawn as a
  // flat line pinned to the bottom of a 0-47 box.
  it("leaves an unstacked panel auto-scaling off its own data floor", () => {
    const { container } = render(
      <ChartPanel
        title="Temperature"
        series={[{ name: "cpu", color: "var(--s1)", values: [44, 47] }]}
        max={47}
        height={64}
      />,
    );

    const paths = Array.from(
      container.querySelectorAll("svg path[data-line]"),
    ).map((el) => el.getAttribute("d") ?? "");
    // The line spans the box rather than sitting flat near its top: with a
    // floor of 44 the two points are the extremes of the plot area.
    const ys = paths
      .join(" ")
      .match(/-?\d+(?:\.\d+)?,(-?\d+(?:\.\d+)?)/g)!
      .map((pair) => Number(pair.split(",")[1]));
    expect(Math.max(...ys) - Math.min(...ys)).toBeGreaterThan(50);
  });

  // `footer` exists so a reading that qualifies a chart can be drawn with it
  // rather than after the grid the chart sits in -- see the prop's own comment.
  it("draws a footer under the chart", () => {
    render(
      <ChartPanel
        title="Memory"
        series={[{ name: "used", color: "var(--s1)", values: [1, 2] }]}
        footer={<p>90%</p>}
      />,
    );

    const panel = screen.getByLabelText("Memory chart", {
      selector: "section",
    });
    expect(panel.querySelector(".foot")).toHaveTextContent("90%");
  });

  // The not-collected branch returns before the footer, and must: a panel that
  // collected nothing has no reading to qualify, so a meter or a headline
  // underneath it would state a measurement the panel just denied having.
  it("suppresses the footer when the panel is not collected", () => {
    render(
      <ChartPanel
        title="Memory"
        series={[{ name: "used", color: "var(--s1)", values: [1, 2] }]}
        unavailable="No cgroup memory controller on this host."
        footer={<p>90%</p>}
      />,
    );

    const panel = screen.getByLabelText("Memory, not collected", {
      selector: "section",
    });
    expect(panel.querySelector(".foot")).toBeNull();
    expect(panel).not.toHaveTextContent("90%");
  });
});
