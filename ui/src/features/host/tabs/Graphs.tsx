// The Graphs tab: small multiples. ONE ChartPanel component, N instances,
// uniform size (nothing here overrides its width/height), one range
// control -- the header's -- driving all of them. Adding a family later is
// a row in PANELS, not a new component.
import type { MetricsResponse } from "../../../lib/api";
import type { Range } from "../../../lib/range";
import {
  carriesColumn,
  counterDeltas,
  griddedValues,
  seriesCells,
  seriesOnGrid,
  seriesTimestamps,
  windowNotice,
} from "../../../lib/metrics";
import { ABSENT, bytes, duration, percent } from "../../../lib/format";
import { ChartPanel, type Band } from "../../../ui/charts/ChartPanel";
import { RANGE_VALUES } from "../ranges";

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

/**
 * The three requested families with no data behind them (spec §11). They
 * render as explicit "not collected" panels carrying the reason, never as
 * an empty chart: an empty chart claims the host reported nothing, which
 * is a different and much more alarming fact than "netra never asked".
 */
export const UNAVAILABLE: Record<string, string> = {
  "IP statistics":
    "only fragmentation and reassembly are parsed from /proc/net/snmp; InReceives, InDelivers and OutRequests reach neither the wire nor the schema",
  "ICMP statistics":
    "no ICMP columns exist in 0001_init.sql; the Icmp rows of /proc/net/snmp are never parsed",
  "ICMP informational":
    "the same gap as ICMP statistics — no ICMP columns exist in the schema",
};

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
  "host" | "net" | "diskIo" | "filesystem" | "collector" | "cpuCore" | "agent";

/**
 * A panel's source to the family name the read API takes.
 *
 * Two of the seven differ (diskIo/disk_io, cpuCore/cpu_core), which is
 * exactly why this is a table rather than a cast: the enlarged view fetches
 * by family, and a silently wrong name is a 400 that surfaces as one chart
 * refusing to widen.
 */
const FAMILY: Record<Source, string> = {
  host: "host",
  net: "net",
  diskIo: "disk_io",
  filesystem: "filesystem",
  collector: "collector",
  cpuCore: "cpu_core",
  agent: "agent",
};

interface PanelSpec {
  title: string;
  unit?: string;
  source: Source;
  /** One band per base for a single-series family; one band per base PER
   * SERIES for a keyed one (disk_io, net, filesystem), named by the key so
   * "sda read" and "sdb read" stay distinguishable inside one panel. */
  bases: { base: string; label: string }[];
  max?: number;
  fmt?: (n: number | null) => string;
  /** Read this family's columns as booleans (1 = true), not as numbers. */
  boolean?: boolean;
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
}

// A count or a rate has no unit prefix worth inventing, so it is printed
// as-is, rounded -- but never as "0" when it is absent.
function count(n: number | null): string {
  return n === null ? ABSENT : String(Math.round(n * 100) / 100);
}

const SYSTEM: PanelSpec[] = [
  // One band per logical CPU, each divided by the core count so the top of
  // the stack is the mean -- cpu_total. Unnormalised, 32 cores at 50% would
  // stack to 1600 against a ceiling of 100.
  {
    title: "CPU cores",
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
    source: "collector",
    bases: [{ base: "ok", label: "ok" }],
    boolean: true,
    max: 1,
    fmt: (n) => (n === null ? ABSENT : n >= 1 ? "up" : "down"),
  },
  {
    title: "Uptime",
    source: "host",
    bases: [{ base: "uptime_s", label: "uptime" }],
    fmt: duration,
  },
  {
    title: "Load averages",
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
    unit: "/s",
    source: "host",
    bases: [{ base: "ctxt_per_s", label: "ctxt" }],
    fmt: count,
  },
  {
    title: "Interrupts",
    unit: "/s",
    source: "host",
    bases: [{ base: "intr_per_s", label: "intr" }],
    fmt: count,
  },
  {
    title: "Running processes",
    source: "host",
    bases: [
      { base: "procs_running", label: "running" },
      { base: "procs_blocked", label: "blocked" },
    ],
    fmt: count,
  },
  {
    title: "Users logged in",
    source: "host",
    bases: [{ base: "users_logged_in", label: "users" }],
    fmt: count,
  },
  {
    title: "Total processes",
    source: "host",
    bases: [{ base: "processes_total", label: "processes" }],
    fmt: count,
  },
];

const NETWORK: PanelSpec[] = [
  // The gap between the two lines is the hub, not the network.
  //
  // hub_connect stops at SYN-ACK, so it is the path. post_latency is the
  // whole round trip -- TLS, upload, the hub's handling, the Postgres write
  // -- so a slow database lifts it while the handshake stays flat. Drawn
  // together because neither answers "where is the time going" alone.
  {
    title: "Hub latency",
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
    source: "agent",
    bases: [{ base: "hub_connect_failures_total", label: "failures" }],
    counter: true,
    fmt: count,
  },
  {
    // Ingress and egress, not rx and tx: the direction is the point of this
    // chart, and "rx" is the kernel's word for it rather than the reader's.
    title: "Interface throughput",
    unit: "B/s",
    source: "net",
    bases: [
      { base: "rx_bytes", label: "ingress" },
      { base: "tx_bytes", label: "egress" },
    ],
    // Mirrored about a midline, the way the fleet row has always drawn
    // traffic: two lines climbing one axis make a reader compare shapes to
    // answer "which way is this going".
    mirrored: true,
    fmt: bytes,
  },
  {
    title: "TCP statistics",
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
    source: "host",
    bases: [
      { base: "tcp_curr_estab", label: "established" },
      { base: "tcp_active_opens_per_s", label: "active opens" },
      { base: "tcp_passive_opens_per_s", label: "passive opens" },
      { base: "tcp_attempt_fails_per_s", label: "attempt fails" },
    ],
    fmt: count,
  },
  {
    title: "IP fragmentation",
    unit: "/s",
    source: "host",
    bases: [
      { base: "ip_frag_fails_per_s", label: "frag fails" },
      { base: "ip_reasm_fails_per_s", label: "reasm fails" },
      { base: "ip6_frag_fails_per_s", label: "frag fails (v6)" },
      { base: "ip6_reasm_fails_per_s", label: "reasm fails (v6)" },
    ],
    fmt: count,
  },
  {
    title: "UDP statistics",
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
];

const STORAGE: PanelSpec[] = [
  {
    title: "Disk throughput",
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
    source: "diskIo",
    bases: [{ base: "io_util_pct", label: "utilisation" }],
    max: 100,
    fmt: (n) => percent(n),
  },
  {
    title: "Filesystem space",
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
    source: "filesystem",
    bases: [
      { base: "inodes_used", label: "used" },
      { base: "inodes_total", label: "total" },
    ],
    fmt: count,
  },
];

export interface GraphsProps {
  host?: MetricsResponse | null;
  net?: MetricsResponse | null;
  diskIo?: MetricsResponse | null;
  filesystem?: MetricsResponse | null;
  collector?: MetricsResponse | null;
  cpuCore?: MetricsResponse | null;
  agent?: MetricsResponse | null;
  /** The range the page is showing. It seeds each enlarged view's own
   * picker; it is not written back from one. */
  range?: Range;
  /**
   * Loads ONE family at another range, for an enlarged chart alone.
   *
   * The page keeps fetching all seven families at the page's range, as it
   * always has. This exists so a single dialog can ask for a longer window
   * without dragging the other nineteen panels along -- which is what the
   * dialog's picker did when it was wired to the page's setter.
   */
  fetchFamily?: (family: string, range: Range) => Promise<MetricsResponse>;
}

function bandsFor(spec: PanelSpec, res: MetricsResponse | null): Band[] {
  if (res === null) return [];
  const keyed = res.key_columns.length > 0;
  const bands: Band[] = [];

  res.series.forEach((series, index) => {
    // A keyed family names its bands by the key it is keyed on, so two
    // devices in one panel stay tellable apart by their label, not by
    // colour alone.
    const prefix = keyed
      ? res.key_columns
          .map((k) => series.key[k])
          .filter(Boolean)
          .join(" ")
      : "";
    for (const { base, label } of spec.bases) {
      // griddedValues, not optionalValues: the response carries only the
      // buckets that exist, so an outage arrives as a SHORTER series rather
      // than as nulls, and the geometry breaks a line only on a null. Drawn
      // straight from the response, three hours of a host being down became
      // one unbroken line across the hole.
      const gridded = spec.boolean
        ? booleanValues(res, index, base)
        : griddedValues(res, index, base);
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
        color: SERIES_VARS[bands.length % SERIES_VARS.length],
        values,
      });
    }
  });

  return bands;
}

// Names the columns this tier is missing, because "not collected" without a
// reason is indistinguishable from a bug -- and the usual reason here is
// recoverable by the reader: most per-state columns exist only at full
// resolution, so a shorter range brings them back.
function missingReason(spec: PanelSpec, res: MetricsResponse): string {
  const missing = spec.bases
    .map((b) => b.base)
    .filter((base) => !carriesColumn(res, base));
  if (missing.length === 0) {
    return `The host reported no ${spec.title.toLowerCase()} samples in this window.`;
  }
  return `${missing.join(", ")} ${missing.length === 1 ? "is" : "are"} not stored at the ${res.tier} resolution. Choose a shorter range to see this panel.`;
}

function Panel({
  spec,
  res,
  range,
  fetchFamily,
}: {
  spec: PanelSpec;
  res: MetricsResponse | null;
  range?: Range;
  fetchFamily?: (family: string, range: Range) => Promise<MetricsResponse>;
}) {
  const series = bandsFor(spec, res);

  // The same bandsFor the page uses, over a response for one family at one
  // other range -- so an enlarged chart draws its wider window exactly as
  // the small one drew its narrower one, counters, stacks and all. Rebuilt
  // per render rather than memoised: useDetailRange only calls it when its
  // own range actually differs from the page's, which is at most once per
  // click on a picker nobody clicks in a loop.
  const fetchSeries = fetchFamily
    ? async (next: Range) => {
        const answered = await fetchFamily(FAMILY[spec.source], next);
        return {
          series: bandsFor(spec, answered),
          window: answered.window ?? null,
        };
      }
    : undefined;
  // An empty band list has two causes and they are not the same fact: this
  // tier does not carry the columns (the rollups drop most per-state
  // columns), or nothing has been fetched yet. Either way an empty chart
  // asserts "the host reported nothing", which spec 7.6 forbids -- so the
  // panel says which one it is instead of drawing a blank box.
  const unavailable =
    series.length > 0
      ? undefined
      : res === null
        ? "No data has been read for this family yet."
        : missingReason(spec, res);

  // No legend is built here: Overlay (inside ChartPanel) already renders
  // one as soon as a panel carries two or more bands, which is exactly the
  // point at which colour alone stops carrying identity.
  return (
    <ChartPanel
      title={spec.title}
      unit={spec.unit}
      series={series}
      max={spec.max}
      fmt={spec.fmt}
      stacked={spec.stacked}
      mirrored={spec.mirrored}
      // A 32-core legend is longer than the chart it explains. Suppressed
      // with legend, not highlight: the latter also dims every other series
      // to 35% and washed the whole stack out.
      legend={series.length <= 6}
      hideAxis={spec.hideAxis}
      // No per-panel notice: the window statement is about the RANGE, not
      // about any one chart, and repeating it under twenty panels made it
      // twenty pieces of noise nobody reads. It is rendered once, above the
      // grid (spec 7.2 puts it on the range control).
      notice={null}
      unavailable={unavailable}
      // The answered window and the page's range, so the enlarged view has
      // a real time axis and a picker seeded where the page is.
      window={res?.window ?? null}
      range={range}
      fetchSeries={fetchSeries}
      // Only what the host page's own fetcher will serve. It used to show
      // all five and hand the choice to the PAGE, so 30d here re-ranged a
      // toolbar that had no button for it and left every one unpressed.
      ranges={RANGE_VALUES}
    />
  );
}

function Group({
  title,
  specs,
  sources,
  extra,
}: {
  title: string;
  specs: PanelSpec[];
  sources: GraphsProps;
  extra?: string[];
}) {
  const { range, fetchFamily } = sources;
  return (
    <>
      <h3 className="grouphead">{title}</h3>
      <div className="sm">
        {specs.map((spec) => (
          <Panel
            key={spec.title}
            spec={spec}
            res={sources[spec.source] ?? null}
            range={range}
            fetchFamily={fetchFamily}
          />
        ))}
        {(extra ?? []).map((missing) => (
          <ChartPanel
            key={missing}
            title={missing}
            unavailable={UNAVAILABLE[missing]}
          />
        ))}
      </div>
    </>
  );
}

export function Graphs(props: GraphsProps) {
  // One line for the whole tab, deduplicated: every family answering the
  // same clamped window says the same sentence, and saying it once is the
  // difference between a statement and wallpaper.
  const notices = [
    ...new Set(
      [
        props.host,
        props.net,
        props.diskIo,
        props.filesystem,
        props.collector,
        props.agent,
      ]
        .map((res) => (res ? windowNotice(res) : null))
        .filter((n): n is string => n !== null),
    ),
  ];

  return (
    <div>
      {notices.map((notice) => (
        <p className="note" key={notice}>
          {notice}
        </p>
      ))}
      <Group title="System" specs={SYSTEM} sources={props} />
      <Group
        title="Network"
        specs={NETWORK}
        sources={props}
        extra={Object.keys(UNAVAILABLE)}
      />
      <Group title="Storage" specs={STORAGE} sources={props} />
    </div>
  );
}
