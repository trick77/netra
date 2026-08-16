// The chart page asks the read API for the family a spec actually lives in.
//
// This is the test that was missing when the page shipped. ChartPage put
// `spec.source` -- a UI key -- straight into the request, and three of the
// eight keys are not family names: hostSnmp/host_snmp, diskIo/disk_io,
// cpuCore/cpu_core. The hub 400s on an unknown family, so seven of the
// twenty-six slugs rendered "Could not load this chart." and nothing caught
// it, because the only test touching this path mocked getMetrics to resolve
// for ANY string and never looked at what it was asked for.
//
// So: assert the family, per source, by name.
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { ChartPage } from "./ChartPage";
import { ALL_SPECS, FAMILIES, familyFor } from "./chartSpecs";
import * as api from "../../lib/api";

vi.mock("../../lib/api", async () => {
  const actual = await vi.importActual<typeof api>("../../lib/api");
  return { ...actual, getMetrics: vi.fn() };
});

function metrics(family: string): api.MetricsResponse {
  return {
    family,
    tier: "raw",
    step_s: 60,
    window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T01:00:00Z" },
    requested_window: {
      from: "2025-08-10T00:00:00Z",
      to: "2025-08-10T01:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: [],
    series: [],
    truncated: false,
  } as unknown as api.MetricsResponse;
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.getMetrics).mockImplementation((_id, params) =>
    Promise.resolve(metrics(params.family)),
  );
});

function renderChart(slug: string) {
  return render(
    <ChartPage
      hostId="7"
      slug={slug}
      range="1h"
      onRangeChange={() => {}}
      onBack={() => {}}
    />,
  );
}

describe("the family the chart page requests", () => {
  // The three that differ. Named individually rather than only swept below,
  // so a regression names the chart it broke.
  it.each([
    ["cpu-cores", "cpu_core"],
    ["disk-throughput", "disk_io"],
    ["ip-statistics", "host_snmp"],
  ])("%s asks for family=%s", async (slug, family) => {
    // When
    renderChart(slug);

    // Then
    await waitFor(() => expect(api.getMetrics).toHaveBeenCalled());
    const asked = vi
      .mocked(api.getMetrics)
      .mock.calls.map((call) => call[1].family);
    expect(asked).toContain(family);
    // And never the UI key, which is what the hub rejects.
    expect(asked).not.toContain("cpuCore");
    expect(asked).not.toContain("diskIo");
    expect(asked).not.toContain("hostSnmp");
  });

  // The registry, spelled out once, by hand. The sweeps below import
  // FAMILIES, so they cannot catch a name that is wrong in chartSpecs itself
  // -- a typo there is a typo they would assert against. This is the second
  // reading, and the only place the list is written independently of the
  // code under test (internal/hub/read/family.go).
  it("FAMILIES is the hub's registry", () => {
    expect([...FAMILIES]).toEqual([
      "host",
      "host_snmp",
      "net",
      "disk_io",
      "filesystem",
      "collector",
      "cpu_core",
      "agent",
      "container",
      "sensor",
      "smart",
      "process",
    ]);
  });

  it("every spec resolves to a family the hub serves", () => {
    // Given all specs, When each is mapped, Then the name is a real family.
    for (const spec of ALL_SPECS) {
      expect(FAMILIES).toContain(familyFor(spec));
    }
  });

  it("asks the hub only for names it serves, for every slug", async () => {
    for (const spec of ALL_SPECS) {
      // Given
      vi.clearAllMocks();
      vi.mocked(api.getMetrics).mockImplementation((_id, params) =>
        Promise.resolve(metrics(params.family)),
      );

      // When
      const view = renderChart(spec.slug);
      await waitFor(() => expect(api.getMetrics).toHaveBeenCalled());

      // Then
      for (const call of vi.mocked(api.getMetrics).mock.calls) {
        expect(FAMILIES).toContain(call[1].family);
      }
      view.unmount();
    }
  });
});
