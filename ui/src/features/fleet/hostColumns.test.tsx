// hostColumns() is the single source of truth for what a "host row" looks
// like: Task 12 (HostTable) and Task 13 (HostCards) both render the same
// Column<HostRow>[] this file produces, so a column added here cannot go
// missing from either renderer. These tests pin the contract they both
// depend on: column order, the disk cell's fullest-mount naming, and the
// sub-300s uptime severity -- see task-11-brief.md.
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { stackBands } from "../../ui/charts/geometry";
import { SPARK_WIDTH } from "../../ui/charts/size";
import { hostColumns, type AttentionView, type HostRow } from "./hostColumns";
import { hostConditions, groupByHost, type HostGroup } from "./conditions";
import { ABSENT } from "../../lib/format";

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
    cpu: [
      { name: "user", color: "var(--s1)", values: [10, 12, 11] },
      { name: "system", color: "var(--s2)", values: [5, 4, 6] },
      { name: "iowait", color: "var(--s3)", values: [1, 1, 2] },
      { name: "steal", color: "var(--s4)", values: [0, 0, 0] },
    ],
    mem: [
      { name: "used", color: "var(--s1)", values: [3e9, 3.5e9, 4e9] },
      { name: "buffers", color: "var(--s2)", values: [1e8, 1e8, 1e8] },
      { name: "cached", color: "var(--s3)", values: [5e8, 5e8, 5e8] },
      { name: "arc", color: "var(--s4)", values: [2e8, 2e8, 2e8] },
    ],
    reporting: [10, 12, 11],
    rx: [1e6, 2e6, 1.5e6],
    tx: [5e5, 6e5, 4e5],
    net_rx_bytes: 1.5e6,
    net_tx_bytes: 4e5,
    fullest: { mount: "/data", pct: 88, others: 2 },
    disk: [],
    oomKills: null,
    dropped: null,
    postFailures: null,
    ...overrides,
  };
}

describe("hostColumns", () => {
  // Uptime is gone: it is a fact about a host rather than a reading to scan
  // a fleet by -- the same number all day, where the row's job is what
  // changed. It still leads the host page's System card. Filesystem takes
  // its place beside Disk, because how full a disk is now and how fast it
  // got there are different questions and the meter only answers one.
  // Traffic sits second, right of the host it belongs to, rather than fourth
  // behind two other charts: it is the reading this list is most often
  // scanned for.
  it("yields Host, Traffic, CPU, Memory, Filesystem, Disk in that exact order", () => {
    const cols = hostColumns("1h");
    expect(cols.map((c) => c.header)).toEqual([
      "Host",
      "Traffic",
      "CPU",
      "Memory",
      "Filesystem",
      "Disk",
    ]);
  });

  it("consumes the range parameter (build fails silently otherwise via noUnusedParameters)", () => {
    // Both calls must succeed and be independent column arrays.
    expect(hostColumns("1h")).toHaveLength(6);
    expect(hostColumns("24h")).toHaveLength(6);
  });

  // The accessors are separate from the cell on purpose -- a cell is a
  // sparkline or a meter and has no order -- so they need their own check
  // that they read the same fact the cell shows.
  describe("sorting", () => {
    it("sorts hosts on the name shown, and disks on the percentage shown", () => {
      const cols = hostColumns("1h");
      const row = makeRow({
        hostname: "web-01",
        fullest: { mount: "/data", pct: 88, others: 2 },
      });

      const host = cols.find((c) => c.header === "Host")!;
      const disk = cols.find((c) => c.header === "Disk")!;
      expect(host.sortValue!(row)).toBe("web-01");
      // The percentage, not bytes: sorting on size would put the biggest
      // disk first rather than the one closest to filling up.
      expect(disk.sortValue!(row)).toBe(88);
    });

    // Unknown must not sort as zero, or a host whose filesystems were never
    // read would rank as the emptiest disk on the page.
    it("gives a host with no filesystems no disk sort value at all", () => {
      const disk = hostColumns("1h").find((c) => c.header === "Disk")!;

      expect(disk.sortValue!(makeRow({ fullest: null }))).toBeNull();
    });

    // A sparkline has no order. Offering a control that cannot sort is worse
    // than offering none.
    it("offers no ordering on the chart columns", () => {
      const cols = hostColumns("1h");

      for (const header of ["CPU", "Memory", "Traffic", "Filesystem"]) {
        expect(
          cols.find((c) => c.header === header)!.sortValue,
        ).toBeUndefined();
      }
    });
  });

  describe("host cell", () => {
    it("writes the site under the hostname", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const { container } = render(<>{col.cell(makeRow())}</>);

      expect(container.querySelector(".host-cell-site")!.textContent).toBe(
        "zurich-dc1",
      );
    });

    // An unassigned host is not a host with a missing site: it is one that
    // has not been put in a site yet, and an em dash under every such
    // hostname reads as a fleet full of holes.
    it("writes no site line at all when the host has no site", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const { container } = render(
        <>{col.cell(makeRow({ site_id: null, site_name: null }))}</>,
      );

      expect(container.querySelector(".host-cell-site")).toBeNull();
      expect(container.textContent).not.toContain(ABSENT);
    });
  });

  describe("filesystem cell", () => {
    // A host that reported no filesystems has no usage to draw. An empty
    // chart would claim it reported flat zero.
    it("renders the absent marker rather than an empty chart", () => {
      const col = hostColumns("1h").find((c) => c.header === "Filesystem")!;
      const { container } = render(<>{col.cell(makeRow({ disk: [] }))}</>);

      expect(container.querySelector("svg")).toBeNull();
      expect(container.textContent).toBe(ABSENT);
    });

    it("draws one line per filesystem", () => {
      const col = hostColumns("1h").find((c) => c.header === "Filesystem")!;
      const row = makeRow({
        disk: [
          { name: "root", color: "var(--s7)", values: [40, 41] },
          { name: "data", color: "var(--s1)", values: [88, 89] },
        ],
      });
      const { container } = render(<>{col.cell(row)}</>);

      expect(container.querySelectorAll("g[data-series]")).toHaveLength(2);
    });
  });

  describe("disk cell", () => {
    it("names the fullest mount and its +N count of other filesystems", () => {
      const cols = hostColumns("1h");
      const diskCol = cols.find((c) => c.header === "Disk")!;
      const row = makeRow({ fullest: { mount: "/data", pct: 88, others: 2 } });
      render(<>{diskCol.cell(row)}</>);
      expect(screen.getByText("/data +2")).toBeInTheDocument();
    });

    it("omits the +N suffix when there are no other filesystems", () => {
      const cols = hostColumns("1h");
      const diskCol = cols.find((c) => c.header === "Disk")!;
      const row = makeRow({ fullest: { mount: "/", pct: 40, others: 0 } });
      render(<>{diskCol.cell(row)}</>);
      expect(screen.getByText("/")).toBeInTheDocument();
      expect(screen.queryByText(/\+0/)).not.toBeInTheDocument();
    });
  });

  describe("cpu cell", () => {
    // Without a ceiling StackedSparkline auto-scales each host to its own
    // running total, so an idle host and a saturated one draw the identical
    // silhouette and the rows stop being comparable -- the one thing a fleet
    // list is for. Two rows an order of magnitude apart must not render the
    // same path data.
    it("scales every host's stack to 100, not to its own peak", () => {
      const cpuCol = hostColumns("1h").find((c) => c.header === "CPU")!;
      const idle = render(
        <>
          {cpuCol.cell(
            makeRow({
              cpu: [{ name: "user", color: "var(--s1)", values: [1, 2, 3] }],
            }),
          )}
        </>,
      );
      const idlePaths = idle.container.innerHTML;
      idle.unmount();

      const busy = render(
        <>
          {cpuCol.cell(
            makeRow({
              cpu: [{ name: "user", color: "var(--s1)", values: [30, 60, 90] }],
            }),
          )}
        </>,
      );

      expect(busy.container.innerHTML).not.toBe(idlePaths);
    });
  });

  describe("disk cell", () => {
    // A host that has reported no filesystems has no fullest one. The row
    // type used to forbid saying so, and the only expressible stand-in was
    // pct: 0 -- an empty, healthy, green bar where "never collected"
    // belongs, absent rendered as a fact.
    it("renders the absent marker, not an empty meter, when nothing was collected", () => {
      const diskCol = hostColumns("1h").find((c) => c.header === "Disk")!;
      const { container } = render(
        <>{diskCol.cell(makeRow({ fullest: null }))}</>,
      );

      expect(container.querySelector(".meter")).not.toBeInTheDocument();
      expect(container.textContent).toBe(ABSENT);
    });
  });

  describe("traffic cell", () => {
    it("renders rx and tx in identical typographic weight, distinguished only by direction", () => {
      const cols = hostColumns("1h");
      const trafficCol = cols.find((c) => c.header === "Traffic")!;
      const row = makeRow({ rx: [1e6, 2e6], tx: [5e5, 6e5] });
      const { container } = render(<>{trafficCol.cell(row)}</>);
      const rates = container.querySelectorAll(".rate");
      expect(rates).toHaveLength(2);
      const [rxEl, txEl] = Array.from(rates);
      // Same class, no per-element inline font styling -- neither rate is
      // asserted as more important than the other.
      expect(rxEl!.className).toBe(txEl!.className);
      expect(rxEl!.getAttribute("style")).toBeNull();
      expect(txEl!.getAttribute("style")).toBeNull();
      // The only distinguishing signal is the arrow, and each carries its
      // own accessible label so the direction survives without colour.
      expect(rxEl!.getAttribute("aria-label")).toMatch(/inbound/i);
      expect(txEl!.getAttribute("aria-label")).toMatch(/outbound/i);
    });

    // rx_bytes and tx_bytes are BYTES per second -- network.go divides a
    // byte delta by the elapsed seconds. Rendered through bitrate() they
    // read 8x low and entirely plausible: 1 MB/s showed as "1 Mb/s". The
    // host overview's Traffic card had the identical bug, so the two pages
    // agreed with each other and with nothing else.
    it("renders traffic in bytes per second, not bits", () => {
      const cols = hostColumns("1h");
      const trafficCol = cols.find((c) => c.header === "Traffic")!;
      // Reporting now: the cell blanks the rates of a host that has gone
      // quiet, and the row fixture's last_seen is fixed in the past.
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
        net_rx_bytes: 2e6,
        net_tx_bytes: 1e6,
      });
      const { container } = render(<>{trafficCol.cell(row)}</>);

      expect(container.textContent).toContain("2 MB/s");
      expect(container.textContent).toContain("1 MB/s");
      expect(container.textContent).not.toMatch(/b\/s/);
    });

    // The reported bug. The rates are host_current's gauges; only the
    // sparkline comes from the series. Read off the series they moved with
    // the RANGE -- the raw instantaneous rate at 1h, a five-minute average
    // from a quarter of an hour ago at 6h and wider -- so the number beside
    // a chart changed when the chart was widened. The two disagree here
    // precisely so that reading the wrong one fails.
    it("takes its rates from the gauge, not the end of the series", () => {
      const cols = hostColumns("1h");
      const trafficCol = cols.find((c) => c.header === "Traffic")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
        net_rx_bytes: 2e6,
        net_tx_bytes: 1e6,
        rx: [1e6, 9e6],
        tx: [5e5, 9e6],
      });
      const { container } = render(<>{trafficCol.cell(row)}</>);

      expect(container.textContent).toContain("2 MB/s");
      expect(container.textContent).toContain("1 MB/s");
      expect(container.textContent).not.toContain("9 MB/s");
    });

    // The gauge is the one number on this row that does not go absent on its
    // own when the agent dies: host_current keeps the last pair it was
    // written, and the upsert coalesces so a post carrying no net samples
    // cannot clear it either. Ungated, the cell drew a steady rate beside
    // this row's own "offline" badge -- "the agent is down" rendered as
    // "traffic is steady", which is exactly what the series version's
    // trailing-null check used to prevent.
    it("blanks the rates of a host that stopped reporting", () => {
      const cols = hostColumns("1h");
      const trafficCol = cols.find((c) => c.header === "Traffic")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 600_000).toISOString(),
        net_rx_bytes: 2e6,
        net_tx_bytes: 1e6,
      });
      const { container } = render(<>{trafficCol.cell(row)}</>);

      expect(container.textContent).not.toContain("MB/s");
      expect(container.textContent).toContain("—");
    });
  });

  describe("memory cell", () => {
    // Removing the sparkline legend was aimed at the 32-core CPU cell, where
    // a 32-entry list was taller than the row and thirty-two cores have no
    // identity a legend could carry anyway. It also stripped this cell,
    // where five separately-coloured bands were left carrying their identity
    // on colour alone -- which is the one thing a legend exists to prevent.
    it("carries no band legend, like every other sparkline in the row", () => {
      const cols = hostColumns("1h");
      const memCol = cols.find((c) => c.header === "Memory")!;
      const row = makeRow({
        mem_total: 16_000_000_000,
        mem: [
          { name: "used", color: "var(--s1)", values: [3e9, 3e9] },
          { name: "ARC", color: "var(--s7)", values: [2e8, 2e8] },
          { name: "buffers", color: "var(--s2)", values: [1e8, 1e8] },
          { name: "cached", color: "var(--s8)", values: [5e8, 5e8] },
          { name: "shared", color: "var(--s4)", values: [5e7, 5e7] },
        ],
      });

      const { container } = render(<>{memCol.cell(row)}</>);

      // The shape is the message in a dense list; naming five bands under a
      // 32px chart costs more row height than the names are worth, and the
      // host page's Memory panel is where the breakdown gets named. A
      // previous review turned this back on for the memory cell alone.
      expect(container.querySelector(".legend")).toBeNull();
      for (const name of ["used", "ARC", "buffers", "cached", "shared"]) {
        expect(container.textContent).not.toContain(name);
      }
    });

    // The caller that motivated removing it stays legend-free: thirty-two
    // hairlines cannot each own a hue, and the list was five times taller
    // than the chart it explained.
    it("leaves the per-core CPU cell without one", () => {
      const cols = hostColumns("1h");
      const cpuCol = cols.find((c) => c.header === "CPU")!;
      const row = makeRow({
        cpu: Array.from({ length: 32 }, (_, i) => ({
          name: `core ${i}`,
          color: `hsl(${i * 9} 60% 50%)`,
          values: [1, 2],
        })),
      });

      const { container } = render(<>{cpuCol.cell(row)}</>);

      expect(container.querySelector(".legend")).toBeNull();
    });

    it("scales the stack against mem_total, not the sum of its bands, so free is the gap to the top", () => {
      const cols = hostColumns("1h");
      const memCol = cols.find((c) => c.header === "Memory")!;
      const row = makeRow({
        mem_total: 16_000_000_000,
        mem: [
          { name: "used", color: "var(--s1)", values: [3e9, 3e9] },
          { name: "buffers", color: "var(--s2)", values: [1e8, 1e8] },
          { name: "cached", color: "var(--s3)", values: [5e8, 5e8] },
          { name: "arc", color: "var(--s4)", values: [2e8, 2e8] },
        ],
      });
      const { container } = render(<>{memCol.cell(row)}</>);
      // The host's total memory is marked, so "is this host nearly full" is
      // answerable from the chart instead of only from its shape.
      expect(
        container.querySelector("line[data-reference]"),
      ).toBeInTheDocument();
      const paths = Array.from(
        container.querySelectorAll("path[data-band]"),
      ).map((p) => p.getAttribute("d"));
      const expected = stackBands(
        row.mem.map((b) => b.values),
        // The shared sparkline width, not a literal: every list chart reads
        // it from one constant so a row's cells stay the same length.
        SPARK_WIDTH,
        32,
        // Scaled to mem_total plus the headroom the total's own dashed rule
        // needs to sit inside the plot rather than on its border. Free is
        // still the gap between the stack and that rule.
        (row.mem_total as number) * 1.08,
        2,
      );
      expect(paths).toEqual(expected);
    });

    it("renders the absent marker instead of a chart with an invented ceiling when mem_total is unknown", () => {
      const cols = hostColumns("1h");
      const memCol = cols.find((c) => c.header === "Memory")!;
      const row = makeRow({ mem_total: null });
      render(<>{memCol.cell(row)}</>);
      expect(screen.getByText("—")).toBeInTheDocument();
    });
  });

  describe("host cell", () => {
    it("carries a status word inside the chip, never colour alone", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const row = makeRow({ last_seen: "2026-08-10T13:59:30Z" });
      const { container } = render(<>{hostCol.cell(row)}</>);
      const badge = container.querySelector(".badge")!;
      expect(badge.className).toMatch(/st-/);
      expect(badge).toHaveTextContent(/./);
      expect(screen.getByText("web-01")).toBeInTheDocument();
    });

    // The staleness threshold mirrors the product's own definition of
    // "down" (design spec: no POST within 3x the 60s scrape interval,
    // i.e. 180s) rather than a separately-invented number -- these two
    // tests pin the boundary at that 180s line, not at some other value
    // a future edit might drift to.
    // Healthy is the majority state, so it carries no badge at all: a row
    // that says "online" down the whole page spends the eye's first stop on
    // the word that never changes. The absence of a badge IS the healthy
    // reading, and the boundary below is still pinned at 180s.
    it("says nothing at 179s since last_seen, just under 3x the scrape interval", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const lastSeen = new Date(Date.now() - 179_000).toISOString();
      const row = makeRow({ last_seen: lastSeen });
      const { container } = render(<>{hostCol.cell(row)}</>);
      expect(container.querySelector(".badge.st-crit")).not.toBeInTheDocument();
      expect(screen.queryByText("online")).toBeNull();
      expect(container.querySelector(".badge")).not.toBeInTheDocument();
    });

    // Answering now, but a fifth of the window missing. "online" and
    // "offline" are both wrong summaries of that host: one says it is fine,
    // the other says it is gone, and the interesting state is neither.
    it("marks a host that answers but keeps dropping scrapes as sporadic", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
        reporting: [10, null, 12, null, 11, null, 9, 10, 11, 12],
      });
      const { container } = render(<>{hostCol.cell(row)}</>);
      expect(screen.getByText("sporadic")).toBeInTheDocument();
      expect(container.querySelector(".badge.st-crit")).not.toBeInTheDocument();
    });

    // Leading nulls are the time before the host was reporting at all. A
    // host added minutes ago is one real bucket at the end of a whole
    // window's grid, and counting the emptiness in front of it badged every
    // newly added agent sporadic for most of its first day.
    it("does not call a just-added host sporadic for the time before it existed", () => {
      const cols = hostColumns("24h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
        reporting: [...Array<number | null>(283).fill(null), 12],
      });
      const { container } = render(<>{hostCol.cell(row)}</>);
      expect(screen.queryByText("sporadic")).toBeNull();
      expect(container.querySelector(".badge.st-crit")).not.toBeInTheDocument();
    });

    // Trailing nulls are every tier materialising behind now, not a fault:
    // the newest buckets are empty for every host on the page.
    it("does not call a clean host sporadic for the buckets no tier has yet", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
        reporting: [10, 11, 12, 11, 10, 11, 12, null, null],
      });
      render(<>{hostCol.cell(row)}</>);
      expect(screen.queryByText("sporadic")).toBeNull();
    });

    // The badge used to read row.cpu[0], which is a per-core band under 32
    // threads and the cpu_total fallback above it -- so one host was judged
    // against the cpu_core family and its neighbour against host_samples.
    // Two relations, two materialisation lags, one column claiming to mean
    // the same thing on every row.
    it("judges sporadic from the reporting series, never from the CPU bands", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      // A gappy per-core band beside a clean cpu_total series: the host is
      // reporting fine, and only the cpu_core tier lags.
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
        cpu: [
          {
            name: "core 0",
            color: "var(--s1)",
            values: [10, null, 12, null, 11, null, 9, null, 11, null],
          },
        ],
        reporting: [10, 11, 12, 11, 10, 11, 12, 11, 10, 11],
      });

      render(<>{hostCol.cell(row)}</>);

      expect(screen.queryByText("sporadic")).toBeNull();
    });

    // And the converse, so the test above cannot pass by the badge simply
    // never appearing.
    it("marks sporadic from the reporting series even when the CPU bands are clean", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
        cpu: [
          {
            name: "core 0",
            color: "var(--s1)",
            values: [10, 11, 12, 11, 10, 11, 12, 11, 10, 11],
          },
        ],
        reporting: [10, null, 12, null, 11, null, 9, 10, 11, 12],
      });

      render(<>{hostCol.cell(row)}</>);

      expect(screen.getByText("sporadic")).toBeInTheDocument();
    });

    it("reads offline at 181s since last_seen, just past 3x the scrape interval", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const lastSeen = new Date(Date.now() - 181_000).toISOString();
      const row = makeRow({ last_seen: lastSeen });
      const { container } = render(<>{hostCol.cell(row)}</>);
      expect(container.querySelector(".badge.st-crit")).toBeInTheDocument();
      expect(screen.getByText("offline")).toBeInTheDocument();
    });
  });
});

// The columns follow the question. Unfiltered this list is the monitoring
// table it has always been; filtered to what is wrong, CPU and memory
// sparklines beside a full filesystem say nothing about why the row is there
// and quietly suggest CPU is the problem.
describe("hostColumns, filtered to what is wrong", () => {
  const NOW = new Date("2026-08-16T14:00:00Z");

  // Reporting normally: makeRow's own last_seen is days old, which is a
  // condition in its own right -- "stopped reporting" would be the worst
  // thing about every row here and the disk would never get a look in.
  function troubled(overrides: Partial<HostRow> = {}): HostRow {
    return makeRow({
      last_seen: "2026-08-16T13:59:30Z",
      reporting: [10, 11, 12],
      ...overrides,
    });
  }

  function view(rows: HostRow[], kind: AttentionView["kind"]): AttentionView {
    const groups = groupByHost(rows.flatMap((row) => hostConditions(row, NOW)));
    return {
      groups: new Map<string, HostGroup>(groups.map((g) => [g.hostId, g])),
      kind,
      range: "1h",
    };
  }

  it("replaces the charts with the sentence, the evidence and the onset", () => {
    const row = troubled({
      services_failed: 2,
      failed_units: ["nginx.service", "certbot.timer"],
      failed_since: "2026-08-13T10:00:00Z",
    });
    const cols = hostColumns("1h", view([row], null));

    expect(cols.map((c) => c.header)).toEqual([
      "Host",
      "What is wrong",
      "Evidence",
      "Since",
      "",
    ]);
  });

  it("draws the mark that proves the row's own condition", () => {
    const row = troubled({
      services_failed: 2,
      failed_units: ["nginx.service", "certbot.timer"],
    });
    const cols = hostColumns("1h", view([row], "failed-units"));
    const evidence = cols.find((c) => c.header === "Evidence")!;

    render(<>{evidence.cell(row)}</>);
    expect(
      screen.getByText("nginx.service, certbot.timer"),
    ).toBeInTheDocument();
  });

  // The hub sends at most three names however many failed, and the count is
  // the authoritative number -- so the cell says how many it is not naming
  // rather than quietly showing three.
  it("counts the units it cannot name", () => {
    const row = troubled({
      services_failed: 5,
      failed_units: ["a.service", "b.service", "c.service"],
    });
    const cols = hostColumns("1h", view([row], "failed-units"));
    const evidence = cols.find((c) => c.header === "Evidence")!;

    render(<>{evidence.cell(row)}</>);
    expect(screen.getByText("+2")).toBeInTheDocument();
  });

  // A reader filtering to one kind is asking about that kind. Answering with
  // the host's worst condition instead would drop the row's subject on the
  // floor -- the row is in the list BECAUSE of the disk.
  it("speaks about the kind that was filtered to, not the host's worst", () => {
    const row = troubled({
      oomKills: 3,
      fullest: { mount: "/var", pct: 93, others: 0 },
    });
    const cols = hostColumns("1h", view([row], "disk"));
    const what = cols.find((c) => c.header === "What is wrong")!;

    render(<>{what.cell(row)}</>);
    expect(screen.getByText(/\/var is 93% full/)).toBeInTheDocument();
    // And it says the host has more wrong with it than the row is about.
    expect(screen.getByText(/\+1 more/)).toBeInTheDocument();
  });

  it("falls back to the worst condition when no kind was picked", () => {
    const row = troubled({
      oomKills: 3,
      fullest: { mount: "/var", pct: 93, others: 0 },
    });
    const cols = hostColumns("1h", view([row], null));
    const what = cols.find((c) => c.header === "What is wrong")!;

    render(<>{what.cell(row)}</>);
    expect(screen.getByText(/3 OOM kills/)).toBeInTheDocument();
  });

  // netra cannot see past the range the reader picked, so a disk that was
  // already full when the window opened gets a floor, not a moment.
  it("states a floor rather than a moment for a condition older than the window", () => {
    const row = troubled({
      fullest: {
        mount: "/var",
        pct: 93,
        others: 0,
        since: "2026-08-16T13:00:00Z",
        sinceAtLeast: true,
      },
    });
    const cols = hostColumns("1h", view([row], "disk"));
    const since = cols.find((c) => c.header === "Since")!;

    render(<>{since.cell(row)}</>);
    expect(screen.getByText(/over 1h/)).toBeInTheDocument();
  });

  // A counter delta over a window has no onset, and an empty column is the
  // honest answer -- not now(), and not the window start.
  it("leaves the onset empty when there is no honest one", () => {
    const row = troubled({ oomKills: 2 });
    const cols = hostColumns("1h", view([row], "oom"));
    const since = cols.find((c) => c.header === "Since")!;

    render(<>{since.cell(row)}</>);
    expect(screen.getByText(ABSENT)).toBeInTheDocument();
  });

  // The dot is the only severity mark on the row, so the word §3.3 requires
  // has to reach a screen reader some other way.
  it("spells the severity out for assistive tech beside the bare dot", () => {
    const row = troubled({ oomKills: 2 });
    const cols = hostColumns("1h", view([row], null));
    const host = cols.find((c) => c.header === "Host")!;

    const { container } = render(<>{host.cell(row)}</>);
    expect(screen.getByLabelText("Critical")).toBeInTheDocument();
    expect(container.querySelector(".dot.st-crit")).toBeInTheDocument();
    // The site is gone: the row is about a condition, and the location is
    // inventory the unfiltered list still carries.
    expect(screen.queryByText("zurich-dc1")).toBeNull();
  });

  it("links each row to the tab that answers its condition", () => {
    const row = troubled({ services_failed: 1, failed_units: ["a.service"] });
    const cols = hostColumns("1h", view([row], "failed-units"));
    const drill = cols[4]!;

    render(<>{drill.cell(row)}</>);
    const link = screen.getByRole("link", { name: /units on web-01/i });
    expect(link).toHaveAttribute("href", "/hosts/1/units");
  });

  // Nothing to say about a healthy host: it is never in this list, and a
  // stray one must render as absent rather than throw.
  it("renders the absent marker for a host with no conditions", () => {
    const row = troubled();
    const cols = hostColumns("1h", view([row], null));
    const what = cols.find((c) => c.header === "What is wrong")!;

    render(<>{what.cell(row)}</>);
    expect(screen.getByText(ABSENT)).toBeInTheDocument();
  });
});
