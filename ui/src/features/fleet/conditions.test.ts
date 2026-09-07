import { describe, expect, it } from "vitest";
import {
  catalogueOf,
  diskSeverityFor,
  diskThresholds,
  EMPTY_CATALOGUE,
  failedUnitsShown,
  filterKind,
  fleetConditions,
  groupByHost,
  groupByKind,
  hostConditions,
  hostsNeedingAttention,
  isConditionKind,
  kindLabel,
  kindSeverity,
} from "./conditions";
import type { ConditionKindInfo, ConditionRow } from "../../lib/api";

const NOW = new Date("2026-08-12T12:00:00Z");
const GB = 1024 ** 3;

// The catalogue as the hub actually serves it -- internal/hub/conditions/
// catalogue.go. Written out here rather than imported from anywhere, because
// that is exactly what a fixture is for: if the hub's labels move, these tests
// keep asserting the old ones and the API test on the Go side is what catches
// it. What must NOT drift is the shape.
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
      warn_free: 100 * GB,
      crit_free: 20 * GB,
    },
  },
  { kind: "drive", label: "Drive errors", severity: "critical" },
];

const CATALOGUE = catalogueOf(KINDS);

function row(over: Partial<ConditionRow> = {}): ConditionRow {
  return {
    id: 1,
    host_id: 1,
    hostname: "web-01",
    kind: "disk",
    subject: "root",
    severity: "warning",
    opened_ts: "2026-08-12T06:00:00Z",
    opened_at_least: false,
    detail: { pct: 91.2, mount: "/", free: 9 * GB },
    measured_ts: "2026-08-12T11:59:30Z",
    stale: false,
    ...over,
  };
}

const HOST = { id: 1, hostname: "web-01", last_seen: "2026-08-12T11:59:30Z" };

describe("hostConditions", () => {
  it("says nothing about a host the hub raised nothing for", () => {
    expect(hostConditions([], CATALOGUE, NOW)).toEqual([]);
  });

  // The column that used to be empty for four kinds out of five, and the whole
  // point of moving the judgement to the hub: a derivation reading the current
  // row has no memory of when it first became true.
  it("takes the onset from the row rather than inventing one", () => {
    const [condition] = hostConditions([row()], CATALOGUE, NOW);
    expect(condition!.since).toBe("2026-08-12T06:00:00Z");
    expect(condition!.sinceAtLeast).toBe(false);
  });

  // "over 7 d", not a bucket where nothing happened: the hub's walk hit the
  // end of what is retained and says so.
  it("carries the floor flag when the onset is only a floor", () => {
    const [condition] = hostConditions(
      [row({ opened_at_least: true })],
      CATALOGUE,
      NOW,
    );
    expect(condition!.sinceAtLeast).toBe(true);
  });

  it("writes the disk sentence from the row's own numbers", () => {
    const [condition] = hostConditions([row()], CATALOGUE, NOW);
    expect(condition!.kind).toBe("disk");
    expect(condition!.severity).toBe("warning");
    expect(condition!.label).toBe("Filesystem nearly full");
    expect(condition!.what).toBe("/ is 91% full");
    expect(condition!.evidence).toEqual({ type: "meter", pct: 91.2 });
    expect(condition!.tab).toBe("storage");
  });

  // A 96 % disk on a machine that is off is still a 96 % disk. Only the TENSE
  // moves: the figure is the last one anybody measured rather than a statement
  // about this minute, and the severity is deliberately unchanged.
  it("says a silent host's disk WAS full, without softening the severity", () => {
    const conditions = hostConditions(
      [
        row({ kind: "silent", subject: "", severity: "critical", detail: {} }),
        row({ id: 2, severity: "critical", detail: { pct: 97, mount: "/" } }),
      ],
      CATALOGUE,
      NOW,
    );
    const disk = conditions.find((c) => c.kind === "disk")!;
    expect(disk.what).toBe("/ was 97% full");
    expect(disk.severity).toBe("critical");
  });

  // Reporting leads, because it qualifies everything below it: a host that has
  // not spoken for an hour has stale disk figures too.
  it("writes reporting first, whatever order the rows arrive in", () => {
    const conditions = hostConditions(
      [
        row(),
        row({ id: 2, kind: "silent", subject: "", severity: "critical" }),
      ],
      CATALOGUE,
      NOW,
    );
    expect(conditions.map((c) => c.kind)).toEqual(["silent", "disk"]);
  });

  it("counts failed units from the row and names them as evidence", () => {
    const [condition] = hostConditions(
      [
        row({
          kind: "failed-units",
          subject: "",
          detail: { count: 5, units: ["a.service", "b.service"] },
        }),
      ],
      CATALOGUE,
      NOW,
    );
    expect(condition!.what).toBe("5 failed units");
    expect(condition!.evidence).toEqual({
      type: "units",
      names: ["a.service", "b.service"],
      extra: 3,
    });
    expect(condition!.tab).toBe("units");
  });

  it("counts one failed unit in the singular", () => {
    const [condition] = hostConditions(
      [row({ kind: "failed-units", subject: "", detail: { count: 1 } })],
      CATALOGUE,
      NOW,
    );
    expect(condition!.what).toBe("1 failed unit");
  });

  // A rate has no onset: the gaps ARE the condition, and naming the first of
  // them would date it to a scrape the host happened to miss.
  it("leaves the onset empty for sporadic, whatever the row says", () => {
    const [condition] = hostConditions(
      [
        row({
          kind: "sporadic",
          subject: "",
          detail: { present: 24, span: 30 },
        }),
      ],
      CATALOGUE,
      NOW,
    );
    expect(condition!.since).toBeNull();
    expect(condition!.what).toBe(
      "Reporting sporadically — gaps in the last few hours",
    );
  });

  // The hub keeps a condition per MOUNT, deliberately -- collapsed in the state
  // machine, it would open and close every time the fullest mount changed from
  // /var to /mnt. The collapse to one line per host is a RENDERING decision,
  // and this is where it lives now.
  it("collapses a host's mounts to the worst one", () => {
    const conditions = hostConditions(
      [
        row({ subject: "var", detail: { pct: 91, mount: "/var" } }),
        row({
          id: 2,
          subject: "root",
          severity: "critical",
          detail: { pct: 97, mount: "/" },
        }),
      ],
      CATALOGUE,
      NOW,
    );
    expect(conditions).toHaveLength(1);
    expect(conditions[0]!.what).toBe("/ is 97% full");
  });

  // A subject the hub can no longer measure is SAID, not dropped. The page used
  // to retire a mount whose reading was three minutes old, which silently
  // retired the condition on it -- and the hub refuses to make that call at all
  // because a hung NFS export and an unmounted volume are indistinguishable
  // from where it stands.
  it("keeps a stale subject on screen and says the reading is old", () => {
    const [condition] = hostConditions(
      [
        row({
          stale: true,
          measured_ts: "2026-08-12T08:00:00Z",
          detail: { pct: 97, mount: "/mnt/backup" },
        }),
      ],
      CATALOGUE,
      NOW,
    );
    expect(condition!.stale).toBe(true);
    expect(condition!.what).toBe(
      "/mnt/backup is 97% full — not measured since 4 h ago",
    );
  });

  // A kind the hub raised and this file has no sentence for still appears.
  // Dropping it would be a fleet reading clean because the browser did not
  // recognise what was wrong with it -- the exact failure the engine exists to
  // end, reintroduced by an incomplete switch statement.
  it("still shows a kind it has no sentence for, named by the catalogue", () => {
    const catalogue = catalogueOf([
      ...KINDS,
      { kind: "thermal", label: "Running hot", severity: "warning" },
    ]);
    const [condition] = hostConditions(
      [row({ kind: "thermal", subject: "", detail: {} })],
      catalogue,
      NOW,
    );
    expect(condition!.kind).toBe("thermal");
    expect(condition!.what).toBe("Running hot");
  });

  // detail is `unknown` on the wire on purpose: its shape belongs to the
  // observer that produced it. Anything that is not a plain object says
  // nothing rather than throwing.
  it("survives a detail that is not an object", () => {
    for (const detail of [null, "text", 7, ["a"]]) {
      const [condition] = hostConditions([row({ detail })], CATALOGUE, NOW);
      expect(condition!.what).toBe("root is 0% full");
    }
  });
});

describe("fleetConditions", () => {
  it("renders every host's rows, in host order", () => {
    const conditions = fleetConditions(
      [
        row({ host_id: 2, hostname: "db-01" }),
        row({ host_id: 1, hostname: "web-01" }),
      ],
      [HOST, { id: 2, hostname: "db-01", last_seen: "2026-08-12T11:59:00Z" }],
      CATALOGUE,
      NOW,
    );
    expect(conditions.map((c) => c.hostname)).toEqual(["web-01", "db-01"]);
    expect(hostsNeedingAttention(conditions)).toBe(2);
  });

  // The hub deliberately refuses to raise this: a critical condition in the
  // gap between creating a host and installing its agent is false history in
  // the log an alerting engine reads. The PAGE states what is true now and
  // forgets it, which is what this fact wants.
  it("says a never-reported host is silent, though the hub raised nothing", () => {
    const conditions = fleetConditions(
      [],
      [{ id: 3, hostname: "new-01", last_seen: null }],
      CATALOGUE,
      NOW,
    );
    expect(conditions).toHaveLength(1);
    expect(conditions[0]!.kind).toBe("silent");
    expect(conditions[0]!.severity).toBe("critical");
    expect(conditions[0]!.what).toBe("Has never reported");
    // No onset: the host has never been observed at all, so there is nothing
    // to date it from.
    expect(conditions[0]!.since).toBeNull();
  });

  it("says nothing about a healthy host", () => {
    expect(fleetConditions([], [HOST], CATALOGUE, NOW)).toEqual([]);
  });
});

describe("failedUnitsShown", () => {
  it("names what it can and counts the rest", () => {
    expect(
      failedUnitsShown(5, ["a.service", "b.service", "c.service"]),
    ).toEqual({ names: ["a.service", "b.service", "c.service"], extra: 2 });
  });

  it("adds no remainder when the names are complete", () => {
    expect(failedUnitsShown(2, ["a.service", "b.service"])).toEqual({
      names: ["a.service", "b.service"],
      extra: 0,
    });
  });

  // "1 failed unit" beside two names contradicts itself in one breath. The
  // count is the number the rest of netra is counting, so the names give way
  // to it.
  it("never names more units than the count claims", () => {
    expect(failedUnitsShown(1, ["a.service", "b.service"])).toEqual({
      names: ["a.service"],
      extra: 0,
    });
  });

  it("counts them all as unnamed when there are no names", () => {
    expect(failedUnitsShown(1, [])).toEqual({ names: [], extra: 1 });
    expect(failedUnitsShown(4, [])).toEqual({ names: [], extra: 4 });
  });
});

// Grouping is what makes fifty warnings readable, so both rules are pinned
// here rather than left to a component that renders them.
describe("groupByHost", () => {
  const cond = (
    hostId: string,
    severity: "critical" | "warning",
    kind: "disk" | "drive" | "failed-units",
  ) => ({
    hostId,
    hostname: `host-${hostId}`,
    kind,
    severity,
    label: kind,
    what: kind,
    since: null,
    evidence: null,
    tab: null,
  });

  // One critical outranks four warnings: a noisy-but-healthy host must never
  // displace a genuinely broken one.
  it("orders hosts by their worst condition, not by how many they have", () => {
    const groups = groupByHost([
      cond("noisy", "warning", "disk"),
      cond("noisy", "warning", "failed-units"),
      cond("broken", "critical", "drive"),
    ]);
    expect(groups.map((g) => g.hostId)).toEqual(["broken", "noisy"]);
    expect(groups[0]!.worst.severity).toBe("critical");
  });

  it("orders a host's own conditions worst first, stably", () => {
    const [group] = groupByHost([
      cond("h", "warning", "disk"),
      cond("h", "critical", "drive"),
      cond("h", "warning", "failed-units"),
    ]);
    expect(group!.conditions.map((c) => c.kind)).toEqual([
      "drive",
      "disk",
      "failed-units",
    ]);
  });

  it("drops nothing: grouping is presentation, never suppression", () => {
    const [group] = groupByHost([
      cond("h", "warning", "disk"),
      cond("h", "warning", "failed-units"),
    ]);
    expect(group!.conditions).toHaveLength(2);
  });
});

describe("groupByKind", () => {
  const cond = (
    hostId: string,
    kind: "disk" | "failed-units",
    severity: "critical" | "warning" = "warning",
  ) => ({
    hostId,
    hostname: `host-${hostId}`,
    kind,
    severity,
    label: kind === "disk" ? "Filesystem nearly full" : "Failed units",
    what: kind,
    since: null,
    evidence: null,
    tab: null,
  });

  // The whole answer to fifty warnings: thirty-one hosts with the same
  // condition are one line, not thirty-one rows.
  it("counts hosts per kind", () => {
    const kinds = groupByKind([
      cond("a", "failed-units"),
      cond("b", "failed-units"),
      cond("c", "disk"),
    ]);
    const units = kinds.find((k) => k.kind === "failed-units")!;
    expect(units.hostIds).toEqual(["a", "b"]);
    expect(units.label).toBe("Failed units");
  });

  // The disk rule is 90% warning and 95% critical, so one kind can be both.
  // A counts line that dotted it warning while a host sits at 97% would
  // understate the fleet.
  it("takes a kind's worst severity, and leads with the worst kind", () => {
    const kinds = groupByKind([
      cond("a", "failed-units"),
      cond("b", "disk"),
      cond("c", "disk", "critical"),
    ]);
    expect(kinds[0]!.kind).toBe("disk");
    expect(kinds[0]!.severity).toBe("critical");
  });

  it("counts a host once per kind, however many conditions it has", () => {
    const kinds = groupByKind([cond("a", "disk"), cond("a", "disk")]);
    expect(kinds[0]!.hostIds).toEqual(["a"]);
  });
});

// A ?attn= nobody recognises is "all", never a filter that silently matches
// nothing.
// The fixtures elsewhere in this file supply their own labels, so nothing
// else pins the copy: the kind was renamed from "Filesystem over 90%" the
// moment the rule stopped being a bare percentage, and every one of those
// fixtures went on passing with the old wording in it.
// The catalogue is the hub's, and everything that reads it has to survive not
// having one yet -- the first render happens before the first response.
describe("the kind catalogue", () => {
  it("names a kind from the hub's own label", () => {
    expect(kindLabel(CATALOGUE, "disk")).toBe("Filesystem nearly full");
    // The kind was renamed from "Filesystem over 90%" the moment the rule
    // stopped being a bare percentage. It is the hub's wording now, so this
    // pins the shape rather than the copy.
    expect(kindLabel(CATALOGUE, "disk")).not.toMatch(/%/);
    expect(kindSeverity(CATALOGUE, "silent")).toBe("critical");
  });

  // A filter for a kind NOBODY is carrying must still name itself, which is
  // the whole reason the catalogue is fetched rather than derived from the
  // rows on screen.
  it("names a kind no host is carrying", () => {
    expect(kindLabel(CATALOGUE, "drive")).toBe("Drive errors");
    expect(isConditionKind(CATALOGUE, "drive")).toBe(true);
  });

  it("falls back to the kind's own name rather than blanking it", () => {
    expect(kindLabel(EMPTY_CATALOGUE, "disk")).toBe("disk");
  });

  // A ?attn= nobody recognises is "all", never a filter that silently matches
  // nothing -- and that includes every value before the catalogue lands.
  it("validates a URL parameter against the hub's vocabulary", () => {
    expect(isConditionKind(CATALOGUE, "disk")).toBe(true);
    expect(isConditionKind(CATALOGUE, "critical")).toBe(false);
    expect(isConditionKind(CATALOGUE, "")).toBe(false);
    expect(isConditionKind(CATALOGUE, "toString")).toBe(false);
    expect(isConditionKind(EMPTY_CATALOGUE, "disk")).toBe(false);
    expect(filterKind(EMPTY_CATALOGUE, "disk")).toBeNull();
    expect(filterKind(CATALOGUE, "disk")).toBe("disk");
    expect(filterKind(CATALOGUE, "critical")).toBeNull();
  });

  // An older hub, or one that answers without the field. A page that threw
  // here would be a total loss where an unnamed filter is a small one.
  it("treats a missing kinds list as no catalogue rather than a crash", () => {
    expect(catalogueOf(undefined).kinds).toEqual([]);
    expect(catalogueOf(null).kinds).toEqual([]);
  });
});

// The four numbers stopped being written out in TypeScript and arrive from the
// hub instead. They survive at all only because the Disk meter and the host
// page's disk tile have to judge HEALTHY mounts, which no condition covers.
describe("the disk thresholds", () => {
  it("reads the hub's numbers off the catalogue", () => {
    expect(diskThresholds(CATALOGUE)).toEqual({
      warnPct: 90,
      critPct: 95,
      warnFree: 100 * GB,
      critFree: 20 * GB,
    });
  });

  // Null is not a default. Writing 90 and 95 here would restore the second
  // copy this change deleted, and it would go on being wrong invisibly if the
  // hub's numbers ever moved.
  it("has no numbers of its own before the catalogue lands", () => {
    expect(diskThresholds(EMPTY_CATALOGUE)).toBeNull();
    expect(diskSeverityFor(99, 1, null)).toBeNull();
  });

  // The compound rule, and the case that made it necessary: netra used to say
  // "/mnt/ark is 90% full -- 674.4 GB free" in one breath and expect someone
  // to act on it.
  it("needs both a high percentage and little room left", () => {
    const t = diskThresholds(CATALOGUE);
    expect(diskSeverityFor(91, 9 * GB, t)).toBe("warning");
    expect(diskSeverityFor(97, 3 * GB, t)).toBe("critical");
    expect(diskSeverityFor(91, 674 * GB, t)).toBeNull();
    expect(diskSeverityFor(89, 1 * GB, t)).toBeNull();
  });

  // "not known" and "none left" are different facts: a row that has lost track
  // of the bytes must not go silent about a disk at 97%.
  it("falls back to the percentage alone when the bytes are unknown", () => {
    const t = diskThresholds(CATALOGUE);
    expect(diskSeverityFor(97, null, t)).toBe("critical");
    expect(diskSeverityFor(91, undefined, t)).toBe("warning");
  });
});

// The bug this kind exists to fix: netra rated a drive critical on the host's
// Storage tab and called the same host healthy one click up, because nothing
// carried the verdict out of that table.
describe("drive conditions", () => {
  const drive = (over: Partial<ConditionRow> = {}) =>
    row({
      kind: "drive",
      subject: "sda",
      severity: "critical",
      detail: {
        device: "sda",
        text: "2 pending sectors",
        alarms: 1,
        urgency: 0,
      },
      ...over,
    });

  it("names the drive and what is wrong with it", () => {
    const [condition] = hostConditions([drive()], CATALOGUE, NOW);
    expect(condition!.severity).toBe("critical");
    expect(condition!.label).toBe("Drive errors");
    expect(condition!.what).toBe("sda — 2 pending sectors");
    expect(condition!.tab).toBe("storage");
  });

  // ONE condition for the host, never one per drive: the counts line would
  // otherwise read "Drive errors 1" while the list showed four rows for it.
  // The count of the rest rides along instead of expanding into rows.
  it("collapses a host's drives into one line and counts the rest", () => {
    const conditions = hostConditions(
      [
        drive({
          detail: {
            device: "sda",
            text: "12 reallocated sectors",
            alarms: 2,
            urgency: 1,
          },
        }),
        drive({
          id: 2,
          subject: "sdb",
          detail: {
            device: "sdb",
            text: "2 pending sectors",
            alarms: 1,
            urgency: 0,
          },
        }),
      ],
      CATALOGUE,
      NOW,
    );
    expect(conditions).toHaveLength(1);
    // The acute finding leads: everything that escalates is critical, so
    // severity alone cannot say whether an unreadable sector or a counter that
    // is merely climbing is the one to name.
    expect(conditions[0]!.what).toBe("sdb — 2 pending sectors (+2 more)");
  });

  // SMART attributes are counters with no zero baseline, sampled hourly: the
  // first non-zero reading netra holds is when netra started LOOKING, not when
  // the sector went bad.
  it("dates nothing, and the hub offers no onset either", () => {
    const [condition] = hostConditions([drive()], CATALOGUE, NOW);
    expect(condition!.since).toBeNull();
    expect(condition!.evidence).toBeNull();
  });
});
