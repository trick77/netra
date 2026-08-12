/**
 * The stacked CPU and memory bands, computed once for every view that draws
 * them.
 *
 * The fleet row and the host page show the same host, and a reader moving
 * between them is entitled to see the same shape. Both call in here rather
 * than each deriving bands from the raw columns, because "used" in
 * particular is a subtraction with several ways to get it subtly wrong.
 */
import { griddedValues, carriesColumn } from "./metrics";
import type { MetricsResponse } from "./api";
import type { Band } from "../ui/charts/StackedSparkline";

/** Colours are token references; index.css owns the palette. */
const USED = "var(--s1)";
const ARC = "var(--s5)";
const BUFFERS = "var(--s2)";
const CACHED = "var(--s3)";
const SHARED = "var(--s4)";

/**
 * The memory stack, bottom to top, as a partition of mem_total.
 *
 * mem_used is NOT the bottom band, and that is the whole point of this
 * function. The agent reports it as MemTotal - MemAvailable, so it already
 * contains the ZFS ARC and the unreclaimable shmem pages -- stacking those
 * on top of it draws the same bytes twice and can push the stack through its
 * own ceiling. The measured parts are stacked instead and "used" is whatever
 * is left over, which also makes the chart self-correcting: any overlap
 * between ARC and reclaimable slab moves bytes between bands rather than
 * inventing memory the host does not have.
 *
 * Free is never a band. It is the gap between the top of the stack and
 * mem_total, and stacking it would make every host look full -- the one
 * reading these charts exist to avoid.
 */
export function memoryBands(res: MetricsResponse | null): Band[] {
  if (res === null) return [];

  // Without mem_free there is no partition to compute: used would silently
  // absorb the entire free pool and every host would read as nearly full.
  // One true band beats five wrong ones, so this falls back to what older
  // data can actually support.
  if (!carriesColumn(res, "mem_free") || !carriesColumn(res, "mem_total")) {
    const used = griddedValues(res, 0, "mem_used");
    return used.length === 0
      ? []
      : [{ name: "used", color: USED, values: used }];
  }

  const total = griddedValues(res, 0, "mem_total");
  const free = griddedValues(res, 0, "mem_free");
  const buffers = optional(res, "mem_buffers");
  const shared = optional(res, "mem_shared");
  const arc = optional(res, "mem_zfs_arc");
  // Reclaimable slab is neither Buffers nor Cached, but it is cache all the
  // same and there is nothing useful to say about it on its own at this
  // size. Folding it in keeps the band count at five.
  const cached = add(
    optional(res, "mem_cached"),
    optional(res, "mem_sreclaimable"),
  );

  const width = Math.max(total.length, free.length);
  const used: (number | null)[] = [];
  for (let i = 0; i < width; i++) {
    const t = total[i] ?? null;
    const parts = sumAt(i, [free, buffers, cached, shared, arc]);
    // A null anywhere makes the remainder unknowable rather than smaller.
    // Drawing zero there would claim the host had no memory in use.
    used.push(t === null || parts === null ? null : Math.max(0, t - parts));
  }

  // Bottom to top: the resident pages first, then the caches, so the
  // volatile part of the chart is the part that moves.
  return [
    { name: "used", color: USED, values: used },
    { name: "ARC", color: ARC, values: arc },
    { name: "buffers", color: BUFFERS, values: buffers },
    { name: "cached", color: CACHED, values: cached },
    { name: "shared", color: SHARED, values: shared },
  ].filter((b) => b.values.some((v) => v !== null));
}

/**
 * One band per CPU core, each scaled so the whole stack sums to cpu_total.
 *
 * Every core reports 0-100, so a raw stack of N cores runs to N x 100 and
 * overflows a chart whose ceiling is 100. Dividing by N makes the top of the
 * stack the MEAN across cores -- which is cpu_total -- so a 4-core host and a
 * 32-core host stay comparable in a list, and the same chart agrees with the
 * number the meter shows for that instant.
 *
 * N is the response's own series count, never the host's inventory `threads`:
 * the two disagree when a core stops reporting mid-window, and only the
 * response knows how many series it actually carries.
 */
export function perCoreBands(res: MetricsResponse | null): Band[] {
  if (res === null || res.series.length === 0) return [];
  const n = res.series.length;

  return res.series
    .map((series, i) => {
      const values = griddedValues(res, i, "busy").map((v) =>
        v === null ? null : v / n,
      );
      return {
        // The key names the core, so a hovered band is identifiable even
        // though thirty-two of them cannot each own a hue.
        name: `core ${series.key.core ?? i}`,
        // Two alternating tokens rather than a cycle of six: with this many
        // bands colour cannot carry identity, and all it has left to do is
        // keep neighbours apart.
        color: i % 2 === 0 ? "var(--s1)" : "var(--s6)",
        values,
      };
    })
    .filter((b) => b.values.length > 0);
}

function optional(res: MetricsResponse, base: string): (number | null)[] {
  return carriesColumn(res, base) ? griddedValues(res, 0, base) : [];
}

/** Index-wise sum of two series; a missing series contributes nothing. */
function add(a: (number | null)[], b: (number | null)[]): (number | null)[] {
  if (a.length === 0) return b;
  if (b.length === 0) return a;
  const width = Math.max(a.length, b.length);
  const out: (number | null)[] = [];
  for (let i = 0; i < width; i++) {
    const x = a[i] ?? null;
    const y = b[i] ?? null;
    out.push(x === null || y === null ? null : x + y);
  }
  return out;
}

/**
 * The total at one index across several series, or null if any of them is
 * unknown there. An absent series contributes nothing, which is different
 * from a null: a host with no ZFS has no ARC to add, while a host whose
 * sample is missing makes the whole sum unknowable.
 */
function sumAt(i: number, series: (number | null)[][]): number | null {
  let total = 0;
  for (const s of series) {
    if (s.length === 0) continue;
    const v = s[i] ?? null;
    if (v === null) return null;
    total += v;
  }
  return total;
}
