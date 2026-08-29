import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Enlargeable } from "./Enlargeable";

/**
 * The click-to-enlarge affordance without the card around it.
 *
 * Every sparkline in a table cell or a list row was inert, because opening
 * the enlarged view was a property of ChartPanel -- a whole card, with a
 * title, a unit and a headline value -- rather than of the chart. These tests
 * are about the affordance surviving on its own.
 */
/**
 * The dialog's y-axis labels, top to bottom.
 *
 * SVG text inside the plot rather than an HTML gutter beside it: both scale
 * with the viewBox, so a label stays on the gridline it names at any rendered
 * size. Sorted by y because document order follows the tick list, not the
 * screen.
 */
function yLabels(): string[] {
  return Array.from(
    screen.getByRole("dialog").querySelectorAll('[data-axis-label="y"]'),
  )
    .sort((a, b) => Number(a.getAttribute("y")) - Number(b.getAttribute("y")))
    .map((el) => el.textContent ?? "");
}

describe("Enlargeable", () => {
  const series = [{ name: "temp", color: "var(--s7)", values: [44, 46, 45] }];

  it("makes whatever it wraps the button that opens the chart", async () => {
    render(
      <Enlargeable title="Package" series={series}>
        <svg data-testid="spark" />
      </Enlargeable>,
    );

    // The chart IS the button -- not a separate expand icon beside it.
    const button = screen.getByRole("button", { name: "Enlarge Package" });
    expect(within(button).getByTestId("spark")).toBeInTheDocument();

    await userEvent.click(button);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  // Twenty rows of "Enlarge CPU" name twenty different charts identically,
  // which is unusable with a screen reader.
  it("takes an accessible name naming the row, not just the metric", () => {
    render(
      <Enlargeable title="CPU" label="Enlarge CPU for shop/api" series={series}>
        <svg />
      </Enlargeable>,
    );

    expect(
      screen.getByRole("button", { name: "Enlarge CPU for shop/api" }),
    ).toBeInTheDocument();
  });

  // No fetcher means no rail: tiles that cannot change anything, and have
  // nothing to draw, are worse than no tiles.
  it("carries no range rail without a fetcher", async () => {
    render(
      <Enlargeable title="Package" series={series} range="1h">
        <svg />
      </Enlargeable>,
    );

    await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));
    expect(
      within(screen.getByRole("dialog")).queryByRole("button", {
        name: /last 6h$/,
      }),
    ).not.toBeInTheDocument();
  });

  it("refetches for its own range and keeps the page's untouched", async () => {
    const fetchSeries = vi.fn().mockResolvedValue({
      series: [{ name: "temp", color: "var(--s7)", values: [50, 51] }],
      window: { from: "a", to: "b" },
    });

    render(
      <Enlargeable
        title="Package"
        series={series}
        range="1h"
        ranges={["1h", "6h", "24h"]}
        fetchSeries={fetchSeries}
      >
        <svg />
      </Enlargeable>,
    );
    await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: /last 6h$/,
      }),
    );

    await waitFor(() => expect(fetchSeries).toHaveBeenCalledWith("6h"));
    // Once per rail window that is not the page's own, and no more: clicking
    // a tile shows what the rail already fetched.
    expect(fetchSeries.mock.calls.map((c) => c[0]).sort()).toEqual([
      "24h",
      "6h",
    ]);
  });

  // The fleet list mounts one of these per chart cell -- twenty hosts is
  // sixty. A gate that only held while closed would turn one page render
  // into sixty requests.
  it("fetches nothing for a dialog nobody opened", () => {
    const fetchSeries = vi.fn();

    render(
      <Enlargeable
        title="Package"
        series={series}
        range="1h"
        fetchSeries={fetchSeries}
      >
        <svg />
      </Enlargeable>,
    );

    expect(fetchSeries).not.toHaveBeenCalled();
  });

  /**
   * A fixed ceiling is a decision about the SMALL chart -- one shared scale
   * down a list's column, or a pinned 100 -- and opening the dialog keeps it.
   * Widening is the other case: that window is asked for precisely to find
   * what the page's window did not show.
   */
  describe("a fixed ceiling", () => {
    function axisTop() {
      return (yLabels()[0] ?? "").trim();
    }

    it("is raised to fit a widened window rather than clipping it", async () => {
      const fetchSeries = vi.fn().mockResolvedValue({
        // A spike four times the ceiling the list shared. linePath does not
        // clamp, so held to `max` this is drawn outside the box -- the one
        // thing the reader widened the window to see goes off the top.
        series: [{ name: "cpu", color: "var(--s1)", values: [10, 400] }],
        window: { from: "a", to: "b" },
      });

      render(
        <Enlargeable
          title="CPU"
          series={[{ name: "cpu", color: "var(--s1)", values: [10, 20] }]}
          max={100}
          range="1h"
          ranges={["1h", "6h"]}
          fetchSeries={fetchSeries}
        >
          <svg />
        </Enlargeable>,
      );
      await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));
      expect(axisTop()).toBe("100");

      await userEvent.click(
        within(screen.getByRole("dialog")).getByRole("button", {
          name: /last 6h$/,
        }),
      );
      await waitFor(() => expect(axisTop()).toBe("400"));
    });

    // Raised only. A quieter wide window keeps the ceiling it was opened
    // with, so the shape stays comparable with the cell behind it.
    it("is not lowered for a quieter window", async () => {
      const fetchSeries = vi.fn().mockResolvedValue({
        series: [{ name: "cpu", color: "var(--s1)", values: [1, 2] }],
        window: { from: "a", to: "b" },
      });

      render(
        <Enlargeable
          title="CPU"
          series={[{ name: "cpu", color: "var(--s1)", values: [10, 20] }]}
          max={100}
          range="1h"
          ranges={["1h", "6h"]}
          fetchSeries={fetchSeries}
        >
          <svg />
        </Enlargeable>,
      );
      await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));
      await userEvent.click(
        within(screen.getByRole("dialog")).getByRole("button", {
          name: /last 6h$/,
        }),
      );

      await waitFor(() => expect(fetchSeries).toHaveBeenCalled());
      expect(axisTop()).toBe("100");
    });
  });

  // A reader who opened the chart on row 14 of a twenty-host table must not
  // be returned to the masthead. The fleet page carries sixty of these.
  it("returns focus to the button that opened it", async () => {
    render(
      <Enlargeable title="Package" series={series}>
        <svg />
      </Enlargeable>,
    );
    const button = screen.getByRole("button", { name: /Enlarge/ });

    await userEvent.click(button);
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }),
    );

    expect(button).toHaveFocus();
  });

  /**
   * The sensor list free-scales every row to its own extent on purpose: a
   * package between 44 and 47 degrees drawn against a 0-47 box is a flat line
   * at the bottom, so the enlarged view would say LESS than the 110px chart
   * it was opened from.
   */
  describe("autoScale", () => {
    /** The y axis' labels alone. The stats table underneath prints the same
     * numbers, so a bare getByText would match either. */
    function axis(): string[] {
      return yLabels();
    }

    it("labels the axis from the series' own floor, not zero", async () => {
      render(
        <Enlargeable title="Package" series={series} autoScale>
          <svg />
        </Enlargeable>,
      );
      await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));

      // The floor is the coolest reading, not 0.
      expect(axis()).toContain("44");
      expect(axis()).not.toContain("0");
    });

    it("still scales from zero when it is not asked for", async () => {
      render(
        <Enlargeable title="Package" series={series}>
          <svg />
        </Enlargeable>,
      );
      await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));

      expect(axis()).toContain("0");
    });

    // A fan pinned at one speed is a real reading, not an absent one. The
    // sparkline above draws it centred (scaleY centres a min === max series),
    // and dropping back to a zero floor here would put the same fan at the
    // top of a 0-1200 box -- the disagreement autoScale exists to prevent.
    it("keeps a series that never moved at its own value", async () => {
      render(
        <Enlargeable
          title="fan1"
          series={[{ name: "fan1", color: "var(--s3)", values: [1200, 1200] }]}
          autoScale
        >
          <svg />
        </Enlargeable>,
      );
      await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));

      // One label, at the one value the series has -- not three steps across
      // a range it does not span.
      expect(axis()).toEqual(["1200"]);
    });

    // extent() answers {min: 0, max: 0} for a series with nothing in it,
    // which is reachable rather than theoretical: the 1h tier materialises
    // about ninety minutes behind now, so a widened window can come back with
    // no rows at all. Passed through, that 0 would ALSO defeat ChartDetail's
    // own `max || 1` guard, because `0 ?? x` is 0.
    it("does not hand the plot a zero-height box for an empty window", async () => {
      render(
        <Enlargeable
          title="Package"
          series={[{ name: "temp", color: "var(--s7)", values: [] }]}
          autoScale
        >
          <svg />
        </Enlargeable>,
      );
      await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));

      expect(axis().join(" ")).not.toMatch(/NaN/);
      expect(axis()).toContain("1");
    });

    // A fixed floor would be the extent frozen when the dialog opened, so a
    // widened window would draw its new data against the old window's floor.
    it("rescales to the window it was just given", async () => {
      const fetchSeries = vi.fn().mockResolvedValue({
        series: [{ name: "temp", color: "var(--s7)", values: [70, 80] }],
        window: { from: "a", to: "b" },
      });

      render(
        <Enlargeable
          title="Package"
          series={series}
          autoScale
          range="1h"
          ranges={["1h", "6h"]}
          fetchSeries={fetchSeries}
        >
          <svg />
        </Enlargeable>,
      );
      await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));
      await userEvent.click(
        within(screen.getByRole("dialog")).getByRole("button", {
          name: /last 6h$/,
        }),
      );

      await waitFor(() => expect(axis()).toContain("70"));
      expect(axis()).not.toContain("44");
    });
  });
});
