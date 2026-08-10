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
    it("reports a materialization-lag trailing clamp at a rolled-up tier", () => {
      const laggy = {
        ...fiveMin,
        window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T13:50:00Z" },
        requested_window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T14:00:00Z" },
      };
      expect(windowNotice(laggy as never)).toMatch(/materializ|available|fresh/i);
    });

    // internal/hub/read/tier.go:165-171 clamps `to` down to `now` whenever the
    // requested window extends into the future, and this clamp fires at EVERY
    // tier -- including raw, which has lag: 0 and no materialization step at
    // all (tier.go:41). A raw-tier future-`to` clamp must never be described
    // with materialization language: that mechanism does not exist at raw.
    it("does not claim materialization for a raw-tier future-to clamp", () => {
      const futureClamped = {
        ...raw,
        window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T14:00:00Z" },
        requested_window: { from: "2026-08-09T14:00:00Z", to: "2026-08-11T00:00:00Z" },
      };
      const notice = windowNotice(futureClamped as never);
      expect(notice).not.toBeNull();
      expect(notice).not.toMatch(/materializ/i);
    });

    // The server already knows exactly which clamp fired and says so with
    // real numbers (tier.go's Warnings). The client must surface that
    // verbatim rather than re-deriving a generic sentence that could get the
    // mechanism wrong, as the raw-tier case above shows.
    it("surfaces server warnings verbatim instead of re-deriving them", () => {
      const serverWarned = {
        ...raw,
        window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T14:00:00Z" },
        requested_window: { from: "2026-08-01T14:00:00Z", to: "2026-08-11T00:00:00Z" },
        warnings: [
          "from predates the raw tier's 7 days retention; the window starts at the oldest data that still exists",
          "to was in the future and was clamped to now",
        ],
      };
      const notice = windowNotice(serverWarned as never);
      expect(notice).toContain(
        "from predates the raw tier's 7 days retention; the window starts at the oldest data that still exists",
      );
      expect(notice).toContain("to was in the future and was clamped to now");
    });

    // Truncation is a point-limit cut -- exactly the "chart is silently
    // wrong" failure this module exists to catch -- and must never be
    // dropped from the notice, independent of whether the window also moved.
    it("reports a truncated result even when the window matches exactly", () => {
      const truncated = { ...raw, truncated: true };
      expect(windowNotice(truncated as never)).toMatch(/truncat/i);
    });

    it("combines a passed-through server warning with the truncated notice", () => {
      const both = {
        ...raw,
        window: { from: "2026-08-08T14:00:00Z", to: "2026-08-10T14:00:00Z" },
        requested_window: { from: "2026-05-12T14:00:00Z", to: "2026-08-10T14:00:00Z" },
        warnings: ["from predates the raw tier's 7 days retention; the window starts at the oldest data that still exists"],
        truncated: true,
      };
      const notice = windowNotice(both as never);
      expect(notice).toMatch(/retention/i);
      expect(notice).toMatch(/truncat/i);
    });
  });
});
