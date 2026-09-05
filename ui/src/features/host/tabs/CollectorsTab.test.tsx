import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type { HostDetail } from "../../../lib/api";
import { CollectorsTab } from "./CollectorsTab";

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

describe("CollectorsTab", () => {
  // Why the SMART panels on Storage are empty, and why the container list is.
  // The agent is the only party that knows, and this is where it says so.
  it("reports a collector that is not running, with its reason", () => {
    render(<CollectorsTab host={host} sources={{}} />);

    const list = screen.getByRole("region", { name: "Collectors" });
    expect(within(list).getByText("not permitted")).toBeInTheDocument();
    expect(within(list).getByText("ok")).toBeInTheDocument();
  });

  // An agent too old to report capabilities is not an agent with none, and
  // an empty card that says nothing reads as the second.
  it("says the agent reported nothing rather than drawing an empty list", () => {
    render(<CollectorsTab host={{ ...host, capabilities: {} }} sources={{}} />);

    expect(
      screen.getByText(/agent reported no capabilities/i),
    ).toBeInTheDocument();
  });

  // The list says which collectors are failing now; the panels say for how
  // long. The tab is the pair -- a list on its own cannot answer either
  // question fully, which is why these panels came over from System.
  it("draws the agent's own panels under the list", () => {
    render(<CollectorsTab host={host} sources={{}} />);

    expect(screen.getByRole("heading", { name: "Agent" })).toBeInTheDocument();
    for (const title of ["Hub latency", "Scrape duration"]) {
      expect(screen.getByRole("heading", { name: title })).toBeInTheDocument();
    }
  });
});
