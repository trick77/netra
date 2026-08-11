import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { ABSENT } from "../../lib/format";
import {
  FleetContainers,
  containerColumns,
  type ContainerRow,
} from "./FleetContainers";

function makeRow(overrides: Partial<ContainerRow> = {}): ContainerRow {
  return {
    id: 1,
    container_key: "9f2c1ab3",
    name: "postgres",
    image: "postgres:16",
    is_agent: false,
    host_id: 7,
    hostname: "db-01",
    ...overrides,
  };
}

describe("containerColumns", () => {
  it("carries a Host column fleet-wide and drops it on a single host", () => {
    expect(containerColumns({ showHost: true }).map((c) => c.header)).toContain(
      "Host",
    );
    expect(
      containerColumns({ showHost: false }).map((c) => c.header),
    ).not.toContain("Host");
  });
});

describe("FleetContainers", () => {
  it("renders the same row plus a Host column", () => {
    render(<FleetContainers rows={[makeRow()]} showHost />);

    const headers = screen
      .getAllByRole("columnheader")
      .map((h) => h.textContent);
    expect(headers).toEqual(
      containerColumns({ showHost: true }).map((c) => c.header),
    );

    const row = screen.getAllByRole("row")[1]!;
    expect(within(row).getByText("postgres")).toBeInTheDocument();
    expect(within(row).getByText("db-01")).toBeInTheDocument();
  });

  it("renders an unnamed container as absent, never as a blank or a zero", () => {
    render(<FleetContainers rows={[makeRow({ name: null, image: null })]} />);

    const row = screen.getAllByRole("row")[1]!;
    expect(within(row).getAllByText(ABSENT).length).toBe(2);
    // The key still identifies it -- absence of a name is not absence of a
    // container.
    expect(within(row).getByText("9f2c1ab3")).toBeInTheDocument();
  });

  it("links each container to its detail page", () => {
    render(<FleetContainers rows={[makeRow()]} />);

    expect(screen.getByRole("link", { name: /postgres/ })).toHaveAttribute(
      "href",
      "#/containers/7/9f2c1ab3",
    );
  });

  it("says so when the fleet runs no containers", () => {
    render(<FleetContainers rows={[]} />);

    expect(screen.getByText(/no containers/i)).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("marks the agent's own container so it is not read as a workload", () => {
    render(<FleetContainers rows={[makeRow({ is_agent: true })]} />);

    expect(screen.getByText("agent")).toBeInTheDocument();
  });
});
