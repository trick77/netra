// The single host column definition. `HostTable` renders the exact
// Column<HostRow>[] this file produces and owns no idea of its own about
// "what a host row looks like", so a column added here is a column the fleet
// shows; there is no second place one could go missing from.
//
// `width`/`align` (Table-only, see ui/Table.tsx) are deliberately never
// set below: this file has no opinion on layout, only on content.
import type { Column } from "../../ui/Table";
import { Badge } from "../../ui/Badge";
import { Meter } from "../../ui/Meter";
import { StackedSparkline, type Band } from "../../ui/charts/StackedSparkline";
import { Overlay } from "../../ui/charts/Overlay";
import { DETAIL_WIDTH, SPARK_HEIGHT, SPARK_WIDTH } from "../../ui/charts/size";
import {
  DOWN_COLOR,
  UP_COLOR,
  UpDownSparkline,
} from "../../ui/charts/UpDownSparkline";
import { binaryBytes, byterate, bytes, percent } from "../../lib/format";
import type { Host } from "../../lib/api";
import { hostStatus, isReporting } from "../../lib/host";
import { RAIL_RANGES, rangeLabel, type Range } from "../../lib/range";
import { Enlargeable, type DetailData } from "../../ui/charts/Enlargeable";
import { filesystemBands, memoryBands } from "../../lib/bands";
import {
  MAX_PER_CORE,
  cpuBands,
  fetchHostFamily,
  trafficDetailSeries,
  trafficSeries,
} from "./hostTrends";
import { lastReported } from "../container/columns";

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
 * Nothing about WHERE a host is is derived here. `GET /api/v1/hosts` carries
 * location, provider and facility on every row, straight from what each
 * host's own agent reported, so the cell reads them off `Host` and this type
 * adds nothing for them. It briefly did the opposite -- a site_name joined in
 * from a second fetch of the sites table, then a provider name from a third
 * fetch of providers -- to answer a question the agent had been answering all
 * along.
 *
 * `cpu`/`mem`: bytes/percent-per-second bands, already named and coloured
 * (`var(--sN)`) by whatever builds this row -- this file never invents a
 * hue (spec: no hardcoded colour in a .tsx file). `mem_total` (bytes, see
 * lib/api.ts's Host type) is the chart's scale ceiling for Memory, not an
 * extra band -- see the Memory column below for why.
 */
export type HostRow = Host & {
  // location/provider/facility are NOT declared here: they ride `Host`,
  // straight off the fleet list endpoint. This row briefly carried its own
  // site_name/provider_name/facility/country_code, assembled by joining the
  // sites and providers tables client-side -- two extra whole-table fetches
  // to answer a question the agent was already answering on every metadata
  // post and the hub was discarding.
  /** The window the hub answered, for the enlarged view's time axis. See
   * HostTrends.window for why it is the answer rather than the ask. */
  window: { from: string; to: string } | null;
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
  /** The same pair as the bucket peak, drawn only by the ENLARGED view --
   * see HostTrends. Empty at the raw tier, where the sample is its own peak,
   * and optional because a row assembled without it simply opens into a
   * chart with no envelope rather than failing to compile. */
  rxPeak?: (number | null)[];
  txPeak?: (number | null)[];
  // null when the host has reported no filesystems at all. Non-nullable, the
  // only way to say "never collected" was pct: 0, which renders as an empty,
  // healthy, green disk -- absent read as a fact.
  // `since` is when this mount last became notable under the compound disk
  // rule -- high enough AND with little enough left, see diskSeverityFor in
  // conditions.ts -- and stayed there, walked back through its own series by
  // crossedAt in hostTrends.ts. `sinceAtLeast` marks the case where it was
  // already notable at the start of the window, which is a floor rather than
  // a moment: the row says "over 24 h" instead of naming a bucket where
  // nothing actually happened.
  fullest: {
    mount: string;
    pct: number;
    // Bytes left on THIS mount. The percentage cannot decide on its own
    // whether the mount is worth surfacing: 90% of a 6.7 TB array is 674 GB
    // free. Null is "not known", which falls back to the percentage alone.
    free?: number | null;
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

/**
 * The location line under a hostname: "OVH · Roubaix, France".
 *
 * Both halves come off the host itself, reported by its own agent. The
 * provider is a separate fact from the place and takes the dot this app uses
 * everywhere else to separate peers.
 *
 * The place is printed exactly as the agent sent it. AGENT_LOCATION is free
 * text an operator wrote -- "Roubaix, France", "basement", "AWS eu-west-1a" --
 * so there is no city to split off, no country code to resolve, and nothing
 * to normalise. Anything this did to that string would be this UI overruling
 * the person who typed it.
 *
 * An absent half is left out rather than dashed, and null means neither was
 * reported -- the common case for a fleet nobody has set the variables on.
 * The caller then writes no line at all, because a dash under every hostname
 * reads as a fleet full of holes.
 */
export function hostLocation(row: {
  provider?: string | null;
  location?: string | null;
}): string | null {
  // Truthy-string tests rather than null checks: both fields are optional on
  // Host, so a row built before they existed -- a fixture, a cached response
  // -- must render a hostname with no location line rather than throw on the
  // way to drawing the whole table.
  const parts = [row.provider, row.location].filter(
    (part): part is string => typeof part === "string" && part !== "",
  );
  return parts.length === 0 ? null : parts.join(" · ");
}

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
  const location = hostLocation(row);
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
          vertical space. Inline, a long place name pushed the hostname off
          the eye's scan line down the column.

          A host whose agent reports no location gets no line at all rather
          than an em dash: the dash is a placeholder for a value that should
          be there and is missing, and an agent with neither AGENT_LOCATION
          nor AGENT_PROVIDER set is not missing anything -- it was never told
          where it is. A column of dashes under every hostname reads as a
          fleet full of holes. Note this is about what the AGENT reported and
          not about sites: a host can be in a site and still draw no line.

          What the line SAYS is the provider and the place, not the site name
          it said before. The site name was an internal label out of a table
          somebody fills in by hand -- a reader scanning a fleet learns
          nothing from "gra-rack-7" that the hostname beside it had not
          already told them, whereas "OVH · Roubaix, France" answers whose
          machine this is and where it sits. Both come from the host's own
          agent, so a fleet says where it is with nobody maintaining a table
          of places.

          Still one line, deliberately. This column had two and keeps two --
          a third would put height back on every row of the table, which is
          the opposite of what the header change just spent itself on. */}
      {location !== null && <div className="host-cell-site">{location}</div>}
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
  // The page keeps the cell's normalisation and its 0-100 ceiling, which is
  // why the link goes to host-cpu and not to the Graphs tab's "CPU cores":
  // normalised, the per-core stack tops out at cpu_total, so 100 is a real
  // ceiling and the axis reads as percent of this host. Unnormalised would
  // put a 0-3200 axis on a 32-core host and redraw the shape the reader just
  // clicked. The Graphs tab draws the same cores unnormalised, where the
  // numbers matter more than cross-host comparability -- a different
  // question, with its own chart.
  // The dialog keeps the cell's normalisation and its 0-100 ceiling. That is
  // not laziness about reusing the fetch: normalised, the per-core stack tops
  // out at cpu_total, so 100 is a real ceiling and the axis reads as percent
  // of this host. Refetching unnormalised would put a 0-3200 axis on a
  // 32-core host and redraw the shape the reader just clicked.
  const fetchSeries = async (next: Range): Promise<DetailData> => {
    const [host, cores] = await Promise.all([
      fetchHostFamily(row.id, "host", next),
      // The same guard the fan-out uses: a 128-core host would ship 128
      // series, and cpu_total is the silhouette it falls back to.
      //
      // Swallowed to null on failure, unlike the host family beside it: the
      // per-core read is an enrichment with a documented fallback (cpuBands
      // draws cpu_total when it gets none), so failing the whole dialog on it
      // would report "could not load that range" for a range the chart can in
      // fact draw.
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
      fmt={(n) => percent(n)}
      window={row.window}
      range={range}
      ranges={RAIL_RANGES}
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
    // No known ceiling means no scale to draw the stack against. Drawing one
    // anyway (e.g. falling back to the bands' own auto-scale) would silently
    // readmit the always-full bug this column is built to avoid -- the same
    // rule Meter itself follows.
    //
    // NOTHING, rather than a dash. A dash is a mark, and a mark asserts that
    // netra looked and found a value it could not print; the truth here is
    // that there is nothing to say. It also read as data on a row whose other
    // cells are charts: a column of em dashes down a silent host looked like
    // a reading of zero. The host cell already carries the "stopped
    // reporting" badge that explains the empty row.
    return null;
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
      window={row.window}
      range={range}
      ranges={RAIL_RANGES}
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
        // under a 45px chart costs more row height than the names are worth.
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
  // A page, not a dialog. This cell's chart -- every interface summed into
  // one in/out pair -- now has a spec and a slug of its own
  // (host-traffic, chartSpecs.ts), drawn from the same sumSeries the cell
  // Ingress and egress, in that order: Overlay's mirrored mode reads its
  // series in pairs, (0,1) being one interface's in and out. The cell sums
  // every interface into one pair, so the dialog does too -- the enlarged
  // view of a cell must be the same chart, larger, not a different one.
  const fetchSeries = async (next: Range): Promise<DetailData> => {
    const net = await fetchHostFamily(row.id, "net", next);
    // Folded to the DIALOG's width, not the cell's: the enlarged view has
    // six times the pixels and is entitled to six times the detail. Same
    // reduction, same reading, more of it.
    return {
      series: trafficDetailSeries(trafficSeries(net, DETAIL_WIDTH)),
      window: net.window,
    };
  };

  return (
    <div className="traffic-cell">
      <Enlargeable
        // "in" and "out", not rx and tx: the direction is the point of this
        // chart, and "rx" is the kernel's word for it rather than the
        // reader's. The wire and the schema keep rx/tx.
        title={`Traffic · ${row.hostname}`}
        label={`Enlarge Traffic for ${row.hostname}`}
        className="inline"
        unit="B/s"
        series={[
          { name: "in", color: UP_COLOR, values: row.rx },
          { name: "out", color: DOWN_COLOR, values: row.tx },
        ]}
        mirrored
        // What the DIALOG draws on open, before any refetch: the mean as the
        // line with the peak as the envelope. Without it the dialog shows the
        // cell's peak series and its stats table prints peak numbers under
        // Min and Mean headers until somebody touches the range picker.
        detailSeries={trafficDetailSeries(row)}
        fmt={bytes}
        window={row.window}
        range={range}
        ranges={RAIL_RANGES}
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
      {/* A rate the host never reported prints NOTHING, not a dash. Stacked
          two deep beside a chart, "↑ — ↓ —" read as a pair of readings rather
          than as their absence -- and on a silent host the whole row became a
          column of dashes that looked like data. The gap in the sparkline
          beside it already says the host stopped talking, and the badge on the
          host cell says why. */}
      <div className="traffic-rates">
        {rx === null ? null : (
          <span className="rate" aria-label={`inbound ${byterate(rx)}`}>
            <span aria-hidden="true">↑</span> {byterate(rx)}
          </span>
        )}
        {tx === null ? null : (
          <span className="rate" aria-label={`outbound ${byterate(tx)}`}>
            <span aria-hidden="true">↓</span> {byterate(tx)}
          </span>
        )}
      </div>
    </div>
  );
}

// The filesystem worth acting on, named, plus a +N count of the rest -- never
// a sum across filesystems (spec: a 503 GB root at 68% and a 7.8 TB array at
// 88% average to a number that hides a root at 99%). `fullest` is a
// single pre-picked summary, not a list, so there is nothing here to sum:
// the row's assembler already did the picking -- and it picks by severity
// before percentage, so this is not always the highest number on the host.
// See outranks() in hostTrends.ts.
// Free-scaled to 0-100 with a fixed ceiling, like CPU: a disk at 40% and one
// at 95% must not draw the same silhouette, which is exactly what a
// self-scaled sparkline would do.
function DiskTrendCell({ row, range }: { row: HostRow; range: Range }) {
  // Empty, not a dash -- see MemoryCell.
  if (row.disk.length === 0) return null;
  // One band per filesystem, exactly as the row assembler built the cell's
  // (hostTrends.ts) -- so widening the range redraws these mounts rather
  // than a different set of them.
  const fetchSeries = async (next: Range): Promise<DetailData> => {
    const fs = await fetchHostFamily(row.id, "filesystem", next);
    return { series: filesystemBands(fs), window: fs.window };
  };
  return (
    // The one sparkline in this row that was not even clickable: it was a
    // bare Overlay, so the chart most likely to be the reason a host is on
    // this list at all had no way in.
    <Enlargeable
      title={`Filesystem usage · ${row.hostname}`}
      label={`Enlarge Filesystem usage for ${row.hostname}`}
      className="inline"
      series={row.disk}
      min={0}
      max={100}
      fmt={(n) => percent(n)}
      window={row.window}
      range={range}
      ranges={RAIL_RANGES}
      fetchSeries={fetchSeries}
    >
      <Overlay
        series={row.disk}
        min={0}
        max={100}
        width={SPARK_WIDTH}
        height={SPARK_HEIGHT}
        // Lines rather than filled areas, and no legend: usage sits between
        // 40% and 95%, so masses anchored at zero would pile into one solid
        // block, and naming six mounts under a 45px chart is the same
        // row-height problem the CPU column already solved by not naming
        // thirty-two cores.
        legend={false}
        label={`Filesystem usage trend, ${rangeLabel(range)}`}
      />
    </Enlargeable>
  );
}

function DiskCell({ row }: { row: HostRow }) {
  if (row.fullest === null) {
    // A host that has reported no filesystems has no fullest one. Drawing a
    // meter anyway would put an empty green bar where "never collected"
    // belongs, which reads as a fact rather than as an absence -- and a dash
    // is the same mistake one step quieter. See MemoryCell.
    return null;
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
 * The stack's latest total: what a stacked sparkline's right-hand edge is
 * standing at, summed across every band.
 *
 * Per-band lastReported rather than one shared index, because the bands are
 * gridded independently and a host whose newest cpu_core bucket has landed
 * for six cores and not the seventh must not read as having lost that core's
 * load. Null only when NO band ever reported -- which is the "unknown" the
 * table sorts last, and is not the same as a host genuinely sitting at 0.
 */
function latestStackTotal(bands: readonly Band[]): number | null {
  let total: number | null = null;
  for (const band of bands) {
    const v = lastReported(band.values);
    if (v !== null) total = (total ?? 0) + v;
  }
  return total;
}

/**
 * The highest reading across a set of independently gridded series.
 *
 * What the Filesystem cell is scanned for: it draws one line per mount and
 * the question a reader brings to it is which host has a mount running out,
 * so the column orders on the topmost line rather than on an average across
 * mounts -- the same argument the Disk column's own comment makes about not
 * summing filesystems.
 */
function latestPeak(bands: readonly Band[]): number | null {
  let peak: number | null = null;
  for (const band of bands) {
    const v = lastReported(band.values);
    if (v !== null && (peak === null || v > peak)) peak = v;
  }
  return peak;
}

export function hostColumns(range: Range): Column<HostRow>[] {
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
      // The two rates the cell PRINTS, added: the cell shows in and out as a
      // pair and the fleet question is which host is moving the most, not
      // which direction it moved it in. Read through the same isReporting
      // guard the cell reads them through, so a host whose last rates are
      // hours stale sorts as unknown rather than as busy -- the sparkline
      // beside them has already gone to a gap, and ordering the list by a
      // number the cell refuses to draw would put a silent host at the top.
      sortValue: (row) => {
        if (!isReporting(row)) return null;
        const rx = row.net_rx_bytes;
        const tx = row.net_tx_bytes;
        if (rx === null && tx === null) return null;
        return (rx ?? 0) + (tx ?? 0);
      },
    },
    {
      key: "cpu",
      header: "CPU",
      cell: (row) => <CpuCell row={row} range={range} />,
      // Where the stack stands now. The cell prints no figure -- it is a
      // sparkline and nothing else -- so "sort on what the row shows" means
      // its right-hand edge, which is the reading a reader takes off it.
      // Normalised against cpu_total by the assembler, so this is percent of
      // THIS host and a 32-core box does not outrank a busy 4-core one.
      sortValue: (row) => latestStackTotal(row.cpu),
    },
    {
      key: "memory",
      header: "Memory",
      cell: (row) => <MemoryCell row={row} range={range} />,
      // A FRACTION of mem_total, not the bytes: the cell draws its stack
      // against that ceiling and the dashed rule says where the ceiling is,
      // so what it shows is how full the host is. Sorting on bytes would
      // order the fleet by how much RAM each machine has, which is the one
      // question this column is not asking. No ceiling means the cell draws
      // nothing at all (see MemoryCell), so there is nothing to order on.
      sortValue: (row) => {
        if (row.mem_total === null) return null;
        const used = latestStackTotal(row.mem);
        return used === null ? null : used / row.mem_total;
      },
    },
    {
      key: "diskTrend",
      header: "Filesystem",
      cell: (row) => <DiskTrendCell row={row} range={range} />,
      // The topmost line's current value -- see latestPeak. Deliberately not
      // the same reading as the Disk column beside it: that one orders by the
      // mount its row NAMES, which is picked by severity before percentage,
      // so the two columns can disagree about which host is worst and each is
      // answering its own question.
      sortValue: (row) => latestPeak(row.disk),
    },
    {
      key: "disk",
      header: "Disk",
      cell: (row) => <DiskCell row={row} />,
      // The percentage of the mount the cell NAMES -- sorting on anything the
      // row does not show would order the list by a number nobody can see.
      // Which is why this is no longer strictly "fullest first": the row
      // picks the mount worth acting on, so a host showing / at 91% with 2 GB
      // left sorts under one showing an array at 92% with 674 GB left. The
      // column is for finding the disks in trouble, and they are still at the
      // top. Sorting on bytes would put the biggest disk first instead, which
      // answers no question at all.
      sortValue: (row) => row.fullest?.pct ?? null,
    },
  ];
}
