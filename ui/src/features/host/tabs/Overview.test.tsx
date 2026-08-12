import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type { HostDetail, MetricsResponse } from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import { Overview, filesystemRows } from "./Overview";

const host: HostDetail = {
  id: 7,
  hostname: "kessel",
  site_id: 3,
  last_seen: "2026-08-10T01:00:00Z",
  cpu_total: 12,
  mem_used: 4_000_000_000,
  mem_total: 8_000_000_000,
  uptime_s: 86_400,
  site_name: "Zurich",
  provider_name: "Hetzner",
  fingerprint: "fp",
  host_type: "vps",
  agent_version: "0.4.1",
  go_version: "go1.25",
  build_commit: "abc1234",
  kernel: "6.8.0-31-generic",
  os_name: "Ubuntu 24.04",
  arch: "amd64",
  cpu_model: "EPYC 7003",
  cores: 4,
  threads: 8,
  memory_total: 8_000_000_000,
  latitude: null,
  longitude: null,
  created_at: "2026-01-01T00:00:00Z",
  capabilities: { docker: "ok", smart: "not permitted" },
};

function response(
  over: Partial<MetricsResponse> & { family: string },
): MetricsResponse {
  return {
    tier: "raw",
    step_s: 60,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T01:00:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T01:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: [],
    series: [],
    truncated: false,
    ...over,
  };
}

// One host sample: swap_total null is the case §7.5 exists for.
function hostMetrics(swapTotal: number | null, swapUsed: number | null) {
  return response({
    family: "host",
    columns: [
      "cpu_user",
      "cpu_system",
      "cpu_iowait",
      "cpu_steal",
      "mem_total",
      "mem_used",
      "swap_total",
      "swap_used",
    ],
    series: [
      {
        key: {},
        points: [
          [
            1_754_784_000_000,
            10,
            4,
            1,
            0,
            8_000_000_000,
            4_000_000_000,
            swapTotal,
            swapUsed,
          ],
        ],
      },
    ],
  });
}

const fsMetrics = response({
  family: "filesystem",
  key_columns: ["filesystem"],
  columns: ["total", "used", "free", "inodes_total", "inodes_used"],
  series: [
    {
      key: { filesystem: "/" },
      points: [
        [
          1_754_784_000_000, 100_000_000_000, 40_000_000_000, 55_000_000_000,
          100, 40,
        ],
      ],
    },
  ],
});

const netMetrics = response({
  family: "net",
  key_columns: ["interface"],
  columns: ["rx_bytes", "tx_bytes"],
  series: [
    {
      key: { interface: "eth0" },
      // The FINAL bucket of the response window above. Inside the window at
      // all, because griddedValues() drops a point that falls outside it --
      // and in the last bucket specifically, so this test is about the
      // formatter rather than about which bucket the card reads from.
      points: [[1_786_323_540_000, 2_000_000, 500_000]],
    },
  ],
});

function agentMetrics(dropped: number) {
  return response({
    family: "agent",
    columns: ["buffer_depth", "buffer_dropped_total", "post_failures_total"],
    series: [{ key: {}, points: [[1_754_784_000_000, 2, dropped, 0]] }],
  });
}

function renderOverview(over: Partial<Parameters<typeof Overview>[0]> = {}) {
  return render(
    <Overview
      host={host}
      hostMetrics={hostMetrics(null, null)}
      filesystemMetrics={fsMetrics}
      agentMetrics={agentMetrics(0)}
      sensorMetrics={null}
      containers={[]}
      units={[]}
      now={new Date("2026-08-10T01:00:30Z")}
      {...over}
    />,
  );
}

describe("Overview", () => {
  it("renders an absent swap as none, never as 0", () => {
    renderOverview();
    const memory = screen.getByRole("region", { name: "Memory" });
    expect(within(memory).getByText(/none/i)).toBeInTheDocument();
    expect(within(memory).queryByText(/\b0 B\b/)).toBeNull();
  });

  it("distinguishes swap that exists and is unused from swap that does not exist", () => {
    renderOverview({ hostMetrics: hostMetrics(2_000_000_000, 0) });
    const memory = screen.getByRole("region", { name: "Memory" });
    expect(within(memory).queryByText(/none/i)).toBeNull();
  });

  it("shows disk as absolute bytes per filesystem, never as a ratio", () => {
    renderOverview();
    const disk = screen.getByRole("region", { name: /disk/i });
    expect(within(disk).getByText("/")).toBeInTheDocument();
    expect(within(disk).getByText(/40 GB used/)).toBeInTheDocument();
    expect(disk.textContent).not.toMatch(/%/);
  });

  it("raises a dropped-sample count into needs-attention, with a word beside the dot", () => {
    renderOverview({ agentMetrics: agentMetrics(12) });
    const attention = screen.getByRole("region", { name: /needs attention/i });
    expect(within(attention).getByText(/critical/i)).toBeInTheDocument();
    expect(within(attention).getByText(/12/)).toBeInTheDocument();
  });

  it("says so when nothing is wrong rather than rendering an empty card", () => {
    renderOverview();
    const attention = screen.getByRole("region", { name: /needs attention/i });
    expect(within(attention).getByText(/all clear/i)).toBeInTheDocument();
  });

  it("summarises the tabs instead of duplicating them", () => {
    renderOverview({
      containers: [
        {
          id: 1,
          container_key: "proj/web",
          name: "web",
          image: "nginx",
          is_agent: false,
        },
        {
          id: 2,
          container_key: "proj/db",
          name: "db",
          image: "pg",
          is_agent: false,
        },
      ],
      units: [
        {
          id: 1,
          unit_name: "ssh.service",
          state: "active",
          substate: "running",
          since: null,
        },
        {
          id: 2,
          unit_name: "cron.service",
          state: "failed",
          substate: "dead",
          since: null,
        },
      ],
    });
    // A count and a link, not the inventory itself.
    expect(screen.getByText("2 containers")).toBeInTheDocument();
    expect(screen.queryByText("nginx")).toBeNull();
    expect(screen.getByText(/1 failed/)).toBeInTheDocument();
  });

  it("reads the sensor family's temp column", () => {
    renderOverview({
      sensorMetrics: response({
        family: "sensor",
        // The window has to contain the point. Every sensor row now reads
        // its number off the SAME gridded array its sparkline draws, so a
        // fixture whose sample sits outside its own window grids to all
        // null and the row correctly reads absent -- which is what these
        // fixtures used to hide by taking the number from the ungridded
        // series while the sparkline beside it drew nothing.
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label"],
        columns: ["temp"],
        series: [
          {
            key: { chip: "coretemp", label: "Package id 0" },
            points: [[1_754_784_000_000, 47.5]],
          },
        ],
      }),
    });
    const temperature = screen.getByRole("region", { name: /temperature/i });
    expect(
      within(temperature).getByText("coretemp Package id 0"),
    ).toBeInTheDocument();
    expect(within(temperature).getByText("48 °C")).toBeInTheDocument();
  });

  // The sensor family carries fans, voltages, currents and power now, and
  // only temperatures have a temp column. Mapping the whole family would fill
  // a panel headed "Temperature" with rows reading "nct6775 fan1 —", burying
  // the readings it exists to show under ones it cannot render.
  it("shows only temperature sensors, not the fans and rails beside them", () => {
    renderOverview({
      sensorMetrics: response({
        family: "sensor",
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["temp", "value"],
        series: [
          {
            key: { chip: "nct6775", label: "CPU", kind: "temperature" },
            points: [[1_754_784_000_000, 45, 45]],
          },
          {
            // temp is null: a fan has no temperature.
            key: { chip: "nct6775", label: "CPU Fan", kind: "fan" },
            points: [[1_754_784_000_000, null, 1200]],
          },
          {
            key: { chip: "nct6775", label: "+12V", kind: "voltage" },
            points: [[1_754_784_000_000, null, 12.1]],
          },
        ],
      }),
    });

    const temperature = screen.getByRole("region", { name: /temperature/i });
    expect(within(temperature).getByText("nct6775 CPU")).toBeInTheDocument();
    expect(within(temperature).getByText("45 °C")).toBeInTheDocument();
    // The non-temperature series must not appear in THIS panel -- an empty
    // row is worse than an absent one, because it reads as a broken sensor.
    expect(within(temperature).queryByText("nct6775 CPU Fan")).toBeNull();
    expect(within(temperature).queryByText("nct6775 +12V")).toBeNull();

    // They are not discarded, though: each kind gets its own card, in its
    // own unit, on its own scale. Putting a 1200 RPM fan on the same axis as
    // a 45 °C package is the reason they are separated rather than merged.
    const fans = screen.getByRole("region", { name: "Fans" });
    expect(within(fans).getByText("nct6775 CPU Fan")).toBeInTheDocument();
    expect(within(fans).getByText("1200 RPM")).toBeInTheDocument();

    const power = screen.getByRole("region", { name: "Power" });
    expect(within(power).getByText("nct6775 +12V")).toBeInTheDocument();
    expect(within(power).getByText("12.10 V")).toBeInTheDocument();
  });

  // A host with no fans -- every VM, every cloud instance -- must not carry
  // an empty "Fans" card. A card that is blank on most of the fleet teaches
  // people to stop reading this column.
  it("omits the fan and power cards on a host that reports neither", () => {
    renderOverview({
      sensorMetrics: response({
        family: "sensor",
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["temp", "value"],
        series: [
          {
            key: {
              chip: "coretemp",
              label: "Package id 0",
              kind: "temperature",
            },
            points: [[1_754_784_000_000, 45, 45]],
          },
        ],
      }),
    });

    expect(
      screen.getByRole("region", { name: /temperature/i }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Fans" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Power" })).toBeNull();
  });

  // A fan's failure is its minimum. Averaged across a five-minute bucket a
  // stall is invisible -- the mean of a stopped fan and a spin-up is a
  // perfectly healthy number -- so the fan row must read value_min at the
  // rolled tiers, and must not fall back to the _avg that candidates()
  // prefers.
  it("reads a fan from value_min, not the average that hides a stall", () => {
    renderOverview({
      sensorMetrics: response({
        family: "sensor",
        tier: "5m",
        step_s: 300,
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:10:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:10:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["value_avg", "value_max", "value_min"],
        series: [
          {
            key: { chip: "nct6775", label: "fan2", kind: "fan" },
            // A bucket the fan spent partly stopped: the average and the
            // maximum both look fine, and only the minimum says so.
            points: [[1_754_784_300_000, 1180, 1400, 0]],
          },
        ],
      }),
    });

    const fans = screen.getByRole("region", { name: "Fans" });
    expect(within(fans).getByText("0 RPM")).toBeInTheDocument();
    expect(within(fans).queryByText("1180 RPM")).toBeNull();
  });

  it("reports a collector that is not running, with its reason", () => {
    renderOverview();
    expect(screen.getByText("not permitted")).toBeInTheDocument();
  });
});

describe("filesystemRows", () => {
  it("returns nothing rather than guessing when the tier lacks the columns", () => {
    expect(filesystemRows(response({ family: "filesystem" }))).toEqual([]);
    expect(filesystemRows(null)).toEqual([]);
  });

  it("keeps used and free as measured, because they do not sum to total", () => {
    const [row] = filesystemRows(fsMetrics);
    expect(row).toMatchObject({
      label: "/",
      used: 40_000_000_000,
      free: 55_000_000_000,
      total: 100_000_000_000,
    });
  });
});

describe("Overview processor panel", () => {
  // The rollup tiers carry cpu_total and not the four per-state columns, so
  // above an hour this -- the headline chart on the page -- had nothing to
  // draw and rendered as not-collected, while the fleet row for the same
  // host drew a silhouette from the total. The two must not disagree about
  // whether a host's CPU can be drawn.
  it("draws the total as one band when the tier has no per-state breakdown", () => {
    render(
      <Overview
        host={host}
        filesystemMetrics={null}
        agentMetrics={null}
        sensorMetrics={null}
        containers={null}
        units={null}
        hostMetrics={response({
          family: "host",
          tier: "5m",
          columns: ["cpu_total_avg"],
          series: [
            {
              key: {},
              points: [
                [1_754_784_000_000, 30],
                [1_754_784_300_000, 35],
              ],
            },
          ],
        })}
      />,
    );

    // Scoped to the Processor panel: the page carries other panels now, and
    // at this tier some of them legitimately say "Not collected". The claim
    // here is about THIS chart falling back to a total band.
    const panel = screen.getByRole("region", { name: "Processor chart" });
    expect(panel).toBeInTheDocument();
    expect(within(panel).queryByText("Not collected")).toBeNull();
  });

  // net_rx and net_tx are BYTES per second: network.go computes them as
  // (rxBytes - prev) / elapsed. Rendered through bitrate() the card read 8x
  // low and entirely plausible -- a 2 MB/s link showed as "2 Mb/s" -- and
  // the fleet's traffic cell carried the identical bug, so nothing on screen
  // contradicted it.
  it("shows traffic in bytes per second, not bits", () => {
    renderOverview({ netMetrics });
    const traffic = screen.getByRole("region", { name: "Traffic" });

    expect(within(traffic).getByText(/2 MB\/s/)).toBeInTheDocument();
    expect(within(traffic).getByText(/500 kB\/s/)).toBeInTheDocument();
    expect(traffic.textContent).not.toMatch(/b\/s/);
  });

  // A host that stopped reporting must read as absent, never as the last
  // rate it ever sent. This card scanned backwards for the last non-null,
  // so a dead agent's traffic sat frozen at its final value -- while the
  // fleet's traffic cell, reading the latest bucket, showed the same host as
  // absent. "The agent is down" must not render as "traffic is steady".
  it("reads a host that stopped reporting as absent, not as its last known rate", () => {
    const stale = response({
      family: "net",
      key_columns: ["interface"],
      columns: ["rx_bytes", "tx_bytes"],
      series: [
        {
          key: { interface: "eth0" },
          // A real reading early in the window and nothing since: every
          // later bucket grids to null.
          points: [[1_786_321_800_000, 2_000_000, 500_000]],
        },
      ],
    });

    renderOverview({ netMetrics: stale });
    const traffic = screen.getByRole("region", { name: "Traffic" });

    expect(traffic.textContent).not.toMatch(/MB\/s/);
    expect(
      within(traffic).getAllByText(new RegExp(ABSENT)).length,
    ).toBeGreaterThan(0);
  });
});

// The exhaustion gauges. Nothing else in netra answers "am I about to hit a
// limit", and running out does not present as a resource problem: accept()
// starts failing and conntrack drops flows while the CPU and memory cards
// stay calm.
describe("Overview limits card", () => {
  function limitsResponse(
    over: Partial<MetricsResponse> = {},
  ): MetricsResponse {
    return {
      family: "host",
      tier: "raw",
      step_s: 60,
      // A one-bucket window, so the sample below IS the latest bucket. The
      // gauge deliberately reads the latest bucket rather than the last
      // number the host ever sent: a host that stopped reporting an hour
      // ago must read absent, not "comfortably at 40%".
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:01:00Z" },
      requested_window: {
        from: "2026-08-10T00:00:00Z",
        to: "2026-08-10T00:01:00Z",
      },
      warnings: [],
      key_columns: [],
      columns: ["fd_used", "fd_limit", "conntrack_count", "conntrack_limit"],
      series: [
        {
          key: {},
          points: [[1_786_320_000_000, 48231, 262144, 1800, 262144]],
        },
      ],
      truncated: false,
      ...over,
    };
  }

  it("shows a gauge against its ceiling, because the ratio is the story", () => {
    renderOverview({ hostMetrics: limitsResponse() });
    const limits = screen.getByRole("region", { name: "Limits" });
    // Grouped digits, not a rounded magnitude: "48 k of 262 k" throws away
    // the only digits that separate comfortable from nearly-full.
    expect(within(limits).getByText(/48 231 of 262 144/)).toBeInTheDocument();
  });

  // At a rolled tier the mean hides the moment that matters: accept() fails
  // at the peak, not at the average. candidates() prefers _avg, so the peak
  // has to be asked for by name.
  it("reads the peak at a rolled tier, not the average", () => {
    renderOverview({
      hostMetrics: limitsResponse({
        tier: "5m",
        step_s: 300,
        columns: ["fd_used_avg", "fd_used_max", "fd_limit"],
        series: [
          {
            key: {},
            points: [[1_786_320_000_000, 40000, 250000, 262144]],
          },
        ],
      }),
    });
    const limits = screen.getByRole("region", { name: "Limits" });
    expect(within(limits).getByText(/250 000 of 262 144/)).toBeInTheDocument();
    expect(within(limits).queryByText(/40 000/)).toBeNull();
  });

  // The capability the agent reported, in place of the meter it explains.
  // An em-dash next to a bar that never fills is indistinguishable from a
  // broken collector.
  // /proc/sys/fs/file-max is int64 max on a great many hosts. A bar against
  // 9.2 quintillion can never move, and past Number.MAX_SAFE_INTEGER the
  // figure has already lost precision in transit -- so the ratio is both
  // useless and wrong.
  it("says no limit rather than drawing a ratio against an unbounded ceiling", () => {
    renderOverview({
      hostMetrics: limitsResponse({
        columns: ["fd_used", "fd_limit"],
        series: [
          {
            key: {},
            points: [[1_786_320_000_000, 3352, 9223372036854775807]],
          },
        ],
      }),
    });
    const limits = screen.getByRole("region", { name: "Limits" });
    expect(within(limits).getByText(/3 352 · no limit/)).toBeInTheDocument();
    // The number the host never reported must not appear.
    expect(within(limits).queryByText(/776 000/)).toBeNull();
  });

  it("says why a gauge is missing when the agent explained it", () => {
    renderOverview({
      host: {
        ...host,
        capabilities: { ...host.capabilities, conntrack: "unavailable" },
      },
      hostMetrics: limitsResponse(),
    });
    const limits = screen.getByRole("region", { name: "Limits" });
    expect(within(limits).getByText("unavailable")).toBeInTheDocument();
  });

  // sockets_used and tcp_alloc have no ceiling in the schema, so they cannot
  // answer the headroom question this card exists for. They are deliberately
  // not here, and this pins that decision rather than leaving it to be
  // "fixed" later.
  it("carries no row for a gauge that has no ceiling", () => {
    renderOverview({ hostMetrics: limitsResponse() });
    const limits = screen.getByRole("region", { name: "Limits" });
    expect(within(limits).queryByText(/sockets used|tcp alloc/i)).toBeNull();
  });

  it("is absent entirely on a host that reported no limits at all", () => {
    renderOverview();
    expect(screen.queryByRole("region", { name: "Limits" })).toBeNull();
  });
});

// An OOM kill is the one memory fact no chart can carry: mem_used is back to
// normal by the time anyone looks, precisely BECAUSE the kill happened.
describe("Overview OOM attention", () => {
  function oomResponse(points: (number | null)[][]): MetricsResponse {
    return {
      family: "host",
      tier: "raw",
      step_s: 60,
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:05:00Z" },
      requested_window: {
        from: "2026-08-10T00:00:00Z",
        to: "2026-08-10T00:05:00Z",
      },
      warnings: [],
      key_columns: [],
      columns: ["oom_kill_total"],
      series: [{ key: {}, points }],
      truncated: false,
    };
  }

  it("reports kills that happened inside the window", () => {
    renderOverview({
      hostMetrics: oomResponse([
        [1_786_320_000_000, 4],
        [1_786_320_060_000, 6],
      ]),
    });
    const attention = screen.getByRole("region", { name: /attention/i });
    expect(within(attention).getByText(/2 OOM kills/)).toBeInTheDocument();
  });

  // The counter is cumulative since boot. A host that killed something a
  // year ago and nothing since is healthy, and must not carry a permanent
  // badge -- which is what reading the raw total would do.
  it("stays silent for a counter that is high but flat", () => {
    renderOverview({
      hostMetrics: oomResponse([
        [1_786_320_000_000, 4000],
        [1_786_320_060_000, 4000],
      ]),
    });
    const attention = screen.getByRole("region", { name: /attention/i });
    expect(within(attention).queryByText(/OOM/)).toBeNull();
  });
});
