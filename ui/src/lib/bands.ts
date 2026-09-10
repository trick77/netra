/**
 * The stacked CPU and memory bands, computed once for every view that draws
 * them.
 *
 * The fleet row and the host page show the same host, and a reader moving
 * between them is entitled to see the same shape. Both call in here rather
 * than each deriving bands from the raw columns, because "used" in
 * particular is a subtraction with several ways to get it subtly wrong.
 */
import { fsName, griddedValues, carriesColumn, hasReading } from "./metrics";
import { SWEPT_FILL_OPACITY } from "../ui/charts/size";
import type { ContainerTrend } from "./containers";
import type { MetricsResponse } from "./api";
import type { Band } from "../ui/charts/StackedSparkline";

/**
 * The memory bands, in the order they stack.
 *
 * Colours are token references; index.css owns the palette.
 *
 * THE STACK HAS ITS OWN PALETTE, --mem-*, and does not draw from s1-s8. That
 * is the point. The series ramp is tuned for a chart with a legend under it
 * that a reader is looking AT; this chart is drawn once per fleet row at 45px
 * and is meant to be looked PAST until something is wrong. Every --mem-*
 * colour is a dimmer relative of the s-token it grew out of.
 *
 * THE RAMP IS THE EXPLANATION. The stack is ordered by how readily the kernel
 * can take the bytes back, and the colour says the same thing the order does:
 * warm and present at the bottom for the pages nobody can have back, cooling
 * and dimming upward, ending in a grey you have to look for. Amber -> violet
 * -> teal -> slate -> dark grey. A reader who has never been told what a band
 * means can still see that the bottom is the part that costs something.
 *
 * CACHED IS THE DIMMEST BAND, NOT THE BRIGHTEST. It was #b6b3ab, 8.23:1 on
 * --surface, so the band meaning "you can have this back" glared harder than
 * the one meaning you cannot. It went to #4f4c48 at 2.02:1 and then to
 * #3d3a37 at 1.53:1: the band the host will hand back first is the one your
 * eye reaches last. Buffers is a dark steel blue one step above it, so the
 * TOP of a memory cell is never the loudest thing on the row.
 *
 * The second darkening is a pure lightness drop at the same hue, which is why
 * it costs nothing: cached separates FROM buffers and the dashed total-RAM
 * rule by more than it did, not less. What bounds it is the card behind the
 * chart -- a band that reaches --surface has disappeared rather than
 * quietened -- and at dE2000 10.2 from --surface there is about one more step
 * of this and no more.
 *
 * AMBER IS BACK, KNOWINGLY. ARC was --s7 (#d95926) and cached was --s8 amber
 * (#c98500); both were taken out because a memory band, "something needs
 * attention" and a severity were the same colour two columns apart.
 * --mem-used is amber again at ~38 degrees, but a dark one: dE 11.2 from
 * --accent where a full-brightness amber measures 5.8. It is allowed because
 * the band it colours is a QUANTITY rather than a state: always present, never
 * appearing or disappearing to mean something, with severity on the same row
 * carried by a rail, a dot and a word as well as a hue. The magenta it
 * replaces was correct by the rule and still made the cell shout.
 *
 * ORDER IS STILL THE SAFETY MECHANISM. The pairs that have to separate are
 * decided by which bands touch, so re-check if the stacking order changes.
 * Adjacent separation, dE2000 under normal vision and Machado
 * protan/deutan/tritan at full severity, worst figure per pair: used/shared
 * 32.7, shared/ARC 13.1, ARC/buffers 10.9, buffers/cached 18.7 -- every pair
 * above the 9.1 the --s7/--s8 set was shipped at. Those were measured at
 * cached #4f4c48; the later drop to #3d3a37 only moves that last pair, and
 * only apart -- under normal vision it goes 19.3 to 20.7, and a lightness
 * gap is the one kind of separation CVD leaves alone. On --surface the bands
 * now run 3.76 down to 1.53, all under the 4:1 the series hold and
 * deliberately so; see the tokens' own note in index.css.
 */
const USED = "var(--mem-used)";
const SHARED = "var(--mem-shared)";
const ARC = "var(--mem-arc)";
const BUFFERS = "var(--mem-buffers)";
const CACHED = "var(--mem-cached)";

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
  //
  // The test is on the DATA, not only on the column. An upgraded hub carries
  // mem_free on the family for every host, so carriesColumn is true even for
  // a host still running an agent that never sends it -- all five bands then
  // come back all-null, the trailing filter drops every one of them, and the
  // page says "reported no memory samples" about a host reporting perfectly
  // well. A column the tier carries but the host never filled is exactly as
  // absent as a column the tier does not have.
  const hasColumns =
    carriesColumn(res, "mem_free") && carriesColumn(res, "mem_total");
  const total = hasColumns ? griddedValues(res, 0, "mem_total") : [];
  const free = hasColumns ? griddedValues(res, 0, "mem_free") : [];
  if (!hasReading(total) || !hasReading(free)) {
    // And the fallback answers to the same rule it was just given. A host
    // whose agent was down for the whole window reports nothing at all,
    // mem_used included: a lone band of nulls draws an empty chart where
    // naming the absence is the entire point. The five-band return below has
    // always dropped bands this way; the one-band path was checking only its
    // LENGTH, which an all-null series passes.
    const used = griddedValues(res, 0, "mem_used");
    return hasReading(used)
      ? [{ name: "used", color: USED, values: used }]
      : [];
  }
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

  // Bottom to top, in increasing order of how readily the kernel can take the
  // bytes back: the pages it cannot reclaim first, then the caches it can, so
  // the volatile part of the chart is the part nearest the free gap.
  //
  // shared sits with used rather than above the caches, which is where it used
  // to be drawn. It is Shmem -- tmpfs and shm pages with no backing store --
  // so under pressure it cannot be dropped at all, only swapped. Painting it
  // topmost, against the free gap, told the reader it was the first thing the
  // host would hand back, which is the exact opposite of the truth. htop puts
  // shared below cache for the same reason. ARC stays under buffers and cached
  // because it is reclaimable but stickier than page cache.
  //
  // This order is also what the palette above is validated against -- the
  // colours were assigned to fit it, not the other way round, and the ramp
  // from full chroma to neutral only reads as reclaimability while the bands
  // stay in this sequence. Reordering these five lines changes which pairs sit
  // next to each other and therefore which pairs have to clear the separation
  // floors; re-check the figures in that comment if you do.
  return [
    { name: "used", color: USED, values: used },
    { name: "shared", color: SHARED, values: shared },
    { name: "ARC", color: ARC, values: arc },
    { name: "buffers", color: BUFFERS, values: buffers },
    { name: "cached", color: CACHED, values: cached },
  ].filter((b) => hasReading(b.values));
}

/**
 * One band per CPU core.
 *
 * `normalise` decides which of two true charts this is, and the choice is
 * forced by where it is drawn:
 *
 * - Normalised (the fleet list, and the host page's Overview and System
 *   charts): every core is divided by the core count, so the top of the
 *   stack is the MEAN across cores -- cpu_total -- and a 4-core and a
 *   32-core host can share one 0-100 cell and stay comparable. It is also
 *   what lets a panel be pinned to a 0-100 axis, so a reader can tell an
 *   idle host from a busy one without enlarging it. The cost is that a
 *   band's value is that core's share of the host total, not its own
 *   utilisation: a core at 43% busy on a 32-core box reads 1.3, so a chart
 *   drawing these needs a formatter with the decimals to say so.
 * - Raw (the System tab's "CPU cores" panel, and nothing else): each band is
 *   the core's real utilisation, so every number in the tooltip and the
 *   stats table is the number that core actually reported. The stack then
 *   runs to N x 100, which is why the chart drawing it hides its y axis --
 *   the height is a shape, not a quantity. This is what beszel does, and it
 *   is right there: one host, no cross-host comparison to protect, and a
 *   reader who wants to know what core 7 is doing.
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
        // The key names the core. The sweep gives each one its own hue now,
        // but a hue is not a label: this is what a hovered band reads out.
        name: `core ${series.key.core ?? i}`,
        color: seriesHue(i, n, CORE_HUE_OFFSET),
        fill: SWEPT_FILL_OPACITY,
        values,
      };
    })
    .filter((b) => b.values.length > 0);
}

/**
 * One band per container on a host, for the three stacked Docker panels above
 * its Containers list.
 *
 * The same construction perCoreBands uses, for the same reason: an unbounded
 * number of series that no fixed palette can name apart, so cpu and mem take
 * seriesHue's sweep and the band's name -- the container_key -- is what
 * identifies it beyond that.
 *
 * "net" is the exception, and deliberately. It returns bands INTERLEAVED --
 * [c1 in, c1 out, c2 in, c2 out, ...] -- which is the order MirrorStackMarks
 * reads its halves off (every other series, ui/charts/Chart.tsx), and it
 * colours them from IN_SHADES and OUT_SHADES rather than the sweep. Direction
 * is the first question a traffic chart answers, and green-in over amber-out
 * is what every other traffic mark in the app already says.
 *
 * The interleaved pair means a container contributes TWO entries, so the
 * shade index is the container's position and not the band's -- container k
 * is --in-{k%4} above the midline and --out-{k%4} below it, and its two
 * halves belong to each other.
 *
 * Band ORDER is the response's, never re-sorted by size. The host page polls,
 * and a stack re-ordered on every poll would shuffle its bands between
 * refreshes; the enlarged view refetches at its own range and would order
 * them differently again from the panel it was opened out of.
 *
 * THE NULL RULE IS LOAD-BEARING. stackBands (ui/charts/geometry.ts) breaks
 * EVERY band at an index where ANY series is null -- a running total is
 * undefined there -- and griddedValues puts each container on the whole
 * window's grid. So a container started two hours into a 24h window carries
 * nulls for the other twenty-two, and stacked raw it would blank the entire
 * chart rather than its own band. perCoreBands never meets this because cores
 * do not come and go, and the memory stack sums its parts before stacking.
 *
 * The two cases have to be told apart, and only the whole response can:
 *
 * - some container reported in this bucket -> a null is that container not
 *   running, and 0 is what it consumed. A fact, not a fabricated reading.
 * - NO container reported in this bucket -> the host said nothing, and the
 *   gap stays a gap. Absent is never zero, here as everywhere else.
 */
export function containerBands(
  /** Already decoded, by containerTrends. The caller holds it because the
   * list's own sparklines read the same three columns out of the same
   * response, and decoding it per panel as well was three passes where one
   * does -- on a page that polls. */
  byKey: ReadonlyMap<string, ContainerTrend>,
  metric: "cpu" | "mem" | "net",
): Band[] {
  if (metric === "net") return containerNetBands(byKey);

  const trends = [...byKey.entries()]
    .map(([key, trend]) => ({ key, values: trend[metric] }))
    .filter((t) => t.values.length > 0);
  if (trends.length === 0) return [];

  const reported = bucketsWithAnyReading(trends.map((t) => t.values));
  const n = trends.length;

  return trends.map((trend, i) => ({
    name: trend.key,
    color: seriesHue(i, n, CONTAINER_HUE_OFFSET),
    fill: SWEPT_FILL_OPACITY,
    values: reported.map((any, j) => trend.values[j] ?? (any ? 0 : null)),
  }));
}

/**
 * The mirrored half of containerBands: one in band and one out band per
 * container, interleaved.
 *
 * A container is kept only when it reported at least one of the two -- a
 * container with rx and no tx is a real reading and draws one half -- and
 * both halves are then emitted, so the pairing MirrorStackMarks relies on
 * (index 2k is up, 2k+1 is down) holds however sparse the data is. An absent
 * half is an empty series rather than a missing entry, which the null rule
 * below then fills exactly as it fills a container that was not running.
 *
 * The reported-bucket test spans BOTH directions: a bucket where some
 * container moved bytes either way is a bucket the host answered in, and a
 * container silent in it consumed nothing rather than being unknown.
 */
function containerNetBands(byKey: ReadonlyMap<string, ContainerTrend>): Band[] {
  const trends = [...byKey.entries()]
    .map(([key, trend]) => ({ key, rx: trend.netRx, tx: trend.netTx }))
    .filter((t) => t.rx.length > 0 || t.tx.length > 0);
  if (trends.length === 0) return [];

  const reported = bucketsWithAnyReading(
    trends.flatMap((t) => [t.rx, t.tx]).filter((v) => v.length > 0),
  );
  const fill = (values: (number | null)[]): (number | null)[] =>
    reported.map((any, j) => values[j] ?? (any ? 0 : null));

  return trends.flatMap((trend, i) => [
    {
      name: `${trend.key} in`,
      color: IN_SHADES[i % IN_SHADES.length]!,
      values: fill(trend.rx),
    },
    {
      name: `${trend.key} out`,
      color: OUT_SHADES[i % OUT_SHADES.length]!,
      values: fill(trend.tx),
    },
  ]);
}

/**
 * Per-bucket: did ANY of these series report a number there?
 *
 * Measured over the longest of them, not the first: griddedValues returns the
 * window's grid, but a family answered for a shorter window -- or a series
 * truncated by the point limit -- leaves some rows shorter, and measuring off
 * series[0] would read `undefined` past its end and call every later bucket
 * silent. stackBands measures its own width the same way and for the same
 * reason.
 */
function bucketsWithAnyReading(
  series: readonly (number | null)[][],
): boolean[] {
  const n = series.reduce((longest, s) => Math.max(longest, s.length), 0);
  const out = new Array<boolean>(n).fill(false);
  for (const values of series) {
    for (let i = 0; i < values.length; i++) {
      const v = values[i];
      if (v !== null && v !== undefined) out[i] = true;
    }
  }
  return out;
}

/**
 * The total across a container stack at each bucket, under the same rule the
 * bands are built with: a bucket nobody reported in is null, never 0.
 *
 * This is what the panel's headline reads, so the number printed beside the
 * chart and the silhouette drawn in it cannot come to disagree.
 */
export function containerStackTotal(bands: readonly Band[]): (number | null)[] {
  if (bands.length === 0) return [];
  const n = bands.reduce((longest, b) => Math.max(longest, b.values.length), 0);
  return Array.from({ length: n }, (_, i) => {
    let total: number | null = null;
    for (const band of bands) {
      const v = band.values[i];
      if (v === null || v === undefined) continue;
      total = (total ?? 0) + v;
    }
    return total;
  });
}

/**
 * The traffic stack's two walks -- one band per container, in above the
 * midline and out below it.
 *
 * DIRECTION WINS, and the container is a lightness step within its half. It
 * is the construction the host's per-interface Traffic panel already uses,
 * and the reason is the same one index.css gives there: the fleet row's
 * traffic cell, the host Traffic panel and this one are the same fact at
 * three sizes, and a panel that re-hued its bands per container would be a
 * different chart from the cell it was opened out of. So the CPU and memory
 * panels beside this one sweep hue per container, and this one does not:
 * green is inbound bytes and amber is outbound, here as everywhere.
 *
 * Four steps, wrapping. A host running more than four containers repeats a
 * shade, exactly as more than four interfaces does; the shade only ever told
 * a band from the one beside it, and the band's key is what names it.
 */
export const IN_SHADES = [
  "var(--in-1)",
  "var(--in-2)",
  "var(--in-3)",
  "var(--in-4)",
];

export const OUT_SHADES = [
  "var(--out-1)",
  "var(--out-2)",
  "var(--out-3)",
  "var(--out-4)",
];

/** Where each sweep starts, in degrees. See seriesHue. */
export const CORE_HUE_OFFSET = 212;
export const CONTAINER_HUE_OFFSET = 0;

/**
 * The colour of band `i` in a stack of `n` anonymous ones -- one core of
 * many, one container of many.
 *
 * A full hue sweep, evenly divided, at a fixed saturation and lightness that
 * index.css owns as --series-s and --series-l. It is beszel's construction
 * (site/src/components/charts/hooks.ts), taken because it answers the thing a
 * four-step wrapping walk could not: on a host with eighteen containers the
 * walk repeats itself four and a half times, so the stack tells you where one
 * band ends and says nothing about which container you are looking at. Hue is
 * the only channel with the range to keep that many neighbours apart.
 *
 * ORDER IS THE RESPONSE'S, never usage rank. beszel sorts its containers by
 * total usage before assigning hue, which re-colours the whole stack whenever
 * the busiest container changes. Position in the response does not move like
 * that, so a container keeps its place in the stack between polls.
 *
 * THE STEP IS 360/n, DIVIDING THE WHOLE CIRCLE, and it is the step rather
 * than the palette that makes this read as beszel. Neighbours in the stack
 * get neighbouring hues, so the bands sweep bottom to top as one spectrum --
 * red, amber, green, teal, blue, violet -- and the stack looks like a
 * deliberate ramp. It was briefly the golden angle, 137.508 degrees, which is
 * independent of the count and therefore perfectly stable; it also puts every
 * band two thirds of the wheel from the one below it, and the result is a
 * jumble. Olive against blue against purple is a set of unrelated colours,
 * not a family, which is exactly the "reads as a category" objection the old
 * four-step walk was defended with.
 *
 * WHAT THAT COSTS, stated because it is real: every hue depends on `n`, so a
 * container starting or stopping re-colours the whole stack on the next poll.
 * Three containers sit at 0/120/240; a fourth appears and they redraw at
 * 0/90/180/270, so the second one goes green to yellow with nothing else on
 * the page changing. The enlarged view can differ from the panel for the same
 * reason, since it refetches at a wider range and may see a container the
 * panel's window does not. That is accepted: the colour identifies a band
 * against its neighbours within one chart, never across time, and the key on
 * hover and the enlarged view's stats table are what name a container.
 *
 * This is NOT the monotonic one-hue ramp that was rejected here before: that
 * one subdivided a single hue's lightness, landing 0.047 apart in L per step
 * across thirty-two bands. A hue sweep holds full lightness contrast between
 * every pair no matter how many bands share it, because it never subdivides
 * the channel that carries the contrast.
 *
 * The offsets keep each sweep anchored to the token its chart used to lead
 * with: CORE_HUE_OFFSET is --cpu-1 #3987e5's own hue, so band 0 is still that
 * blue. totalBand() in features/fleet/hostTrends.ts -- the cpu_total
 * silhouette drawn when the cpu_core family does not answer -- computes band 0
 * of this sweep rather than the --s1 it used to hold, so a one-core host draws
 * the same chart either way. That property predates the sweep and survives it;
 * at the series saturation the two are 12 dE apart if the fallback is left
 * behind, which is why it is not.
 *
 * Accepted cost: container hue 0 lands in the 0-41 degree band index.css
 * reserves for --accent and the status palette. A full sweep cannot avoid
 * status hues once a host runs more than a handful of containers, and these
 * bands carry no severity -- the rail, the dot and the word to their left do.
 */
export function seriesHue(i: number, n: number, offset: number): string {
  const hue = (offset + (i * 360) / Math.max(1, n)) % 360;
  return `hsl(${hue.toFixed(1)}, var(--series-s), var(--series-l))`;
}

function optional(res: MetricsResponse, base: string): (number | null)[] {
  return carriesColumn(res, base) ? griddedValues(res, 0, base) : [];
}

/**
 * Index-wise sum of two series that may each legitimately be absent -- both
 * as a whole series and at any one index.
 *
 * A null on ONE side contributes nothing rather than poisoning the total, the
 * same rule sumOptional() below already documents and for the same reason:
 * these are optional subsystems, and mem_sreclaimable is the case that
 * proves it. A kernel with no SReclaimable line is supported (memory.go,
 * TestMemoryAbsentShmemLeavesCachedWhole) and reports it NULL for every
 * bucket, so `x === null || y === null -> null` made the whole cached band
 * null. The trailing filter then dropped it, sumOptional counted the missing
 * band as 0 in the remainder, and several GB of page cache were reported as
 * resident memory.
 *
 * Both null IS still null: neither input said anything about that bucket, so
 * there is no total to state. That is the one case where the sum really is
 * unknowable rather than merely partial.
 */
function add(a: (number | null)[], b: (number | null)[]): (number | null)[] {
  if (a.length === 0) return b;
  if (b.length === 0) return a;
  const width = Math.max(a.length, b.length);
  const out: (number | null)[] = [];
  for (let i = 0; i < width; i++) {
    const x = a[i] ?? null;
    const y = b[i] ?? null;
    out.push(x === null && y === null ? null : (x ?? 0) + (y ?? 0));
  }
  return out;
}

// Whether a series carries a reading at all is lib/metrics.ts's hasReading():
// the distinction memoryBands() turns on is that a column the answering tier
// does not have comes back as an empty array, and a column it has but this
// host never filled comes back all null. Both mean "nothing to compute a
// partition from", and only the first was being checked.

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

// One hue per filesystem, cycling. A host with more mounts than this has more
// than a fleet row could name anyway -- the column's question is "is any of
// them climbing", and the meter beside it names the one that matters.
//
// FOUR, not six, and validated on ALL PAIRS rather than adjacent ones: these
// are separate lines in one frame, so a reader compares any two of them, not
// just neighbours. Under that harder test six hues cannot be found at all --
// the previous set opened with --s7 orange (attention's hue) and included both
// --s3 and --s5, a violet pair that collapses under protanopia. Blue, amber,
// magenta and cyan clear every pair on the #1b1b1a surface.
//
// The fifth mount reuses blue. That is honest for this column: past four
// lines nobody is tracing identity by hue anyway, and the meter names the
// mount that matters.
const FS_COLORS = ["var(--s1)", "var(--s8)", "var(--s4)", "var(--s6)"];

/**
 * One filesystem's Use% over the window, by its index into res.series.
 *
 * Split out of filesystemBands below so a caller that has already picked a
 * mount -- the fleet row's Disk cell, through fullestFilesystem() -- can draw
 * that one on the same definition, without re-deriving the ratio itself. By
 * INDEX and not by name, because filesystemBands drops the mounts that
 * reported nothing, so a band's position in its list is not the series'
 * position in the response.
 */
export function fsUsePercent(
  res: MetricsResponse,
  index: number,
): (number | null)[] {
  const used = griddedValues(res, index, "used");
  const free = griddedValues(res, index, "free");
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
  return values;
}

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
export function filesystemBands(res: MetricsResponse | null): Band[] {
  if (res === null || res.series.length === 0) return [];
  if (!carriesColumn(res, "used") || !carriesColumn(res, "free")) return [];

  const bands: Band[] = [];
  for (let i = 0; i < res.series.length; i++) {
    const values = fsUsePercent(res, i);
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

/**
 * The four CPU states -- user, system, iowait, steal -- drawn as one swept
 * stack, so the CPU time breakdown reads as the same chart family as the
 * per-core stack sitting beside it in the System tab's Resources group.
 *
 * WHY NOT seriesHue(i, 4, CORE_HUE_OFFSET) DIRECTLY, which is what "the same
 * as CPU cores" would literally mean: a four-way sweep from 212 lands at
 * 212 / 302 / 32 / 122, and 32 is inside the 0-41 degree band index.css
 * reserves for --accent and the status palette while 122 is --ok's green.
 * The sweep accepts that collision for cores and containers, and the argument
 * there is explicit: those bands are ANONYMOUS and carry no severity. These
 * four are named states, and the panel exists to say that iowait and steal
 * are the two where something else held the CPU up. Painting steal ok-green
 * and iowait accent-orange asserts the opposite, by accident.
 *
 * So the hues stay the ones index.css already measured for a legended
 * four-band chart -- --s1..--s4, at 213 / 159 / 249 / 337, chosen to clear
 * that reserved band -- and what changes is how they are DRAWN: the sweep's
 * own --series-s and --series-l instead of four separately tuned hexes, and
 * SWEPT_FILL_OPACITY instead of the opaque default. That is the part of the
 * difference a reader actually sees between the two stacks.
 *
 * Positional against the spec's `bases`, never against the bands that got
 * pushed: a bare metal host's cpu_steal is correctly all-null and is dropped
 * (chartSpecs.ts), and user/system/iowait keep their own colours when it is.
 */
const SWEPT_STATE_HUES = [213, 159, 249, 337];

export const CPU_STATE_COLORS = SWEPT_STATE_HUES.map(
  (h) => `hsl(${h.toFixed(1)}, var(--series-s), var(--series-l))`,
);
