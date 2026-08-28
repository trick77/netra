// The equivalence test lives here: HostCards and HostTable must render the
// same columns for the same data, or the "one column definition" contract
// (Task 11) is a claim rather than a fact.
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { HostCards } from "./HostCards";
import { HostTable } from "./HostTable";
import { hostColumns, type HostRow } from "./hostColumns";

function makeRow(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    site_id: 3,
    site_name: "zurich-dc1",
    provider_name: null,
    facility: null,
    country_code: null,
    window: null,
    last_seen: "2026-08-10T13:59:30Z",
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
    fullest: { mount: "/data", pct: 88, others: 2 },
    disk: [],
    oomKills: null,
    dropped: null,
    postFailures: null,
    ...overrides,
  };
}

describe("HostCards", () => {
  // The columns after Host are the metric tiles; Host itself is the card's
  // header, since a card has a name at the top rather than a first cell.
  it("renders every metric column as a tile, labelled by its column header", () => {
    render(<HostCards rows={[makeRow()]} range="24h" />);

    const metricColumns = hostColumns("24h").slice(1);
    for (const col of metricColumns) {
      expect(screen.getByText(col.header)).toBeInTheDocument();
    }
    expect(document.querySelectorAll(".hcard .m")).toHaveLength(
      metricColumns.length,
    );
  });

  // The two renderings share one definition, so the same data must produce
  // the same set of metric cells in the same order. A column added to
  // hostColumns appears in both or the contract is broken.
  it("renders the same columns, in the same order, as the table does", () => {
    const rows = [makeRow()];

    const cards = render(<HostCards rows={rows} range="24h" />);
    const cardLabels = [...document.querySelectorAll(".hcard .mk")].map(
      (el) => el.firstChild?.textContent,
    );
    cards.unmount();

    render(<HostTable rows={rows} range="24h" />);
    const tableHeaders = screen
      .getAllByRole("columnheader")
      .map((h) => h.textContent);

    expect(cardLabels).toEqual(tableHeaders.slice(1));
  });

  // Four metric tiles in a two-column grid is the 2x2 the design asks for;
  // the grid is defined in CSS (.hcard .grid) so the component states only
  // that every tile is inside it.
  it("lays the tiles out in the card's grid, not loose in the card", () => {
    render(<HostCards rows={[makeRow()]} range="24h" />);

    const grid = document.querySelector(".hcard .grid")!;
    expect(grid).not.toBeNull();
    expect(grid.querySelectorAll(".m")).toHaveLength(
      hostColumns("24h").length - 1,
    );
  });

  it("renders one card per host, keyed by id", () => {
    render(
      <HostCards
        rows={[makeRow({ id: 1 }), makeRow({ id: 2, hostname: "db-01" })]}
        range="24h"
      />,
    );

    expect(document.querySelectorAll(".hcard")).toHaveLength(2);
    expect(screen.getByText("db-01")).toBeInTheDocument();
  });

  it("renders the empty state when there are no hosts", () => {
    render(<HostCards rows={[]} range="24h" />);

    expect(document.querySelectorAll(".hcard")).toHaveLength(0);
    expect(screen.getByText(/no hosts/i)).toBeInTheDocument();
  });

  // The table gets its structure free from <th scope="col">; an unnamed
  // <article> gives a screen-reader user a nameless region holding a
  // nameless div. Cards are automatic below the mobile breakpoint, so this
  // is not a path anyone opted into.
  it("names each card after its host", () => {
    render(<HostCards rows={[makeRow({ hostname: "web-01" })]} range="24h" />);

    expect(screen.getByRole("article", { name: "web-01" })).toBeInTheDocument();
  });

  // The header column is found by key. Selected by position, a column added
  // at the front of hostColumns would silently become the card title
  // instead of a tile -- a column disappearing from the grid without anyone
  // touching this file.
  it("puts the Host column in the header and every other column in the grid", () => {
    render(<HostCards rows={[makeRow()]} range="24h" />);

    const header = document.querySelector(".hcard > header")!;
    expect(header.querySelector(".host-cell-name")).toBeInTheDocument();
    expect(header.querySelectorAll(".m")).toHaveLength(0);
  });

  // The Cards view draws hostColumns()' own cells, so the fixes to the
  // memory legend and to what the status badge is judged from reach it
  // without this file knowing about either. That delegation is the thing
  // worth pinning: a card and a table row show the same host, and a reader
  // toggling between them must not see two different answers.
  it("keeps the memory tile legend-free, exactly as the table row is", () => {
    const row = makeRow({
      mem_total: 16_000_000_000,
      mem: [
        { name: "used", color: "var(--s1)", values: [3e9, 3e9] },
        { name: "ARC", color: "var(--s7)", values: [2e8, 2e8] },
        { name: "buffers", color: "var(--s2)", values: [1e8, 1e8] },
        { name: "cached", color: "var(--s8)", values: [5e8, 5e8] },
        { name: "shared", color: "var(--s4)", values: [5e7, 5e7] },
      ],
    });

    const { container } = render(<HostCards rows={[row]} range="1h" />);

    // The card renders hostColumns()' own cells, so this follows the table
    // without HostCards knowing anything about legends -- which is the
    // property worth pinning: a card and a table row show the same host and
    // must not diverge on how much chrome they put under one sparkline.
    expect(container.querySelector(".legend")).toBeNull();
  });

  it("judges a card's status from the reporting series, not its CPU bands", () => {
    const row = makeRow({
      last_seen: new Date(Date.now() - 10_000).toISOString(),
      // Gappy per-core bands beside a clean cpu_total series: the host is
      // reporting fine and only the cpu_core tier lags.
      cpu: [
        {
          name: "core 0",
          color: "var(--s1)",
          values: [10, null, 12, null, 11, null, 9, null, 11, null],
        },
      ],
      reporting: [10, 11, 12, 11, 10, 11, 12, 11, 10, 11],
    });

    render(<HostCards rows={[row]} range="1h" />);

    expect(screen.queryByText("sporadic")).toBeNull();
  });
});
