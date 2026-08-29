import { describe, expect, it } from "vitest";
import type { HostDetail, MetricsResponse } from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import { overviewTiles, type Tile } from "./overviewTiles";
import { DISK_WARN_PCT } from "../../fleet/conditions";

const t0 = Date.parse("2026-08-10T00:59:00Z");
const t1 = Date.parse("2026-08-10T01:00:00Z");
const now = new Date("2026-08-10T01:00:30Z");

const host = {
  id: 7,
  hostname: "kessel",
  last_seen: "2026-08-10T01:00:00Z",
  cpu_total: 12,
  mem_used: 4_000_000_000,
  mem_total: 8_000_000_000,
  uptime_s: 86_400,
  net_rx_bytes: 2_000_000,
  net_tx_bytes: 500_000,
  cores: 4,
  memory_total: 8_000_000_000,
  capabilities: {},
} as unknown as HostDetail;

function response(
  over: Partial<MetricsResponse> & { family: string },
): MetricsResponse {
  return {
    tier: "raw",
    step_s: 60,
    // The window must reach PAST the later point: griddedValues places a
    // series on the window's grid, and a point on the closing edge falls
    // outside it -- which reads as a one-bucket series rather than as the
    // pair every rate assertion here needs.
    window: { from: "2026-08-10T00:59:00Z", to: "2026-08-10T01:01:00Z" },
    requested_window: {
      from: "2026-08-10T00:59:00Z",
      to: "2026-08-10T01:01:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: [],
    series: [],
    truncated: false,
    ...over,
  } as MetricsResponse;
}

/** family=host with the columns a test names, two points so a rate has a
 * pair and a gridded series has a latest bucket. */
function hostMetrics(
  columns: string[],
  first: number[],
  second: number[] = first,
): MetricsResponse {
  return response({
    family: "host",
    columns,
    series: [
      {
        key: {},
        points: [
          [t0, ...first],
          [t1, ...second],
        ] as unknown as unknown[][],
      },
    ],
  });
}

function tiles(over: Partial<Parameters<typeof overviewTiles>[0]> = {}) {
  return overviewTiles({
    host,
    hostMetrics: null,
    filesystemMetrics: null,
    netMetrics: null,
    now,
    ...over,
  });
}

function find(group: Tile[], label: string): Tile | undefined {
  return group.find((tile) => tile.label === label);
}

describe("overviewTiles system group", () => {
  it("reads CPU off the latest bucket of cpu_total", () => {
    const { system } = tiles({
      hostMetrics: hostMetrics(["cpu_total"], [30], [37]),
    });

    expect(find(system, "CPU")?.value).toBe("37%");
  });

  // Nothing in this codebase judges a CPU percentage -- not conditions.ts,
  // not the fleet list -- so inventing a threshold here would make a host
  // read "warning" on its own page and clean in the row a reader arrived
  // from. A busy CPU is a fact; whether it is a problem is the attention
  // band's question.
  it("never puts a status hue on CPU, however busy the host is", () => {
    const { system } = tiles({
      hostMetrics: hostMetrics(["cpu_total"], [99], [99]),
    });

    expect(find(system, "CPU")?.severity).toBeNull();
  });

  it("says absent, never 0, for a tier that does not carry the column", () => {
    const { system } = tiles({ hostMetrics: hostMetrics(["load1"], [1]) });

    expect(find(system, "CPU")?.value).toBe(ABSENT);
  });

  // The same threshold the Memory meter carried on this page before the tile
  // replaced it: severityFromPercent against DEFAULT_THRESHOLDS. Nothing new
  // is being judged, the judgement moved from a bar to a tile.
  it("warns on memory at the meter's own threshold", () => {
    const { system } = tiles({
      hostMetrics: hostMetrics(["mem_used", "mem_total"], [75, 100], [75, 100]),
    });

    expect(find(system, "Memory")?.severity).toBe("warning");
  });

  it("leaves a healthy memory reading neutral rather than green", () => {
    const { system } = tiles({
      hostMetrics: hostMetrics(["mem_used", "mem_total"], [10, 100]),
    });

    expect(find(system, "Memory")?.severity).toBeNull();
  });

  // mem_total moves when a VM is resized. Dividing yesterday's usage by
  // today's capacity draws a step that never happened, so the percentage is
  // built bucket by bucket.
  it("builds the memory percentage against each bucket's own total", () => {
    const { system } = tiles({
      hostMetrics: hostMetrics(["mem_used", "mem_total"], [50, 100], [50, 200]),
    });

    expect(find(system, "Memory")?.values).toEqual([50, 25]);
  });
});

describe("overviewTiles swap", () => {
  // A host with no swap is not a host whose swap could not be read. A dash
  // where a figure goes says the second about a machine doing the first --
  // and three readings pinned at zero on every swapless VM in a fleet is how
  // a card teaches people to stop looking at it. The agent is what makes
  // this answerable: collector/memory.go writes swap_used NULL, not 0, when
  // the host has no swap.
  it("omits both swap tiles on a host that reports no swap", () => {
    const { pressure } = tiles({
      hostMetrics: hostMetrics(["pgmajfault_per_s"], [1]),
    });

    expect(find(pressure, "Swap used")).toBeUndefined();
    expect(find(pressure, "Swap out")).toBeUndefined();
    expect(find(pressure, "Major page faults")).toBeDefined();
  });

  it("shows swap that exists and is unused", () => {
    const { pressure } = tiles({
      hostMetrics: hostMetrics(["swap_used", "swap_total"], [0, 100]),
    });

    expect(find(pressure, "Swap used")?.value).toBe("0%");
  });

  it("warns on a full swap at the meter's own threshold", () => {
    const { pressure } = tiles({
      hostMetrics: hostMetrics(["swap_used", "swap_total"], [75, 100]),
    });

    expect(find(pressure, "Swap used")?.severity).toBe("warning");
  });

  // host_samples_5m carries swap_used_avg and NO swap_total, so above the raw
  // tier there is no denominator. A percentage built from the last total this
  // host ever reported would be a figure with no measurement behind it, so
  // the tile states the bytes instead -- and drops the threshold with the
  // percentage, because severity here is "how full".
  it("states bytes, not a percentage, at a tier with no swap_total", () => {
    const { pressure } = tiles({
      hostMetrics: hostMetrics(["swap_used"], [1024 ** 3]),
    });
    const tile = find(pressure, "Swap used");

    expect(tile?.value).toBe("1 GiB");
    expect(tile?.severity).toBeNull();
  });
});

describe("overviewTiles busiest filesystem", () => {
  function fsMetrics(rows: [string, number, number][]): MetricsResponse {
    return response({
      family: "filesystem",
      key_columns: ["mountpoint"],
      columns: ["total", "used", "free"],
      series: rows.map(([mountpoint, used, free]) => ({
        key: { mountpoint },
        points: [
          [t0, used + free, used, free],
          [t1, used + free, used, free],
        ] as unknown as unknown[][],
      })),
    });
  }

  it("picks the fullest filesystem and names it", () => {
    const { system } = tiles({
      filesystemMetrics: fsMetrics([
        ["/", 30, 70],
        ["/mnt/ark", 94, 6],
        ["/boot", 20, 80],
      ]),
    });
    const tile = find(system, "Busiest filesystem");

    expect(tile?.value).toBe("94%");
    expect(tile?.sub).toBe("/mnt/ark");
  });

  // Through diskState, so this tile, the Disk meters below it, the attention
  // band above it and the fleet row a reader arrived from cannot disagree
  // about one disk.
  it("warns on the fleet's own disk threshold", () => {
    const used = DISK_WARN_PCT;
    const { system } = tiles({
      filesystemMetrics: fsMetrics([["/mnt/ark", used, 100 - used]]),
    });

    expect(find(system, "Busiest filesystem")?.severity).toBe("warning");
  });

  // diskSeverityFor weighs the bytes left as well as the percentage: a 4 TB
  // disk at 91 % still has 360 GB free.
  it("stays quiet on a large disk with real headroom left", () => {
    const gib = 1024 ** 3;
    const { system } = tiles({
      filesystemMetrics: fsMetrics([["/mnt/ark", 3640 * gib, 360 * gib]]),
    });

    expect(find(system, "Busiest filesystem")?.severity).toBeNull();
  });

  it("says absent rather than 0% for a host reporting no filesystems", () => {
    const { system } = tiles();

    expect(find(system, "Busiest filesystem")?.value).toBe(ABSENT);
  });
});

describe("overviewTiles kernel and pressure groups", () => {
  it("reads the kernel rates off their own columns", () => {
    const { kernel } = tiles({
      hostMetrics: hostMetrics(
        ["ctxt_per_s", "intr_per_s", "procs_running", "procs_blocked"],
        [51000, 41000, 7, 0],
      ),
    });

    expect(find(kernel, "Context switches")?.value).toBe("51 000");
    expect(find(kernel, "Interrupts")?.value).toBe("41 000");
    expect(find(kernel, "Running processes")?.value).toBe("7");
    expect(find(kernel, "Running processes")?.sub).toBe("0 blocked");
  });

  // A rate has a "/s"; a level -- things that exist right now -- does not.
  it("marks a rate as per second and a level as neither", () => {
    const { kernel } = tiles({
      hostMetrics: hostMetrics(["ctxt_per_s", "procs_running"], [10, 3]),
    });

    expect(find(kernel, "Context switches")?.unit).toBe("/s");
    expect(find(kernel, "Running processes")?.unit).toBeUndefined();
  });

  // None of these carries a threshold: nothing measured a good value for a
  // context-switch rate, and a green tile would claim one.
  it("leaves every kernel and pressure reading neutral", () => {
    const { kernel, pressure } = tiles({
      hostMetrics: hostMetrics(
        ["ctxt_per_s", "intr_per_s", "pgmajfault_per_s", "pswpout_per_s"],
        [900000, 900000, 900000, 900000],
      ),
    });

    for (const tile of [...kernel, ...pressure]) {
      expect(tile.severity).toBeNull();
    }
  });

  // All three read together and all three open the same panel: swap-out
  // climbing with major faults flat is reclaim doing its job, both climbing
  // together is thrash.
  it("points the pressure rates at the one panel that draws them", () => {
    const { pressure } = tiles({
      hostMetrics: hostMetrics(
        ["pgmajfault_per_s", "pswpout_per_s", "swap_used"],
        [18, 2, 1000],
      ),
    });

    expect(
      pressure.filter((tile) => tile.unit === "/s").map((tile) => tile.slug),
    ).toEqual(["memory-pressure", "memory-pressure"]);
  });
});

describe("overviewTiles network group", () => {
  const netMetrics = response({
    family: "net",
    key_columns: ["iface"],
    columns: ["rx_bytes", "tx_bytes"],
    series: [
      {
        key: { iface: "eth0" },
        points: [
          [t0, 1_000_000, 250_000],
          [t1, 2_000_000, 500_000],
        ] as unknown as unknown[][],
      },
    ],
  });

  // net_rx and net_tx are BYTES per second. Rendered through bitrate() the
  // figure reads 8x low and entirely plausible, which is how the fleet cell
  // and this page stayed wrong together.
  it("states traffic in bytes per second, never bits", () => {
    const { network } = tiles({ netMetrics });

    expect(find(network, "Traffic in")?.value).toBe("2 MB/s");
    expect(find(network, "Traffic out")?.value).toBe("500 kB/s");
  });

  // host_current keeps a dead host's last pair for as long as the row
  // exists, so the figure gates on the host still reporting. "The agent is
  // down" must not render as "traffic is steady".
  it("blanks the rates of a host that stopped reporting", () => {
    const { network } = tiles({
      netMetrics,
      host: { ...host, last_seen: "2026-08-10T00:00:00Z" } as HostDetail,
    });

    expect(find(network, "Traffic in")?.value).toBe(ABSENT);
    expect(find(network, "Traffic out")?.value).toBe(ABSENT);
  });

  it("says in and out, never rx and tx", () => {
    const { network } = tiles({ netMetrics });

    expect(network.map((tile) => tile.label)).toContain("Traffic in");
    expect(network.map((tile) => tile.label)).not.toContain("Traffic rx");
  });
});
