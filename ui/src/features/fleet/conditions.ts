// What is wrong with the fleet, derived from the rows the page already has.
//
// This is not an alerting engine. It has no rules, no thresholds a user can
// set, no history and no notion of acknowledgement. It states what is already
// true in the row.
//
// Most of what it states is free. Reporting, OOM kills and the fullest
// filesystem are read from data the page fetched for its sparklines, and the
// failed-unit count rides the hosts list the page already asks for. Two facts
// are not: buffer_dropped_total and post_failures_total only mean anything
// against the series around them, so a gauge on the list cannot carry either.
// They cost this page one more family per host -- see the note on
// fetchHostTrends in hostTrends.ts, which owns that fan-out.
//
// The fleet page used to render these as a band above the list: one block per
// host, capped at twenty, with the overflow written as "+30 more hosts" that
// was not a link. At fifty warned hosts out of a hundred that is a wall with
// no way past it, so the band is gone and the host list itself carries the
// conditions -- which is why every Condition now names its KIND. A kind is
// what lets fifty hosts that all failed the same unit collapse to one line
// the reader can click, instead of fifty rows that have to be read one by
// one.
import type { ReactNode } from "react";
import type { Severity } from "../../ui/Badge";
import type { HostTab } from "../host/HostPage";
import type { HostRow } from "./hostColumns";
import { hostStatus } from "../../lib/host";
import { percent } from "../../lib/format";

/**
 * Every kind of thing netra says about a host, as a value rather than as a
 * sentence.
 *
 * The sentence in `what` cannot serve as the identity of a condition: it is a
 * ReactNode, it has the host's own numbers baked into it, and no two hosts
 * write it the same way. The kind is what the counts line groups by, what the
 * URL filter carries, and what tells an evidence cell which mark to draw.
 */
export type ConditionKind =
  | "silent"
  | "sporadic"
  | "dropped"
  | "oom"
  | "post-failures"
  | "failed-units"
  | "disk";

/**
 * The mark that PROVES a condition, chosen by the condition rather than by
 * the column.
 *
 * A row about a full filesystem used to be drawn beside the host's CPU and
 * memory sparklines, which say nothing about why the row is there and quietly
 * suggest CPU is the problem. What belongs next to "94% full" is the disk
 * meter; next to "2 failed units", the unit names; next to an OOM kill, the
 * memory series it happened in.
 *
 * The honest cost: a column whose meaning changes per row cannot be sorted or
 * compared downward. That is acceptable here and nowhere else -- this is a
 * list of DIFFERENT problems, not a table of the same measurement.
 *
 * `memory` and `reporting` carry no data because the row already holds those
 * series; naming them keeps the series out of a type that is otherwise cheap
 * to construct for every host on every render.
 */
export type Evidence =
  | { type: "meter"; pct: number }
  | { type: "units"; names: readonly string[]; extra: number }
  | { type: "memory" }
  | { type: "reporting" }
  | null;

/**
 * A host-level condition worth surfacing on the overview. `what` is a
 * ReactNode (not string) so a caller can embed a value inline (e.g. "disk
 * 92% full") without this module's readers reaching back into formatting
 * logic they have no business owning.
 */
export interface Condition {
  hostId: string;
  hostname: string;
  kind: ConditionKind;
  severity: Severity;
  /**
   * The kind's own name, identical for every host carrying it -- "Failed
   * units", "Filesystem nearly full". This is what the counts line prints; the
   * per-host detail lives in `what`. Sentence case, because it heads a count
   * rather than labelling a column.
   */
  label: string;
  /** What is wrong with THIS host, in its own numbers. */
  what: ReactNode;
  /**
   * When this started, when that is genuinely known -- and null when it is
   * not.
   *
   * Three kinds can answer honestly. A silent host has last_seen, which IS
   * the moment. A failed unit has systemd_units.state_ts, when it entered the
   * failed state; a host with five of them takes the OLDEST, because five
   * units failing at five times is one condition that began with the first.
   * A filesystem is walked back through its own series to the first sample
   * over the threshold, and says "over <window>" rather than a number when it
   * was already full when the window opened.
   *
   * The other four cannot. A counter delta over a window says only that the
   * total moved; the obvious stand-ins -- the window start, last_seen, now --
   * are all a timestamp the reader would take literally, and "since 5 m ago"
   * beside a disk that has been filling for a week is worse than saying
   * nothing. Those rows leave the column empty.
   */
  since: string | null;
  /**
   * `since` is a FLOOR rather than a moment.
   *
   * Only the filesystem walk can say this: netra cannot see past the range
   * the reader picked, so a disk that was already full when the window opened
   * gets the window's own start and this flag, and the row prints "over 24 h"
   * instead of naming a bucket where nothing happened. Optional because six
   * of the seven kinds never set it.
   */
  sinceAtLeast?: boolean;
  /** See Evidence. */
  evidence: Evidence;
  /**
   * The host tab that answers this condition in full, when one does.
   *
   * null is for the conditions with no such page -- a host that stopped
   * reporting is not explained better by any one tab, and a link that lands
   * somewhere unhelpful teaches people to stop following links.
   */
  tab: HostTab | null;
}

/**
 * How full a filesystem has to be before it is worth someone's attention.
 *
 * The same two thresholds the host page's needsAttention() uses, and
 * literally the same two constants: the host page imports these rather than
 * writing 90 and 95 out again. That is the part that had to stop drifting: a
 * host that warns on its own page and reads clean on the fleet page is the
 * disagreement this whole module exists to end.
 */
export const DISK_WARN_PCT = 90;
export const DISK_CRIT_PCT = 95;

/**
 * How little room has to be LEFT before a percentage means anything.
 *
 * A percentage on its own is the wrong unit for a disk. Ten per cent of a
 * 20 GB root is 2 GB and genuinely urgent; ten per cent of a 6.7 TB array is
 * 674 GB and a week of headroom, and netra used to say "/mnt/ark is 90% full
 * -- 674.4 GB free" in one breath and expect someone to act on it. What an
 * operator actually runs out of is bytes.
 *
 * So both halves have to agree: the disk is a high proportion full AND there
 * is little enough left that filling it is near.
 *
 * Where each floor starts to bite follows from its own percentage, and they
 * are NOT the same point. At 90% the tenth that is left passes 100 GiB once
 * the volume is over a terabyte, so nothing under that size warns any
 * differently than before. At 95% the twentieth that is left passes 20 GiB at
 * about 400 GiB of capacity -- so a 512 GB SSD at 96%, which has 20.5 GB
 * free, is now a warning where it used to be critical, and stays one until
 * roughly 96.1%. That is the intended reading of "critical": twenty gigabytes
 * is the point where filling up is hours away, and five per cent of a
 * half-terabyte disk is not.
 */
export const DISK_WARN_FREE = 100 * 1024 ** 3;
export const DISK_CRIT_FREE = 20 * 1024 ** 3;

/** How bad a filesystem is, or null for one nobody needs to look at. */
export type DiskSeverity = "critical" | "warning" | null;

/**
 * The severity a percentage earns given the headroom behind it.
 *
 * `free` is bytes, and `null`/undefined is "not known" rather than "none
 * left": a caller that cannot say how much room is left falls back to the
 * percentage alone. A row that has lost track of the bytes must not go silent
 * about a disk at 97%.
 */
export function diskSeverityFor(
  pct: number,
  free: number | null | undefined,
): DiskSeverity {
  const room = free ?? null;
  if (pct >= DISK_CRIT_PCT && (room === null || room < DISK_CRIT_FREE)) {
    return "critical";
  }
  if (pct >= DISK_WARN_PCT && (room === null || room < DISK_WARN_FREE)) {
    return "warning";
  }
  return null;
}

/**
 * df's Use% for one filesystem, plus what that percentage is worth.
 *
 * used / (used + free), never used / total: total includes the root reserve,
 * so dividing by it reports a disk as less full than df does -- the number an
 * operator has already seen over SSH. null is a filesystem with nothing
 * measurable behind it, which is not the same as an empty one.
 */
export function diskState(
  used: number | null,
  free: number | null,
): { pct: number; severity: DiskSeverity } | null {
  if (used === null || free === null) return null;
  const capacity = used + free;
  if (capacity === 0) return null;
  const pct = (used / capacity) * 100;
  return { pct, severity: diskSeverityFor(pct, free) };
}

// Higher rank == worse. `ok` and `neutral` never appear in practice (a
// condition is definitionally something wrong), but are ranked lowest so a
// stray one sorts to the bottom rather than crashing.
const SEVERITY_RANK: Record<Severity, number> = {
  critical: 4,
  serious: 3,
  warning: 2,
  ok: 1,
  neutral: 0,
};

export function worstOf(conditions: readonly Condition[]): Condition {
  return conditions.reduce((worst, c) =>
    SEVERITY_RANK[c.severity] > SEVERITY_RANK[worst.severity] ? c : worst,
  );
}

export interface HostGroup {
  hostId: string;
  hostname: string;
  /** Every condition for this host, worst first -- grouping is presentation,
   * never suppression, so nothing is dropped here. */
  conditions: Condition[];
  /** The single worst condition, used as this group's sort key. */
  worst: Condition;
}

/**
 * Groups conditions by host and orders the groups by each host's WORST
 * condition, not by how many conditions it has -- a host with one critical
 * outranks a host with four warnings, so a noisy-but-healthy host never
 * displaces a genuinely broken one. Within a host the conditions are sorted
 * worst-first too, with a stable sort, so hostConditions()'s own ordering
 * survives inside each severity -- reporting still leads the criticals, which
 * is the whole reason it is written first.
 *
 * Lives here rather than in a component so the ordering rule is unit-testable
 * without rendering anything. It outlived the band that used to own it.
 */
export function groupByHost(conditions: readonly Condition[]): HostGroup[] {
  const byHost = new Map<string, Condition[]>();
  for (const c of conditions) {
    const existing = byHost.get(c.hostId);
    if (existing) {
      existing.push(c);
    } else {
      byHost.set(c.hostId, [c]);
    }
  }
  const groups: HostGroup[] = Array.from(byHost.entries()).map(
    ([hostId, hostConditions]) => ({
      hostId,
      hostname: hostConditions[0].hostname,
      conditions: [...hostConditions].sort(
        (a, b) => SEVERITY_RANK[b.severity] - SEVERITY_RANK[a.severity],
      ),
      worst: worstOf(hostConditions),
    }),
  );
  groups.sort(
    (a, b) => SEVERITY_RANK[b.worst.severity] - SEVERITY_RANK[a.worst.severity],
  );
  return groups;
}

/**
 * Which part of what is wrong the reader is looking at.
 *
 * "all" is the fleet as a monitoring list; a severity is the hosts whose
 * WORST condition is that bad; a kind is the hosts carrying that one
 * condition. One value rather than two independent filters, because they
 * answer one question and because the control that shows it must always have
 * exactly one option selected.
 */
export type AttentionFilter = "all" | "critical" | "warning" | ConditionKind;

/**
 * Every kind's name and the severity it is normally at.
 *
 * Static, and that is the point: a label derived from the conditions actually
 * present disappears the moment the last host carrying that kind recovers,
 * and the page is then holding a filter it cannot name -- "Showing 0 of 100
 * hosts with · show all", with a severity segment pressed for something that
 * is no longer on screen. A reader who followed a link to a kind that has
 * since cleared deserves to be told which kind cleared.
 *
 * The severity here is the kind's ENTRY severity, used only when nothing is
 * carrying the kind any more; a kind that is present takes its severity from
 * the conditions themselves (see groupByKind), because one disk warns where
 * another criticals and the counts line must not understate that.
 */
const CONDITION_KIND_INFO: Record<
  ConditionKind,
  { label: string; severity: Severity }
> = {
  silent: { label: "Stopped reporting", severity: "critical" },
  sporadic: { label: "Reporting sporadically", severity: "warning" },
  dropped: { label: "Samples dropped", severity: "critical" },
  oom: { label: "OOM kills", severity: "critical" },
  "post-failures": { label: "Failed deliveries", severity: "warning" },
  "failed-units": { label: "Failed units", severity: "warning" },
  disk: { label: "Filesystem nearly full", severity: "warning" },
};

const CONDITION_KINDS = Object.keys(
  CONDITION_KIND_INFO,
) as readonly ConditionKind[];

/** The kind's name, with no data needed to produce it. */
export function kindLabel(kind: ConditionKind): string {
  return CONDITION_KIND_INFO[kind].label;
}

/** The severity a kind enters at -- see CONDITION_KIND_INFO. */
export function kindSeverity(kind: ConditionKind): Severity {
  return CONDITION_KIND_INFO[kind].severity;
}

/** Narrows an AttentionFilter to a kind -- and validates an arbitrary string,
 * which is what a URL parameter is. A ?attn= nobody recognises is "all",
 * never a filter that silently matches nothing. */
export function isConditionKind(value: string): value is ConditionKind {
  return (CONDITION_KINDS as readonly string[]).includes(value);
}

/**
 * The kind a filter names, or null -- looked up in the static table rather
 * than in the conditions on screen, so a filter whose last host recovered can
 * still say what it is filtering to.
 */
export function filterKind(filter: AttentionFilter): ConditionKind | null {
  return isConditionKind(filter) ? filter : null;
}

export interface KindGroup {
  kind: ConditionKind;
  /** The worst severity any host carries this kind at: the disk rule warns
   * and criticals at different points, so one kind can be both, and a counts
   * line that dotted it warning while a host is out of room would understate
   * the fleet. */
  severity: Severity;
  label: string;
  /** Hosts carrying this kind, in the order the rows were read. */
  hostIds: string[];
}

/**
 * The counts line: one entry per kind that is actually present, worst kind
 * first.
 *
 * This is the whole answer to fifty warnings on a hundred hosts. Thirty-one
 * hosts that failed the same unit are one line reading "Failed units 31",
 * because a fleet-wide problem is one problem -- and the thirty-one hosts are
 * one click away rather than thirty-one rows already on screen.
 *
 * A host is counted once per kind even if it somehow produced the kind twice;
 * the count is hosts, not conditions, because that is what the line says.
 */
export function groupByKind(conditions: readonly Condition[]): KindGroup[] {
  const byKind = new Map<ConditionKind, KindGroup>();
  for (const c of conditions) {
    const existing = byKind.get(c.kind);
    if (existing === undefined) {
      byKind.set(c.kind, {
        kind: c.kind,
        severity: c.severity,
        label: c.label,
        hostIds: [c.hostId],
      });
      continue;
    }
    if (SEVERITY_RANK[c.severity] > SEVERITY_RANK[existing.severity]) {
      existing.severity = c.severity;
    }
    if (!existing.hostIds.includes(c.hostId)) existing.hostIds.push(c.hostId);
  }
  return Array.from(byKind.values()).sort(
    (a, b) => SEVERITY_RANK[b.severity] - SEVERITY_RANK[a.severity],
  );
}

/**
 * Which unit names a row may show, and how many it is not showing.
 *
 * The count leads and comes from services_failed, which is the agent's own
 * summary; the names annotate it and come from the hub's unit rows. The two
 * are allowed to disagree -- a host heard from once has a summary and no unit
 * rows yet -- and every branch here resolves that disagreement in favour of
 * the count:
 *
 *   - no names at all: nothing to show, and `extra` is the whole count. "The
 *     hub cannot name them" is not "none failed".
 *   - fewer names than the count (the list caps at three, or the snapshot is
 *     behind): the names it has, and the rest as "+N", so what is drawn adds
 *     up to the count beside it.
 *   - MORE names than the count: only as many as the count claims. A row
 *     reading "1 failed unit" beside "a.service, b.service" contradicts
 *     itself, and the count is the number every other part of netra is
 *     counting.
 */
export function failedUnitsShown(
  count: number,
  names: readonly string[],
): { names: readonly string[]; extra: number } {
  const shown = names.slice(0, Math.max(count, 0));
  return { names: shown, extra: Math.max(count - shown.length, 0) };
}

/**
 * Everything wrong with one host, in a stable written order.
 *
 * Every condition names a MEASUREMENT and what it means, never a diagnosis:
 * "3 OOM kills" is a thing that happened, "the host is out of memory" is a
 * guess about why. Callers group and order these; ordering within a host is
 * left as written so the reading is stable.
 */
export function hostConditions(row: HostRow, now: Date): Condition[] {
  const out: Condition[] = [];
  const base = { hostId: String(row.id), hostname: row.hostname };
  // Every `what` below starts a cell of its own now, so it is capitalised as
  // a sentence -- these used to trail a hostname inside a band row, where
  // "web-01 stopped reporting" read as one line of prose.

  // Reporting first, because it qualifies everything below it: a host that
  // has not spoken for an hour has stale disk and memory figures too, and
  // saying so first stops the rest reading as current.
  const status = hostStatus(row, now, row.reporting);
  if (status.severity === "critical") {
    out.push({
      ...base,
      kind: "silent",
      severity: "critical",
      label: kindLabel("silent"),
      what:
        row.last_seen === null
          ? "Has never reported"
          : "Stopped reporting — every figure here is its last known one",
      // The one condition whose onset needs no derivation: this IS the
      // timestamp.
      since: row.last_seen,
      // The series stopping is the evidence, and the row already holds it.
      evidence: { type: "reporting" },
      // No tab explains a silent host better than the host page itself does.
      tab: null,
    });
  } else if (status.severity === "warning") {
    out.push({
      ...base,
      kind: "sporadic",
      severity: "warning",
      label: kindLabel("sporadic"),
      what: "Reporting sporadically — gaps in the last few hours",
      since: null,
      evidence: { type: "reporting" },
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
      kind: "dropped",
      severity: "critical",
      label: kindLabel("dropped"),
      what: `${row.dropped} ${row.dropped === 1 ? "sample" : "samples"} dropped before delivery — this host's history has holes`,
      since: null,
      // Deliberately none. The evidence is data that is not there, and every
      // mark this column can draw would be drawn out of the samples that DID
      // arrive -- which is the half that is not in question.
      evidence: null,
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
      kind: "oom",
      severity: "critical",
      label: kindLabel("oom"),
      what: `${row.oomKills} OOM ${row.oomKills === 1 ? "kill" : "kills"} — the kernel killed processes to reclaim memory`,
      since: null,
      evidence: { type: "memory" },
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
      kind: "post-failures",
      severity: "warning",
      label: kindLabel("post-failures"),
      what: `${row.postFailures} failed ${row.postFailures === 1 ? "delivery" : "deliveries"} to the hub in this window`,
      since: null,
      evidence: null,
      tab: null,
    });
  }

  // One condition for the whole set, never one per unit -- but it NAMES the
  // units, up to the three the hosts list carries. Eight unit names would
  // bury the next host, which is why the list is capped there and the count
  // stays authoritative here -- see read.HostSummary.FailedUnits.
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
    const shown = failedUnitsShown(row.services_failed, row.failed_units ?? []);
    out.push({
      ...base,
      kind: "failed-units",
      severity: "warning",
      label: kindLabel("failed-units"),
      // The count alone: the names are the evidence beside it now, not part
      // of the sentence. Grouped by kind, thirty-one hosts print thirty-one
      // sentences, and repeating three unit names inside every one of them
      // made the column that says HOW MANY unreadable.
      what: `${row.services_failed} failed ${row.services_failed === 1 ? "unit" : "units"}`,
      // The oldest state_ts of this host's failed units -- see Condition.
      // null when the hub has no unit rows for it yet, or when systemd
      // reported no timestamp.
      since: row.failed_since ?? null,
      evidence: { type: "units", ...shown },
      // The units tab lists every failed unit with its state and its restart
      // count -- the names beside this row are a summary of exactly that page.
      tab: "units",
    });
  }

  // df's Use%, already computed as used / (used + free) by fullestFilesystem,
  // which also passes the winner's remaining bytes through -- the percentage
  // alone cannot say whether this is worth waking up for. Only the fullest
  // one: the row carries a single pre-picked summary, and a second mount at
  // 91% is not a second thing to do -- the disk column already says "+N".
  const fullest = row.fullest;
  const fullestSeverity =
    fullest === null ? null : diskSeverityFor(fullest.pct, fullest.free);
  if (fullest !== null && fullestSeverity !== null) {
    out.push({
      ...base,
      kind: "disk",
      severity: fullestSeverity,
      label: kindLabel("disk"),
      what: `${fullest.mount} is ${percent(fullest.pct)} full`,
      // Walked back through THIS mount's own series -- see fullestFilesystem
      // in hostTrends.ts, which owns the walk because it is the only place
      // that knows which series the mount came from.
      since: fullest.since ?? null,
      sinceAtLeast: fullest.sinceAtLeast ?? false,
      evidence: { type: "meter", pct: fullest.pct },
      // Only the fullest mount is named here; the Storage tab is where this
      // host's other mounts are -- and now its disk charts too.
      tab: "storage",
    });
  }

  return out;
}

/**
 * The whole fleet's conditions, in row order.
 *
 * Ordering is the caller's job, not this one's -- groupByHost ranks hosts by
 * their worst and groupByKind ranks kinds by theirs. Sorting here as well
 * would be a third ordering rule to keep in step with the other two.
 */
export function fleetConditions(
  rows: readonly HostRow[],
  now: Date,
): Condition[] {
  return rows.flatMap((row) => hostConditions(row, now));
}

/**
 * How many distinct hosts the conditions cover.
 *
 * Exported for the same reason the thresholds are constants: the line above
 * the list states a count, and the count has to be derived from the same
 * conditions the list renders or the two disagree on screen.
 */
export function hostsNeedingAttention(
  conditions: readonly Condition[],
): number {
  return new Set(conditions.map((c) => c.hostId)).size;
}
