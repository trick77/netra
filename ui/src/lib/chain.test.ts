// The wave-level review that produced this test found that each module --
// api.ts, metrics.ts, format.ts, geometry.ts -- was sound on its own, but no
// test drove a null all the way from the wire shape through to a rendered
// chart's gap. This is that test: a fixture shaped like a real
// MetricsResponse (correct epoch-millis timestamps, one null sample),
// pushed through seriesValues() -> extent() -> linePath(), asserting the
// null survives as a subpath break rather than being coerced to 0 or
// dropped anywhere along the chain.
import { describe, expect, it } from "vitest";
import type { MetricsResponse } from "./api";
import { seriesTimestamps, seriesValues } from "./metrics";
import { extent, linePath } from "../ui/charts/geometry";

// Real internal/hub/read/metrics.go:198 emits point[0] as ts.UnixMilli().
// These are five one-minute steps starting at a fixed instant.
const T0 = 1_754_755_200_000; // 2025-08-09T14:00:00Z in epoch millis
const STEP_MS = 60_000;

const response: MetricsResponse = {
  family: "cpu_core",
  tier: "raw",
  step_s: 60,
  window: { from: "2025-08-09T14:00:00Z", to: "2025-08-09T14:05:00Z" },
  requested_window: {
    from: "2025-08-09T14:00:00Z",
    to: "2025-08-09T14:05:00Z",
  },
  warnings: [],
  key_columns: ["core"],
  columns: ["busy"],
  series: [
    {
      key: { core: "0" },
      points: [
        [T0 + 0 * STEP_MS, 12.5],
        [T0 + 1 * STEP_MS, 18.0],
        [T0 + 2 * STEP_MS, null], // the host reported nothing at this tick
        [T0 + 3 * STEP_MS, 41.2],
        [T0 + 4 * STEP_MS, 39.9],
      ],
    },
  ],
  truncated: false,
};

describe("chain: wire -> seriesValues -> extent -> linePath", () => {
  it("carries a null from the response all the way to a subpath break", () => {
    const values = seriesValues(response, 0, "busy");
    expect(values).toEqual([12.5, 18.0, null, 41.2, 39.9]);

    const { min, max } = extent(values);
    // The null must not have coerced to 0 and dragged the floor down.
    expect(min).toBe(12.5);
    expect(max).toBe(41.2);

    const { paths, points } = linePath(values, 200, 60, min, max);
    // One run before the gap, one after -- the null split the line rather
    // than being bridged across.
    expect(paths).toHaveLength(2);
    expect(points).toHaveLength(0);
  });

  it("carries real epoch-millisecond timestamps alongside the values, in step", () => {
    const timestamps = seriesTimestamps(response, 0);
    expect(timestamps).toEqual([
      T0,
      T0 + STEP_MS,
      T0 + 2 * STEP_MS,
      T0 + 3 * STEP_MS,
      T0 + 4 * STEP_MS,
    ]);
    // The timestamp at the null value's index still exists -- the x-axis
    // point survives even though the y-value doesn't.
    const values = seriesValues(response, 0, "busy");
    expect(values[2]).toBeNull();
    expect(timestamps[2]).toBe(T0 + 2 * STEP_MS);
  });
});
