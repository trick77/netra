import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type { HostDetail, MetricsResponse } from "../../../lib/api";
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
});
