// The single host column definition. `HostTable` (Task 12) and `HostCards`
// (Task 13) both render the exact Column<HostRow>[] this file produces --
// neither owns its own idea of "what a host row looks like". A column
// added here appears in both the table and the card grid; there is no
// second place a column could go missing from.
//
// `width`/`align` (Table-only, see ui/Table.tsx) are deliberately never
// set below: this file has no opinion on layout, only on content.
import type { Column } from "../../ui/Table";
import { Badge } from "../../ui/Badge";
import { Meter } from "../../ui/Meter";
import { StackedSparkline, type Band } from "../../ui/charts/StackedSparkline";
import { Overlay } from "../../ui/charts/Overlay";
import { SPARK_WIDTH } from "../../ui/charts/size";
import {
  DOWN_COLOR,
  UP_COLOR,
  UpDownSparkline,
} from "../../ui/charts/UpDownSparkline";
import {
  ABSENT,
  binaryBytes,
  byterate,
  bytes,
  percent,
  relative,
} from "../../lib/format";
import type { Host } from "../../lib/api";
import { hostStatus, isReporting } from "../../lib/host";
import type { Severity } from "../../ui/Badge";
import type { Condition, ConditionKind, HostGroup } from "./conditions";
import { rangeLabel, type Range } from "../../lib/range";
import { Enlargeable, type DetailData } from "../../ui/charts/Enlargeable";
import { memoryBands } from "../../lib/bands";
import {
  MAX_PER_CORE,
  cpuBands,
  fetchHostFamily,
  trafficSeries,
} from "./hostTrends";
import { FLEET_RANGE_VALUES } from "./ranges";

// The range is only ever a label here: this file never resolves one into a
// getMetrics() call, it labels a chart that has already been handed
// absolute-time series. The resolution -- and the fact that the hub rejects
// relative strings outright -- lives in lib/range.
export type { Range };

/**
 * The view-model both renderers consume. `Host` as the read API actually
 * returns it (lib/api.ts), plus everything a later assembly task derives
 * from a host's metrics: pre-built, pre-coloured chart series and the
 * fullest-filesystem summary (never a per-filesystem list -- Disk shows
 * the one that matters plus a count of the rest, see the Disk column
 * below).
 *
 * `site_name` is additive beyond the brief's literal `Host & {...}` --
 * `Host` (HostSummary) carries only `site_id: number | null`, which has no
 * displayable name. Rendering the raw ID under the hostname would read as
 * a label ("Site 3") for an internal foreign key, which is worse than not
 * having a site name at all. `HostDetail` proves the name is resolvable
 * server-side, so this field asserts that a future assembly step joins it
 * in alongside the summary, the same way it builds `cpu`/`mem`/etc. It
 * changes nothing about the table/card contract: both renderers only ever
 * read fields off `Column.cell(row)`, never `HostRow`'s shape directly.
 *
 * Confirmed against the live read API where this join should happen:
 * `GET /api/v1/hosts` (the fleet list) returns only `site_id`;
 * `GET /api/v1/hosts/{id}` returns `site_name` but is a per-host detail
 * call, so calling it once per row would be an N+1 against the fleet
 * endpoint just to resolve a label. `GET /api/v1/sites` returns every
 * site with its name in one call -- the assembler should fetch that once
 * and join client-side by `site_id`, not hit the detail endpoint per host.
 *
 * `cpu`/`mem`: bytes/percent-per-second bands, already named and coloured
 * (`var(--sN)`) by whatever builds this row -- this file never invents a
 * hue (spec: no hardcoded colour in a .tsx file). `mem_total` (bytes, see
 * lib/api.ts's Host type) is the chart's scale ceiling for Memory, not an
 * extra band -- see the Memory column below for why.
 */
export type HostRow = Host & {
  site_name: string | null;
  cpu: Band[];
  mem: Band[];
  /** cpu_total from the `host` family, for every host -- the one series the
   * status badge is judged from. See HostTrends.reporting for why it is not
   * `cpu[0]`. */
  reporting: (number | null)[];
  // (number | null)[], matching Band.values, because an agent outage inside
  // the window is a hole in these series too. Typed number[] the row could
  // not express one, and UpDownSparkline -- which already breaks each side
  // at its own gaps -- would have drawn a straight line across it: "the host
  // was down" rendered as "traffic was steady".
  rx: (number | null)[];
  tx: (number | null)[];
  // null when the host has reported no filesystems at all. Non-nullable, the
  // only way to say "never collected" was pct: 0, which renders as an empty,
  // healthy, green disk -- absent read as a fact.
  // `since` is when this mount last crossed DISK_WARN_PCT and stayed over it,
  // walked back through its own series -- see crossedAt in hostTrends.ts.
  // `sinceAtLeast` marks the case where it was already over at the start of
  // the window, which is a floor rather than a moment: the row says "over
  // 24 h" instead of naming a bucket where nothing actually happened.
  fullest: {
    mount: string;
    pct: number;
    others: number;
    // Optional, unlike the two fields above: hostTrends always sets both, and
    // the hand-built row literals across the tests predate them. Same
    // convention lib/api.ts uses for a field added after its fixtures.
    since?: string | null;
    sinceAtLeast?: boolean;
  } | null;
  /** Every filesystem's Use% over the window, one band each. */
  disk: Band[];
  /** OOM kills inside the window -- the increase, never the cumulative
   * counter. null is "cannot say", which the attention band stays silent
   * about; 0 is the host confirming nothing happened. */
  oomKills: number | null;
  /** Samples the agent dropped before delivery, and failed deliveries to the
   * hub, both inside the window and both the increase rather than the
   * cumulative counter -- same null/0 rule as oomKills above. Neither is
   * plotted by any column here: they are read only by the attention band,
   * which is also the only reason the `agent` family is fetched at all. */
  dropped: number | null;
  postFailures: number | null;
};

function HostCell({ row }: { row: HostRow }) {
  // Judged from row.reporting -- cpu_total, from the `host` family -- and
  // never from row.cpu[0], which is a per-core band under 32 threads and the
  // cpu_total fallback above it. Reading cpu[0] judged one host against
  // cpu_core and its neighbour against host_samples: two relations with
  // different materialisation lags, so the badge meant a different thing on
  // adjacent rows of the same page. A host that answers now but keeps
  // dropping scrapes reads as sporadic rather than healthy; the gaps are
  // already visible in its sparkline, and this says the same thing in a word.
  const status = hostStatus(row, undefined, row.reporting);
  return (
    <div className="host-cell">
      <div className="host-cell-top">
        {/* The hostname is the way into the host page, and it is an anchor
            rather than a row click handler: middle-click, copy-link and
            bookmark all have to work, and a row-wide handler would swallow
            the text selection someone needs to read an id off the screen.
            The fleet list had no link into detail at all. */}
        <a className="host-cell-name" href={`/hosts/${row.id}/overview`}>
          {row.hostname}
        </a>
        {/* After the name, and only when there is something to say. Healthy is
          the overwhelming majority state, so a badge on every row spent the
          eye's first stop -- and the leftmost column -- on the word "online"
          repeated down the page. What a reader scans for is the exception,
          which is now the only thing marked. */}
        {status.severity !== "ok" && (
          <Badge severity={status.severity}>{status.label}</Badge>
        )}
      </div>
      {/* The location goes under the name rather than beside it: the two are
          a heading and its subtitle, not two peers, and the row has the
          vertical space. Inline, a long site name pushed the hostname off
          the eye's scan line down the column.

          A host with no site gets no line at all rather than an em dash: the
          dash is a placeholder for a value that should be there and is
          missing, and an unassigned host is not missing anything -- it is
          simply not in a site yet. A column of dashes under every hostname
          reads as a fleet full of holes. */}
      {row.site_name !== null && (
        <div className="host-cell-site">{row.site_name}</div>
      )}
    </div>
  );
}

// The stack is scaled to 100, never to its own running total. Without a
// ceiling StackedSparkline auto-scales each host to its own peak, so a host
// idling at 3% and one saturated at 90% draw the identical silhouette
// touching the top of the box -- the rows stop being comparable, which is
// what a fleet list is for. It is the same always-full reading MemoryCell
// below carries its own ceiling to avoid; the spec's silhouette is
// cpu_total, not cpu_total normalised to itself.
const CPU_PERCENT_MAX = 100;

function CpuCell({ row, range }: { row: HostRow; range: Range }) {
  // The dialog keeps the cell's normalisation and its 0-100 ceiling. That is
  // not laziness about reusing the fetch: normalised, the per-core stack tops
  // out at cpu_total, so 100 is a real ceiling and the axis reads as percent
  // of this host. Refetching unnormalised would put a 0-3200 axis on a
  // 32-core host and redraw the shape the reader just clicked. The host
  // page's Graphs tab draws the same cores unnormalised, where the numbers
  // matter more than cross-host comparability -- a different question, with
  // its own chart.
  const fetchSeries = async (next: Range): Promise<DetailData> => {
    const [host, cores] = await Promise.all([
      fetchHostFamily(row.id, "host", next),
      // The same guard the fan-out uses: a 128-core host would ship 128
      // series, and cpu_total is the silhouette it falls back to.
      //
      // Swallowed to null on failure, unlike the host family beside it: the
      // per-core read is an enrichment with a documented fallback (cpuBands
      // draws cpu_total when it gets none), so failing the whole dialog on
      // it would report "could not load that range" for a range the chart
      // can in fact draw. The host family is the primary read and still
      // fails loudly.
      row.threads !== null && row.threads <= MAX_PER_CORE
        ? fetchHostFamily(row.id, "cpu_core", next).catch(() => null)
        : Promise.resolve(null),
    ]);
    // The window of the response the BANDS were gridded against -- cpuBands
    // says which one that was, because only it knows which branch it took.
    const cpu = cpuBands(host, cores);
    return { series: cpu.bands, window: (cpu.from ?? host).window };
  };

  return (
    <Enlargeable
      title={`CPU · ${row.hostname}`}
      label={`Enlarge CPU for ${row.hostname}`}
      className="inline"
      unit="%"
      series={row.cpu}
      max={CPU_PERCENT_MAX}
      stacked
      // No legend in the dialog either when the stack is per-core: the stats
      // table underneath already names every core beside its colour, and 32
      // entries above the plot squeeze it into a corner.
      legend={row.cpu.length <= 1}
      fmt={(n) => percent(n)}
      range={range}
      ranges={FLEET_RANGE_VALUES}
      fetchSeries={fetchSeries}
    >
      <StackedSparkline
        bands={row.cpu}
        max={CPU_PERCENT_MAX}
        label={`CPU trend, ${rangeLabel(range)}`}
      />
    </Enlargeable>
  );
}

// Free memory is deliberately never a band: stacking it would make every
// host look full regardless of how much headroom it actually has. Instead
// the stack is scaled against `mem_total` (the real ceiling) rather than
// StackedSparkline's own auto-scale (the largest running total of the
// bands themselves, i.e. "used + buffers + cached + ARC" with no free
// left over) -- that auto-scale is exactly the always-full reading this
// column exists to avoid. See hostColumns.test.tsx's
// "scales the stack against mem_total" test, which proves this by
// recomputing stackBands() with the same max and diffing the rendered
// path data against it.
// Scaled to mem_total the stack says how the parts move but not whether the
// host is nearly full, because nothing on screen says what the top of the box
// means. The dashed rule says it -- and it needs room above the stack to be
// visible as a rule.
const MEM_HEADROOM = 1.08;

function MemoryCell({ row, range }: { row: HostRow; range: Range }) {
  if (row.mem_total === null) {
    // No known ceiling means no scale to draw the stack against. Drawing
    // one anyway (e.g. falling back to the bands' own auto-scale) would
    // silently readmit the always-full bug this column is built to avoid,
    // so this renders the absent marker instead of a chart with an
    // invented ceiling -- the same rule Meter itself follows.
    return <>{ABSENT}</>;
  }
  const total = row.mem_total;
  const fetchSeries = async (next: Range): Promise<DetailData> => {
    const host = await fetchHostFamily(row.id, "host", next);
    return { series: memoryBands(host), window: host.window };
  };

  return (
    <Enlargeable
      title={`Memory · ${row.hostname}`}
      label={`Enlarge Memory for ${row.hostname}`}
      className="inline"
      series={row.mem}
      max={total * MEM_HEADROOM}
      reference={total}
      stacked
      // Binary, like the host page's Memory panel: the bands are read against
      // the mem_total rule, and a stack labelled decimally under a rule
      // labelled binarily makes one quantity look like two.
      fmt={(n) => binaryBytes(n)}
      range={range}
      ranges={FLEET_RANGE_VALUES}
      fetchSeries={fetchSeries}
    >
      <StackedSparkline
        bands={row.mem}
        // A little headroom above total, so the dashed rule marking it lands
        // inside the plot instead of on the border, where it would read as the
        // edge of the box rather than as the host's ceiling.
        max={total * MEM_HEADROOM}
        reference={total}
        // No legend, like every other cell in this row. A previous review
        // argued the five memory bands carry identity a legend should name and
        // turned it back on here; that is not the call. These are sparklines
        // in a dense list -- the shape is the message, and naming five bands
        // under a 32px chart costs more row height than the names are worth.
        // The host page's Memory panel is where the breakdown gets named.
        //
        // The ENLARGED view does name them: it has the room, and "which part
        // of memory is growing" is the question someone opens it to ask.
        label={`Memory trend, ${rangeLabel(range)}`}
      />
    </Enlargeable>
  );
}

// The NUMBERS come from host_current, the sparkline from the series.
//
// They used to share the series: the rates were latestValue(row.rx/tx), the
// value at the latest bucket. That made a current rate depend on the RANGE,
// because the range picks the step and the step picks the storage tier --
// raw rx_bytes at 1h, a five-minute rx_bytes_avg that ended a quarter of an
// hour ago at 6h and 24h. Widening the charts changed the number beside
// them, which is not something a reader can be expected to account for.
//
// The scalar has no bucket and no window, so it is the same at every range.
// The sparkline still follows the range, which is what a sparkline is for.
// A null is absent rather than zero: a host that stopped reporting must not
// read as a host moving no traffic. The gauge is the one thing about such a
// host that does NOT go absent on its own -- host_current keeps the last
// pair it was written, deliberately -- so an offline host is gated back to
// absent here. Without it the row drew a steady rate beside its own
// "offline" badge, which is exactly the frozen-in-place reading the series
// version was written to avoid.

// Both rates render in identical type, weighted only by an arrow glyph --
// netra cannot know whether a given host is meant to push or pull more
// traffic, so bolding or upsizing one over the other would assert a
// direction that isn't true. The arrow alone is not an accessible
// distinguisher, so each rate carries its own aria-label naming the
// direction in words.
function TrafficCell({ row, range }: { row: HostRow; range: Range }) {
  const live = isReporting(row);
  const rx = live ? row.net_rx_bytes : null;
  const tx = live ? row.net_tx_bytes : null;
  // Ingress and egress, in that order: Overlay's mirrored mode reads its
  // series in pairs, (0,1) being one interface's in and out. The cell sums
  // every interface into one pair, so the dialog does too -- the enlarged
  // view of a cell must be the same chart, larger, not a different one.
  const fetchSeries = async (next: Range): Promise<DetailData> => {
    const net = await fetchHostFamily(row.id, "net", next);
    const traffic = trafficSeries(net);
    return {
      series: [
        { name: "ingress", color: UP_COLOR, values: traffic.rx },
        { name: "egress", color: DOWN_COLOR, values: traffic.tx },
      ],
      window: net.window,
    };
  };

  return (
    <div className="traffic-cell">
      <Enlargeable
        // Ingress and egress, not rx and tx: the direction is the point of
        // this chart, and "rx" is the kernel's word for it rather than the
        // reader's. The wire and the schema keep rx/tx.
        title={`Traffic · ${row.hostname}`}
        label={`Enlarge Traffic for ${row.hostname}`}
        className="inline"
        unit="B/s"
        series={[
          { name: "ingress", color: UP_COLOR, values: row.rx },
          { name: "egress", color: DOWN_COLOR, values: row.tx },
        ]}
        mirrored
        fmt={bytes}
        range={range}
        ranges={FLEET_RANGE_VALUES}
        fetchSeries={fetchSeries}
      >
        <UpDownSparkline
          up={row.rx}
          down={row.tx}
          label={`Traffic trend, ${rangeLabel(range)}`}
        />
      </Enlargeable>
      {/* byterate, never bitrate: rx_bytes/tx_bytes are BYTES per second
          (internal/agent/collector/network.go divides a byte delta by the
          elapsed seconds), and bitrate() drew every rate on this page 8x
          low. The host overview's Traffic card had the identical bug, which
          is why nothing on screen contradicted it. */}
      <div className="traffic-rates">
        <span className="rate" aria-label={`inbound ${byterate(rx)}`}>
          <span aria-hidden="true">↑</span> {byterate(rx)}
        </span>
        <span className="rate" aria-label={`outbound ${byterate(tx)}`}>
          <span aria-hidden="true">↓</span> {byterate(tx)}
        </span>
      </div>
    </div>
  );
}

// The fullest filesystem, named, plus a +N count of the rest -- never a
// sum across filesystems (spec: a 503 GB root at 68% and a 7.8 TB array at
// 88% average to a number that hides a root at 99%). `fullest` is a
// single pre-picked summary, not a list, so there is nothing here to sum:
// the row's assembler already did the picking.
// Free-scaled to 0-100 with a fixed ceiling, like CPU: a disk at 40% and one
// at 95% must not draw the same silhouette, which is exactly what a
// self-scaled sparkline would do.
function DiskTrendCell({ row, range }: { row: HostRow; range: Range }) {
  if (row.disk.length === 0) return <>{ABSENT}</>;
  return (
    <Overlay
      series={row.disk}
      min={0}
      max={100}
      width={SPARK_WIDTH}
      height={32}
      // Lines rather than filled areas, and no legend: usage sits between
      // 40% and 95%, so masses anchored at zero would pile into one solid
      // block, and naming six mounts under a 32px chart is the same
      // row-height problem the CPU column already solved by not naming
      // thirty-two cores.
      legend={false}
      label={`Filesystem usage trend, ${rangeLabel(range)}`}
    />
  );
}

function DiskCell({ row }: { row: HostRow }) {
  if (row.fullest === null) {
    // A host that has reported no filesystems has no fullest one. Drawing a
    // meter anyway would put an empty green bar where "never collected"
    // belongs, which reads as a fact rather than as an absence.
    return <>{ABSENT}</>;
  }
  const { mount, pct, others } = row.fullest;
  const label = others > 0 ? `${mount} +${others}` : mount;
  // Meter's own row reserves a fixed 92px for its value, which is sized for
  // the host page's "108.9 GB of 137.4 GB" and leaves a percentage floating
  // half a column away from the bar it belongs to. Scoped here rather than
  // changed in .mrow, which that panel still needs.
  return (
    <div className="disk-cell">
      <Meter value={pct} max={100} label={label} />
    </div>
  );
}

// The Uptime column and its cell are gone from this list. Uptime is a fact
// about a host, not a reading to scan a fleet by: it is the same number all
// day and the row's job is what changed. It still leads the host page's
// System card, where a reader has asked about one machine.
//
// The "rebooted N minutes ago" warning it carried went with it, and the
// comment here used to claim it survived "as the header's own status" --
// which it did not: hostStatus() has no reboot branch and nothing on any
// page said the word. It is now a real badge in the host page header
// (HostPage.tsx, RECENT_BOOT_S), which is where a reader who has asked
// about one machine can act on it.

/**
 * What the list is being asked, when it is being asked something other than
 * "how is the fleet".
 *
 * `groups` is every troubled host's conditions, keyed by host id, and `kind`
 * is the one condition kind the reader picked -- null when they picked a
 * severity instead, which is several kinds at once. Both come from the page,
 * which owns the filter; this file only decides what a row looks like once
 * the question is known.
 */
export interface AttentionView {
  groups: ReadonlyMap<string, HostGroup>;
  kind: ConditionKind | null;
  range: Range;
}

/**
 * The condition this row is on screen FOR.
 *
 * With a kind picked it is that kind's condition, not the host's worst: a
 * reader filtering to "filesystem over 90%" on a host that is also silent is
 * asking about the disk, and answering with the silence would drop the row's
 * subject on the floor. With only a severity picked there is no such subject,
 * so the worst condition speaks for the host.
 */
function subject(view: AttentionView, row: HostRow): Condition | null {
  const group = view.groups.get(String(row.id));
  if (group === undefined) return null;
  if (view.kind === null) return group.worst;
  return group.conditions.find((c) => c.kind === view.kind) ?? group.worst;
}

/** The status hue as a bare mark. The word that has to go with it (spec
 * §3.3) is not visible text here -- the sentence in the next column is what
 * a sighted reader gets -- so the severity is spelled out for assistive tech
 * and for hover instead of being left to the colour. */
function SeverityDot({ severity }: { severity: Severity }) {
  const word = SEVERITY_WORD[severity];
  return (
    <span
      className={`dot ${SEVERITY_CLASS[severity]}`}
      role="img"
      aria-label={word}
      title={word}
    />
  );
}

const SEVERITY_WORD: Record<Severity, string> = {
  critical: "Critical",
  serious: "Serious",
  warning: "Warning",
  ok: "OK",
  neutral: "Unknown",
};

const SEVERITY_CLASS: Record<Severity, string> = {
  critical: "st-crit",
  serious: "st-serious",
  warning: "st-warn",
  ok: "st-ok",
  neutral: "",
};

/**
 * The mark that proves the row's own condition -- see Evidence in
 * conditions.ts for why this column changes meaning from row to row.
 */
function EvidenceCell({
  row,
  condition,
  range,
}: {
  row: HostRow;
  condition: Condition;
  range: Range;
}) {
  const evidence = condition.evidence;
  if (evidence === null) {
    // Said out loud rather than left blank. An empty cell reads as a
    // rendering fault; "the missing data is the evidence" is the actual
    // reason a dropped-samples row has no mark to draw.
    return <span className="ev-none">no mark — see the host</span>;
  }
  switch (evidence.type) {
    case "meter":
      return (
        <div className="disk-cell">
          <Meter value={evidence.pct} max={100} label={condition.label} />
        </div>
      );
    case "units":
      return (
        <span className="mono">
          {evidence.names.length === 0 ? ABSENT : evidence.names.join(", ")}
          {evidence.extra > 0 ? (
            <span className="ev-more"> +{evidence.extra}</span>
          ) : null}
        </span>
      );
    case "memory":
      return <MemoryCell row={row} range={range} />;
    case "reporting":
      return <CpuCell row={row} range={range} />;
  }
}

/**
 * How long the condition has been true, or nothing at all.
 *
 * "over 24 h" rather than a timestamp when the condition predates the
 * window: netra cannot see further back than the range the reader picked,
 * and a floor stated as a floor is the difference between a fact and a
 * guess. See Condition.sinceAtLeast.
 */
function SinceCell({
  condition,
  range,
}: {
  condition: Condition;
  range: Range;
}) {
  if (condition.since === null) return <>{ABSENT}</>;
  if (condition.sinceAtLeast === true) {
    // The bare range, not rangeLabel's "last 1h": "over last 1h" is not a
    // duration. This is the same string the range control shows, which is
    // what makes the floor legible -- widen the range and the number appears.
    return <span className="tnum">{`over ${range}`}</span>;
  }
  return <span className="tnum">{relative(condition.since)}</span>;
}

/**
 * The columns for "what is wrong", which are not the columns for "how is the
 * fleet".
 *
 * CPU and memory sparklines beside a full filesystem say nothing about why
 * the row is there and quietly suggest CPU is the problem -- so in this set
 * they are replaced by the sentence, the mark that proves it, when it
 * started, and the page that answers it in full. The host keeps its name and
 * loses its site: the row is about a condition, and the location is inventory
 * the unfiltered list still carries.
 */
function attentionColumns(view: AttentionView): Column<HostRow>[] {
  return [
    {
      key: "host",
      header: "Host",
      cell: (row) => {
        const condition = subject(view, row);
        return (
          <div className="attn-host-cell">
            {condition === null ? null : (
              <SeverityDot severity={condition.severity} />
            )}
            <a className="host-cell-name" href={`/hosts/${row.id}/overview`}>
              {row.hostname}
            </a>
          </div>
        );
      },
      sortValue: (row) => row.hostname,
    },
    {
      key: "what",
      header: "What is wrong",
      cell: (row) => {
        const condition = subject(view, row);
        if (condition === null) return <>{ABSENT}</>;
        const others =
          (view.groups.get(String(row.id))?.conditions.length ?? 1) - 1;
        return (
          <span className="what">
            {condition.what}
            {others > 0 ? (
              // Never hidden, never expanded here: the host's own page is
              // where its other conditions are read, and a disclosure per row
              // is the band's mistake in a new place.
              <span className="what-more">{` · +${others} more`}</span>
            ) : null}
          </span>
        );
      },
    },
    {
      key: "evidence",
      header: "Evidence",
      cell: (row) => {
        const condition = subject(view, row);
        if (condition === null) return <>{ABSENT}</>;
        return (
          <EvidenceCell row={row} condition={condition} range={view.range} />
        );
      },
    },
    {
      key: "since",
      header: "Since",
      cell: (row) => {
        const condition = subject(view, row);
        if (condition === null) return <>{ABSENT}</>;
        return <SinceCell condition={condition} range={view.range} />;
      },
      // Oldest first when sorted descending: a condition that has been true
      // for six days is the one that has been ignored longest. Nulls sort
      // last in both directions, which Table already guarantees.
      sortValue: (row) => {
        const condition = subject(view, row);
        if (condition?.since == null) return null;
        const ms = Date.parse(condition.since);
        return Number.isNaN(ms) ? null : -ms;
      },
    },
    {
      key: "drill",
      header: "",
      cell: (row) => {
        const condition = subject(view, row);
        const tab = condition?.tab ?? "overview";
        return (
          <a
            className="drill"
            href={`/hosts/${row.id}/${tab}`}
            aria-label={`${tab} on ${row.hostname}`}
          >
            {tab} →
          </a>
        );
      },
    },
  ];
}

export function hostColumns(
  range: Range,
  attention?: AttentionView,
): Column<HostRow>[] {
  if (attention !== undefined) return attentionColumns(attention);
  return [
    {
      key: "host",
      header: "Host",
      cell: (row) => <HostCell row={row} />,
      sortValue: (row) => row.hostname,
    },
    // Second, immediately right of the host it belongs to. Traffic is the
    // reading a fleet list is most often scanned for -- "is anything moving
    // that should not be" -- and it sat fourth, past two charts, where the eye
    // reached it last.
    {
      key: "traffic",
      header: "Traffic",
      cell: (row) => <TrafficCell row={row} range={range} />,
    },
    {
      key: "cpu",
      header: "CPU",
      cell: (row) => <CpuCell row={row} range={range} />,
    },
    {
      key: "memory",
      header: "Memory",
      cell: (row) => <MemoryCell row={row} range={range} />,
    },
    {
      key: "diskTrend",
      header: "Filesystem",
      cell: (row) => <DiskTrendCell row={row} range={range} />,
    },
    {
      key: "disk",
      header: "Disk",
      cell: (row) => <DiskCell row={row} />,
      // The fullest filesystem's percentage -- the number the cell shows.
      // Sorting on bytes would put the biggest disk first rather than the
      // one closest to filling up, which is what this column is for.
      sortValue: (row) => row.fullest?.pct ?? null,
    },
  ];
}
