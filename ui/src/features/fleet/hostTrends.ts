import {
  getFleetMetrics,
  getMetrics,
  type Filesystem,
  type Host,
  type MetricsResponse,
} from "../../lib/api";
import {
  carriesColumn,
  fsName,
  griddedValues,
  latestValue,
  peakBase,
  reduceToColumns,
  sumSeries,
} from "../../lib/metrics";
import {
  filesystemBands,
  fsUsePercent,
  memoryBands,
  perCoreBands,
} from "../../lib/bands";
// containerTrends lives in lib/containers.ts, beside the capability wording
// the same lists share. It was here, because the fleet list needed it first;
// lib/bands.ts builds the host page's stacked Docker panels from it now, and
// lib importing from features would be the wrong direction.
import { containerTrends, type ContainerTrend } from "../../lib/containers";
import { currentFilesystems } from "../../lib/host";
import { rangeWindow, type Range } from "../../lib/range";
import type { Band } from "../../ui/charts/StackedSparkline";
import { SPARK_WIDTH } from "../../ui/charts/size";
import { DOWN_COLOR, UP_COLOR } from "../../ui/charts/UpDownSparkline";
import type { HostRow } from "./hostColumns";
import { diskState, type DiskThresholds } from "./conditions";
import type { DiskSeverity } from "./conditions";

/**
 * The fleet list's trends: four families per host, turned into the series
 * the host columns draw and the two counters the attention band reads.
 *
 * The fourth is `agent`, and it is the one family here that draws nothing.
 * It is fetched for buffer_dropped_total and post_failures_total, which the
 * band reports and no column plots. That is a deliberate widening of what
 * this fan-out is for: both are counters whose meaning depends on the series
 * around them, so neither can be answered by a gauge on the hosts list the
 * way services_failed is. A host silently dropping samples is worth one more
 * request.
 *
 * The two are read differently and the fields below say why at length --
 * post_failures_total as the window's increase, buffer_dropped_total as the
 * agent's running total, because the outage that makes the second one move
 * also punches the hole that makes an increase across it unreadable.
 *
 * This USED to be a fan-out -- one request per family per host -- and it was
 * the cost of the overview's whole premise. The spec is explicit that
 * sparklines are non-negotiable here: a bar shows one instant, and recent
 * history is half of what an overview is for. The read API was per-host by
 * construction (GET /api/v1/hosts/{id}/metrics), so there was no single call
 * that answered this, and the fleet-wide endpoint named here as the fix now
 * exists: GET /api/v1/metrics. fetchFleetTrends below asks it once per
 * family, and fetchHostTrends is the single-host form of the same question.
 *
 * Every request is settled independently. One host answering 500 must cost
 * that host's sparklines, not the fleet's.
 */
export interface HostTrends {
  /**
   * The window the hub actually answered, for the enlarged view's time axis.
   *
   * Not recomputed from the range at render time: the hub clamps a window it
   * cannot serve in full (retention, materialisation lag), so the range the
   * reader picked and the window the series were gridded against are not
   * always the same span. Labelling the axis from the ask rather than the
   * answer would put times on screen the data does not cover.
   *
   * null when every family failed, which is the same "cannot say" the
   * counters below use: no axis at all beats an invented one.
   */
  window: { from: string; to: string } | null;
  cpu: Band[];
  mem: Band[];
  /**
   * The series the row's status is judged from: cpu_total, from the `host`
   * family, for every host without exception.
   *
   * Deliberately NOT `cpu[0]`, which is what the sporadic badge used to
   * read. That is a per-core band under 32 threads and the cpu_total
   * fallback above it, so one host was judged against the cpu_core family
   * and the host beside it against host_samples -- two different relations,
   * two different materialisation lags, two different gap patterns. A
   * status column has to mean the same thing on every row of the same page.
   *
   * The `host` family is fetched unconditionally below, so this costs no
   * extra request.
   */
  reporting: (number | null)[];
  /**
   * mem_used over the window: MemTotal - MemAvailable, what `free` calls
   * used. The Memory cell's silhouette, and the same quantity its printed
   * percentage is a gauge of, so the shape and the figure beside it cannot
   * disagree.
   *
   * NOT the top edge of `mem` above. That stack partitions mem_total into
   * everything that is not free -- used, shared, ARC, buffers, cached -- so
   * on a host doing nothing but serving files it stands at 97% while the
   * figure beside it says 30%. The stack is still assembled: the enlarged
   * view draws it, where "which part of memory is growing" is the question.
   *
   * From the `host` family, so it costs no extra request.
   */
  memUsed: (number | null)[];
  rx: (number | null)[];
  tx: (number | null)[];
  /**
   * The same pair read as the bucket peak, for the ENLARGED view alone.
   *
   * The cell draws the mean, as Observium does. A dialog has the room for
   * both, so it draws the mean as its line with the peak as an envelope
   * behind it -- and it must have that pair the moment it OPENS, not only
   * after a range change, or the enlarged view is a bare line where the cell
   * it came from had a band.
   *
   * Empty at the raw tier, where the sample is its own peak.
   */
  rxPeak: (number | null)[];
  txPeak: (number | null)[];
  /**
   * The filesystem family's own response, carried rather than reduced.
   *
   * `fullest` used to be computed here, and cannot be any more: picking the
   * mount now needs the HOST -- its stored filesystem gauge, and its
   * last_seen to tell a retired mount from a machine that is simply off. Only
   * buildRows has both, so the reduction happens there and this hands it the
   * material. The by-window bands below are unaffected: they are a fact about
   * the response alone.
   */
  filesystem: MetricsResponse | null;
  /** Every filesystem's usage over the window, as df's Use%, one band each.
   * The meter beside it says how full the worst one is now; these say which
   * of them is moving and how fast -- the difference between "watch it" and
   * "act today". */
  disk: Band[];
  /**
   * The three delivery/kill counters that used to live here -- oomKills,
   * dropped and postFailures -- are gone, and with them the `agent` family
   * this module fetched solely to carry two of them.
   *
   * All three fed conditions that read a counter's increase across the range
   * picker's window, so each stated something that moved when the reader moved
   * the range. They are events now: one `hub` event per outage from the agent,
   * at critical when the ring lost anything, and OOM kills from the kmsg
   * collector, which names the process it killed.
   */
}

// The CPU and memory bands both moved to lib/bands.ts, which the host page
// reads too: the fleet row and the detail page show the same host, and a
// reader moving between them is entitled to the same shape. What used to sit
// here as two literal band lists could not express either chart any more --
// the CPU stack is now per-core, and the memory stack derives its "used" band
// by subtraction rather than reading a column.
//
// The user/system/iowait/steal breakdown that used to live here is not lost:
// it answers a different question (where the time went, not which core spent
// it) and it has its own panel on the host page.

/**
 * The one-band fallback: cpu_total as a single silhouette.
 *
 * Drawn when a host has no per-core series -- too many threads to ask for
 * them, or a tier that does not carry them. One true band beats a
 * not-collected cell where a silhouette is available, and the fleet row and
 * the host page must not disagree about whether a host's CPU can be drawn.
 *
 * This was a general bandsFrom(res, specs, fallback) building N bands from a
 * list of column names. Nothing needs that any more -- the CPU stack is
 * per-core and the memory stack derives its bottom band by subtraction -- so
 * its only caller passed an empty spec list and reached nothing but the
 * fallback.
 */
// Takes the already-gridded cpu_total rather than the response: the same
// series is also what the row's status is judged from (HostTrends.reporting),
// and one column read twice is two readings that can be made to disagree.
function totalBand(values: (number | null)[]): Band[] {
  return values.length === 0
    ? []
    : [{ name: "busy", color: "var(--s1)", values }];
}

/**
 * The fullest filesystem, named.
 *
 * The percentage is used / (used + free) -- df's Use% -- and NOT
 * used / total: total includes the root reserve, so dividing by it reports a
 * disk as less full than df does, which is the number an operator has
 * already seen over SSH. The API deliberately computes no percentage, so
 * this definition lives here.
 */
// crossedAt used to live here: a walk backwards through THIS mount's series to
// the bucket it crossed on, run again on every render.
//
// It is the hub's job now, done once when the condition opens
// (Store.diskOnset), and that is not a relocation -- it is the difference
// between an onset and a guess. This walk was bounded by the range the reader
// had picked, so the same disk answered "since 14:02" on one range and "over
// 24 h" on another; and it read whichever tier the range selected, so the
// answer changed shape as the data aged. The hub walks raw samples while they
// still exist and stores the result, so it is exact and it stops moving.
//
// What is left of the pair is `fullest.since`, which is gone from this row
// entirely: nothing derives an onset in the browser any more, and
// Condition.since comes from host_conditions.opened_ts.

/**
 * The mount this host's Disk cell names, and everything the cell prints.
 *
 * Two sources, and which one answers is the whole shape of this function.
 *
 * `filesystems` is the GAUGE -- filesystem_current, one stored row per mount,
 * no window and no rollup lag (0013_filesystem_current.sql). It answers
 * whenever the hub sends it, because it is the only one that survives the host
 * being switched off: the fleet page is pinned to 24 h, so a NAS off since
 * Friday has no bucket in the answered window at all, and the cell used to go
 * blank on a machine whose disks had not moved a byte.
 *
 * `res` is the SERIES, and stays the fallback for a hub that does not send the
 * gauge yet. It reads the window's last slot through latestValue -- last slot
 * INCLUDING a trailing null, not last non-null -- and that was never about
 * freshness for its own sake. This picks the MAXIMUM across a host's mounts,
 * so a retired series is not merely a stale row here, it is one that WINS: a
 * filesystem frozen at 94 % the moment its agent was upgraded outranks every
 * live disk on the host, and the cell then reports 94 % for a host whose real
 * disks are at 20 %, naming a mount nobody is measuring.
 *
 * That defence has not been dropped, it has MOVED. currentFilesystems in
 * lib/host.ts makes the same call on better evidence -- the mount's own
 * reading timestamp against the host's last_seen -- which separates the
 * retired mount from the host that is simply off, a distinction the window's
 * last slot cannot draw because both of them look like a null.
 */
export function fullestFilesystem(
  res: MetricsResponse | null,
  filesystems: Filesystem[] | null,
  // The hub's own disk thresholds, from the conditions catalogue. Null before
  // the catalogue lands: every mount then ranks as "none" and the fullest is
  // decided by percentage alone, which is the honest answer for one poll --
  // restating 90 and 95 here is the second copy this change deleted.
  thresholds: DiskThresholds | null,
): HostRow["fullest"] {
  const usable =
    res !== null &&
    res.series.length > 0 &&
    carriesColumn(res, "used") &&
    carriesColumn(res, "free");
  if (filesystems === null && !usable) return null;

  let best: {
    mount: string;
    pct: number;
    free: number;
    severity: DiskSeverity;
    asOf: string | null;
    index: number;
  } | null = null;

  const consider = (
    mount: string,
    used: number | null,
    free: number | null,
    asOf: string | null,
    index: number,
  ) => {
    if (used === null || free === null || used + free === 0) return;
    const state = diskState(used, free, thresholds)!;
    const candidate = {
      mount,
      pct: state.pct,
      free,
      severity: state.severity,
      asOf,
      index,
    };
    if (best === null || outranks(candidate, best)) best = candidate;
  };

  if (filesystems !== null) {
    for (const fs of filesystems) {
      const mount = fsName(
        { filesystem: fs.label, mountpoint: fs.mountpoint ?? "" },
        "?",
      );
      // By NAME, unlike the series branch below. These rows are the hub's
      // stored gauge and the response is a separate answer over a window, so
      // the index that wins here means nothing against res.series -- and a
      // mount the window never reached is simply absent from it, which is the
      // offline case and draws no line at all.
      const index =
        res === null
          ? -1
          : res.series.findIndex((one) => fsName(one.key, "?") === mount);
      consider(mount, fs.used ?? null, fs.free ?? null, fs.ts ?? null, index);
    }
  } else {
    const answered = res!;
    for (let i = 0; i < answered.series.length; i++) {
      consider(
        fsName(answered.series[i]!.key, "?"),
        latestValue(griddedValues(answered, i, "used")),
        latestValue(griddedValues(answered, i, "free")),
        null,
        i,
      );
    }
  }

  if (best === null) return null;
  const winner: {
    mount: string;
    pct: number;
    free: number;
    severity: DiskSeverity;
    asOf: string | null;
    index: number;
  } = best;
  const drawable = res !== null && winner.index >= 0;
  return {
    mount: winner.mount,
    pct: winner.pct,
    free: winner.free,
    // The winner's OWN Use% over the window, taken by the index that won --
    // not matched back out of filesystemBands by name, which drops the mounts
    // that reported nothing and so does not index alike. This is the series
    // the Disk cell draws, and it has to be the same mount the percentage
    // beside it names or the cell says two things about two disks.
    //
    // Empty when the window holds nothing for this mount, which is the host
    // that has been off all day. The cell then draws the reading with no line
    // above it -- the mirror of a mount that is being measured and has no
    // stored gauge yet, which draws a line with no reading.
    series: drawable ? fsUsePercent(res!, winner.index) : [],
    asOf: winner.asOf,
  };
}

const FULLEST_RANK: Record<"critical" | "warning" | "none", number> = {
  critical: 2,
  warning: 1,
  none: 0,
};

/**
 * Which of two filesystems this row should name: the one worth acting on,
 * and only then the bigger number.
 *
 * Highest percentage alone is the wrong pick now that a percentage no longer
 * decides anything on its own. A host with /mnt/ark at 92% (674 GB free, not
 * worth a word) beside / at 91% (2 GB free, nearly out) would name ark and
 * then have nothing to say about it, while the disk that is actually filling
 * sat behind a "+1".
 */
function outranks(
  a: { pct: number; severity: DiskSeverity },
  b: { pct: number; severity: DiskSeverity },
): boolean {
  const ra = FULLEST_RANK[a.severity ?? "none"];
  const rb = FULLEST_RANK[b.severity ?? "none"];
  if (ra !== rb) return ra > rb;
  return a.pct > b.pct;
}

// Generic over what it swallows, so the per-host fetch (one MetricsResponse)
// and the fleet fetch (a Map of them) share one failure rule.
async function orNull<T>(p: Promise<T>): Promise<T | null> {
  try {
    return await p;
  } catch {
    // One family the hub cannot answer costs that column, not the row.
    return null;
  }
}

/**
 * Above this many cores the per-core stack is not drawn.
 *
 * Not a legibility limit -- thirty-two hairlines in a 120x32 box still read
 * as an activity band. It is a transfer limit: the read API has no
 * aggregate-across-keys mode, so a 128-core host would ship 128 series per
 * host per fleet render on top of the fan-out this page already costs.
 * Those hosts fall back to cpu_total, which the host family carries anyway,
 * so the row costs nothing extra and still shows a true silhouette.
 */
export const MAX_PER_CORE = 32;

/**
 * One family at one range, for a chart enlarged out of a fleet row.
 *
 * The same rangeWindow-then-getMetrics shape the fan-out below uses, so a
 * widened dialog and a widened page ask the hub the same question -- and
 * deliberately NOT wrapped in orNull(): the fan-out swallows a failure into
 * a missing column because a fleet row has nineteen others to draw, while a
 * dialog has one chart on screen and can say the range it just asked for
 * failed. Same split as HostPage's fetchFamily.
 */
export function fetchHostFamily(
  hostId: number | string,
  family: string,
  range: Range,
  now?: Date,
): Promise<MetricsResponse> {
  const window = rangeWindow(range, now);
  return getMetrics(hostId, {
    family,
    from: window.from,
    to: window.to,
    step: window.step,
  });
}

/**
 * The CPU silhouette: per-core when the host was small enough to ask for it,
 * cpu_total otherwise.
 *
 * Shared with the enlarged view rather than inlined in the fan-out, so the
 * dialog a reader opens off a CPU cell draws the same bands the cell does.
 * Normalised, like the cell: see the comment at the call site below.
 */
export function cpuBands(
  host: MetricsResponse | null,
  cores: MetricsResponse | null,
): { bands: Band[]; from: MetricsResponse | null } {
  const perCore = perCoreBands(cores, { normalise: true });
  // `from` is the response the bands were actually gridded against, and it is
  // returned rather than inferred because only this function knows which
  // branch it took: the two families can be answered from different tiers, so
  // a caller labelling a per-core plot with the host response's endpoints
  // would put times on it the shape was never gridded to. A caller that
  // guessed `cores ?? host` would be wrong for a cpu_core response that came
  // back carrying no series.
  return perCore.length > 0
    ? { bands: perCore, from: cores }
    : { bands: totalBand(griddedValues(host, 0, "cpu_total")), from: host };
}

/**
 * A host's traffic pair, summed over its interfaces and read at the bucket's
 * MEAN, folded to the width of the chart that will draw it.
 *
 * Both halves of that are what RRDtool does, and the reason it looks like
 * every traffic graph an operator has already read.
 *
 * The mean, and this is the correction of a mistake worth writing down. The
 * cell read the bucket PEAK for one release, on the argument that a burst is
 * the reading -- judged against a simulated host whose bursts are 3500x its
 * floor, where no linear axis is legible anyway. On a real host it is wrong,
 * because reading the bucket peak AND then folding each pixel column to its
 * peak compounds: the same 24 h reads a ceiling of 7.3 MB/s through the mean
 * and 26.6 MB/s through that pair of maxima. Everything under the peak is
 * squashed by 3.7x, and the quiet body of the chart -- which is most of it --
 * drops below one pixel. Observium's DEF asks for AVERAGE and rrdtool reduces
 * with the same function; rendered side by side at 150x45 on identical
 * numbers, that is the difference between a dense band and an empty cell.
 *
 * The fold, through reduceToColumns(): a 24 h window is 285 five-minute
 * buckets and the cell is 150 px, so without it every pixel column carries
 * about 1.9 buckets and the polyline zigzags between neighbours inside a
 * single pixel. Folded, each column is one reading.
 *
 * `columns` is the pixel width of the chart. Omitted, nothing is folded: the
 * caller is drawing at the data's own resolution or does not know its width
 * yet.
 *
 * Shared with the enlarged view so it cannot disagree with the cell it was
 * opened from -- the dialog folds to its own, wider, width.
 */
export function trafficSeries(
  net: MetricsResponse | null | undefined,
  columns?: number,
): {
  /** What a chart DRAWS: the bucket mean, folded to the pixel column with the
   * same function. Observium's `DEF ... AVERAGE`. */
  rx: (number | null)[];
  tx: (number | null)[];
  /**
   * The bucket PEAK, folded with the peak -- the envelope an enlarged view
   * has the room to draw behind the mean, and which the rolled-up tiers
   * materialise max(rx_bytes) for.
   *
   * Summing per-interface peaks is a known bias and it is accepted only here:
   * two links can burst in different seconds of one bucket and this adds them
   * as though they had not. A band behind a line can carry that; the line an
   * operator reads a number off cannot.
   *
   * Empty at the raw tier, where peakBase() falls back to the bare column and
   * the two series are the same numbers -- an envelope drawn exactly on its
   * own line is ink for nothing.
   */
  rxPeak: (number | null)[];
  txPeak: (number | null)[];
} {
  const fold = (vals: (number | null)[], combine: "mean" | "max") =>
    columns === undefined ? vals : reduceToColumns(vals, columns, combine);
  const rxPeakColumn = peakBase(net, "rx_bytes");
  const txPeakColumn = peakBase(net, "tx_bytes");
  const rolledUp = rxPeakColumn !== "rx_bytes" || txPeakColumn !== "tx_bytes";
  return {
    rx: fold(sumSeries(net, "rx_bytes"), "mean"),
    tx: fold(sumSeries(net, "tx_bytes"), "mean"),
    rxPeak: rolledUp ? fold(sumSeries(net, rxPeakColumn), "max") : [],
    txPeak: rolledUp ? fold(sumSeries(net, txPeakColumn), "max") : [],
  };
}

/*
 * There is no window threshold on the peak envelope any more.
 *
 * There was one: 48 hours, the reference's own -- Observium's
 * common.inc.php:167 sets `$graph_max = 0` below 172800 seconds, on the
 * reasoning that a short enough bucket has a mean that IS the reading. It
 * costs something to drop it, and the cost is worth writing down rather than
 * rediscovering: a banded pair is scaled to its BAND (mirrorHalves in
 * Chart.tsx, so the envelope cannot overflow the mean it contains), so an
 * envelope on a 24 h dialog also lifts the ceiling -- measured on a real host,
 * an axis running to 65 MB over a reading that topped out at 20.5 MB.
 *
 * The envelope is drawn wherever the answering tier materialised a max column
 * and the chart is not a stack. At the raw tier there is no separate peak and
 * the pair collapses to one series, which is honest: the sample is its own
 * peak there.
 */

/**
 * The in/out pair an ENLARGED traffic view draws: the mean as the line, the
 * bucket peak as the envelope over it wherever the tier has one.
 *
 * One function rather than a copy in each dialog. The fleet row's cell and
 * the host overview's Traffic card are the same chart at two sizes and both
 * open into this; two hand-built copies of the same pair is exactly how they
 * came to disagree before.
 *
 * The envelope's outer edge is the silhouette the cell drew, so enlarging
 * does not change the shape -- and the stats table under the chart reads the
 * LINE, so its Mean is a mean. At the raw tier there is no separate peak and
 * the pair collapses to one series, which is honest: the sample is its own
 * peak there.
 */
export function trafficDetailSeries(t: {
  rx: (number | null)[];
  tx: (number | null)[];
  rxPeak?: (number | null)[];
  txPeak?: (number | null)[];
}): {
  name: string;
  color: string;
  values: (number | null)[];
  band?: (number | null)[];
}[] {
  const pair = (
    name: string,
    color: string,
    mean: (number | null)[],
    peak: (number | null)[],
  ) =>
    peak.length === 0
      ? { name, color, values: mean }
      : { name, color, values: mean, band: peak };
  return [
    pair("in", UP_COLOR, t.rx, t.rxPeak ?? []),
    pair("out", DOWN_COLOR, t.tx, t.txPeak ?? []),
  ];
}

/**
 * What this page actually draws, per family, as BASE column names.
 *
 * Sent as ?columns= so the hub ships these instead of everything: family=host
 * alone carries 71 value columns at the raw tier and 101 at 5m, and a fleet
 * row reads the dozen below. Left unnarrowed, a 24h fleet render moved an
 * order of magnitude more numbers than it drew.
 *
 * BASE names, deliberately -- the hub expands each to itself plus whichever of
 * _avg/_max/_min the answering tier carries (narrow(), internal/hub/read/
 * columns.go), which is the same resolution candidates() does on this side.
 * Naming a suffix here would pin the request to one tier and 400 at the
 * others, and orNull() below would turn that into a blank cell rather than an
 * error anyone could see.
 *
 * Every entry has a reader. Adding a griddedValues() call for a column that is
 * not on its family's list gets nulls, not a warning, so the two move
 * together.
 */
const FLEET_COLUMNS: Record<string, string[]> = {
  // cpu_total for the silhouette and the reporting series; mem_used for the
  // Memory cell's silhouette; oom_kill_total for the attention band. The
  // rest are memoryBands' five-band partition, drawn by the enlarged view:
  // it needs mem_free AND mem_total or it falls back to a lone mem_used
  // band, and the others are the subsystems it subtracts.
  host: [
    "cpu_total",
    "mem_total",
    "mem_free",
    "mem_used",
    "mem_buffers",
    "mem_cached",
    "mem_shared",
    "mem_sreclaimable",
    "mem_zfs_arc",
    "oom_kill_total",
  ],
  net: ["rx_bytes", "tx_bytes"],
  // used and free, never a percentage: fullestFilesystem() and
  // filesystemBands() both derive the ratio themselves.
  filesystem: ["used", "free"],
  cpu_core: ["busy"],
  container: ["cpu_pct", "mem_used", "mem_limit"],
};

export async function fetchHostTrends(
  hostId: number,
  range: Range,
  now?: Date,
  /** The host's logical CPU count, deciding whether the per-core stack is
   * worth fetching. Unknown means don't: an unbounded fetch on a host whose
   * size nobody knows is exactly the case this guard is for. */
  threads?: number | null,
): Promise<HostTrends> {
  const window = rangeWindow(range, now);
  const ask = (family: string) =>
    orNull(
      getMetrics(hostId, {
        family,
        from: window.from,
        to: window.to,
        step: window.step,
        columns: FLEET_COLUMNS[family],
      }),
    );

  const [host, net, filesystem, cores] = await Promise.all([
    ask("host"),
    ask("net"),
    ask("filesystem"),
    wantsCores(threads) ? ask("cpu_core") : Promise.resolve(null),
  ]);

  return hostTrendsFrom(host, net, filesystem, cores);
}

/**
 * Whether a host is small enough for the per-core stack to be worth asking
 * for. Unknown thread count means don't: an unbounded fetch on a host whose
 * size nobody knows is exactly the case MAX_PER_CORE is for.
 */
function wantsCores(threads?: number | null): boolean {
  return threads !== null && threads !== undefined && threads <= MAX_PER_CORE;
}

/**
 * The five family responses turned into one row's trends.
 *
 * Split out of the fetch so the per-host and the fleet-wide paths cannot
 * disagree about what a row means: they ask the hub differently -- N requests
 * or one -- and then run the identical code over the identical response shape.
 */
export function hostTrendsFrom(
  host: MetricsResponse | null,
  net: MetricsResponse | null,
  filesystem: MetricsResponse | null,
  cores: MetricsResponse | null,
): HostTrends {
  // Per-core when the host is small enough to ask for it, and cpu_total
  // otherwise. Never the user/system/iowait/steal breakdown here: that is a
  // different question -- where the time went, rather than which core spent
  // it -- and it has its own panel on the host page.
  // Normalised here and only here: a 4-core and a 32-core host share one
  // 0-100 cell in this list, so the stack has to top out at cpu_total. The
  // host page draws the same cores unnormalised, where the numbers matter
  // more than cross-host comparability.
  // One series, one relation, every host -- see HostTrends.reporting. The
  // status badge is judged from it; cpuBands() grids it again for the
  // silhouette when the host was too large to fetch per-core.
  const total = griddedValues(host, 0, "cpu_total");
  // Folded to the cell it will be drawn in, not left at the tier's
  // resolution: 24 h is 285 buckets and the cell is 150 px.
  const traffic = trafficSeries(net, SPARK_WIDTH);

  return {
    // Whichever family answered. They are all asked for the same window, so
    // any of them names it; host is listed first because it is the one fetch
    // this row cannot do without.
    window: (host ?? net ?? filesystem)?.window ?? null,
    cpu: cpuBands(host, cores).bands,
    mem: memoryBands(host),
    reporting: total,
    memUsed: griddedValues(host, 0, "mem_used"),
    // The MEAN of each pixel column, summed across interfaces --
    // trafficSeries() carries why both halves of that are what RRDtool does.
    // The interface that actually burst is one click away on the host page,
    // where the pairs are drawn per interface.
    rx: traffic.rx,
    tx: traffic.tx,
    rxPeak: traffic.rxPeak,
    txPeak: traffic.txPeak,
    filesystem,
    disk: filesystemBands(filesystem),
  };
}

/** The subset of Host fetchFleetTrends needs: an id, and how big the host is. */
export interface TrendHost {
  id: number;
  threads?: number | null;
}

/**
 * Every host's trends, in one request per family instead of one per host.
 *
 * This is what the doc comment at the top of this file said did not exist yet.
 * The read API was per-host by construction, so the fleet page asked N hosts
 * the same question N times -- 6N+2 requests per render at four hosts, re-sent
 * on every poll and every range toggle, through a browser that will open six
 * connections. GET /api/v1/metrics answers all of them at once, and the
 * responses come back in the per-host shape so nothing downstream changes.
 *
 * Failure isolation moves with the fan-out: it used to be per host (one host
 * answering 500 cost that host's row), and it is now per FAMILY (one family
 * failing costs that column across the fleet). Both are the same bargain --
 * a page that draws what it has -- and a family that fails now fails for a
 * reason that was never host-specific anyway.
 */
export async function fetchFleetTrends(
  hosts: readonly TrendHost[],
  range: Range,
  now?: Date,
): Promise<Map<number, HostTrends>> {
  const trends = new Map<number, HostTrends>();
  if (hosts.length === 0) return trends;

  const window = rangeWindow(range, now);
  const ids = hosts.map((h) => h.id);
  const ask = (family: string, forHosts: number[]) =>
    orNull(
      getFleetMetrics(forHosts, {
        family,
        from: window.from,
        to: window.to,
        step: window.step,
        columns: FLEET_COLUMNS[family],
      }),
    );

  // Only the hosts small enough for a per-core stack, and no request at all
  // when none of them are: MAX_PER_CORE is a transfer limit, and it still is
  // one when the fan-out collapses -- thirty-two series per host add up the
  // same either way.
  const coreIds = hosts.filter((h) => wantsCores(h.threads)).map((h) => h.id);

  const [host, net, filesystem, cores] = await Promise.all([
    ask("host", ids),
    ask("net", ids),
    ask("filesystem", ids),
    coreIds.length > 0 ? ask("cpu_core", coreIds) : Promise.resolve(null),
  ]);

  for (const id of ids) {
    trends.set(
      id,
      hostTrendsFrom(
        host?.get(id) ?? null,
        net?.get(id) ?? null,
        filesystem?.get(id) ?? null,
        cores?.get(id) ?? null,
      ),
    );
  }
  return trends;
}

/**
 * Every host's container trends, in one request rather than one per host.
 *
 * Keyed host id, then container_key. A host whose containers could not be
 * asked about gets an empty inner map, never a missing entry.
 */
export async function fetchFleetContainerTrends(
  hosts: readonly TrendHost[],
  range: Range,
  now?: Date,
): Promise<{
  trends: Map<number, Map<string, ContainerTrend>>;
  /** The window the hub answered, for the enlarged view's time axis -- the
   * answer rather than the ask, for the reason HostTrends.window gives.
   *
   * ONE window for the whole list, and that is now a fact rather than an
   * approximation: this is a single request, so every host was answered from
   * the same plan. It used to be per host because it had to be -- N separate
   * requests could each be clamped differently, and labelling one host's
   * chart with another's times was a real risk. */
  window: { from: string; to: string } | null;
}> {
  const trends = new Map<number, Map<string, ContainerTrend>>();
  if (hosts.length === 0) return { trends, window: null };

  const window = rangeWindow(range, now);
  const ids = hosts.map((h) => h.id);
  const res = await orNull(
    getFleetMetrics(ids, {
      family: "container",
      from: window.from,
      to: window.to,
      step: window.step,
      columns: FLEET_COLUMNS.container,
    }),
  );

  for (const id of ids) {
    trends.set(id, containerTrends(res?.get(id) ?? null));
  }
  // Every host carries the same header, so the first is the answer for all
  // of them -- see getFleetMetrics.
  const answered = ids.map((id) => res?.get(id)).find(Boolean);
  return { trends, window: answered?.window ?? null };
}

/** Joins hosts and their trends into the rows the columns read. */
export function buildRows(
  hosts: readonly Host[],
  trends: ReadonlyMap<number, HostTrends>,
  // The hub's disk thresholds, for the Disk cell's ranking. See
  // fullestFilesystem: null until the conditions catalogue lands.
  thresholds: DiskThresholds | null = null,
): HostRow[] {
  return hosts.map((host) => {
    const trend = trends.get(host.id);
    return {
      // Where the host is comes with it: location, provider and facility are
      // on the list response, reported by the host's own agent. There is
      // nothing to join, which is why this function no longer takes the
      // sites and providers lists -- it used to fetch both whole to resolve
      // a name per row.
      ...host,
      window: trend?.window ?? null,
      cpu: trend?.cpu ?? [],
      mem: trend?.mem ?? [],
      reporting: trend?.reporting ?? [],
      memUsed: trend?.memUsed ?? [],
      rx: trend?.rx ?? [],
      tx: trend?.tx ?? [],
      rxPeak: trend?.rxPeak ?? [],
      txPeak: trend?.txPeak ?? [],
      // Reduced HERE rather than in hostTrendsFrom, because picking the mount
      // needs the host: currentFilesystems reads its stored gauge and dates
      // each reading against its own last_seen, which is what separates a
      // mount the agent has stopped naming from a machine that is simply
      // switched off. The second of those keeps its Disk cell now.
      //
      // null, not a zero percentage: a host whose filesystems have not been
      // read has no fullest one, and an empty green meter would say its
      // disks are empty.
      fullest: fullestFilesystem(
        trend?.filesystem ?? null,
        currentFilesystems(host),
        thresholds,
      ),
      disk: trend?.disk ?? [],
      // null, not 0: a host whose trends failed to load has not told us
      // there were no kills, and a fleet page must not report silence it
      // never heard. The two delivery counters below follow the same rule --
      // an unanswered `agent` family is "cannot say", never "nothing wrong".
    };
  });
}
