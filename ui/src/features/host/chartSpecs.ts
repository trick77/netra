import type { MetricsResponse } from "../../lib/api";
import {
  carriesColumn,
  counterDeltas,
  griddedValues,
  optionalValues,
  peakBase,
  seriesCells,
  seriesOnGrid,
  seriesTimestamps,
  sumSeries,
} from "../../lib/metrics";
import {
  ABSENT,
  binaryBytes,
  bytes,
  duration,
  percent,
} from "../../lib/format";
import { type Band } from "../../ui/charts/ChartPanel";
import { DOWN_COLOR, UP_COLOR } from "../../ui/charts/UpDownSparkline";
import { filesystemBands, memoryBands, perCoreBands } from "../../lib/bands";

/** See Overview.tsx for why every column lookup on these pages is
 * optional: column() throws during render for a column the answering tier
 * does not carry, and one such throw would blank the whole tab. */
/**
 * A boolean column read as a plottable 0/1 line.
 *
 * seriesValues() refuses a non-numeric cell by design -- it will not
 * conflate "the host reported nothing" with "this column is not a number"
 * -- and collector_samples.ok is exactly that column: a boolean sitting
 * beside a numeric one. The mapping from true/false to 1/0 is a decision
 * about how to DRAW availability, so it is made here, at the panel that
 * draws it, rather than smuggled into the accessor everything else uses.
 */
function booleanValues(
  res: MetricsResponse | null,
  seriesIndex: number,
  base: string,
): (number | null)[] {
  if (res === null || !res.columns.includes(base)) return [];
  if (res.series[seriesIndex] === undefined) return [];
  const mapped = seriesCells(res, seriesIndex, base).map((cell) => {
    if (typeof cell === "boolean") return cell ? 1 : 0;
    if (typeof cell === "number") return cell;
    // A string error_code, or anything else, is not a reading -- and a
    // gap is the honest rendering of "no reading".
    return null;
  });
  // Onto the window's grid, exactly like every numeric band -- this path
  // used to return the mapped cells raw, which is the one failure the grid
  // rule exists to prevent, on the one panel where it matters most. The
  // response carries only the buckets that exist, so a collector that
  // stopped reporting arrives as a SHORTER series rather than as nulls, and
  // the geometry breaks a line only on an explicit null: "this collector
  // was down for three hours" drew an unbroken line at 1, asserting it had
  // been up the whole time. It also left every collector's band its own
  // length, so the bands in this panel were misaligned in time against each
  // other while claiming to share an x axis.
  return seriesOnGrid(res, mapped, seriesTimestamps(res, seriesIndex));
}

// Colours are token references (index.css owns the palette); the series
// index wraps so a family with many devices still never names a hue here.
// Eight, not four. The throughput panel draws two series per interface, so
// four wrapped after the second one and bond0 and enp1s0f1 came out the same
// colour in the same chart.
const SERIES_VARS = [
  "var(--s1)",
  "var(--s2)",
  "var(--s3)",
  "var(--s4)",
  "var(--s5)",
  "var(--s6)",
  "var(--s7)",
  "var(--s8)",
];

type Source =
  | "host"
  | "hostSnmp"
  | "hostProto"
  | "net"
  | "diskIo"
  | "filesystem"
  | "collector"
  | "cpuCore"
  | "agent";

/**
 * The family names the read API takes, as its registry spells them
 * (internal/hub/read/family.go). A name outside this set is a 400.
 *
 * Written as a union rather than `string` because `string` is what let a
 * Source reach the wire unmapped: both fetchers took `(family: string, ...)`,
 * a Source is assignable to string, and `family=cpuCore` type-checked all the
 * way to a rejected request. Now it does not compile.
 *
 * The whole registry, not only the names a panel spec uses: this list is the
 * one statement of what the hub serves, so a caller outside the Graphs specs
 * (Overview's sensor chart, a future smart page) has a name to
 * pass rather than a reason to fall back to `string`. The type is derived
 * from the array so the two cannot drift, and the tests sweep against the
 * same export instead of a hand-copied twin.
 */
export const FAMILIES = [
  "host",
  "host_snmp",
  "host_proto",
  "net",
  "disk_io",
  "filesystem",
  "collector",
  "cpu_core",
  "agent",
  "container",
  "sensor",
  "smart",
] as const;

export type Family = (typeof FAMILIES)[number];

/**
 * A panel's source to the family name the read API takes.
 *
 * Two of the seven differ (diskIo/disk_io, cpuCore/cpu_core), which is
 * exactly why this is a table rather than a cast: the enlarged view fetches
 * by family, and a silently wrong name is a 400 that surfaces as one chart
 * refusing to widen.
 */
const FAMILY: Record<Source, Family> = {
  host: "host",
  hostSnmp: "host_snmp",
  hostProto: "host_proto",
  net: "net",
  diskIo: "disk_io",
  filesystem: "filesystem",
  collector: "collector",
  cpuCore: "cpu_core",
  agent: "agent",
};

interface PanelSpec {
  /**
   * This panel's identity in a URL: /hosts/3/chart/<slug>.
   *
   * Written out rather than derived from `title`. A title is UI copy and
   * changes -- "ingress"/"egress" became "in"/"out" in this very file -- and
   * a link that breaks when a label is reworded is not a link anyone can
   * send. Once a slug has been published it is an API: rename the title
   * freely, never the slug.
   */
  slug: string;
  title: string;
  unit?: string;
  source: Source;
  /** One band per base for a single-series family; one band per base PER
   * SERIES for a keyed one (disk_io, net, filesystem), named by the key so
   * "sda read" and "sdb read" stay distinguishable inside one panel.
   *
   * `source` overrides the SPEC's source for that one band, so a panel can
   * draw columns that live in two tables. The fragmentation panels are why:
   * four of their six counters are on host_samples and reasm_oks/frag_oks are
   * on host_snmp_samples, because a continuous aggregate cannot gain a column
   * and the split is where the schema had to put them. The reader is owed one
   * panel regardless of which table a counter landed in.
   *
   * Honoured for UNKEYED families only (host, hostSnmp, hostProto, agent). A
   * keyed family draws one pass per entry in res.series, and two families'
   * series do not correspond -- disk sda in one response is not disk sda in
   * another's ordering. bandsFor asserts this rather than drawing something
   * plausible and wrong. */
  bases: { base: string; label: string; source?: Source }[];
  max?: number;
  /**
   * A fixed floor, for a non-stacked panel whose scale is the reading.
   *
   * Without it a line chart is scaled from the data's own minimum, which is
   * right for a sensor sitting between 44 and 47 degrees and wrong for
   * filesystem usage: six mounts between 88% and 95% would fill the box top
   * to bottom, drawing "nearly full" and "nearly full" as a dramatic spread
   * -- and disagreeing with the fleet cell, which pins 0-100. A stacked
   * panel is always drawn from zero and ignores this.
   */
  min?: number;
  /**
   * The ladder the y-axis ticks step on. 1024 for a panel formatted in
   * binary bytes: ticked decimally, a 16 GiB host reads 1.9 / 3.7 / 5.6 GiB
   * and every label on the axis is a ragged number. The same choice the
   * host overview's memory card makes.
   */
  tickBase?: 1000 | 1024;
  fmt?: (n: number | null) => string;
  /** Read this family's columns as booleans (1 = true), not as numbers. */
  boolean?: boolean;
  /**
   * Resolve each base to its _max peer at the rolled-up tiers rather than
   * letting column() take the _avg it prefers. For a rate read as a shape --
   * traffic -- the burst IS the reading, and averaging a 60s scrape into a
   * 5m or 1h bucket is what flattened it. See peakBase() in lib/metrics.
   */
  peak?: boolean;
  /**
   * These bases are cumulative totals since boot; draw the per-bucket
   * increase instead of the running sum.
   *
   * A counter plotted as stored is a staircase that only ever climbs, and
   * the reader has to differentiate it by eye to answer the only question
   * it can answer: did this happen inside the window I am looking at. See
   * counterDeltas() in lib/metrics.ts for what a gap and a reset do.
   */
  counter?: boolean;
  /** Draw the bands as a cumulative stack rather than as overlaid lines.
   * Only honest where the bands really do partition something: per-core CPU
   * (each core divided by the core count, so the stack tops out at
   * cpu_total) and the CPU time breakdown (the states sum to busy). */
  stacked?: boolean;
  /** Hide the enlarged view's y axis, for a stack whose height is a shape
   * rather than a quantity. */
  hideAxis?: boolean;
  /** Draw the bases as mirrored pairs about a midline -- in above, out
   * below. Only meaningful when the bases come in twos, in that order. */
  mirrored?: boolean;
  /**
   * Scale the value axis through asinh rather than proportionally.
   *
   * For a quantity with no ceiling and a heavy tail, where the typical
   * reading and the peak are orders of magnitude apart and a proportional
   * axis can only show one of them. Measured on ark.o11.net: 29 kB/s typical
   * against a 101 MB/s peak, and the typical reading drew at four thousandths
   * of a pixel.
   *
   * Declared per spec rather than inferred from `mirrored`. Every mirrored
   * panel here happens to be throughput today, so the two look
   * interchangeable -- but the scale is a statement about a quantity's
   * DISTRIBUTION and the mirror is a statement about its having a direction,
   * and the first mirrored panel of something evenly distributed would
   * silently get an axis bent for traffic. See scale.ts.
   */
  compressed?: boolean;
  /**
   * Fold a keyed family across its series: ONE band per base, summed, rather
   * than one band per base per series.
   *
   * Only honest where the sum is itself the reading. A host's traffic is --
   * "how much is this box moving" is not a question about eth0 -- while the
   * sum of two filesystems' used bytes is a number nobody asked for. It is
   * also a different QUESTION from the per-series panel rather than a
   * tidier version of it, which is why host-traffic and
   * interface-throughput are two specs and not one with a flag.
   */
  summed?: boolean;
  /**
   * Colours for the bases, positional against `bases`, overriding the
   * per-index walk through SERIES_VARS.
   *
   * For a summed spec the index walk is actively wrong: a chart drawn in
   * whatever two hues happened to land at positions 0 and 1 is a third
   * colour scheme for a fact the fleet row and the host overview already
   * draw in a fixed pair. Direction is the reading here, so the colour has
   * to carry direction. A per-series spec must NOT use this -- see
   * SERIES_VARS on why interface-throughput needs eight of them.
   */
  colors?: string[];
  /**
   * Build this panel's bands with a shared helper instead of the base loop.
   *
   * For the charts a fleet row draws, the bands are not "one column per
   * series": they are a normalised per-core stack, a five-band memory
   * partition, a per-filesystem Use% derived from used/(used + free). Those
   * derivations already exist in lib/bands.ts, and every view that draws
   * them calls in there precisely so the fleet cell and the page cannot
   * disagree about the same host. A spec with `bands` says "the chart is
   * whatever that function returns"; `bases` is then what the family lookup
   * and the missing-column message read, and nothing else.
   */
  bands?: (res: MetricsResponse) => Band[];
  /**
   * A ceiling read from the data rather than fixed, drawn as BOTH the scale
   * and a dashed reference rule.
   *
   * Memory is the case it exists for. The stack has to be scaled against
   * mem_total, never against its own running total: auto-scaled, every host
   * draws as nearly full, which is the one reading these charts exist to
   * avoid. And a stack scaled to a ceiling nothing on screen names says how
   * the parts move without saying whether the host is nearly out of memory,
   * so the same number is also the rule.
   */
  ceiling?: (res: MetricsResponse) => number | null;
}

/**
 * Room above a reference rule so it reads as a limit rather than as the top
 * border of the plot.
 *
 * The same 1.08 as MEM_HEADROOM in the fleet's memory cell and the literal
 * in the host overview's memory card, which each still own theirs. This one
 * is what every SPEC-driven view uses -- the Graphs tab's panel, the chart
 * page and its range thumbnails -- so that a panel and the page it links to
 * cannot scale the same host differently, which is the class of bug these
 * specs exist to close.
 */
export const REFERENCE_HEADROOM = 1.08;

/**
 * A ceiling read from the data, or nothing.
 *
 * Zero and negative are absent, not a scale: `?? undefined` alone would let
 * a mem_total of 0 through as a real ceiling, and everything downstream then
 * divides by it -- stackBands scales a running total by `max` -- while the
 * "no ceiling" refusal, which tests only for undefined, would say the view
 * has a scale to draw against.
 */
export function ceilingOf(
  value: number | null | undefined,
): number | undefined {
  return value != null && value > 0 ? value : undefined;
}

/**
 * What a view says instead of drawing a spec whose ceiling is missing.
 *
 * ONE spelling, called by both the Graphs tab's panel and the chart page --
 * the same rule missingReason() follows and for the same reason. The panel
 * and the page it links to are two views of one host, and the moment the
 * sentence exists twice they are free to drift about which hosts they
 * decline to draw and why.
 */
export function noCeilingReason(spec: PanelSpec): string {
  return `This host reported no ${spec.title.toLowerCase()} ceiling in this window, so there is no scale to draw the bands against.`;
}

// The window's last non-null reading. mem_total is a constant for the life of
// a boot, so any bucket carrying it answers "how much memory does this host
// have" -- but the LAST bucket is routinely null (the tier materialises
// behind now), and a null ceiling would drop the memory chart back to the
// always-full auto-scale.
function lastKnown(values: (number | null)[]): number | null {
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i];
    if (v !== null && v !== undefined) return v;
  }
  return null;
}

// A count or a rate has no unit prefix worth inventing, so it is printed
// as-is, rounded -- but never as "0" when it is absent.
function count(n: number | null): string {
  return n === null ? ABSENT : String(Math.round(n * 100) / 100);
}

export const SYSTEM: PanelSpec[] = [
  // The fleet row's CPU cell, on a page.
  //
  // Normalised, like the cell and unlike "CPU cores" below: each core is
  // divided by the core count, so the top of the stack is cpu_total and 100
  // is a real ceiling. That is what makes a 4-core and a 32-core host
  // comparable in one column, and redrawing the click unnormalised against a
  // 0-3200 axis would change the very shape the reader pointed at. The
  // unnormalised view answers a different question and keeps its own panel.
  //
  // On a host with more than MAX_PER_CORE threads the cell and the page do
  // differ, deliberately: the fleet row will not fetch 128 series to draw a
  // 170px chart, so it falls back to a single cpu_total band, while this page
  // has the room and draws all 128. The SILHOUETTE is identical either way --
  // a normalised per-core stack sums to cpu_total, which is exactly what that
  // fallback band is -- so the shape the reader clicked is the shape they
  // land on, with the cores it was made of underneath it. That is the page
  // showing more, not the page showing something else.
  {
    title: "CPU",
    slug: "host-cpu",
    unit: "%",
    source: "cpuCore",
    bases: [{ base: "busy", label: "busy" }],
    bands: (res) => perCoreBands(res, { normalise: true }),
    max: 100,
    stacked: true,
    fmt: (n) => (n === null ? ABSENT : `${count(n)}%`),
  },
  // The fleet row's Memory cell, on a page. Five bands against mem_total,
  // with the total as a dashed rule -- see PanelSpec.ceiling for why that
  // rule is not decoration.
  //
  // Binary bytes, like the cell: the bands are read against a rule labelled
  // in GiB, and a stack labelled decimally under it makes one quantity look
  // like two.
  {
    title: "Memory",
    slug: "host-memory",
    source: "host",
    bases: [
      { base: "mem_used", label: "used" },
      { base: "mem_free", label: "free" },
    ],
    bands: memoryBands,
    ceiling: (res) => lastKnown(griddedValues(res, 0, "mem_total")),
    stacked: true,
    fmt: (n) => binaryBytes(n),
    tickBase: 1024,
  },
  // One band per logical CPU, each divided by the core count so the top of
  // the stack is the mean -- cpu_total. Unnormalised, 32 cores at 50% would
  // stack to 1600 against a ceiling of 100.
  {
    title: "CPU cores",
    slug: "cpu-cores",
    source: "cpuCore",
    bases: [{ base: "busy", label: "busy" }],
    stacked: true,
    // No ceiling and no axis: each band is one core's real utilisation, so
    // the stack runs to cores x 100. Every number a reader sees is the
    // number that core reported, which is the point of this panel.
    hideAxis: true,
    fmt: (n) => (n === null ? ABSENT : `${count(n)}%`),
  },
  // The states partition busy time, so they stack honestly. They used to
  // exist only in the raw table and vanish above an hour; they reach the 5m
  // and 1h rollups now.
  {
    title: "CPU time breakdown",
    slug: "cpu-time-breakdown",
    source: "host",
    bases: [
      { base: "cpu_user", label: "user" },
      { base: "cpu_system", label: "system" },
      { base: "cpu_iowait", label: "iowait" },
      { base: "cpu_steal", label: "steal" },
    ],
    max: 100,
    stacked: true,
    fmt: (n) => (n === null ? ABSENT : `${count(n)}%`),
  },
  // What the machine had to DO to keep memory available, which the memory
  // charts cannot show: a host sitting at a comfortable 40% used can be
  // thrashing, because the pages it needed were evicted and fetched again.
  // Major faults are the reads that had to hit disk; the two swap rates are
  // the kernel paging to keep going.
  //
  // Three rates, one axis, honestly: all three are events per second, and
  // they are read together -- swap-out climbing with major faults flat is
  // reclaim doing its job, both climbing together is thrash.
  //
  // Not stacked. They overlap by nature (a major fault can BE a swap-in),
  // so a stack would assert a partition that does not exist.
  {
    title: "Memory pressure",
    slug: "memory-pressure",
    unit: "/s",
    source: "host",
    bases: [
      { base: "pgmajfault_per_s", label: "major faults" },
      { base: "pswpin_per_s", label: "swap in" },
      { base: "pswpout_per_s", label: "swap out" },
    ],
    fmt: count,
  },
  // "Device availability" in the spec's list; collector_samples.ok is the
  // only availability signal the schema actually holds, so that is what
  // this draws -- one band per collector, 1 when it ran, 0 when it did not.
  {
    title: "Device availability",
    slug: "device-availability",
    source: "collector",
    bases: [{ base: "ok", label: "ok" }],
    boolean: true,
    max: 1,
    fmt: (n) => (n === null ? ABSENT : n >= 1 ? "up" : "down"),
  },
  {
    title: "Uptime",
    slug: "uptime",
    source: "host",
    bases: [{ base: "uptime_s", label: "uptime" }],
    fmt: duration,
  },
  {
    title: "Load averages",
    slug: "load-averages",
    source: "host",
    bases: [
      { base: "load1", label: "1m" },
      { base: "load5", label: "5m" },
      { base: "load15", label: "15m" },
    ],
    fmt: count,
  },
  {
    title: "Context switches",
    slug: "context-switches",
    unit: "/s",
    source: "host",
    bases: [{ base: "ctxt_per_s", label: "ctxt" }],
    fmt: count,
  },
  {
    title: "Interrupts",
    slug: "interrupts",
    unit: "/s",
    source: "host",
    bases: [{ base: "intr_per_s", label: "intr" }],
    fmt: count,
  },
  {
    title: "Running processes",
    slug: "running-processes",
    source: "host",
    bases: [
      { base: "procs_running", label: "running" },
      { base: "procs_blocked", label: "blocked" },
    ],
    fmt: count,
  },
  {
    title: "Users logged in",
    slug: "users-logged-in",
    source: "host",
    bases: [{ base: "users_logged_in", label: "users" }],
    fmt: count,
  },
  {
    title: "Total processes",
    slug: "total-processes",
    source: "host",
    bases: [{ base: "processes_total", label: "processes" }],
    fmt: count,
  },
];

export const NETWORK: PanelSpec[] = [
  // The gap between the two lines is the hub, not the network.
  //
  // hub_connect stops at SYN-ACK, so it is the path. post_latency is the
  // whole round trip -- TLS, upload, the hub's handling, the Postgres write
  // -- so a slow database lifts it while the handshake stays flat. Drawn
  // together because neither answers "where is the time going" alone.
  {
    title: "Hub latency",
    slug: "hub-latency",
    unit: "ms",
    source: "agent",
    bases: [
      { base: "hub_connect_us", label: "handshake" },
      { base: "hub_connect_max_us", label: "handshake peak" },
      { base: "post_latency_ms", label: "round trip" },
    ],
    fmt: count,
  },
  // The panel above goes blank during the exact event worth seeing.
  //
  // hub_connect_us and hub_connect_max_us are the duration of a handshake
  // that COMPLETED; when the hub is unreachable there is no duration to
  // report, so both are NULL by design and Hub latency correctly draws a
  // gap. Nothing there says why. This is the counter that carries the
  // outage, and it belongs beside that gap rather than inside it -- it is
  // a count of events, not a time, and hanging it off an axis labelled ms
  // would put "3" and "42 ms" on one scale.
  {
    title: "Hub connect failures",
    slug: "hub-connect-failures",
    source: "agent",
    bases: [{ base: "hub_connect_failures_total", label: "failures" }],
    counter: true,
    fmt: count,
  },
  // The fleet row's traffic cell and the host overview's Traffic card, on a
  // page. Both draw every interface summed into one in/out pair, which is a
  // different chart from the per-interface panel below -- so this is a
  // different spec, and the cell links HERE. Pointing it at
  // interface-throughput would have swapped one chart for another on click,
  // which is the one thing an enlarged view must not do.
  {
    title: "Traffic",
    slug: "host-traffic",
    unit: "B/s",
    source: "net",
    bases: [
      { base: "rx_bytes", label: "in" },
      { base: "tx_bytes", label: "out" },
    ],
    // The same green-above / purple-below the sparkline draws, pinned rather
    // than taken from the index walk. See PanelSpec.colors.
    colors: [UP_COLOR, DOWN_COLOR],
    mirrored: true,
    compressed: true,
    peak: true,
    summed: true,
    fmt: bytes,
  },
  {
    // "in" and "out", not rx and tx: the direction is the point of this
    // chart, and "rx" is the kernel's word for it rather than the reader's.
    // (It read "ingress"/"egress" until an operator pointed out that an
    // interface has an in and an out; the schema and the wire are unchanged.)
    title: "Interface throughput",
    slug: "interface-throughput",
    unit: "B/s",
    source: "net",
    bases: [
      { base: "rx_bytes", label: "in" },
      { base: "tx_bytes", label: "out" },
    ],
    // Mirrored about a midline, the way the fleet row has always drawn
    // traffic: two lines climbing one axis make a reader compare shapes to
    // answer "which way is this going".
    mirrored: true,
    compressed: true,
    // Read the bucket's peak, not its mean -- see peakBase(). Unlike the
    // fleet cell this panel draws one pair per interface and sums nothing,
    // so the mark here is a true per-interface maximum.
    peak: true,
    fmt: bytes,
  },
  {
    title: "TCP statistics",
    slug: "tcp-statistics",
    unit: "/s",
    source: "host",
    bases: [
      { base: "tcp_retrans_segs_per_s", label: "retransmits" },
      { base: "tcp_in_errs_per_s", label: "in errors" },
      { base: "tcp_out_rsts_per_s", label: "resets out" },
    ],
    fmt: count,
  },
  {
    title: "TCP connections",
    slug: "tcp-connections",
    source: "host",
    bases: [
      { base: "tcp_curr_estab", label: "established" },
      { base: "tcp_active_opens_per_s", label: "active opens" },
      { base: "tcp_passive_opens_per_s", label: "passive opens" },
      { base: "tcp_attempt_fails_per_s", label: "attempt fails" },
    ],
    fmt: count,
  },
  // Two panels of six, where there was one panel of four.
  //
  // The four were the FAILS, v4 and v6 mixed, and both halves of that were
  // wrong. Fragmentation failures are zero on a healthy host, so the panel
  // drew a flat line at zero and told a reader nothing -- while frag creates
  // runs in the tens per second on the same host and is the shape that says
  // what the stack is actually doing. And mixing the families put two
  // unrelated readings on one axis: a host can fragment heavily on v4 and not
  // at all on v6, and one panel cannot say so.
  //
  // This is the deliberate exception to the "four bands, resist completing
  // these" rule below, and it is an exception because a reader asked for it
  // with a working example in hand. It is not licence to widen the others.
  //
  // reasm ok and frag ok come from hostSnmp: the schema put the success
  // counters in host_snmp_samples and the rest in host_samples, because a
  // continuous aggregate cannot gain a column and that is where each landed
  // when it was added. Which table a counter is in is not a fact about
  // fragmentation, so the panel spans both -- see PanelSpec.bases.
  {
    title: "IP fragmentation",
    slug: "ip-fragmentation",
    unit: "/s",
    source: "host",
    bases: [
      { base: "ip_reasm_reqds_per_s", label: "reasm reqd" },
      { base: "ip_reasm_oks_per_s", label: "reasm ok", source: "hostSnmp" },
      { base: "ip_reasm_fails_per_s", label: "reasm fail" },
      { base: "ip_frag_oks_per_s", label: "frag ok", source: "hostSnmp" },
      { base: "ip_frag_fails_per_s", label: "frag fail" },
      { base: "ip_frag_creates_per_s", label: "frag create" },
    ],
    fmt: count,
  },
  {
    title: "IPv6 fragmentation",
    slug: "ip6-fragmentation",
    unit: "/s",
    source: "host",
    bases: [
      { base: "ip6_reasm_reqds_per_s", label: "reasm reqd" },
      { base: "ip6_reasm_oks_per_s", label: "reasm ok", source: "hostSnmp" },
      { base: "ip6_reasm_fails_per_s", label: "reasm fail" },
      { base: "ip6_frag_oks_per_s", label: "frag ok", source: "hostSnmp" },
      { base: "ip6_frag_fails_per_s", label: "frag fail" },
      { base: "ip6_frag_creates_per_s", label: "frag create" },
    ],
    fmt: count,
  },
  // The three panels that used to render as "not collected". host_snmp is a
  // second host-level family rather than more columns on "host" -- see
  // 0003_host_snmp_samples.sql for why that split had to happen.
  //
  // Four bands each, out of seventy stored columns. The rest are queryable
  // and deliberately undrawn: a continuous aggregate cannot gain a column, so
  // storing a counter now is cheap and adding one later is not, whereas a
  // twenty-band panel is simply unreadable. Resist "completing" these.
  {
    title: "IP statistics",
    slug: "ip-statistics",
    unit: "/s",
    source: "hostSnmp",
    bases: [
      { base: "ip_in_receives_per_s", label: "received" },
      { base: "ip_out_requests_per_s", label: "sent" },
      { base: "ip6_in_receives_per_s", label: "received (v6)" },
      { base: "ip6_out_requests_per_s", label: "sent (v6)" },
    ],
    fmt: count,
  },
  {
    title: "ICMP statistics",
    slug: "icmp-statistics",
    unit: "/s",
    source: "hostSnmp",
    bases: [
      { base: "icmp_in_errors_per_s", label: "in errors" },
      { base: "icmp_out_errors_per_s", label: "out errors" },
      { base: "icmp_in_dest_unreachs_per_s", label: "dest unreachable" },
      { base: "icmp6_in_errors_per_s", label: "in errors (v6)" },
    ],
    fmt: count,
  },
  {
    // Echo is reachability, not failure: a host answering pings is healthy,
    // and charting it beside dest-unreachable would put a good signal on a
    // failure axis.
    title: "ICMP informational",
    slug: "icmp-informational",
    unit: "/s",
    source: "hostSnmp",
    bases: [
      { base: "icmp_in_echos_per_s", label: "echos in" },
      { base: "icmp_out_echo_reps_per_s", label: "echo replies out" },
      { base: "icmp6_in_echos_per_s", label: "echos in (v6)" },
      { base: "icmp6_out_echo_replies_per_s", label: "echo replies out (v6)" },
    ],
    fmt: count,
  },
  {
    title: "UDP statistics",
    slug: "udp-statistics",
    unit: "/s",
    source: "host",
    bases: [
      { base: "udp_in_errors_per_s", label: "in errors" },
      { base: "udp_rcvbuf_errors_per_s", label: "rcvbuf errors" },
      { base: "udp_no_ports_per_s", label: "no ports" },
      { base: "udp6_in_errors_per_s", label: "in errors (v6)" },
    ],
    fmt: count,
  },
  // The denominator the panel above never had. UDP was error-only: four
  // counters of things going wrong and no measure of how much was going
  // right, so 0.5 rcvbuf errors a second read the same on a host doing
  // nothing and one doing 600 datagrams a second.
  {
    title: "UDP datagrams",
    slug: "udp-datagrams",
    unit: "/s",
    source: "hostProto",
    bases: [
      { base: "udp_in_datagrams_per_s", label: "in" },
      { base: "udp_out_datagrams_per_s", label: "out" },
      { base: "udp6_in_datagrams_per_s", label: "in (v6)" },
      { base: "udp6_out_datagrams_per_s", label: "out (v6)" },
    ],
    fmt: count,
  },
  // And the same argument for TCP: "TCP statistics" draws retransmits against
  // nothing, and a retransmit rate is only readable as a fraction of the
  // segments carrying it.
  //
  // estab resets rides here rather than with the error panel because it is
  // the same MIB block and the same table -- and because a reset is a
  // connection ending, which is a fact about volume as much as about failure.
  //
  // No v6 peers: Linux's Tcp: block counts both families together, and
  // /proc/net/snmp6 has no Tcp6 block at all.
  {
    title: "TCP segments",
    slug: "tcp-segments",
    unit: "/s",
    source: "hostProto",
    bases: [
      { base: "tcp_in_segs_per_s", label: "in" },
      { base: "tcp_out_segs_per_s", label: "out" },
      { base: "tcp_estab_resets_per_s", label: "estab resets" },
    ],
    fmt: count,
  },
  // Time spent reading /proc, beside the two panels measuring time spent
  // reaching the hub. Together they answer "the agent is slow -- is that this
  // box or the network", which neither answers alone.
  //
  // The column has been written and rolled up since the schema was first
  // laid down; nothing had ever read it back.
  {
    title: "Scrape duration",
    slug: "scrape-duration",
    unit: "ms",
    source: "agent",
    bases: [{ base: "scrape_duration_ms", label: "scrape" }],
    fmt: count,
  },
];

export const STORAGE: PanelSpec[] = [
  // The fleet row's Filesystem cell, on a page: every mount's Use% over the
  // window, one line each.
  //
  // Lines, not a stack: the mounts partition nothing, and stacking six of
  // them would draw a host at 400% full. Fixed 0-100 for the same reason the
  // cell has one -- self-scaled, a disk at 40% and one at 95% draw the same
  // silhouette.
  {
    title: "Filesystem usage",
    slug: "host-filesystem",
    source: "filesystem",
    bases: [
      { base: "used", label: "used" },
      { base: "free", label: "free" },
    ],
    bands: filesystemBands,
    // Both ends fixed, like the cell (DiskTrendCell passes min={0} max={100}).
    // The ceiling alone is not enough: a non-stacked panel is otherwise
    // scaled from the data's own floor, so six mounts between 88% and 95%
    // would be spread across the whole plot -- the exact self-scaling this
    // chart exists to avoid, and a different picture from the sparkline the
    // reader clicked.
    min: 0,
    max: 100,
    fmt: (n) => percent(n),
  },
  {
    title: "Disk throughput",
    slug: "disk-throughput",
    unit: "B/s",
    source: "diskIo",
    bases: [
      { base: "read_bytes", label: "read" },
      { base: "write_bytes", label: "write" },
    ],
    fmt: bytes,
  },
  {
    // await is how a busy disk is told apart from a failing one (spec §10),
    // which is why it gets a panel of its own rather than a legend entry
    // on utilisation.
    title: "Disk latency",
    slug: "disk-latency",
    unit: "ms",
    source: "diskIo",
    bases: [
      { base: "r_await_ms", label: "read await" },
      { base: "w_await_ms", label: "write await" },
    ],
    fmt: count,
  },
  {
    // No unit: percent() prints one already, and passing both rendered
    // "12 % %". See ChartPanel's unit prop.
    title: "Disk utilisation",
    slug: "disk-utilisation",
    source: "diskIo",
    bases: [{ base: "io_util_pct", label: "utilisation" }],
    max: 100,
    fmt: (n) => percent(n),
  },
  {
    title: "Filesystem space",
    slug: "filesystem-space",
    unit: "B",
    source: "filesystem",
    bases: [
      { base: "used", label: "used" },
      { base: "free", label: "free" },
    ],
    fmt: bytes,
  },
  {
    // Inode exhaustion presents as "disk full" with free space showing,
    // and nothing else in this UI would explain it (spec §10).
    title: "Filesystem inodes",
    slug: "filesystem-inodes",
    source: "filesystem",
    bases: [
      { base: "inodes_used", label: "used" },
      { base: "inodes_total", label: "total" },
    ],
    fmt: count,
  },
];
export interface BandOptions {
  /**
   * Draw the bucket's MEAN as the line and its PEAK as a pale envelope under
   * it, instead of drawing the peak alone.
   *
   * The rollups materialise both (0001_init.sql) and a chart has only ever
   * shown one. A peak-only line answers "did it burst" and hides where the
   * link actually sits; a mean-only line loses the burst entirely, which is
   * the failure peakBase() exists to prevent. Together they answer both, and
   * there is room for the pair on a page where there is not on a 260px
   * panel.
   *
   * Ignored at the raw tier, where the sample IS its own peak and there is
   * no _max column to ask for -- there the envelope is correctly absent
   * rather than a duplicate of the line.
   */
  withPeakBand?: boolean;

  /**
   * The responses for the OTHER families this spec's bases name, keyed by
   * source. `res` still carries the spec's own.
   *
   * Only the sources a base actually overrides need an entry; anything else
   * here is ignored. A base whose response is missing or null is dropped
   * rather than drawn empty, which is the same treatment a base whose column
   * this tier does not carry already gets.
   */
  extra?: Partial<Record<Source, MetricsResponse | null>>;
}

function bandsFor(
  spec: PanelSpec,
  res: MetricsResponse | null,
  opts: BandOptions = {},
): Band[] {
  if (res === null) return [];
  // A spec that names its own builder IS that builder's output -- no base
  // loop, no peak envelope, no colour walk. The point of these specs is that
  // the page draws exactly what the fleet cell draws, and the way to
  // guarantee that is to call the same function rather than to reproduce it
  // here and keep the two in step by hand.
  if (spec.bands) return spec.bands(res);
  const keyed = res.key_columns.length > 0;
  const bands: Band[] = [];

  /**
   * The passes this spec draws: one per series normally, exactly ONE for a
   * summed spec.
   *
   * A summed spec folds the family before anything is drawn, so it has no
   * key to prefix a band with -- "in" here is the host's in, not eth0's,
   * and labelling it with an interface would be a lie about what was
   * summed. sumSeries grids every series the same way griddedValues does
   * for one, so both passes hand the loop below the same shape.
   *
   * No boolean branch on the summed side: adding 1s and 0s across series
   * produces a count, not a state, and no summed spec is boolean. The
   * `boolean` flag and `summed` are mutually exclusive by that argument
   * rather than by a type -- if a third flag ever needs the same guard,
   * that is the moment to make it one.
   */
  const passes: {
    prefix: string;
    read: (column: string) => (number | null)[];
  }[] = spec.summed
    ? [{ prefix: "", read: (column) => sumSeries(res, column) }]
    : res.series.map((series, index) => ({
        // A keyed family names its bands by the key it is keyed on, so two
        // devices in one panel stay tellable apart by their label, not by
        // colour alone.
        prefix: keyed
          ? res.key_columns
              .map((k) => series.key[k])
              .filter(Boolean)
              .join(" ")
          : "",
        read: (column) =>
          spec.boolean
            ? booleanValues(res, index, column)
            : griddedValues(res, index, column),
      }));

  /**
   * The response a base reads from, and how to read it.
   *
   * Returns null for a base whose family was not handed over, which drops the
   * band rather than drawing it empty.
   *
   * The other family's values are gridded onto the PRIMARY response's window
   * and step, not their own. Both families answer the same requested range so
   * they normally agree -- but "normally" is not "always": a family whose
   * oldest sample is younger can be served a different tier, and two bands of
   * different lengths in one chart are spread across their own lengths and so
   * misaligned in time against each other. Gridding both against one window
   * makes that impossible rather than unlikely.
   */
  const foreign = (
    source: Source,
  ): {
    res: MetricsResponse;
    read: (column: string) => (number | null)[];
  } | null => {
    const other = opts.extra?.[source] ?? null;
    if (other === null) return null;
    if (other.key_columns.length > 0) {
      // A keyed family has one pass per series and no way to say which of
      // this response's series corresponds to which of the primary's. Refusing
      // is the point: the alternative is a plausible-looking chart pairing
      // sda's reads with sdb's writes.
      return null;
    }
    return {
      res: other,
      read: (column) =>
        seriesOnGrid(
          res,
          optionalValues(other, 0, column),
          seriesTimestamps(other, 0),
        ),
    };
  };

  passes.forEach(({ prefix, read }) => {
    for (const [baseIndex, { base, label, source }] of spec.bases.entries()) {
      // A base naming its own source reads from that family's response; every
      // other base is unaffected and reads exactly as it did before.
      let bandRes = res;
      let bandRead = read;
      if (source !== undefined && source !== spec.source) {
        const other = foreign(source);
        if (other === null) continue;
        bandRes = other.res;
        bandRead = other.read;
      }
      // griddedValues, not optionalValues: the response carries only the
      // buckets that exist, so an outage arrives as a SHORTER series rather
      // than as nulls, and the geometry breaks a line only on a null. Drawn
      // straight from the response, three hours of a host being down became
      // one unbroken line across the hole.
      // Resolved per response, not per tier constant: the raw table has no
      // _max peer, and there peakBase() falls back to the base name.
      const peakColumn = spec.peak ? peakBase(bandRes, base) : base;
      // With the envelope, the LINE is the mean and the band is the peak.
      // peakBase falls back to the bare name at the raw tier, and there the
      // two resolve to the same column -- which is the signal that this tier
      // has no envelope to draw.
      // Mirrored only, for now, and deliberately: Chart draws the envelope in
      // its mirror branch alone. Without this guard the first non-mirrored
      // `peak` spec added would silently switch its LINE to the mean and
      // then draw no envelope at all -- strictly less than the 260px panel
      // it was opened from. Widen this when LineMarks learns the band.
      const wantsBand =
        opts.withPeakBand === true &&
        spec.peak === true &&
        spec.mirrored === true &&
        peakColumn !== base;
      const column = wantsBand ? base : peakColumn;
      const gridded = bandRead(column);
      const band = wantsBand ? bandRead(peakColumn) : undefined;
      // After the grid, never before: counterDeltas subtracts NEIGHBOURING
      // buckets, so it has to run on the window's own even spacing. Applied
      // to the raw response -- which omits the buckets a host did not
      // report -- it would subtract across a three-hour hole and attribute
      // every failure in it to the single bucket where reporting resumed.
      const values = spec.counter ? counterDeltas(gridded) : gridded;
      if (values.length === 0) continue;
      // A band with no readings at all is dropped rather than drawn empty.
      // On a STACKED panel this is not cosmetic: stackBands() breaks every
      // band at any index where any series is null, so one all-null series
      // blanks the entire chart -- which is what a bare metal host's
      // cpu_steal (correctly NULL, there is no hypervisor) did to the CPU
      // time breakdown.
      if (spec.stacked && values.every((v) => v === null)) continue;
      bands.push({
        name: prefix ? `${prefix} ${label}` : label,
        // Indexed by BASE when the spec pins its colours, not by how many
        // bands happen to have been pushed: a spec that names its hues is
        // saying "in is green", and reading the running count instead would
        // hand the second interface of a keyed spec the wrong end of the
        // pair.
        color:
          spec.colors?.[baseIndex] ??
          SERIES_VARS[bands.length % SERIES_VARS.length],
        values,
        ...(band && band.length > 0 ? { band } : {}),
      });
    }
  });

  return bands;
}

// Names the columns this tier is missing, because "not collected" without a
// reason is indistinguishable from a bug -- and the usual reason here is
// recoverable by the reader: most per-state columns exist only at full
// resolution, so a shorter range brings them back.
export function missingReason(
  spec: PanelSpec,
  res: MetricsResponse,
  extra: Partial<Record<Source, MetricsResponse | null>> = {},
): string {
  // Each base against ITS OWN response, not all of them against the spec's:
  // a cross-source panel whose foreign family answered a rolled-up tier would
  // otherwise name a column as missing from a table that never held it, and
  // send the reader to shorten a range that was never the problem.
  const missing = spec.bases
    .filter(({ base, source }) => {
      const from =
        source === undefined || source === spec.source
          ? res
          : (extra[source] ?? null);
      return from === null || !carriesColumn(from, base);
    })
    .map((b) => b.base);
  if (missing.length === 0) {
    return `The host reported no ${spec.title.toLowerCase()} samples in this window.`;
  }
  return `${missing.join(", ")} ${missing.length === 1 ? "is" : "are"} not stored at the ${res.tier} resolution. Choose a shorter range to see this panel.`;
}

/**
 * Every panel, in one list.
 *
 * The chart page resolves a URL slug against this, so the page and the tab
 * cannot disagree about what a chart IS -- same spec, same bases, same
 * formatter, same mark.
 */
export const ALL_SPECS: readonly PanelSpec[] = [
  ...SYSTEM,
  ...NETWORK,
  ...STORAGE,
];

/** A named run of panels under one .grouphead. */
export interface PanelGroup {
  title: string;
  specs: PanelSpec[];
}

/**
 * The groups, by slug.
 *
 * There is no "Graphs" tab any more, and that is the point of this block.
 * "Graphs" named a RENDERING FORMAT rather than a subject, and it split every
 * subject in two: the Network tab held the address table while every network
 * chart sat in Graphs, and Filesystems held the mount table while the disk
 * charts sat in Graphs. A reader asking "what is this box's network doing"
 * had to visit two tabs and know which half was where. Each subject tab now
 * carries its own charts.
 *
 * Written as slugs rather than by moving the spec objects into groups: the
 * specs above are long and heavily commented, and a reshuffle that moved them
 * bodily would bury the actual change. resolveGroups throws on a slug that
 * names no spec, and specsAreFullyGrouped() below is the other half -- a spec
 * defined and then left out of every group renders nowhere at all, which is
 * invisible without a check.
 *
 * The network groups run bottom-up the stack -- interface, IP, then the
 * transports above it, then ICMP beside them. UDP and IP were briefly one
 * group on the grounds that it balanced the panel counts, which is not an
 * argument: fragmentation and reassembly happen at IP and UDP sits on top.
 */
const GROUP_SLUGS: Record<string, { title: string; slugs: string[] }[]> = {
  system: [
    {
      title: "Resources",
      slugs: [
        "host-cpu",
        "host-memory",
        "cpu-cores",
        "cpu-time-breakdown",
        "memory-pressure",
        "uptime",
      ],
    },
    {
      title: "Kernel",
      slugs: [
        "load-averages",
        "context-switches",
        "interrupts",
        "running-processes",
        "total-processes",
        "users-logged-in",
      ],
    },
    // The agent's own health sits under System because it is a fact about
    // this box, not about netra's networking -- and because three panels do
    // not earn a tab.
    {
      title: "Agent",
      slugs: [
        "hub-latency",
        "hub-connect-failures",
        "scrape-duration",
        "device-availability",
      ],
    },
  ],
  network: [
    { title: "Traffic", slugs: ["host-traffic", "interface-throughput"] },
    {
      title: "IP",
      slugs: ["ip-statistics", "ip-fragmentation", "ip6-fragmentation"],
    },
    {
      title: "TCP",
      slugs: ["tcp-statistics", "tcp-connections", "tcp-segments"],
    },
    { title: "UDP", slugs: ["udp-statistics", "udp-datagrams"] },
    { title: "ICMP", slugs: ["icmp-statistics", "icmp-informational"] },
  ],
  storage: [
    {
      title: "Filesystems",
      slugs: ["host-filesystem", "filesystem-space", "filesystem-inodes"],
    },
    {
      title: "Disks",
      slugs: ["disk-throughput", "disk-latency", "disk-utilisation"],
    },
  ],
};

function resolveGroups(key: string): PanelGroup[] {
  return GROUP_SLUGS[key].map(({ title, slugs }) => ({
    title,
    specs: slugs.map((slug) => {
      const spec = specForSlug(slug);
      if (spec === undefined) {
        // At module load, not at render: a mistyped slug that only failed on
        // the tab it belongs to would ship.
        throw new Error(`chartSpecs: no panel with slug "${slug}"`);
      }
      return spec;
    }),
  }));
}

export const SYSTEM_GROUPS: PanelGroup[] = resolveGroups("system");
export const NETWORK_GROUPS: PanelGroup[] = resolveGroups("network");
export const STORAGE_GROUPS: PanelGroup[] = resolveGroups("storage");

/**
 * Every slug that appears in some group, for the test that pairs with
 * resolveGroups: that one catches a group naming a spec that does not exist,
 * this one catches a spec that exists and is in no group.
 */
export function groupedSlugs(): string[] {
  return [...SYSTEM_GROUPS, ...NETWORK_GROUPS, ...STORAGE_GROUPS].flatMap((g) =>
    g.specs.map((s) => s.slug),
  );
}

export function specForSlug(slug: string): PanelSpec | undefined {
  return ALL_SPECS.find((spec) => spec.slug === slug);
}

/**
 * The wire family a spec's data comes from. The one way to ask -- every
 * fetcher goes through here rather than reading spec.source, which is a UI
 * key and not a family name.
 */
export function familyFor(spec: PanelSpec): Family {
  return FAMILY[spec.source];
}

/**
 * Every source a spec reads from: its own, plus whatever its bases override.
 *
 * The list the enlarged view fetches. familyFor() alone is the spec's own
 * family, and a dialog that fetched only that would widen a cross-source
 * panel by dropping its foreign bands -- strictly less than the 260px panel
 * it was opened from.
 *
 * Deduplicated and primary-first, so a caller can rely on [0] being the
 * spec's own.
 */
export function sourcesFor(spec: PanelSpec): Source[] {
  const out: Source[] = [spec.source];
  for (const { source } of spec.bases) {
    if (source !== undefined && !out.includes(source)) out.push(source);
  }
  return out;
}

/** The same list as wire family names. */
export function familiesFor(spec: PanelSpec): Family[] {
  return sourcesFor(spec).map((s) => FAMILY[s]);
}

export { bandsFor };
export type { PanelSpec };
