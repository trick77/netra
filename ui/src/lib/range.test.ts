import { describe, expect, it } from "vitest";
import { clampRange, isRange, RANGES, rangeMs, rangeWindow } from "./range";

// The real sets, so these tests break if a page changes what it offers.
const FLEET = ["1h", "6h", "24h"] as const;
const HOST = ["1h", "6h", "24h", "7d"] as const;
const EVENTS = ["1h", "24h", "7d", "30d"] as const;

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

  describe("clampRange", () => {
    it("leaves a range the page offers alone", () => {
      for (const range of FLEET) expect(clampRange(range, FLEET)).toBe(range);
      expect(clampRange("30d", RANGES)).toBe("30d");
    });

    // Widening, not narrowing. The events page skips 6h precisely because an
    // hour of events is usually empty; sending a remembered 6h down to 1h
    // would land on the emptiness that omission exists to avoid.
    it("widens to the narrowest offered range that is still wide enough", () => {
      expect(clampRange("6h", EVENTS)).toBe("24h");
      expect(clampRange("30d", EVENTS)).toBe("30d");
    });

    // Nothing offered is as wide, so the widest there is -- the fleet stops
    // at 24h and a remembered 7d has to show as something.
    it("falls back to the widest offered when the ask is wider than all", () => {
      expect(clampRange("7d", FLEET)).toBe("24h");
      expect(clampRange("30d", FLEET)).toBe("24h");
      expect(clampRange("30d", HOST)).toBe("7d");
    });

    // The whole point: a value outside the options renders every Segmented
    // button unpressed, which reads as "no range selected".
    it("always answers with something the page can show as pressed", () => {
      for (const offered of [FLEET, HOST, EVENTS]) {
        for (const range of RANGES) {
          expect(offered).toContain(clampRange(range, offered));
        }
      }
    });
  });
});
