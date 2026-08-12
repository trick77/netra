import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host, Site } from "../../lib/api";
import { ABSENT } from "../../lib/format";
import type { HostRow } from "./hostColumns";
import type { ContainerRow } from "./FleetContainers";
import { FleetPage, buildHostRows, DENSITY_KEY } from "./FleetPage";

const NOW = new Date("2026-08-10T14:00:00Z");

function makeRow(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    site_id: 3,
    site_name: "zurich-dc1",
    last_seen: "2026-08-10T13:59:30Z",
    cpu_total: 42,
    mem_used: 4_000_000_000,
    mem_total: 16_000_000_000,
    uptime_s: 864_000,
    threads: null,
    cpu: [{ name: "user", color: "var(--s1)", values: [10, 12] }],
    reporting: [10, 12],
    mem: [{ name: "used", color: "var(--s1)", values: [3e9, 4e9] }],
    rx: [1e6, 2e6],
    tx: [5e5, 6e5],
    fullest: { mount: "/", pct: 41, others: 1 },
    disk: [],
    oomKills: null,
    ...overrides,
  };
}

function makeContainer(overrides: Partial<ContainerRow> = {}): ContainerRow {
  return {
    id: 1,
    container_key: "9f2c1ab3",
    name: "postgres",
    image: "postgres:16",
    is_agent: false,
    host_id: 1,
    hostname: "web-01",
    ...overrides,
  };
}

/** The Containers TAB. The Containers stat tile above it is now a link of
 * the same name, so every query for one has to exclude the other. */
function containersTab(): HTMLElement {
  const nav = document.querySelector<HTMLElement>("nav.tabs")!;
  return within(nav).getByRole("link", { name: /containers/i });
}

/** A stat tile by the href it leads to -- its accessible name is the whole
 * tile ("Containers 22 across the fleet"), which is not worth matching on. */
function tile(href: string): HTMLElement {
  return document.querySelector<HTMLElement>(`a.tile[href="${href}"]`)!;
}

function renderPage(props: Parameters<typeof FleetPage>[0] = {}) {
  return render(
    <FleetPage
      now={NOW}
      rows={[makeRow()]}
      containers={[makeContainer()]}
      {...props}
    />,
  );
}

beforeEach(() => {
  window.localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("FleetPage entity tabs", () => {
  it("swaps the list and the filter's placeholder when the entity changes", async () => {
    const user = userEvent.setup();
    renderPage();

    expect(
      screen.getByRole("columnheader", { name: "Host" }),
    ).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/filter hosts/i)).toBeInTheDocument();

    await user.click(containersTab());

    expect(
      screen.getByPlaceholderText(/filter containers/i),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "postgres" })).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "Uptime" })).toBeNull();
  });

  // A card grid of 247 containers is not useful, so density is a hosts-only
  // axis (spec 4.5).
  it("hides the density toggle in the containers view", async () => {
    const user = userEvent.setup();
    renderPage();

    expect(screen.getByRole("button", { name: "Cards" })).toBeInTheDocument();

    await user.click(containersTab());

    expect(screen.queryByRole("button", { name: "Cards" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Table" })).toBeNull();
  });
});

describe("FleetPage toolbar", () => {
  it("switches the host list between table and cards, and remembers it", async () => {
    const user = userEvent.setup();
    const { unmount } = renderPage();

    await user.click(screen.getByRole("button", { name: "Cards" }));
    expect(screen.queryByRole("table")).toBeNull();
    expect(window.localStorage.getItem(DENSITY_KEY)).toBe("cards");

    unmount();
    renderPage();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("filters the list by what the tab is showing", async () => {
    const user = userEvent.setup();
    renderPage({
      rows: [makeRow(), makeRow({ id: 2, hostname: "db-01" })],
    });

    await user.type(screen.getByPlaceholderText(/filter hosts/i), "db");

    expect(screen.getByText("db-01")).toBeInTheDocument();
    expect(screen.queryByText("web-01")).toBeNull();
  });
});

describe("FleetPage header", () => {
  it("replaces the attention band with one quiet line when nothing is wrong", () => {
    renderPage();

    expect(screen.getByText(/nothing needs attention/i)).toBeInTheDocument();
  });

  it("shows the band, and no all-clear line, when something is wrong", () => {
    renderPage({
      conditions: [
        {
          hostId: "1",
          hostname: "web-01",
          severity: "critical",
          what: "disk 99% full",
          since: "2026-08-10T13:00:00Z",
        },
      ],
    });

    expect(screen.getByText(/disk 99% full/)).toBeInTheDocument();
    expect(screen.queryByText(/nothing needs attention/i)).toBeNull();
  });

  it("counts only hosts that have actually reported recently", () => {
    renderPage({
      rows: [
        makeRow(),
        makeRow({ id: 2, hostname: "db-01", last_seen: null }),
        makeRow({
          id: 3,
          hostname: "old-01",
          last_seen: "2026-08-10T13:00:00Z",
        }),
      ],
    });

    const tile = screen
      .getByText("Hosts reporting")
      .closest<HTMLElement>(".tile")!;
    expect(within(tile).getByText("1")).toBeInTheDocument();
    expect(within(tile).getByText(/of 3/)).toBeInTheDocument();
  });

  // Absent is not zero: with no traffic series fetched yet the fleet has an
  // unknown throughput, not a throughput of nothing.
  it("renders unknown fleet traffic as absent, never as zero", () => {
    renderPage({ rows: [makeRow({ rx: [], tx: [] })] });

    const tile = screen
      .getByText("Fleet traffic")
      .closest<HTMLElement>(".tile")!;
    expect(within(tile).getByText(ABSENT)).toBeInTheDocument();
  });
});

describe("buildHostRows", () => {
  const hosts: Host[] = [
    {
      id: 1,
      hostname: "web-01",
      site_id: 3,
      last_seen: "2026-08-10T13:59:30Z",
      cpu_total: 42,
      mem_used: 4e9,
      mem_total: 16e9,
      uptime_s: 100,
      threads: null,
    },
    {
      id: 2,
      hostname: "db-01",
      site_id: null,
      last_seen: null,
      cpu_total: null,
      mem_used: null,
      mem_total: null,
      uptime_s: null,
      threads: null,
    },
  ];
  const sites: Site[] = [
    {
      id: 3,
      provider_id: null,
      name: "zurich-dc1",
      facility: null,
      address: null,
      latitude: null,
      longitude: null,
      country_code: null,
      timezone: null,
    },
  ];

  it("joins the site name client-side by site_id", () => {
    const rows = buildHostRows(hosts, sites);

    expect(rows[0]!.site_name).toBe("zurich-dc1");
    expect(rows[1]!.site_name).toBeNull();
  });

  it("leaves every unfetched series empty rather than inventing a zero", () => {
    const rows = buildHostRows(hosts, sites);

    expect(rows[0]!.cpu).toEqual([]);
    expect(rows[0]!.rx).toEqual([]);
    // null, not a zero percentage: nothing measured this host's disks, and
    // an empty green meter would say they are empty.
    expect(rows[0]!.fullest).toBeNull();
  });
});

describe("FleetPage data fetching", () => {
  it("resolves site names with one /sites call, never one detail call per host", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const body = url.includes("/sites")
        ? [{ id: 3, name: "zurich-dc1" }]
        : url.includes("/containers")
          ? []
          : [
              {
                id: 1,
                hostname: "web-01",
                site_id: 3,
                last_seen: "2026-08-10T13:59:30Z",
                cpu_total: 1,
                mem_used: null,
                mem_total: null,
                uptime_s: 10,
                threads: null,
              },
            ];
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<FleetPage now={NOW} />);

    await waitFor(() => expect(screen.getByText("web-01")).toBeInTheDocument());
    expect(screen.getByText("zurich-dc1")).toBeInTheDocument();

    const urls = fetchMock.mock.calls.map(([u]) => String(u));
    expect(urls.filter((u) => u.endsWith("/api/v1/sites"))).toHaveLength(1);
    expect(urls.filter((u) => /\/api\/v1\/hosts\/\d+$/.test(u))).toHaveLength(
      0,
    );
  });

  // Containers are a per-host fan-out with no fleet-wide endpoint; one
  // failing call must not claim the host list that already rendered is gone.
  it("keeps the host list when only the container fan-out fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/containers"))
          return new Response("", { status: 500 });
        return new Response(
          JSON.stringify(
            url.includes("/sites")
              ? []
              : [
                  {
                    id: 1,
                    hostname: "web-01",
                    site_id: null,
                    last_seen: "2026-08-10T13:59:30Z",
                    cpu_total: 1,
                    mem_used: null,
                    mem_total: null,
                    uptime_s: 10,
                    threads: null,
                  },
                ],
          ),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }),
    );

    render(<FleetPage now={NOW} />);

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        /containers did not/i,
      ),
    );
    expect(screen.getByText("web-01")).toBeInTheDocument();
  });

  it("dates the all-clear line from injected data too", () => {
    renderPage({ checkedAt: "2026-08-10T13:59:20Z" });

    expect(screen.getByText(/checked 40 s ago/)).toBeInTheDocument();
  });

  it("says what went wrong instead of rendering an empty fleet", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("nope", { status: 500 })),
    );

    render(<FleetPage now={NOW} />);

    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
  });

  // The band existed from the start and had nothing to render: `conditions`
  // defaulted to [], so this page said "nothing needs attention" beside a
  // host whose own page was showing three OOM kills in red.
  it("derives the attention band from the rows instead of rendering an empty one", () => {
    render(
      <FleetPage
        rows={[
          makeRow({ id: 1, hostname: "web-01" }),
          makeRow({ id: 2, hostname: "db-01", oomKills: 3 }),
        ]}
        checkedAt={null}
        now={NOW}
      />,
    );

    expect(screen.getByText(/3 OOM kills/)).toBeInTheDocument();
    expect(screen.queryByText(/nothing needs attention/)).toBeNull();
  });

  it("counts the troubled hosts, not the conditions, above the band", () => {
    render(
      <FleetPage
        rows={[
          makeRow({ id: 1, hostname: "web-01" }),
          makeRow({
            id: 2,
            hostname: "db-01",
            oomKills: 3,
            fullest: { mount: "/var/log", pct: 97, others: 0 },
          }),
        ]}
        checkedAt={null}
        now={NOW}
      />,
    );

    // One host in trouble, two conditions on it.
    expect(screen.getByText(/1 of 2 hosts/)).toBeInTheDocument();
  });

  // A filter is someone looking for one machine. Recomputing the band from
  // the filtered rows would hide a critical host because its name does not
  // match what was typed, which is exactly how an overview lies.
  it("keeps the band whole while the list is filtered", async () => {
    const user = userEvent.setup();
    render(
      <FleetPage
        rows={[
          makeRow({ id: 1, hostname: "web-01" }),
          makeRow({ id: 2, hostname: "db-01", oomKills: 3 }),
        ]}
        checkedAt={null}
        now={NOW}
      />,
    );

    await user.type(screen.getByPlaceholderText(/filter hosts/i), "web");
    expect(screen.getByText(/3 OOM kills/)).toBeInTheDocument();
  });

  it("still shows the quiet all-clear line for a healthy fleet", () => {
    render(
      <FleetPage rows={[makeRow({ id: 1 })]} checkedAt={null} now={NOW} />,
    );
    expect(screen.getByText(/nothing needs attention/)).toBeInTheDocument();
  });
});

describe("FleetPage stat tiles as controls", () => {
  it("switches to the containers list when the Containers tile is clicked", async () => {
    const user = userEvent.setup();
    renderPage();

    expect(screen.getByPlaceholderText(/filter hosts/i)).toBeInTheDocument();

    await user.click(tile("/?entity=containers"));

    expect(
      screen.getByPlaceholderText(/filter containers/i),
    ).toBeInTheDocument();
  });

  it("returns to the hosts list from the Hosts reporting tile", async () => {
    const user = userEvent.setup();
    renderPage({ entity: "containers" });

    await user.click(tile("/"));

    expect(screen.getByPlaceholderText(/filter hosts/i)).toBeInTheDocument();
  });

  // Real links, so middle-click, cmd-click and bookmarking work without this
  // component reimplementing any of them. The hrefs match the tabs' own.
  it("gives the navigable tiles the same hrefs as the tabs", () => {
    renderPage();

    expect(tile("/")).not.toBeNull();
    expect(tile("/?entity=containers")).not.toBeNull();
  });

  // Fleet traffic is a rate, not a set: there is no list of it to go to, and
  // a tile that looks clickable and does nothing is worse than an inert one.
  it("leaves the fleet traffic tile inert", () => {
    renderPage();

    expect(screen.getByText("Fleet traffic").closest("a")).toBeNull();
  });
});
