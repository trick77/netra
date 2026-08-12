import {
  getMetrics,
  type Host,
  type MetricsResponse,
  type Site,
} from "../../lib/api";
import { carriesColumn, griddedValues } from "../../lib/metrics";
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
  rx: (number | null)[];
  tx: (number | null)[];
  fullest: HostRow["fullest"];
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

function bandsFrom(
  res: MetricsResponse | null,
  specs: readonly { base: string; name: string; color: string }[],
  fallback?: { base: string; name: string; color: string },
): Band[] {
  if (res === null) return [];
  const bands = specs
    .map((spec) => ({
      name: spec.name,
      color: spec.color,
      values: griddedValues(res, 0, spec.base),
    }))
    .filter((band) => band.values.length > 0);

  if (bands.length > 0 || fallback === undefined) return bands;

  // This tier carries the total but not the breakdown. One band is a true
  // silhouette; four fabricated ones would not be.
  const values = griddedValues(res, 0, fallback.base);
  return values.length === 0
    ? []
    : [{ name: fallback.name, color: fallback.color, values }];
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
    const used = lastNumber(griddedValues(res, i, "used"));
    const free = lastNumber(griddedValues(res, i, "free"));
    if (used === null || free === null || used + free === 0) continue;
    measured++;
    const pct = (used / (used + free)) * 100;
    const mount = res.series[i]!.key.filesystem ?? "?";
    if (best === null || pct > best.pct) best = { mount, pct };
  }
  if (best === null) return null;
  return {
    mount: best.mount,
    pct: best.pct,
    others: Math.max(0, measured - 1),
  };
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
  const cpu =
    perCore.length > 0
      ? perCore
      : bandsFrom(host, [], {
          base: "cpu_total",
          name: "busy",
          color: "var(--s1)",
        });

  return {
    cpu,
    mem: memoryBands(host),
    rx: sumSeries(net, "rx_bytes"),
    tx: sumSeries(net, "tx_bytes"),
    fullest: fullestFilesystem(filesystem),
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
      rx: trend?.rx ?? [],
      tx: trend?.tx ?? [],
      // null, not a zero percentage: a host whose filesystems have not been
      // read has no fullest one, and an empty green meter would say its
      // disks are empty.
      fullest: trend?.fullest ?? null,
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
