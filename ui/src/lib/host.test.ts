import { describe, expect, it } from "vitest";
import {
  currentFilesystems,
  hostStatus,
  isReporting,
  reportsSporadically,
} from "./host";
import type { Filesystem } from "./api";

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

  // At the shortest span the rule will judge, a single missing bucket is
  // exactly the 0.2 ratio -- so a host added minutes ago that dropped one
  // scrape while its agent settled would have been badged on one bucket.
  it("does not call a single missed bucket a pattern", () => {
    expect(reportsSporadically([...nulls(283), 10, null, 10, 10, 12])).toBe(
      false,
    );
    expect(reportsSporadically([...nulls(283), 10, null, 10, null, 12])).toBe(
      true,
    );
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

describe("currentFilesystems", () => {
  const mount = (label: string, ts: string | null): Filesystem => ({
    id: 1,
    label,
    mountpoint: `/mnt/${label}`,
    device_id: null,
    ts,
    total: 100,
    used: 90,
    free: 10,
  });

  // The bug this rule exists for. `filesystems` is never pruned, so a mount
  // the agent has stopped naming keeps its row and its last bytes forever --
  // and the fleet's Disk cell picks the FULLEST mount, so one frozen at 94 %
  // does not merely linger, it wins the cell.
  it("drops a mount that stopped reporting while its host kept talking", () => {
    const kept = currentFilesystems({
      last_seen: ago(30),
      filesystems: [mount("live", ago(30)), mount("retired", ago(86_400))],
    });

    expect(kept?.map((fs) => fs.label)).toEqual(["live"]);
  });

  // The other half, and the whole point of the gauge: a machine switched off
  // for the weekend has not retired anything. Every mount's reading is simply
  // the last one anybody took, which for a disk is still true.
  it("keeps every mount when the whole host is silent", () => {
    const kept = currentFilesystems({
      last_seen: ago(3 * 86_400),
      filesystems: [
        mount("pool", ago(3 * 86_400)),
        mount("root", ago(3 * 86_400 + 30)),
      ],
    });

    expect(kept?.map((fs) => fs.label)).toEqual(["pool", "root"]);
  });

  // Against the HOST'S OWN last_seen, never the wall clock: an agent with a
  // skewed clock must not lose its inventory to a fact about its NTP config.
  it("dates a mount against its host rather than against now", () => {
    const kept = currentFilesystems({
      // Both stamps are hours ahead of the test clock, and consistent with
      // each other.
      last_seen: ago(0 - 7200),
      filesystems: [mount("pool", ago(0 - 7200))],
    });

    expect(kept?.map((fs) => fs.label)).toEqual(["pool"]);
  });

  // driveIsCurrent's rule, for the same reason: with no reference point the
  // honest answer is the reading netra holds.
  it("keeps a mount whose reading has no timestamp", () => {
    expect(
      currentFilesystems({
        last_seen: ago(30),
        filesystems: [mount("pool", null)],
      })?.map((fs) => fs.label),
    ).toEqual(["pool"]);

    expect(
      currentFilesystems({
        last_seen: null,
        filesystems: [mount("pool", ago(1))],
      })?.map((fs) => fs.label),
    ).toEqual(["pool"]);
  });

  // null and [] are different facts. null is an older hub that does not send
  // the gauge, and every caller falls back to its window-derived reading; []
  // is a host that has been asked and reports no mounts.
  it("tells an unasked host from one with no mounts", () => {
    expect(currentFilesystems({ last_seen: ago(30) })).toBeNull();
    expect(currentFilesystems({ last_seen: ago(30), filesystems: [] })).toEqual(
      [],
    );
  });
});
