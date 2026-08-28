import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type { HostDetail, MetricsResponse } from "../../../lib/api";
import { LimitsCard } from "./LimitsCard";

// The card moved off the Overview tab and onto System; these tests moved with
// it, and are otherwise the ones that were written against it there.
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

function limitsResponse(over: Partial<MetricsResponse> = {}): MetricsResponse {
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

function renderCard(over: Partial<Parameters<typeof LimitsCard>[0]> = {}) {
  return render(<LimitsCard host={host} hostMetrics={null} {...over} />);
}

describe("LimitsCard", () => {
  it("shows a gauge against its ceiling, because the ratio is the story", () => {
    renderCard({ hostMetrics: limitsResponse() });
    const limits = screen.getByRole("region", { name: "Limits" });
    // Grouped digits, not a rounded magnitude: "48 k of 262 k" throws away
    // the only digits that separate comfortable from nearly-full.
    expect(within(limits).getByText(/48 231 of 262 144/)).toBeInTheDocument();
  });

  // At a rolled tier the mean hides the moment that matters: accept() fails
  // at the peak, not at the average. candidates() prefers _avg, so the peak
  // has to be asked for by name.
  it("reads the peak at a rolled tier, not the average", () => {
    renderCard({
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
    renderCard({
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
    renderCard({
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
    renderCard({ hostMetrics: limitsResponse() });
    const limits = screen.getByRole("region", { name: "Limits" });
    expect(within(limits).queryByText(/sockets used|tcp alloc/i)).toBeNull();
  });

  it("is absent entirely on a host that reported no limits at all", () => {
    renderCard();
    expect(screen.queryByRole("region", { name: "Limits" })).toBeNull();
  });
});
