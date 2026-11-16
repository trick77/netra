import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { HostCards, orderCards } from "./HostCards";
import type { HostRow } from "./hostColumns";

function makeRow(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    window: null,
    last_seen: new Date().toISOString(),
    cpu_total: 42,
    mem_used: 4_000_000_000,
    mem_total: 16_000_000_000,
    uptime_s: 864_000,
    threads: 4,
    cpu: [{ name: "user", color: "var(--s1)", values: [10, 12, 11] }],
    reporting: [10, 12, 11],
    mem: [{ name: "used", color: "var(--s1)", values: [3e9, 3.5e9, 4e9] }],
    memUsed: [3e9, 3.5e9, 4e9],
    rx: [1e6, 2e6],
    tx: [5e5, 6e5],
    net_rx_bytes: 1.5e6,
    net_tx_bytes: 5.5e5,
    fullest: { mount: "/data", pct: 40 },
    disk: [],
    ...overrides,
  };
}

const names = (rows: readonly HostRow[]) => rows.map((r) => r.hostname);

describe("orderCards", () => {
  const rows = [
    makeRow({ id: 1, hostname: "c", reporting: [5] }),
    makeRow({ id: 2, hostname: "a", reporting: [90] }),
    makeRow({ id: 3, hostname: "b", reporting: [40] }),
  ];

  it("leads with critical, then warning, and keeps the rest in order", () => {
    const mark = (r: HostRow) =>
      r.hostname === "b" ? "critical" : r.hostname === "a" ? "warning" : null;
    expect(names(orderCards(rows, "status", "24h", mark))).toEqual([
      "b",
      "a",
      "c",
    ]);
  });

  it("orders names ascending and readings descending", () => {
    const none = () => null;
    expect(names(orderCards(rows, "name", "24h", none))).toEqual([
      "a",
      "b",
      "c",
    ]);
    expect(names(orderCards(rows, "cpu", "24h", none))).toEqual([
      "a",
      "b",
      "c",
    ]);
  });

  it("puts a host with no reading last, whichever way", () => {
    const quiet = makeRow({
      id: 4,
      hostname: "z",
      last_seen: "2020-01-01T00:00:00Z",
    });
    expect(
      names(orderCards([quiet, ...rows], "cpu", "24h", () => null)).at(-1),
    ).toBe("z");
  });
});

describe("HostCards", () => {
  it("draws one card per host, with all four readings named", () => {
    render(<HostCards rows={[makeRow()]} range="24h" />);

    const card = screen.getByRole("article", { name: "web-01" });
    const labels = [...card.querySelectorAll(".treading-head .k")].map(
      (k) => k.textContent,
    );
    expect(labels).toEqual(["CPU", "Memory", "Filesystem", "Traffic"]);
  });

  it("shows the empty state, not an empty grid, with no hosts", () => {
    render(<HostCards rows={[]} range="24h" />);
    expect(screen.queryByRole("article")).toBeNull();
    expect(screen.getByText(/no hosts/i)).toBeInTheDocument();
  });
});
