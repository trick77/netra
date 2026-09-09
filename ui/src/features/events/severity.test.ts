// The fixtures are real emitter shapes: mdraid's detail JSON comes from
// agent/collector/mdraid.go, matching collector/testdata/mdraid/degraded. The
// severity rides its own field, as it does on the wire -- see severity.ts on
// why the row's column is now the source rather than a derivation from detail.
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
    severity: "info",
    ...overrides,
  };
}

const DEGRADED = event({
  id: "e:2",
  type: "mdraid",
  subject: "md0",
  // array_state is "clean" even here: the kernel reports consistency, not how
  // many disks are left -- which is why severityOf in agent/collector/mdraid.go
  // counts devices, and why the answer arrives as a field rather than being
  // guessed from `state`.
  severity: "critical",
  detail: { state: "clean", level: "raid10", raid_disks: 4, degraded: 1 },
});

// Degraded AND rebuilding, which is the pair that makes this a warning rather
// than a critical: the array is short a disk and is doing something about it.
const RECOVERING = event({
  id: "e:3",
  type: "mdraid",
  subject: "md1",
  severity: "warning",
  detail: {
    state: "clean",
    level: "raid10",
    raid_disks: 4,
    degraded: 1,
    sync_action: "recover",
  },
});

describe("severityOf", () => {
  it("reports what the hub stated", () => {
    expect(severityOf(DEGRADED)).toBe("critical");
    expect(severityOf(RECOVERING)).toBe("warning");
  });

  // A package upgrade is not a colour-coded emergency (spec 6).
  it("leaves a package upgrade as info", () => {
    expect(severityOf(event())).toBe("info");
  });

  // The detail blob is the emitting collector's own object and has no say in
  // this any more. It used to: the severity was read out of it, so a detail
  // that was not an object had to be guarded against right here.
  it("ignores the detail entirely", () => {
    expect(severityOf(event({ detail: { severity: "critical" } }))).toBe(
      "info",
    );
    expect(severityOf(event({ detail: "degraded" }))).toBe("info");
    expect(severityOf(event({ detail: null }))).toBe("info");
    expect(
      severityOf(event({ severity: "critical", detail: { severity: "info" } })),
    ).toBe("critical");
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
