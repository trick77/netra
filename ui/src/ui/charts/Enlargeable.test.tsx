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

  // No fetcher means no picker: buttons that cannot change anything are
  // worse than no buttons.
  it("carries no range picker without a fetcher", async () => {
    render(
      <Enlargeable title="Package" series={series} range="1h">
        <svg />
      </Enlargeable>,
    );

    await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));
    expect(
      within(screen.getByRole("dialog")).queryByRole("button", { name: "6h" }),
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
      within(screen.getByRole("dialog")).getByRole("button", { name: "6h" }),
    );

    await waitFor(() => expect(fetchSeries).toHaveBeenCalledWith("6h"));
    expect(fetchSeries).toHaveBeenCalledTimes(1);
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
   * The sensor list free-scales every row to its own extent on purpose: a
   * package between 44 and 47 degrees drawn against a 0-47 box is a flat line
   * at the bottom, so the enlarged view would say LESS than the 110px chart
   * it was opened from.
   */
  describe("autoScale", () => {
    /** The y axis' labels alone. The stats table underneath prints the same
     * numbers, so a bare getByText would match either. */
    function axis(): string[] {
      const labels = screen
        .getByRole("dialog")
        .querySelector(".cd-y") as HTMLElement | null;
      return Array.from(labels?.children ?? []).map(
        (el) => el.textContent ?? "",
      );
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
        within(screen.getByRole("dialog")).getByRole("button", { name: "6h" }),
      );

      await waitFor(() => expect(axis()).toContain("70"));
      expect(axis()).not.toContain("44");
    });
  });
});
