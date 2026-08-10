import { describe, expect, it } from "vitest";
import { column, hasGaps, seriesValues, windowNotice } from "./metrics";

const raw = {
  family: "cpu_core",
  tier: "raw",
  step_s: 60,
  window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T14:00:00Z" },
  requested_window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T14:00:00Z" },
  warnings: [],
  key_columns: ["core"],
  columns: ["busy"],
  series: [{ key: { core: "0" }, points: [[1, 4.1], [2, null], [3, 5.0]] }],
  truncated: false,
} as const;

const fiveMin = { ...raw, tier: "5m", step_s: 300, columns: ["busy_avg", "busy_max"] } as const;

describe("metrics", () => {
  // Column names differ per tier BY CONSTRUCTION -- that is the guarantee
  // that a client cannot silently read an average as a raw value.
  it("resolves a base column name per tier", () => {
    expect(column(raw as never, "busy")).toBe(0);
    expect(column(fiveMin as never, "busy")).toBe(0); // busy_avg
  });

  it("throws a named error when the tier has no such column", () => {
    expect(() => column(raw as never, "nonexistent")).toThrowError(/nonexistent/);
  });

  // A null inside the window means the host reported nothing. It must survive
  // to the chart, which breaks the line. Coercing it to 0 or dropping the
  // point both turn "agent was down" into a measurement.
  it("preserves nulls rather than coercing them", () => {
    expect(seriesValues(raw as never, 0, "busy")).toEqual([4.1, null, 5.0]);
  });

  it("reports when the served window is shorter than the requested one", () => {
    const clamped = {
      ...raw,
      window: { from: "2026-08-08T14:00:00Z", to: "2026-08-10T14:00:00Z" },
      requested_window: { from: "2026-05-12T14:00:00Z", to: "2026-08-10T14:00:00Z" },
    };
    expect(windowNotice(clamped as never)).toMatch(/retention|available/i);
    expect(windowNotice(raw as never)).toBeNull();
  });

  describe("hasGaps", () => {
    it("is false when every value is a number", () => {
      expect(hasGaps([1, 2, 3])).toBe(false);
    });

    it("is true when any value is null", () => {
      expect(hasGaps([1, null, 3])).toBe(true);
    });

    it("is false for an empty series", () => {
      expect(hasGaps([])).toBe(false);
    });
  });

  describe("column resolution order", () => {
    it("tries the exact base name before the _avg/_max suffixed forms", () => {
      const both = { ...raw, columns: ["busy", "busy_avg"] } as const;
      expect(column(both as never, "busy")).toBe(0);
    });

    it("falls back to _max when only that suffix exists", () => {
      const maxOnly = { ...raw, tier: "1h", columns: ["busy_max"] } as const;
      expect(column(maxOnly as never, "busy")).toBe(0);
    });

    it("lists the available columns in the thrown error", () => {
      expect(() => column(fiveMin as never, "nonexistent")).toThrowError(
        /busy_avg.*busy_max|busy_max.*busy_avg/s,
      );
    });
  });

  describe("windowNotice edge differences", () => {
    it("reports a materialization-lag trailing clamp", () => {
      const laggy = {
        ...raw,
        window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T13:00:00Z" },
        requested_window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T14:00:00Z" },
      };
      expect(windowNotice(laggy as never)).toMatch(/materializ|available|fresh/i);
    });
  });
});
