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

/**
 * The memory bands, in the order they stack.
 *
 * Colours are token references; index.css owns the palette. The order is the
 * validated categorical order -- blue, orange, aqua, yellow, magenta -- and
 * not the s1..s5 sequence, which put blue, aqua, violet and magenta next to
 * each other and read as one muddy mass in a 32px cell. Orange and yellow are
 * the two hues that ramp never had, and they are most of why this chart is
 * legible at fleet-row size. Yellow and orange are deliberately NOT adjacent
 * in the stack: that is the one pair in this set that fails the separation
 * floors.
 */
const USED = "var(--s1)";
const ARC = "var(--s7)";
const BUFFERS = "var(--s2)";
const CACHED = "var(--s8)";
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
    const f = free[i] ?? null;
    // mem_total and mem_free are reported by every kernel, so a null in
    // either means the host reported nothing for this bucket and the
    // remainder is genuinely unknowable. Drawing zero there would claim the
    // host had no memory in use.
    if (t === null || f === null) {
      used.push(null);
      continue;
    }
    // The rest are subsystems a host may simply not have. A machine without
    // ZFS reports mem_zfs_arc as NULL for every bucket -- the column exists
    // on the family whether or not the host uses it -- and treating that as
    // "unknown" rather than "none" made the remainder null on every non-ZFS
    // host, which silently deleted the used band from three hosts out of
    // four. An absent subsystem contributes nothing; it does not make the
    // arithmetic unanswerable.
    const parts = sumOptional(i, [buffers, cached, shared, arc]);
    used.push(Math.max(0, t - f - parts));
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
 * One band per CPU core.
 *
 * `normalise` decides which of two true charts this is, and the choice is
 * forced by where it is drawn:
 *
 * - Normalised (the fleet list): every core is divided by the core count, so
 *   the top of the stack is the MEAN across cores -- cpu_total -- and a
 *   4-core and a 32-core host can share one 0-100 cell and stay comparable.
 *   The cost is that a band's value is that core's share of the host total,
 *   not its own utilisation: a core at 43% busy on a 32-core box reads 1.3.
 * - Raw (the host page): each band is the core's real utilisation, so every
 *   number in the tooltip and the stats table is the number that core
 *   actually reported. The stack then runs to N x 100, which is why the
 *   chart drawing it hides its y axis -- the height is a shape, not a
 *   quantity. This is what beszel does, and it is right there: one host,
 *   no cross-host comparison to protect, and a reader who wants to know
 *   what core 7 is doing.
 *
 * N is the response's own series count, never the host's inventory `threads`:
 * the two disagree when a core stops reporting mid-window, and only the
 * response knows how many series it actually carries.
 */
export function perCoreBands(
  res: MetricsResponse | null,
  { normalise = false }: { normalise?: boolean } = {},
): Band[] {
  if (res === null || res.series.length === 0) return [];
  const n = res.series.length;

  return res.series
    .map((series, i) => {
      const raw = griddedValues(res, i, "busy");
      const values = normalise
        ? raw.map((v) => (v === null ? null : v / n))
        : raw;
      return {
        // The key names the core, so a hovered band is identifiable even
        // though thirty-two of them cannot each own a hue.
        name: `core ${series.key.core ?? i}`,
        color: coreColor(i, n),
        values,
      };
    })
    .filter((b) => b.values.length > 0);
}

/**
 * The colour of one core's band in a stack of `n`.
 *
 * A spectrum sweep, and deliberately NOT a design token or a single-hue ramp.
 * Both of those were tried and both are unreadable here:
 *
 * - Two alternating tokens gave a 32-band stack no internal structure at all.
 * - A one-hue light-to-dark ramp -- the textbook answer for ordered bands --
 *   puts adjacent steps 0.047 apart in L across eight steps, let alone
 *   thirty-two. The palette validator fails it outright, and on screen it is
 *   a blob of blues.
 *
 * Hue is the only channel with enough range to separate this many neighbours,
 * which is what every tool that draws this chart well does. It encodes nothing
 * -- core 7 is not "hotter" than core 3 for being further round the wheel --
 * and nobody reads a core's identity off its colour. The job is purely to keep
 * one band distinguishable from the next, and the hairline stroke each band
 * carries does the rest.
 *
 * Raw HSL rather than a var(): the ramp has to subdivide to fit the host, so
 * there is no fixed set of steps to name, and mid-lightness saturated hues sit
 * legibly on both the light and the dark surface.
 */
function coreColor(i: number, n: number): string {
  // Violet through blue, teal, green and yellow to red -- the long way round,
  // so even 32 cores land ~9 degrees apart. n === 1 takes the first hue rather
  // than dividing by zero.
  const hue = n <= 1 ? 265 : 265 - (285 * i) / (n - 1);
  // Saturation and lightness come from index.css so the ramp follows the
  // theme; only the hue is computed here, because the sweep has to subdivide
  // to fit the host and there is no fixed set of steps to name.
  return `hsl(${((hue % 360) + 360) % 360} var(--chart-saturation) var(--chart-lightness))`;
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
 * The total at one index across series that may legitimately be absent.
 *
 * A null contributes nothing rather than poisoning the sum. That is right
 * here and would be wrong for a required column: these are subsystems a host
 * may not have (no ZFS, no tmpfs worth reporting), and the columns exist on
 * the family regardless of whether any given host uses them. The caller
 * checks mem_total and mem_free separately, and those going null is what
 * marks a bucket the host did not report at all.
 */
function sumOptional(i: number, series: (number | null)[][]): number {
  let total = 0;
  for (const s of series) {
    if (s.length === 0) continue;
    total += s[i] ?? 0;
  }
  return total;
}
