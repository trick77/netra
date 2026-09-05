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
import type { Drive } from "../../lib/api";
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

/**
 * One drive finding that has left the Storage tab, with the drive it is about.
 *
 * `text` is the finding's own words, unchanged -- the sentence an operator
 * reads on the overview is the sentence they will read again on the drive row
 * they click through to.
 */
export interface DriveAlarm {
  device: string;
  severity: "serious" | "critical";
  text: string;
}

/**
 * How far a drive's last reading may fall behind the host's last report
 * before the drive counts as gone rather than as failing.
 *
 * A `devices` row outlives the disk. It is unique on (host_id, device), it is
 * never deleted by set difference, and the prune only reaches it at 120 days
 * -- so a failing disk that is PULLED, or that comes back as a different
 * /dev/sdX after a reorder, leaves its row behind frozen at its last SMART
 * reading. DISTINCT ON (attr_id) keeps handing that reading back, and without
 * this gate the host would carry "Drive errors · Critical" for the ninety days
 * it takes the readings to age out, with nothing an operator could do to clear
 * it. That is exactly the permanent-condition failure driveAlarms rejects CRC
 * errors for; it must not be reintroduced by the back door.
 *
 * Seven days, against the HOST's own last report rather than the wall clock,
 * so an offline host does not lose its drives: the last thing netra knew about
 * that machine was a failing disk, and it should keep saying so. The window is
 * wide because the collector's interval is the operator's to set -- an hour by
 * default (AGENT_SMART_INTERVAL), and a daily or weekly schedule is a
 * reasonable thing to want on spinning rust.
 */
export const DRIVE_STALE_MS = 7 * 24 * 60 * 60 * 1000;

/**
 * Whether this drive was still being read when the host last reported.
 *
 * Unknown timestamps do not disqualify a drive: with no reference point the
 * honest answer is the reading netra holds, not silence about a disk that may
 * be dying.
 */
function driveIsCurrent(drive: Drive, hostLastSeen?: string | null): boolean {
  if (hostLastSeen === null || hostLastSeen === undefined) return true;
  const host = new Date(hostLastSeen).getTime();
  const seen = new Date(drive.last_seen).getTime();
  if (Number.isNaN(host) || Number.isNaN(seen)) return true;
  return host - seen <= DRIVE_STALE_MS;
}

/**
 * The findings serious enough to count against the HOST, worst first.
 *
 * This is the one place the drive table and the attention panels meet, and
 * both read it rather than re-deriving anything: netra used to rate a drive
 * `critical` on the Storage tab while the same host read clean on the fleet
 * page and in its own "Needs attention" list, because nothing carried the
 * verdict out of that one table.
 *
 * `warning` findings stay behind, and that is the whole judgement here. CRC
 * errors and 80% wear are worth seeing on the drive row, but neither counter
 * ever resets -- one cable glitch two years ago would park the host in the
 * attention list for the rest of its life, and a list nobody can ever clear
 * stops being read. What escalates is the states a drive does not come back
 * from on its own: pending and uncorrectable sectors, an NVMe critical
 * warning, spare below threshold, rated endurance spent, and the two counters
 * that mean the drive has already started substituting for damage.
 */
export function driveAlarms(
  drives: readonly Drive[],
  hostLastSeen?: string | null,
): DriveAlarm[] {
  const out: DriveAlarm[] = [];
  for (const drive of drives) {
    if (!driveIsCurrent(drive, hostLastSeen)) continue;
    for (const finding of driveFindings(drive)) {
      if (finding.severity !== "serious" && finding.severity !== "critical") {
        continue;
      }
      out.push({
        device: drive.device,
        severity: finding.severity,
        text: finding.text,
      });
    }
  }
  // Across drives as well as within one: a host with a spent NVMe and a disk
  // with one reallocated sector is a host with a spent NVMe. Stable, so two
  // alarms of equal severity keep drive order.
  return out.sort((a, b) => RANK[a.severity] - RANK[b.severity]);
}

/**
 * The drive's state as a number a column can be ORDERED by, worst highest.
 *
 * Inverted against RANK above, which is worst-first because it drives a
 * sort of findings within a row. A table column is read the other way round:
 * clicking a header once gives ascending, and the reader who clicks Findings
 * is looking for the drives in trouble, which have to arrive together at one
 * end rather than be split by an alphabetical accident.
 *
 * Lives here rather than in the table so RANK stays the single statement of
 * how these severities compare.
 */
export function driveSeverityRank(drive: Drive): number {
  const worst = driveSeverity(drive);
  return Object.keys(RANK).length - RANK[worst];
}

/**
 * Temperature in degrees Celsius, from whichever id this drive uses.
 *
 * ATA's attribute 194 needs masking and NVMe's 1008 does not, which is the
 * whole reason this is a function rather than two `attr` calls.
 *
 * The collector stores smartctl's raw field verbatim -- a 48-bit value, by
 * the schema's stated contract that the agent reports and does not interpret.
 * Many vendors pack the lifetime minimum and maximum into its upper words, so
 * a drive at 28 °C can arrive as 0x1C0000001C, which is 120259084316. Printed
 * unmasked that is a temperature column reading a hundred and twenty billion
 * degrees.
 *
 * The masking belongs here rather than in the collector for the reason
 * everything else in this file does: it is interpretation, and a rule shipped
 * inside an agent is frozen into every host in the fleet.
 *
 * The low byte, not the low word: a disk temperature in Celsius has never
 * needed more than eight bits, and the packing puts the current reading
 * there on every layout in the wild.
 *
 * The simulator writes clean single-byte values, so this case cannot appear
 * in a local fleet -- which is why it survived a browser check.
 */
export function driveTemperature(drive: Drive): number | null {
  const kind = driveKind(drive);
  return temperatureFromRaw(attr(drive, driveTempAttrId(drive)), kind);
}

/**
 * Which attribute id carries this drive's temperature.
 *
 * Separate from driveTemperature because the history needs it on its own: a
 * series arrives keyed by (device, attr_id) and has to be matched to the row
 * it belongs to before there is a value to convert.
 */
export function driveTempAttrId(drive: Drive): number {
  return driveKind(drive) === "nvme" ? NVME.temperature : ATA.temperature;
}

/**
 * One raw reading in degrees Celsius.
 *
 * Split out of driveTemperature so the masking rule above is applied ONCE and
 * to everything. The latest value and every point of a 24-hour series are the
 * same field read at different instants, and a chart that skipped the mask
 * would draw a flat line at a hundred and twenty billion beside a table cell
 * reading 28 °C.
 */
export function temperatureFromRaw(
  raw: number | null,
  kind: DriveKind,
): number | null {
  if (raw === null) return null;
  // smartctl reports the NVMe health log's temperature in degrees, already
  // converted from the raw log's Kelvin. Nothing is packed into it.
  if (kind === "nvme") return raw;
  return raw & 0xff;
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
