import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { MetricsResponse } from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import { specForSlug, type PanelGroup } from "../chartSpecs";
import { Ledger } from "./Ledger";

const t = (i: number) => Date.parse(`2026-08-10T00:0${i}:00Z`);
const host = (columns: string[], rows: (number | null)[][]): MetricsResponse =>
  ({
    family: "host",
    tier: "raw",
    step_s: 60,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:03:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T00:03:00Z",
    },
    warnings: [],
    key_columns: [],
    columns,
    series: [{ key: {}, points: rows.map((r, i) => [t(i), ...r]) }],
    truncated: false,
  }) as unknown as MetricsResponse;

const group = (title: string, ...slugs: string[]): PanelGroup => ({
  title,
  form: "ledger",
  specs: slugs.map((s) => specForSlug(s)!),
});

function rowOf(name: string) {
  return screen.getByText(name).closest("tr")!;
}

describe("Ledger", () => {
  it("reads a series as now, mean and peak, with its unit", () => {
    render(
      <Ledger
        groups={[group("Kernel", "context-switches")]}
        sources={{
          host: host(["ctxt_per_s"], [[100], [300], [200]]),
        }}
      />,
    );
    const row = rowOf("Context switches");
    const cells = within(row).getAllByRole("cell");
    expect(cells.map((c) => c.textContent)).toEqual([
      "Context switches",
      "200 /s",
      "200 /s",
      "300 /s",
      expect.any(String),
    ]);
    // The trend opens into the full chart
    expect(
      within(row).getByRole("button", {
        name: /Context switches/,
      }),
    ).toBeInTheDocument();
  });

  it("names a spec's series by their own names under a group of its title", () => {
    render(
      <Ledger
        groups={[group("Memory pressure", "memory-pressure")]}
        sources={{
          host: host(
            ["pgmajfault_per_s", "pswpin_per_s", "pswpout_per_s"],
            [
              [1, 0, 0],
              [2, 0, 0],
              [null, 0, 0],
            ],
          ),
        }}
      />,
    );
    expect(screen.getByText("major faults")).toBeInTheDocument();
    // "Now" is the newest bucket, a gap included: a host that stopped
    // reporting has no current rate, and its last one is not "now"
    const cells = within(rowOf("major faults")).getAllByRole("cell");
    expect(cells[1]).toHaveTextContent(ABSENT);
    // The peak still counts what it did say
    expect(cells[3]).toHaveTextContent("2 /s");
  });

  it("folds the rows that stayed at zero, and opens them on request", async () => {
    render(
      <Ledger
        fold
        groups={[group("Memory pressure", "memory-pressure", "swap-used")]}
        sources={{
          host: host(
            ["pgmajfault_per_s", "pswpin_per_s", "pswpout_per_s", "swap_used"],
            [
              [1, 0, 0, null],
              [2, 0, 0, null],
            ],
          ),
        }}
      />,
    );
    expect(screen.getByText("major faults")).toBeInTheDocument();
    expect(screen.queryByText("swap in")).toBeNull();
    // swap in, swap out and the unreported swap used
    expect(screen.getByText(/3 rows stayed at zero/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "show" }));
    expect(screen.getByText("swap in")).toBeInTheDocument();
    expect(screen.getByText("Swap used")).toBeInTheDocument();

    await userEvent.click(
      screen.getByRole("button", { name: "hide the quiet rows" }),
    );
    expect(screen.queryByText("swap in")).toBeNull();
  });

  it("says why a spec draws nothing rather than leaving it out", () => {
    render(
      <Ledger
        groups={[group("Kernel", "interrupts")]}
        sources={{ host: null }}
      />,
    );
    expect(
      within(rowOf("Interrupts")).getByText(/No data has been read/),
    ).toBeInTheDocument();
  });

  // A family still being asked for is not quiet: folding it would say it
  // stayed at zero before the hub was asked.
  it("never folds a row that is still loading", () => {
    render(
      <Ledger
        fold
        groups={[group("Kernel", "interrupts")]}
        sources={{ host: null, pending: true }}
      />,
    );
    expect(
      within(rowOf("Interrupts")).getByText("Asking the hub for this window."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/stayed at zero/)).toBeNull();
  });
});
