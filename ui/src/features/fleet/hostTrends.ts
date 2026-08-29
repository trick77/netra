import {
  getFleetMetrics,
  getMetrics,
  type Host,
  type MetricsResponse,
} from "../../lib/api";
import {
  carriesColumn,
  counterIncrease,
  fsName,
  griddedValues,
  latestValue,
  peakBase,
  reduceToColumns,
  sumSeries,
} from "../../lib/metrics";
import { filesystemBands, memoryBands, perCoreBands } from "../../lib/bands";
import { rangeWindow, type Range } from "../../lib/range";
import type { Band } from "../../ui/charts/StackedSparkline";
import { SPARK_WIDTH } from "../../ui/charts/size";
import { DOWN_COLOR, UP_COLOR } from "../../ui/charts/UpDownSparkline";
import type { HostRow } from "./hostColumns";
import { diskState } from "./conditions";
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
  fullest: HostRow["fullest"];
  /** Every filesystem's usage over the window, as df's Use%, one band each.
   * The meter beside it says how full the worst one is now; these say which
   * of them is moving and how fast -- the difference between "watch it" and
   * "act today". */
  disk: Band[];
  /**
   * OOM kills inside the window, or null when the window carries no usable
   * pair of readings to difference.
   *
   * The INCREASE, never oom_kill_total itself: the counter is cumulative
   * since boot, so a host that killed something a year ago would otherwise
   * carry a permanent condition. null is "cannot say", which stays silent;
   * 0 is the host confirming nothing happened.
   *
   * From the `host` family, which is fetched unconditionally below, so this
   * costs no extra request -- the same reason `reporting` reads from it.
   */
  oomKills: number | null;
  /**
   * Samples the agent's ring buffer overflowed and never delivered, as the
   * agent last reported it. null when it never reported one.
   *
   * The LATEST value, not the window increase, and it is the one counter here
   * that has to be read that way. The ring only evicts once it is full, and it
   * holds a whole BufferWindow of scrapes (buffer/ring.go, an hour by
   * default), so buffer_dropped_total cannot move until the hub has been
   * unreachable for that entire window -- which guarantees a gap of at least
   * that long in the very series this counter arrives on. griddedValues fills
   * the gap with nulls and counterDeltas refuses any pair with a null end, so
   * the increase across it is discarded and the flat runs either side sum to
   * exactly 0. Read as an increase, this condition was silent precisely when
   * it was true.
   *
   * postFailures below is the opposite case and stays an increase: those posts
   * are retried and their samples arrive, so its series has no hole in it.
   *
   * The cost of latest() is that the count is cumulative for the life of the
   * agent process, so a host carries it until the agent restarts. That is the
   * honest reading: the dropped samples are gone, the history has holes, and
   * the holes do not heal. The host page has always read it this way.
   */
  dropped: number | null;
  /**
   * Failed deliveries to the hub inside the window, or null when the window
   * carries no usable pair.
   *
   * The INCREASE, and for a sharper reason than oomKills: post_failures_total
   * is cumulative for the whole life of the agent PROCESS and is deliberately
   * never reset by a success (see postFailures in
   * internal/agent/client/client.go), and the agent re-sends it every scrape.
   * Read as a latest value it is a permanent badge -- one hub restart pins
   * "1 failed delivery" to the page forever, even though the ring buffer
   * replayed those samples the moment the hub came back and nothing was lost.
   *
   * counterDeltas drops a negative step, so a counter going back to zero on
   * an agent restart is skipped rather than counted as a huge recovery.
   */
  postFailures: number | null;
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
 * The fullest filesystem, named, plus how many others there are.
 *
 * The percentage is used / (used + free) -- df's Use% -- and NOT
 * used / total: total includes the root reserve, so dividing by it reports a
 * disk as less full than df does, which is the number an operator has
 * already seen over SSH. The API deliberately computes no percentage, so
 * this definition lives here.
 */
/**
 * When this filesystem last became notable and stayed that way.
 *
 * Walked backwards from the newest reading through THIS series and no other:
 * the row names one mount, and dating it from whichever series crossed first
 * would put a timestamp from /var beside a sentence about /srv. The caller
 * has the series index for exactly that reason.
 *
 * A gap does not end the run. A host that was down for an hour did not empty
 * its disk while it was away, and treating the hole as a return under the
 * threshold would restart the clock every time the agent restarted.
 *
 * `atLeast` is the honest answer to a disk that was already full when the
 * window opened: netra cannot see past the range the reader picked, so the
 * row says "over 24 h" rather than naming the first bucket as if something
 * happened there.
 */
function crossedAt(
  res: MetricsResponse,
  index: number,
): { since: string | null; atLeast: boolean } {
  const used = griddedValues(res, index, "used");
  const free = griddedValues(res, index, "free");
  const count = Math.min(used.length, free.length);
  const from = Date.parse(res.window.from);
  const stepMs = res.step_s * 1000;
  if (count === 0 || !Number.isFinite(from) || !(stepMs > 0)) {
    return { since: null, atLeast: false };
  }

  let start = -1;
  // Whether the walk ever SAW this filesystem below notable. That, not
  // reaching index 0, is what separates a crossing from a floor: the loop
  // steps over gap buckets, so an agent that restarted at the window edge
  // leaves bucket 0 empty and the walk stops at bucket 1 having never seen a
  // reading below the line. Dated from `start` that printed a precise
  // "23 h ago" for a disk that was over threshold for the whole observable
  // window -- the fabricated onset this function exists to avoid.
  let dropped = false;
  for (let i = count - 1; i >= 0; i--) {
    const u = used[i];
    const f = free[i];
    // A bucket with no reading, or one whose two halves add to nothing, says
    // nothing either way -- keep walking.
    if (u === null || f === null || u + f === 0) continue;
    // The same compound rule the condition itself is judged by, not a bare
    // percentage: dating a mount from when it crossed 90% would put a
    // timestamp on a disk that only became worth reading about days later,
    // when the bytes ran low.
    if (diskState(u, f)?.severity == null) {
      dropped = true;
      break;
    }
    start = i;
  }
  if (start < 0) return { since: null, atLeast: false };
  if (!dropped) return { since: res.window.from, atLeast: true };
  return {
    since: new Date(from + start * stepMs).toISOString(),
    atLeast: false,
  };
}

function fullestFilesystem(res: MetricsResponse | null): HostRow["fullest"] {
  if (res === null || res.series.length === 0) return null;
  if (!carriesColumn(res, "used") || !carriesColumn(res, "free")) return null;

  let best: {
    mount: string;
    pct: number;
    free: number;
    severity: DiskSeverity;
    index: number;
  } | null = null;
  let measured = 0;
  for (let i = 0; i < res.series.length; i++) {
    // latestValue, not lastNumber: this picks the MAXIMUM across a host's
    // filesystems, so a retired series is not merely a stale row here, it is
    // one that WINS. A filesystem frozen at 94 % the moment its agent was
    // upgraded outranks every live disk on the host, and the fleet cell then
    // reports 94 % for a host whose real disks are at 20 % -- naming a mount
    // that is not being measured any more.
    //
    // It also keeps `measured` honest: the "+N" beside the meter counts the
    // filesystems this host HAS, not every one it has ever had.
    const used = latestValue(griddedValues(res, i, "used"));
    const free = latestValue(griddedValues(res, i, "free"));
    if (used === null || free === null || used + free === 0) continue;
    measured++;
    const state = diskState(used, free)!;
    const mount = fsName(res.series[i]!.key, "?");
    const candidate = {
      mount,
      pct: state.pct,
      free,
      severity: state.severity,
      index: i,
    };
    if (best === null || outranks(candidate, best)) best = candidate;
  }
  if (best === null) return null;
  // The onset is computed for the winner only, and only when it is notable at
  // all: every other mount on the host is a walk nobody reads.
  const crossed =
    best.severity !== null
      ? crossedAt(res, best.index)
      : { since: null, atLeast: false };
  return {
    mount: best.mount,
    pct: best.pct,
    free: best.free,
    others: Math.max(0, measured - 1),
    since: crossed.since,
    sinceAtLeast: crossed.atLeast,
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

/** The latest non-null value, or null when the series never reported.
 *
 * NOT lib/metrics.ts's latestValue(), which is the LATEST BUCKET including a
 * trailing null. The two answer different questions and only this one is
 * right for mem_limit: a configured ceiling does not stop being the ceiling
 * because the newest bucket has not materialised yet. */
function lastNumber(values: readonly (number | null)[]): number | null {
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i];
    if (v !== null && v !== undefined) return v;
  }
  return null;
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

/**
 * How long a window has to be before a chart draws the peak envelope.
 *
 * 48 hours, which is the reference's own threshold. Observium's
 * common.inc.php:167:
 *
 *     if ($to - $from <= 172800) { $graph_max = 0; }
 *     // Do not graph MAX areas for intervals less then 48 hours
 *
 * Below that a bucket is short enough that its mean IS the reading, and the
 * envelope costs more than it says. It cost two things here. It drew a layer
 * the reference does not draw at these windows -- the pale column behind
 * every spike. And because a banded pair is scaled to its BAND (mirrorHalves
 * in Chart.tsx, so the envelope cannot overflow the mean it contains), it
 * also set the ceiling: on a real host the 24h dialog ran its axis to 65 MB
 * while the reading topped out at 20.5 MB, drawing the whole quiet band at a
 * third of the height it had earned.
 */
export const PEAK_BAND_MIN_SPAN_MS = 48 * 60 * 60 * 1000;

export function drawsPeakBand(
  window?: { from: string; to: string } | null,
): boolean {
  // No window is no reason to drop a layer: a caller that cannot say gets
  // what it always got.
  if (!window) return true;
  const span = Date.parse(window.to) - Date.parse(window.from);
  if (!Number.isFinite(span)) return true;
  return span > PEAK_BAND_MIN_SPAN_MS;
}

/**
 * The in/out pair an ENLARGED traffic view draws: the mean as the line, the
 * bucket peak as the envelope over it where the window is long enough to
 * want one -- see drawsPeakBand().
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
export function trafficDetailSeries(
  t: {
    rx: (number | null)[];
    tx: (number | null)[];
    rxPeak?: (number | null)[];
    txPeak?: (number | null)[];
  },
  /* The window the chart covers. Without one the envelope is drawn, which is
     the old behaviour and what a caller that genuinely cannot say should
     get. See drawsPeakBand(). */
  window?: { from: string; to: string } | null,
): {
  name: string;
  color: string;
  values: (number | null)[];
  band?: (number | null)[];
}[] {
  const band = drawsPeakBand(window);
  const pair = (
    name: string,
    color: string,
    mean: (number | null)[],
    peak: (number | null)[],
  ) =>
    !band || peak.length === 0
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
  // cpu_total for the silhouette and the reporting series; oom_kill_total for
  // the attention band. The rest are memoryBands' five-band partition: it
  // needs mem_free AND mem_total or it falls back to a lone mem_used band,
  // and the others are the subsystems it subtracts.
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
  agent: ["buffer_dropped_total", "post_failures_total"],
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

  const [host, net, filesystem, agent, cores] = await Promise.all([
    ask("host"),
    ask("net"),
    ask("filesystem"),
    // Plots nothing. Fetched for the two delivery counters the attention band
    // reports -- see HostTrends.dropped and .postFailures for why they cannot
    // ride the hosts list instead.
    ask("agent"),
    wantsCores(threads) ? ask("cpu_core") : Promise.resolve(null),
  ]);

  return hostTrendsFrom(host, net, filesystem, agent, cores);
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
  agent: MetricsResponse | null,
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
    window: (host ?? net ?? filesystem ?? agent)?.window ?? null,
    cpu: cpuBands(host, cores).bands,
    mem: memoryBands(host),
    reporting: total,
    // The MEAN of each pixel column, summed across interfaces --
    // trafficSeries() carries why both halves of that are what RRDtool does.
    // The interface that actually burst is one click away on the host page,
    // where the pairs are drawn per interface.
    rx: traffic.rx,
    tx: traffic.tx,
    rxPeak: traffic.rxPeak,
    txPeak: traffic.txPeak,
    fullest: fullestFilesystem(filesystem),
    disk: filesystemBands(filesystem),
    oomKills: counterIncrease(griddedValues(host, 0, "oom_kill_total")),
    // The last value the agent reported, gaps and all -- see HostTrends.dropped
    // for why this one cannot be an increase. lastNumber, not latestValue: the
    // final bucket is null whenever this counter is interesting, and the
    // question here is "what did the agent last say" rather than "what is true
    // in the newest bucket".
    dropped: lastNumber(griddedValues(agent, 0, "buffer_dropped_total")),
    postFailures: counterIncrease(
      griddedValues(agent, 0, "post_failures_total"),
    ),
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

  const [host, net, filesystem, agent, cores] = await Promise.all([
    ask("host", ids),
    ask("net", ids),
    ask("filesystem", ids),
    ask("agent", ids),
    coreIds.length > 0 ? ask("cpu_core", coreIds) : Promise.resolve(null),
  ]);

  for (const id of ids) {
    trends.set(
      id,
      hostTrendsFrom(
        host?.get(id) ?? null,
        net?.get(id) ?? null,
        filesystem?.get(id) ?? null,
        agent?.get(id) ?? null,
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
      rx: trend?.rx ?? [],
      tx: trend?.tx ?? [],
      rxPeak: trend?.rxPeak ?? [],
      txPeak: trend?.txPeak ?? [],
      // null, not a zero percentage: a host whose filesystems have not been
      // read has no fullest one, and an empty green meter would say its
      // disks are empty.
      fullest: trend?.fullest ?? null,
      disk: trend?.disk ?? [],
      // null, not 0: a host whose trends failed to load has not told us
      // there were no kills, and a fleet page must not report silence it
      // never heard. The two delivery counters below follow the same rule --
      // an unanswered `agent` family is "cannot say", never "nothing wrong".
      oomKills: trend?.oomKills ?? null,
      dropped: trend?.dropped ?? null,
      postFailures: trend?.postFailures ?? null,
    };
  });
}

/**
 * Per-container CPU and memory over the same window, keyed by container_key.
 *
 * The container lists were the one place in the app showing a fleet of
 * things over time with no time in them: names, images and a host, and
 * nothing about what any of them was doing. family=container carries
 * cpu_pct, mem_used and mem_limit per container, so the rows can say it.
 */
export interface ContainerTrend {
  cpu: (number | null)[];
  mem: (number | null)[];
  /** The container's own ceiling, or null when it runs unlimited. */
  memLimit: number | null;
}

/**
 * Every container's series in a family=container response, keyed by
 * container_key.
 *
 * Shared with the host page's inventory list and with the enlarged view a
 * reader opens off either list's CPU or Memory cell, so all three read the
 * same columns out of the same response shape.
 */
export function containerTrends(
  res: MetricsResponse | null,
): Map<string, ContainerTrend> {
  const trends = new Map<string, ContainerTrend>();
  if (res === null) return trends;

  res.series.forEach((series, index) => {
    // The keySpec's NAME is "container" (internal/hub/read/family.go), not
    // the SQL expression behind it. Reading series[0] instead would chart a
    // neighbouring container under this one's name.
    const key = series.key.container;
    if (key === undefined) return;
    trends.set(key, {
      cpu: griddedValues(res, index, "cpu_pct"),
      mem: griddedValues(res, index, "mem_used"),
      memLimit: lastNumber(griddedValues(res, index, "mem_limit")),
    });
  });
  return trends;
}
