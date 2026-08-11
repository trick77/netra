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
  it("stacks the four CPU states when the tier carries them", async () => {
    serve({
      host: response({
        columns: ["cpu_user", "cpu_system", "cpu_iowait", "cpu_steal"],
        series: [
          {
            key: {},
            points: [
              [t0, 10, 5, 1, 0],
              [t0 + hour, 12, 4, 1, 0],
              [t0 + 2 * hour, 11, 6, 2, 0],
            ],
          },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.cpu.map((b) => b.name)).toEqual([
      "user",
      "system",
      "iowait",
      "steal",
    ]);
    expect(trends.cpu[0]!.values).toEqual([10, 12, 11]);
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

  // used / (used + free) is df's Use%, which is the number the operator has
  // already seen over SSH. used / total is not: total includes the root
  // reserve, so it reports a full disk as less full than df does.
  it("picks the fullest filesystem by df's own percentage", async () => {
    serve({
      filesystem: response({
        key_columns: ["filesystem"],
        columns: ["used", "free", "total"],
        series: [
          { key: { filesystem: "root" }, points: [[t0, 68, 32, 110]] },
          { key: { filesystem: "data" }, points: [[t0, 88, 12, 110]] },
          { key: { filesystem: "boot" }, points: [[t0, 10, 90, 110]] },
        ],
      }),
    });

    const trends = await fetchHostTrends(1, "1h");

    expect(trends.fullest).toEqual({ mount: "data", pct: 88, others: 2 });
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
      rx: [10],
      tx: [20],
      fullest: { mount: "/", pct: 50, others: 0 },
    };

    const rows = buildRows([host], [site], new Map([[1, trends]]));

    expect(rows[0]!.cpu).toEqual(trends.cpu);
    expect(rows[0]!.fullest).toEqual(trends.fullest);
  });
});
