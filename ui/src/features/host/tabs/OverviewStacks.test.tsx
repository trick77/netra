import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type { HostDetail, MetricsResponse } from "../../../lib/api";
import { Overview } from "./Overview";

const host: HostDetail = {
  id: 7,
  hostname: "kessel",
  site_id: 3,
  last_seen: "2026-08-10T01:00:00Z",
  cpu_total: 12,
  mem_used: 4_000_000_000,
  mem_total: 8_000_000_000,
  uptime_s: 86_400,
  net_rx_bytes: null,
  net_tx_bytes: null,
  threads: 4,
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
  memory_total: 8_000_000_000,
  latitude: null,
  longitude: null,
  created_at: "2026-01-01T00:00:00Z",
  capabilities: {},
};

const t0 = 1_754_784_000_000;

function response(
  over: Partial<MetricsResponse> & { family: string },
): MetricsResponse {
  return {
    tier: "raw",
    step_s: 60,
    // The window must actually contain t0, or griddedValues() maps every
    // point outside the grid and each band comes back all-null.
    window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:02:00Z" },
    requested_window: {
      from: "2025-08-10T00:00:00Z",
      to: "2025-08-10T00:02:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: [],
    series: [],
    truncated: false,
    ...over,
  } as MetricsResponse;
}

const cores = response({
  family: "cpu_core",
  key_columns: ["core"],
  columns: ["busy"],
  series: [
    { key: { core: "0" }, points: [[t0, 80]] },
    { key: { core: "1" }, points: [[t0, 40]] },
    { key: { core: "2" }, points: [[t0, 0]] },
    { key: { core: "3" }, points: [[t0, 0]] },
  ],
});

const hostMetrics = response({
  family: "host",
  columns: [
    "cpu_total",
    "cpu_user",
    "cpu_system",
    "cpu_iowait",
    "cpu_steal",
    "mem_total",
    "mem_free",
    "mem_buffers",
    "mem_cached",
    "mem_shared",
  ],
  series: [
    {
      key: {},
      points: [[t0, 30, 20, 8, 1, 1, 1000, 200, 30, 100, 50]],
    },
  ],
});

function renderOverview(over: Partial<Parameters<typeof Overview>[0]> = {}) {
  return render(
    <Overview
      host={host}
      hostMetrics={hostMetrics}
      coreMetrics={cores}
      filesystemMetrics={null}
      agentMetrics={null}
      sensorMetrics={null}
      containers={null}
      units={null}
      {...over}
    />,
  );
}

describe("Overview processor stack", () => {
  // The host page and the fleet row show the same machine, and they must not
  // disagree about it. Both draw the per-core stack.
  it("draws one band per core rather than the state breakdown", () => {
    const { container } = renderOverview();
    const panel = screen.getByRole("region", { name: "Processor chart" });

    expect(within(panel).getByLabelText(/Processor over time/i)).toBeTruthy();
    expect(
      container.querySelectorAll(
        '[aria-label="Processor chart"] path[data-band]',
      ),
    ).toHaveLength(4);
  });

  // Falling back rather than showing a not-collected panel: a true
  // silhouette is available, and the fleet row for this host draws one.
  it("falls back to a single total band when there are no per-core series", () => {
    const { container } = renderOverview({ coreMetrics: null });

    expect(
      container.querySelectorAll(
        '[aria-label="Processor chart"] path[data-band]',
      ),
    ).toHaveLength(1);
  });

  // stackBands() breaks every band at any index where ANY series is null,
  // because a running total is undefined there. A bare metal host reports
  // cpu_steal as NULL in every bucket -- correctly, it has no hypervisor to
  // steal from -- and that one empty series blanked the whole chart: a legend
  // naming four states above an empty box. An all-null band is not a band.
  it("still draws the breakdown when one state is absent on this host", () => {
    const { container } = renderOverview({
      hostMetrics: response({
        family: "host",
        columns: [
          "cpu_total",
          "cpu_user",
          "cpu_system",
          "cpu_iowait",
          "cpu_steal",
        ],
        series: [
          {
            key: {},
            points: [
              [t0, 30, 20, 8, 1, null],
              [t0 + 60_000, 32, 22, 8, 1, null],
            ],
          },
        ],
      }),
    });

    const bands = container.querySelectorAll(
      '[aria-label="CPU time breakdown chart"] path[data-band]',
    );
    expect(bands).toHaveLength(3);
    for (const b of bands) expect(b.getAttribute("d")).not.toBe("");
  });

  // The breakdown is not displaced by the per-core stack: "which core" and
  // "doing what" are different questions, and a reader wants both.
  it("keeps the CPU time breakdown as its own panel", () => {
    renderOverview();

    const panel = screen.getByRole("region", {
      name: "CPU time breakdown chart",
    });
    // getAllByText: the panel names each state twice, once in the latest-value
    // line and once in the legend.
    expect(within(panel).getAllByText(/user/).length).toBeGreaterThan(0);
    expect(within(panel).getAllByText(/steal/).length).toBeGreaterThan(0);
  });
});

describe("Overview memory stack", () => {
  it("draws the memory partition against mem_total", () => {
    const { container } = renderOverview();

    // used, buffers, cached, shared -- no ARC on a host without ZFS, and
    // free is the gap to the top rather than a band.
    expect(
      container.querySelectorAll('[aria-label="Memory chart"] path[data-band]'),
    ).toHaveLength(4);
    expect(
      screen.queryByRole("region", { name: "Memory chart" }),
    ).toBeInTheDocument();
  });

  // The meter answers "how full right now" and the chart answers "how did it
  // get there". Adding the chart must not cost the meter.
  it("keeps the memory meter alongside the chart", () => {
    renderOverview();

    const meter = screen.getByRole("region", { name: "Memory" });
    expect(within(meter).getByText(/of/)).toBeTruthy();
  });
});
