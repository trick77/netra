// HostTable renders ONE column definition (hostColumns, Task 11) and may not
// invent a cell, drop one, or reorder them: the whole point of the shared
// definition is that adding a column there is what puts it on the page.
import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { HostTable } from "./HostTable";
import { hostColumns, type HostRow } from "./hostColumns";

function makeRow(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    window: null,
    // Reporting now, so a fixture host is healthy unless a test says not.
    last_seen: new Date().toISOString(),
    cpu_total: 42,
    mem_used: 4_000_000_000,
    mem_total: 16_000_000_000,
    uptime_s: 864_000,
    threads: null,
    cpu: [{ name: "user", color: "var(--s1)", values: [10, 12, 11] }],
    reporting: [10, 12, 11],
    mem: [{ name: "used", color: "var(--s1)", values: [3e9, 3.5e9, 4e9] }],
    rx: [1e6, 2e6],
    tx: [5e5, 6e5],
    net_rx_bytes: 1.5e6,
    net_tx_bytes: 5.5e5,
    fullest: { mount: "/data", pct: 88 },
    disk: [],
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

  // A host with a mark goes first, under its own heading, and a fleet with
  // nothing wrong gets no headings at all.
  it("groups hosts that need attention ahead of healthy ones", () => {
    const { container, rerender } = render(
      <HostTable
        rows={[
          makeRow({ id: 1, hostname: "web-01" }),
          makeRow({ id: 2, hostname: "db-01" }),
        ]}
        range="24h"
        severity={(row) => (row.id === 2 ? "critical" : null)}
      />,
    );

    const heads = [...container.querySelectorAll(".grouprow")].map(
      (r) => r.textContent,
    );
    expect(heads).toEqual(["Needs attention1", "Healthy1"]);
    const names = [...container.querySelectorAll(".host-cell-name")].map(
      (a) => a.textContent,
    );
    expect(names).toEqual(["db-01", "web-01"]);

    rerender(
      <HostTable
        rows={[makeRow({ id: 1 }), makeRow({ id: 2, hostname: "db-01" })]}
        range="24h"
        severity={() => null}
      />,
    );
    expect(container.querySelector(".grouprow")).toBeNull();
  });

  // The group follows the mark, not only the conditions: a host the row
  // judges offline is in "Needs attention" before the hub has said so.
  it("puts a host that stopped reporting in the attention group", () => {
    const { container } = render(
      <HostTable
        rows={[
          makeRow({ id: 1, hostname: "web-01" }),
          makeRow({
            id: 2,
            hostname: "nas",
            last_seen: "2020-01-01T00:00:00Z",
          }),
        ]}
        range="24h"
      />,
    );

    const first = container.querySelector("tbody .host-cell-name");
    expect(first?.textContent).toBe("nas");
  });

  // Critical before warning inside the group, the order the card view uses.
  it("ranks critical above warning inside the attention group", () => {
    const { container } = render(
      <HostTable
        rows={[
          makeRow({ id: 1, hostname: "warn" }),
          makeRow({ id: 2, hostname: "crit" }),
          makeRow({ id: 3, hostname: "ok" }),
        ]}
        range="24h"
        severity={(row) =>
          row.id === 1 ? "warning" : row.id === 2 ? "critical" : null
        }
      />,
    );

    expect(
      [...container.querySelectorAll(".host-cell-name")].map(
        (a) => a.textContent,
      ),
    ).toEqual(["crit", "warn", "ok"]);
  });

  it("renders the empty state instead of a headerless table when there are no hosts", () => {
    render(<HostTable rows={[]} range="24h" />);

    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByText(/no hosts/i)).toBeInTheDocument();
  });
});
