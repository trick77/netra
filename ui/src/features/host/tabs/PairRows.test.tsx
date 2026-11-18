import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { MetricsResponse } from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import { NETWORK_GROUPS } from "../chartSpecs";
import { PairRows } from "./PairRows";

const at = (i: number) => Date.parse(`2026-08-10T00:0${i}:00Z`);
const net = {
  family: "net",
  tier: "raw",
  step_s: 60,
  window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:03:00Z" },
  requested_window: {
    from: "2026-08-10T00:00:00Z",
    to: "2026-08-10T00:03:00Z",
  },
  warnings: [],
  key_columns: ["iface"],
  columns: ["rx_bytes", "tx_bytes"],
  series: [
    {
      key: { iface: "eth0" },
      points: [
        [at(0), 1000, 200],
        [at(1), 2000, 300],
        [at(2), 3000, 400],
      ],
    },
    {
      key: { iface: "eth1" },
      points: [
        [at(0), 10, 20],
        [at(1), 10, 20],
        [at(2), 10, 20],
      ],
    },
    // Idle all window: no row
    {
      key: { iface: "eth2" },
      points: [
        [at(0), 0, 0],
        [at(1), 0, 0],
        [at(2), 0, 0],
      ],
    },
  ],
  truncated: false,
} as unknown as MetricsResponse;

const group = NETWORK_GROUPS.find((g) => g.form === "pairs")!;

describe("PairRows", () => {
  it("draws a row per carrying interface with its latest in and out", () => {
    render(<PairRows group={group} sources={{ net }} />);
    const rows = screen.getAllByRole("row").slice(1);
    expect(
      rows.map((r) => within(r).getByRole("rowheader").textContent),
    ).toEqual(["eth0", "eth1"]);
    expect(rows[0]).toHaveTextContent("3 kB/s");
    expect(rows[0]).toHaveTextContent("400 B/s");
    // Each row opens into its own pair's chart
    expect(
      within(rows[1]!).getByRole("button", { name: "eth1: open the chart" }),
    ).toBeInTheDocument();
  });

  it("says why when there is nothing to draw", () => {
    render(<PairRows group={group} sources={{ net: null }} />);
    expect(
      screen.getByText("No data has been read for this family yet."),
    ).toBeInTheDocument();
  });

  // A link on a host that stopped reporting has no current rate: the newest
  // bucket is a gap, and the row says so rather than printing an old one.
  it("prints no rate for an interface whose newest bucket is empty", () => {
    const stopped = {
      ...net,
      series: [
        {
          key: { iface: "eth0" },
          points: [
            [at(0), 1000, 200],
            [at(1), 2000, 300],
          ],
        },
      ],
    } as unknown as MetricsResponse;
    render(<PairRows group={group} sources={{ net: stopped }} />);
    const row = screen.getAllByRole("row")[1]!;
    expect(row).not.toHaveTextContent("kB/s");
    expect(row).toHaveTextContent(ABSENT);
  });

  // The enlarged row keeps its range picker: the family at another range,
  // narrowed to this interface.
  it("widens a row to that interface alone", async () => {
    const fetchFamily = vi.fn().mockResolvedValue(net);
    render(
      <PairRows
        group={group}
        sources={{ net }}
        fetchFamily={fetchFamily}
        range="1h"
      />,
    );
    await userEvent.click(
      screen.getByRole("button", { name: "eth1: open the chart" }),
    );
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: /last 6h$/ }),
    );
    await waitFor(() =>
      expect(
        within(dialog)
          .getAllByRole("row")
          .slice(1)
          .map((r) => r.textContent),
      ).toEqual([
        expect.stringMatching(/^eth1 in/),
        expect.stringMatching(/^eth1 out/),
      ]),
    );
    expect(fetchFamily).toHaveBeenCalledWith("net", "6h");
  });
});
