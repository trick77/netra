import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type { HostDetail, MetricsResponse } from "../../../lib/api";
import { AgentTab, collectorRows } from "./AgentTab";

const host: HostDetail = {
  id: 7,
  hostname: "kessel",
  last_seen: "2026-08-10T01:00:00Z",
  cpu_total: 12,
  mem_used: 4_000_000_000,
  mem_total: 8_000_000_000,
  uptime_s: 86_400,
  net_rx_bytes: 1.5e6,
  net_tx_bytes: 4e5,
  services_total: 397,
  services_failed: 1,
  fingerprint: "fp",
  host_type: "vps",
  agent_version: "0.4.1",
  go_version: "go1.25",
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

const t = (i: number) => Date.parse(`2026-08-10T00:0${i}:00Z`);
const window = { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:03:00Z" };

// The raw tier: a boolean `ok` per sample, the error code beside it.
const raw = {
  family: "collector",
  tier: "raw",
  step_s: 60,
  window,
  requested_window: window,
  warnings: [],
  key_columns: ["collector"],
  columns: ["duration_ms", "ok", "error_code"],
  series: [
    {
      key: { collector: "cpu" },
      points: [
        [t(0), 2, true, null],
        [t(1), 4, true, null],
        [t(2), 3, true, null],
      ],
    },
    {
      key: { collector: "netstat" },
      points: [
        [t(0), 10, true, null],
        [t(1), 30, false, "timeout"],
        [t(2), 20, true, null],
      ],
    },
    {
      key: { collector: "sensors" },
      points: [
        [t(0), 1, true, null],
        [t(1), 1, true, null],
        [t(2), 1, false, "eperm"],
      ],
    },
  ],
  truncated: false,
} as unknown as MetricsResponse;

// A rolled-up tier: counts instead of a boolean, a peak beside the mean.
const rolled = {
  ...raw,
  tier: "5m",
  columns: [
    "duration_ms_avg",
    "duration_ms_max",
    "sample_count",
    "failure_count",
    "error_code",
  ],
  series: [
    {
      key: { collector: "netstat" },
      points: [
        [t(0), 10, 50, 5, 0, null],
        [t(1), 20, 80, 5, 2, "timeout"],
        [t(2), 30, 40, 5, 0, null],
      ],
    },
  ],
} as unknown as MetricsResponse;

describe("collectorRows", () => {
  it("reads failures, mean and peak off the raw samples", () => {
    const rows = collectorRows(raw, {});
    expect(rows.map((r) => [r.name, r.failures, r.failing])).toEqual([
      ["cpu", 0, false],
      ["netstat", 1, false],
      ["sensors", 1, true],
    ]);
    expect(rows[1]).toMatchObject({ mean: 20, peak: 30, reason: "timeout" });
  });

  it("sums the failure counts of a rolled-up tier and takes its peaks", () => {
    const [row] = collectorRows(rolled, {});
    expect(row).toMatchObject({
      failures: 2,
      failing: false,
      mean: 20,
      peak: 80,
    });
  });

  // A collector that runs and returns partial data is not fine: the agent's
  // stated reason rides its row rather than a green ok.
  it("keeps the agent's reason on a collector that has samples", () => {
    const rows = collectorRows(raw, { cpu: "not permitted" });
    expect(rows.find((r) => r.name === "cpu")).toMatchObject({
      capability: "not permitted",
      failures: 0,
    });
  });

  // A collector that never ran has no samples, and it is the row a reader
  // most needs: the agent's own reason, from its capabilities.
  it("adds a row for a capability that is off, with the agent's reason", () => {
    const rows = collectorRows(null, { docker: "ok", smart: "not permitted" });
    expect(rows).toEqual([
      expect.objectContaining({
        name: "smart",
        failures: null,
        reason: "not permitted",
      }),
    ]);
  });
});

describe("AgentTab", () => {
  it("lists every collector, the ones in trouble first", () => {
    render(<AgentTab host={host} sources={{ collector: raw }} />);
    const table = screen.getByRole("region", { name: "Collectors" });
    const names = within(table)
      .getAllByRole("row")
      .slice(1)
      // The row rail's screen-reader word ("Critical") rides the first cell
      .map((r) =>
        within(r)
          .getAllByRole("cell")[0]!
          .textContent!.replace(/^(Critical|Warning)/, ""),
      );
    // Failing now first: the red row is the one to act on, then the
    // collector that never ran, then the one that failed at times
    expect(names).toEqual(["sensors", "smart", "netstat", "cpu"]);
    expect(within(table).getByText("failing")).toBeInTheDocument();
    expect(within(table).getByText("failed at times")).toBeInTheDocument();
    expect(within(table).getByText("not permitted")).toBeInTheDocument();
  });

  // An agent too old to report capabilities or collectors is not an agent
  // with none, and an empty card that says nothing reads as the second.
  it("says the agent reported nothing rather than drawing an empty list", () => {
    render(
      <AgentTab
        host={{ ...host, capabilities: {} }}
        sources={{ collector: { ...raw, series: [] } }}
      />,
    );
    expect(
      screen.getByText(/agent reported no collectors/i),
    ).toBeInTheDocument();
  });

  // Only an answer can say there are none: a family still being asked for,
  // or one the hub could not answer, says nothing about the host.
  it("never claims no collectors before the hub has answered", () => {
    const bare = { ...host, capabilities: {} };
    const { rerender } = render(
      <AgentTab host={bare} sources={{ collector: null, pending: true }} />,
    );
    const panel = () => screen.getByRole("region", { name: "Collectors" });
    expect(
      within(panel()).getByText("Asking the hub for this window."),
    ).toBeInTheDocument();
    rerender(<AgentTab host={bare} sources={{ collector: null }} />);
    expect(
      screen.getByText("The hub did not answer for the collectors."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/agent reported no collectors/i)).toBeNull();
  });

  // Hub latency leads; scrape duration and connect failures are rows.
  it("draws the hub latency chart and the agent's own rows", () => {
    const { container } = render(<AgentTab host={host} sources={{}} />);
    expect(
      screen.getByRole("heading", { name: "Hub latency" }),
    ).toBeInTheDocument();
    const ledger = container.querySelector(".ledger") as HTMLElement;
    expect(within(ledger).getByText("Scrape duration")).toBeInTheDocument();
    expect(
      within(ledger).getByText("Hub connect failures"),
    ).toBeInTheDocument();
  });
});
