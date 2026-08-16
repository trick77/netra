// What is wrong with the fleet, derived from the rows the page already has.
//
// AttentionBand has existed since the fleet page was built and has never had
// anything to render: FleetPage's `conditions` prop defaulted to [] with a
// note that computing them was "a separate Stage 2 workstream". So the band
// was dead code, and the page said "nothing needs attention" beside a host
// whose own detail page was showing three OOM kills in red -- the fleet
// overview disagreeing with the host it was summarising.
//
// This is not that alerting engine. It has no rules, no thresholds a user can
// set, no history and no notion of acknowledgement. It states what is already
// true in the row: every fact below is read from data the page fetched for
// its sparklines, so this costs no request that was not already made.
import type { Condition } from "./AttentionBand";
import type { HostRow } from "./hostColumns";
import { hostStatus } from "../../lib/host";
import { percent } from "../../lib/format";

/**
 * How full a filesystem has to be before it is worth someone's attention.
 *
 * The same two thresholds the host page's needsAttention() uses, and now
 * literally the same two constants: the host page imports these rather than
 * writing 90 and 95 out again. The two functions still read different shapes
 * -- the host page has every filesystem's bytes, this has one pre-picked
 * summary -- so only the numbers are shared, not the logic around them. That
 * is the part that had to stop drifting: a host that warns on its own page
 * and reads clean on the fleet page is the disagreement this whole module
 * exists to end.
 */
export const DISK_WARN_PCT = 90;
export const DISK_CRIT_PCT = 95;

/**
 * Everything wrong with one host, worst first.
 *
 * Every condition names a MEASUREMENT and what it means, never a diagnosis:
 * "3 OOM kills" is a thing that happened, "the host is out of memory" is a
 * guess about why. AttentionBand groups and orders these; ordering within a
 * host is left as written so the reading is stable.
 */
export function hostConditions(row: HostRow, now: Date): Condition[] {
  const out: Condition[] = [];
  const base = { hostId: String(row.id), hostname: row.hostname };

  // Reporting first, because it qualifies everything below it: a host that
  // has not spoken for an hour has stale disk and memory figures too, and
  // saying so first stops the rest reading as current.
  const status = hostStatus(row, now, row.reporting);
  if (status.severity === "critical") {
    out.push({
      ...base,
      severity: "critical",
      what:
        row.last_seen === null
          ? "has never reported"
          : "stopped reporting — every figure below is its last known one",
      // The one condition with an honest onset: this IS the timestamp.
      since: row.last_seen,
    });
  } else if (status.severity === "warning") {
    out.push({
      ...base,
      severity: "warning",
      what: "reporting sporadically — gaps in the last few hours",
      since: null,
    });
  }

  // Cumulative since boot, so this is the INCREASE across the window --
  // otherwise a host that killed something a year ago carries a permanent
  // condition. null means the window had no usable pair to difference, which
  // is "cannot say" and stays silent; 0 is the host confirming nothing
  // happened, which is also silence but a different kind.
  if (row.oomKills !== null && row.oomKills > 0) {
    out.push({
      ...base,
      severity: "critical",
      what: `${row.oomKills} OOM ${row.oomKills === 1 ? "kill" : "kills"} — the kernel killed processes to reclaim memory`,
      since: null,
    });
  }

  // df's Use%, already computed as used / (used + free) by fullestFilesystem.
  // Only the fullest one: the row carries a single pre-picked summary, and a
  // second mount at 91% is not a second thing to do -- the disk column
  // already says "+N".
  const fullest = row.fullest;
  if (fullest !== null && fullest.pct >= DISK_WARN_PCT) {
    out.push({
      ...base,
      severity: fullest.pct >= DISK_CRIT_PCT ? "critical" : "warning",
      what: `${fullest.mount} is ${percent(fullest.pct)} full`,
      since: null,
    });
  }

  return out;
}

/**
 * The whole fleet's conditions, in row order.
 *
 * Ordering by severity is AttentionBand's job, not this one's -- it groups by
 * host and ranks by each host's worst, so a host with one critical outranks a
 * host with four warnings. Sorting here as well would be a second ordering
 * rule to keep in step with the first.
 */
export function fleetConditions(
  rows: readonly HostRow[],
  now: Date,
): Condition[] {
  return rows.flatMap((row) => hostConditions(row, now));
}

/**
 * The memory figure a host row would show, for the summary line.
 *
 * Exported for the same reason the thresholds are constants: the line above
 * the band states a count, and the count has to be derived from the same
 * conditions the band renders or the two disagree on screen.
 */
export function hostsNeedingAttention(
  conditions: readonly Condition[],
): number {
  return new Set(conditions.map((c) => c.hostId)).size;
}
