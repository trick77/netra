// The Overview's tiles, derived rather than rendered.
//
// This module answers "what does each tile say" and nothing about how it
// looks. That split is what makes the tiles testable: every rule below --
// which column a tile reads, what an absent column renders as, when a reading
// earns a status hue -- is a pure function of a MetricsResponse and a
// HostDetail, checkable without a DOM.
import type { HostDetail, MetricsResponse } from "../../../lib/api";
import {
  carriesColumn,
  griddedValues,
  fsName,
  hasReading,
  latestValue,
  optionalValues,
} from "../../../lib/metrics";
import {
  ABSENT,
  binaryBytes,
  byterate,
  cardinal,
  percent,
} from "../../../lib/format";
import { isReporting } from "../../../lib/host";
import { severityFromPercent, type FillSeverity } from "../../../ui/Meter";
// The fleet's disk thresholds, imported rather than restated: the tile, the
// Disk meters below it, the attention band above it and the fleet row a
// reader arrived from must not disagree about one filesystem.
import { diskState } from "../../fleet/conditions";
// The one derivation of a host's traffic pair, shared with the fleet row.
import { trafficSeries } from "../../fleet/hostTrends";

/**
 * The most recent non-null reading, or null when there is none. A null here
 * means "the host reported nothing", which every caller renders as a gap or
 * as a word -- never as 0.
 *
 * Moved out of Overview.tsx with current() below, because both files need
 * them now and a second copy of either is how the page and its tiles would
 * come to answer differently about the same column.
 */
export function latest(
  res: MetricsResponse | null,
  base: string,
  seriesIndex = 0,
): number | null {
  const vals = optionalValues(res, seriesIndex, base);
  for (let i = vals.length - 1; i >= 0; i--) {
    const v = vals[i];
    if (v !== null) return v;
  }
  return null;
}

/**
 * The reading in the LATEST bucket of the window, or null when the series
 * does not reach it.
 *
 * latest() above answers "what did this series last say", which is right for
 * a configured ceiling and wrong for a filesystem: a row whose agent stopped
 * writing it keeps handing back its final measurement, and nothing
 * downstream can tell that from a current one. That is how one disk came to
 * warn twice under two names -- /netra/fs/ark frozen at the moment its agent
 * was upgraded, /mnt/ark live, both at 94 %, both stated as facts about now.
 *
 * griddedValues, not optionalValues, and that is the whole mechanism:
 * internal/hub/read/metrics.go emits only the rows that EXIST, so a series
 * that stopped simply ends early and its last element is its last reading,
 * indistinguishable from a current one. Placing it on the window's grid
 * turns the buckets it never reached into the nulls that say so.
 *
 * lib/metrics.ts:latestValue is the one spelling of the rule itself; see its
 * docstring on why the two questions must never share a name.
 */
export function current(
  res: MetricsResponse | null,
  base: string,
  seriesIndex = 0,
): number | null {
  return latestValue(griddedValues(res, seriesIndex, base));
}

export interface FilesystemRow {
  label: string;
  total: number | null;
  used: number | null;
  free: number | null;
}

/**
 * One row per filesystem, in bytes as measured.
 *
 * No percentage is computed here, and that is a schema fact rather than a
 * layout preference: internal/hub/read/family.go records that `used` and
 * `free` do not sum to `total` (the gap is the root reserve, which is
 * neither in use nor allocatable), and that at the 5m/1h tiers a ratio
 * built from used_max and free_min composes two different instants and is
 * not the maximum of the true ratio. Absolute bytes are the only figure
 * that stays true at every tier.
 *
 * Here rather than in Overview.tsx because current() is here now and this is
 * its other caller. Overview imports it back for the Disk card.
 */
export function filesystemRows(res: MetricsResponse | null): FilesystemRow[] {
  // `== null`, not `=== null`: these props are optional, so a caller that
  // simply does not have this family yet passes undefined -- and reading
  // .series off it threw during render, with no error boundary under it.
  if (res == null) return [];
  return res.series.map((series, index) => ({
    // The mount point, same as the fleet row: one disk must not be called
    // /mnt/ark on one page and ark on the other.
    label: fsName(series.key, ABSENT),
    // current(), not latest(): a filesystem that has stopped reporting has no
    // fullness right now, and saying otherwise is what kept a retired row on
    // the page beside the one that replaced it. The card renders the absent
    // marker for the nulls and diskWarnings already skips them, so the disk
    // stays listed -- it is only its numbers that stop claiming to be current.
    total: current(res, "total", index),
    used: current(res, "used", index),
    free: current(res, "free", index),
  }));
}

/** The four cards the tiles are grouped into. Order is the order they are
 * laid out in -- see Overview.tsx. */
export type TileGroup = "system" | "kernel" | "pressure" | "network";

export interface Tile {
  /** React key, and what a test finds the tile by. */
  key: string;
  label: string;
  /** Already formatted -- ABSENT when the reading is null. */
  value: string;
  unit?: string;
  sub?: string;
  values: (number | null)[];
  color: string;
  /**
   * null, never "ok". severityFromPercent answers "ok" for a healthy
   * reading, and a green tile would make a hue on this page mean "someone
   * checked" rather than "look at this" -- see StatTile's own note.
   */
  severity: FillSeverity | null;
  /**
   * The chart slug this tile summarises, or undefined when no panel draws
   * this column. Every tile here has one; the field is optional so a future
   * reading without a chart is an inert tile rather than a broken link.
   */
  slug?: string;
}

const STATUS_COLOR: Record<FillSeverity, string> = {
  ok: "var(--st-ok)",
  warning: "var(--st-warn)",
  serious: "var(--st-serious)",
  critical: "var(--st-crit)",
};

/** A tile that is off draws its trend in the status hue, not in its series
 * colour: the tint, the figure and the line have to agree, or the tile says
 * "warning" in three places and "normal blue" in a fourth. */
function trendColor(severity: FillSeverity | null, series: string): string {
  return severity === null ? series : STATUS_COLOR[severity];
}

/** Worse sorts higher. Only the three diskState can answer with, plus the
 * null it answers with for a healthy disk. */
const SEVERITY_RANK: Record<string, number> = {
  critical: 3,
  serious: 2,
  warning: 1,
};

function severityRank(severity: FillSeverity | null): number {
  return severity === null ? 0 : (SEVERITY_RANK[severity] ?? 0);
}

/** "ok" is not a status treatment here. See Tile.severity. */
function offOnly(severity: FillSeverity): FillSeverity | null {
  return severity === "ok" ? null : severity;
}

/**
 * A percentage series built bucket by bucket from a numerator and a
 * denominator.
 *
 * Elementwise rather than "divide by the latest total": mem_total moves when
 * a VM is resized and swap_total moves when a swapfile is added, and dividing
 * yesterday's usage by today's capacity draws a step that never happened. A
 * bucket missing either side is null -- unknown, not zero.
 */
function ratioSeries(
  used: (number | null)[],
  total: (number | null)[],
): (number | null)[] {
  return used.map((u, i) => {
    const t = total[i];
    if (u === null || t == null || t === 0) return null;
    return (u / t) * 100;
  });
}

/** One rate off the host family, as a count per second. */
function rateTile(
  res: MetricsResponse | null,
  key: string,
  label: string,
  base: string,
  slug: string,
  color: string,
  sub?: string,
): Tile {
  return {
    key,
    label,
    value: cardinal(current(res, base)),
    unit: "/s",
    sub,
    values: griddedValues(res, 0, base),
    color,
    severity: null,
    slug,
  };
}

/** One level off the host family -- a count of things that exist right now,
 * not a rate. No "/s". */
function levelTile(
  res: MetricsResponse | null,
  key: string,
  label: string,
  base: string,
  slug: string,
  color: string,
  sub?: string,
): Tile {
  return {
    key,
    label,
    value: cardinal(current(res, base)),
    sub,
    values: griddedValues(res, 0, base),
    color,
    severity: null,
    slug,
  };
}

export interface TileInput {
  host: HostDetail;
  hostMetrics: MetricsResponse | null;
  filesystemMetrics: MetricsResponse | null;
  netMetrics?: MetricsResponse | null;
  /** Injected by tests so "is this host reporting" is deterministic. */
  now?: Date;
}

/**
 * Every tile on the page, grouped by the card it sits in.
 *
 * A group is returned even when its host reports none of its columns: the
 * tiles inside render ABSENT, which is the honest answer, and a card that
 * appeared and disappeared per host would move every card below it. The one
 * exception is swap, which a host can legitimately not have at all -- see
 * there.
 */
export function overviewTiles(input: TileInput): Record<TileGroup, Tile[]> {
  const { host, hostMetrics, filesystemMetrics, netMetrics, now } = input;

  return {
    system: systemTiles(host, hostMetrics, filesystemMetrics),
    kernel: kernelTiles(hostMetrics),
    pressure: pressureTiles(hostMetrics),
    network: networkTiles(host, hostMetrics, netMetrics ?? null, now),
  };
}

function systemTiles(
  host: HostDetail,
  hostMetrics: MetricsResponse | null,
  filesystemMetrics: MetricsResponse | null,
): Tile[] {
  const tiles: Tile[] = [];

  // CPU carries the same threshold the fleet row already reads it by. The
  // fleet's CPU cell hands its percentage to NowReading with no severity of
  // its own, and NowReading falls through to severityFromPercent against
  // DEFAULT_THRESHOLDS -- so 96% is red in the row a reader arrived from,
  // and leaving the tile plain was the disagreement, not the fix for one.
  // Nothing new is being judged here either: this is the rule memory and
  // swap below already use, and the one every meter and sparkline in the
  // app is drawn by.
  const cpuNow = current(hostMetrics, "cpu_total");
  const cpuSeverity =
    cpuNow === null ? null : offOnly(severityFromPercent(cpuNow));
  tiles.push({
    key: "cpu",
    label: "CPU",
    value: percent(cpuNow),
    sub: host.cores === null ? undefined : `${host.cores} cores`,
    values: griddedValues(hostMetrics, 0, "cpu_total"),
    color: trendColor(cpuSeverity, "var(--s1)"),
    severity: cpuSeverity,
    slug: "host-cpu",
  });

  // Memory and swap DO carry one, and it is the same one their meters
  // carried on this page before the tiles replaced them:
  // severityFromPercent against DEFAULT_THRESHOLDS. Nothing new is being
  // judged -- the judgement moved from a bar to a tile.
  const memUsed = griddedValues(hostMetrics, 0, "mem_used");
  const memTotal = griddedValues(hostMetrics, 0, "mem_total");
  const memPct = ratioSeries(memUsed, memTotal);
  const memNow = latestValue(memPct);
  const memSeverity =
    memNow === null ? null : offOnly(severityFromPercent(memNow));
  tiles.push({
    key: "memory",
    label: "Memory",
    value: percent(memNow),
    sub: memorySub(hostMetrics),
    values: memPct,
    color: trendColor(memSeverity, "var(--s3)"),
    severity: memSeverity,
    slug: "host-memory",
  });

  // The one filesystem worth a tile: the fullest. Which one it is goes in
  // the sub-line, because "94%" with no mount point is a number a reader
  // cannot act on -- and the Disk card below lists all of them anyway.
  tiles.push(busiestFilesystemTile(filesystemMetrics));

  return tiles;
}

/**
 * Whether this host has swap at all.
 *
 * A host with none has nothing to be a percentage OF, and a dash where a
 * figure goes says "we could not read it" about a machine that is working
 * exactly as intended -- so the swap tiles are absent rather than ABSENT.
 * Three readings pinned at zero on every swapless VM in a fleet is also how
 * a card teaches people to stop looking at it.
 *
 * The tier check is the separate half: at 5m and 1h the rollups may not
 * carry swap_total, which is not a fact about the host either.
 */
function hasSwap(res: MetricsResponse | null): boolean {
  // swap_used, not swap_total, and that is a schema fact: host_samples_5m
  // carries swap_used_avg and NO swap_total at all, so a check on the total
  // answered "no swap" for every host at every range past 1h. The agent is
  // what makes swap_used the honest signal -- collector/memory.go writes
  // both columns NULL when SwapTotal is zero ("swap absent is not swap
  // empty"), so a reading here means the host really has swap.
  return hasReading(griddedValues(res, 0, "swap_used"));
}

/** "78 of 128 GiB" -- the denominator the percentage is of. Falls back to the
 * host row's memory_total, which is the same fallback the memory meters used.
 */
/**
 * The denominator the percentage is of, or nothing.
 *
 * NO fallback to the host row, and that is the whole fix here: it used to
 * read `current(hostMetrics, ...) ?? host.mem_used`, so a failed
 * metrics("host") -- which HostPage turns into null through orNull, and which
 * a host dead all window produces too -- rendered the figure as ABSENT with
 * "4 of 8 GiB" printed directly underneath it. The line stated the exact two
 * numbers the figure above it had just called unknowable.
 *
 * One source for both halves. Where the percentage cannot be measured there
 * is no sub-line either, which is the same rule the unit follows beside an
 * absent value in StatTile.
 */
function memorySub(hostMetrics: MetricsResponse | null): string | undefined {
  const used = current(hostMetrics, "mem_used");
  const total = latest(hostMetrics, "mem_total");
  if (used === null || total === null) return undefined;
  return `${gib(used)} of ${gib(total)} GiB`;
}

function gib(bytesValue: number): string {
  return Math.round(bytesValue / 1024 ** 3).toString();
}

/**
 * The fullest filesystem on the host.
 *
 * Through filesystemRows() and diskState(), both already used by the Disk
 * card and the attention band, so one disk cannot read 94 % here and 91 %
 * eight lines down. A host that reported no filesystems gets the tile with
 * ABSENT in it rather than no tile: "we have no filesystem readings" is
 * worth saying on a page whose whole job is to say what is going on.
 */
function busiestFilesystemTile(res: MetricsResponse | null): Tile {
  const rows = filesystemRows(res);
  let worstIndex = -1;
  let worstPct = -1;
  let worstNotable: FillSeverity | null = null;
  // SEVERITY first, percentage only to break a tie within it.
  //
  // Picking by percentage alone is not the same question the attention band
  // asks: diskSeverityFor weighs the bytes left as well as the ratio, so a
  // 20 TB array at 96 % with 800 GB free outranks a 256 GB root at 93 % with
  // 17 GB left -- and the tile then showed a neutral 96 % directly under a
  // warning about a disk it did not name. The band and the tile have to
  // agree about which disk is the problem; they already share diskState, and
  // this is the other half of sharing it.
  //
  // WHICH disk, only. What COLOUR the tile then reads is the percentage's
  // own question, answered below by the same 70/85/95 the meters use -- see
  // the comment on the returned severity.
  rows.forEach((row, index) => {
    const state = diskState(row.used, row.free);
    if (state === null) return;
    if (
      worstIndex !== -1 &&
      severityRank(state.severity) < severityRank(worstNotable)
    ) {
      return;
    }
    if (
      worstIndex !== -1 &&
      severityRank(state.severity) === severityRank(worstNotable) &&
      state.pct <= worstPct
    ) {
      return;
    }
    worstPct = state.pct;
    worstIndex = index;
    worstNotable = state.severity;
  });

  if (worstIndex === -1) {
    return {
      key: "disk",
      label: "Busiest filesystem",
      value: ABSENT,
      values: [],
      color: "var(--s6)",
      severity: null,
      slug: "host-filesystem",
    };
  }

  // The percentage OVER THE WINDOW, on the same used/(used+free) definition
  // as the figure -- not a used-bytes series, which would climb on a disk
  // that was being grown and read as filling up.
  const used = griddedValues(res, worstIndex, "used");
  const free = griddedValues(res, worstIndex, "free");
  const pctSeries = used.map((u, i) => {
    const f = free[i];
    if (u === null || f == null || u + f === 0) return null;
    return (u / (u + f)) * 100;
  });

  // The tile is coloured by the percentage it prints, on the same 70/85/95
  // the Disk panel's meters below it read by -- not by the compound rule
  // that picked WHICH filesystem this is. The compound rule answers "is this
  // worth acting on" and is why a 20 TB array with 800 GB free stays out of
  // the attention band; using it for the fill as well made red unreachable
  // above roughly 400 GB of capacity, and a 97 % disk drew amber.
  const severity = offOnly(severityFromPercent(worstPct));

  return {
    key: "disk",
    label: "Busiest filesystem",
    value: percent(worstPct),
    sub: rows[worstIndex]?.label,
    values: pctSeries,
    color: trendColor(severity, "var(--s6)"),
    severity,
    slug: "host-filesystem",
  };
}

/**
 * Three tiles a card, and that is a measurement rather than a preference: a
 * half-width card on this grid is ~500px inside its padding, which holds
 * three 140px tiles. A fourth wrapped to a row of its own and left two thirds
 * of that row empty -- half a page of blank surface under the first card on
 * the tab.
 *
 * So each group carries its three most-read figures and nothing else. The
 * rest are not lost: every tile links to the panel that draws its column in
 * full, and those panels carry the readings beside it -- TCP retransmits on
 * `tcp-statistics`, swap-in on `memory-pressure`.
 */
function kernelTiles(res: MetricsResponse | null): Tile[] {
  return [
    rateTile(
      res,
      "ctxt",
      "Context switches",
      "ctxt_per_s",
      "context-switches",
      "var(--s1)",
    ),
    rateTile(
      res,
      "intr",
      "Interrupts",
      "intr_per_s",
      "interrupts",
      "var(--s1)",
    ),
    processesTile(res),
  ];
}

/**
 * The total is the headline and the runnable count is the context, not the
 * other way round.
 *
 * procs_running is /proc/stat's count of tasks in the runnable state at the
 * instant of the sample, the agent included, so a server that is not
 * saturated reads 1-5 all day. Under the words "Running processes" that reads
 * as "this server has three processes", which is not what the column says and
 * not believable. processes_total is the figure a reader actually checks --
 * what ps counts -- and the runnable and blocked counts underneath are what
 * make it mean something: how many of those want CPU, how many are stuck on
 * I/O.
 *
 * A PID-namespaced agent cannot see the host's processes and leaves
 * processes_total unset, so there the tile falls back to the runnable count
 * under a label that says what it is.
 */
function processesTile(res: MetricsResponse | null): Tile {
  // "Did this host report the column ANYWHERE in the window", the same
  // question hasSwap() asks, and not current() !== null. The two readings
  // come from different collectors -- the total from procs.go's own /proc
  // scan, the runnable count from kernelstat.go -- so one missed scrape, or
  // one bucket of a host being offline, would leave the total null while
  // procs_running still landed. On current() that silently swaps the tile's
  // identity mid-day: headline, label, sparkline and panel link all change,
  // for a host whose agent reports the total perfectly well.
  if (!hasReading(griddedValues(res, 0, "processes_total"))) {
    return levelTile(
      res,
      "procs-running",
      "Runnable now",
      "procs_running",
      "running-processes",
      "var(--s2)",
      blockedSub(res),
    );
  }
  return levelTile(
    res,
    "procs-running",
    "Processes",
    "processes_total",
    "total-processes",
    "var(--s2)",
    stateSub(res),
  );
}

/** "3 running, 1 blocked" -- whichever of the two the host reports. Zero is a
 * reading like any other and renders; only null drops a part. */
function stateSub(res: MetricsResponse | null): string | undefined {
  const running = current(res, "procs_running");
  const parts: string[] = [];
  if (running !== null) parts.push(`${cardinal(running)} running`);
  const blocked = current(res, "procs_blocked");
  if (blocked !== null) parts.push(`${cardinal(blocked)} blocked`);
  return parts.length === 0 ? undefined : parts.join(", ");
}

function blockedSub(res: MetricsResponse | null): string | undefined {
  const blocked = current(res, "procs_blocked");
  return blocked === null ? undefined : `${cardinal(blocked)} blocked`;
}

// All three read together and all three link to the same panel, which is the
// point of that panel: swap-out climbing with major faults flat is reclaim
// doing its job, both climbing together is thrash. Three tiles rather than
// one so the reader sees which of the three moved.
function pressureTiles(res: MetricsResponse | null): Tile[] {
  const tiles: Tile[] = [
    rateTile(
      res,
      "pgmajfault",
      "Major page faults",
      "pgmajfault_per_s",
      "memory-pressure",
      "var(--s7)",
    ),
  ];

  if (!hasSwap(res)) return tiles;

  // How full swap is, and how hard the kernel is pushing into it. Swap-out
  // rather than swap-in as the rate: the panel's own comment puts it plainly
  // -- swap-out climbing while faults stay flat is reclaim doing its job,
  // both climbing together is thrash. Swap-in is on that panel, one click
  // away through either tile.
  //
  // A PERCENTAGE only where the tier carries the denominator. Above the raw
  // tier there is no swap_total, and a percentage invented from the last one
  // this host ever reported would be a figure with no measurement behind it
  // -- so the tile falls back to the bytes it can actually stand behind. The
  // threshold goes with the percentage for the same reason: severity here is
  // "how full", and without a capacity there is no how-full to judge.
  const used = griddedValues(res, 0, "swap_used");
  const total = griddedValues(res, 0, "swap_total");
  const asPercent = carriesColumn(res, "swap_total");
  const swapPct = asPercent ? ratioSeries(used, total) : used;
  const swapNow = latestValue(swapPct);
  // The same threshold the Memory meter carried on this page before the
  // tiles replaced it. Nothing new is judged; the judgement moved.
  const swapSeverity =
    !asPercent || swapNow === null
      ? null
      : offOnly(severityFromPercent(swapNow));
  tiles.push({
    key: "swap",
    label: "Swap used",
    value: asPercent ? percent(swapNow) : binaryBytes(swapNow),
    values: swapPct,
    color: trendColor(swapSeverity, "var(--s5)"),
    severity: swapSeverity,
    slug: "host-memory",
  });
  tiles.push(
    rateTile(
      res,
      "pswpout",
      "Swap out",
      "pswpout_per_s",
      "memory-pressure",
      "var(--s8)",
    ),
  );
  return tiles;
}

function networkTiles(
  host: HostDetail,
  hostMetrics: MetricsResponse | null,
  netMetrics: MetricsResponse | null,
  now: Date | undefined,
): Tile[] {
  // The sum over interfaces, through trafficSeries() -- called rather than
  // re-derived, because this tile, the fleet row and the Traffic chart are
  // one reading and a second copy of the sum is exactly how they came to
  // disagree. A null anywhere in a bucket makes that bucket unknowable
  // rather than smaller.
  const { rx, tx } = trafficSeries(netMetrics);
  // The FIGURES come off host_current, blanked when the host is not
  // reporting -- isReporting is the fleet list's predicate too, so a host
  // cannot read offline there and busy here.
  const live = isReporting(host, now);

  return [
    {
      key: "rx",
      // "in" and "out", never rx and tx: the direction is the point, and rx
      // is the kernel's word for it rather than the reader's.
      label: "Traffic in",
      value: byterate(live ? host.net_rx_bytes : null),
      values: rx,
      color: "var(--s1)",
      severity: null,
      slug: "host-traffic",
    },
    {
      key: "tx",
      label: "Traffic out",
      value: byterate(live ? host.net_tx_bytes : null),
      values: tx,
      color: "var(--s2)",
      severity: null,
      slug: "host-traffic",
    },
    levelTile(
      hostMetrics,
      "tcp-estab",
      "TCP established",
      "tcp_curr_estab",
      "tcp-connections",
      "var(--s6)",
    ),
  ];
}
