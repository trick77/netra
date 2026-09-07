// The detail shapes here are the real ones: package and unit details are
// built by internal/hub/read/events.go's jsonb_build_object, and mdraid's is
// agent/collector/mdraid.go's arrayState marshalled as-is. A fixture that
// invented its own keys would pass while the page rendered blanks.
import { describe, expect, it } from "vitest";
import type { Event } from "../../lib/api";
import {
  KERNEL_EVENT_TYPES,
  KNOWN_EVENT_TYPES,
  mdraidSeverity,
  messageOf,
  packageRunSize,
  packagesOmitted,
} from "./message";

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
  // THE fixture to get right. These are the exact values in
  // internal/agent/collector/testdata/mdraid/degraded: the kernel reports a
  // half-dead array as array_state=clean, because clean is about consistency
  // rather than about how many disks are left. Reading `state` for the
  // condition is therefore always wrong, and a fixture that says
  // state:"degraded" is testing a shape no agent can send.
  const REAL_DEGRADED = {
    state: "clean",
    level: "raid1",
    raid_disks: 2,
    degraded: 1,
    sync_action: "idle",
  };

  it("calls a degraded array degraded, though sysfs called it clean", () => {
    expect(messageOf(event({ detail: REAL_DEGRADED }))).toBe(
      "md0 degraded — raid1, 1 of 2 devices",
    );
  });

  it("calls it rebuilding once a repair is under way", () => {
    expect(
      messageOf(
        event({ detail: { ...REAL_DEGRADED, sync_action: "recover" } }),
      ),
    ).toBe("md0 rebuilding — raid1, 1 of 2 devices");
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
  // Two sources, unioned. The first three are the hub's own branches in
  // internal/hub/read/events.go; the rest are what the kmsg collector
  // classifies, and ride the generic `events` branch. If a producer is added
  // on either side, this list is the other half of the change.
  it("is the set the hub's union can emit", () => {
    expect([...KNOWN_EVENT_TYPES]).toEqual([
      "mdraid",
      "package",
      "unit",
      ...KERNEL_EVENT_TYPES,
    ]);
  });

  // The kernel types mirror kmsgClasses in agent/collector/kmsg.go. A type the
  // collector emits and this list omits still renders, but as a generic detail
  // dump rather than as the kernel's own sentence.
  it("covers every type the kmsg collector emits", () => {
    expect([...KERNEL_EVENT_TYPES]).toEqual([
      "disk_error",
      "ata_error",
      "scsi_error",
      "nvme_error",
      "md_fail",
      "fs_error",
      "oom_kill",
      "hw_error",
      "thermal",
      "kernel_fault",
      "link_change",
    ]);
  });
});

describe("kernel events", () => {
  const kernelEvent = (detail: Record<string, unknown>) =>
    event({
      type: "disk_error",
      subject: "sdd",
      detail: { severity: "critical", priority: 3, ...detail },
    });

  // The kernel's own sentence, verbatim. An operator searching for the string
  // their monitoring gave them has to find it here, and a paraphrase is both
  // longer and less useful than the line itself.
  it("renders the kernel line as the kernel wrote it", () => {
    expect(
      messageOf(
        kernelEvent({
          message:
            "blk_update_request: I/O error, dev sdd, sector 13211246 op 0x0:(READ)",
        }),
      ),
    ).toBe(
      "blk_update_request: I/O error, dev sdd, sector 13211246 op 0x0:(READ)",
    );
  });

  // The count is the difference between "sdd threw an error" and "sdd threw
  // four hundred", which is the whole diagnosis.
  it("shows how many records were folded into the row", () => {
    expect(
      messageOf(kernelEvent({ message: "ata4.00: error: { UNC }", count: 4 })),
    ).toBe("ata4.00: error: { UNC } (×4)");
  });

  it("shows what the quiet window withheld", () => {
    expect(
      messageOf(
        kernelEvent({
          message: "ata4.00: error: { UNC }",
          count: 2,
          suppressed: 118,
        }),
      ),
    ).toBe("ata4.00: error: { UNC } (×2, 118 more since)");
  });

  // A count of 1 is the uninteresting case and the collector omits it; a row
  // reading "(×1)" would be noise dressed as information.
  it("says nothing about a count of one", () => {
    expect(
      messageOf(kernelEvent({ message: "mce: [Hardware Error]", count: 1 })),
    ).toBe("mce: [Hardware Error]");
  });
});

describe("mdraidSeverity", () => {
  // The bug this exists for: EventsPage's severity table matches the words
  // "degraded", "faulty", "recovering", "rebuilding" against detail.state --
  // and none of them is a value sysfs array_state can take. So for mdraid the
  // table never fired once, and a raid1 down to its last disk was rendered
  // "info", in the log whose whole job is to surface that.
  const REAL_DEGRADED = {
    state: "clean",
    level: "raid1",
    raid_disks: 2,
    degraded: 1,
    sync_action: "idle",
  };

  it("calls a degraded array with nothing being done about it critical", () => {
    expect(mdraidSeverity(event({ detail: REAL_DEGRADED }))).toBe("critical");
  });

  it("softens to a warning while it rebuilds onto a spare", () => {
    for (const sync of ["recover", "resync", "repair"]) {
      expect(
        mdraidSeverity(
          event({ detail: { ...REAL_DEGRADED, sync_action: sync } }),
        ),
      ).toBe("warning");
    }
  });

  it("has no opinion about a whole array, whatever it is doing", () => {
    expect(
      mdraidSeverity(event({ detail: { ...REAL_DEGRADED, degraded: 0 } })),
    ).toBeNull();
    expect(
      mdraidSeverity(
        event({
          detail: { state: "clean", degraded: 0, sync_action: "check" },
        }),
      ),
    ).toBeNull();
  });

  it("judges only mdraid, leaving other types to their own emitter", () => {
    expect(
      mdraidSeverity(event({ type: "package", detail: REAL_DEGRADED })),
    ).toBeNull();
  });
});

describe("packagesOmitted / packageRunSize", () => {
  // One apt run is one timestamp, so a dist-upgrade would otherwise fill the
  // page. The hub keeps the first few and counts the rest; these read that
  // count. Both keys are ABSENT on an ordinary run rather than sent as zero.
  it("is zero for a run the hub sent in full", () => {
    expect(packagesOmitted(pkg({ action: "upgrade" }))).toBe(0);
    expect(packageRunSize(pkg({ action: "upgrade" }))).toBe(0);
  });

  it("reads the counts off the row the hub marked", () => {
    const marked = pkg({ action: "upgrade", more: 397, run_size: 400 });
    expect(packagesOmitted(marked)).toBe(397);
    expect(packageRunSize(marked)).toBe(400);
  });

  it("ignores the keys on any other type", () => {
    // Only the package branch truncates runs; a `more` key on an mdraid event
    // would be a coincidence of shape, not a truncated apt run.
    expect(packagesOmitted(event({ detail: { more: 9, run_size: 12 } }))).toBe(
      0,
    );
    expect(packageRunSize(event({ detail: { more: 9, run_size: 12 } }))).toBe(
      0,
    );
  });

  it("treats a zero or a non-number as nothing omitted", () => {
    expect(packagesOmitted(pkg({ action: "upgrade", more: 0 }))).toBe(0);
    expect(packagesOmitted(pkg({ action: "upgrade", more: "397" }))).toBe(0);
  });

  it("keeps the counts out of the sentence", () => {
    // They are instructions to the UI, not facts about what happened to curl.
    expect(
      messageOf(
        pkg({
          action: "upgrade",
          from_version: "8.5.0",
          to_version: "8.5.0-2",
          more: 397,
          run_size: 400,
        }),
      ),
    ).toBe("curl upgraded 8.5.0 → 8.5.0-2");

    // Including via the unknown-action fallback, which spells out every key.
    expect(messageOf(pkg({ action: "held", more: 397, run_size: 400 }))).toBe(
      "action held",
    );
  });
});
