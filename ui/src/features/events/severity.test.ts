// The fixtures are real emitter shapes: mdraid's detail JSON comes from
// agent/collector/mdraid.go, matching collector/testdata/mdraid/degraded.
// severity is asserted as something derived FROM them -- see severity.ts on
// why the row's own column is not the source.
import { describe, expect, it } from "vitest";
import type { Event } from "../../lib/api";
import {
  SEVERITY_RANK,
  SEVERITY_TINT,
  railSeverity,
  severityOf,
} from "./severity";

function event(overrides: Partial<Event> = {}): Event {
  return {
    id: "e:1",
    host_id: 3,
    hostname: "web-01",
    ts: "2026-08-10T13:59:00Z",
    type: "package",
    subject: "nginx",
    detail: { from: "1.26", to: "1.27" },
    ...overrides,
  };
}

const DEGRADED = event({
  id: "e:2",
  type: "mdraid",
  subject: "md0",
  // array_state is "clean" even here: the kernel reports consistency, not how
  // many disks are left. See mdraidSeverity in ./message.
  detail: { state: "clean", level: "raid10", raid_disks: 4, degraded: 1 },
});

// Degraded AND rebuilding, which is the pair that makes this a warning rather
// than a critical: the array is short a disk and is doing something about it.
const RECOVERING = event({
  id: "e:3",
  type: "mdraid",
  subject: "md1",
  detail: {
    state: "clean",
    level: "raid10",
    raid_disks: 4,
    degraded: 1,
    sync_action: "recover",
  },
});

describe("severityOf", () => {
  it("reads a degraded array as critical", () => {
    expect(severityOf(DEGRADED)).toBe("critical");
  });

  it("reads a recovering array as a warning", () => {
    expect(severityOf(RECOVERING)).toBe("warning");
  });

  // A package upgrade is not a colour-coded emergency (spec 6).
  it("leaves a package upgrade as info", () => {
    expect(severityOf(event())).toBe("info");
  });

  it("believes a severity the emitter stated outright", () => {
    expect(severityOf(event({ detail: { severity: "critical" } }))).toBe(
      "critical",
    );
    // Not one of the two the design admits, so it decides nothing.
    expect(severityOf(event({ detail: { severity: "spicy" } }))).toBe("info");
  });

  it("does not trip over a detail that is not an object", () => {
    expect(severityOf(event({ detail: "degraded" }))).toBe("info");
    expect(severityOf(event({ detail: null }))).toBe("info");
  });
});

describe("severity rendering", () => {
  // Every severity has a tint and a rank. The point of the maps being keyed on
  // EventSeverity is that a fourth one cannot be added without visiting both,
  // and this asserts the pair stays total rather than that it holds today's
  // three values.
  it("covers every severity in both maps", () => {
    for (const severity of ["info", "warning", "critical"] as const) {
      expect(SEVERITY_TINT[severity]).toBeDefined();
      expect(SEVERITY_RANK[severity]).toBeTypeOf("number");
    }
  });

  it("orders the ranks worst-highest, so a filter can be a threshold", () => {
    expect(SEVERITY_RANK.critical).toBeGreaterThan(SEVERITY_RANK.warning);
    expect(SEVERITY_RANK.warning).toBeGreaterThan(SEVERITY_RANK.info);
  });

  // A table where every row is railed has marked nothing.
  it("rails the two severities that are wrong and not info", () => {
    expect(railSeverity("critical")).toBe("critical");
    expect(railSeverity("warning")).toBe("warning");
    expect(railSeverity("info")).toBeNull();
  });

  it("gives info the neutral tint rather than no badge at all", () => {
    expect(SEVERITY_TINT.info).toBe("neutral");
  });
});
