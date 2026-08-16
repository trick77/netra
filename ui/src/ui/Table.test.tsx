import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
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

  it("is unchanged without groupBy: one tbody, no rowheader", () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    expect(screen.getByRole("table").querySelectorAll("tbody")).toHaveLength(1);
    expect(screen.queryAllByRole("rowheader")).toHaveLength(0);
  });
});
