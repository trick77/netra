import { describe, expect, it } from "vitest";
import type { MdArray } from "../../lib/api";
import {
  arraysByDrive,
  etaText,
  memberDrive,
  slotMap,
  syncLine,
  syncing,
} from "./arrays";

function mdArray(over: Partial<MdArray> = {}): MdArray {
  return {
    name: "md2",
    level: "raid1",
    raid_disks: 2,
    degraded: 0,
    state: "clean",
    sync_action: "idle",
    progress: null,
    speed_bps: null,
    eta_s: null,
    mismatch_cnt: null,
    members: [
      { device: "sdc1", state: "in_sync", slot: 0 },
      { device: "sdd1", state: "in_sync", slot: 1 },
    ],
    ts: "2026-11-28T13:00:00Z",
    stale: false,
    ...over,
  };
}

describe("md arrays", () => {
  it("counts a check, a repair or a rebuild as work, not idle or frozen", () => {
    for (const a of ["check", "repair", "resync", "recover"])
      expect(syncing({ sync_action: a })).toBe(true);
    for (const a of ["idle", "frozen", ""])
      expect(syncing({ sync_action: a })).toBe(false);
  });

  it("finds the drive under a member", () => {
    expect(memberDrive("sdd1")).toBe("sdd");
    expect(memberDrive("sdf")).toBe("sdf");
    expect(memberDrive("nvme0n1p2")).toBe("nvme0n1");
    expect(memberDrive("nvme0n1")).toBe("nvme0n1");
    expect(memberDrive("mmcblk0p1")).toBe("mmcblk0");
    expect(memberDrive("vdb3")).toBe("vdb");
    expect(memberDrive("dm-0")).toBe("dm-0");
  });

  it("lists the arrays each drive serves, once each", () => {
    const got = arraysByDrive([
      mdArray({ name: "md0", members: [{ device: "sdc2", state: "in_sync" }] }),
      mdArray(),
    ]);
    expect(got.get("sdc")).toEqual(["md0", "md2"]);
    expect(got.get("sdd")).toEqual(["md2"]);
  });

  it("draws mdstat's slot map, a gap for a missing or failed member", () => {
    expect(slotMap(mdArray())).toBe("[UU]");
    expect(
      slotMap(
        mdArray({
          degraded: 1,
          members: [
            { device: "sdc1", state: "in_sync", slot: 0 },
            { device: "sdd1", state: "faulty" },
          ],
        }),
      ),
    ).toBe("[U_]");
    expect(
      slotMap(
        mdArray({
          members: [
            { device: "sdc1", state: "in_sync,faulty", slot: 0 },
            { device: "sdd1", state: "in_sync", slot: 9 },
          ],
        }),
      ),
    ).toBe("[__]");
  });

  it("says the time left in hours and minutes", () => {
    expect(etaText(30)).toBe("< 1 min");
    expect(etaText(12 * 60)).toBe("12 min");
    expect(etaText(2 * 3600)).toBe("2 h");
    expect(etaText(6318)).toBe("1 h 45 min");
  });

  it("puts an array at work in one line", () => {
    expect(
      syncLine(mdArray({ sync_action: "check", progress: 0.896, eta_s: 6318 })),
    ).toBe("md2 check 89.6 %, 1 h 45 min left");
    expect(syncLine(mdArray({ sync_action: "resync" }))).toBe("md2 resync");
  });
});
