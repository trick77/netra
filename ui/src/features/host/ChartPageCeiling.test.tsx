// A memory chart with no mem_total draws nothing, and says so.
//
// The stack has to be scaled against the host's total memory. Scaled against
// its own running total instead -- which is what ChartPage's auto-scale does
// for any stacked spec with no fixed max -- every host draws as nearly full
// whatever its headroom, which is the single reading these charts exist to
// avoid. The fleet cell has always refused this case outright (hostColumns:
// `if (row.mem_total === null) return ABSENT`), and the page reached it by a
// different route: the Graphs tab renders host-memory for every host, total
// or no total.
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { ChartPage } from "./ChartPage";
import * as api from "../../lib/api";

vi.mock("../../lib/api", async () => {
  const actual = await vi.importActual<typeof api>("../../lib/api");
  return { ...actual, getMetrics: vi.fn() };
});

function hostFamily(columns: string[], point: number[]): api.MetricsResponse {
  const at = Date.parse("2026-08-10T00:00:00Z");
  return {
    family: "host",
    tier: "raw",
    step_s: 60,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:03:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T00:03:00Z",
    },
    warnings: [],
    key_columns: [],
    columns,
    series: [{ key: {}, points: [[at, ...point]] }],
    truncated: false,
  } as unknown as api.MetricsResponse;
}

beforeEach(() => {
  vi.clearAllMocks();
});

function renderMemory(res: api.MetricsResponse) {
  vi.mocked(api.getMetrics).mockResolvedValue(res);
  return render(
    <ChartPage
      hostId="7"
      slug="host-memory"
      range="1h"
      onRangeChange={() => {}}
      onBack={() => {}}
      backLabel="Back to fleet"
    />,
  );
}

describe("a chart whose ceiling the host never reported", () => {
  it("says there is no scale rather than inventing one", async () => {
    // Given a host reporting memory use but no total
    renderMemory(hostFamily(["mem_used"], [1_000_000]));

    // Then the page says so, and draws no chart at all -- an auto-scaled
    // stack here would read as a host nearly out of memory.
    await waitFor(() =>
      expect(
        screen.getByText(/no memory ceiling in this window/i),
      ).toBeVisible(),
    );
    expect(screen.queryByRole("img", { name: /Memory over time/ })).toBeNull();
  });

  it("draws normally once the total is there", async () => {
    // Given the same host with mem_total
    renderMemory(
      hostFamily(
        ["mem_used", "mem_free", "mem_total"],
        [1_000_000, 3_000_000, 4_000_000],
      ),
    );

    // Then the chart is drawn, against that ceiling
    await waitFor(() =>
      expect(
        screen.getByRole("img", { name: /Memory over time/ }),
      ).toBeVisible(),
    );
    expect(screen.queryByText(/no memory ceiling/i)).toBeNull();
  });
});
