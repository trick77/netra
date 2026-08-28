import { describe, expect, it } from "vitest";
import {
  failedUnitsShown,
  fleetConditions,
  groupByHost,
  groupByKind,
  hostConditions,
  isConditionKind,
  kindLabel,
} from "./conditions";
import type { HostRow } from "./hostColumns";

const NOW = new Date("2026-08-12T12:00:00Z");

const GB = 1024 ** 3;
const MB = 1024 ** 2;

function makeRow(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    site_id: 3,
    site_name: "zurich-dc1",
    provider_name: null,
    facility: null,
    country_code: null,
    window: null,
    last_seen: "2026-08-12T11:59:30Z",
    cpu_total: 20,
    mem_used: 4_000_000_000,
    mem_total: 16_000_000_000,
    uptime_s: 86_400,
    net_rx_bytes: null,
    net_tx_bytes: null,
    threads: 8,
    cpu: [],
    mem: [],
    // Long enough and clean enough that reportsSporadically() has something
    // to judge and finds nothing wrong.
    reporting: [10, 11, 12, 11, 10, 11],
    rx: [],
    tx: [],
    fullest: { mount: "/", pct: 41, others: 1 },
    disk: [],
    oomKills: 0,
    dropped: null,
    postFailures: null,
    ...overrides,
  };
}

describe("hostConditions", () => {
  it("says nothing about a healthy host", () => {
    expect(hostConditions(makeRow(), NOW)).toEqual([]);
  });

  // The disagreement this module exists to end: the host page showed three
  // OOM kills in red while the fleet page said "nothing needs attention".
  it("raises OOM kills that happened inside the window", () => {
    const [c] = hostConditions(makeRow({ oomKills: 3 }), NOW);
    expect(c?.severity).toBe("critical");
    expect(String(c?.what)).toMatch(/3 OOM kills/);
  });

  it("counts one kill in the singular", () => {
    const [c] = hostConditions(makeRow({ oomKills: 1 }), NOW);
    expect(String(c?.what)).toMatch(/1 OOM kill\b/);
  });

  // The counter is cumulative since boot, so the row carries the INCREASE.
  // 0 is the host confirming nothing happened; null is "we cannot say",
  // which must not be reported as either trouble or an all-clear.
  it("stays silent for no kills and for an unanswerable window alike", () => {
    expect(hostConditions(makeRow({ oomKills: 0 }), NOW)).toEqual([]);
    expect(hostConditions(makeRow({ oomKills: null }), NOW)).toEqual([]);
  });

  // The other half of the disagreement this module exists to end: a host page
  // listing "nginx.service failed" beside a fleet row that showed nothing,
  // because the band had no notion of a unit at all.
  it("raises failed units", () => {
    const [c] = hostConditions(makeRow({ services_failed: 3 }), NOW);
    expect(c?.severity).toBe("warning");
    expect(String(c?.what)).toMatch(/3 failed units/);
  });

  it("counts one failed unit in the singular", () => {
    const [c] = hostConditions(makeRow({ services_failed: 1 }), NOW);
    expect(String(c?.what)).toMatch(/1 failed unit\b/);
  });

  // One row however many are broken: the row names up to three of them and
  // stays one row, rather than becoming one row per unit.
  it("says it once, not once per unit", () => {
    expect(hostConditions(makeRow({ services_failed: 8 }), NOW)).toHaveLength(
      1,
    );
  });

  // The dead end this fixes: the row said "1 failed unit" and the only way to
  // learn whether that was a backup job or the container runtime was to open
  // the host. The names are the row's EVIDENCE now rather than part of its
  // sentence -- the sentence is the count, and the count is what the list
  // groups thirty-one hosts by.
  it("names the failed unit as evidence and points at the tab that lists it", () => {
    const [c] = hostConditions(
      makeRow({ services_failed: 1, failed_units: ["docker.service"] }),
      NOW,
    );
    expect(String(c?.what)).toBe("1 failed unit");
    expect(c?.evidence).toEqual({
      type: "units",
      names: ["docker.service"],
      extra: 0,
    });
    expect(c?.kind).toBe("failed-units");
    expect(c?.tab).toBe("units");
  });

  // systemd's own timestamp, and the OLDEST of them: five units failing at
  // five times is one condition that began with the first.
  it("dates the failed units from the hub's oldest state change", () => {
    const [c] = hostConditions(
      makeRow({
        services_failed: 2,
        failed_units: ["a.service", "b.service"],
        failed_since: "2026-08-13T09:00:00Z",
      }),
      NOW,
    );
    expect(c?.since).toBe("2026-08-13T09:00:00Z");
  });

  // Null is the honest answer, not now(): state_ts is nullable and a host
  // with a count but no unit rows has nothing to date.
  it("leaves the onset empty when the hub cannot date the units", () => {
    const [c] = hostConditions(makeRow({ services_failed: 2 }), NOW);
    expect(c?.since).toBeNull();
  });

  it("falls back to the bare count when the hub cannot name them", () => {
    const [c] = hostConditions(
      makeRow({ services_failed: 2, failed_units: [] }),
      NOW,
    );
    expect(String(c?.what)).toBe("2 failed units");
  });

  // 0 is the host confirming its units are fine. null is a host that has
  // never reported a unit -- no systemd collector, or nothing heard yet --
  // and reporting that as an all-clear would be netra vouching for something
  // it has never looked at. Both stay silent, for different reasons.
  it("stays silent for no failures and for a host it has never looked at", () => {
    expect(hostConditions(makeRow({ services_failed: 0 }), NOW)).toEqual([]);
    expect(hostConditions(makeRow({ services_failed: null }), NOW)).toEqual([]);
    expect(
      hostConditions(makeRow({ services_failed: undefined }), NOW),
    ).toEqual([]);
  });

  // The agent's own health. Both counters are the increase across the window,
  // and both are worth a request the sparklines did not already need -- see
  // the module header for why they cannot ride the hosts list.
  it("leads with dropped samples, because the missing data is the evidence", () => {
    const rows = hostConditions(
      makeRow({ dropped: 12, fullest: { mount: "/", pct: 97, others: 0 } }),
      NOW,
    );
    expect(rows[0]?.severity).toBe("critical");
    expect(String(rows[0]?.what)).toMatch(/12 samples dropped before delivery/);
  });

  it("says sample, singular, for a single dropped sample", () => {
    const [c] = hostConditions(makeRow({ dropped: 1 }), NOW);
    expect(String(c?.what)).toMatch(/1 sample dropped\b/);
  });

  it("reports failed deliveries to the hub", () => {
    const [c] = hostConditions(makeRow({ postFailures: 4 }), NOW);
    expect(c?.severity).toBe("warning");
    expect(String(c?.what)).toMatch(/4 failed deliveries/);
  });

  it("counts one failed delivery in the singular", () => {
    const [c] = hostConditions(makeRow({ postFailures: 1 }), NOW);
    expect(String(c?.what)).toMatch(/1 failed delivery\b/);
  });

  // post_failures_total is cumulative for the life of the agent process and
  // is never reset by a success, so read as a latest value one hub restart
  // would pin a failure here forever. 0 and null are both silence.
  it("stays silent for a clean window and an unanswerable one alike", () => {
    expect(
      hostConditions(makeRow({ dropped: 0, postFailures: 0 }), NOW),
    ).toEqual([]);
    expect(
      hostConditions(makeRow({ dropped: null, postFailures: null }), NOW),
    ).toEqual([]);
  });

  it("warns on a filesystem at 90% and escalates at 95%", () => {
    const warn = hostConditions(
      makeRow({ fullest: { mount: "/var/log", pct: 91, others: 2 } }),
      NOW,
    );
    expect(warn[0]?.severity).toBe("warning");
    expect(String(warn[0]?.what)).toMatch(/\/var\/log is 91% full/);

    const crit = hostConditions(
      makeRow({ fullest: { mount: "/var/log", pct: 96, others: 2 } }),
      NOW,
    );
    expect(crit[0]?.severity).toBe("critical");
  });

  // The report this rule exists for: "/mnt/ark is 90% full -- 674.4 GB free"
  // is a sentence that argues with itself, and nobody has anything to do
  // about it.
  it("stays quiet about a big volume with room left, at any percentage", () => {
    const rows = hostConditions(
      makeRow({
        fullest: { mount: "/mnt/ark", pct: 90, free: 674 * GB, others: 3 },
      }),
      NOW,
    );
    expect(rows).toEqual([]);
  });

  it("warns on the same percentage when the bytes are nearly gone", () => {
    const [c] = hostConditions(
      makeRow({ fullest: { mount: "/", pct: 90, free: 2 * GB, others: 3 } }),
      NOW,
    );
    expect(c?.severity).toBe("warning");
    expect(String(c?.what)).toMatch(/\/ is 90% full/);
  });

  // Both floors have to bind for the worse word to apply. 96% of a 6.8 TB
  // array with 67 GB left is under the warning floor and over the critical
  // one: worth a word, not an emergency. With 500 GB left neither binds and
  // the percentage on its own buys nothing.
  it("holds a deep-but-roomy volume below critical", () => {
    const [warn] = hostConditions(
      makeRow({
        fullest: { mount: "/mnt/ark", pct: 96, free: 67 * GB, others: 3 },
      }),
      NOW,
    );
    expect(warn?.severity).toBe("warning");

    expect(
      hostConditions(
        makeRow({
          fullest: { mount: "/mnt/ark", pct: 96, free: 500 * GB, others: 3 },
        }),
        NOW,
      ),
    ).toEqual([]);
  });

  it("criticals when the same percentage leaves under 20 GiB", () => {
    const [c] = hostConditions(
      makeRow({ fullest: { mount: "/", pct: 96, free: 800 * MB, others: 3 } }),
      NOW,
    );
    expect(c?.severity).toBe("critical");
  });

  // A row that has lost track of the bytes must not go silent about a disk at
  // 97%: unknown headroom falls back to the percentage alone.
  it("judges on the percentage alone when free bytes are unknown", () => {
    const [c] = hostConditions(
      makeRow({ fullest: { mount: "/", pct: 97, free: null, others: 0 } }),
      NOW,
    );
    expect(c?.severity).toBe("critical");
  });

  it("leaves a comfortable disk alone", () => {
    const rows = hostConditions(
      makeRow({ fullest: { mount: "/", pct: 89, others: 0 } }),
      NOW,
    );
    expect(rows).toEqual([]);
  });

  // A host that stopped reporting has stale disk and memory figures too, so
  // saying so FIRST stops everything below it reading as current.
  it("leads with a host that stopped reporting, and dates it honestly", () => {
    const rows = hostConditions(
      makeRow({
        last_seen: "2026-08-12T11:00:00Z",
        fullest: { mount: "/", pct: 97, others: 0 },
      }),
      NOW,
    );
    expect(rows[0]?.severity).toBe("critical");
    expect(String(rows[0]?.what)).toMatch(/Stopped reporting/);
    // last_seen IS the onset here -- the one condition with a real one.
    expect(rows[0]?.since).toBe("2026-08-12T11:00:00Z");
    expect(rows).toHaveLength(2);
  });

  it("distinguishes a host that has never reported from one that stopped", () => {
    const [c] = hostConditions(makeRow({ last_seen: null }), NOW);
    expect(String(c?.what)).toMatch(/never reported/);
    expect(c?.since).toBeNull();
  });

  it("reports a host answering now but dropping scrapes as sporadic", () => {
    const [c] = hostConditions(
      makeRow({ reporting: [10, null, 12, null, 10, 11] }),
      NOW,
    );
    expect(c?.severity).toBe("warning");
    expect(String(c?.what)).toMatch(/sporadic/);
  });

  // The band said "reporting sporadically -- gaps in the last few hours"
  // beside a host whose agent had been running for five minutes. The gaps
  // were real and they were the window before the host existed: the fleet
  // page asks for its whole range regardless of when a host was added.
  it("says nothing about a host that was only just added", () => {
    const justAdded = makeRow({
      reporting: [...Array<number | null>(283).fill(null), 12],
    });

    expect(hostConditions(justAdded, NOW)).toEqual([]);
  });

  // No honest onset exists for most of these: a filesystem at 91 % crossed
  // 90 at some moment netra never recorded. A plausible-looking timestamp
  // would be read literally, so there is none.
  it("carries no onset for a condition whose start was never observed", () => {
    const [c] = hostConditions(makeRow({ oomKills: 2 }), NOW);
    expect(c?.since).toBeNull();
  });
});

describe("fleetConditions", () => {
  it("gathers every host's conditions", () => {
    const rows = [
      makeRow({ id: 1, hostname: "web-01" }),
      makeRow({ id: 2, hostname: "db-01", oomKills: 4 }),
      makeRow({
        id: 3,
        hostname: "log-01",
        fullest: { mount: "/var/log", pct: 93, others: 0 },
      }),
    ];
    const all = fleetConditions(rows, NOW);
    expect(all).toHaveLength(2);
    expect(all.map((c) => c.hostname)).toEqual(["db-01", "log-01"]);
  });

  it("is empty for a wholly healthy fleet, so the all-clear line shows", () => {
    expect(fleetConditions([makeRow(), makeRow({ id: 2 })], NOW)).toEqual([]);
  });
});

// The count leads and the names annotate it. The two come from different
// tables and are allowed to disagree; every branch resolves that in favour of
// the count.
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
    kind: "disk" | "oom" | "failed-units",
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
      cond("broken", "critical", "oom"),
    ]);
    expect(groups.map((g) => g.hostId)).toEqual(["broken", "noisy"]);
    expect(groups[0]!.worst.severity).toBe("critical");
  });

  it("orders a host's own conditions worst first, stably", () => {
    const [group] = groupByHost([
      cond("h", "warning", "disk"),
      cond("h", "critical", "oom"),
      cond("h", "warning", "failed-units"),
    ]);
    expect(group!.conditions.map((c) => c.kind)).toEqual([
      "oom",
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
describe("kindLabel", () => {
  it("names the disk kind without quoting a percentage the rule outgrew", () => {
    expect(kindLabel("disk")).toBe("Filesystem nearly full");
    expect(kindLabel("disk")).not.toMatch(/%/);
  });
});

describe("isConditionKind", () => {
  it("accepts the kinds and rejects everything else", () => {
    expect(isConditionKind("disk")).toBe(true);
    expect(isConditionKind("failed-units")).toBe(true);
    expect(isConditionKind("critical")).toBe(false);
    expect(isConditionKind("")).toBe(false);
    expect(isConditionKind("toString")).toBe(false);
  });
});
