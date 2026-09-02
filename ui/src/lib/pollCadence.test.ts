import { describe, expect, it } from "vitest";
import { POLL_MAX_MS, POLL_MS, pollMsFor } from "./poll";
import { rangeStepMs } from "./range";

// The cadence a RANGED fetch runs at. The host record is deliberately not in
// here: "last seen" and the reporting badge are claims about right now, and
// stay on POLL_MS whatever window the charts beside them are drawn over.
//
// Its own file rather than poll.test.tsx, which renders components with fake
// timers -- this is arithmetic over two constants and needs neither.
describe("pollMsFor", () => {
  it("never runs faster than the agent reports", () => {
    expect(pollMsFor(1_000)).toBe(POLL_MS);
    expect(pollMsFor(rangeStepMs("1h"))).toBe(POLL_MS);
  });

  it("follows the bucket width between the floor and the ceiling", () => {
    expect(pollMsFor(rangeStepMs("24h"))).toBe(5 * 60_000);
  });

  // The argument for slowing down is waste, not freshness: an hourly bucket
  // does not license an hour of staleness.
  it("caps a coarse range rather than matching its bucket", () => {
    expect(rangeStepMs("7d")).toBe(3_600_000);
    expect(pollMsFor(rangeStepMs("7d"))).toBe(POLL_MAX_MS);
    expect(pollMsFor(rangeStepMs("12mo"))).toBe(POLL_MAX_MS);
  });
});
