// The single host column definition. `HostTable` renders the exact
// Column<HostRow>[] this file produces and owns no idea of its own about
// "what a host row looks like", so a column added here is a column the fleet
// shows; there is no second place one could go missing from.
//
// `width`/`align` (Table-only, see ui/Table.tsx) are deliberately never
// set below: this file has no opinion on layout, only on content.
import type { Column } from "../../ui/Table";
import { SeverityMark, type MarkKind } from "../../ui/SeverityMark";
import { When } from "../../ui/When";
import { NowReading } from "../../ui/NowReading";
import { OsIcon } from "../../ui/OsIcon";
import type { Band } from "../../ui/charts/StackedSparkline";
import { Sparkline } from "../../ui/charts/Sparkline";
import { severityFromPercent, trendColor } from "../../ui/Meter";
import {
  DETAIL_WIDTH,
  METRIC_CELL_GAP,
  METRIC_CELL_STYLE,
  SPARK_STRIP_HEIGHT,
  SPARK_WIDTH,
} from "../../ui/charts/size";
import {
  DOWN_COLOR,
  UP_COLOR,
  UpDownSparkline,
} from "../../ui/charts/UpDownSparkline";
import { binaryBytes, byterate, bytes, percent } from "../../lib/format";
import type { Drive, Host } from "../../lib/api";
import { hostStatus, isReporting, type HostStatus } from "../../lib/host";
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
 * fullest-filesystem summary (never a per-filesystem list -- the Filesystem
 * column shows the one mount that matters and says nothing about the rest,
 * see that column below).
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
 * hue (spec: no hardcoded colour in a .tsx file). Neither stack is drawn in
 * the ROW any more: the cells draw one silhouette each, `reporting` for CPU
 * and `memUsed` for Memory, and the bands are what the ENLARGED view opens
 * on. `mem_total` (bytes, see lib/api.ts's Host type) is the chart's scale
 * ceiling for Memory, not an extra band -- see the Memory column below for
 * why.
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
  /** mem_used over the window -- the Memory cell's silhouette, and the same
   * quantity the percentage beside it is a gauge of. See HostTrends.memUsed
   * for why it is not the top of the `mem` stack. Optional for the same
   * reason `rxPeak` is: a row assembled without it draws an empty chart
   * rather than failing to compile. */
  memUsed?: (number | null)[];
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
  //
  // `since` and `sinceAtLeast` used to be here, walked back through this
  // mount's own series on every render. The hub does that walk once, when the
  // condition opens and while the raw samples still exist, and the answer
  // reaches the page on the condition row instead -- see Condition.since. A
  // walk bounded by the range picker answered differently on every range and
  // changed shape as the data aged through tiers.
  fullest: {
    mount: string;
    pct: number;
    // Bytes left on THIS mount. The percentage cannot decide on its own
    // whether the mount is worth surfacing: 90% of a 6.7 TB array is 674 GB
    // free. Null is "not known", which falls back to the percentage alone.
    free?: number | null;
    /** THIS mount's Use% over the window, one value per bucket, gaps kept as
     * null -- the line the Disk cell draws over its bar. Optional because the
     * hand-built row literals across the tests predate it: a row without it
     * draws the bar alone, exactly as the cell did before the line existed.
     *
     * Empty on a host that has been off longer than the window is wide: the
     * reading survives that, because it comes from the stored gauge rather
     * than from the window, and the cell draws it with no line above it. */
    series?: (number | null)[];
    /**
     * When this reading was taken, from the hub's stored gauge. Null when the
     * figure came from the window instead.
     *
     * The cell does not print it: at 150px the mount and what is left are
     * what a reader needs, and the row already says how long the host has
     * been quiet in its status chip and in its "stopped reporting" condition.
     * It is here so the disk condition can say "was 96 % full" rather than
     * "is", and so a test can tell the two sources apart.
     */
    asOf?: string | null;
  } | null;
  /**
   * The host's physical drives with their latest SMART attributes, for the
   * drive condition in conditions.ts -- no cell draws them.
   *
   * OPTIONAL, and undefined means the fleet-wide /api/v1/drives call has not
   * answered or failed, which is not the same as a host with no drives (that
   * is `[]`). hostConditions leans on the distinction: only one of the two may
   * read as nothing wrong, and it is not the one where netra never looked.
   */
  drives?: Drive[];
  /** Every filesystem's Use% over the window, one band each.
   *
   * Not what the Disk cell draws. The list answers "is this one filling up"
   * with a single line for the mount the cell already names -- `fullest.series`
   * above -- because six lines inside a 26px strip are a texture, not a
   * reading. All of them together are one click away, on the host page's
   * Filesystem usage panel (chartSpecs.ts). Still assembled: it costs no extra
   * fetch, `fullest` reads the same response, and the attention band reads
   * these. */
  disk: Band[];
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

/**
 * The same line, broken for the fleet table: the place on one line and
 * whatever followed its last comma on the next.
 *
 * Only the fleet list wraps it. The host page has one host to describe and
 * the room to say it in a sentence, whereas the table repeats this under
 * every hostname in the widest text column of the row -- so
 * "OVH · Roubaix, France" was the string deciding how wide the Host column
 * had to be, and it ellipsised away on the fleets where it lost that
 * argument.
 *
 * The LAST comma, not the first: "Roubaix, Hauts-de-France, France" has to
 * put France under the rest, and splitting at the first comma would have put
 * the region on the country's line. The comma itself goes -- the break has
 * already done the separating, and a comma at the end of a line is
 * punctuation with nothing after it.
 *
 * Everything else is still printed verbatim. A location with no comma
 * ("basement", "AWS eu-west-1a") stays one line, and so does one whose comma
 * has nothing after it: the split has to be an improvement on the string the
 * operator typed, or not happen at all.
 */
export function hostLocationLines(row: {
  provider?: string | null;
  location?: string | null;
}): { head: string; tail: string | null } | null {
  const provider = reported(row.provider);
  const location = reported(row.location);
  if (provider === null && location === null) return null;

  let place = location;
  let tail: string | null = null;
  if (location !== null) {
    const comma = location.lastIndexOf(",");
    const before = comma === -1 ? "" : location.slice(0, comma).trim();
    const after = comma === -1 ? "" : location.slice(comma + 1).trim();
    // Both halves have to survive the trim, not just be there: a location of
    // " , France" has a comma with an index above zero and something after
    // it, and splitting it would open the cell with a blank line -- or, with
    // a provider set, with "OVH · " and a separator pointing at nothing.
    if (before !== "" && after !== "") {
      place = before;
      tail = after;
    }
  }

  return {
    head: [provider, place].filter((part) => part !== null).join(" · "),
    tail,
  };
}

// Same truthy-string test hostLocation makes, and for the same reason: both
// fields are optional on Host, so a row built before they existed has to draw
// a hostname with no location rather than throw.
function reported(value: string | null | undefined): string | null {
  return typeof value === "string" && value !== "" ? value : null;
}

/**
 * What the one mark beside a hostname says, or null when the row has nothing
 * to report.
 *
 * ONE mark, never two. A row that showed "sporadic" and "critical" side by
 * side spent the identity column on two of them and left the reader to rank
 * them -- and the ranking is not theirs to do: it is the same worst-first
 * rule the counts line above the table already applies.
 *
 * It returns a KIND and a word, and only the kind is drawn. The word used to
 * be the whole point -- "critical", the way the Events tab writes it -- and
 * what killed it on screen is that it was the same word on every row that had
 * one, set in the narrowest column of the table, four columns to the left of
 * the cells that say WHICH thing is critical. It survives as the mark's
 * accessible name, so the specific fact ("never seen", "sporadic") still
 * reaches a screen reader while the page stops repeating it.
 *
 * The order below is the ranking rule, and each step earns its place:
 *
 *   offline / never seen -- its own mark, the bare X, rather than the cross
 *     in an octagon the next branch draws. A machine nobody has heard from
 *     has stale figures for everything else, so a "critical" derived from its
 *     last known disk reading would claim to describe this minute; the two
 *     facts are different and they get different marks rather than one of
 *     them borrowing the other's. It outranks everything, including
 *     conditions that are still true of the machine: the row's Filesystem
 *     cell still says "was 96 % full", and the host page holds the rest.
 *
 *     It briefly drew NOTHING here, on the argument that the red hostname and
 *     the Last seen column say it twice already. They do, and it was still
 *     wrong: severity may not ride on colour alone, an age is not a severity,
 *     and the counts line above the table goes on counting a silent host as
 *     critical -- so "Critical 3" pressed above three rows, one of which
 *     carried no mark at all.
 *   critical -- something on a host that IS reporting needs acting on now.
 *   sporadic -- a host that answers but keeps dropping scrapes. It is the
 *     same severity as the generic warning below and gets the same mark; what
 *     keeps it from being flattened into "warning" is its word, which is now
 *     the accessible name, and the Last seen column ticking past a scrape
 *     interval while the row keeps drawing figures. Judged by the HUB, over a
 *     fixed window -- counting it here off row.reporting made the mark a fact
 *     about the reader's range picker as much as about the host.
 *   warning
 */
function hostMark(
  status: HostStatus,
  worst: "warning" | "critical" | null,
  sporadic: boolean,
): { kind: MarkKind; label: string } | null {
  if (status.severity === "critical") {
    return { kind: "offline", label: status.label };
  }
  if (worst === "critical") return { kind: "critical", label: "critical" };
  if (sporadic) return { kind: "warning", label: "sporadic" };
  if (status.severity === "warning") {
    return { kind: "warning", label: status.label };
  }
  if (worst === "warning") return { kind: "warning", label: "warning" };
  return null;
}

function HostCell({
  row,
  worst,
  sporadic,
  now,
}: {
  row: HostRow;
  /** The instant the page is reading itself at. undefined is the wall clock,
   * which is what a browser wants and what every caller that derives no
   * conditions of its own gets. */
  now?: Date;
  /**
   * The worst severity anything on this host is at -- read through the very
   * function that draws the row's rail, so the mark and the rail can never
   * disagree about the same host.
   *
   * undefined is a caller that derives no conditions at all, and leaves the
   * row with only its reporting status to say. That is not a degraded mode to
   * design around: it is the honest reading when nobody has looked.
   */
  worst?: (row: HostRow) => "warning" | "critical" | null;
  /** Whether the hub raised `sporadic` on this host. Read through the same
   * conditions `worst` is, so the mark's shape and its colour cannot come from
   * two different answers. */
  sporadic?: (row: HostRow) => boolean;
}) {
  // Judged from row.reporting -- cpu_total, from the `host` family -- and
  // never from row.cpu[0], which is a per-core band under 32 threads and the
  // cpu_total fallback above it. Reading cpu[0] judged one host against
  // cpu_core and its neighbour against host_samples: two relations with
  // different materialisation lags, so the badge meant a different thing on
  // adjacent rows of the same page. A host that answers now but keeps
  // dropping scrapes reads as sporadic rather than healthy; the gaps are
  // already visible in its sparkline, and this says the same thing in a word.
  //
  // `now` comes from the page rather than being read off the wall clock here,
  // and that matters now that the mark is the row's only severity mark: the
  // conditions this cell's `worst` is derived from are judged against the
  // page's clock, so a cell reading its own would let the two halves of one
  // mark disagree about whether a host is still reporting. They are the same
  // instant in a browser; they are not in a test, and they are not for a
  // caller that supplies its own.
  const status = hostStatus(row, now);
  // The location, and NOT the OS name beside it. The mark says which
  // distribution at a glance; spelling out "Debian GNU/Linux 12 (bookworm)"
  // under every hostname said it a second time in the row's longest string,
  // and os-release runs long enough that it had to be truncated to stay on
  // one line. The host page names the release in full.
  const location = hostLocationLines(row);
  // Null on almost every row -- healthy is the majority state. See hostMark
  // for which of a host's several truths it picks, and why only one.
  const mark = hostMark(status, worst?.(row) ?? null, sporadic?.(row) ?? false);
  return (
    // Its own wrapper rather than .host-cell itself: the container list's
    // name cell is built from .host-cell too, and it has no mark to seat, so
    // the row layout the mark needs cannot live on the shared class.
    <div className="fleet-host">
      {/* The distribution mark labels the whole host, so it sits beside both
          lines and is centred against them rather than riding the name -- the
          arrangement Observium uses. OsIcon renders nothing for an OS it has
          no mark for, so an unknown distro leaves the space empty instead of
          taking a placeholder. */}
      <OsIcon name={row.os_name ?? null} />
      <div className="host-cell">
        <div className="host-cell-top">
          {/* The hostname is the way into the host page, and it is an anchor
            rather than a row click handler: middle-click, copy-link and
            bookmark all have to work, and a row-wide handler would swallow
            the text selection someone needs to read an id off the screen.
            The fleet list had no link into detail at all. */}
          {/* `gone` only when the host is not reporting -- see
            .host-cell-name.gone. The name is the last thing left to mark on a
            row whose figures have all gone absent; every other row's name is
            plain ink, so the colour marks the exception instead of every
            line in the column. */}
          <a
            className={`host-cell-name${
              status.severity === "critical" ? " gone" : ""
            }`}
            href={`/hosts/${row.id}/overview`}
          >
            {row.hostname}
          </a>
          {/* After the name, and only when there is something to say. Healthy is
          the overwhelming majority state, so a badge on every row spent the
          eye's first stop -- and the leftmost column -- on the word "online"
          repeated down the page. What a reader scans for is the exception,
          which is now the only thing marked. Which exception it marks when a
          host has more than one, and why it marks only one: see hostMark.

          A mark rather than a badge, since the badge that survived that cut
          was still a variable-width object carrying a word the reader had
          already learned by the third row. SeverityMark is one glyph wide on
          every row, so the column ranks itself by which rows have one --
          and the shape, not the hue, is what separates the two severities.
          See SeverityMark on why that is not a style choice. */}
          {mark !== null && (
            <SeverityMark kind={mark.kind} label={mark.label} />
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

          The country goes on its own line, and the two lines are set tight
          against the name to pay for the height -- see the .fleet-host rules
          in index.css. On one line "OVH · Roubaix, France" ellipsised away on
          any fleet whose Host column was not the widest thing on the page,
          which lost the country on exactly the rows that reported one. */}
        {location !== null && (
          <>
            <div className="host-cell-site">{location.head}</div>
            {location.tail !== null && (
              <div className="host-cell-site">{location.tail}</div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

// The silhouette is scaled to 100, never to its own peak. Without a ceiling
// Sparkline auto-scales each host to its own extent, so a host idling at 3%
// and one saturated at 90% draw the identical silhouette touching the top of
// the box -- the rows stop being comparable, which is what a fleet list is
// for. It is the same always-full reading MemoryCell below carries its own
// ceiling to avoid; the spec's silhouette is cpu_total, not cpu_total
// normalised to itself.

const CPU_PERCENT_MAX = 100;

const DISK_CELL_STYLE = {
  width: SPARK_WIDTH,
  paddingTop: SPARK_STRIP_HEIGHT + METRIC_CELL_GAP,
};

// One line and a light fill. The row used to draw the per-core stack here --
// up to 32 bands in four cycling blues, a hairline between each, inside 45px.
// What a fleet glance reads off this cell is whether the top edge moved, and
// the stripes buried exactly that under a texture whose hues meant "core
// index". The per-core stack is still what the ENLARGED view opens on: it has
// the room, and "which core is pinned" is the question someone opens it to
// ask.
//
// The colour is the cell's own severity, not --cpu-1; see trendColor().

function CpuCell({ row, range }: { row: HostRow; range: Range }) {
  // The right-hand edge of the silhouette the cell draws, which is also what
  // this column sorts on: the number the cell prints, the shape beside it and
  // the order the column puts its rows in cannot disagree. cpu_total is
  // already percent of THIS host, so a 32-core box does not outrank a busy
  // 4-core one.
  const busy = isReporting(row) ? lastReported(row.reporting) : null;
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

  const chart = (
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
      <Sparkline
        values={row.reporting}
        // min={0} as well as the ceiling: Sparkline auto-scales BOTH ends to
        // the data, and a floor at the series' own minimum would draw a host
        // steady at 40% as a flat line along the bottom of the box.
        min={0}
        max={CPU_PERCENT_MAX}
        color={trendColor(busy)}
        // Shorter than the traffic chart beside it: the now-bar and its unit
        // line sit underneath and take the rest of the row's height.
        height={SPARK_STRIP_HEIGHT}
        label={`CPU trend, ${rangeLabel(range)}`}
      />
    </Enlargeable>
  );

  return (
    <div className="metric-cell" style={METRIC_CELL_STYLE}>
      {chart}
      {busy !== null && (
        <NowReading
          pct={busy}
          label="CPU now"
          under={
            row.threads === null
              ? undefined
              : `of ${row.threads} core${row.threads === 1 ? "" : "s"}`
          }
        />
      )}
    </div>
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
// Scaled to mem_total, the height of the silhouette IS how full the host is.
// The dashed total rule this headroom was originally for is gone from the
// row -- five bands, a rule and a severity mark inside 45px was more marks
// than a cell that exists to be scanned can carry. The headroom stays:
// without it a nearly-full host draws flush against the border and reads as
// clipped rather than as full. The ENLARGED chart and the host page's Memory
// panel still draw the rule, where there is room for it to read as a
// ceiling.
const MEM_HEADROOM = 1.08;

// The row draws mem_used as one silhouette, in its own severity colour (see
// trendColor), and NOT the
// five-band stack any more. The stack's top edge is "not free" -- used,
// shared, ARC, buffers, cached -- which on a Linux host that caches
// everything is nearly the whole box, so every row was a near-full brick
// beside a figure saying 30%: the shape and the number disagreed about the
// same machine, and a reader could not say which to believe. The silhouette
// is the quantity the figure is a gauge of, so its height and the percentage
// beside it are one reading. The stack is still what the ENLARGED view opens
// on, with the bands named -- "which part of memory is growing" is the
// question someone opens it to ask.

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
  // mem_used, NOT the height of the stack beside it. The stack partitions
  // mem_total into everything that is not free -- used, shmem, ARC, buffers,
  // cached -- so its top edge is "not free", and a host doing nothing but
  // serving files reads 97% while most of its memory is available. mem_used
  // is MemTotal - MemAvailable (internal/agent/collector/memory.go), which is
  // what `free` calls used and what the host page's own Memory tile prints
  // (overviewTiles.ts): a fleet row and a host page must not disagree about
  // the same machine.
  //
  // A gauge off host_current rather than the end of the series, for the same
  // reason the traffic rates are: the series' last bucket depends on the
  // range, because the range picks the step and the step picks the storage
  // tier. Same isReporting guard, so a host that stopped talking prints
  // nothing rather than its last percentage.
  const used = isReporting(row) ? row.mem_used : null;
  const fetchSeries = async (next: Range): Promise<DetailData> => {
    const host = await fetchHostFamily(row.id, "host", next);
    return { series: memoryBands(host), window: host.window };
  };

  const chart = (
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
      <Sparkline
        values={row.memUsed ?? []}
        // Scaled from zero to mem_total, with a little headroom so a
        // nearly-full host does not draw flush against the border and read
        // as clipped. Both ends pinned: Sparkline auto-scales to the data
        // otherwise, which is the always-full reading this cell exists to
        // avoid.
        min={0}
        max={total * MEM_HEADROOM}
        color={trendColor(used === null ? null : (used / total) * 100)}
        height={SPARK_STRIP_HEIGHT}
        // No legend, like every other cell in this row. A previous review
        // argued the five memory bands carry identity a legend should name and
        // turned it back on here; that is not the call. These are sparklines
        // in a dense list -- the shape is the message. The ENLARGED view
        // names the bands: it has the room.
        label={`Memory trend, ${rangeLabel(range)}`}
      />
    </Enlargeable>
  );

  return (
    <div className="metric-cell" style={METRIC_CELL_STYLE}>
      {chart}
      {used !== null && (
        <NowReading
          pct={(used / total) * 100}
          label="Memory now"
          // binaryBytes, like the enlarged chart's own axis: a stack read
          // against a binary ceiling and labelled decimally underneath makes
          // one quantity look like two.
          under={`of ${binaryBytes(total)}`}
        />
      )}
    </div>
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
        {/* --in-1/--out-1, the sparkline's own defaults and the hues the
            enlarged chart names its bands in. The cell was briefly two steps
            of grey, to stop it being the one saturated mark in a row of
            neutral silhouettes; the three beside it carry severity colour
            again, so the odd one out now is the cell with no severity to
            carry. Hue is what separates in from out here -- a rate has no
            ceiling, so there is no threshold for it to mean instead, and
            mirrored around a midline two greys fuse into one shape.

            Those two hues ARE the status green and amber now (index.css owns
            the argument). Nothing about this cell means severity: the green
            is not "ok" and the amber is not "warning", they are inbound and
            outbound bytes. The rail, the dot and the word all sit left of
            here and are the only marks on the row that carry state. */}
        <UpDownSparkline
          up={row.rx}
          down={row.tx}
          // The peak envelope, which the row has carried since trafficSeries
          // started computing it and this cell alone dropped on the floor.
          //
          // The cell is fixed at 24h, which resolves to the 5m tier, so every
          // point it draws is a five-minute average: a saturation that lasted
          // three minutes reached the mean at a fifth of its height and the
          // pixel fold halved it again. It was invisible here and visible on
          // the 1h chart, which is the one range that reads raw -- reported as
          // "the sparkline never shows it". The dialog this cell opens into
          // already drew the envelope, so the cell was also disagreeing with
          // its own enlarged view.
          upBand={row.rxPeak}
          downBand={row.txPeak}
          // No weight override: a mirror fills solid at this density and
          // that is the mark. It drew as a line over a light fill for a
          // while, to read as one of a row of silhouettes, and what that
          // cost is the reading -- a translucent wash where rrdtool, and
          // every traffic graph an operator has already read, draws a filled
          // area. The cell is allowed to be the one mass in the row: it is
          // the one column whose two halves are a comparison rather than a
          // level against a threshold.
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
      {/* The words, not arrows. This app says "in" and "out" everywhere else
          it names the two directions -- the fleet's own traffic figure reads
          "in + out", and Graphs.tsx names its bands the same -- so a pair of
          arrows was the one place a reader had to translate. The aria-labels
          keep saying inbound and outbound, which is what a screen reader
          needs and what an arrow never said to one anyway. */}
      <div className="traffic-rates">
        {rx === null ? null : (
          <span className="rate" aria-label={`inbound ${byterate(rx)}`}>
            <i aria-hidden="true">in</i> {byterate(rx)}
          </span>
        )}
        {tx === null ? null : (
          <span className="rate" aria-label={`outbound ${byterate(tx)}`}>
            <i aria-hidden="true">out</i> {byterate(tx)}
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
// The hue the ENLARGED disk chart and its range rail open in: the colour the
// host page's busiest-filesystem tile already defaults to, so a mount looks
// the same wherever it is drawn large. The cell itself takes the mount's
// severity like the two silhouettes beside it (see trendColor) -- the dialog
// is where a filesystem gets a colour of its own, because there it is a chart
// someone opened to read rather than a mark scanned down a column.
const DISK_COLOR = "var(--s6)";

// The narrowest window the disk line is ever drawn against, in percentage
// points.
//
// The axis is fitted rather than pinned to 0-100 -- that is the whole point
// of the line, since five points of growth over a day is 1.3px inside a 26px
// strip -- but a fitted axis with nothing to fit magnifies whatever is left:
// a mount that did not move all day would draw its own rounding as a
// mountain range. Below two points the extent is widened to two, so a flat
// disk draws flat.
const DISK_MIN_SPAN = 2;

/**
 * The floor and ceiling for one mount's Use% line: its own extent, widened to
 * DISK_MIN_SPAN and slid back inside 0-100 if that pushed it out.
 *
 * null when the series has no reading at all -- the caller then draws no
 * chart rather than an empty box, the same answer every other cell here gives
 * for a reading it does not have.
 */
export function diskAxis(
  values: readonly (number | null)[],
): { min: number; max: number } | null {
  let lo = Infinity;
  let hi = -Infinity;
  for (const v of values) {
    if (v === null) continue;
    if (v < lo) lo = v;
    if (v > hi) hi = v;
  }
  if (lo === Infinity) return null;
  const grow = Math.max(0, (DISK_MIN_SPAN - (hi - lo)) / 2);
  let min = lo - grow;
  let max = hi + grow;
  // Slid, not clipped: a root at 0.4% and one at 99.8% both keep the full
  // span, which is what makes their lines the same steepness for the same
  // growth. Only a disk whose own extent is already wider than the window
  // can lose any of it, and that one never needed the floor.
  if (min < 0) {
    max = Math.min(100, max - min);
    min = 0;
  } else if (max > 100) {
    min = Math.max(0, min - (max - 100));
    max = 100;
  }
  return { min, max };
}

function DiskCell({ row, range }: { row: HostRow; range: Range }) {
  if (row.fullest === null) {
    // A host that has reported no filesystems has no fullest one. Drawing a
    // meter anyway would put an empty green bar where "never collected"
    // belongs, which reads as a fact rather than as an absence -- and a dash
    // is the same mistake one step quieter. See MemoryCell.
    //
    // This used to catch a second, much commoner case that did not belong in
    // it: a host that had simply STOPPED reporting. The reading came off the
    // last slot of the answered window, so a machine switched off for the day
    // had no fullest mount either, and the whole cell went with it -- chart
    // included, while CPU and Memory beside it kept drawing their history and
    // dropped only their now-bar. Disk fullness does not change while a
    // machine is off, so there was a true reading to show and the row showed
    // nothing. fullestFilesystem reads the hub's stored gauge now and the case
    // no longer reaches here.
    return null;
  }
  const { mount, pct, free, series } = row.fullest;
  const values = series ?? [];
  const axis = diskAxis(values);

  // The same ONE mount at another range, never the whole host's filesystems:
  // enlarging has to show more of the shape that was clicked, not a different
  // chart. The all-mounts view is a real view and it has a home -- the host
  // page's Filesystem usage panel -- but opening it from here would answer a
  // question the reader did not ask, the way an unnormalised per-core stack
  // would in the CPU cell.
  const fetchSeries = async (next: Range): Promise<DetailData> => {
    const fs = await fetchHostFamily(row.id, "filesystem", next);
    const bands = filesystemBands(fs);
    // By name here, unlike the assembler: at another range this is a fresh
    // response whose series order is the hub's, so the index that won over
    // the page's window means nothing against it. A mount that has stopped
    // reporting in the wider window is simply absent, and the dialog then
    // draws nothing rather than another disk's line under this one's title.
    const one = bands.find((band) => band.name === mount);
    // Recoloured, because filesystemBands colours by POSITION -- FS_COLORS
    // cycling over the mounts that reported -- and this cell draws one mount
    // in DISK_COLOR. Taken as it comes, the dialog and its rail would open in
    // whatever hue this mount happened to be nth of, beside a figure in
    // another, and picking a range would change the line's colour.
    return {
      series: one === undefined ? [] : [{ ...one, color: DISK_COLOR }],
      window: fs.window,
    };
  };

  // One silhouette for the mount named underneath, filled, like every other
  // sparkline in the app.
  //
  // It was briefly a bare line, on the argument Sparkline's `fill` docstring
  // used to make: filesystem usage sits between 40% and 95%, so shading under
  // it floods the box. That is true of an axis anchored at ZERO and this axis
  // is not -- diskAxis fits it to the window, and areaPath closes the fill at
  // the bottom of the BOX (geometry.ts), so the shaded mass tracks where the
  // line sits inside its own window exactly as CPU's and Memory's do. An
  // unfilled line here was the one sparkline in the app drawn differently
  // from the rest, for a reason that had stopped applying.
  const chart =
    axis === null ? null : (
      <Enlargeable
        title={`Filesystem · ${mount} · ${row.hostname}`}
        label={`Enlarge filesystem usage for ${mount} on ${row.hostname}`}
        className="inline"
        unit="%"
        series={[{ name: mount, color: DISK_COLOR, values }]}
        // Not the cell's frozen min/max: those are THIS window's extent, and
        // the picker can widen the window -- a floor held from the old one
        // clips every older bucket below it off the bottom of the plot, which
        // is the half fitted() cannot fix (it raises a ceiling, never lowers
        // a floor).
        //
        // So the dialog free-scales, and it does NOT carry DISK_MIN_SPAN:
        // the two are not the same rule and should not be. The span floor
        // exists because a 26px strip has no axis -- a flat mount magnified
        // to full height there is read as movement and there is nothing on
        // screen to say otherwise. The dialog has a labelled axis: a mount
        // between 41.9 and 42.1 draws its wobble large and says 41.9 and 42.1
        // beside it, which is the truth at a zoom the reader asked for.
        autoScale
        // Filled, because the cell it opens from is: markFor() reads an
        // absent `filled` as "line", so without this the dialog and its rail
        // tiles draw a bare stroke where the reader clicked a shaded
        // silhouette -- the one thing enlarging must never do. Honest for
        // the same reason Inventory's drive-temperature dialog passes it: the
        // chart free-scales, so the fill's bottom edge is the window's own
        // floor rather than an axis decision.
        filled
        fmt={(n) => percent(n)}
        window={row.window}
        range={range}
        ranges={RAIL_RANGES}
        fetchSeries={fetchSeries}
      >
        <Sparkline
          values={values}
          min={axis.min}
          max={axis.max}
          color={trendColor(pct)}
          height={SPARK_STRIP_HEIGHT}
          label={`Filesystem trend for ${mount}, ${rangeLabel(range)}`}
        />
      </Enlargeable>
    );

  // The same now-bar the CPU and Memory cells draw under their sparklines,
  // and now under one of its own. Drawing it with the shared NowReading
  // rather than Meter is what makes the three saturation columns read alike
  // -- the same ten cells, the same figure in the same severity colour, the
  // same quiet line underneath. The mount and what is left go on that line:
  // the mount is which disk the reading is about, and bytes left is what the
  // percentage cannot say on its own (90% of a 6.7 TB array is 674 GB free,
  // 90% of a 4 GB root is not). Null free is "not known" -- the assembler
  // leaves it unset when the host reported no size -- and prints nothing
  // rather than a dash.
  return (
    <div
      className="metric-cell disk-cell"
      style={axis === null ? DISK_CELL_STYLE : METRIC_CELL_STYLE}
    >
      {chart}
      <NowReading
        pct={pct}
        label={`Filesystem ${mount}`}
        // No severity passed: NowReading's own 70/95, the rule every
        // reading in this table is drawn by. The bytes underneath say how
        // much room is left; the bar says how full the mount is. What decides
        // whether it is worth acting on is hostConditions(), which still
        // judges the same mount on the compound rule and keeps a big volume
        // with headroom out of the attention counts.
        under={
          <>
            <span className="dmount">{mount}</span>
            {free == null ? null : (
              <span className="dfree">{bytes(free)} left</span>
            )}
          </>
        }
      />
    </div>
  );
}

/**
 * How long ago the hub last heard from this host.
 *
 * The column exists because the fleet stopped saying it in words. "offline"
 * used to sit beside the hostname and answer a question it could not
 * actually answer -- offline since when? A minute past the threshold and
 * four days dead drew the identical chip, and the difference between those
 * two is the whole of what a reader wants at that moment. An age answers it
 * and needs no threshold to do so, which is also why it is on EVERY row and
 * not only the silent ones: a healthy fleet reading "18 s ago" down the
 * column is the fleet telling you the scrape loop is alive, and a row that
 * has drifted to "4 m ago" is visible here before any threshold has fired
 * and before the name goes red.
 *
 * `When` does the formatting, and it is the same `When` the container list's
 * own Last seen column renders. Its header says why: three tables now print a
 * seen-at column, and a second copy is how two columns headed the same thing
 * come to format differently. This cell adds the type (.seen-cell) and the
 * one case the container list does not have.
 *
 * That case is "never", and it is a WORD rather than the absent dash. A
 * container row always has a last_seen; a host record can exist having never
 * reported once, and every figure on that row is absent for the ordinary
 * reason that there is nothing to draw. A dash here joins them and reads as
 * "this cell has no value", which is true of four other cells on the same row
 * and is not the fact. "never" is the fact, and it is the word hostStatus
 * already uses.
 *
 * `now` is the page's clock, the same instant hostMark judges the row's
 * severity against. A cell reading its own would let one row's name go red
 * against a different millisecond than the one its Last seen was measured
 * from; identical in a browser, not in a test.
 */
function LastSeenCell({ row, now }: { row: HostRow; now?: Date }) {
  return (
    <span className="seen-cell">
      {row.last_seen === null ? (
        <span className="absent">never</span>
      ) : (
        <When iso={row.last_seen} now={now} />
      )}
    </span>
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

export function hostColumns(
  range: Range,
  /** See HostCell's own `worst`. Passed straight through: which severity a
   * host is at is the PAGE's to derive -- it owns the conditions -- and this
   * module's only to draw. */
  worst?: (row: HostRow) => "warning" | "critical" | null,
  /** See HostCell's own `sporadic`. */
  sporadic?: (row: HostRow) => boolean,
  /** See HostCell's own `now` -- the clock `worst` was derived against. */
  now?: Date,
): Column<HostRow>[] {
  return [
    {
      key: "host",
      header: "Host",
      cell: (row) => (
        <HostCell row={row} worst={worst} sporadic={sporadic} now={now} />
      ),
      sortValue: (row) => row.hostname,
    },
    {
      key: "cpu",
      header: "CPU",
      cell: (row) => <CpuCell row={row} range={range} />,
      // Where the silhouette ends -- the figure the cell prints, read
      // through the same guard the cell reads it through, so a host whose
      // last sample is hours old sorts as unknown rather than as busy.
      // Without that a machine that died at 90% sits above every live host
      // in the fleet while printing no figure at all, which is exactly what
      // the traffic column's own guard is there to prevent.
      // cpu_total is percent of THIS host, so a 32-core box does not outrank
      // a busy 4-core one.
      sortValue: (row) =>
        isReporting(row) ? lastReported(row.reporting) : null,
    },
    {
      key: "memory",
      header: "Memory",
      cell: (row) => <MemoryCell row={row} range={range} />,
      // The FRACTION the cell prints, off the same gauge: mem_used over
      // mem_total. Ordering on the stack's own height ranked the fleet by how
      // little memory was FREE rather than by how much was in use, so a file
      // server with a big page cache outranked a host actually short of
      // memory -- and the column disagreed with the figure printed in it.
      // Sorting on bytes would order the fleet by how much RAM each machine
      // has, which is the one question this column is not asking. No ceiling
      // means the cell draws nothing at all (see MemoryCell), so there is
      // nothing to order on.
      sortValue: (row) => {
        if (row.mem_total === null || !isReporting(row)) return null;
        return row.mem_used === null ? null : row.mem_used / row.mem_total;
      },
    },
    {
      key: "disk",
      // "Filesystem", not "Disk": the app draws that line already -- the host
      // page's Storage tab groups its charts as Filesystems (usage, space,
      // inodes) and Disks (throughput, operations), so "Disk" here named the
      // drive while the cell reports a mount. The attention band directly
      // above this column says "Filesystem nearly full" and the chart this
      // cell opens is titled "Filesystem usage", which is also what Observium
      // calls it. Singular: the cell reports the ONE mount worth acting on.
      header: "Filesystem",
      cell: (row) => <DiskCell row={row} range={range} />,
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
    // After the gauges. It sat second, on the argument that traffic is what a
    // fleet list is most often scanned for; the three columns after it are
    // the ones with a bar and a threshold, so scanning for what needs acting
    // on had to step over a rate that has neither. CPU, memory and disk now
    // run as one uninterrupted block of gauges and traffic reads as the
    // context beside them.
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
    // Last, at the right end of the row, and the only column after Traffic.
    // It is the row's quietest fact: on a healthy fleet every line of it says
    // the same thing, seconds, and nobody reads it until something else has
    // already drawn the eye. Put beside the hostname it explains it would
    // have been the second thing scanned on every row to answer a question
    // asked on almost none of them, and it would have split the identity from
    // the block of gauges the list exists to be scanned by.
    {
      key: "seen",
      header: "Last seen",
      cell: (row) => <LastSeenCell row={row} now={now} />,
      // The INSTANT, the way the container list's Last seen column sorts the
      // same field. It sorted the raw ISO string for one commit, on the claim
      // that every last_seen is UTC so lexicographic order is chronological
      // order -- nothing enforces that claim: read/host.go marshals a
      // *time.Time with whatever offset the driver hands back, and one row
      // arriving at +02:00 would sort into the wrong place through Table's
      // string branch. Two spellings of one column is also how the two drift.
      //
      // Null and an unparseable date both go to the unknown group (see
      // Column.sortValue), which is where a host nobody has ever heard from
      // belongs: it is not the longest-silent host on the page, and flipping
      // the arrow must not promote it to the top.
      sortValue: (row) => {
        if (row.last_seen === null) return null;
        const ms = Date.parse(row.last_seen);
        return Number.isNaN(ms) ? null : ms;
      },
    },
  ];
}
