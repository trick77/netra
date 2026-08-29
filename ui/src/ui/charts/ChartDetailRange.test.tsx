import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChartPanel } from "./ChartPanel";
import type { DetailData } from "./ChartPanel";
import type { Range } from "../../lib/range";

/**
 * The enlarged view's range belongs to the enlarged view.
 *
 * It used to be wired to the PAGE's setter, so widening one chart refetched
 * every panel beside it and moved the toolbar behind the dialog -- the
 * opposite of what enlarging one chart asks for. A reader who opens one
 * chart to look at it closely is asking a question about that chart.
 */
describe("the enlarged view owns its range", () => {
  const series = [{ name: "busy", color: "var(--s1)", values: [1, 2, 3] }];
  const wider = [{ name: "busy", color: "var(--s1)", values: [9, 9, 9] }];

  function open() {
    return userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));
  }

  function dialog() {
    return screen.getByRole("dialog");
  }

  it("changes only this panel's data, never the page's range", async () => {
    const fetchSeries = vi.fn().mockResolvedValue({
      series: wider,
      window: { from: "a", to: "b" },
    });

    render(
      <ChartPanel
        title="CPU"
        series={series}
        range="1h"
        ranges={["1h", "6h", "24h"]}
        fetchSeries={fetchSeries}
      />,
    );
    await open();
    await userEvent.click(
      within(dialog()).getByRole("button", { name: /last 6h$/ }),
    );

    // There is no page setter to call: the only thing reachable from the
    // dialog is this panel's own fetcher, for this panel's own family. Once
    // per rail window that is not the page's own, and NOT again when a tile
    // is clicked -- the rail already holds that window.
    await waitFor(() => expect(fetchSeries).toHaveBeenCalledWith("6h"));
    expect(fetchSeries.mock.calls.map((c) => c[0]).sort()).toEqual([
      "24h",
      "6h",
    ]);
    expect(
      within(dialog()).getByRole("button", { name: /last 6h$/ }),
    ).toHaveAttribute("aria-pressed", "true");
  });

  // Opening fetches the rail's OTHER windows -- each tile is a drawn preview
  // and cannot be drawn unfetched -- but never the page's own. Those series
  // are already on screen behind the dialog, which is what seeds that tile.
  it("never asks for the page's own range", async () => {
    const fetchSeries = vi
      .fn()
      .mockResolvedValue({ series: wider, window: null });

    render(
      <ChartPanel
        title="CPU"
        series={series}
        range="1h"
        ranges={["1h", "6h"]}
        fetchSeries={fetchSeries}
      />,
    );
    await open();
    await waitFor(() => expect(fetchSeries).toHaveBeenCalledWith("6h"));
    await userEvent.click(screen.getByRole("button", { name: "Close" }));

    expect(fetchSeries).not.toHaveBeenCalledWith("1h");
    expect(fetchSeries).toHaveBeenCalledTimes(1);
  });

  // A dialog is a question asked and answered, not a second piece of page
  // state to keep track of. Reopening starts from the page again.
  it("returns to the page's range when closed and reopened", async () => {
    const fetchSeries = vi
      .fn()
      .mockResolvedValue({ series: wider, window: null });

    render(
      <ChartPanel
        title="CPU"
        series={series}
        range="1h"
        ranges={["1h", "6h"]}
        fetchSeries={fetchSeries}
      />,
    );
    await open();
    await userEvent.click(
      within(dialog()).getByRole("button", { name: /last 6h$/ }),
    );
    await waitFor(() => expect(fetchSeries).toHaveBeenCalled());
    await userEvent.click(screen.getByRole("button", { name: "Close" }));

    await open();
    expect(
      within(dialog()).getByRole("button", { name: /last 1h$/ }),
    ).toHaveAttribute("aria-pressed", "true");
  });

  // An empty chart in netra asserts "the host reported nothing" (spec 7.6),
  // so a failed widen must not produce one.
  it("keeps the chart it had when a range fails to load", async () => {
    const fetchSeries = vi.fn().mockRejectedValue(new Error("boom"));

    const { container } = render(
      <ChartPanel
        title="CPU"
        series={series}
        range="1h"
        ranges={["1h", "6h"]}
        fetchSeries={fetchSeries}
      />,
    );
    await open();
    await userEvent.click(
      within(dialog()).getByRole("button", { name: /last 6h$/ }),
    );

    expect(await screen.findByText(/could not load that range/i)).toBeVisible();
    expect(
      container.querySelectorAll(".cd path[data-line]").length,
    ).toBeGreaterThan(0);
  });

  // Going back to the page's range shows the page's own chart again, which
  // loaded fine -- so neither the failure nor the in-flight state of the
  // range just left may still be on screen beside it.
  it("clears the failure when the reader returns to the page's range", async () => {
    const fetchSeries = vi.fn().mockRejectedValue(new Error("boom"));

    render(
      <ChartPanel
        title="CPU"
        series={series}
        range="1h"
        ranges={["1h", "6h"]}
        fetchSeries={fetchSeries}
      />,
    );
    await open();
    await userEvent.click(
      within(dialog()).getByRole("button", { name: /last 6h$/ }),
    );
    expect(await screen.findByText(/could not load that range/i)).toBeVisible();

    await userEvent.click(
      within(dialog()).getByRole("button", { name: /last 1h$/ }),
    );

    expect(screen.queryByText(/could not load that range/i)).toBeNull();
  });

  // A fetch left in flight never settles into the dialog: its resolver is
  // cancelled. If nothing else ended it, "Loading…" would be permanent.
  it("stops loading when the range is changed back mid-flight", async () => {
    const fetchSeries = vi.fn().mockReturnValue(new Promise(() => {}));

    render(
      <ChartPanel
        title="CPU"
        series={series}
        range="1h"
        ranges={["1h", "6h"]}
        fetchSeries={fetchSeries}
      />,
    );
    await open();
    await userEvent.click(
      within(dialog()).getByRole("button", { name: /last 6h$/ }),
    );
    expect(within(dialog()).getByText(/loading/i)).toBeVisible();

    await userEvent.click(
      within(dialog()).getByRole("button", { name: /last 1h$/ }),
    );

    expect(within(dialog()).queryByText(/loading/i)).toBeNull();
  });

  // The same fetch, rebuilt inline by the page on every poll, is the same
  // question -- asking it again every sixty seconds is not.
  it("does not refetch when the page re-renders with a new fetcher", async () => {
    // Counted outside the closures, because each render builds a NEW one --
    // which is the whole point: the page rebuilds it, the dialog must not
    // treat that as a new question.
    const asked: Range[] = [];
    const fetcher = () => async (next: Range) => {
      asked.push(next);
      return { series: wider, window: null };
    };
    const panel = (fetchSeries: (next: Range) => Promise<DetailData>) => (
      <ChartPanel
        title="CPU"
        series={series}
        range="1h"
        ranges={["1h", "6h"]}
        fetchSeries={fetchSeries}
      />
    );

    const { rerender } = render(panel(fetcher()));
    await open();
    await userEvent.click(
      within(dialog()).getByRole("button", { name: /last 6h$/ }),
    );
    await waitFor(() => expect(asked).toEqual(["6h"]));

    // A poll landed behind the dialog: same panel, a new closure.
    rerender(panel(fetcher()));

    await waitFor(() =>
      expect(within(dialog()).queryByText(/loading/i)).toBeNull(),
    );
    expect(asked).toEqual(["6h"]);
  });

  // Tiles that cannot change anything, and have nothing to draw, are worse
  // than no tiles.
  it("carries no rail at all without a fetcher", async () => {
    render(<ChartPanel title="CPU" series={series} range="1h" />);
    await open();

    expect(
      within(dialog()).queryByRole("button", { name: /last 6h$/ }),
    ).toBeNull();
  });
});
