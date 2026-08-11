// hostColumns() is the single source of truth for what a "host row" looks
// like: Task 12 (HostTable) and Task 13 (HostCards) both render the same
// Column<HostRow>[] this file produces, so a column added here cannot go
// missing from either renderer. These tests pin the contract they both
// depend on: column order, the disk cell's fullest-mount naming, and the
// sub-300s uptime severity -- see task-11-brief.md.
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { stackBands } from "../../ui/charts/geometry";
import { hostColumns, type HostRow } from "./hostColumns";

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
    ...overrides,
  };
}

describe("hostColumns", () => {
  it("yields Host, CPU, Memory, Traffic, Disk, Uptime in that exact order", () => {
    const cols = hostColumns("1h");
    expect(cols.map((c) => c.header)).toEqual([
      "Host",
      "CPU",
      "Memory",
      "Traffic",
      "Disk",
      "Uptime",
    ]);
  });

  it("consumes the range parameter (build fails silently otherwise via noUnusedParameters)", () => {
    // Both calls must succeed and be independent column arrays.
    expect(hostColumns("1h")).toHaveLength(6);
    expect(hostColumns("24h")).toHaveLength(6);
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

  describe("uptime cell", () => {
    it("carries the warning severity, as a badge with a dot and a word, when uptime is under 300s", () => {
      const cols = hostColumns("1h");
      const uptimeCol = cols.find((c) => c.header === "Uptime")!;
      const row = makeRow({ uptime_s: 240 });
      const { container } = render(<>{uptimeCol.cell(row)}</>);
      const badge = container.querySelector(".badge");
      expect(badge).toHaveClass("st-warn");
      expect(badge?.querySelector(".dot")).toBeInTheDocument();
      expect(badge).toHaveTextContent(/./); // a word, not just a dot
    });

    it("does not warn at exactly 300s", () => {
      const cols = hostColumns("1h");
      const uptimeCol = cols.find((c) => c.header === "Uptime")!;
      const row = makeRow({ uptime_s: 300 });
      const { container } = render(<>{uptimeCol.cell(row)}</>);
      expect(container.querySelector(".badge.st-warn")).not.toBeInTheDocument();
    });

    it("warns one second under the 300s boundary", () => {
      const cols = hostColumns("1h");
      const uptimeCol = cols.find((c) => c.header === "Uptime")!;
      const row = makeRow({ uptime_s: 299 });
      const { container } = render(<>{uptimeCol.cell(row)}</>);
      expect(container.querySelector(".badge.st-warn")).toBeInTheDocument();
    });

    it("renders the absent marker with no badge when uptime is unknown", () => {
      const cols = hostColumns("1h");
      const uptimeCol = cols.find((c) => c.header === "Uptime")!;
      const row = makeRow({ uptime_s: null });
      const { container } = render(<>{uptimeCol.cell(row)}</>);
      expect(container.querySelector(".badge")).not.toBeInTheDocument();
      expect(screen.getByText("—")).toBeInTheDocument();
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
      const paths = Array.from(
        container.querySelectorAll("path[data-band]"),
      ).map((p) => p.getAttribute("d"));
      const expected = stackBands(
        row.mem.map((b) => b.values),
        120,
        32,
        row.mem_total as number,
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
    it("still reads online at 179s since last_seen, just under 3x the scrape interval", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const lastSeen = new Date(Date.now() - 179_000).toISOString();
      const row = makeRow({ last_seen: lastSeen });
      const { container } = render(<>{hostCol.cell(row)}</>);
      expect(container.querySelector(".badge.st-crit")).not.toBeInTheDocument();
      expect(screen.getByText("online")).toBeInTheDocument();
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
