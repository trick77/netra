// The Graphs tab: small multiples. ONE ChartPanel component, N instances,
// uniform size (nothing here overrides its width/height), one range
// control -- the header's -- driving all of them. Adding a family later is
// a row in PANELS, not a new component.
import type { MetricsResponse } from "../../../lib/api";
import type { Range } from "../../../lib/range";
import {
  carriesColumn,
  griddedValues,
  seriesCells,
  windowNotice,
} from "../../../lib/metrics";
import { ABSENT, bytes, duration, percent } from "../../../lib/format";
import { ChartPanel, type Band } from "../../../ui/charts/ChartPanel";

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
  return seriesCells(res, seriesIndex, base).map((cell) => {
    if (typeof cell === "boolean") return cell ? 1 : 0;
    if (typeof cell === "number") return cell;
    // A string error_code, or anything else, is not a reading -- and a
    // gap is the honest rendering of "no reading".
    return null;
  });
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
const SERIES_VARS = ["var(--s1)", "var(--s2)", "var(--s3)", "var(--s4)"];

type Source = "host" | "net" | "diskIo" | "filesystem" | "collector";

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
}

// A count or a rate has no unit prefix worth inventing, so it is printed
// as-is, rounded -- but never as "0" when it is absent.
function count(n: number | null): string {
  return n === null ? ABSENT : String(Math.round(n * 100) / 100);
}

const SYSTEM: PanelSpec[] = [
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
  {
    title: "Interface throughput",
    unit: "B/s",
    source: "net",
    bases: [
      { base: "rx_bytes", label: "rx" },
      { base: "tx_bytes", label: "tx" },
    ],
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
  /** The page's range and setter, passed to each panel's enlarged view. */
  range?: Range;
  onRangeChange?: (range: Range) => void;
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
      const values = spec.boolean
        ? booleanValues(res, index, base)
        : griddedValues(res, index, base);
      if (values.length === 0) continue;
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
  onRangeChange,
}: {
  spec: PanelSpec;
  res: MetricsResponse | null;
  range?: Range;
  onRangeChange?: (range: Range) => void;
}) {
  const series = bandsFor(spec, res);
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
      // No per-panel notice: the window statement is about the RANGE, not
      // about any one chart, and repeating it under twenty panels made it
      // twenty pieces of noise nobody reads. It is rendered once, above the
      // grid (spec 7.2 puts it on the range control).
      notice={null}
      unavailable={unavailable}
      // The answered window and the page's range, so the enlarged view has
      // a real time axis and the control to widen it without closing.
      window={res?.window ?? null}
      range={range}
      onRangeChange={onRangeChange}
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
  const { range, onRangeChange } = sources;
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
            onRangeChange={onRangeChange}
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
      [props.host, props.net, props.diskIo, props.filesystem, props.collector]
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
