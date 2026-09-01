import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Host } from "../../lib/api";
import { ABSENT } from "../../lib/format";
import type { HostRow } from "./hostColumns";
import type { ContainerRow } from "./FleetContainers";
import { FleetPage, buildHostRows } from "./FleetPage";

const NOW = new Date("2026-08-10T14:00:00Z");

function makeRow(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    window: null,
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
    rxPeak: [],
    txPeak: [],
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
    last_seen: "2026-08-10T14:00:00Z",
    host_id: 1,
    hostname: "web-01",
    ...overrides,
  };
}

/** A rail figure by the href it leads to -- its accessible name is the whole
 * figure ("22 containers"), which is not worth matching on. */
function tile(href: string): HTMLElement {
  return document.querySelector<HTMLElement>(`.srail a[href="${href}"]`)!;
}

/** One segment of the figure rail, found by the phrase that finishes it. */
function figure(label: RegExp): HTMLElement {
  return screen.getByText(label).closest<HTMLElement>(".s")!;
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
  it("swaps the list and the filter's placeholder when the entity changes", () => {
    const hosts = renderPage();

    expect(
      screen.getByRole("columnheader", { name: "Host" }),
    ).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/filter hosts/i)).toBeInTheDocument();

    hosts.unmount();
    renderPage({ entity: "containers" });

    expect(
      screen.getByPlaceholderText(/filter containers/i),
    ).toBeInTheDocument();
    // The container list groups by host, and those groups arrive OPEN.
    expect(screen.getByRole("link", { name: "postgres" })).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "Uptime" })).toBeNull();
  });

  // The host is named ONCE, by the group it heads. A Host column beside it
  // restated that heading on every row -- eighty-four rows saying what four
  // headings say, in the widest table on the page.
  // The rail marks Containers as its own destination; a heading fixed at
  // "Fleet" would contradict it, and mislabel the page for anyone landing
  // there by link.
  it("names the list it is showing", () => {
    const hosts = renderPage();
    expect(
      screen.getByRole("heading", { level: 1, name: "Fleet" }),
    ).toBeInTheDocument();
    hosts.unmount();

    renderPage({ entity: "containers" });
    expect(
      screen.getByRole("heading", { level: 1, name: "Containers" }),
    ).toBeInTheDocument();
  });

  it("names the host in the group header and not again on every row", () => {
    renderPage({ entity: "containers" });

    expect(screen.queryByRole("columnheader", { name: "Host" })).toBeNull();
    // Once for the one container: the group heading, and nothing else.
    expect(screen.getAllByRole("link", { name: "web-01" })).toHaveLength(1);
  });

  // The counts line reads whichever entity is on screen. Host conditions and
  // container states are different vocabularies over different rows, and one
  // line showing both would be counting two things in one row of chips.
  describe("the container counts line", () => {
    const SILENT = makeContainer({
      id: 2,
      container_key: "shop/web",
      name: "web",
      last_seen: "2026-08-10T12:00:00Z",
      host_last_seen: "2026-08-10T14:00:00Z",
    });

    it("counts container states, not host conditions", () => {
      renderPage({
        entity: "containers",
        containers: [
          makeContainer({ host_last_seen: "2026-08-10T14:00:00Z" }),
          SILENT,
        ],
      });

      const list = screen.getByRole("list", { name: /by kind/i });
      expect(within(list).getByText("Silent")).toBeInTheDocument();
      // The one that is fine is not a chip: a filter names what is wrong.
      expect(within(list).queryByText("Reporting")).toBeNull();
    });

    it("narrows the list to the state that was picked", () => {
      renderPage({
        entity: "containers",
        attention: "silent",
        onAttentionChange: vi.fn(),
        containers: [
          makeContainer({ host_last_seen: "2026-08-10T14:00:00Z" }),
          SILENT,
        ],
      });

      expect(screen.getByRole("link", { name: "web" })).toBeInTheDocument();
      expect(screen.queryByRole("link", { name: "postgres" })).toBeNull();
    });

    // The counts line drops a kind once nothing carries it, so a link to a
    // filter whose last container recovered would leave an empty list, no
    // chip, and no way out short of editing the URL.
    it("offers a way out of a filter nothing matches any more", () => {
      renderPage({
        entity: "containers",
        attention: "silent",
        onAttentionChange: vi.fn(),
        containers: [makeContainer({ host_last_seen: "2026-08-10T14:00:00Z" })],
      });

      expect(screen.queryByRole("list", { name: /by kind/i })).toBeNull();
      expect(
        screen.getByRole("link", { name: /show all/i }),
      ).toBeInTheDocument();
    });

    // A host kind in the URL while the container list is up would otherwise
    // narrow it to nothing and read as a broken page.
    it("ignores a host kind on the container tab", () => {
      renderPage({
        entity: "containers",
        attention: "failed-units",
        onAttentionChange: vi.fn(),
        containers: [
          makeContainer({ host_last_seen: "2026-08-10T14:00:00Z" }),
          SILENT,
        ],
      });

      expect(
        screen.getByRole("link", { name: "postgres" }),
      ).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "web" })).toBeInTheDocument();
    });
  });
});

describe("FleetPage toolbar", () => {
  // The card grid and its toggle are gone: the fleet is one rendering of one
  // window, so the toolbar carries the tabs, the filter and the attention
  // band and nothing that reshapes the page.
  it("offers neither a density toggle nor a range picker", () => {
    renderPage();

    expect(screen.queryByRole("button", { name: "Cards" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Table" })).toBeNull();
    expect(screen.queryByRole("button", { name: "24h" })).toBeNull();
    expect(screen.getByRole("table")).toBeInTheDocument();
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
  // The all-clear is the ABSENCE of the row, and nothing else. A line that
  // only ever reads "nothing needs attention" is a line people stop reading,
  // and it costs the page's best position on every healthy fleet -- the same
  // reason the band it replaced was never a green card. What confirms the
  // check ran is the rail's "since last check" figure, which is on the page
  // in both states.
  it("shows no attention row at all when nothing is wrong", () => {
    renderPage();

    expect(screen.queryByRole("list", { name: /by kind/i })).toBeNull();
  });

  it("counts what is wrong by kind", () => {
    renderPage({
      conditions: [
        {
          hostId: "1",
          hostname: "web-01",
          kind: "disk",
          severity: "critical",
          label: "Filesystem nearly full",
          what: "disk 99% full",
          since: "2026-08-10T13:00:00Z",
          evidence: { type: "meter", pct: 99 },
          tab: "storage",
        },
      ],
    });

    // The kind and its host count, not the host's own sentence: unfiltered,
    // this page is still the monitoring list and the sentence is one click
    // away. That IS the change -- fifty warned hosts cannot each get a line.
    const counts = screen.getByRole("list", { name: /by kind/i });
    expect(
      within(counts).getByText(/Filesystem nearly full/),
    ).toBeInTheDocument();
    expect(within(counts).getByText("1")).toBeInTheDocument();
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

    const reporting = figure(/hosts reporting/);
    expect(within(reporting).getByText("1")).toBeInTheDocument();
    expect(within(reporting).getByText(/of 3/)).toBeInTheDocument();
  });

  // The rail's phrase agrees with the all-clear sentence above it, which
  // already says "All 1 host reporting".
  it("says host, not hosts, for a fleet of one", () => {
    renderPage({ rows: [makeRow()] });

    expect(screen.getByText(/of 1 host reporting/)).toBeInTheDocument();
  });

  function trafficTile(): HTMLElement {
    return figure(/in \+ out/);
  }

  // Absent is not zero: a host that has reported no rate has an unknown
  // throughput, not a throughput of nothing.
  it("renders unknown fleet traffic as absent, never as zero", () => {
    renderPage({
      rows: [makeRow({ net_rx_bytes: null, net_tx_bytes: null })],
    });

    expect(within(trafficTile()).getByText(ABSENT)).toBeInTheDocument();
  });

  // rx_bytes/tx_bytes are BYTES per second, so bitrate() labelled this figure
  // "Mb/s" above rows reading "MB/s" -- off by eight and entirely plausible.
  // The third copy of that bug: #51 fixed the fleet row's traffic cell and the
  // host overview's card, and missed the figure sitting above both.
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
      last_seen: "2026-08-10T13:59:30Z",
      cpu_total: 42,
      mem_used: 4e9,
      mem_total: 16e9,
      uptime_s: 100,
      net_rx_bytes: 1.5e6,
      net_tx_bytes: 5.5e5,
      threads: null,
      location: "Roubaix, France",
      provider: "OVH",
    },
    {
      id: 2,
      hostname: "db-01",
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
  // Where a host is arrives ON the host, reported by its own agent, so there
  // is nothing to join. This used to fetch the sites table whole and resolve
  // a name per row by id, and then the providers table on top of it.
  it("carries the location the host reported, with nothing to join", () => {
    const rows = buildHostRows(hosts);

    expect(rows[0]!.location).toBe("Roubaix, France");
    expect(rows[0]!.provider).toBe("OVH");
    expect(rows[1]!.location).toBeUndefined();
  });

  it("leaves every unfetched series empty rather than inventing a zero", () => {
    const rows = buildHostRows(hosts);

    expect(rows[0]!.cpu).toEqual([]);
    expect(rows[0]!.rx).toEqual([]);
    // null, not a zero percentage: nothing measured this host's disks, and
    // an empty green meter would say they are empty.
    expect(rows[0]!.fullest).toBeNull();
  });
});

describe("FleetPage data fetching", () => {
  // The location arrives with the host list. It used to take a second
  // whole-table read of /sites to resolve a name by id, and then a third of
  // /providers on top of that -- all to answer a question every agent had
  // been answering on every metadata post.
  it("draws the location from the host list alone, asking for nothing else", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const body = url.includes("/containers")
        ? []
        : [
            {
              id: 1,
              hostname: "web-01",
              last_seen: "2026-08-10T13:59:30Z",
              cpu_total: 1,
              mem_used: null,
              mem_total: null,
              uptime_s: 10,
              threads: null,
              location: "Roubaix, France",
              provider: "OVH",
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
    expect(screen.getByText("OVH \u00b7 Roubaix, France")).toBeInTheDocument();

    const urls = fetchMock.mock.calls.map(([u]) => String(u));
    expect(urls.filter((u) => u.endsWith("/api/v1/sites"))).toHaveLength(0);
    expect(urls.filter((u) => u.endsWith("/api/v1/providers"))).toHaveLength(0);
    // And still never one detail call per host, which is what the site join
    // was avoiding in the first place.
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

  it("dates the rail's check figure from injected data too", () => {
    renderPage({ checkedAt: "2026-08-10T13:59:20Z" });

    // The age alone, with the words that finish it as its label: the rail
    // states figures, and "40 s ago since last check" is not a sentence.
    expect(screen.getByText("40 s")).toBeInTheDocument();
    expect(screen.getByText("since last check")).toBeInTheDocument();
  });

  // ABSENT under "since last check" reads as "the check failed", which is a
  // claim the page has no basis for: a hub that did not say when it last
  // looked has not said anything went wrong either.
  it("omits the check figure entirely rather than dashing it", () => {
    renderPage({ checkedAt: null });

    expect(screen.queryByText("since last check")).toBeNull();
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
  // nothing else -- in the same columns the unfiltered list uses.
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

    expect(screen.getByText("db-01")).toBeInTheDocument();
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

  // Onboarding copy in front of a hundred hosts is the page contradicting
  // itself. Reachable by opening a shared "/?attn=oom" link after that kind
  // cleared -- and by the text filter, which could always do it.
  it("says the filter is hiding the fleet, not that the fleet is empty", () => {
    render(
      <FleetPage
        rows={[makeRow({ id: 1, hostname: "web-01", oomKills: 3 })]}
        attention="oom"
        onAttentionChange={() => {}}
        conditions={[]}
        checkedAt={null}
        now={NOW}
      />,
    );

    expect(screen.getByText(/no hosts match/i)).toBeInTheDocument();
    expect(screen.queryByText(/no hosts yet/i)).toBeNull();
  });

  it("keeps the onboarding state for a hub with no hosts at all", () => {
    render(<FleetPage rows={[]} checkedAt={null} now={NOW} />);

    expect(screen.getByText(/no hosts yet/i)).toBeInTheDocument();
  });

  // The label comes from the static kind table, not from the conditions on
  // screen: the last host carrying a kind can recover between a link being
  // sent and being opened, and "Showing 0 of 1 hosts with · show all" is a
  // sentence with its noun missing.
  it("still names the kind it is filtering to after that kind clears", () => {
    render(
      <FleetPage
        rows={[makeRow({ id: 1, hostname: "web-01" })]}
        attention="oom"
        onAttentionChange={() => {}}
        conditions={[]}
        checkedAt={null}
        now={NOW}
      />,
    );

    expect(screen.getByText(/with oom kills/i)).toBeInTheDocument();
  });

  // A cmd-click never reaches onAttentionChange -- it follows the href -- so
  // an href naming only the filter would drop the entity the reader is on.
  it("lets the page decide where a filter link points", () => {
    render(
      <FleetPage
        rows={[makeRow({ id: 1, hostname: "web-01", oomKills: 3 })]}
        attentionHref={(next) =>
          next === "all"
            ? "/?entity=containers"
            : `/?entity=containers&attn=${next}`
        }
        checkedAt={null}
        now={NOW}
      />,
    );

    expect(screen.getByRole("link", { name: /OOM kills/ })).toHaveAttribute(
      "href",
      "/?entity=containers&attn=oom",
    );
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

    // One host in trouble, two conditions on it -- so the segment says 1,
    // not 2. The segments count HOSTS; the kind list beside them is what
    // counts the conditions, and it has a line for each.
    expect(
      screen.getByRole("button", { name: "Critical 1" }),
    ).toBeInTheDocument();
    const counts = screen.getByRole("list", { name: /by kind/i });
    expect(within(counts).getAllByRole("listitem")).toHaveLength(2);
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
    // db-01 is filtered out of the list; its OOM kills are still counted,
    // and the segment still knows the fleet has two hosts in it.
    expect(within(counts).getByText(/OOM kills/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "All 2" })).toBeInTheDocument();
  });

  // The rail replaces the worst-first ordering: rows stay where the API put
  // them, and the mark says which of them to look at.
  it("rails a troubled row and leaves a healthy one unmarked", () => {
    const { container } = render(
      <FleetPage
        rows={[
          makeRow({ id: 1, hostname: "web-01" }),
          makeRow({ id: 2, hostname: "db-01", oomKills: 3 }),
        ]}
        checkedAt={null}
        now={NOW}
      />,
    );

    const rows = container.querySelectorAll("tbody tr");
    expect(rows[0].className).toBe("");
    expect(rows[1].className).toContain("rail-critical");
    // The colour is never the only channel: the word rides the row for a
    // reader who gets no colour, and a healthy row says nothing.
    expect(rows[1].querySelector(".sr-only")?.textContent).toBe("Critical");
    expect(rows[0].querySelector(".sr-only")).toBeNull();
  });

  it("draws no attention row for a healthy fleet", () => {
    render(
      <FleetPage rows={[makeRow({ id: 1 })]} checkedAt={null} now={NOW} />,
    );
    expect(screen.queryByRole("list", { name: /by kind/i })).toBeNull();
  });
});

describe("FleetPage stat figures as controls", () => {
  it("switches to the containers list when the Containers figure is clicked", async () => {
    const user = userEvent.setup();
    renderPage();

    expect(screen.getByPlaceholderText(/filter hosts/i)).toBeInTheDocument();

    await user.click(tile("/?entity=containers"));

    expect(
      screen.getByPlaceholderText(/filter containers/i),
    ).toBeInTheDocument();
  });

  it("returns to the hosts list from the hosts-reporting figure", async () => {
    const user = userEvent.setup();
    renderPage({ entity: "containers" });

    await user.click(tile("/"));

    expect(screen.getByPlaceholderText(/filter hosts/i)).toBeInTheDocument();
  });

  // Real links, so middle-click, cmd-click and bookmarking work without this
  // component reimplementing any of them. The hrefs match the tabs' own.
  it("gives the navigable figures the same hrefs as the tabs", () => {
    renderPage();

    expect(tile("/")).not.toBeNull();
    expect(tile("/?entity=containers")).not.toBeNull();
  });

  // Fleet traffic is a rate, not a set: there is no list of it to go to, and
  // a tile that looks clickable and does nothing is worse than an inert one.
  it("leaves the fleet traffic figure inert", () => {
    renderPage();

    expect(screen.getByText(/in \+ out/).closest("a")).toBeNull();
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
    renderPage({
      entity: "containers",
      rows: [makeRow(), broken],
      containers: [],
    });

    expect(screen.getByText(/setup-agent\.sh/)).toBeInTheDocument();
  });

  // A filtered list that still has rows keeps the note: there it annotates
  // what is shown rather than standing in for it.
  it("keeps the note on a filtered list that still has rows", async () => {
    const user = userEvent.setup();
    renderPage({ entity: "containers", rows: [makeRow(), broken] });
    await user.type(screen.getByPlaceholderText(/filter containers/i), "post");

    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(screen.getByText(/setup-agent\.sh/)).toBeInTheDocument();
  });

  it("does not blame a host for a search that matched nothing", async () => {
    const user = userEvent.setup();
    renderPage({ entity: "containers", rows: [makeRow(), broken] });
    await user.type(screen.getByPlaceholderText(/filter containers/i), "zzz");

    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByText(/setup-agent\.sh/)).toBeNull();
    expect(screen.queryByText("No containers collected")).toBeNull();
  });
});
