// The dark-launch gate, browser half.
//
// One fixture, test/conditions/equality.json, read by this file and by
// internal/hub/store/equality_integration_test.go. Each side seeds itself from
// the same description and asserts the same verdicts, so a disagreement shows
// up as one of them going red rather than as a comparison nobody runs -- and
// there is no cross-language runner to keep working.
//
// This is the load-bearing test in the whole change. It was written while the
// browser still derived its own conditions and asserted the LEGACY answer; it
// asserts the renderer's now, which is what keeps it earning its place: the
// verdicts in the fixture are the ones the browser produced before the switch,
// so any drift in what the page says is a failure here.
//
// What it deliberately does not cover, and why, is written in the fixture:
// sporadic (a rate that does not survive a tick boundary), never-seen hosts
// (the hub refuses to judge them by design), stale subjects (the hub keeps
// them, the old browser dropped them at 180 s).
import { describe, expect, it } from "vitest";
import { catalogueOf, fleetConditions } from "./conditions";
import type { ConditionKindInfo, ConditionRow } from "../../lib/api";
// The SAME file the hub's integration test seeds itself from. Imported rather
// than read off the filesystem: this suite carries no node types, vite resolves
// JSON natively, and two copies of a fixture that has to describe one fleet
// would be exactly the disagreement this gate exists to catch.
import fixtureJson from "../../../../test/conditions/equality.json";

type Fixture = {
  hosts: {
    hostname: string;
    last_seen_offset_s: number | null;
    services_failed: number | null;
    units: { name: string; state: string; state_ts_offset_s: number }[];
    filesystems: {
      label: string;
      mountpoint: string;
      samples: { offset_s: number; used: number; free: number }[];
    }[];
  }[];
  expect: {
    hostname: string;
    kind: string;
    subject: string;
    severity: string;
    since_offset_s: number | null;
    since_at_least: boolean;
  }[];
};

const fixture = fixtureJson as unknown as Fixture;

// The same catalogue internal/hub/conditions/catalogue.go serves.
const KINDS: ConditionKindInfo[] = [
  { kind: "silent", label: "Stopped reporting", severity: "critical" },
  { kind: "sporadic", label: "Reporting sporadically", severity: "warning" },
  { kind: "failed-units", label: "Failed units", severity: "warning" },
  {
    kind: "disk",
    label: "Filesystem nearly full",
    severity: "warning",
    thresholds: {
      warn_pct: 90,
      crit_pct: 95,
      warn_free: 100 * 1024 ** 3,
      crit_free: 20 * 1024 ** 3,
    },
  },
  { kind: "drive", label: "Drive errors", severity: "critical" },
];

const NOW = new Date("2026-08-12T12:00:00Z");
const at = (offsetS: number) =>
  new Date(NOW.getTime() + offsetS * 1000).toISOString();

/**
 * The rows the hub would serve for this fixture.
 *
 * Built here rather than fetched, because this half is testing the RENDERER:
 * that the hub's verdicts reach the page as the sentences and severities the
 * fixture names. The Go half is what proves the hub produces these rows from
 * the same fleet -- the two together are the equality.
 */
function conditionRows(): ConditionRow[] {
  const rows: ConditionRow[] = [];
  let id = 0;
  fixture.hosts.forEach((host, index) => {
    const hostId = index + 1;
    for (const want of fixture.expect) {
      if (want.hostname !== host.hostname) continue;
      const detail: Record<string, unknown> = {};
      if (want.kind === "disk") {
        const fs = host.filesystems.find((one) => one.label === want.subject)!;
        const newest = fs.samples[fs.samples.length - 1]!;
        detail.pct = (newest.used / (newest.used + newest.free)) * 100;
        detail.mount = fs.mountpoint;
        detail.free = newest.free;
      }
      if (want.kind === "failed-units") {
        detail.count = host.services_failed;
        detail.units = host.units
          .filter((one) => one.state === "failed")
          .map((one) => one.name)
          .sort()
          .slice(0, 3);
      }
      if (want.kind === "silent") {
        detail.last_seen = at(host.last_seen_offset_s!);
      }
      rows.push({
        id: ++id,
        host_id: hostId,
        hostname: host.hostname,
        kind: want.kind,
        subject: want.subject,
        severity: want.severity as "warning" | "critical",
        opened_ts: at(want.since_offset_s ?? 0),
        opened_at_least: want.since_at_least,
        detail,
        measured_ts: null,
        stale: false,
      });
    }
  });
  return rows;
}

describe("the shared condition fixture", () => {
  it("puts every expected verdict on the page, and nothing else", () => {
    const hosts = fixture.hosts.map((host, index) => ({
      id: index + 1,
      hostname: host.hostname,
      last_seen:
        host.last_seen_offset_s === null ? null : at(host.last_seen_offset_s),
    }));

    const conditions = fleetConditions(
      conditionRows(),
      hosts,
      catalogueOf(KINDS),
      NOW,
    );

    const got = conditions
      .map((c) => `${c.hostname} ${c.kind} ${c.severity} ${c.since ?? "none"}`)
      .sort();
    const want = fixture.expect
      .map(
        (e) =>
          `${e.hostname} ${e.kind} ${e.severity} ${
            e.since_offset_s === null ? "none" : at(e.since_offset_s)
          }`,
      )
      .sort();

    expect(got).toEqual(want);
  });

  // The onset is the visible payoff, so it is asserted as an instant rather
  // than folded into the string above alone: a disk that filled at 09:00 says
  // 09:00, not "somewhere in the last three hours".
  it("dates the disk from the sample it crossed on", () => {
    const conditions = fleetConditions(
      conditionRows(),
      fixture.hosts.map((host, index) => ({
        id: index + 1,
        hostname: host.hostname,
        last_seen:
          host.last_seen_offset_s === null ? null : at(host.last_seen_offset_s),
      })),
      catalogueOf(KINDS),
      NOW,
    );
    const disk = conditions.find((c) => c.kind === "disk")!;
    const want = fixture.expect.find((e) => e.kind === "disk")!;
    expect(disk.since).toBe(at(want.since_offset_s!));
    expect(disk.sinceAtLeast).toBe(want.since_at_least);
  });

  // The fixture must keep describing a fleet with something wrong AND
  // something healthy in it. A gate that went vacuous -- an empty `expect`, or
  // every host broken -- would pass forever without proving anything.
  it("describes a fleet worth checking", () => {
    expect(fixture.expect.length).toBeGreaterThan(0);
    expect(fixture.hosts.length).toBeGreaterThan(fixture.expect.length - 1);
    const troubled = new Set(fixture.expect.map((e) => e.hostname));
    expect(fixture.hosts.some((host) => !troubled.has(host.hostname))).toBe(
      true,
    );
  });
});
