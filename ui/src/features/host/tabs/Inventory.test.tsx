import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  Address,
  Container,
  Filesystem,
  MetricsResponse,
  Pkg,
  Unit,
} from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import {
  Containers,
  Filesystems,
  Inventory,
  Network,
  Packages,
  Units,
  changedSince,
} from "./Inventory";

const containers: Container[] = [
  {
    id: 41,
    container_key: "netra/hub",
    name: "netra-hub-1",
    image: "netra:0.4",
    is_agent: false,
  },
  {
    id: 42,
    container_key: "shop/web",
    name: "shop-web-1",
    image: "nginx:1.27",
    is_agent: false,
  },
];

const filesystems: Filesystem[] = [
  { id: 1, label: "/", mountpoint: "/", device_id: 3 },
  { id: 2, label: "data", mountpoint: "/srv/data", device_id: 4 },
];

const addresses: Address[] = [
  {
    iface: "eth0",
    if_index: 2,
    address: "192.0.2.10/24",
    family: 4,
    scope: "global",
    vrf: null,
    description: null,
    first_seen: "2026-07-01T00:00:00Z",
    last_seen: "2026-08-10T00:00:00Z",
  },
];

function pkg(over: Partial<Pkg> = {}): Pkg {
  return {
    name: "openssl",
    version: "3.0.13",
    arch: "amd64",
    format: "deb",
    size_bytes: 2_000_000,
    first_seen: "2026-01-01T00:00:00Z",
    last_seen: "2026-08-10T00:00:00Z",
    ...over,
  };
}

const units: Unit[] = [
  {
    id: 1,
    unit_name: "ssh.service",
    state: "active",
    substate: "running",
    since: "2026-08-01T00:00:00Z",
    restarts_1h: 0,
  },
  {
    id: 2,
    unit_name: "cron.service",
    state: "failed",
    substate: "dead",
    since: "2026-08-09T00:00:00Z",
    restarts_1h: 0,
  },
];

describe("Inventory", () => {
  it("is one component parameterised by columns", () => {
    render(
      <Inventory
        label="Things"
        columns={[
          { key: "n", header: "Name", cell: (r: { n: string }) => r.n },
        ]}
        rows={[{ n: "alpha" }, { n: "beta" }]}
        rowKey={(r) => r.n}
        searchText={(r) => r.n}
      />,
    );
    expect(
      screen.getByRole("columnheader", { name: "Name" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("row")).toHaveLength(3);
  });

  it("filters on the search box", async () => {
    render(
      <Inventory
        label="Things"
        columns={[
          { key: "n", header: "Name", cell: (r: { n: string }) => r.n },
        ]}
        rows={[{ n: "alpha" }, { n: "beta" }]}
        rowKey={(r) => r.n}
        searchText={(r) => r.n}
      />,
    );
    await userEvent.type(screen.getByRole("searchbox"), "alp");
    expect(screen.getAllByRole("row")).toHaveLength(2);
    expect(screen.queryByText("beta")).toBeNull();
  });

  it("offers an empty state rather than an empty table", () => {
    render(
      <Inventory
        label="Things"
        columns={[
          { key: "n", header: "Name", cell: (r: { n: string }) => r.n },
        ]}
        rows={[]}
        rowKey={(r) => r.n}
        searchText={(r) => r.n}
      />,
    );
    expect(screen.queryByRole("table")).toBeNull();
    expect(
      screen.getByRole("heading", { name: /nothing/i }),
    ).toBeInTheDocument();
  });
});

describe("Containers", () => {
  const host = { id: 7, hostname: "web-01" };

  // The service, never the project beside it: this list GROUPS by project, so
  // the project is already the heading these rows sit under, and printing it
  // again on every one of them spends the widest column in the table saying
  // what the reader was just told. (The fleet's list, which groups by host,
  // still carries both -- there the project is in no heading at all.)
  it("identifies a container by its compose service, the project being the group", () => {
    render(<Containers rows={containers} host={host} />);
    const row = screen.getByRole("row", { name: /shop-web-1/ });
    expect(within(row).getByText("web")).toBeInTheDocument();
    expect(within(row).queryByText("shop / web")).toBeNull();
    // The Docker id changes on every `compose up -d`; showing it invites
    // people to key on it, which orphans all history.
    expect(row.textContent).not.toContain("42");
  });

  // ... and not even the service, when the name already carries it: "grafana"
  // under a name of "grafana" is one fact printed twice. The line exists for
  // the names that do NOT say it -- a compose-generated "monitoring_loki_1",
  // or a container renamed away from its service.
  it("drops the identity line when the name already says the same thing", () => {
    render(
      <Containers
        rows={[
          {
            id: 51,
            container_key: "monitoring/grafana",
            name: "grafana",
            image: "grafana/grafana:11.2.0",
            is_agent: false,
          },
          {
            id: 52,
            container_key: "monitoring/loki",
            name: "monitoring_loki_1",
            image: "grafana/loki:3.1.1",
            is_agent: false,
          },
        ]}
        host={host}
      />,
    );

    // "grafana" appears once in its row -- as the link. No second line under
    // it repeating the service. Located via the link rather than by row name:
    // the sibling's image is "grafana/loki", so /grafana/ matches both rows.
    const said = screen
      .getByRole("link", { name: "grafana" })
      .closest("tr") as HTMLElement;
    expect(within(said).getAllByText("grafana")).toHaveLength(1);

    // The compose-numbered name does not say "loki", so the line stays.
    const unsaid = screen.getByRole("row", { name: /monitoring_loki_1/ });
    expect(within(unsaid).getByText("loki")).toBeInTheDocument();
  });

  // The regression this whole alignment exists for: the fleet's container
  // list has always linked into container detail and this one never did, so
  // from a host's own Containers tab the page was simply unreachable.
  it("links every container to its detail page", () => {
    render(<Containers rows={containers} host={host} />);
    expect(
      screen.getByRole("link", { name: "shop-web-1" }).getAttribute("href"),
      // The key is "project/service" and router.ts splits the path before it
      // decodes it, so the slash must survive as %2F or the link 404s.
    ).toBe("/containers/7/shop%2Fweb");
  });

  // One list of containers, not two: the fleet tab and this tab render the
  // same Column[] now, so a column that sorts there sorts here.
  it("sorts, the way the fleet's container list always did", () => {
    render(<Containers rows={containers} host={host} />);
    expect(screen.getByRole("button", { name: "Image" })).toBeInTheDocument();
  });

  // On one host, what belongs together is a compose stack.
  it("groups the list by compose project", () => {
    render(<Containers rows={containers} host={host} />);
    expect(
      screen.getByRole("rowheader", { name: /netra/ }),
    ).toBeInTheDocument();
    expect(screen.getByRole("rowheader", { name: /shop/ })).toBeInTheDocument();
  });

  // A key with no slash is a container the agent could not read compose
  // labels for. It has no project, and "no project" is not a project.
  it("keeps a container with no compose project out of the named groups", () => {
    render(
      <Containers
        rows={[
          containers[0]!,
          {
            id: 43,
            container_key: "a1b2c3d4e5f6",
            name: null,
            image: null,
            is_agent: false,
          },
        ]}
        host={host}
      />,
    );
    const heads = screen
      .getAllByRole("rowheader")
      .map((el) => el.textContent ?? "");
    expect(heads.at(-1)).toContain("No compose project");
  });

  it("carries no first_seen/last_seen columns, because the schema has none", () => {
    render(<Containers rows={containers} host={host} />);
    expect(
      screen.queryByRole("columnheader", { name: /first seen/i }),
    ).toBeNull();
    expect(
      screen.queryByRole("columnheader", { name: /last seen/i }),
    ).toBeNull();
  });

  // "This host has reported no containers" is true of a host running none and
  // of a host whose agent cannot see the ones it runs. Only the second is
  // something to go and fix, and only the agent knows which it is.
  it("says why the list is empty when the agent explained it", () => {
    render(
      <Containers
        rows={[]}
        host={host}
        capabilities={{ containers: "no-cgroup-scopes" }}
      />,
    );

    expect(screen.getByText(/\/host\/sys\/fs\/cgroup/)).toBeInTheDocument();
    expect(screen.getByText(/setup-agent\.sh/)).toBeInTheDocument();
    // Alongside the empty state, not instead of it: "nothing was collected"
    // and "here is what stopped it" are both facts, and the second does not
    // replace the first.
    expect(screen.getByText("Nothing collected yet")).toBeInTheDocument();
  });

  // The milder value, on a list that is fully present and badly labelled.
  it("explains raw ids on a list it has not emptied", () => {
    render(
      <Containers
        rows={containers}
        host={host}
        capabilities={{ containers: "no-docker-socket" }}
      />,
    );

    expect(screen.getByText(/Docker socket/)).toBeInTheDocument();
    expect(screen.getByRole("row", { name: /shop-web-1/ })).toBeInTheDocument();
  });

  it("stays silent when the agent reported no trouble", () => {
    render(
      <Containers
        rows={containers}
        host={host}
        capabilities={{ smart: "absent" }}
      />,
    );

    expect(screen.queryByText(/Docker socket/)).toBeNull();
    expect(screen.queryByText(/setup-agent\.sh/)).toBeNull();
  });
});

describe("Filesystems", () => {
  it("lists label and mountpoint without inventing timestamps", () => {
    render(<Filesystems rows={filesystems} />);
    expect(screen.getByText("/srv/data")).toBeInTheDocument();
    expect(
      screen.queryByRole("columnheader", { name: /last seen/i }),
    ).toBeNull();
  });
});

describe("Network", () => {
  // "Last changed", not "Last seen": the agent only sends addresses when the
  // set changes, so the column holds the time of the last change and not the
  // last time the host was heard from. See the comment on the column.
  it("shows first seen and last changed, which the schema does have", () => {
    render(<Network rows={addresses} />);
    expect(
      screen.getByRole("columnheader", { name: /first seen/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("columnheader", { name: /last changed/i }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("columnheader", { name: /last seen/i }),
    ).toBeNull();
    expect(screen.getByText("192.0.2.10/24")).toBeInTheDocument();
  });

  // The interface alias the agent reports. It was searchable and shown
  // nowhere, so an operator who had labelled every NIC still got a table of
  // bare kernel names.
  it("shows the interface description, and the absent marker when there is none", () => {
    render(
      <Network
        rows={[
          { ...addresses[0]!, description: "uplink to core-sw1" },
          { ...addresses[0]!, address: "192.0.2.11/24", description: null },
        ]}
      />,
    );
    expect(
      screen.getByRole("columnheader", { name: /description/i }),
    ).toBeInTheDocument();
    expect(screen.getByText("uplink to core-sw1")).toBeInTheDocument();
  });

  // sysfs cannot identify a VRF master -- drivers/net/vrf.c sets no DEVTYPE
  // -- so the collector writes its documented vrfUnknown for every interface
  // on every host. A column structurally incapable of holding a value trains
  // people to stop reading the table.
  it("carries no VRF column, which could never hold a value", () => {
    render(<Network rows={addresses} />);
    expect(screen.queryByRole("columnheader", { name: /vrf/i })).toBeNull();
  });
});

describe("Packages", () => {
  const now = new Date("2026-08-10T00:00:00Z");
  const rows = [
    pkg({ name: "openssl", first_seen: "2026-08-05T00:00:00Z" }),
    pkg({
      name: "vim",
      first_seen: "2026-01-01T00:00:00Z",
      last_seen: "2026-08-10T00:00:00Z",
    }),
  ];

  it("filters to what changed in the last 30 days", async () => {
    render(<Packages rows={rows} now={now} />);
    expect(screen.getByText("vim")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("checkbox", { name: /30 days/i }));
    expect(screen.getByText("openssl")).toBeInTheDocument();
    expect(screen.queryByText("vim")).toBeNull();
  });

  it("counts a package as changed when it first appeared inside the window", () => {
    expect(
      changedSince(pkg({ first_seen: "2026-08-05T00:00:00Z" }), now, 30),
    ).toBe(true);
    expect(
      changedSince(pkg({ first_seen: "2026-01-01T00:00:00Z" }), now, 30),
    ).toBe(false);
  });
});

describe("Units", () => {
  it("shows since, the only timestamp the schema carries for a unit", () => {
    render(<Units rows={units} />);
    expect(
      screen.getByRole("columnheader", { name: /since/i }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("columnheader", { name: /first seen/i }),
    ).toBeNull();
  });

  it("gives a failed unit a dot and the word", () => {
    render(<Units rows={units} />);
    const row = screen.getByRole("row", { name: /cron/ });
    expect(within(row).getByText("failed")).toBeInTheDocument();
  });

  // The list shows only units that need attention, so empty is the COMMON case
  // -- a healthy host. The generic copy ("This host has reported no units")
  // would be a flat falsehood about a host running several hundred of them,
  // and an empty table with no copy at all reads as a loading bug.
  it("says a healthy host is healthy, not that it reported no units", () => {
    render(<Units rows={[]} />);
    expect(screen.getByText(/nothing needs attention/i)).toBeInTheDocument();
    expect(screen.queryByText(/reported no units/i)).toBeNull();
  });

  // A unit that keeps dying and coming back reads active/running at nearly
  // every scrape, so its row would otherwise look like a mistake: a green
  // badge in a list of things that need attention. The restart count is the
  // reason it is there, so the table has to show it.
  it("shows why a unit that looks healthy is in the list at all", () => {
    render(
      <Units
        rows={[
          {
            id: 3,
            unit_name: "backup.service",
            state: "active",
            substate: "running",
            since: "2026-08-09T00:00:00Z",
            restarts_1h: 9,
          },
        ]}
      />,
    );
    const row = screen.getByRole("row", { name: /backup/ });
    expect(within(row).getByText("9")).toBeInTheDocument();
  });
});

describe("Filesystems", () => {
  const rows = [
    { id: 1, label: "root", mountpoint: "/", device_id: null },
    { id: 2, label: "data", mountpoint: "/data", device_id: null },
  ];

  const metrics = {
    family: "filesystem",
    tier: "raw",
    step_s: 60,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:02:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T00:02:00Z",
    },
    warnings: [],
    key_columns: ["filesystem"],
    columns: ["total", "used", "free"],
    series: [
      {
        key: { filesystem: "root" },
        points: [[Date.parse("2026-08-10T00:00:00Z"), 110, 68, 32]],
      },
    ],
    truncated: false,
  } as unknown as MetricsResponse;

  // The inventory row carries a label and a mountpoint and nothing else, so
  // the tab answered none of the question anyone opens it for. The sizes
  // come from the metrics family, joined by label.
  it("joins the sizes in from the metrics family", () => {
    render(<Filesystems rows={rows} metrics={metrics} />);

    expect(screen.getByText("68 B")).toBeInTheDocument();
    expect(screen.getByText("32 B")).toBeInTheDocument();
  });

  // A filesystem the metrics did not answer for is not an empty disk.
  it("renders the absent marker for a filesystem with no samples", () => {
    render(<Filesystems rows={rows} metrics={metrics} />);

    const dataRow = screen.getByText("data").closest("tr")!;
    expect(dataRow.textContent).toContain(ABSENT);
    expect(dataRow.querySelector(".meter")).toBeNull();
  });
});
