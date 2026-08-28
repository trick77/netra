import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  Address,
  Container,
  Drive,
  Filesystem,
  Iface,
  MetricsResponse,
  Pkg,
  Unit,
} from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import {
  Containers,
  Drives,
  Interfaces,
  Mounts,
  Inventory,
  Network,
  Packages,
  Units,
} from "./Inventory";

const containers: Container[] = [
  {
    id: 41,
    container_key: "netra/hub",
    name: "netra-hub-1",
    image: "netra:0.4",
    is_agent: false,
    last_seen: "2026-08-10T14:00:00Z",
  },
  {
    id: 42,
    container_key: "shop/web",
    name: "shop-web-1",
    image: "nginx:1.27",
    is_agent: false,
    last_seen: "2026-08-10T14:00:00Z",
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
    version_changed_at: "2026-01-01T00:00:00Z",
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
  const host = {
    id: 7,
    hostname: "web-01",
    last_seen: "2026-08-10T14:00:00Z",
  };

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
            last_seen: "2026-08-10T14:00:00Z",
          },
          {
            id: 52,
            container_key: "monitoring/loki",
            name: "monitoring_loki_1",
            image: "grafana/loki:3.1.1",
            is_agent: false,
            last_seen: "2026-08-10T14:00:00Z",
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
            last_seen: "2026-08-10T14:00:00Z",
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

  // last_seen is on containers now (0006_container_seen.sql) and is what the
  // gone pill is derived from. first_seen stays off the wire: it is the
  // prune's hub-clocked floor, and beside an agent-clocked last_seen it
  // could read first_seen > last_seen after a replay.
  it("carries a last_seen column and no first_seen, matching the schema", () => {
    render(<Containers rows={containers} host={host} />);
    expect(
      screen.queryByRole("columnheader", { name: /first seen/i }),
    ).toBeNull();
    expect(
      screen.getByRole("columnheader", { name: /last seen/i }),
    ).toBeInTheDocument();
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

describe("Mounts", () => {
  it("lists label and mountpoint without inventing timestamps", () => {
    render(<Mounts rows={filesystems} />);
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
  // The alias is a fact about the INTERFACE, so it moved to the Interfaces
  // table: on an address-keyed table one alias was printed once per address,
  // so eth0 with a v4, a v6 and a link-local showed the same sentence three
  // times. Only the COLUMN moved -- the field still arrives and is still
  // searched here, so typing an alias still finds its addresses.
  it("no longer repeats the interface alias once per address", () => {
    render(
      <Network
        rows={[
          { ...addresses[0]!, description: "uplink to core-sw1" },
          { ...addresses[0]!, address: "192.0.2.11/24", description: null },
        ]}
      />,
    );
    expect(
      screen.queryByRole("columnheader", { name: /description/i }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("uplink to core-sw1")).not.toBeInTheDocument();
  });

  // The hub already classified every address (AddressScope in
  // internal/hub/store/scope.go); it was printed in a column two away from
  // the address it described.
  it("marks each address with the scope the hub derived", () => {
    render(
      <Network
        rows={[
          { ...addresses[0]!, scope: "public" },
          { ...addresses[0]!, address: "10.0.0.1/8", scope: "private" },
          { ...addresses[0]!, address: "127.0.0.1/8", scope: "loopback" },
        ]}
      />,
    );
    expect(screen.getByText("public")).toBeInTheDocument();
    expect(screen.getByText("private")).toBeInTheDocument();
    expect(screen.getByText("loopback")).toBeInTheDocument();
    // The pill IS the scope, so the column is gone.
    expect(
      screen.queryByRole("columnheader", { name: /scope/i }),
    ).not.toBeInTheDocument();
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
  // Alphabetical order is the REVERSE of the change order here, so a test
  // claiming to have re-sorted the list has to have actually done it.
  const rows = [
    pkg({
      name: "apt",
      first_seen: "2026-01-01T00:00:00Z",
      version_changed_at: "2026-01-01T00:00:00Z",
    }),
    pkg({
      name: "zlib1g",
      first_seen: "2026-01-01T00:00:00Z",
      version_changed_at: "2026-08-05T00:00:00Z",
    }),
  ];

  // The list arrives answering "what moved on this host recently", which is
  // not the order the server sends (name, arch) and not an order the reader
  // should have to ask for.
  it("arrives sorted by what changed last", () => {
    render(<Packages rows={rows} />);
    const names = screen
      .getAllByRole("row")
      .slice(1)
      .map((row) => row.querySelector("td")?.textContent);
    expect(names).toEqual(["zlib1g", "apt"]);
  });

  it("lets the reader sort by any column from there", async () => {
    render(<Packages rows={rows} />);
    await userEvent.click(screen.getByRole("button", { name: /name/i }));
    const names = screen
      .getAllByRole("row")
      .slice(1)
      .map((row) => row.querySelector("td")?.textContent);
    expect(names).toEqual(["apt", "zlib1g"]);
  });

  // The first click on the column the list ARRIVED sorted on has to flip it,
  // not clear it: a reader after oldest-changed-first gets it in one.
  it("flips the column it arrived sorted on", async () => {
    render(<Packages rows={rows} />);
    await userEvent.click(
      screen.getByRole("button", { name: /last changed/i }),
    );
    const names = screen
      .getAllByRole("row")
      .slice(1)
      .map((row) => row.querySelector("td")?.textContent);
    expect(names).toEqual(["apt", "zlib1g"]);
  });

  // It filtered on first_seen, which an upgrade never moves: the row is keyed
  // (host_id, name, arch) and rewritten in place. Last changed is the column
  // that answers what it claimed to.
  it("carries no 30-day filter", () => {
    render(<Packages rows={rows} />);
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(
      screen.getByRole("columnheader", { name: /last changed/i }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("columnheader", { name: /last seen/i }),
    ).toBeNull();
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

describe("Mounts", () => {
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
    render(<Mounts rows={rows} metrics={metrics} />);

    expect(screen.getByText("68 B")).toBeInTheDocument();
    expect(screen.getByText("32 B")).toBeInTheDocument();
  });

  // A filesystem the metrics did not answer for is not an empty disk.
  it("renders the absent marker for a filesystem with no samples", () => {
    render(<Mounts rows={rows} metrics={metrics} />);

    const dataRow = screen.getByText("data").closest("tr")!;
    expect(dataRow.textContent).toContain(ABSENT);
    expect(dataRow.querySelector(".meter")).toBeNull();
  });
});

describe("Drives", () => {
  const ata: Drive = {
    device: "sdc",
    model: "ST16000NM000J-2TW103",
    serial: "ZR5A1PQ2",
    attributes: [
      { id: 5, raw: 238, normalized: 68 },
      { id: 9, raw: 21_402, normalized: 98 },
      { id: 194, raw: 49, normalized: 71 },
      { id: 197, raw: 15, normalized: 68 },
    ],
    last_seen: "2026-08-23T11:00:00Z",
  };

  const nvme: Drive = {
    device: "nvme0n1",
    model: "SAMSUNG MZQL21T9HCJR-00A07",
    serial: "S64FNE0R512345",
    attributes: [
      { id: 1000, raw: 0, normalized: null },
      { id: 1001, raw: 7, normalized: null },
      { id: 1006, raw: 9400, normalized: null },
      { id: 1008, raw: 49, normalized: null },
    ],
    last_seen: "2026-08-23T11:00:00Z",
  };

  const unread: Drive = {
    device: "sdz",
    model: null,
    serial: null,
    attributes: [],
    // Its readings aged out under retention; the devices row still dates it.
    last_seen: "2026-08-23T11:00:00Z",
  };

  it("names what is wrong with a failing drive, worst first", () => {
    render(<Drives rows={[ata]} />);
    const row = screen.getByRole("row", { name: /sdc/ });
    expect(within(row).getByText("15 pending sectors")).toBeInTheDocument();
    expect(
      within(row).getByText("238 reallocated sectors"),
    ).toBeInTheDocument();
    // The pill carries the worst of them.
    expect(within(row).getByText("critical")).toBeInTheDocument();
  });

  // A healthy drive SAYS so. On a table whose whole point is advance warning,
  // "we checked, it is fine" and "there is nothing left to check" must not
  // render the same.
  it("distinguishes a healthy drive from one it could not read", () => {
    render(<Drives rows={[nvme, unread]} />);

    const healthy = screen.getByRole("row", { name: /nvme0n1/ });
    expect(within(healthy).getByText("healthy")).toBeInTheDocument();

    const notRead = screen.getByRole("row", { name: /sdz/ });
    expect(within(notRead).getByText("not read")).toBeInTheDocument();
    expect(within(notRead).queryByText("healthy")).not.toBeInTheDocument();
  });

  it("reads temperature and power-on hours from whichever id space the drive uses", () => {
    render(<Drives rows={[ata, nvme]} />);
    // Both report 49 degrees, from attribute 194 and 1008 respectively.
    expect(screen.getAllByText("49 °C")).toHaveLength(2);
  });

  // The smart family, keyed by device and attr_id. attr_id is TEXT on the
  // wire (s.attr_id::text in read/family.go), which is why the join compares
  // strings -- a numeric comparison type-checks and matches nothing, and the
  // column then renders exactly as it did before the history existed.
  const smart = {
    family: "smart",
    tier: "raw",
    step_s: 3600,
    window: { from: "2026-08-23T09:00:00Z", to: "2026-08-23T12:00:00Z" },
    requested_window: {
      from: "2026-08-23T09:00:00Z",
      to: "2026-08-23T12:00:00Z",
    },
    warnings: [],
    key_columns: ["device", "attr_id"],
    columns: ["raw", "normalized"],
    series: [
      {
        key: { device: "sdc", attr_id: "194" },
        points: [
          // Packed, as a real drive reports it: 0x1C0000001C is 28 °C.
          [Date.parse("2026-08-23T09:00:00Z"), 0x1c0000001c, 71],
          [Date.parse("2026-08-23T10:00:00Z"), 0x1d0000001d, 71],
          [Date.parse("2026-08-23T11:00:00Z"), 0x1e0000001e, 71],
        ],
      },
      // Another attribute of the same drive, which must not be plotted as a
      // temperature.
      {
        key: { device: "sdc", attr_id: "9" },
        points: [[Date.parse("2026-08-23T09:00:00Z"), 21_402, 98]],
      },
    ],
    truncated: false,
  } as unknown as MetricsResponse;

  // A temperature is only interesting as a movement, which is the whole
  // reason the history is in this cell rather than in a panel below the
  // table: this is where the number already is.
  it("draws each drive's temperature history beside its reading", () => {
    render(<Drives rows={[ata, nvme]} range="24h" metrics={smart} />);

    const row = screen.getByRole("row", { name: /sdc/ });
    expect(
      within(row).getByRole("img", { name: /sdc temperature trend/ }),
    ).toBeInTheDocument();
    // The reading is still the drive's own current one, unchanged.
    expect(within(row).getByText("49 °C")).toBeInTheDocument();
  });

  // A drive the family answered nothing for still has a reading, and a
  // reading with no line is a complete answer -- SMART is hourly, so a drive
  // first seen this hour has exactly that.
  it("renders the reading alone for a drive with no history", () => {
    render(<Drives rows={[ata, nvme]} range="24h" metrics={smart} />);

    const row = screen.getByRole("row", { name: /nvme0n1/ });
    expect(within(row).getByText("49 °C")).toBeInTheDocument();
    expect(within(row).queryByRole("img")).toBeNull();
  });

  // NVMe reports consumed endurance; ATA has no comparable figure, and a
  // vendor-specific life-left counter read as though it were standard would
  // print a confident number meaning something different per model.
  it("shows wear for NVMe and the absent marker for ATA", () => {
    render(<Drives rows={[ata, nvme]} />);
    expect(
      within(screen.getByRole("row", { name: /nvme0n1/ })).getByText("7%"),
    ).toBeInTheDocument();
    const ataRow = screen.getByRole("row", { name: /sdc/ });
    expect(within(ataRow).queryByText(/%$/)).not.toBeInTheDocument();
  });

  it("finds a drive by its findings, not only by its name", () => {
    render(<Drives rows={[ata, nvme]} />);
    fireEvent.change(screen.getByRole("searchbox", { name: "Filter Drives" }), {
      target: { value: "pending" },
    });
    expect(screen.getByRole("row", { name: /sdc/ })).toBeInTheDocument();
    expect(
      screen.queryByRole("row", { name: /nvme0n1/ }),
    ).not.toBeInTheDocument();
  });

  // The empty state states the fact and no more. Why the table is empty is
  // the agent's to say, and it says it in the notice below -- guessing at all
  // three reasons here sent somebody looking for whichever one did not apply.
  it("states an empty table as a fact rather than guessing at the cause", () => {
    render(<Drives rows={[]} />);
    expect(screen.getByText("No drives reported")).toBeInTheDocument();
    expect(
      screen.getByText("No drive on this host has reported SMART data."),
    ).toBeInTheDocument();
  });

  // "No drives reported" is true of a host with no disks and of a container
  // agent with no device mapped in. Only the second is something to go and
  // fix, and only the agent knows which it is.
  it("says why the table is empty when the agent explained it", () => {
    render(<Drives rows={[]} capabilities={{ smart: "no-devices" }} />);

    expect(screen.getByText(/smartctl/)).toBeInTheDocument();
    expect(screen.getByText(/setup-agent\.sh/)).toBeInTheDocument();
    // Alongside the empty state, not instead of it.
    expect(screen.getByText("No drives reported")).toBeInTheDocument();
  });

  // The value on a table that is fully present and still worth explaining:
  // drives were scanned and none answered, so nothing was collected for any
  // of them.
  it("explains drives that answered nothing", () => {
    render(
      <Drives
        rows={[unread]}
        capabilities={{ smart: "no-readable-devices" }}
      />,
    );

    expect(screen.getByText(/produced a reading/)).toBeInTheDocument();
    expect(screen.getByRole("row", { name: /sdz/ })).toBeInTheDocument();
  });

  // It used to assert the passthrough was working, which is false as often as
  // it is true: `--scan` globs /dev and opens nothing, so a node the container
  // can see but not open -- the device cgroup rule missing -- lists here and
  // then fails every read, and the operator was told to go and look at their
  // hardware instead of at the grant.
  it("does not claim device access works when no drive answered", () => {
    render(
      <Drives rows={[]} capabilities={{ smart: "no-readable-devices" }} />,
    );

    expect(screen.getByText(/not open them/)).toBeInTheDocument();
    expect(screen.getByText(/setup-agent\.sh/)).toBeInTheDocument();
  });

  // A SAS host: every drive answers and reports SCSI health pages instead of
  // an attribute table. Nothing to fix, so no remedy -- but it must not be
  // silent, because silence is what made an unread SATA host look like one
  // whose first hourly reading had not landed yet.
  it("explains drives that answered without an attribute table", () => {
    render(<Drives rows={[]} capabilities={{ smart: "no-attributes" }} />);

    expect(screen.getByText(/SAS drives/)).toBeInTheDocument();
    expect(screen.queryByText(/setup-agent\.sh/)).toBeNull();
    expect(screen.getByText("No drives reported")).toBeInTheDocument();
  });

  // An empty table that is working as intended still has to say so. The one
  // capability that names no remedy, because there is nothing to fix.
  it("explains a host whose only drives are USB-attached", () => {
    render(<Drives rows={[]} capabilities={{ smart: "usb-only-devices" }} />);

    expect(screen.getByText(/USB-attached/)).toBeInTheDocument();
    expect(screen.queryByText(/setup-agent\.sh/)).toBeNull();
    expect(screen.getByText("No drives reported")).toBeInTheDocument();
  });

  it("stays silent when the agent reported no trouble", () => {
    render(
      <Drives rows={[ata]} capabilities={{ containers: "no-docker-socket" }} />,
    );

    expect(screen.queryByText(/setup-agent\.sh/)).toBeNull();
    expect(screen.queryByText(/produced a reading/)).toBeNull();
  });
});

// Every inventory table shipped with zero sortable columns: the shared Table
// sorts any column that hands it a `sortValue`, and none of these ever did.
// Forty columns of strings, byte counts and timestamps, each of them an
// obvious thing to order a list by, and the only order available was the
// hub's own ORDER BY.
//
// These tests click through the real header buttons rather than reaching for
// the accessors, because the accessor is only half of it -- a column with a
// sortValue and no button in its header sorts nothing.
describe("inventory sorting", () => {
  /** The visible text of the first cell of every body row, in order. */
  function firstCells(): string[] {
    const [, ...rows] = screen.getAllByRole("row");
    return rows.map((row) => within(row).getAllByRole("cell")[0]!.textContent!);
  }

  function header(name: RegExp) {
    return within(screen.getByRole("columnheader", { name })).getByRole(
      "button",
    );
  }

  /**
   * Clicks every column header of the rendered table, asserting each one is a
   * control and that no click loses a row.
   *
   * Every header, not a sample: the point of these tables is that all of
   * their columns sort now, and an accessor that throws on a null or reads
   * the wrong field only shows itself when its own column is clicked.
   */
  async function sortByEveryColumn() {
    const before = screen.getAllByRole("row").length;

    for (const cell of screen.getAllByRole("columnheader")) {
      await userEvent.click(within(cell).getByRole("button"));
      expect(screen.getAllByRole("row")).toHaveLength(before);
    }
  }

  describe("Mounts", () => {
    // The sizes are joined in from the metrics family by label, so a row
    // literal cannot carry them -- see Mounts itself.
    const mounts = [
      { id: 1, label: "root", mountpoint: "/", device_id: null },
      { id: 2, label: "data", mountpoint: "/data", device_id: null },
    ];

    // root: 500 GB, 450 used, 50 free -> 90% of what df counts.
    // data: 8 TB, 800 used, 7200 free -> 10%.
    const sizes = {
      family: "filesystem",
      tier: "raw",
      step_s: 60,
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:01:00Z" },
      requested_window: {
        from: "2026-08-10T00:00:00Z",
        to: "2026-08-10T00:01:00Z",
      },
      warnings: [],
      key_columns: ["filesystem"],
      columns: ["total", "used", "free"],
      series: [
        {
          key: { filesystem: "root" },
          points: [[Date.parse("2026-08-10T00:00:00Z"), 500e9, 450e9, 50e9]],
        },
        {
          key: { filesystem: "data" },
          points: [[Date.parse("2026-08-10T00:00:00Z"), 8000e9, 800e9, 7200e9]],
        },
      ],
      truncated: false,
    } as unknown as MetricsResponse;

    it("sorts on every column", async () => {
      render(<Mounts rows={mounts} metrics={sizes} />);

      await sortByEveryColumn();
    });

    // The cells read "500 GB" and "8 TB", which as strings put the 8 TB mount
    // first. Sorting on the byte count is the whole point of the column
    // having its own accessor.
    it("orders Size by bytes and not by the formatted string", async () => {
      render(<Mounts rows={mounts} metrics={sizes} />);
      await userEvent.click(header(/size/i));

      expect(firstCells()).toEqual(["root", "data"]);
    });

    // The bigger disk is the emptier one, so Usage and Size must not agree.
    it("orders Usage by the same Use% the meter draws", async () => {
      render(<Mounts rows={mounts} metrics={sizes} />);
      await userEvent.click(header(/usage/i));

      expect(firstCells()).toEqual(["data", "root"]);
    });
  });

  describe("Network", () => {
    const two = [
      addresses[0]!,
      { ...addresses[0]!, iface: "eth1", address: "192.0.2.9/24", family: 6 },
    ];

    it("sorts on every column", async () => {
      render(<Network rows={two} />);

      await sortByEveryColumn();
    });

    // .10 above .9: the shared comparator is numeric-aware, which is the one
    // property that makes an address column worth sorting at all.
    it("orders addresses numerically, not lexically", async () => {
      render(<Network rows={two} />);
      await userEvent.click(header(/address/i));

      expect(firstCells()).toEqual(["eth1", "eth0"]);
    });

    // The family NUMBER, so v4 groups before v6 whatever they are called.
    it("orders Family by the number behind the name", async () => {
      render(<Network rows={two} />);
      await userEvent.click(header(/family/i));

      expect(firstCells()).toEqual(["eth0", "eth1"]);
    });
  });

  describe("Interfaces", () => {
    const ifaces: Iface[] = [
      {
        iface: "eth0",
        if_index: 2,
        oper_state: "up",
        speed_mbps: 10_000,
        duplex: "full",
        mtu: 9000,
        mac: "aa:bb:cc:dd:ee:01",
        description: "uplink",
        first_seen: "2026-07-01T00:00:00Z",
        last_seen: "2026-08-01T00:00:00Z",
      },
      {
        iface: "eth1",
        if_index: 3,
        oper_state: "down",
        speed_mbps: 100,
        duplex: null,
        mtu: 1500,
        mac: null,
        description: null,
        first_seen: "2026-07-01T00:00:00Z",
        last_seen: "2026-08-02T00:00:00Z",
      },
    ];

    it("sorts on every column", async () => {
      render(<Interfaces rows={ifaces} />);

      await sortByEveryColumn();
    });

    // "10 Gb/s" and "100 Mb/s" as strings put the 10 Gb link first, which is
    // the wrong end. Megabits do not.
    it("orders Speed by megabits and not by the printed unit", async () => {
      render(<Interfaces rows={ifaces} />);
      await userEvent.click(header(/speed/i));

      expect(firstCells()[0]).toMatch(/eth1/);
    });

    it("orders MTU by the number, so 9000 sorts above 1500", async () => {
      render(<Interfaces rows={ifaces} />);
      await userEvent.click(header(/mtu/i));

      expect(firstCells()[0]).toMatch(/eth1/);
    });
  });

  describe("Drives", () => {
    // 197 pending sectors is a finding; a drive with no attributes has none
    // to judge and reads as ok.
    const failing: Drive = {
      device: "sda",
      model: "ST16000NM000J",
      serial: "ZR5A1PQ2",
      attributes: [
        { id: 9, raw: 40_000, normalized: 98 },
        { id: 194, raw: 49, normalized: 71 },
        { id: 197, raw: 15, normalized: 68 },
      ],
      last_seen: "2026-08-23T11:00:00Z",
    };
    const healthy: Drive = {
      device: "sdb",
      model: "WDC WD40EFRX",
      serial: "WD-WCC4E",
      attributes: [
        { id: 9, raw: 100, normalized: 99 },
        { id: 194, raw: 30, normalized: 80 },
      ],
      last_seen: "2026-08-23T11:00:00Z",
    };

    it("sorts on every column", async () => {
      render(<Drives rows={[failing, healthy]} />);

      await sortByEveryColumn();
    });

    // The reason the table exists: the drives in trouble have to collect at
    // one end, which ordering on the finding TEXT would never do.
    it("orders Findings by severity, worst last ascending", async () => {
      render(<Drives rows={[failing, healthy]} />);
      await userEvent.click(header(/findings/i));

      expect(firstCells()[0]).toMatch(/sdb/);
    });

    // 49 °C against 30 °C, and the cell prints "49 °C" -- a string sort would
    // agree here by luck, so the assertion is on the reading being numeric.
    it("orders Temp by degrees", async () => {
      render(<Drives rows={[failing, healthy]} />);
      await userEvent.click(header(/temp/i));

      expect(firstCells()[0]).toMatch(/sdb/);
    });

    // The cell prints "4 y" and "4 d"; hours are what separate them.
    it("orders Power on by hours and not by the printed duration", async () => {
      render(<Drives rows={[failing, healthy]} />);
      await userEvent.click(header(/power on/i));

      expect(firstCells()[0]).toMatch(/sdb/);
    });
  });

  describe("Packages", () => {
    const rows = [
      pkg({ name: "vim", size_bytes: 900_000_000 }),
      pkg({ name: "openssl", size_bytes: 2_000_000_000 }),
    ];

    it("sorts on every column", async () => {
      render(<Packages rows={rows} />);

      await sortByEveryColumn();
    });

    it("orders Size by bytes", async () => {
      render(<Packages rows={rows} />);
      await userEvent.click(header(/size/i));

      expect(firstCells()).toEqual(["vim", "openssl"]);
    });
  });

  describe("Units", () => {
    const flapping = [
      { ...units[0]!, restarts_1h: 9 },
      { ...units[1]!, restarts_1h: 0 },
    ];

    it("sorts on every column", async () => {
      render(<Units rows={flapping} />);

      await sortByEveryColumn();
    });

    // The badge above the threshold must not change how the number orders.
    it("orders Restarts by the count, badge or no badge", async () => {
      render(<Units rows={flapping} />);
      await userEvent.click(header(/restarts/i));

      expect(firstCells()).toEqual(["cron.service", "ssh.service"]);
    });

    it("orders Since by the instant behind the relative phrase", async () => {
      render(<Units rows={flapping} />);
      await userEvent.click(header(/since/i));

      expect(firstCells()).toEqual(["ssh.service", "cron.service"]);
    });
  });
});
