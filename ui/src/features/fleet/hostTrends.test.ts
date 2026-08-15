import { describe, expect, it, vi, beforeEach } from "vitest";
import { buildRows, fetchHostTrends, type HostTrends } from "./hostTrends";
import * as api from "../../lib/api";
import type { Host, MetricsResponse, Site } from "../../lib/api";

vi.mock("../../lib/api", async () => {
  const actual = await vi.importActual<typeof api>("../../lib/api");
  return { ...actual, getMetrics: vi.fn() };
});

const getMetrics = vi.mocked(api.getMetrics);

function response(over: Partial<MetricsResponse>): MetricsResponse {
  return {
    family: "host",
    tier: "raw",
    step_s: 3600,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T03:00:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T03:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: [],
    series: [],
    truncated: false,
    ...over,
  } as MetricsResponse;
}

const t0 = Date.parse("2026-08-10T00:00:00Z");
const hour = 3_600_000;
// The window's FINAL bucket. A filesystem's fullness is read there rather
// than from the last value the series happens to hold, so a fixture that
// means "this disk is 88 % full now" has to say it in the bucket that is now
// -- t0 is three hours of window away and means "it was, once".
const tNow = t0 + 2 * hour;

beforeEach(() => {
  vi.clearAllMocks();
});

/** Answers each family with whatever the test gives it. */
function serve(byFamily: Record<string, MetricsResponse | Error>) {
  getMetrics.mockImplementation(async (_id, params) => {
    const answer = byFamily[params.family];
    if (answer === undefined) return response({});
    if (answer instanceof Error) throw answer;
    return answer;
  });
}

describe("fetchHostTrends", () => {
  // The fleet CPU sparkline is a per-core stack, normalised so its top edge
  // is cpu_total. It is NOT the user/system/iowait/steal breakdown any more:
  // that answers where the time went rather than which core spent it, and it
  // has its own panel on the host page.
  it("stacks one band per core, normalised so the top is cpu_total", async () => {
    serve({
      cpu_core: response({
        family: "cpu_core",
        key_columns: ["core"],
        columns: ["busy"],
        series: [
          { key: { core: "0" }, points: [[t0, 80]] },
          { key: { core: "1" }, points: [[t0, 20]] },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h", undefined, 2);

    expect(trends.cpu.map((b) => b.name)).toEqual(["core 0", "core 1"]);
    expect(trends.cpu[0]!.values[0]).toBe(40);
  });

  // The read API has no aggregate-across-keys mode, so asking a 128-thread
  // host for its cores would ship 128 series per host per fleet render. Those
  // hosts get cpu_total, which the host family carries anyway.
  it("does not ask a very large host for one series per core", async () => {
    serve({
      host: response({
        columns: ["cpu_total"],
        series: [{ key: {}, points: [[t0, 30]] }],
      }),
    });

    const trends = await fetchHostTrends(1, "1h", undefined, 128);

    expect(getMetrics.mock.calls.map((c) => c[1].family)).not.toContain(
      "cpu_core",
    );
    expect(trends.cpu).toHaveLength(1);
    expect(trends.cpu[0]!.name).toBe("busy");
  });

  // A host whose thread count nobody knows is exactly the case the guard is
  // for: an unbounded fetch on a host of unknown size.
  it("does not ask for cores when the host size is unknown", async () => {
    serve({
      host: response({
        columns: ["cpu_total"],
        series: [{ key: {}, points: [[t0, 30]] }],
      }),
    });

    await fetchHostTrends(1, "1h", undefined, null);

    expect(getMetrics.mock.calls.map((c) => c[1].family)).not.toContain(
      "cpu_core",
    );
  });

  // The 5m and 1h rollups carry cpu_total and not the breakdown. One true
  // band beats four fabricated ones -- a breakdown cannot be recovered from
  // a total, and pretending otherwise is a chart that states something
  // nobody measured.
  it("falls back to one total band on a tier without the breakdown", async () => {
    serve({
      host: response({
        tier: "5m",
        columns: ["cpu_total_avg"],
        series: [{ key: {}, points: [[t0, 30]] }],
      }),
    });

    const trends = await fetchHostTrends(1, "24h");

    expect(trends.cpu).toHaveLength(1);
    expect(trends.cpu[0]!.name).toBe("busy");
  });

  // The memory stack is a partition of mem_total with used derived as the
  // remainder: mem_used cannot be the bottom band because it already
  // contains the ARC and the unreclaimable shmem pages, so stacking those on
  // top of it draws the same bytes twice. lib/bands.ts owns the arithmetic
  // and its own tests; this pins that the fleet row asks for it at all --
  // every band base here has to be a column name the schema really has, and
  // one that does not resolve is indistinguishable on screen from a host
  // that reported nothing.
  it("builds the memory partition rather than a single used band", async () => {
    serve({
      host: response({
        columns: [
          "mem_total",
          "mem_free",
          "mem_buffers",
          "mem_cached",
          "mem_shared",
          "mem_zfs_arc",
        ],
        series: [{ key: {}, points: [[t0, 1000, 200, 30, 100, 50, 100]] }],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.mem.map((b) => b.name)).toEqual([
      "used",
      "ARC",
      "buffers",
      "cached",
      "shared",
    ]);
    // The stack is mem_total minus free, never more: free is the gap to the
    // top rather than a band.
    const stack = trends.mem.reduce(
      (sum, b) => sum + (b.values[0] as number),
      0,
    );
    expect(stack).toBe(800);
  });

  // A host's traffic is the sum over its interfaces, and a null in any of
  // them makes the bucket's total unknowable rather than smaller. Counting
  // it as zero would draw a dip that never happened.
  it("sums traffic across interfaces, and keeps a gap a gap", async () => {
    serve({
      net: response({
        key_columns: ["iface"],
        columns: ["rx_bytes", "tx_bytes"],
        series: [
          {
            key: { iface: "eth0" },
            points: [
              [t0, 100, 10],
              [t0 + hour, 200, 20],
              [t0 + 2 * hour, null, 30],
            ],
          },
          {
            key: { iface: "wlan0" },
            points: [
              [t0, 5, 1],
              [t0 + hour, 5, 1],
              [t0 + 2 * hour, 5, 1],
            ],
          },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.rx).toEqual([105, 205, null]);
    expect(trends.tx).toEqual([11, 21, 31]);
  });

  // Every mount, not just the worst one: a root sitting flat at 40% while a
  // log volume climbs into trouble is exactly the case a single line for the
  // fullest filesystem hides.
  it("draws one line per filesystem, each on df's own percentage", async () => {
    serve({
      filesystem: response({
        key_columns: ["filesystem"],
        columns: ["used", "free", "total"],
        series: [
          { key: { filesystem: "root" }, points: [[t0, 68, 32, 110]] },
          { key: { filesystem: "data" }, points: [[t0, 88, 12, 110]] },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.disk.map((b) => b.name)).toEqual(["root", "data"]);
    expect(trends.disk[0]!.values[0]).toBe(68);
    expect(trends.disk[1]!.values[0]).toBe(88);
    // Each mount keeps its own hue, so a reader can follow one line across
    // the window rather than losing it where two cross.
    expect(trends.disk[0]!.color).not.toBe(trends.disk[1]!.color);
  });

  // A mount that reported nothing all window is not a flat line at zero: it
  // is a filesystem with no readings, and drawing one would claim otherwise.
  it("leaves out a filesystem that reported nothing", async () => {
    serve({
      filesystem: response({
        key_columns: ["filesystem"],
        columns: ["used", "free"],
        series: [
          { key: { filesystem: "root" }, points: [[t0, 68, 32]] },
          { key: { filesystem: "ghost" }, points: [[t0, null, null]] },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.disk.map((b) => b.name)).toEqual(["root"]);
  });

  // used / (used + free) is df's Use%, which is the number the operator has
  // already seen over SSH. used / total is not: total includes the root
  // reserve, so it reports a full disk as less full than df does.
  it("picks the fullest filesystem by df's own percentage", async () => {
    serve({
      filesystem: response({
        key_columns: ["filesystem"],
        columns: ["used", "free", "total"],
        series: [
          { key: { filesystem: "root" }, points: [[tNow, 68, 32, 110]] },
          { key: { filesystem: "data" }, points: [[tNow, 88, 12, 110]] },
          { key: { filesystem: "boot" }, points: [[tNow, 10, 90, 110]] },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.fullest).toEqual({ mount: "data", pct: 88, others: 2 });
  });

  // A filesystem is named to an operator by its mount point -- the thing they
  // would type into df -- and by its label only when there is no mount point
  // to use. The label is an identifier; on a containerised agent it is derived
  // from the marker file the agent measures through, not from the host.
  it("names a filesystem by its mount point, falling back to the label", async () => {
    serve({
      filesystem: response({
        key_columns: ["filesystem", "mountpoint"],
        columns: ["used", "free", "total"],
        series: [
          {
            key: { filesystem: "ark", mountpoint: "/mnt/ark" },
            points: [[tNow, 94, 6, 110]],
          },
          {
            key: { filesystem: "root", mountpoint: "" },
            points: [[tNow, 10, 90, 110]],
          },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.fullest?.mount).toBe("/mnt/ark");
    expect(trends.disk.map((b) => b.name)).toEqual(["/mnt/ark", "root"]);
  });

  // This cell reports the MAXIMUM, so a filesystem that stopped being
  // measured is not merely a stale row here -- it is one that WINS. The disk
  // frozen at 94 % the moment its agent was upgraded outranked every live
  // filesystem on the host, and the fleet said 94 % for a box whose real
  // disks were at 20 %, naming a mount nothing measures any more.
  //
  // The band still draws: a chart showing a series that ends is telling the
  // truth about it. It is the headline figure that must be about now.
  it("ignores a filesystem that stopped reporting when picking the fullest", async () => {
    serve({
      filesystem: response({
        key_columns: ["filesystem", "mountpoint"],
        columns: ["used", "free", "total"],
        series: [
          {
            // Retired: a reading in the first bucket and nothing since.
            key: { filesystem: "/netra/fs/ark", mountpoint: "" },
            points: [[t0, 94, 6, 110]],
          },
          {
            key: { filesystem: "ark", mountpoint: "/mnt/ark" },
            points: [[tNow, 20, 80, 110]],
          },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.fullest).toEqual({ mount: "/mnt/ark", pct: 20, others: 0 });
  });

  // Never a zero-percent meter: an empty green bar says the disks were
  // measured and are empty.
  it("reports no fullest filesystem rather than an empty meter", async () => {
    serve({ filesystem: response({ columns: [], series: [] }) });

    expect((await fetchHostTrends(1, "1h")).fullest).toBeNull();
  });

  // One family the hub cannot answer costs that column, not the row: a
  // failed filesystem call must still leave the CPU sparkline drawn.
  it("keeps the families that answered when one fails", async () => {
    serve({
      host: response({
        columns: ["cpu_total"],
        series: [{ key: {}, points: [[t0, 30]] }],
      }),
      filesystem: new Error("boom"),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.cpu).toHaveLength(1);
    expect(trends.fullest).toBeNull();
  });

  // The hub rejects relative times outright, and the fan-out is the one
  // place that could get this wrong for every host at once.
  it("asks for an absolute window and a step", async () => {
    serve({});

    await fetchHostTrends(7, "24h", new Date("2026-08-11T12:00:00Z"));

    for (const call of getMetrics.mock.calls) {
      expect(call[1].from).toBe("2026-08-10T12:00:00.000Z");
      expect(call[1].to).toBe("2026-08-11T12:00:00.000Z");
      expect(call[1].step).toBe("5m");
    }
  });
});

describe("buildRows", () => {
  const host: Host = {
    id: 1,
    hostname: "web-01",
    site_id: 3,
    last_seen: null,
    cpu_total: null,
    mem_used: null,
    mem_total: null,
    uptime_s: null,
    net_rx_bytes: null,
    net_tx_bytes: null,
    threads: null,
  };
  const site = { id: 3, name: "zrh1" } as Site;

  it("joins the site name from the one /sites call", () => {
    const rows = buildRows([host], [site], new Map());

    expect(rows[0]!.site_name).toBe("zrh1");
  });

  // A host whose trends have not arrived yet must render as gaps and absent
  // markers, never as zeroes: not fetched is not the same as measured zero.
  it("leaves a host without trends empty rather than at zero", () => {
    const rows = buildRows([host], [site], new Map());

    expect(rows[0]!.cpu).toEqual([]);
    expect(rows[0]!.rx).toEqual([]);
    expect(rows[0]!.fullest).toBeNull();
  });

  it("carries a host's trends onto its row", () => {
    const trends: HostTrends = {
      cpu: [{ name: "busy", color: "var(--s1)", values: [1, 2] }],
      mem: [],
      reporting: [1, 2],
      rx: [10],
      tx: [20],
      fullest: { mount: "/", pct: 50, others: 0 },
      disk: [],
      oomKills: 0,
    };

    const rows = buildRows([host], [site], new Map([[1, trends]]));

    expect(rows[0]!.cpu).toEqual(trends.cpu);
    expect(rows[0]!.fullest).toEqual(trends.fullest);
  });
});
