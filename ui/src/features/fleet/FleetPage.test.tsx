import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host, Site } from "../../lib/api";
import { ABSENT } from "../../lib/format";
import type { HostRow } from "./hostColumns";
import type { ContainerRow } from "./FleetContainers";
import { FleetPage, buildHostRows, DENSITY_KEY } from "./FleetPage";
import { expandAllGroups } from "../../testing/groups";

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
    // The gauges the traffic cell and the fleet tile actually read. The
    // series above are the sparkline, which still follows the range; these
    // do not, which is the whole point of them being separate.
    net_rx_bytes: 1.5e6,
    net_tx_bytes: 5.5e5,
    fullest: { mount: "/", pct: 41, others: 1 },
    disk: [],
    oomKills: null,
    dropped: null,
    postFailures: null,
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
    // The container list groups by host and those groups start collapsed.
    expandAllGroups();
    expect(screen.getByRole("link", { name: "postgres" })).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "Uptime" })).toBeNull();
  });

  // The container list groups BY host, so this column repeats its own group
  // header -- deliberately. A collapsible group's header scrolls away once it
  // is open, and a row thirteen containers deep into a host would otherwise
  // say nothing about the machine it runs on.
  it("names the host on every container row, not only in the group header", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(containersTab());
    expect(
      screen.getByRole("columnheader", { name: "Host" }),
    ).toBeInTheDocument();

    expandAllGroups();
    // Twice for the one container: the group heading, and the row own cell.
    expect(screen.getAllByRole("link", { name: "web-01" })).toHaveLength(2);
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

  it("counts what is wrong by kind, and drops the all-clear line", () => {
    renderPage({
      conditions: [
        {
          hostId: "1",
          hostname: "web-01",
          kind: "disk",
          severity: "critical",
          label: "Filesystem over 90%",
          what: "disk 99% full",
          since: "2026-08-10T13:00:00Z",
          evidence: { type: "meter", pct: 99 },
          tab: "filesystems",
        },
      ],
    });

    // The kind and its host count, not the host's own sentence: unfiltered,
    // this page is still the monitoring list and the sentence is one click
    // away. That IS the change -- fifty warned hosts cannot each get a line.
    const counts = screen.getByRole("list", { name: /by kind/i });
    expect(within(counts).getByText(/Filesystem over 90%/)).toBeInTheDocument();
    expect(within(counts).getByText("1")).toBeInTheDocument();
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

  function trafficTile(): HTMLElement {
    return screen.getByText("Fleet traffic").closest<HTMLElement>(".tile")!;
  }

  // Absent is not zero: a host that has reported no rate has an unknown
  // throughput, not a throughput of nothing.
  it("renders unknown fleet traffic as absent, never as zero", () => {
    renderPage({
      rows: [makeRow({ net_rx_bytes: null, net_tx_bytes: null })],
    });

    expect(within(trafficTile()).getByText(ABSENT)).toBeInTheDocument();
  });

  // rx_bytes/tx_bytes are BYTES per second, so bitrate() labelled this tile
  // "Mb/s" above rows reading "MB/s" -- off by eight and entirely plausible.
  // The third copy of that bug: #51 fixed the fleet row's traffic cell and the
  // host overview's card, and missed the tile sitting above both.
  it("states fleet traffic in bytes per second, like every row under it", () => {
    renderPage({
      rows: [makeRow({ net_rx_bytes: 1_000_000, net_tx_bytes: 1_000_000 })],
    });

    const tile = trafficTile();
    expect(within(tile).getByText(/2 MB\/s/)).toBeInTheDocument();
    expect(tile.textContent).not.toMatch(/Mb\/s/);
  });

  // The reported bug. This number is a RATE, and it used to be read off the
  // end of the fetched series -- so it changed with the range, because the
  // range picks the step and the step picks the storage tier: the raw
  // instantaneous rate at 1h, a five-minute average from a quarter of an hour
  // ago at 6h and 24h. The series here disagree with the gauges precisely so
  // that reading the wrong one fails.
  it("reads the gauge rather than the end of the sparkline series", () => {
    renderPage({
      rows: [
        makeRow({
          net_rx_bytes: 1_000_000,
          net_tx_bytes: 1_000_000,
          rx: [9_000_000],
          tx: [9_000_000],
        }),
      ],
    });

    expect(within(trafficTile()).getByText(/2 MB\/s/)).toBeInTheDocument();
  });

  // A gauge has no grid and no trailing bucket, so a host whose last scrape
  // landed mid-bucket cannot silently drop out of the total. Off the series
  // it did: the trailing-null check skipped it until its next post, and the
  // tile jittered between polls at a fixed range.
  it("counts a host whose series has a trailing gap", () => {
    renderPage({
      rows: [
        makeRow({
          net_rx_bytes: 1_000_000,
          net_tx_bytes: 1_000_000,
          rx: [1_000_000, null],
          tx: [1_000_000, null],
        }),
      ],
    });

    expect(within(trafficTile()).getByText(/2 MB\/s/)).toBeInTheDocument();
  });

  // The gauge does not go absent when an agent dies -- host_current keeps
  // the last pair it was written, and the upsert coalesces so a post with no
  // net samples cannot clear it. So a host that is not reporting is skipped
  // here, the same way the trailing-null check used to skip it: otherwise a
  // machine powered off a week ago keeps counting towards a tile that says
  // "right now", and the "Hosts reporting" tile directly above contradicts
  // it on the same screen.
  it("leaves a host that stopped reporting out of the fleet total", () => {
    renderPage({
      rows: [
        makeRow({ net_rx_bytes: 1_000_000, net_tx_bytes: 1_000_000 }),
        makeRow({
          id: 2,
          hostname: "old-01",
          last_seen: "2026-08-10T13:00:00Z",
          net_rx_bytes: 9_000_000,
          net_tx_bytes: 9_000_000,
        }),
      ],
    });

    expect(within(trafficTile()).getByText(/2 MB\/s/)).toBeInTheDocument();
  });

  // And a fleet where every host has gone quiet has an UNKNOWN throughput,
  // not one of zero: skipping the last host must reach the absent marker
  // rather than falling through to "0 b/s", which reads as a fleet that is
  // up and idle.
  it("renders an entirely offline fleet as absent, never as zero", () => {
    renderPage({
      rows: [
        makeRow({
          last_seen: "2026-08-10T13:00:00Z",
          net_rx_bytes: 9_000_000,
          net_tx_bytes: 9_000_000,
        }),
      ],
    });

    expect(within(trafficTile()).getByText(ABSENT)).toBeInTheDocument();
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
      net_rx_bytes: 1.5e6,
      net_tx_bytes: 5.5e5,
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
      net_rx_bytes: null,
      net_tx_bytes: null,
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
  it("derives what is wrong from the rows instead of claiming nothing is", () => {
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

    const counts = screen.getByRole("list", { name: /by kind/i });
    expect(within(counts).getByText(/OOM kills/)).toBeInTheDocument();
    expect(screen.queryByText(/nothing needs attention/)).toBeNull();
  });

  // The point of the counts line, in one assertion: the number of entries is
  // bounded by how many KINDS of thing can be wrong, never by how many hosts
  // are wrong. Twelve hosts with the same problem is one line -- which is
  // what the band could not do, and why fifty warnings buried it.
  it("stays one line per kind however many hosts carry it", () => {
    render(
      <FleetPage
        rows={Array.from({ length: 12 }, (_, i) =>
          makeRow({ id: i + 1, hostname: `web-${i + 1}`, oomKills: 1 }),
        )}
        checkedAt={null}
        now={NOW}
      />,
    );

    const counts = screen.getByRole("list", { name: /by kind/i });
    expect(within(counts).getAllByRole("listitem")).toHaveLength(1);
    expect(within(counts).getByText("12")).toBeInTheDocument();
  });

  // Clicking a kind is the whole navigation model: the hosts carrying it, and
  // nothing else, with the sentence that was too long to print twelve times.
  it("filters the list to the hosts carrying the kind that was clicked", async () => {
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

    const counts = screen.getByRole("list", { name: /by kind/i });
    await user.click(within(counts).getByRole("link", { name: /OOM kills/ }));

    expect(screen.getByText(/3 OOM kills/)).toBeInTheDocument();
    // The healthy host is gone, and the page says so rather than leaving the
    // reader to notice -- the band's own overflow line ("+30 more hosts") was
    // the counter-example, with nothing to click.
    expect(screen.queryByText("web-01")).toBeNull();
    expect(screen.getByRole("link", { name: /show all/i })).toBeInTheDocument();
  });

  // Severity is the coarse cut, and the two buckets have to add up to the
  // count stated above them or the control contradicts the line.
  it("splits the troubled hosts into critical and warning, and filters by either", async () => {
    const user = userEvent.setup();
    render(
      <FleetPage
        rows={[
          makeRow({ id: 1, hostname: "web-01" }),
          makeRow({ id: 2, hostname: "db-01", oomKills: 3 }),
          makeRow({
            id: 3,
            hostname: "build-01",
            services_failed: 2,
          }),
        ]}
        checkedAt={null}
        now={NOW}
      />,
    );

    expect(
      screen.getByRole("button", { name: /critical 1/i }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /warning 1/i }));

    expect(screen.getByText("build-01")).toBeInTheDocument();
    expect(screen.queryByText("db-01")).toBeNull();
  });

  // A host with nothing wrong is never in an attention view, whichever way
  // the reader got there.
  it("offers no severity control when the whole fleet is healthy", () => {
    render(
      <FleetPage rows={[makeRow({ id: 1 })]} checkedAt={null} now={NOW} />,
    );
    expect(screen.queryByRole("button", { name: /critical/i })).toBeNull();
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
  // The counts are computed over every row, never over the visible ones: a
  // filter is someone looking for one machine, and hiding a critical host
  // because its name does not match what was typed is exactly how an overview
  // lies.
  it("keeps the counts whole while the list is filtered", async () => {
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
    const counts = screen.getByRole("list", { name: /by kind/i });
    expect(within(counts).getByText(/OOM kills/)).toBeInTheDocument();
    expect(screen.getByText(/1 of 2 hosts/)).toBeInTheDocument();
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

// The gap between FleetPage and FleetContainers: `rows` arrives already
// filtered, so an empty one means either "the fleet has none" or "your search
// matched none". Only the page knows which, and the capability note must not
// answer the second -- filtering a fleet down to nothing would otherwise reply
// "no cgroup scopes ... re-run setup-agent.sh", turning a search term into an
// instruction to go and reconfigure a host.
describe("FleetPage, the container filter and the capability note", () => {
  const broken = makeRow({
    id: 2,
    hostname: "web-02",
    capabilities: { containers: "no-cgroup-scopes" },
  });

  it("explains a genuinely empty container list", async () => {
    const user = userEvent.setup();
    renderPage({ rows: [makeRow(), broken], containers: [] });
    await user.click(containersTab());

    expect(screen.getByText(/setup-agent\.sh/)).toBeInTheDocument();
  });

  // A filtered list that still has rows keeps the note: there it annotates
  // what is shown rather than standing in for it.
  it("keeps the note on a filtered list that still has rows", async () => {
    const user = userEvent.setup();
    renderPage({ rows: [makeRow(), broken] });
    await user.click(containersTab());
    await user.type(screen.getByPlaceholderText(/filter containers/i), "post");

    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(screen.getByText(/setup-agent\.sh/)).toBeInTheDocument();
  });

  it("does not blame a host for a search that matched nothing", async () => {
    const user = userEvent.setup();
    renderPage({ rows: [makeRow(), broken] });
    await user.click(containersTab());
    await user.type(screen.getByPlaceholderText(/filter containers/i), "zzz");

    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByText(/setup-agent\.sh/)).toBeNull();
    expect(screen.queryByText("No containers collected")).toBeNull();
  });
});
