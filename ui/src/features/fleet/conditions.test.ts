import { describe, expect, it } from "vitest";
import { fleetConditions, hostConditions } from "./conditions";
import type { HostRow } from "./hostColumns";

const NOW = new Date("2026-08-12T12:00:00Z");

function makeRow(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    site_id: 3,
    site_name: "zurich-dc1",
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

  it("warns on a filesystem at 90 % and escalates at 95 %", () => {
    const warn = hostConditions(
      makeRow({ fullest: { mount: "/var/log", pct: 91, others: 2 } }),
      NOW,
    );
    expect(warn[0]?.severity).toBe("warning");
    expect(String(warn[0]?.what)).toMatch(/\/var\/log is 91 % full/);

    const crit = hostConditions(
      makeRow({ fullest: { mount: "/var/log", pct: 96, others: 2 } }),
      NOW,
    );
    expect(crit[0]?.severity).toBe("critical");
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
    expect(String(rows[0]?.what)).toMatch(/stopped reporting/);
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
