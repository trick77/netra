import { describe, expect, it, vi, beforeEach } from "vitest";
import {
  buildRows,
  fetchFleetTrends,
  fetchHostTrends,
  trafficDetailSeries,
  trafficSeries,
  type HostTrends,
} from "./hostTrends";
import * as api from "../../lib/api";
import type { Host, MetricsResponse, Site } from "../../lib/api";

vi.mock("../../lib/api", async () => {
  const actual = await vi.importActual<typeof api>("../../lib/api");
  return { ...actual, getMetrics: vi.fn(), getFleetMetrics: vi.fn() };
});

const getMetrics = vi.mocked(api.getMetrics);
const getFleetMetrics = vi.mocked(api.getFleetMetrics);

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

  // The `agent` family is the one fetch here that draws nothing: it exists
  // for the two delivery counters the attention band reports. They are read
  // DIFFERENTLY, and the difference is the whole point -- post_failures_total
  // as the window's increase, buffer_dropped_total as the agent's running
  // total.
  it("reads failed deliveries as the window's increase", async () => {
    serve({
      agent: response({
        family: "agent",
        columns: ["buffer_dropped_total", "post_failures_total"],
        series: [
          {
            key: {},
            points: [
              [t0, 0, 7],
              [t0 + hour, 0, 9],
              [tNow, 0, 9],
            ],
          },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(getMetrics.mock.calls.map((c) => c[1].family)).toContain("agent");
    expect(trends.postFailures).toBe(2);
  });

  // The case the first version of this got wrong, and could not have caught
  // with a contiguous fixture. The ring only evicts once full, so the counter
  // cannot move until the hub has been unreachable for a whole BufferWindow
  // -- which puts a hole of exactly that length in this series, immediately
  // before the samples that report the drop. Read as an increase, the jump
  // across the hole is discarded and the flat runs sum to 0: silent for the
  // one host it exists to catch.
  it("still sees dropped samples across the outage that caused them", async () => {
    serve({
      agent: response({
        family: "agent",
        // Six hourly buckets, so the series has a run either side of the
        // hole rather than only its two ends. That is what makes this the
        // real failure: counterDeltas scores the flat runs 0 and refuses the
        // pair spanning the gap, so the increase is 0 -- a confident "nothing
        // happened" -- rather than the null it would return with no usable
        // pair anywhere.
        window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T06:00:00Z" },
        requested_window: {
          from: "2026-08-10T00:00:00Z",
          to: "2026-08-10T06:00:00Z",
        },
        columns: ["buffer_dropped_total", "post_failures_total"],
        series: [
          // Two hours of quiet at 100, two hours of nothing at all while the
          // hub is away, then the agent returns carrying 112 losses.
          {
            key: {},
            points: [
              [t0, 100, 0],
              [t0 + hour, 100, 0],
              [t0 + 4 * hour, 112, 0],
              [t0 + 5 * hour, 112, 0],
            ],
          },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.dropped).toBe(112);
  });

  // One family the hub cannot answer costs that family, not the row -- and
  // "cannot say" is null rather than 0, so a fleet page never reports an
  // all-clear it did not hear.
  it("says nothing about delivery when the agent family fails", async () => {
    serve({ agent: new Error("500") });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.dropped).toBeNull();
    expect(trends.postFailures).toBeNull();
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

    // Bottom to top, least reclaimable first: shared is Shmem and cannot be
    // dropped under pressure, so it stacks with used rather than above the
    // caches.
    expect(trends.mem.map((b) => b.name)).toEqual([
      "used",
      "shared",
      "ARC",
      "buffers",
      "cached",
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

    expect(trends.fullest).toEqual({
      mount: "data",
      pct: 88,
      // Carried through beside the percentage: the condition rule needs the
      // bytes, not only the ratio.
      free: 12,
      others: 2,
      // Under DISK_WARN_PCT, so there is no crossing to date.
      since: null,
      sinceAtLeast: false,
    });
  });

  // Highest percentage is the wrong pick once a percentage no longer decides
  // anything on its own: the array is fuller but has 674 GB left, the root is
  // a hair behind it and nearly out. Naming the array would leave the row
  // with nothing to say while the disk that is actually filling sat behind a
  // "+1".
  it("names the mount worth acting on, not the biggest percentage", async () => {
    const GB = 1024 ** 3;
    serve({
      filesystem: response({
        key_columns: ["filesystem", "mountpoint"],
        columns: ["used", "free", "total"],
        series: [
          {
            key: { filesystem: "ark", mountpoint: "/mnt/ark" },
            points: [[tNow, 6126 * GB, 674 * GB, 6800 * GB]],
          },
          {
            key: { filesystem: "root", mountpoint: "/" },
            points: [[tNow, 18 * GB, 2 * GB, 21 * GB]],
          },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.fullest?.mount).toBe("/");
    expect(trends.fullest?.others).toBe(1);
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

    expect(trends.fullest).toEqual({
      mount: "/mnt/ark",
      pct: 20,
      free: 80,
      others: 0,
      since: null,
      sinceAtLeast: false,
    });
  });

  // The onset is dated from when the mount became worth reading about, which
  // on a big array is not when it crossed 90%. This one is over 90% for the
  // whole window and only runs short of bytes in the last bucket.
  it("dates the onset from the bytes, not from the percentage", async () => {
    const GB = 1024 ** 3;
    serve({
      filesystem: response({
        key_columns: ["filesystem"],
        columns: ["used", "free", "total"],
        series: [
          {
            key: { filesystem: "ark" },
            points: [
              // 91%, and 600 GB still left: nothing to say.
              [t0, 6060 * GB, 600 * GB, 6800 * GB],
              [t0 + hour, 6260 * GB, 400 * GB, 6800 * GB],
              // 99%, and now under the 100 GiB floor.
              [tNow, 6610 * GB, 50 * GB, 6800 * GB],
            ],
          },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.fullest?.sinceAtLeast).toBe(false);
    expect(trends.fullest?.since).toBe("2026-08-10T02:00:00.000Z");
  });

  // The onset walk, and the case that made it lie. A gap at the start of the
  // window is not the moment a disk filled up: the walk steps over empty
  // buckets, so an agent that restarted at the window edge left it stopping
  // at bucket 1 with nothing under the threshold behind it, and the row
  // printed a precise timestamp for a disk that was full the whole time netra
  // can see.
  it("states a floor when the disk was over the line for the whole window", async () => {
    serve({
      filesystem: response({
        key_columns: ["filesystem"],
        columns: ["used", "free", "total"],
        series: [
          {
            key: { filesystem: "root" },
            // Nothing in the first bucket -- the agent was restarting -- and
            // over 90% in both buckets after it.
            points: [
              [t0 + hour, 95, 5, 110],
              [tNow, 96, 4, 110],
            ],
          },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.fullest?.sinceAtLeast).toBe(true);
    expect(trends.fullest?.since).toBe("2026-08-10T00:00:00Z");
  });

  // The other half of the same rule: a reading BELOW the threshold inside the
  // window is a real crossing and gets a real timestamp.
  it("dates the crossing when the disk was under the line earlier in the window", async () => {
    serve({
      filesystem: response({
        key_columns: ["filesystem"],
        columns: ["used", "free", "total"],
        series: [
          {
            key: { filesystem: "root" },
            points: [
              [t0, 40, 60, 110],
              [t0 + hour, 95, 5, 110],
              [tNow, 96, 4, 110],
            ],
          },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.fullest?.sinceAtLeast).toBe(false);
    // Milliseconds because this one is computed from the grid rather than
    // echoed from the window string -- both parse to the same instant, which
    // is all `relative()` reads.
    expect(trends.fullest?.since).toBe("2026-08-10T01:00:00.000Z");
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

// The fleet form. This is what the page actually calls now: it used to spend
// six requests per host per render -- four families, five on a host small
// enough for a per-core stack, plus containers -- and re-send the lot on every
// poll and every range toggle. One request per family answers all of them.
describe("fetchFleetTrends", () => {
  /** Answers each family with the same response for every host asked about. */
  function serveFleet(byFamily: Record<string, MetricsResponse | Error>) {
    getFleetMetrics.mockImplementation(async (ids, params) => {
      const answer = byFamily[params.family];
      if (answer instanceof Error) throw answer;
      const res = answer ?? response({});
      return new Map(ids.map((id) => [Number(id), res]));
    });
  }

  it("asks each family once for the whole fleet", async () => {
    serveFleet({});

    await fetchFleetTrends(
      [
        { id: 1, threads: 4 },
        { id: 2, threads: 8 },
      ],
      "24h",
      new Date("2026-08-11T12:00:00Z"),
    );

    // Five families, five calls -- not five per host.
    const families = getFleetMetrics.mock.calls.map((c) => c[1].family).sort();
    expect(families).toEqual([
      "agent",
      "cpu_core",
      "filesystem",
      "host",
      "net",
    ]);
    for (const call of getFleetMetrics.mock.calls) {
      expect(call[0]).toEqual([1, 2]);
      expect(call[1].from).toBe("2026-08-10T12:00:00.000Z");
      expect(call[1].to).toBe("2026-08-11T12:00:00.000Z");
      expect(call[1].step).toBe("5m");
    }
  });

  // Narrowed, and this is the test that says so. family=host carries 71 value
  // columns at raw and 101 at 5m; a fleet row reads ten of them. Unnarrowed,
  // a 24h render moved an order of magnitude more numbers than it drew.
  it("asks only for the columns it draws", async () => {
    serveFleet({});

    await fetchFleetTrends([{ id: 1, threads: 4 }], "24h");

    const host = getFleetMetrics.mock.calls.find(
      (c) => c[1].family === "host",
    )?.[1];
    expect(host?.columns).toContain("cpu_total");
    expect(host?.columns).toContain("mem_free");
    expect(host?.columns).toContain("oom_kill_total");
    // BASE names, never a tier's suffixed ones: the hub expands each to the
    // aggregates the answering tier carries, and naming a suffix here would
    // pin the request to one tier and 400 at every other range.
    expect(host?.columns?.some((c) => c.endsWith("_avg"))).toBe(false);
  });

  // MAX_PER_CORE is a transfer limit, and it still is one when the fan-out
  // collapses: thirty-two series per host add up the same either way. A host
  // too large for the per-core stack is left out of that ONE call rather than
  // costing the whole fleet its silhouettes.
  it("asks for per-core samples only for the hosts small enough to want them", async () => {
    serveFleet({});

    await fetchFleetTrends(
      [
        { id: 1, threads: 4 },
        { id: 2, threads: 128 },
      ],
      "1h",
    );

    const cores = getFleetMetrics.mock.calls.find(
      (c) => c[1].family === "cpu_core",
    );
    expect(cores?.[0]).toEqual([1]);
  });

  it("skips the per-core call entirely when no host is small enough", async () => {
    serveFleet({});

    await fetchFleetTrends([{ id: 1, threads: 128 }], "1h");

    expect(
      getFleetMetrics.mock.calls.some((c) => c[1].family === "cpu_core"),
    ).toBe(false);
  });

  // Failure isolation moved with the fan-out: it used to be per host, and it
  // is now per family. Either way the page draws what it has.
  it("loses one column rather than the fleet when a family fails", async () => {
    serveFleet({
      net: new Error("boom"),
      host: response({
        columns: ["cpu_total"],
        series: [{ key: {}, points: [[t0, 12]] }],
      }),
    });

    const trends = await fetchFleetTrends([{ id: 1, threads: 4 }], "1h");

    expect(trends.get(1)?.rx).toEqual([]);
    expect(trends.get(1)?.reporting.length).toBeGreaterThan(0);
  });

  it("asks nothing at all for an empty fleet", async () => {
    serveFleet({});

    const trends = await fetchFleetTrends([], "24h");

    expect(trends.size).toBe(0);
    expect(getFleetMetrics).not.toHaveBeenCalled();
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
    location: "Roubaix, France",
  };
  // Nothing is joined any more: the location rides the host, so this takes
  // the host list and the trends and nothing else.
  it("carries the host through untouched", () => {
    const rows = buildRows([host], new Map());

    expect(rows[0]!.hostname).toBe(host.hostname);
    expect(rows[0]!.location).toBe("Roubaix, France");
  });

  // A host whose trends have not arrived yet must render as gaps and absent
  // markers, never as zeroes: not fetched is not the same as measured zero.
  it("leaves a host without trends empty rather than at zero", () => {
    const rows = buildRows([host], new Map());

    expect(rows[0]!.cpu).toEqual([]);
    expect(rows[0]!.rx).toEqual([]);
    expect(rows[0]!.fullest).toBeNull();
  });

  it("carries a host's trends onto its row", () => {
    const trends: HostTrends = {
      window: null,
      cpu: [{ name: "busy", color: "var(--s1)", values: [1, 2] }],
      mem: [],
      reporting: [1, 2],
      rx: [10],
      tx: [20],
      rxPeak: [],
      txPeak: [],
      fullest: { mount: "/", pct: 50, others: 0 },
      disk: [],
      oomKills: 0,
      dropped: 0,
      postFailures: 0,
    };

    const rows = buildRows([host], new Map([[1, trends]]));

    expect(rows[0]!.cpu).toEqual(trends.cpu);
    expect(rows[0]!.fullest).toEqual(trends.fullest);
  });
});

describe("trafficSeries", () => {
  // A rolled-up response, where the mean and the peak are separate columns.
  // Two interfaces, so the sum is a real sum.
  function rolledUp(): MetricsResponse {
    const at = (i: number) => Date.parse(`2026-08-10T00:0${i}:00Z`);
    const iso = (i: number) => `2026-08-10T00:0${i}:00Z`;
    return {
      family: "net",
      tier: "5m",
      step_s: 60,
      window: { from: iso(0), to: iso(3) },
      requested_window: { from: iso(0), to: iso(3) },
      warnings: [],
      key_columns: ["iface"],
      columns: ["rx_bytes", "rx_bytes_max", "tx_bytes", "tx_bytes_max"],
      series: [
        {
          key: { iface: "eth0" },
          points: [
            [at(0), 10, 100, 1, 10],
            [at(1), 20, 200, 2, 20],
            [at(2), 30, 300, 3, 30],
          ],
        },
        {
          key: { iface: "eth1" },
          points: [
            [at(0), 1, 5, 1, 2],
            [at(1), 2, 6, 2, 4],
            [at(2), 3, 7, 3, 6],
          ],
        },
      ],
      truncated: false,
    } as unknown as MetricsResponse;
  }

  // The raw tier has no _max peer at all: the sample IS its own peak.
  function raw(): MetricsResponse {
    const at = (i: number) => Date.parse(`2026-08-10T00:0${i}:00Z`);
    const iso = (i: number) => `2026-08-10T00:0${i}:00Z`;
    return {
      family: "net",
      tier: "raw",
      step_s: 60,
      window: { from: iso(0), to: iso(3) },
      requested_window: { from: iso(0), to: iso(3) },
      warnings: [],
      key_columns: ["iface"],
      columns: ["rx_bytes", "tx_bytes"],
      series: [
        {
          key: { iface: "eth0" },
          points: [
            [at(0), 10, 1],
            [at(1), 20, 2],
            [at(2), 30, 3],
          ],
        },
      ],
      truncated: false,
    } as unknown as MetricsResponse;
  }

  it("draws the bucket mean, with the peak kept aside", () => {
    // Given a rolled-up response where the two columns differ
    const t = trafficSeries(rolledUp());

    // Then the DRAWN pair is the sum of the means -- Observium's
    // `DEF ... AVERAGE`. Reading the peak here and folding columns to their
    // peak as well compounds into a ceiling several times the real one, and
    // everything under it drops below a pixel.
    expect(t.rx).toEqual([11, 22, 33]);
    // The peak is still read, for the envelope an enlarged view can draw.
    expect(t.rxPeak).toEqual([105, 206, 307]);
  });

  it("folds each pixel column with the same function it read", () => {
    // Given three buckets folded into two columns
    const t = trafficSeries(rolledUp(), 2);

    // Then the drawn series averages within a column, as rrdtool's reduce
    // does with an AVERAGE RRA. Three buckets over two columns splits
    // one/two, so the second column is the mean of 22 and 33.
    expect(t.rx).toEqual([11, 27.5]);
    // And the envelope keeps taking the loudest, so a band behind a line is
    // still the peak it claims to be.
    expect(t.rxPeak).toEqual([105, 307]);
  });

  it("has no separate peak at the raw tier", () => {
    // peakBase() falls back to the bare column there, so the two series
    // would be the same numbers and an envelope drawn on its own line is
    // ink for nothing.
    const t = trafficSeries(raw());
    expect(t.rx).toEqual([10, 20, 30]);
    expect(t.rxPeak).toEqual([]);
  });

  it("builds the pair from a row, so the dialog has it on OPEN", () => {
    // The cell hands its Enlargeable a detailSeries built from the row it
    // already holds. Without that the dialog draws a bare line until somebody
    // touches the range picker.
    const [rx] = trafficDetailSeries({
      rx: [11, 22],
      tx: [1, 2],
      rxPeak: [105, 206],
      txPeak: [2, 4],
    });
    expect(rx?.values).toEqual([11, 22]);
    expect(rx?.band).toEqual([105, 206]);

    // A row without the peak pair -- assembled before it existed, or answered
    // by the raw tier -- opens into a chart with no envelope rather than one
    // with a wrong axis.
    const [bare] = trafficDetailSeries({ rx: [11, 22], tx: [1, 2] });
    expect(bare?.values).toEqual([11, 22]);
    expect(bare?.band).toBeUndefined();
  });

  it("hands the enlarged view a mean line under a peak envelope", () => {
    // The stats table under an enlarged chart prints Latest/Min/Max/Mean off
    // the LINE, so the line has to be the mean or the dialog states a mean
    // that is not one -- and the host page's Traffic dialog, same host and
    // same header, would print a different number.
    const [rx, tx] = trafficDetailSeries(trafficSeries(rolledUp(), 3));
    expect(rx?.values).toEqual([11, 22, 33]);
    expect(rx?.band).toEqual([105, 206, 307]);
    expect(tx?.values).toEqual([2, 4, 6]);

    // At the raw tier there is one series and no envelope.
    const [rawRx] = trafficDetailSeries(trafficSeries(raw(), 3));
    expect(rawRx?.values).toEqual([10, 20, 30]);
    expect(rawRx?.band).toBeUndefined();
  });
});
