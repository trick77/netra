import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
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
  // The labels are SVG text inside the plot now, not an HTML gutter beside
  // it, and that is the point rather than an implementation detail: both
  // scale with the viewBox, so a label cannot drift from the gridline it
  // names when the panel is narrower than the width the chart was drawn at.
  // Read top-to-bottom, which for SVG means by ascending y.
  const axisLabels = () =>
    Array.from(document.querySelectorAll('[data-axis-label="y"]'))
      .map((el) => ({
        y: Number(el.getAttribute("y")),
        text: el.textContent,
      }))
      .sort((a, b) => a.y - b.y)
      .map((t) => t.text);

  // mirrorPaths() puts the baseline at h/2 and draws egress DOWNWARD from
  // it, so the midline is ZERO and both edges are a peak. The shared
  // [ceiling, ceiling/2, 0] axis said the opposite -- zero at the bottom,
  // where the largest egress actually sits -- so a saturated downlink read
  // as an idle one. Both mirrored callers (the host page's "Traffic" and the
  // container page's "Network") left the axis on.
  it("labels a mirrored chart with zero at the midline and a peak at each edge", () => {
    render(
      <ChartDetail
        title="Traffic"
        series={[
          { name: "ingress", color: "var(--s2)", values: [10, 40] },
          { name: "egress", color: "var(--s5)", values: [5, 20] },
        ]}
        max={100}
        mirrored
        onClose={() => {}}
      />,
    );

    // Symmetric about a zero midline: whatever magnitudes the tick chooser
    // lands on, the top half must mirror the bottom half and 0 must sit
    // between them.
    const labels = axisLabels();
    expect(labels).toEqual([...labels].reverse());
    expect(labels[Math.floor(labels.length / 2)]).toBe("0");
  });

  // The mean-plus-peak pair: the line is the bucket's mean and the band is
  // its peak, so the band is always the taller of the two. A ceiling derived
  // from the line alone drew the envelope outside the plot -- linePath()
  // never clamps -- so the burst the pair exists to show was the one thing
  // clipped off the top. Neither traffic spec declares a max, so this
  // derived ceiling is the only one those dialogs get.
  it("derives its ceiling from the peak band, not from the mean line", () => {
    render(
      <ChartDetail
        title="Traffic"
        series={[
          {
            name: "in",
            color: "var(--s2)",
            values: [1, 2],
            band: [10, 20],
          },
        ]}
        mirrored
        onClose={() => {}}
      />,
    );

    // Mirrored, so the top label is the ceiling's magnitude: it has to clear
    // the band's 20 rather than stopping at the line's 2.
    const top = Number(axisLabels()[0]);
    expect(top).toBeGreaterThan(2);
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

    expect(axisLabels().length).toBeGreaterThan(0);
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

    const labels = axisLabels();
    expect(labels[0]).toBe("100");
    expect(labels[labels.length - 1]).toBe("0");
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

    expect(axisLabels()).toEqual([]);
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

  /**
   * A panel given neither a floor nor a ceiling derives both from its data,
   * and the dialog it opens has to derive them the same way.
   *
   * Uptime is the case that shows it. A day's window runs from 39d 4h to
   * 40d 3h -- a rising diagonal across the panel. Drawn from an assumed zero
   * floor instead, the same series is a flat line pinned to the top of the
   * box under an axis reading 0 / 11d / 23d / 34d: the enlarged view saying
   * strictly LESS than the 260px chart it was opened from. It went unnoticed
   * because these panels used to open a page, which derived its own floor.
   */
  it("keeps a free-scaled panel's own floor when it is enlarged", async () => {
    const uptime = [
      { name: "uptime", color: "var(--s1)", values: [3_386_400, 3_466_800] },
    ];
    render(<ChartPanel title="Uptime" series={uptime} />);

    await userEvent.click(screen.getByRole("button", { name: /enlarge/i }));

    // The axis starts at the data, not at zero.
    const labels = Array.from(
      screen.getByRole("dialog").querySelectorAll('[data-axis-label="y"]'),
    ).map((el) => el.textContent);
    expect(labels).not.toContain("0");
    expect(labels.length).toBeGreaterThan(0);
  });

  // A pinned scale is a DECISION -- a named ceiling, a stack, a mirror, a
  // reference rule -- and deriving one from the window would throw it away.
  it("keeps a pinned ceiling when it is enlarged", async () => {
    render(<ChartPanel title="Processor" series={series} max={100} />);

    await userEvent.click(screen.getByRole("button", { name: /enlarge/i }));

    const labels = Array.from(
      screen.getByRole("dialog").querySelectorAll('[data-axis-label="y"]'),
    ).map((el) => el.textContent);
    expect(labels).toContain("100");
  });

  /**
   * The panel and the dialog are the same chart at two sizes, and for one
   * caller that means two different SERIES: the Graphs tab draws the peak
   * alone at 260px, where a mean line and a peak envelope together are a
   * smear, and hands the pair to the dialog that has room for it.
   */
  it("draws the enlarged view's own series when it is given one", async () => {
    render(
      <ChartPanel
        title="Traffic"
        series={[{ name: "in", color: "var(--s1)", values: [10, 20] }]}
        detailSeries={[
          { name: "in", color: "var(--s1)", values: [8, 16], band: [10, 20] },
        ]}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: /enlarge/i }));

    // The stats read detailSeries: latest 16, min 8. 20 is the panel's own
    // last value and appears nowhere in the dialog -- which is the whole
    // claim, since the two series are otherwise the same shape.
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getAllByText("16").length).toBeGreaterThan(0);
    expect(within(dialog).queryByText("20")).toBeNull();
  });
});

// Enlarging a chart must not cost the reader the sentence that made it
// readable, so the dialog carries the same glyph and the same text.
describe("ChartDetail about", () => {
  it("shows the panel's explanation in the dialog header", async () => {
    const user = userEvent.setup();
    render(
      <ChartDetail
        title="TCP listen queue"
        about="Connections the kernel dropped before the process accepted them."
        series={series}
        onClose={() => {}}
      />,
    );

    await user.hover(
      screen.getByRole("button", { name: "About TCP listen queue" }),
    );

    expect(screen.getByRole("tooltip")).toHaveTextContent(
      "Connections the kernel dropped",
    );
  });

  it("draws no glyph when the panel had nothing to say", () => {
    render(<ChartDetail title="Uptime" series={series} onClose={() => {}} />);

    expect(screen.queryByRole("button", { name: /^About/ })).toBeNull();
  });

  // One press, one layer. Both the tooltip and the dialog listen for Escape
  // on `document`, and a reader who opened the tooltip to read it should not
  // lose the chart underneath it in the same keystroke.
  it("closes the tooltip before the dialog, one Escape at a time", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(
      <ChartDetail
        title="TCP listen queue"
        about="Connections the kernel dropped before the process accepted them."
        series={series}
        onClose={onClose}
      />,
    );

    await user.hover(
      screen.getByRole("button", { name: "About TCP listen queue" }),
    );
    await user.keyboard("{Escape}");

    expect(screen.queryByRole("tooltip")).toBeNull();
    expect(onClose).not.toHaveBeenCalled();

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalled();
  });
});
