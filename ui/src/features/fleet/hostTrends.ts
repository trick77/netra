import {
  getMetrics,
  type Host,
  type MetricsResponse,
  type Site,
} from "../../lib/api";
import { carriesColumn, griddedValues } from "../../lib/metrics";
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

// The stacked CPU bands, in the order they stack. At raw resolution the hub
// carries all four; the 5m and 1h rollups carry cpu_total only, so a longer
// range draws one band instead of four. Falling back to cpu_total is the
// honest answer -- inventing the breakdown from a total is not.
const CPU_BANDS = [
  { base: "cpu_user", name: "user", color: "var(--s1)" },
  { base: "cpu_system", name: "system", color: "var(--s2)" },
  { base: "cpu_iowait", name: "iowait", color: "var(--s3)" },
  { base: "cpu_steal", name: "steal", color: "var(--s4)" },
];

// Memory stacks used + buffers + cached + ARC against mem_total, never with
// free as a band: stacking free makes every host look full, which is the one
// reading this column exists to avoid.
const MEM_BANDS = [
  { base: "mem_used", name: "used", color: "var(--s1)" },
  { base: "mem_buffers", name: "buffers", color: "var(--s2)" },
  { base: "mem_cached", name: "cached", color: "var(--s3)" },
  { base: "mem_arc", name: "ARC", color: "var(--s4)" },
];

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

export async function fetchHostTrends(
  hostId: number,
  range: Range,
  now?: Date,
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

  const [host, net, filesystem] = await Promise.all([
    ask("host"),
    ask("net"),
    ask("filesystem"),
  ]);

  return {
    cpu: bandsFrom(host, CPU_BANDS, {
      base: "cpu_total",
      name: "busy",
      color: "var(--s1)",
    }),
    // No fallback for memory: mem_total is the chart's ceiling rather than
    // a band, so a rolled-up tier simply draws fewer bands.
    mem: bandsFrom(host, MEM_BANDS),
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
