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
import { hostColumns, type HostRow } from "./hostColumns";
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
    rx: [1e6, 2e6, 1.5e6],
    tx: [5e5, 6e5, 4e5],
    fullest: { mount: "/data", pct: 88, others: 2 },
    disk: [],
    ...overrides,
  };
}

describe("hostColumns", () => {
  // Uptime is gone: it is a fact about a host rather than a reading to scan
  // a fleet by -- the same number all day, where the row's job is what
  // changed. It still leads the host page's System card. Filesystem takes
  // its place beside Disk, because how full a disk is now and how fast it
  // got there are different questions and the meter only answers one.
  it("yields Host, CPU, Memory, Traffic, Filesystem, Disk in that exact order", () => {
    const cols = hostColumns("1h");
    expect(cols.map((c) => c.header)).toEqual([
      "Host",
      "CPU",
      "Memory",
      "Traffic",
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
  });

  describe("memory cell", () => {
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
    it("carries a status dot and a word, never colour alone", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const row = makeRow({ last_seen: "2026-08-10T13:59:30Z" });
      const { container } = render(<>{hostCol.cell(row)}</>);
      const badge = container.querySelector(".badge")!;
      expect(badge.querySelector(".dot")).toBeInTheDocument();
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
        cpu: [
          {
            name: "core 0",
            color: "var(--s1)",
            values: [10, null, 12, null, 11, null, 9, 10, 11, 12],
          },
        ],
      });
      const { container } = render(<>{hostCol.cell(row)}</>);
      expect(screen.getByText("sporadic")).toBeInTheDocument();
      expect(container.querySelector(".badge.st-crit")).not.toBeInTheDocument();
    });

    // Trailing nulls are every tier materialising behind now, not a fault:
    // the newest buckets are empty for every host on the page.
    it("does not call a clean host sporadic for the buckets no tier has yet", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
        cpu: [
          {
            name: "core 0",
            color: "var(--s1)",
            values: [10, 11, 12, 11, 10, 11, 12, null, null],
          },
        ],
      });
      render(<>{hostCol.cell(row)}</>);
      expect(screen.queryByText("sporadic")).toBeNull();
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
