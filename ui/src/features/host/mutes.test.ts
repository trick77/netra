import { describe, expect, it } from "vitest";

import type { Mute } from "../../lib/api";
import {
  coveringMute,
  MUTABLE_EVENT_TYPES,
  muteScope,
  muteText,
  muteUntil,
} from "./mutes";

const mute = (over: Partial<Mute>): Mute => ({
  id: 1,
  host_id: 7,
  hostname: "kessel",
  kind: "",
  subject: null,
  until_resolved: false,
  until: null,
  note: "",
  created: "2026-11-18T10:00:00Z",
  ...over,
});

describe("mute wording", () => {
  it("names the scope, the end and the note", () => {
    expect(muteScope("")).toBe("all notifications");
    expect(muteScope("disk")).toBe("Filesystem nearly full");
    expect(muteScope("disk", "/var")).toBe("Filesystem nearly full — /var");
    expect(muteScope("silent", "")).toBe("Stopped reporting");
    expect(muteScope("disk_error", "sda")).toBe("disk_error — sda");
    expect(muteUntil(mute({}))).toBe("permanently");
    expect(muteUntil(mute({ until_resolved: true }))).toBe("until resolved");
    expect(muteUntil(mute({ until: "2026-11-18T14:30:00Z" }))).toMatch(
      /^until 18 Nov 2026/,
    );
    expect(muteText(mute({ kind: "load", note: "batch job" }))).toBe(
      "Load above normal permanently (batch job)",
    );
  });

  it("offers every event type the hub accepts", () => {
    expect(MUTABLE_EVENT_TYPES).toContain("mdraid");
    expect(MUTABLE_EVENT_TYPES).toContain("hub");
    expect(MUTABLE_EVENT_TYPES).toContain("disk_error");
  });
});

describe("coveringMute", () => {
  const disk = { kind: "disk", subject: "/var" };

  it("matches host, kind and subject the way the hub does", () => {
    expect(
      coveringMute([mute({ kind: "disk", subject: "/var" })], 7, disk),
    ).toBeDefined();
    expect(
      coveringMute([mute({ kind: "disk", subject: "/home" })], 7, disk),
    ).toBeUndefined();
    expect(coveringMute([mute({ kind: "load" })], 7, disk)).toBeUndefined();
    expect(coveringMute([mute({ host_id: 8 })], 7, disk)).toBeUndefined();
    expect(coveringMute([mute({ kind: "disk" })], 7, disk)).toBeDefined();
  });

  it("picks the narrowest mute", () => {
    const got = coveringMute(
      [
        mute({ id: 1 }),
        mute({ id: 2, kind: "disk" }),
        mute({ id: 3, kind: "disk", subject: "/var" }),
      ],
      7,
      disk,
    );
    expect(got?.id).toBe(3);
  });
});
