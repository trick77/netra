import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Table, type Column } from "./Table";

interface Row {
  id: string;
  name: string;
  cpu: number;
}

const rows: Row[] = [
  { id: "h1", name: "host-a", cpu: 12 },
  { id: "h2", name: "host-b", cpu: 87 },
  { id: "h3", name: "host-c", cpu: 45 },
];

const columns: Column<Row>[] = [
  { key: "name", header: "Host", cell: (r) => r.name },
  { key: "cpu", header: "CPU", align: "right", cell: (r) => `${r.cpu}%` },
];

describe("Table", () => {
  it("renders one <tr> per row in tbody", () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    const body = screen.getByRole("table").querySelector("tbody")!;
    expect(body.querySelectorAll("tr")).toHaveLength(3);
  });

  it("renders a header cell per column with scope=col", () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    const headers = screen.getAllByRole("columnheader");
    expect(headers).toHaveLength(2);
    expect(headers[0]).toHaveTextContent("Host");
    expect(headers[1]).toHaveTextContent("CPU");
    headers.forEach((h) => expect(h).toHaveAttribute("scope", "col"));
  });

  it("renders cell content via Column.cell", () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    expect(screen.getByText("host-a")).toBeInTheDocument();
    expect(screen.getByText("87%")).toBeInTheDocument();
  });

  it("applies Column.align to both th and td for that column", () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    const headers = screen.getAllByRole("columnheader");
    expect(headers[1]).toHaveStyle({ textAlign: "right" });
    const cpuCell = screen.getByText("12%");
    expect(cpuCell.closest("td")).toHaveStyle({ textAlign: "right" });
  });

  it("applies Column.width as an inline style on the header cell", () => {
    const widthCols: Column<Row>[] = [
      { key: "name", header: "Host", width: "200px", cell: (r) => r.name },
    ];
    render(<Table columns={widthCols} rows={rows} rowKey={(r) => r.id} />);
    expect(screen.getByRole("columnheader")).toHaveStyle({ width: "200px" });
  });

  it("renders nothing but an empty tbody when rows is empty", () => {
    render(<Table columns={columns} rows={[]} rowKey={(r) => r.id} />);
    const body = screen.getByRole("table").querySelector("tbody")!;
    expect(body.querySelectorAll("tr")).toHaveLength(0);
  });
});

// A long flat list says what is there and nothing about what belongs with
// what. Grouping is that, and it lives here because a group has to interact
// with the sort state, which only this component holds.
describe("Table grouping", () => {
  interface Grouped {
    id: string;
    site: string;
    name: string;
  }

  const groupedColumns: Column<Grouped>[] = [
    {
      key: "name",
      header: "Host",
      cell: (r) => r.name,
      sortValue: (r) => r.name,
    },
  ];

  const groupedRows: Grouped[] = [
    { id: "1", site: "zrh", name: "host-b" },
    { id: "2", site: "ams", name: "host-c" },
    { id: "3", site: "zrh", name: "host-a" },
    { id: "4", site: "", name: "host-d" },
  ];

  const bySite = {
    key: (r: Grouped) => r.site,
    label: (key: string, rs: readonly Grouped[]) =>
      `${key || "unplaced"} (${rs.length})`,
  };

  it("renders one tbody per group, each under its own rowheader", () => {
    render(
      <Table
        columns={groupedColumns}
        rows={groupedRows}
        rowKey={(r) => r.id}
        groupBy={bySite}
      />,
    );
    expect(screen.getByRole("table").querySelectorAll("tbody")).toHaveLength(3);
    expect(screen.getAllByRole("rowheader").map((h) => h.textContent)).toEqual([
      "ams (1)",
      "zrh (2)",
      "unplaced (1)",
    ]);
  });

  // Ordered by key rather than by first appearance, so two renders of the
  // same data read the same way -- and the unnamed group is last in either
  // case, the way an unknown sortValue is.
  it("orders the groups by key and puts the unnamed one last", () => {
    render(
      <Table
        columns={groupedColumns}
        rows={groupedRows}
        rowKey={(r) => r.id}
        groupBy={bySite}
      />,
    );
    const heads = screen.getAllByRole("rowheader").map((h) => h.textContent);
    expect(heads.at(-1)).toContain("unplaced");
  });

  it("sorts WITHIN a group, never across them", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    render(
      <Table
        columns={groupedColumns}
        rows={groupedRows}
        rowKey={(r) => r.id}
        groupBy={bySite}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Host" }));

    // host-c is alphabetically last but sits in the first group, because the
    // group it belongs to is not something a column sort may move it out of.
    expect(screen.getAllByRole("cell").map((c) => c.textContent)).toEqual([
      "host-c",
      "host-a",
      "host-b",
      "host-d",
    ]);
  });

  // Identity and order are two questions, and they come apart whenever the
  // key is an id: the fleet groups containers by host_id (two hosts may share
  // a hostname) but must list those groups the way the Hosts tab lists hosts.
  it("orders the groups by `order` when the key is not what to sort on", () => {
    render(
      <Table
        columns={groupedColumns}
        rows={[
          { id: "1", site: "9", name: "web-01" },
          { id: "2", site: "10", name: "app-01" },
          { id: "3", site: "", name: "loose" },
        ]}
        rowKey={(r) => r.id}
        groupBy={{
          key: (r: Grouped) => r.site,
          order: (_k: string, rs: readonly Grouped[]) => rs[0]!.name,
          label: (key: string) => key || "unplaced",
        }}
      />,
    );
    // By name: app-01 (site 10) before web-01 (site 9). By key it would be
    // the other way round, numerically.
    expect(screen.getAllByRole("rowheader").map((h) => h.textContent)).toEqual([
      "10",
      "9",
      "unplaced",
    ]);
  });

  it("is unchanged without groupBy: one tbody, no rowheader", () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    expect(screen.getByRole("table").querySelectorAll("tbody")).toHaveLength(1);
    expect(screen.queryAllByRole("rowheader")).toHaveLength(0);
  });
});

// A group that can be shut is only honest if its heading keeps answering what
// its rows would have -- which is why `summary` and `collapsible` arrive
// together, and why every test below checks both halves.
describe("Table collapsible groups", () => {
  interface Grouped {
    id: string;
    site: string;
    name: string;
  }

  const groupedColumns: Column<Grouped>[] = [
    { key: "name", header: "Host", cell: (r) => r.name },
  ];

  const groupedRows: Grouped[] = [
    { id: "1", site: "zrh", name: "host-a" },
    { id: "2", site: "ams", name: "host-b" },
  ];

  const collapsible = {
    key: (r: Grouped) => r.site,
    label: (key: string) => key,
    labelText: (key: string) => key,
    collapsible: true,
    summary: (_key: string, rs: readonly Grouped[]) => `${rs.length} up`,
  };

  function renderTable(over: Record<string, unknown> = {}) {
    return render(
      <Table
        columns={groupedColumns}
        rows={groupedRows}
        rowKey={(r) => r.id}
        groupBy={{ ...collapsible, ...over }}
      />,
    );
  }

  // OPEN rather than closed. Closed shipped a Containers tab that arrived
  // showing a column of hostnames and not one container -- a list that hides
  // its own contents has not summarised them.
  it("starts open, with every group's rows in the document", () => {
    renderTable();

    expect(screen.getByText("host-a")).toBeInTheDocument();
    expect(screen.getByText("host-b")).toBeInTheDocument();
    expect(screen.getAllByRole("rowheader")).toHaveLength(2);
  });

  // The summary is readable in both states: it is what a shut group says about
  // itself, and it must not appear or vanish as one is folded.
  it("keeps the summary readable open or shut", () => {
    renderTable();
    expect(screen.getAllByText("1 up")).toHaveLength(2);

    fireEvent.click(screen.getAllByRole("button", { expanded: true })[0]!);

    expect(screen.getAllByText("1 up")).toHaveLength(2);
  });

  it("shuts one group without shutting the others", () => {
    renderTable();

    // Ordered by key: ams first, zrh second.
    fireEvent.click(screen.getAllByRole("button", { expanded: true })[0]!);

    expect(screen.queryByText("host-b")).toBeNull();
    expect(screen.getByText("host-a")).toBeInTheDocument();
  });

  // The disclosure's accessible name cannot come from the heading beside it:
  // that is a heading, not a label for this control, and the key is not always
  // readable either (the fleet groups on a host id).
  it("names the disclosure after the group, not after the key", () => {
    renderTable({ labelText: () => "Zurich" });

    expect(
      screen.getAllByRole("button", { name: "Zurich" }).length,
    ).toBeGreaterThan(0);
  });

  // What a search box sets. Rows arrive already filtered, so a group still
  // standing has a hit in it, and a hit inside a shut group is a hit the
  // reader cannot see.
  it("forceExpanded opens every group at once", () => {
    renderTable({ forceExpanded: true });

    expect(screen.getByText("host-a")).toBeInTheDocument();
    expect(screen.getByText("host-b")).toBeInTheDocument();
  });

  // The filter overrides the reader's choice WITHOUT overwriting it, so
  // clearing the box puts the list back exactly where they left it.
  it("restores what the reader had shut when forceExpanded goes away", () => {
    const { rerender } = renderTable();

    // Shut ams by hand, leave zrh open.
    fireEvent.click(screen.getAllByRole("button", { expanded: true })[0]!);
    expect(screen.queryByText("host-b")).toBeNull();

    const table = (over: Record<string, unknown>) => (
      <Table
        columns={groupedColumns}
        rows={groupedRows}
        rowKey={(r: Grouped) => r.id}
        groupBy={{ ...collapsible, ...over }}
      />
    );
    rerender(table({ forceExpanded: true }));
    expect(screen.getByText("host-b")).toBeInTheDocument();

    rerender(table({ forceExpanded: false }));
    expect(screen.getByText("host-a")).toBeInTheDocument();
    expect(screen.queryByText("host-b")).toBeNull();
  });

  // The same promise, against the click that used to break it: while the
  // filter holds every group open the disclosure cannot move anything, so a
  // reader who clicks one -- meaning to shut the noise -- must not have that
  // click applied behind the filter and handed back to them when it clears.
  it("ignores a click while forceExpanded, rather than remembering it", () => {
    const table = (over: Record<string, unknown>) => (
      <Table
        columns={groupedColumns}
        rows={groupedRows}
        rowKey={(r: Grouped) => r.id}
        groupBy={{ ...collapsible, ...over }}
      />
    );
    const { rerender } = render(table({}));

    // Shut ams by hand, then filter: both groups are open now.
    fireEvent.click(screen.getAllByRole("button", { expanded: true })[0]!);
    rerender(table({ forceExpanded: true }));

    // Click both headers while the filter holds them open: nothing moves.
    for (const button of screen.getAllByRole("button", { expanded: true })) {
      fireEvent.click(button);
    }
    expect(screen.getByText("host-a")).toBeInTheDocument();
    expect(screen.getByText("host-b")).toBeInTheDocument();

    // ... and clearing it leaves exactly what the reader had: ams shut, zrh
    // open. Without the guard, both clicks would have landed -- ams open,
    // zrh shut.
    rerender(table({ forceExpanded: false }));
    expect(screen.getByText("host-a")).toBeInTheDocument();
    expect(screen.queryByText("host-b")).toBeNull();
  });

  // A group that is not collapsible has no disclosure at all -- the lists
  // that group without summarising are unchanged.
  it("draws no disclosure when the caller did not ask for one", () => {
    render(
      <Table
        columns={groupedColumns}
        rows={groupedRows}
        rowKey={(r) => r.id}
        groupBy={{ key: (r: Grouped) => r.site, label: (key: string) => key }}
      />,
    );

    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(screen.getByText("host-a")).toBeInTheDocument();
  });
});
