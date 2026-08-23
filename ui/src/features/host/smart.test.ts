import { describe, expect, it } from "vitest";
import type { Drive } from "../../lib/api";
import {
  ATA,
  NVME,
  driveFindings,
  drivePowerOnHours,
  driveKind,
  driveSeverity,
  driveTemperature,
  driveWearPct,
} from "./smart";

function drive(
  attrs: Record<number, number | null>,
  over: Partial<Drive> = {},
): Drive {
  return {
    device: "sda",
    model: "Samsung SSD 870 EVO 1TB",
    serial: "S5Y2NG0R123456",
    attributes: Object.entries(attrs).map(([id, raw]) => ({
      id: Number(id),
      raw,
      normalized: null,
    })),
    last_seen: "2026-08-23T12:00:00Z",
    ...over,
  };
}

describe("driveKind", () => {
  // The ids the collector wrote, not the device name: /dev/nvme0n1 is the
  // common case and not the only one, and a USB enclosure can put an NVMe
  // drive behind an ATA passthrough.
  it("reads the id space rather than the device name", () => {
    expect(
      driveKind(drive({ [NVME.percentageUsed]: 4 }, { device: "sda" })),
    ).toBe("nvme");
    expect(
      driveKind(drive({ [ATA.reallocatedSectors]: 0 }, { device: "nvme0n1" })),
    ).toBe("ata");
  });

  it("is unknown for a drive with no attributes at all", () => {
    expect(driveKind(drive({}))).toBe("unknown");
  });
});

describe("driveFindings", () => {
  it("says nothing about a healthy ATA drive", () => {
    const d = drive({
      [ATA.reallocatedSectors]: 0,
      [ATA.currentPending]: 0,
      [ATA.offlineUncorrectable]: 0,
      [ATA.crcErrors]: 0,
      [ATA.powerOnHours]: 18_000,
      [ATA.temperature]: 34,
    });
    expect(driveFindings(d)).toEqual([]);
    expect(driveSeverity(d)).toBe("ok");
  });

  // Absent is not zero. A drive that does not report reallocated sectors has
  // not reported zero of them, and treating null as clean would be the same
  // conflation the whole metrics layer refuses.
  it("does not read an absent counter as a clean one", () => {
    const d = drive({ [ATA.currentPending]: null });
    expect(driveFindings(d)).toEqual([]);
  });

  it("reports pending sectors as critical, in an operator's words", () => {
    const found = driveFindings(drive({ [ATA.currentPending]: 3 }));
    expect(found).toHaveLength(1);
    expect(found[0]!.severity).toBe("critical");
    expect(found[0]!.text).toBe("3 pending sectors");
  });

  it("singularises a count of one", () => {
    expect(driveFindings(drive({ [ATA.currentPending]: 1 }))[0]!.text).toBe(
      "1 pending sector",
    );
  });

  // Reallocated sectors are what the spare pool is FOR, so a non-zero count is
  // serious rather than critical -- and must not outrank a sector that is
  // unreadable right now.
  it("orders findings worst first", () => {
    const found = driveFindings(
      drive({
        [ATA.reallocatedSectors]: 12,
        [ATA.crcErrors]: 4,
        [ATA.currentPending]: 1,
      }),
    );
    expect(found.map((f) => f.severity)).toEqual([
      "critical",
      "serious",
      "warning",
    ]);
    expect(driveSeverity(drive({ [ATA.reallocatedSectors]: 12 }))).toBe(
      "serious",
    );
  });

  // CRC errors are corruption on the wire, not on the platter. Saying so is
  // the difference between changing a cable and replacing a healthy disk.
  it("points CRC errors at the cable", () => {
    const found = driveFindings(drive({ [ATA.crcErrors]: 4 }));
    expect(found[0]!.text).toContain("check the cable");
    expect(found[0]!.severity).toBe("warning");
  });

  it("ignores attributes it does not recognise", () => {
    // Every drive reports ids this file has never heard of. Guessing that a
    // non-zero unknown counter is bad would flag every healthy disk.
    expect(driveFindings(drive({ 1: 45_000_000, 241: 88_000 }))).toEqual([]);
  });
});

describe("driveFindings on NVMe", () => {
  it("treats any critical_warning bit as the drive's own verdict", () => {
    const found = driveFindings(drive({ [NVME.criticalWarning]: 4 }));
    expect(found[0]!.severity).toBe("critical");
    expect(found[0]!.text).toContain("critical warning");
  });

  it("says nothing when critical_warning is clear", () => {
    expect(driveFindings(drive({ [NVME.criticalWarning]: 0 }))).toEqual([]);
  });

  // The spare percentage means nothing without the line it is compared
  // against, and that line varies per model -- which is why the collector
  // reports both.
  it("compares spare against the drive's own threshold, not a constant", () => {
    // 12% spare is fine on a drive whose floor is 10.
    expect(
      driveFindings(
        drive({
          [NVME.availableSpare]: 12,
          [NVME.availableSpareThreshold]: 10,
        }),
      ),
    ).toEqual([]);
    // The same 12% is critical on one whose floor is 15.
    const found = driveFindings(
      drive({ [NVME.availableSpare]: 12, [NVME.availableSpareThreshold]: 15 }),
    );
    expect(found[0]!.severity).toBe("critical");
    expect(found[0]!.text).toContain("15%");
  });

  it("grades wear against rated endurance", () => {
    expect(driveFindings(drive({ [NVME.percentageUsed]: 40 }))).toEqual([]);
    expect(
      driveFindings(drive({ [NVME.percentageUsed]: 85 }))[0]!.severity,
    ).toBe("warning");
    expect(
      driveFindings(drive({ [NVME.percentageUsed]: 100 }))[0]!.severity,
    ).toBe("critical");
  });

  // A drive past 100% is out of rated life and still writing. Reporting the
  // real number is the point; clamping it would hide how far past.
  it("reports wear beyond 100% rather than clamping it", () => {
    expect(
      driveFindings(drive({ [NVME.percentageUsed]: 137 }))[0]!.text,
    ).toContain("137%");
  });

  it("reports media errors", () => {
    const found = driveFindings(drive({ [NVME.mediaErrors]: 2 }));
    expect(found[0]!.severity).toBe("serious");
    expect(found[0]!.text).toBe("2 media errors");
  });
});

describe("the readings the table prints", () => {
  it("takes temperature and power-on hours from whichever id the drive uses", () => {
    const ata = drive({ [ATA.temperature]: 41, [ATA.powerOnHours]: 100 });
    expect(driveTemperature(ata)).toBe(41);
    expect(drivePowerOnHours(ata)).toBe(100);

    const nvme = drive({
      [NVME.temperature]: 52,
      [NVME.powerOnHours]: 900,
      [NVME.percentageUsed]: 3,
    });
    expect(driveTemperature(nvme)).toBe(52);
    expect(drivePowerOnHours(nvme)).toBe(900);
  });

  // ATA has no universal wear attribute: the SSD life-left counters are
  // vendor-specific with opposite polarity between models, and a spinning
  // disk has no endurance figure at all. Absent beats a confident wrong
  // number.
  it("reports wear for NVMe only", () => {
    expect(driveWearPct(drive({ [NVME.percentageUsed]: 7 }))).toBe(7);
    expect(
      driveWearPct(drive({ [ATA.reallocatedSectors]: 0, 231: 90 })),
    ).toBeNull();
  });
});

// A drive the hub knows about but has no readings for is not a failing drive.
// smartctl named it and could not read it, which the Device availability panel
// already reports -- calling it critical here would be a claim about hardware
// made from the absence of a reading.
describe("a drive with no attributes", () => {
  it("is not reported as a problem", () => {
    const d = drive({});
    expect(driveFindings(d)).toEqual([]);
    expect(driveSeverity(d)).toBe("ok");
    expect(driveTemperature(d)).toBeNull();
    expect(driveWearPct(d)).toBeNull();
  });
});
