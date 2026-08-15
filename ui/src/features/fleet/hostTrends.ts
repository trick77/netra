import {
  getMetrics,
  type Host,
  type MetricsResponse,
  type Site,
} from "../../lib/api";
import {
  carriesColumn,
  counterIncrease,
  fsName,
  griddedValues,
  hasReading,
  latestValue,
} from "../../lib/metrics";
import { memoryBands, perCoreBands } from "../../lib/bands";
import { rangeWindow, type Range } from "../../lib/range";
import type { Band } from "../../ui/charts/StackedSparkline";
import type { HostRow } from "./hostColumns";

/**
 * The fleet list's trends: three families per host, turned into the series
 * the host columns draw.
 *
 * This is a fan-out -- one request per family per host -- and it is the cost
 * of the overview's whole premise. The spec is explicit that sparklines are
 * non-negotiable here: a bar shows one instant, and recent history is half
 * of what an overview is for. The read API is per-host by construction
 * (GET /api/v1/hosts/{id}/metrics), so there is no single call that answers
 * this; a fleet-wide metrics endpoint would be the fix, and it does not
 * exist yet.
 *
 * Every request is settled independently. One host answering 500 must cost
 * that host's sparklines, not the fleet's.
 */
export interface HostTrends {
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
 * Sums a keyed family across its series, index by index.
 *
 * A host's traffic is the sum over its interfaces, and a null anywhere in a
 * bucket makes the total for that bucket unknowable rather than smaller --
 * so the sum is null there too. Treating it as zero would draw a dip that
 * never happened.
 */
function sumSeries(
  res: MetricsResponse | null,
  base: string,
): (number | null)[] {
  if (res === null || res.series.length === 0) return [];
  const columns = res.series.map((_, i) => griddedValues(res, i, base));
  const width = columns.reduce((w, c) => Math.max(w, c.length), 0);
  const out: (number | null)[] = [];
  for (let i = 0; i < width; i++) {
    let total = 0;
    let known = false;
    let unknown = false;
    for (const column of columns) {
      const v = column[i];
      if (v === undefined) continue;
      if (v === null) unknown = true;
      else {
        total += v;
        known = true;
      }
    }
    out.push(unknown || !known ? null : total);
  }
  return out;
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
function fullestFilesystem(res: MetricsResponse | null): HostRow["fullest"] {
  if (res === null || res.series.length === 0) return null;
  if (!carriesColumn(res, "used") || !carriesColumn(res, "free")) return null;

  let best: { mount: string; pct: number } | null = null;
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
    const pct = (used / (used + free)) * 100;
    const mount = fsName(res.series[i]!.key, "?");
    if (best === null || pct > best.pct) best = { mount, pct };
  }
  if (best === null) return null;
  return {
    mount: best.mount,
    pct: best.pct,
    others: Math.max(0, measured - 1),
  };
}

// One hue per filesystem, cycling. A host with more mounts than this has
// more than a fleet row could name anyway -- the column's question is "is
// any of them climbing", and the meter beside it names the one that matters.
const FS_COLORS = [
  "var(--s7)",
  "var(--s1)",
  "var(--s2)",
  "var(--s5)",
  "var(--s3)",
  "var(--s8)",
];

/**
 * Every filesystem's Use% over the window, one band each.
 *
 * All of them, not just the fullest: a host's root can sit flat at 40% while
 * a log volume climbs into trouble, and one line for the worst mount hides
 * which of them is moving. It also jumps between filesystems whenever
 * another overtakes, drawing a line no single disk ever followed.
 *
 * df's Use% throughout -- used / (used + free), never used / total, since
 * total includes the root reserve. The same definition the meter beside it
 * and fullestFilesystem() use, so nothing on the row can disagree.
 */
function filesystemBands(res: MetricsResponse | null): Band[] {
  if (res === null || res.series.length === 0) return [];
  if (!carriesColumn(res, "used") || !carriesColumn(res, "free")) return [];

  const bands: Band[] = [];
  for (let i = 0; i < res.series.length; i++) {
    const used = griddedValues(res, i, "used");
    const free = griddedValues(res, i, "free");
    const width = Math.max(used.length, free.length);
    const values: (number | null)[] = [];
    for (let j = 0; j < width; j++) {
      const u = used[j] ?? null;
      const f = free[j] ?? null;
      // A gap is a gap: the host reported nothing for that bucket, which is
      // not the same as the disk being empty.
      values.push(
        u === null || f === null || u + f === 0 ? null : (u / (u + f)) * 100,
      );
    }
    // A filesystem that reported nothing all window is not a flat line at
    // zero; it is a mount with no readings, and drawing it would claim one.
    if (!hasReading(values)) continue;
    bands.push({
      name: fsName(res.series[i]!.key, `fs ${i}`),
      color: FS_COLORS[bands.length % FS_COLORS.length]!,
      values,
    });
  }
  return bands;
}

function lastNumber(values: readonly (number | null)[]): number | null {
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i];
    if (v !== null && v !== undefined) return v;
  }
  return null;
}

async function orNull(
  p: Promise<MetricsResponse>,
): Promise<MetricsResponse | null> {
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
const MAX_PER_CORE = 32;

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
      }),
    );

  const wantCores =
    threads !== null && threads !== undefined && threads <= MAX_PER_CORE;
  const [host, net, filesystem, cores] = await Promise.all([
    ask("host"),
    ask("net"),
    ask("filesystem"),
    wantCores ? ask("cpu_core") : Promise.resolve(null),
  ]);

  // Per-core when the host is small enough to ask for it, and cpu_total
  // otherwise. Never the user/system/iowait/steal breakdown here: that is a
  // different question -- where the time went, rather than which core spent
  // it -- and it has its own panel on the host page.
  // Normalised here and only here: a 4-core and a 32-core host share one
  // 0-100 cell in this list, so the stack has to top out at cpu_total. The
  // host page draws the same cores unnormalised, where the numbers matter
  // more than cross-host comparability.
  const perCore = perCoreBands(cores, { normalise: true });
  // One series, one relation, every host -- see HostTrends.reporting. Gridded
  // once and used twice: as the fallback silhouette for a host too large to
  // fetch per-core, and as the series the status badge is judged from.
  const total = griddedValues(host, 0, "cpu_total");
  const cpu = perCore.length > 0 ? perCore : totalBand(total);

  return {
    cpu,
    mem: memoryBands(host),
    reporting: total,
    rx: sumSeries(net, "rx_bytes"),
    tx: sumSeries(net, "tx_bytes"),
    fullest: fullestFilesystem(filesystem),
    disk: filesystemBands(filesystem),
    oomKills: counterIncrease(griddedValues(host, 0, "oom_kill_total")),
  };
}

/** Joins hosts, their site names and their trends into the rows the columns read. */
export function buildRows(
  hosts: readonly Host[],
  sites: readonly Site[],
  trends: ReadonlyMap<number, HostTrends>,
): HostRow[] {
  const siteNames = new Map(sites.map((site) => [site.id, site.name]));
  return hosts.map((host) => {
    const trend = trends.get(host.id);
    return {
      ...host,
      site_name:
        host.site_id === null ? null : (siteNames.get(host.site_id) ?? null),
      cpu: trend?.cpu ?? [],
      mem: trend?.mem ?? [],
      reporting: trend?.reporting ?? [],
      rx: trend?.rx ?? [],
      tx: trend?.tx ?? [],
      // null, not a zero percentage: a host whose filesystems have not been
      // read has no fullest one, and an empty green meter would say its
      // disks are empty.
      fullest: trend?.fullest ?? null,
      disk: trend?.disk ?? [],
      // null, not 0: a host whose trends failed to load has not told us
      // there were no kills, and a fleet page must not report silence it
      // never heard.
      oomKills: trend?.oomKills ?? null,
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

export async function fetchContainerTrends(
  hostId: number,
  range: Range,
  now?: Date,
): Promise<Map<string, ContainerTrend>> {
  const window = rangeWindow(range, now);
  const res = await orNull(
    getMetrics(hostId, {
      family: "container",
      from: window.from,
      to: window.to,
      step: window.step,
    }),
  );

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
