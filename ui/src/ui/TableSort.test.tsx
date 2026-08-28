import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Table, type Column } from "./Table";

interface Row {
  id: string;
  name: string;
  uptime: number | null;
}

const rows: Row[] = [
  { id: "b", name: "host-10", uptime: 500 },
  { id: "a", name: "host-2", uptime: null },
  { id: "c", name: "host-1", uptime: 100 },
];

const columns: Column<Row>[] = [
  {
    key: "name",
    header: "Host",
    cell: (r) => r.name,
    sortValue: (r) => r.name,
  },
  {
    key: "uptime",
    header: "Uptime",
    cell: (r) => String(r.uptime ?? "—"),
    sortValue: (r) => r.uptime,
  },
  // No sortValue: a sparkline has no order, and this column must stay a
  // plain header rather than becoming a control that does nothing.
  { key: "cpu", header: "CPU", cell: () => "chart" },
];

function names() {
  return screen
    .getAllByRole("row")
    .slice(1)
    .map((r) => r.querySelector("td")?.textContent);
}

describe("Table sorting", () => {
  it("offers a control only on the columns that declared one", () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);

    expect(screen.getByRole("button", { name: "Host" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Uptime" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "CPU" })).toBeNull();
  });

  it("leaves the caller's order alone until asked", () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);

    expect(names()).toEqual(["host-10", "host-2", "host-1"]);
  });

  // "host-2" before "host-10": a reader scanning hostnames reads the number
  // as a number, and plain lexical order puts host-10 first.
  it("sorts hostnames the way a human numbers them", async () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    await userEvent.click(screen.getByRole("button", { name: "Host" }));

    expect(names()).toEqual(["host-1", "host-2", "host-10"]);
  });

  it("reverses on the second click and clears on the third", async () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    const host = screen.getByRole("button", { name: "Host" });

    await userEvent.click(host);
    await userEvent.click(host);
    expect(names()).toEqual(["host-10", "host-2", "host-1"]);

    // Back to the order the caller gave -- for the fleet that is the
    // server's own ordering, which a reader otherwise cannot get back to
    // without reloading the page.
    await userEvent.click(host);
    expect(names()).toEqual(["host-10", "host-2", "host-1"]);
    expect(host).toHaveAttribute("data-sort", "none");
  });

  // A host with no uptime reading is not the shortest-lived host on the
  // page. Flipping the arrow must not promote "unknown" to the top.
  it("keeps unknown values last in both directions", async () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    const uptime = screen.getByRole("button", { name: "Uptime" });

    await userEvent.click(uptime);
    expect(names()).toEqual(["host-1", "host-10", "host-2"]);

    await userEvent.click(uptime);
    expect(names()).toEqual(["host-10", "host-1", "host-2"]);
  });

  // A list whose most useful order is not the one the server sent should
  // arrive in it, rather than making every reader sort it by hand.
  it("arrives in the caller's defaultSort order", () => {
    render(
      <Table
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        defaultSort={{ key: "uptime", dir: "desc" }}
      />,
    );

    expect(names()).toEqual(["host-10", "host-1", "host-2"]);
    expect(
      screen.getByRole("columnheader", { name: /Uptime/ }),
    ).toHaveAttribute("aria-sort", "descending");
  });

  it("flips a defaulted column on the first click rather than clearing it", async () => {
    render(
      <Table
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        defaultSort={{ key: "uptime", dir: "desc" }}
      />,
    );
    const uptime = screen.getByRole("button", { name: "Uptime" });

    await userEvent.click(uptime);
    expect(names()).toEqual(["host-1", "host-10", "host-2"]);

    // Three stops from where it stood, not four: descending was the first of
    // them, so the next click is the last one and clears it back to the
    // caller's order.
    await userEvent.click(uptime);
    expect(names()).toEqual(["host-10", "host-2", "host-1"]);
    expect(uptime).toHaveAttribute("data-sort", "none");
  });

  it("hands the order to the reader from there, and does not take it back", async () => {
    render(
      <Table
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        defaultSort={{ key: "uptime", dir: "desc" }}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Host" }));
    expect(names()).toEqual(["host-1", "host-2", "host-10"]);
  });

  it("tells assistive tech which column is sorted, and how", async () => {
    render(<Table columns={columns} rows={rows} rowKey={(r) => r.id} />);
    await userEvent.click(screen.getByRole("button", { name: "Host" }));

    const header = screen.getByRole("columnheader", { name: /Host/ });
    expect(header).toHaveAttribute("aria-sort", "ascending");
  });
});
