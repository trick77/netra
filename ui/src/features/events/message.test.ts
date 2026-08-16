// The detail shapes here are the real ones: package and unit details are
// built by internal/hub/read/events.go's jsonb_build_object, and mdraid's is
// agent/collector/mdraid.go's arrayState marshalled as-is. A fixture that
// invented its own keys would pass while the page rendered blanks.
import { describe, expect, it } from "vitest";
import type { Event } from "../../lib/api";
import { KNOWN_EVENT_TYPES, messageOf } from "./message";

function event(over: Partial<Event> = {}): Event {
  return {
    id: "e:1",
    host_id: 3,
    hostname: "web-01",
    ts: "2026-08-10T13:59:00Z",
    type: "mdraid",
    subject: "md0",
    detail: {},
    ...over,
  };
}

function pkg(detail: Record<string, unknown>, name = "curl") {
  return event({ type: "package", subject: name, detail });
}

function unit(detail: Record<string, unknown>, name = "postgresql.service") {
  return event({ type: "unit", subject: name, detail });
}

describe("messageOf, package events", () => {
  it("names both versions of an upgrade", () => {
    expect(
      messageOf(
        pkg({
          action: "upgrade",
          from_version: "8.5.0",
          to_version: "8.5.0-2",
        }),
      ),
    ).toBe("curl upgraded 8.5.0 → 8.5.0-2");
  });

  it("gives an install the version it arrived at", () => {
    expect(messageOf(pkg({ action: "install", to_version: "14.1.0" }))).toBe(
      "curl installed 14.1.0",
    );
  });

  // A removal is the one a reader is most often hunting for -- "what went
  // missing" -- and the version it HAD is the only version there is.
  it("gives a removal the version it had", () => {
    expect(messageOf(pkg({ action: "remove", from_version: "3.0.13" }))).toBe(
      "curl removed (3.0.13)",
    );
  });

  // The hub drops null keys rather than sending them, so a half-populated
  // upgrade is what a missing side actually looks like on the wire.
  it("still reads as an upgrade when one side is missing", () => {
    expect(messageOf(pkg({ action: "upgrade", to_version: "8.5.0-2" }))).toBe(
      "curl upgraded to 8.5.0-2",
    );
    expect(messageOf(pkg({ action: "upgrade" }))).toBe("curl upgraded");
  });
});

describe("messageOf, unit events", () => {
  it("says a unit failed, and why systemd said it failed", () => {
    expect(
      messageOf(
        unit({
          state: "failed",
          substate: "exit-code",
          previous_state: "active",
        }),
      ),
    ).toBe("postgresql.service entered failed (exit-code)");
  });

  // systemd's substate for a failed unit is usually the word "failed" again;
  // repeating it in brackets says nothing.
  it("does not repeat the state as its own reason", () => {
    expect(messageOf(unit({ state: "failed", substate: "failed" }))).toBe(
      "postgresql.service entered failed",
    );
  });

  it("reads a return from failed as a recovery", () => {
    expect(
      messageOf(
        unit({
          state: "active",
          substate: "running",
          previous_state: "failed",
        }),
      ),
    ).toBe("postgresql.service recovered to active");
  });

  it("falls back to the plain transition when neither side is a failure", () => {
    expect(
      messageOf(unit({ state: "inactive", previous_state: "activating" })),
    ).toBe("postgresql.service activating → inactive");
  });
});

describe("messageOf, mdraid events", () => {
  it("counts the devices still present, which is what gets acted on", () => {
    expect(
      messageOf(
        event({
          detail: {
            state: "degraded",
            level: "raid1",
            raid_disks: 2,
            degraded: 1,
            sync_action: "idle",
          },
        }),
      ),
    ).toBe("md0 degraded — raid1, 1 of 2 devices");
  });

  it("names a sync in progress but stays quiet when idle", () => {
    expect(
      messageOf(
        event({
          detail: {
            state: "clean",
            level: "raid5",
            raid_disks: 3,
            degraded: 0,
            sync_action: "resync",
          },
        }),
      ),
    ).toBe("md0 clean — raid5, 3 devices, resync");
  });

  it("says the state alone when the array reported nothing else", () => {
    expect(messageOf(event({ detail: { state: "clean" } }))).toBe("md0 clean");
  });
});

describe("messageOf, anything else", () => {
  // Worse than terse is empty: a type added to the hub before this module
  // knows about it must still put its facts on the row.
  it("spells out an unknown type's detail rather than rendering a blank", () => {
    expect(
      messageOf(
        event({
          type: "agent_upgrade",
          subject: null,
          detail: { to: "0.9.1" },
        }),
      ),
    ).toBe("to 0.9.1");
  });

  it("keeps the subject alongside an unknown type's detail", () => {
    expect(
      messageOf(event({ type: "smart", subject: "sda", detail: { attr: 5 } })),
    ).toBe("sda — attr 5");
  });

  it("never leaks the severity key into the sentence", () => {
    expect(
      messageOf(
        event({
          type: "smart",
          subject: null,
          detail: { severity: "critical" },
        }),
      ),
    ).toBe("");
  });

  it("says nothing, rather than throwing, when detail is not an object", () => {
    expect(messageOf(event({ subject: null, detail: null }))).toBe("");
    expect(messageOf(event({ subject: null, detail: "oops" }))).toBe("");
    expect(messageOf(event({ subject: null, detail: [1, 2] }))).toBe("");
  });
});

describe("KNOWN_EVENT_TYPES", () => {
  // The hub's three branches in internal/hub/read/events.go. If a fourth is
  // added there, this list is the other half of the change.
  it("is the set the hub's union can emit", () => {
    expect([...KNOWN_EVENT_TYPES]).toEqual(["mdraid", "package", "unit"]);
  });
});
