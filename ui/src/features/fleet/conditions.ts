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
// true in the row.
//
// Most of what it states is free. Reporting, OOM kills and the fullest
// filesystem are read from data the page fetched for its sparklines, and the
// failed-unit count rides the hosts list the page already asks for. Two facts
// are not: buffer_dropped_total and post_failures_total only mean anything
// against the series around them, so a gauge on the list cannot carry either.
// They cost this page one more family per host -- see the note on
// fetchHostTrends in hostTrends.ts, which owns that fan-out. This module used
// to promise it cost no request that was not already made; that stopped being
// true when a host silently dropping samples was judged worth the request.
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
 * "3 failed units — borgbackup.service, docker.service +1".
 *
 * The count leads and comes from services_failed, which is the agent's own
 * summary; the names annotate it and come from the hub's unit rows. The two
 * are allowed to disagree -- a host heard from once has a summary and no unit
 * rows yet -- and every branch here resolves that disagreement in favour of
 * the count:
 *
 *   - no names at all: the count alone, exactly what this said before the
 *     hosts list carried names. "The hub cannot name them" is not "none".
 *   - fewer names than the count (the list caps at three, or the snapshot is
 *     behind): the names it has, then "+N" for the rest, so the sentence adds
 *     up to the count it opened with.
 *   - MORE names than the count: only as many as the count claims. A row
 *     reading "1 failed unit — a.service, b.service" contradicts itself in
 *     the same breath, and the count is the number every other part of netra
 *     is counting.
 */
export function failedUnitsText(
  count: number,
  names: readonly string[],
): string {
  const noun = count === 1 ? "unit" : "units";
  const head = `${count} failed ${noun}`;
  const shown = names.slice(0, Math.max(count, 0));
  if (shown.length === 0) return head;
  const rest = count - shown.length;
  return `${head} — ${shown.join(", ")}${rest > 0 ? ` +${rest}` : ""}`;
}

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
      // No tab explains a silent host better than the host page itself does.
      tab: null,
    });
  } else if (status.severity === "warning") {
    out.push({
      ...base,
      severity: "warning",
      what: "reporting sporadically — gaps in the last few hours",
      since: null,
      tab: null,
    });
  }

  // The agent's ring buffer overflowed: samples it collected were never
  // delivered. Nothing else netra reports about a host is more important,
  // because it is the one condition that says the rest of the row may be
  // incomplete -- and no sparkline can show it, since the missing data is
  // the evidence.
  //
  // The agent's running total, not this window's increase, and the same
  // number the host page prints -- see HostTrends.dropped in hostTrends.ts
  // for why an increase reads as 0 for exactly the host that dropped
  // something.
  if (row.dropped !== null && row.dropped > 0) {
    out.push({
      ...base,
      severity: "critical",
      what: `${row.dropped} ${row.dropped === 1 ? "sample" : "samples"} dropped before delivery — this host's history has holes`,
      since: null,
      tab: null,
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
      // Counted from a metric, not listed anywhere: no tab holds the list
      // this row would be summarising.
      tab: null,
    });
  }

  // The increase, for a sharper reason than the OOM counter above: this one
  // is cumulative for the life of the agent PROCESS and is never reset by a
  // success, so read as a latest value one hub restart pins "1 failed
  // delivery" here forever -- even though the ring buffer replayed those
  // samples the moment the hub came back and nothing was actually lost.
  if (row.postFailures !== null && row.postFailures > 0) {
    out.push({
      ...base,
      severity: "warning",
      what: `${row.postFailures} failed ${row.postFailures === 1 ? "delivery" : "deliveries"} to the hub in this window`,
      since: null,
      tab: null,
    });
  }

  // One condition for the whole set, never one per unit -- but it NAMES the
  // units, up to the three the hosts list carries. It used to state the count
  // alone, on the reasoning that a fleet row answers whether a host is worth
  // opening and the host's own page answers which unit. That was the wrong
  // half of the trade: "1 failed unit" is the count already answering the
  // worth-opening question, and withholding the one word that says whether it
  // is a backup job or the container runtime made the reader open the host to
  // find out. Eight unit names would still bury the next host in the band,
  // which is why the list is capped there and the count stays authoritative
  // here -- see read.HostSummary.FailedUnits.
  //
  // null is a host with no systemd at all, or one not yet heard from, and
  // stays silent -- netra has not looked, which is not the same as nothing
  // being wrong. 0 is the host confirming its units are fine, which is also
  // silence, but earned. The agent draws that line itself; the hub carries it.
  if (
    row.services_failed !== null &&
    row.services_failed !== undefined &&
    row.services_failed > 0
  ) {
    out.push({
      ...base,
      severity: "warning",
      what: failedUnitsText(row.services_failed, row.failed_units ?? []),
      since: null,
      // The units tab lists every failed unit with its state and its restart
      // count -- the names above are a summary of exactly that page.
      tab: "units",
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
      // Only the fullest mount is named here; the filesystems tab is where
      // this host's other mounts are.
      tab: "filesystems",
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
