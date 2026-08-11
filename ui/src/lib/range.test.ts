import { describe, expect, it } from "vitest";
import { isRange, RANGES, rangeMs, rangeWindow } from "./range";

describe("range", () => {
  // The hub rejects relative times outright: `from=-24h` answers "from must
  // be RFC 3339 or unix milliseconds". Every page used to convert this
  // itself, and one of them getting it wrong is a 400 nobody sees until a
  // chart is empty.
  it("resolves a range into the absolute window the hub demands", () => {
    const now = new Date("2026-08-11T12:00:00.000Z");

    expect(rangeWindow("24h", now)).toEqual({
      from: "2026-08-10T12:00:00.000Z",
      to: "2026-08-11T12:00:00.000Z",
      step: "5m",
    });
  });

  it("carries a step for every range, so no caller has to invent one", () => {
    const now = new Date("2026-08-11T12:00:00.000Z");

    for (const range of RANGES) {
      expect(rangeWindow(range, now).step).toMatch(/^\d+(s|m|h)$/);
    }
  });

  // A range arrives from localStorage and from the URL, both of which a user
  // can edit. An unknown one must be rejected rather than indexed into the
  // spec table, where it reads as undefined and produces an Invalid Date.
  it("rejects a value that is not a range", () => {
    expect(isRange("24h")).toBe(true);
    expect(isRange("48h")).toBe(false);
    expect(isRange(null)).toBe(false);
  });

  it("spans what it says it spans", () => {
    expect(rangeMs("1h")).toBe(3_600_000);
    expect(rangeMs("30d")).toBe(30 * 24 * 3_600_000);
  });
});
