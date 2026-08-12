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
import { UpDownSparkline } from "../../ui/charts/UpDownSparkline";
import { ABSENT, bitrate, duration } from "../../lib/format";
import type { Host } from "../../lib/api";
import { hostStatus } from "../../lib/host";
import { rangeLabel, type Range } from "../../lib/range";

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
  fullest: { mount: string; pct: number; others: number } | null;
};

function HostCell({ row }: { row: HostRow }) {
  // The CPU series is this host's own recent history, so a host that answers
  // now but keeps dropping scrapes reads as sporadic rather than healthy --
  // the gaps are already visible in its sparkline, and this says the same
  // thing in a word.
  const status = hostStatus(row, undefined, row.cpu[0]?.values);
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
          the eye's scan line down the column. */}
      <div className="host-cell-site">{row.site_name ?? ABSENT}</div>
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
  return (
    <StackedSparkline
      bands={row.cpu}
      max={CPU_PERCENT_MAX}
      label={`CPU trend, ${rangeLabel(range)}`}
    />
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
function MemoryCell({ row, range }: { row: HostRow; range: Range }) {
  if (row.mem_total === null) {
    // No known ceiling means no scale to draw the stack against. Drawing
    // one anyway (e.g. falling back to the bands' own auto-scale) would
    // silently readmit the always-full bug this column is built to avoid,
    // so this renders the absent marker instead of a chart with an
    // invented ceiling -- the same rule Meter itself follows.
    return <>{ABSENT}</>;
  }
  return (
    <StackedSparkline
      bands={row.mem}
      max={row.mem_total}
      label={`Memory trend, ${rangeLabel(range)}`}
    />
  );
}

// The value at the latest bucket, trailing null included -- never the last
// value that happened to be a number. A host that stopped reporting must
// read as absent, not as its final rate frozen in place.
function lastValue(values: (number | null)[]): number | null {
  return values.length > 0 ? (values[values.length - 1] ?? null) : null;
}

// Both rates render in identical type, weighted only by an arrow glyph --
// netra cannot know whether a given host is meant to push or pull more
// traffic, so bolding or upsizing one over the other would assert a
// direction that isn't true. The arrow alone is not an accessible
// distinguisher, so each rate carries its own aria-label naming the
// direction in words.
function TrafficCell({ row, range }: { row: HostRow; range: Range }) {
  const rx = lastValue(row.rx);
  const tx = lastValue(row.tx);
  return (
    <div className="traffic-cell">
      <UpDownSparkline
        up={row.rx}
        down={row.tx}
        label={`Traffic trend, ${rangeLabel(range)}`}
      />
      <div className="traffic-rates">
        <span className="rate" aria-label={`inbound ${bitrate(rx)}`}>
          <span aria-hidden="true">↑</span> {bitrate(rx)}
        </span>
        <span className="rate" aria-label={`outbound ${bitrate(tx)}`}>
          <span aria-hidden="true">↓</span> {bitrate(tx)}
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

// Uptime under 300s is the most interesting row on the page (a host that
// rebooted four minutes ago), so it carries the warning severity -- but
// never colour alone: Badge always pairs its dot with a word, here the
// duration text itself.
const UPTIME_WARNING_THRESHOLD_S = 300;

function UptimeCell({ row }: { row: HostRow }) {
  const text = duration(row.uptime_s);
  if (row.uptime_s !== null && row.uptime_s < UPTIME_WARNING_THRESHOLD_S) {
    // "rebooted", not the duration alone. Badge's dot is aria-hidden, so a
    // screen reader hearing "1 m 40 s" cannot tell this row from a healthy
    // host's "266 d 6 h", and a deuteranope sees only a hue change -- the
    // state would ride on colour alone, which is precisely what pairing a
    // dot with a WORD is supposed to prevent. A duration is not a severity.
    return <Badge severity="warning">rebooted {text} ago</Badge>;
  }
  // Same type as the traffic cell's rates, in muted ink. Uptime inherited
  // the table body's own size and full-strength colour, which made
  // "37 d 6 h" the loudest thing in a row whose point is the charts.
  return <span className="uptime-cell">{text}</span>;
}

export function hostColumns(range: Range): Column<HostRow>[] {
  return [
    { key: "host", header: "Host", cell: (row) => <HostCell row={row} /> },
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
      key: "traffic",
      header: "Traffic",
      cell: (row) => <TrafficCell row={row} range={range} />,
    },
    { key: "disk", header: "Disk", cell: (row) => <DiskCell row={row} /> },
    {
      key: "uptime",
      header: "Uptime",
      cell: (row) => <UptimeCell row={row} />,
    },
  ];
}
