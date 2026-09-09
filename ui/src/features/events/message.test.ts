// The detail shapes here are the real ones: package and unit details are
// built by internal/hub/read/events.go's jsonb_build_object, and mdraid's is
// agent/collector/mdraid.go's arrayState marshalled as-is. A fixture that
// invented its own keys would pass while the page rendered blanks.
import { describe, expect, it } from "vitest";
import type { Event } from "../../lib/api";
import {
  CONDITION_EVENT_TYPES,
  KERNEL_EVENT_TYPES,
  KNOWN_EVENT_TYPES,
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
    severity: "info",
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
      // The agent's own delivery, which is a producer like any other now that
      // a hub outage is an event rather than a warning derived from a counter.
      "hub",
      // The hub's own judgements, arriving as transitions.
      ...CONDITION_EVENT_TYPES,
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

describe("hub delivery events", () => {
  function hub(detail: Record<string, unknown>): Event {
    return event({ type: "hub", subject: null, detail });
  }

  // The ordinary case, and the reason the warning this replaced was wrong: the
  // ring buffered the scrapes and replayed them, so the outage cost nothing.
  // Saying so is what stops a reader going to look for damage that is not
  // there.
  it("says an outage that lost nothing was replayed", () => {
    expect(
      messageOf(
        hub({
          severity: "info",
          reason: "unreachable",
          outage_ms: 19 * 60 * 1000,
          failures: 19,
        }),
      ),
    ).toBe("Hub unreachable for 19 m — buffered and replayed");
  });

  // The half that is worth acting on: the ring overflowed, so this host's
  // history has holes that nothing can fill.
  it("names what an outage cost when it cost something", () => {
    expect(
      messageOf(
        hub({
          severity: "critical",
          reason: "unreachable",
          outage_ms: 2 * 60 * 60 * 1000,
          failures: 120,
          lost: 47,
        }),
      ),
    ).toBe("Hub unreachable for 2 h — 47 scrapes lost");
  });

  it("counts one lost scrape in the singular", () => {
    expect(
      messageOf(
        hub({ reason: "unreachable", outage_ms: 60_000, failures: 1, lost: 1 }),
      ),
    ).toBe("Hub unreachable for 1 m — 1 scrape lost");
  });

  // A rejected token gets its own sentence. "The hub was away and came back"
  // and "this agent is not allowed to talk to the hub" are different problems
  // with different fixes, and only one of them resolves itself.
  it("says a rejected token is a rejected token", () => {
    expect(
      messageOf(
        hub({
          severity: "critical",
          reason: "token-rejected",
          outage_ms: 60_000,
          failures: 1,
          lost: 47,
        }),
      ),
    ).toBe("Hub rejected this agent's token — 47 scrapes discarded");
  });

  it("still names a rejected token when the buffer was already empty", () => {
    expect(
      messageOf(
        hub({ severity: "critical", reason: "token-rejected", failures: 1 }),
      ),
    ).toBe("Hub rejected this agent's token");
  });

  it("counts one discarded scrape in the singular", () => {
    expect(
      messageOf(hub({ reason: "token-rejected", failures: 1, lost: 1 })),
    ).toBe("Hub rejected this agent's token — 1 scrape discarded");
  });

  // A hub that answered and refused the BODY was never away, so it must not be
  // described as an outage. A host permanently over maxBatchRows earns this on
  // every flush, and "Hub unreachable for 0 s" would be a hub that answered
  // every request appearing in the log as one that did not.
  it("does not call a refused batch an outage", () => {
    expect(
      messageOf(
        hub({ severity: "critical", reason: "rejected", failures: 1, lost: 5 }),
      ),
    ).toBe("Hub refused a batch — 5 scrapes discarded");
  });

  it("names a refused batch even when it discarded nothing", () => {
    expect(messageOf(hub({ reason: "rejected", failures: 1 }))).toBe(
      "Hub refused a batch",
    );
  });
});

describe("condition transitions", () => {
  function cond(
    type: string,
    subject: string | null,
    detail: Record<string, unknown>,
  ): Event {
    return event({ type, subject, detail });
  }

  // These arrive in the log the moment the hub starts evaluating, so they must
  // read as sentences rather than as the generic detail dump -- which for a
  // transition says "root — transition opened · severity warning".
  it("names the kind and the subject when a condition opens", () => {
    expect(
      messageOf(
        cond("disk", "root", {
          transition: "opened",
          severity: "warning",
          opened_ts: "2026-09-07T03:00:00Z",
        }),
      ),
    ).toBe("Filesystem nearly full — root");
  });

  // A host-wide condition has no subject, and must not render a dangling dash.
  it("says the kind alone for a host-wide condition", () => {
    expect(
      messageOf(
        cond("silent", null, { transition: "opened", severity: "critical" }),
      ),
    ).toBe("Stopped reporting");
  });

  // How long it was open is the fact only the log holds: by then the row is
  // out of the open set, and the attention list never knew the duration.
  it("says how long a cleared condition was open", () => {
    expect(
      messageOf(
        cond("disk", "root", {
          transition: "cleared",
          reason: "cleared",
          open_ms: 2 * 60 * 60 * 1000,
        }),
      ),
    ).toBe("Filesystem nearly full — root cleared after 2 h");
  });

  // "No longer reported" is not a recovery. A mount that was unmounted did not
  // get better, and conflating the two is how a fleet goes green because
  // nobody is looking at it.
  it("does not call a vanished subject a recovery", () => {
    expect(
      messageOf(
        cond("disk", "backup", {
          transition: "cleared",
          reason: "vanished",
          open_ms: 30 * 60 * 1000,
        }),
      ),
    ).toBe("Filesystem nearly full — backup no longer reported after 30 m");
  });

  it("renders every kind it claims to know", () => {
    for (const type of CONDITION_EVENT_TYPES) {
      const sentence = messageOf(cond(type, "x", { transition: "opened" }));
      // The fallback spells out raw keys; a kind that fell through to it would
      // show "transition opened" instead of a name.
      expect(sentence).not.toMatch(/transition/);
      expect(sentence).toContain("x");
    }
  });

  // Every kind the hub can PRODUCE has to be offerable in the type filter, or
  // the log holds rows a reader cannot select. Both of these had observers
  // added in the same change that put them here.
  it("offers the kinds the hub now has observers for", () => {
    expect(CONDITION_EVENT_TYPES).toContain("drive");
    expect(CONDITION_EVENT_TYPES).toContain("sporadic");
  });

  // Getting worse is its own transition. Without a sentence of its own it read
  // exactly like the row that opened the condition hours earlier -- which is
  // the silence in the log that the escalation event was added to end.
  it("says a condition worsened, and what it worsened from", () => {
    expect(
      messageOf(
        cond("disk", "var-log", {
          transition: "escalated",
          from: "warning",
          severity: "critical",
        }),
      ),
    ).toBe("Filesystem nearly full — var-log worsened from warning");
  });

  // An escalation must not read as the opening it followed.
  it("does not print an escalation as if the condition had just opened", () => {
    const opened = messageOf(cond("disk", "var-log", { transition: "opened" }));
    const worse = messageOf(
      cond("disk", "var-log", { transition: "escalated", from: "warning" }),
    );
    expect(worse).not.toBe(opened);
  });
});
