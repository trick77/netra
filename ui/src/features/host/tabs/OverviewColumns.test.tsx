// Where each card goes, at each column count.
//
// The old layout poured the cards into a CSS multi-column box and let the
// browser balance them, so a card's column depended on the heights above it
// and on which cards the host happened to have: the same subject sat left on
// one machine and right on the next. CARD_COLUMNS states the placement, and
// these tests are the only thing that can hold it -- jsdom applies no
// stylesheet, so the DOM grouping is the whole assertion available.
import { afterEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
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
  net_rx_bytes: 1.5e6,
  net_tx_bytes: 4e5,
  services_total: 397,
  services_failed: 1,
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
  capabilities: { docker: "ok" },
};

const metrics: MetricsResponse = {
  tier: "raw",
  step_s: 60,
  family: "host",
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
};

/** A matchMedia that answers for the widths `.cardcols` lays out at. */
function stubWidth(px: number) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => {
      const min = Number(/min-width:\s*(\d+)/.exec(query)?.[1] ?? "0");
      return {
        matches: px >= min,
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      };
    }),
  );
}

function columnsOf(px: number | null): string[][] {
  if (px === null) {
    // No matchMedia at all -- an old browser, or jsdom left as it ships.
    vi.stubGlobal("matchMedia", undefined);
  } else {
    stubWidth(px);
  }
  const { container } = render(
    <Overview
      host={host}
      hostMetrics={metrics}
      filesystemMetrics={null}
      agentMetrics={null}
      sensorMetrics={null}
      containers={[]}
      units={[]}
      now={new Date("2026-08-10T01:00:30Z")}
    />,
  );
  return [...container.querySelectorAll(".cardcol")].map((column) =>
    [...column.children].map(
      (card) =>
        card.getAttribute("aria-label") ??
        card.querySelector("h3, h4")?.textContent ??
        "?",
    ),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Overview card columns", () => {
  it("stacks every card in one column on a narrow viewport", () => {
    const columns = columnsOf(700);
    expect(columns.length).toBe(1);
    expect(columns[0]!.slice(0, 3)).toEqual(["Traffic", "Processor", "Memory"]);
  });

  // Fleet order down the first column: traffic, CPU, memory, disk. A reader
  // arriving from a fleet row meets the same subjects in the same sequence.
  it("leads the left column with the fleet row's own order at two columns", () => {
    const columns = columnsOf(1200);
    expect(columns.length).toBe(2);
    expect(columns[0]!.slice(0, 2)).toEqual(["Traffic", "Processor"]);
    expect(columns[0]).toContain("Disk");
    expect(columns[1]![0]).toBe("CPU time breakdown, not collected");
    // A subject is in ONE column, whatever the host carries.
    expect(columns[1]).not.toContain("Traffic");
  });

  it("moves disk to the middle column at three, leaving the first to CPU and memory", () => {
    const columns = columnsOf(1600);
    expect(columns.length).toBe(3);
    expect(columns[0]!.slice(0, 2)).toEqual(["Traffic", "Processor"]);
    expect(columns[0]).not.toContain("Disk");
    expect(columns[1]![0]).toBe("Disk");
  });

  // A missing matchMedia must mean the single column rather than a thrown
  // render -- the same rule FleetPage's useIsNarrow follows.
  it("falls back to one column when the browser has no matchMedia", () => {
    expect(columnsOf(null).length).toBe(1);
  });
});
