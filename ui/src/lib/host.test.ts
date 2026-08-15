import { describe, expect, it } from "vitest";
import { hostStatus, isReporting, reportsSporadically } from "./host";

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

describe("reportsSporadically", () => {
  const nulls = (n: number) => Array<number | null>(n).fill(null);

  it("calls a host that keeps dropping scrapes sporadic", () => {
    // Six of twenty missing inside the span it was reporting across.
    const values = Array.from({ length: 20 }, (_, i) =>
      i % 3 === 0 ? null : 10,
    );

    expect(reportsSporadically(values)).toBe(true);
  });

  // Every tier materialises behind now, so the newest buckets are empty for
  // every host on the page, healthy or not.
  it("ignores the trailing buckets no tier has written yet", () => {
    expect(reportsSporadically([...Array(20).fill(10), ...nulls(10)])).toBe(
      false,
    );
  });

  // The reported bug: an agent started five minutes ago against a 24h/5m
  // grid is 283 empty buckets and one real one, and it was reading as 99%
  // missed. The emptiness in front of a host is not scrapes it failed to
  // send.
  it("does not call a just-added host sporadic for the time before it existed", () => {
    expect(reportsSporadically([...nulls(283), 12])).toBe(false);
    expect(reportsSporadically([...nulls(282), 11, 12])).toBe(false);
  });

  it("judges a host that appeared mid-window on the half it was there for", () => {
    const clean = [...nulls(144), ...Array(144).fill(10)];

    expect(reportsSporadically(clean)).toBe(false);
  });

  // The leading trim must not blunt real detection: gaps AFTER the host
  // started reporting are still gaps.
  it("still catches a mid-window host that misses scrapes once it is up", () => {
    const gappy = [
      ...nulls(144),
      ...Array.from({ length: 144 }, (_, i) => (i % 4 === 0 ? null : 10)),
    ];

    expect(reportsSporadically(gappy)).toBe(true);
  });

  it("counts neither edge when a clean run sits between them", () => {
    expect(
      reportsSporadically([...nulls(50), ...Array(30).fill(10), ...nulls(8)]),
    ).toBe(false);
  });

  // The minimum is measured over the span the host reported across, not
  // over the grid -- which is exactly what let the whole-grid span slip past
  // it before.
  it("declines to judge a span too short to tell a gap from a start", () => {
    expect(reportsSporadically([...nulls(200), 10, null, 12, null])).toBe(
      false,
    );
    expect(reportsSporadically([])).toBe(false);
    expect(reportsSporadically(nulls(288))).toBe(false);
  });
});
