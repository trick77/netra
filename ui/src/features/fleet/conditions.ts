// What is wrong with the fleet, as the HUB decided it.
//
// This module used to derive every condition here, from whatever rows the page
// happened to have fetched. That was survivable while a person reading a page
// was the only consumer of the answer, and it stopped being survivable the
// moment anything else had to ask what is wrong -- an alerting engine cannot
// call into a browser.
//
// It also meant nothing recorded when a condition BEGAN. Four of the five
// kinds left their onset empty, because a derivation has no memory: a disk
// that filled at 03:00 and drained by 09:00 left no trace anywhere in netra.
// The hub opens a condition with an event and closes it with one now, and the
// row carries the onset. What is left here is RENDERING -- turning a row and
// its detail into the sentence and the mark a reader sees.
//
// The other half of the change is what is no longer here. The disk thresholds,
// the staleness window and the SMART alarm rules were all written out in this
// directory and in lib/host.ts and features/host/smart.ts, and no compiler in
// this repo could see across the boundary to the hub's copies -- so a change on
// one side was half a change. The thresholds that are still needed to colour a
// meter for a HEALTHY mount now arrive from the hub in the catalogue, rather
// than being restated.
//
// The fleet page used to render conditions as a band above the list: one block
// per host, capped at twenty, with the overflow written as "+30 more hosts"
// that was not a link. At fifty warned hosts out of a hundred that is a wall
// with no way past it, so the band is gone and the host list itself carries the
// conditions -- which is why every Condition names its KIND. A kind is what
// lets fifty hosts that all failed the same unit collapse to one line the
// reader can click, instead of fifty rows read one by one.
import type { ReactNode } from "react";
import type { ConditionKindInfo, ConditionRow } from "../../lib/api";
import type { Severity } from "../../ui/Badge";
import type { HostTab } from "../host/HostPage";
import { percent, relative } from "../../lib/format";

/**
 * Every kind of thing netra says about a host.
 *
 * A bare string, deliberately, where this was a hand-maintained union. The hub
 * owns which kinds exist -- internal/hub/conditions declares them and the
 * catalogue below carries them over the wire -- and a union here would be a
 * second list to keep in step, silently wrong for exactly as long as it took
 * somebody to notice a filter naming a kind TypeScript had never heard of.
 */
export type ConditionKind = string;

/**
 * The mark that PROVES a condition, chosen by the condition rather than by
 * the column.
 *
 * A row about a full filesystem used to be drawn beside the host's CPU and
 * memory sparklines, which say nothing about why the row is there and quietly
 * suggest CPU is the problem. What belongs next to "94% full" is the disk
 * meter; next to "2 failed units", the unit names.
 *
 * The honest cost: a column whose meaning changes per row cannot be sorted or
 * compared downward. That is acceptable here and nowhere else -- this is a
 * list of DIFFERENT problems, not a table of the same measurement.
 *
 * `reporting` carries no data because the row already holds that series;
 * naming it keeps the series out of a type that is otherwise cheap to build.
 */
export type Evidence =
  | { type: "meter"; pct: number }
  | { type: "units"; names: readonly string[]; extra: number }
  | { type: "reporting" }
  | null;

/**
 * A host-level condition worth surfacing. `what` is a ReactNode (not string)
 * so a caller can embed a value inline without this module's readers reaching
 * back into formatting logic they have no business owning.
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
   * When this started -- host_conditions.opened_ts, walked once when the
   * condition opened and never re-derived.
   *
   * This is the column that used to be empty for four kinds out of five, and
   * filling it is the point of the whole engine. A derivation could only ever
   * say a counter had moved; the obvious stand-ins -- the window start,
   * last_seen, now -- are all a timestamp a reader takes literally, and "since
   * 5 m ago" beside a disk that has been filling for a week is worse than
   * saying nothing.
   *
   * Still nullable, because one kind genuinely has no onset: `sporadic` is a
   * rate, and the gaps ARE the condition. Naming the first of them would date
   * it to a scrape the host happened to miss.
   */
  since: string | null;
  /**
   * `since` is a FLOOR rather than a moment.
   *
   * The hub's walk back through a mount's series hit the end of what is
   * retained -- raw samples are kept 7 days -- so the row says "over 7 d"
   * instead of naming a bucket where nothing happened.
   */
  sinceAtLeast?: boolean;
  /**
   * The subject is present and unmeasurable: still reported, and not re-read
   * for longer than its kind allows.
   *
   * The page used to make this disappear, dropping a mount whose reading was
   * three minutes old. That is the same lie the hub refuses to tell -- it
   * cannot distinguish a hung NFS export from an unmounted volume, so it keeps
   * the condition open rather than declaring a still-full disk recovered. The
   * row stays on screen and says the reading is old.
   */
  stale?: boolean;
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
 * The kind vocabulary, as the hub serves it.
 *
 * Every kind, present or not, and that is the whole reason it is fetched
 * rather than derived from the rows on screen: a label taken from the
 * conditions present disappears the moment the last host carrying that kind
 * recovers, and the page is then holding a filter it cannot name -- "Showing 0
 * of 100 hosts with", with a segment pressed for something no longer on
 * screen. A reader who followed a link to a kind that has since cleared
 * deserves to be told which kind cleared.
 */
export interface Catalogue {
  kinds: readonly ConditionKindInfo[];
  byKind: ReadonlyMap<string, ConditionKindInfo>;
}

export function catalogueOf(
  kinds: readonly ConditionKindInfo[] | null | undefined,
): Catalogue {
  // Anything that is not a list of kinds is no catalogue, not a crash. The
  // page renders against whatever the hub said, and an older hub that answers
  // without this field must leave the fleet list working rather than blanking
  // it -- an unnamed filter is a small loss, a page that threw is a total one.
  const list = Array.isArray(kinds) ? kinds : [];
  return { kinds: list, byKind: new Map(list.map((k) => [k.kind, k])) };
}

/**
 * The catalogue before the first response lands, and after one that failed.
 *
 * Empty rather than a hardcoded fallback, deliberately: a stand-in list would
 * be the copy of the hub's rules this change exists to delete, and it would be
 * indistinguishable from the real thing right up to the moment the two
 * disagreed. Everything below degrades to something honest on it -- an
 * unrecognised filter reads as "all", and a meter draws without a severity
 * colour rather than guessing one.
 */
export const EMPTY_CATALOGUE: Catalogue = catalogueOf([]);

/** The kind's name, or the kind itself for one the catalogue has not named. */
export function kindLabel(catalogue: Catalogue, kind: ConditionKind): string {
  return catalogue.byKind.get(kind)?.label ?? kind;
}

/**
 * The severity a kind ENTERS at, used only when nothing is carrying the kind
 * any more.
 *
 * A kind that IS present takes its severity from the conditions themselves
 * (see groupByKind), because one disk warns where another criticals and the
 * counts line must not understate that.
 */
export function kindSeverity(
  catalogue: Catalogue,
  kind: ConditionKind,
): Severity {
  return catalogue.byKind.get(kind)?.severity ?? "warning";
}

/** How full a filesystem has to be, and how little has to be left, before it
 * is worth someone's attention. The hub's numbers, carried on the wire.
 *
 * The rule is a CONJUNCTION and both halves are here for that reason. netra
 * used to say "/mnt/ark is 90% full -- 674.4 GB free" in one breath and expect
 * someone to act on it; what an operator runs out of is bytes. */
export interface DiskThresholds {
  warnPct: number;
  critPct: number;
  warnFree: number;
  critFree: number;
}

/**
 * The disk thresholds the hub judges by, or null before the catalogue lands.
 *
 * Null is not a default. There is no honest default: writing 90 and 95 here
 * would restore exactly the second copy that had the fleet page and the host
 * page disagreeing about one fact, and it would go on being wrong invisibly if
 * the hub's numbers ever moved. Callers draw without a severity instead, for
 * the one poll it takes.
 */
export function diskThresholds(catalogue: Catalogue): DiskThresholds | null {
  const t = catalogue.byKind.get("disk")?.thresholds;
  if (t === undefined) return null;
  return {
    warnPct: t.warn_pct,
    critPct: t.crit_pct,
    warnFree: t.warn_free,
    critFree: t.crit_free,
  };
}

/** How bad a filesystem is, or null for one nobody needs to look at. */
export type DiskSeverity = "critical" | "warning" | null;

/**
 * The severity a percentage earns given the headroom behind it.
 *
 * `free` is bytes, and null/undefined is "not known" rather than "none left":
 * a caller that cannot say how much room is left falls back to the percentage
 * alone. A row that has lost track of the bytes must not go silent about a
 * disk at 97%.
 *
 * This is NOT what decides a condition any more -- the hub does that, and this
 * agrees with it by taking its numbers. It survives because the fleet's Disk
 * meter ranks a host's mounts by severity before percentage and the host
 * page's disk tile colours itself the same way, and both have to judge mounts
 * that are perfectly healthy and that no condition will ever mention.
 */
export function diskSeverityFor(
  pct: number,
  free: number | null | undefined,
  thresholds: DiskThresholds | null,
): DiskSeverity {
  if (thresholds === null) return null;
  const room = free ?? null;
  if (
    pct >= thresholds.critPct &&
    (room === null || room < thresholds.critFree)
  ) {
    return "critical";
  }
  if (
    pct >= thresholds.warnPct &&
    (room === null || room < thresholds.warnFree)
  ) {
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
  thresholds: DiskThresholds | null,
): { pct: number; severity: DiskSeverity } | null {
  if (used === null || free === null) return null;
  const capacity = used + free;
  if (capacity === 0) return null;
  const pct = (used / capacity) * 100;
  return { pct, severity: diskSeverityFor(pct, free, thresholds) };
}

// Higher rank == worse. `ok` and `neutral` never appear in practice (a
// condition is definitionally something wrong), but are ranked lowest so a
// stray one sorts to the bottom rather than crashing.
const SEVERITY_RANK: Record<Severity, number> = {
  critical: 3,
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

/** Narrows an AttentionFilter to a kind -- and validates an arbitrary string,
 * which is what a URL parameter is.
 *
 * Validated against the CATALOGUE the hub served, so the browser is not
 * holding its own list of what exists. A ?attn= nobody recognises is "all",
 * never a filter that silently matches nothing -- and that includes every
 * value before the catalogue has landed, which is the honest reading for one
 * poll rather than a link that quietly filters to zero. */
export function isConditionKind(
  catalogue: Catalogue,
  value: string,
): value is ConditionKind {
  return catalogue.byKind.has(value);
}

/**
 * The kind a filter names, or null -- looked up in the catalogue rather than
 * in the conditions on screen, so a filter whose last host recovered can still
 * say what it is filtering to.
 */
export function filterKind(
  catalogue: Catalogue,
  filter: AttentionFilter,
): ConditionKind | null {
  return isConditionKind(catalogue, filter) ? filter : null;
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
 * the count is hosts, not conditions, because that is what the line says --
 * which is also what makes the per-host collapse in hostConditions free.
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

// --- Rendering the hub's rows -------------------------------------------

/** A condition's detail, narrowed rather than cast.
 *
 * `detail` is `unknown` on the wire on purpose -- its shape belongs to the
 * observer that produced it, not to the API -- so anything that is not a plain
 * object simply says nothing, exactly as the event log's own reader does. */
function fields(row: ConditionRow): Record<string, unknown> {
  if (typeof row.detail !== "object" || row.detail === null) return {};
  if (Array.isArray(row.detail)) return {};
  return row.detail as Record<string, unknown>;
}

function num(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

function str(v: unknown): string | null {
  return typeof v === "string" && v !== "" ? v : null;
}

/**
 * One deviation reading, in the unit it was measured in.
 *
 * Rounded to one decimal and no further, because the precision the hub sends
 * is not precision a reader can use: a baseline p99 arrives as 46.03921568...
 * and printing it would suggest the threshold is known to eight figures when
 * it is a percentile over a week of 60-second samples. One decimal is the
 * resolution a temperature sensor and a load average actually carry.
 *
 * Trailing ".0" is dropped so a process count reads "1842" rather than
 * "1842.0" -- the same number in a unit that has no fractions.
 */
function deviationValue(value: number | null, unit: string): string {
  if (value === null) return "";
  const rounded = Math.round(value * 10) / 10;
  const text = Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1);
  return unit === "" ? text : `${text} ${unit}`;
}

function names(v: unknown): string[] {
  if (!Array.isArray(v)) return [];
  return v.filter((one): one is string => typeof one === "string");
}

/**
 * The severity words the wire uses, narrowed to the ones a badge can draw.
 *
 * `ok` and `neutral` are UI vocabulary for the ABSENCE of a condition and can
 * never arrive here -- a condition is definitionally something wrong, and the
 * hub's own CHECK constraint allows only these two.
 */
function severityOf(row: ConditionRow): Severity {
  return row.severity === "critical" ? "critical" : "warning";
}

/**
 * Worst first, then by onset, then by subject.
 *
 * The tie-breaks are what stop the collapse below flickering. Two mounts on
 * one host at the same severity would otherwise take turns being named as the
 * page re-rendered, because the hub's row order is stable but the reason for
 * choosing between them was not.
 */
function worstRow(rows: readonly ConditionRow[]): ConditionRow {
  return [...rows].sort((a, b) => {
    const bySeverity =
      SEVERITY_RANK[severityOf(b)] - SEVERITY_RANK[severityOf(a)];
    if (bySeverity !== 0) return bySeverity;
    // Drive alarms carry an urgency the hub decided: everything that escalates
    // is critical, so severity alone cannot say whether an unreadable sector
    // or a counter that is merely climbing is the one to name.
    const urgency =
      (num(fields(a).urgency) ?? 0) - (num(fields(b).urgency) ?? 0);
    if (urgency !== 0) return urgency;
    const pct = (num(fields(b).pct) ?? 0) - (num(fields(a).pct) ?? 0);
    if (pct !== 0) return pct;
    return a.subject.localeCompare(b.subject);
  })[0];
}

/**
 * "— not measured since 4 h ago", appended to a stale subject's sentence.
 *
 * Said rather than hidden. The old page dropped a mount whose reading was
 * three minutes old, which silently retired the condition on it; the hub
 * refuses to make that call at all, because a hung NFS export and an unmounted
 * volume look identical from where it stands. So the row stays and the reader
 * is told the number beside it is old.
 */
function staleNote(row: ConditionRow, now: Date): string {
  if (!row.stale) return "";
  return ` — not measured since ${relative(row.measured_ts, now)}`;
}

/**
 * Everything wrong with one host, in a stable written order.
 *
 * Every condition names a MEASUREMENT and what it means, never a diagnosis:
 * "2 failed units" is a thing that is true, "the host is broken" is a guess
 * about why. Callers group and order these; ordering within a host is left as
 * written so the reading is stable.
 *
 * The per-mount and per-device rows COLLAPSE here. The hub keeps a condition
 * per mount and per device deliberately -- collapsed in the state machine, a
 * condition would open and close every time the fullest mount changed from
 * /var to /mnt, writing transition pairs that describe nothing -- and this is
 * the rendering decision that was always separate from it: one line per host,
 * because the unit of interest on a fleet list is the machine.
 */
export function hostConditions(
  rows: readonly ConditionRow[],
  catalogue: Catalogue,
  now: Date = new Date(),
): Condition[] {
  if (rows.length === 0) return [];
  const base = {
    hostId: String(rows[0].host_id),
    hostname: rows[0].hostname,
  };
  const byKind = new Map<string, ConditionRow[]>();
  for (const row of rows) {
    const existing = byKind.get(row.kind);
    if (existing) existing.push(row);
    else byKind.set(row.kind, [row]);
  }

  const of = (kind: string): ConditionRow | undefined => {
    const found = byKind.get(kind);
    return found === undefined ? undefined : worstRow(found);
  };

  const out: Condition[] = [];
  const common = (row: ConditionRow) => ({
    ...base,
    kind: row.kind,
    severity: severityOf(row),
    label: kindLabel(catalogue, row.kind),
    since: row.opened_ts,
    sinceAtLeast: row.opened_at_least,
    stale: row.stale,
  });

  // Reporting first, because it qualifies everything below it: a host that has
  // not spoken for an hour has stale disk and memory figures too, and saying so
  // first stops the rest reading as current.
  const silent = of("silent");
  if (silent !== undefined) {
    out.push({
      ...common(silent),
      what: "Stopped reporting — every figure here is its last known one",
      // The series stopping is the evidence, and the row already holds it.
      evidence: { type: "reporting" },
      // No tab explains a silent host better than the host page itself does.
      tab: null,
    });
  }

  const sporadic = of("sporadic");
  if (sporadic !== undefined) {
    out.push({
      ...common(sporadic),
      what: "Reporting sporadically — gaps in the last few hours",
      // A rate has no onset: the gaps ARE the condition, and naming the first
      // of them would date it to a scrape the host happened to miss. The hub
      // stamps this when it CONCLUDED it, which is a different and less
      // interesting fact, so the column stays empty rather than printing one.
      since: null,
      sinceAtLeast: false,
      evidence: { type: "reporting" },
      tab: null,
    });
  }

  const units = of("failed-units");
  if (units !== undefined) {
    const detail = fields(units);
    const count = num(detail.count) ?? 0;
    const shown = failedUnitsShown(count, names(detail.units));
    out.push({
      ...common(units),
      // The count alone: the names are the evidence beside it now, not part of
      // the sentence. Grouped by kind, thirty-one hosts print thirty-one
      // sentences, and repeating three unit names inside every one of them
      // made the column that says HOW MANY unreadable.
      what: `${count} failed ${count === 1 ? "unit" : "units"}`,
      evidence: { type: "units", ...shown },
      // The units tab lists every failed unit with its state and its restart
      // count -- the names beside this row are a summary of exactly that page.
      tab: "units",
    });
  }

  const disk = of("disk");
  if (disk !== undefined) {
    const detail = fields(disk);
    const pct = num(detail.pct) ?? 0;
    const mount = str(detail.mount) ?? disk.subject;
    out.push({
      ...common(disk),
      // "was", once this host has stopped reporting. The SEVERITY does not
      // move with the tense, and that is the judgement: a 96 % disk on a
      // machine that is off is still a 96 % disk, and it is worth clearing
      // before the machine comes back. The row already carries "Stopped
      // reporting" as its own critical condition, so both facts are on screen;
      // this one only stops claiming to describe this minute.
      what: `${mount} ${silent !== undefined ? "was" : "is"} ${percent(pct)} full${staleNote(disk, now)}`,
      evidence: { type: "meter", pct },
      // Only one mount is named here; the Storage tab is where this host's
      // other mounts are -- and now its disk charts too.
      tab: "storage",
    });
  }

  const drives = byKind.get("drive");
  if (drives !== undefined && drives.length > 0) {
    const worst = worstRow(drives);
    const detail = fields(worst);
    const device = str(detail.device) ?? worst.subject;
    const text = str(detail.text) ?? "";
    // Every alarm across every drive, not just the number of drives: two
    // findings on one disk are two things wrong, and the count has to add up
    // to what the Storage tab lists.
    const total = drives.reduce(
      (sum, row) => sum + (num(fields(row).alarms) ?? 1),
      0,
    );
    out.push({
      ...common(worst),
      // Names the drive and what is wrong with it. The count of the rest rides
      // along rather than expanding into rows -- see the collapse above.
      what:
        total <= 1
          ? `${device} — ${text}${staleNote(worst, now)}`
          : `${device} — ${text} (+${total - 1} more)${staleNote(worst, now)}`,
      // Deliberately none, and the hub does not offer one. SMART attributes
      // are counters with no zero baseline, sampled hourly: the first non-zero
      // reading netra holds is when netra started LOOKING, not when the sector
      // went bad. A drive whose agent was installed on Tuesday would claim its
      // sectors failed on Tuesday.
      since: null,
      sinceAtLeast: false,
      // Deliberately none. Evidence's marks are meter, units and reporting; a
      // raw attribute counter is none of them, and the finding it would be
      // drawn from is already the sentence above.
      evidence: null,
      // The Storage tab holds the Drives table, with every attribute this
      // sentence was derived from.
      tab: "storage",
    });
  }

  // Anything the hub raised that this renderer has no sentence for still
  // appears, named by the catalogue.
  //
  // A kind added hub-side reaches the page as a row before anybody writes its
  // prose here, and the alternative -- dropping it -- is a fleet that reads
  // clean because the browser did not recognise what was wrong with it. That
  // is the failure this whole module exists to end, so it must not be
  // reintroduced by an incomplete switch statement.
  // The deviation kinds, all three written by one builder because the sentence
  // is the same shape for each: what it reads now, and what it normally reads.
  //
  // THE SECOND HALF IS NOT DECORATION. "Load is 14.2" is a number a reader has
  // to already know this host to interpret, and the whole reason these kinds
  // are calibrated per subject is that nobody knows every host. "14.2, normally
  // under 6.1" carries its own comparison, so the row is readable by someone
  // who has never seen the machine before -- which on a fleet page is everyone.
  for (const kind of ["temperature", "processes", "load"] as const) {
    const all = byKind.get(kind);
    if (all === undefined || all.length === 0) continue;
    const row = worstRow(all);
    const detail = fields(row);
    const value = num(detail.value);

    // A row whose detail lost its reading STILL APPEARS, named by the
    // catalogue. Skipping it would be the one failure this module is built to
    // prevent: these kinds are in the `written` set below, so the catch-all
    // that rescues unrecognised kinds does not run for them either, and the
    // host would read clean on the fleet page while the hub had a condition
    // open on it. Terse beats absent -- see the note above `written`.
    if (value === null) {
      out.push({
        ...common(row),
        what: kindLabel(catalogue, kind),
        evidence: null,
        tab: "system",
      });
      continue;
    }

    const unit = str(detail.unit) ?? "";
    // Every sensor over its own line, not just the worst. A host with three
    // hot drives is three things wrong, and collapsing to one row silently
    // loses the other two -- the same count the drive row a few lines up
    // carries, for the same reason.
    const more = all.length - 1;
    const subject = kind === "temperature" ? `${row.subject} ` : "";
    const normally = num(detail.p99);

    // The vendor's own limit is a different sentence from a calibrated one,
    // and an operator deciding whether to act reads them differently: past the
    // drive's stated limit is a fact about the hardware, above its usual range
    // is a fact about the week.
    const because =
      str(detail.source) === "device"
        ? `past its ${deviationValue(num(detail.crit), unit)} limit`
        : normally === null
          ? ""
          : `normally under ${deviationValue(normally, unit)}`;

    out.push({
      ...common(row),
      what:
        `${subject}${silent !== undefined ? "was" : "is"} ` +
        `${deviationValue(value, unit)}${because === "" ? "" : ` — ${because}`}` +
        (more > 0 ? ` (+${more} more)` : "") +
        staleNote(row, now),
      // Deliberately none. Evidence's marks are meter, units and reporting: a
      // reading against a per-subject threshold is not a proportion of
      // anything, and drawing it as a meter would invent a full scale that
      // does not exist -- there is no "100 % hot".
      evidence: null,
      // The System tab, for all three: it holds SystemGraphs (load, processes)
      // and the Sensors charts below them, so every one of these readings has
      // its series one click away.
      tab: "system",
    });
  }

  const written = new Set([
    "silent",
    "sporadic",
    "failed-units",
    "disk",
    "drive",
    "temperature",
    "processes",
    "load",
  ]);
  for (const [kind, rows] of byKind) {
    if (written.has(kind)) continue;
    const row = worstRow(rows);
    out.push({
      ...common(row),
      what: kindLabel(catalogue, kind),
      evidence: null,
      tab: null,
    });
  }

  return out;
}

/** A host as the renderer needs it: enough to say a machine exists and has
 * never spoken. */
export interface ConditionHost {
  id: number;
  hostname: string;
  last_seen: string | null;
}

/**
 * The whole fleet's conditions, in host order.
 *
 * Ordering beyond that is the caller's job -- groupByHost ranks hosts by their
 * worst and groupByKind ranks kinds by theirs. Sorting here as well would be a
 * third ordering rule to keep in step with the other two.
 */
export function fleetConditions(
  rows: readonly ConditionRow[],
  hosts: readonly ConditionHost[],
  catalogue: Catalogue,
  now: Date = new Date(),
): Condition[] {
  const byHost = new Map<number, ConditionRow[]>();
  for (const row of rows) {
    const existing = byHost.get(row.host_id);
    if (existing) existing.push(row);
    else byHost.set(row.host_id, [row]);
  }

  const out: Condition[] = [];
  for (const host of hosts) {
    // A host that has NEVER reported, said by the page and by nothing else.
    //
    // The hub refuses to raise this, and it is right to: admin.CreateHost
    // inserts the row and hands over a token, and the operator installs the
    // agent minutes or hours later. A critical condition in that gap -- with
    // an event in the log an alerting engine reads -- says a machine has
    // stopped talking when it has not started yet, and clears two ticks after
    // the agent comes up, leaving a permanent opened/cleared pair describing
    // nothing but the provisioning.
    //
    // The PAGE has no such problem: it states what is true right now and
    // forgets it, which is exactly what this fact wants. One boolean off a
    // field the hosts list already carries, not a threshold rule -- so it is
    // not the kind of derivation this change deleted.
    if (host.last_seen === null) {
      out.push({
        hostId: String(host.id),
        hostname: host.hostname,
        kind: "silent",
        severity: "critical",
        label: kindLabel(catalogue, "silent"),
        what: "Has never reported",
        since: null,
        evidence: { type: "reporting" },
        tab: null,
      });
    }
    const rows = byHost.get(host.id);
    if (rows !== undefined) {
      out.push(...hostConditions(rows, catalogue, now));
    }
  }
  return out;
}

/**
 * How many distinct hosts the conditions cover.
 *
 * Exported for the same reason the counts line is built from the same list it
 * renders: the line above the list states a count, and the count has to be
 * derived from the same conditions the list filters by or the two disagree on
 * screen.
 */
export function hostsNeedingAttention(
  conditions: readonly Condition[],
): number {
  return new Set(conditions.map((c) => c.hostId)).size;
}
