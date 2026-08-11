// HostTable and HostCards are two renderings of ONE column definition
// (hostColumns, Task 11). Neither may invent a cell, drop one, or reorder
// them: the whole point of the shared definition is that adding a column
// makes it appear in both. HostCards.test.tsx holds the equivalence test
// that pins the two against each other.
import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { HostTable } from "./HostTable";
import { hostColumns, type HostRow } from "./hostColumns";

function makeRow(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    site_id: 3,
    site_name: "zurich-dc1",
    last_seen: "2026-08-10T13:59:30Z",
    cpu_total: 42,
    mem_used: 4_000_000_000,
    mem_total: 16_000_000_000,
    uptime_s: 864_000,
    cpu: [{ name: "user", color: "var(--s1)", values: [10, 12, 11] }],
    mem: [{ name: "used", color: "var(--s1)", values: [3e9, 3.5e9, 4e9] }],
    rx: [1e6, 2e6],
    tx: [5e5, 6e5],
    fullest: { mount: "/data", pct: 88, others: 2 },
    ...overrides,
  };
}

describe("HostTable", () => {
  it("renders one cell per column, in the column definition's order", () => {
    render(<HostTable rows={[makeRow()]} range="24h" />);

    const headers = screen
      .getAllByRole("columnheader")
      .map((h) => h.textContent);
    expect(headers).toEqual(hostColumns("24h").map((c) => c.header));

    const row = screen.getAllByRole("row")[1]!;
    expect(within(row).getAllByRole("cell")).toHaveLength(
      hostColumns("24h").length,
    );
  });

  // Two hosts can share a hostname across sites, and rows are sorted and
  // filtered by the page above -- index keys would make React reuse a row's
  // DOM for a different host, carrying a chart's state onto the wrong one.
  it("keys rows by host id, not by position", () => {
    const { rerender } = render(
      <HostTable
        rows={[makeRow({ id: 1 }), makeRow({ id: 2, hostname: "web-02" })]}
        range="24h"
      />,
    );
    const first = screen.getAllByRole("row")[1]!;

    rerender(
      <HostTable
        rows={[makeRow({ id: 2, hostname: "web-02" }), makeRow({ id: 1 })]}
        range="24h"
      />,
    );

    // Reordering must move the DOM node with it: the node that held web-01
    // is now the second data row, not the first.
    expect(screen.getAllByRole("row")[2]).toBe(first);
  });

  it("renders the empty state instead of a headerless table when there are no hosts", () => {
    render(<HostTable rows={[]} range="24h" />);

    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByText(/no hosts/i)).toBeInTheDocument();
  });
});
