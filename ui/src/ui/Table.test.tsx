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
