import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import type { MetricsResponse } from "../../../lib/api";
import { Graphs, UNAVAILABLE } from "./Graphs";

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

// A tier that carries uptime but none of the load columns -- the case that
// makes lib/metrics.ts's column() throw mid-render if it is called blind.
const sparseHost = response({
  family: "host",
  columns: ["uptime_s"],
  series: [
    {
      key: {},
      points: [
        [1_754_784_000_000, 3600],
        [1_754_784_060_000, 3660],
      ],
    },
  ],
});

const fullHost = response({
  family: "host",
  columns: ["uptime_s", "load1", "load5", "load15", "ctxt_per_s"],
  series: [
    {
      key: {},
      points: [
        [1_754_784_000_000, 3600, 0.5, 0.4, 0.3, 1200],
        [1_754_784_060_000, 3660, null, 0.4, 0.3, 1300],
      ],
    },
  ],
});

describe("Graphs", () => {
  it("groups the small multiples System / Network / Storage", () => {
    render(<Graphs host={fullHost} />);
    for (const group of ["System", "Network", "Storage"]) {
      expect(screen.getByRole("heading", { name: group })).toBeInTheDocument();
    }
  });

  it("renders the three families with no data behind them as explicitly not collected, with the reason", () => {
    render(<Graphs host={fullHost} />);
    for (const [title, reason] of Object.entries(UNAVAILABLE)) {
      const panel = screen.getByRole("region", {
        name: `${title}, not collected`,
      });
      expect(panel).toHaveTextContent("Not collected");
      expect(panel).toHaveTextContent(reason);
    }
    // Exactly those three -- an unavailable panel is a schema statement,
    // not a loading state.
    expect(screen.getAllByText("Not collected")).toHaveLength(3);
  });

  it("draws every panel at the same size", () => {
    const { container } = render(<Graphs host={fullHost} />);
    const sizes = new Set(
      Array.from(container.querySelectorAll("svg.spark")).map(
        (svg) => `${svg.getAttribute("width")}x${svg.getAttribute("height")}`,
      ),
    );
    expect(sizes.size).toBe(1);
  });

  it("survives a tier that does not carry a panel's columns", () => {
    render(<Graphs host={sparseHost} />);
    const panel = screen.getByRole("region", { name: /Load averages chart/ });
    expect(panel).toHaveTextContent("—");
  });

  it("plots the collector family's boolean ok column instead of throwing on it", () => {
    // family=collector yields `ok` as a boolean and `error_code` as a
    // string beside a numeric column; seriesValues() throws on both, and
    // that throw would happen during render with no boundary under it.
    const collector = response({
      family: "collector",
      key_columns: ["collector"],
      columns: ["duration_ms", "ok", "error_code"],
      series: [
        {
          key: { collector: "smart" },
          points: [
            [1_754_784_000_000, 4, true, null],
            [1_754_784_060_000, 4, false, "eacces"],
          ],
        },
      ],
    });
    render(<Graphs host={fullHost} collector={collector} />);
    const panel = screen.getByRole("region", {
      name: /Device availability chart/,
    });
    expect(panel).toHaveTextContent("down");
  });

  it("carries no range control of its own — the header's drives every panel", () => {
    render(<Graphs host={fullHost} />);
    expect(screen.queryByRole("group")).toBeNull();
  });

  it("keeps one panel per metric when a family has many series", () => {
    const diskIo = response({
      family: "disk_io",
      key_columns: ["device"],
      columns: [
        "read_bytes",
        "write_bytes",
        "io_util_pct",
        "r_await_ms",
        "w_await_ms",
      ],
      series: [
        {
          key: { device: "sda" },
          points: [[1_754_784_000_000, 1000, 2000, 4, 1, 2]],
        },
        {
          key: { device: "sdb" },
          points: [[1_754_784_000_000, 10, 20, 1, 3, 4]],
        },
      ],
    });
    render(<Graphs host={fullHost} diskIo={diskIo} />);
    expect(
      screen.getAllByRole("region", { name: /Disk throughput chart/ }),
    ).toHaveLength(1);
    // One band per device, named by device, inside that single panel.
    expect(screen.getByText("sda read")).toBeInTheDocument();
    expect(screen.getByText("sdb write")).toBeInTheDocument();
  });
});
