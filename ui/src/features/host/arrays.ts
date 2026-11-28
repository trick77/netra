// md arrays: the words and figures the Storage tab, the Overview and the drive
// rows say about them. /proc/mdstat's vocabulary, from the agent's snapshot.

import type { MdArray } from "../../lib/api";

/** True while md is working on the array: a check, a repair, a rebuild.
 * "frozen" is a sync held, not running, and "" a level without one (raid0).
 * The same words store/deviationscan.go leaves out of md_sync. */
export function syncing(a: Pick<MdArray, "sync_action">): boolean {
  return (
    a.sync_action !== "" &&
    a.sync_action !== "idle" &&
    a.sync_action !== "frozen"
  );
}

/** The drive a member sits on: sdd1 → sdd, nvme0n1p2 → nvme0n1, sdf → sdf. */
export function memberDrive(device: string): string {
  const p = /^(nvme\d+n\d+|mmcblk\d+|nbd\d+)p\d+$/.exec(device);
  if (p) return p[1]!;
  const d = /^((?:sd|vd|xvd|hd)[a-z]+)\d+$/.exec(device);
  if (d) return d[1]!;
  return device;
}

/** The arrays each drive belongs to, by drive name, in array order. */
export function arraysByDrive(
  arrays: readonly MdArray[],
): Map<string, string[]> {
  const out = new Map<string, string[]>();
  for (const a of arrays) {
    for (const m of a.members) {
      const drive = memberDrive(m.device);
      const names = out.get(drive) ?? [];
      if (!names.includes(a.name)) names.push(a.name);
      out.set(drive, names);
    }
  }
  return out;
}

/** mdstat's [UU_]: one letter per slot, U for a member in sync. */
export function slotMap(a: MdArray): string {
  const n = Math.max(a.raid_disks, 0);
  const slots = Array.from({ length: n }, () => "_");
  for (const m of a.members) {
    if (m.slot === undefined || m.slot >= n) continue;
    const states = m.state.split(",");
    if (states.includes("in_sync") && !states.includes("faulty"))
      slots[m.slot] = "U";
  }
  return `[${slots.join("")}]`;
}

/** "1 h 45 min", "12 min", "< 1 min". */
export function etaText(seconds: number): string {
  if (seconds < 60) return "< 1 min";
  const m = Math.round(seconds / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  const rest = m % 60;
  return rest === 0 ? `${h} h` : `${h} h ${rest} min`;
}

/** "md2 check 89.6 %, 1 h 45 min left": one array at work, in a line. */
export function syncLine(a: MdArray): string {
  const parts = [a.name, a.sync_action];
  if (a.progress !== null) parts.push(`${(a.progress * 100).toFixed(1)} %`);
  const head = parts.join(" ");
  return a.eta_s !== null ? `${head}, ${etaText(a.eta_s)} left` : head;
}
