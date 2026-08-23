// What a SMART attribute id MEANS, and whether its reading is a problem.
//
// This lives on the UI side, like conditions.ts and unlike AddressScope: the
// hub stores (attr_id, raw, normalized) exactly as the drive reported it,
// because SMART attributes vary per model and a typed column per attribute
// would need a schema change for every new drive (spec §5.3). Naming them is
// interpretation, and interpretation that lives in a shipped agent is frozen
// into every host in the fleet.
//
// TWO ID SPACES share this table. ATA attributes are 1-255, numbered by the
// drive's own table. NVMe has no attribute ids at all -- its health log is a
// fixed struct of named fields -- so the collector maps those fields onto
// SYNTHETIC ids from 1000 up (nvmeAttrs in internal/agent/collector/smart.go).
// The two cannot collide, which is why the range starts where it does.
import type { Drive, DriveAttribute } from "../../lib/api";
import type { Severity } from "../../ui/Badge";

/** ATA attribute ids this file knows how to read. */
export const ATA = {
  reallocatedSectors: 5,
  powerOnHours: 9,
  reportedUncorrect: 187,
  currentPending: 197,
  offlineUncorrectable: 198,
  crcErrors: 199,
  temperature: 194,
} as const;

/** The synthetic ids the collector assigns to NVMe health-log fields. */
export const NVME = {
  criticalWarning: 1000,
  percentageUsed: 1001,
  availableSpare: 1002,
  availableSpareThreshold: 1003,
  mediaErrors: 1004,
  unsafeShutdowns: 1005,
  powerOnHours: 1006,
  powerCycles: 1007,
  temperature: 1008,
  errorLogEntries: 1009,
} as const;

/**
 * The id space a drive's readings came from.
 *
 * Decided by whether any NVMe id is present rather than by the device name:
 * /dev/nvme0n1 is the common case but not the only one, and a USB enclosure
 * can put an NVMe drive behind an ATA passthrough. The ids are what the
 * collector actually wrote, so they are what this reads.
 */
export type DriveKind = "nvme" | "ata" | "unknown";

export function driveKind(drive: Drive): DriveKind {
  if (drive.attributes.length === 0) return "unknown";
  const nvme = drive.attributes.some((a) => a.id >= NVME.criticalWarning);
  return nvme ? "nvme" : "ata";
}

/** The raw value of one attribute, or null when the drive did not report it. */
export function attr(drive: Drive, id: number): number | null {
  const found = drive.attributes.find((a) => a.id === id);
  return found?.raw ?? null;
}

function attribute(drive: Drive, id: number): DriveAttribute | undefined {
  return drive.attributes.find((a) => a.id === id);
}

/**
 * One thing wrong with a drive, in the words an operator would use.
 *
 * `severity` is the shared Severity so a row's badge and its rail agree, and
 * "neutral" never appears: a finding exists because something is wrong.
 */
export interface Finding {
  severity: Exclude<Severity, "neutral">;
  text: string;
}

/**
 * Thresholds, in one place, because every one of them is a judgement.
 *
 * percentageUsed is the drive's own estimate of consumed write endurance and
 * is allowed to pass 100 -- a drive at 105% is out of rated life and still
 * writing, which is worth saying rather than clamping away.
 */
export const SMART_THRESHOLDS = {
  /** Wear at or above this is worth planning a replacement around. */
  wearWarning: 80,
  /** Rated endurance consumed. Past here the drive is on borrowed time. */
  wearCritical: 100,
} as const;

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

/**
 * Everything wrong with one drive, worst first.
 *
 * Reads only the attributes it knows. An unrecognised id is not a finding:
 * every drive reports attributes this file has never heard of, and guessing
 * that a non-zero unknown counter is bad would flag every healthy disk in the
 * fleet.
 *
 * Absent is not zero anywhere here. A drive that does not report reallocated
 * sectors has not reported zero of them, and `attr` returning null must not
 * read as "clean".
 */
export function driveFindings(drive: Drive): Finding[] {
  const out: Finding[] = [];
  const add = (severity: Finding["severity"], text: string) =>
    out.push({ severity, text });

  const kind = driveKind(drive);

  if (kind === "nvme") {
    // A bitfield the drive sets when it is telling the host it is in trouble.
    // Any bit is a verdict rather than a reading, so it outranks everything
    // else here.
    const warning = attr(drive, NVME.criticalWarning);
    if (warning !== null && warning !== 0) {
      add("critical", "drive reports a critical warning");
    }

    // Spare against the drive's OWN threshold. The percentage means nothing
    // without the line it is compared against, and that line varies per model
    // -- which is why the collector reports both.
    const spare = attr(drive, NVME.availableSpare);
    const spareFloor = attr(drive, NVME.availableSpareThreshold);
    if (spare !== null && spareFloor !== null && spare <= spareFloor) {
      add(
        "critical",
        `spare blocks at ${spare}%, at or below the drive's ${spareFloor}% floor`,
      );
    }

    const used = attr(drive, NVME.percentageUsed);
    if (used !== null && used >= SMART_THRESHOLDS.wearCritical) {
      add("critical", `${used}% of rated write endurance used`);
    } else if (used !== null && used >= SMART_THRESHOLDS.wearWarning) {
      add("warning", `${used}% of rated write endurance used`);
    }

    // Uncorrected data-integrity errors: the drive returned bad data or could
    // not return it at all.
    const media = attr(drive, NVME.mediaErrors);
    if (media !== null && media > 0) {
      add("serious", plural(media, "media error"));
    }
    return sorted(out);
  }

  if (kind === "ata") {
    // Sectors the drive has tried to read and could not, not yet reallocated.
    // The most urgent ATA counter there is: the data in them is currently
    // unreadable, and the count moves on the next write or the next failure.
    const pending = attr(drive, ATA.currentPending);
    if (pending !== null && pending > 0) {
      add("critical", plural(pending, "pending sector"));
    }

    // Sectors that failed even offline verification -- unreadable and not
    // recoverable by rewriting.
    const offline = attr(drive, ATA.offlineUncorrectable);
    if (offline !== null && offline > 0) {
      add("critical", plural(offline, "uncorrectable sector"));
    }

    // Already swapped for spares. Not urgent on its own -- that is what the
    // spare pool is for -- but a count that climbs is a drive consuming it.
    const reallocated = attr(drive, ATA.reallocatedSectors);
    if (reallocated !== null && reallocated > 0) {
      add("serious", plural(reallocated, "reallocated sector"));
    }

    const uncorrect = attr(drive, ATA.reportedUncorrect);
    if (uncorrect !== null && uncorrect > 0) {
      add("serious", plural(uncorrect, "uncorrectable error"));
    }

    // The cable, not the drive. UDMA CRC errors are corruption on the wire
    // between host and disk, so the fix is a cable or a port rather than a
    // replacement -- worth saying, because the alternative is somebody
    // replacing a healthy drive.
    const crc = attr(drive, ATA.crcErrors);
    if (crc !== null && crc > 0) {
      add("warning", `${plural(crc, "CRC error")} — check the cable`);
    }
    return sorted(out);
  }

  return out;
}

const RANK: Record<Finding["severity"], number> = {
  critical: 0,
  serious: 1,
  warning: 2,
  ok: 3,
};

function sorted(findings: Finding[]): Finding[] {
  return [...findings].sort((a, b) => RANK[a.severity] - RANK[b.severity]);
}

/**
 * A drive's overall state: its worst finding, or ok.
 *
 * A drive with NO attributes is "ok" rather than a problem. The hub knows
 * about it because something resolved a device id for it, and "smartctl could
 * not read this drive" is a collector fact the Device availability panel
 * already carries -- reporting it as a failing disk here would be a claim
 * about hardware made from the absence of a reading.
 */
export function driveSeverity(drive: Drive): Finding["severity"] {
  const findings = driveFindings(drive);
  return findings.length === 0 ? "ok" : findings[0]!.severity;
}

/** Temperature in degrees Celsius, from whichever id this drive uses. */
export function driveTemperature(drive: Drive): number | null {
  return driveKind(drive) === "nvme"
    ? attr(drive, NVME.temperature)
    : attr(drive, ATA.temperature);
}

/** Power-on hours, from whichever id this drive uses. */
export function drivePowerOnHours(drive: Drive): number | null {
  return driveKind(drive) === "nvme"
    ? attr(drive, NVME.powerOnHours)
    : attr(drive, ATA.powerOnHours);
}

/**
 * Wear, as a percentage of rated endurance consumed.
 *
 * NVMe only, and null everywhere else rather than guessed. ATA has no
 * universal wear attribute: the SSD life-left counters are vendor-specific
 * (231 on some, 233 on others, with opposite polarity), and a spinning disk
 * has no endurance figure at all. Reading one of those ids as though it were
 * standard would print a confident number that means something different on
 * every other model.
 */
export function driveWearPct(drive: Drive): number | null {
  if (driveKind(drive) !== "nvme") return null;
  return attr(drive, NVME.percentageUsed);
}

/**
 * The ATA normalized value for an attribute, when it is the reading worth
 * showing. Exported for the drive detail a later view may want; the table
 * shows raw counts, which is what an operator reasons about.
 */
export function normalizedOf(drive: Drive, id: number): number | null {
  return attribute(drive, id)?.normalized ?? null;
}
