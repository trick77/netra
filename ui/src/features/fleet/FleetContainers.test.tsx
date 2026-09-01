import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { ABSENT } from "../../lib/format";
import { groupLabels } from "../../testing/groups";
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
    docker_state: null,
    health: null,
    state_since: null,
    restart_count: null,
    labels: null,
    last_seen: "2026-08-10T14:00:00Z",
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

    // Groups start collapsed, so the rows are not in the DOM until the
    // disclosure is opened.
    // [0] is the header rail and [1] the host's group header; the data
    // rows follow it.
    const row = screen.getAllByRole("row")[2]!;
    expect(within(row).getByText("postgres")).toBeInTheDocument();
    expect(within(row).getByText("db-01")).toBeInTheDocument();
  });

  // A fleet-wide list is a list of several hosts' containers, and which host
  // a container runs on is the first thing that groups them.
  it("groups the rows by host, naming and linking each one", () => {
    render(
      <FleetContainers
        rows={[
          makeRow(),
          makeRow({
            id: 2,
            container_key: "web",
            host_id: 8,
            hostname: "web-01",
          }),
        ]}
      />,
    );

    const heads = screen.getAllByRole("rowheader");
    expect(groupLabels()).toEqual([
      "db-01 · 1 container",
      "web-01 · 1 container",
    ]);
    expect(
      within(heads[0]!).getByRole("link", { name: "db-01" }),
    ).toHaveAttribute("href", "/hosts/7/overview");
  });

  // Identity is the id; ORDER is the name. On the id the groups came out in
  // registration order under headings that read as names, beside a Hosts tab
  // the read API returns alphabetically.
  it("orders the host groups by hostname, not by host id", () => {
    render(
      <FleetContainers
        rows={[
          makeRow({ host_id: 9, hostname: "app-01" }),
          makeRow({ id: 2, host_id: 3, hostname: "web-01" }),
        ]}
      />,
    );

    expect(groupLabels()).toEqual([
      "app-01 \u00b7 1 container",
      "web-01 \u00b7 1 container",
    ]);
  });

  // Two hosts in different sites may share a hostname (see HostTable), so
  // grouping on the NAME would merge two machines into one group and file
  // one host's containers under the other host's link.
  it("groups on the host id, not on the hostname", () => {
    render(
      <FleetContainers
        rows={[
          makeRow({ host_id: 7, hostname: "db-01" }),
          makeRow({ id: 2, host_id: 9, hostname: "db-01" }),
        ]}
      />,
    );

    expect(screen.getAllByRole("rowheader")).toHaveLength(2);
  });

  it("renders an unnamed container as absent, never as a blank or a zero", () => {
    render(<FleetContainers rows={[makeRow({ name: null, image: null })]} />);

    const row = screen.getAllByRole("row")[2]!;
    expect(within(row).getAllByText(ABSENT).length).toBe(2);
    // The key still identifies it -- absence of a name is not absence of a
    // container.
    expect(within(row).getByText("9f2c1ab3")).toBeInTheDocument();
  });

  it("links each container to its detail page", () => {
    render(<FleetContainers rows={[makeRow()]} />);

    expect(screen.getByRole("link", { name: /postgres/ })).toHaveAttribute(
      "href",
      "/containers/7/9f2c1ab3",
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

  // "None reported" and "not read yet" are different facts, and only the
  // first is a statement about the fleet. Saying it while the fan-out had
  // never run told an operator their fleet ran no containers when nobody
  // had looked.
  it("does not claim the fleet runs no containers before they are read", () => {
    render(<FleetContainers rows={[]} showHost loaded={false} />);

    expect(screen.queryByText(/no host in this fleet/i)).toBeNull();
    expect(screen.getByText(/not read yet/i)).toBeInTheDocument();
  });

  it("says the fleet runs none once they have been read", () => {
    render(<FleetContainers rows={[]} showHost loaded />);

    expect(screen.getByText(/no host in this fleet/i)).toBeInTheDocument();
  });

  // A column of independently scaled sparklines compares nothing: each row
  // fills its own box, so the busiest container and the idlest draw the
  // same picture. The ceiling is shared across the list.
  it("scales every row's CPU against the list's peak, not its own", () => {
    const busy = makeRow({
      container_key: "web/api",
      cpu: [10, 90],
      mem: [1, 2],
    });
    const idle = makeRow({
      container_key: "web/cron",
      cpu: [1, 2],
      mem: [1, 2],
    });

    const { container } = render(
      <FleetContainers rows={[busy, idle]} showHost loaded />,
    );

    const paths = [...container.querySelectorAll("path[data-line]")].map((p) =>
      p.getAttribute("d"),
    );
    // Four charts (two rows x CPU+memory); the two CPU lines must differ.
    expect(paths.length).toBeGreaterThanOrEqual(4);
    expect(paths[0]).not.toBe(paths[2]);
  });

  // A list nobody fetched metrics for renders as it always did, rather than
  // growing two columns of permanent gaps.
  it("shows no trend columns when the rows carry no trends", () => {
    render(<FleetContainers rows={[makeRow()]} showHost loaded />);

    expect(screen.queryByRole("columnheader", { name: "CPU" })).toBeNull();
  });
});

// A host whose cgroup hierarchy is not mounted contributes NO rows, so nothing
// in `rows` can explain the gap -- the explanation has to come from the hosts
// the rows are missing from. Before this, a misconfigured fleet was
// indistinguishable from a fleet running nothing.
describe("FleetContainers and the containers capability", () => {
  const broken = {
    hostname: "db-01",
    capabilities: { containers: "no-cgroup-scopes" },
  };

  it("explains an empty list instead of shrugging at it", () => {
    render(<FleetContainers rows={[]} showHost loaded hosts={[broken]} />);

    expect(screen.getByText("No containers collected")).toBeInTheDocument();
    expect(screen.getByText(/setup-agent\.sh/)).toBeInTheDocument();
    expect(
      screen.queryByText("No host in this fleet has reported a container."),
    ).toBeNull();
  });

  // The generic wording is still right for a fleet that genuinely runs none.
  it("keeps the plain empty state when no host reported a problem", () => {
    render(
      <FleetContainers
        rows={[]}
        showHost
        loaded
        hosts={[{ hostname: "db-01", capabilities: {} }]}
      />,
    );

    expect(
      screen.getByText("No host in this fleet has reported a container."),
    ).toBeInTheDocument();
  });

  // The mixed fleet: rows present, and still short by a whole host.
  it("says a non-empty list is incomplete, above the table", () => {
    const { container } = render(
      <FleetContainers
        rows={[makeRow({ host_id: 9, hostname: "web-01" })]}
        showHost
        loaded
        hosts={[broken, { hostname: "web-01", capabilities: {} }]}
      />,
    );

    const note = screen.getByText(/This list is incomplete/);
    expect(note).toBeInTheDocument();
    expect(note.textContent).toContain("db-01");
    // Above the table, not below it: a list short by a host looks complete,
    // and nobody scrolls past a table they believe is finished.
    const table = container.querySelector("table");
    expect(
      note.compareDocumentPosition(table!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  // no-docker-socket means the names are missing, NOT the containers. A fleet
  // of hosts with no Docker installed reports it alongside an empty list and
  // is perfectly healthy -- "No containers collected" over that turns a fleet
  // running none into a fault and drops the only true sentence about it.
  it("does not call a Docker-less fleet a collection failure", () => {
    render(
      <FleetContainers
        rows={[]}
        showHost
        loaded
        hosts={[
          {
            hostname: "tiny",
            capabilities: { containers: "no-docker-socket" },
          },
        ]}
      />,
    );

    expect(
      screen.getByText("No host in this fleet has reported a container."),
    ).toBeInTheDocument();
    expect(screen.queryByText("No containers collected")).toBeNull();
    // Beside the empty state, not instead of it: both facts hold.
    expect(screen.getByText(/Docker socket/)).toBeInTheDocument();
  });

  // A filter is not a fact about the fleet, so the fleet-wide sentence must
  // not be the answer to one.
  it("answers an unmatched filter about the filter, not about the fleet", () => {
    render(
      <FleetContainers rows={[]} showHost loaded filtered hosts={[broken]} />,
    );

    expect(screen.getByText("No containers match")).toBeInTheDocument();
    expect(
      screen.queryByText("No host in this fleet has reported a container."),
    ).toBeNull();
    expect(screen.queryByText(/setup-agent\.sh/)).toBeNull();
  });

  // Still fetching is a different fact from incomplete, and only one of them
  // is a statement about the fleet.
  it("stays quiet while the fan-out is still running", () => {
    render(
      <FleetContainers rows={[]} showHost loaded={false} hosts={[broken]} />,
    );

    expect(screen.getByText("Containers not read yet")).toBeInTheDocument();
    expect(screen.queryByText(/setup-agent\.sh/)).toBeNull();
  });
});
