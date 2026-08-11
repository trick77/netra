import { describe, expect, it } from "vitest";
import { hostStatus, isReporting } from "./host";

const now = new Date("2026-08-11T12:00:00Z");
const ago = (seconds: number) =>
  new Date(now.getTime() - seconds * 1000).toISOString();

describe("hostStatus", () => {
  it("is online inside three scrape intervals", () => {
    expect(hostStatus({ last_seen: ago(179) }, now)).toEqual({
      severity: "ok",
      label: "online",
    });
  });

  // 3x the 60s scrape interval, matching the alerting rule the spec states.
  // Anything else here would let the fleet list call a host down while the
  // engine still considers it up.
  it("is offline past three scrape intervals", () => {
    expect(hostStatus({ last_seen: ago(181) }, now).severity).toBe("critical");
  });

  // Never seen is a different sentence from gone quiet -- right after
  // creation it is the expected state -- but it is just as absent.
  it("distinguishes never seen from offline, without calling it healthy", () => {
    const status = hostStatus({ last_seen: null }, now);

    expect(status.label).toBe("never seen");
    expect(status.severity).toBe("critical");
  });

  // An unparseable timestamp yields NaN, and NaN > threshold is false: the
  // host would have read as online on the strength of a value nobody could
  // read.
  it("does not call a host with an unreadable timestamp online", () => {
    expect(hostStatus({ last_seen: "not a date" }, now).severity).toBe(
      "critical",
    );
  });

  it("answers the reporting tile's question with the same rule", () => {
    expect(isReporting({ last_seen: ago(10) }, now)).toBe(true);
    expect(isReporting({ last_seen: ago(600) }, now)).toBe(false);
  });
});
