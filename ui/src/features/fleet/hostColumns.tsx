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

// No RangePicker/time-range type exists yet elsewhere in the codebase (no
// earlier task in the plan defines one). Defined here, provisionally, for
// the one thing this file actually needs it for: an accurate accessible
// label on each trend chart ("CPU trend, last 1h"). Whichever task wires
// up range selection can widen or relocate this without touching the
// column contract -- Column<HostRow> does not mention Range.
//
// These are relative labels only, never sent to the API as-is: the hub
// rejects relative time strings outright (confirmed against a live
// instance -- `from=-24h` returns `invalid: from must be RFC 3339 or unix
// milliseconds`). Whatever resolves a `Range` into an actual
// `getMetrics()` call must convert it to an absolute RFC 3339 or
// epoch-millis pair first; this file never does that conversion itself,
// it only labels a chart that's already been handed absolute-time series.
export type Range = "1h" | "6h" | "24h";

const RANGE_LABEL: Record<Range, string> = {
  "1h": "last 1h",
  "6h": "last 6h",
  "24h": "last 24h",
};

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
  rx: number[];
  tx: number[];
  fullest: { mount: string; pct: number; others: number };
};

// internal/agent/config/config.go: ScrapeInterval = 60s is the agent's
// fixed report cadence. No server-side "online"/"offline" status field
// exists yet (grepped internal/hub for stale/offline/heartbeat -- nothing
// there computes one), so this mirrors the product's own definition of
// "down" instead of inventing a separate one: the design spec's alerting
// rule is host-down = no POST within 3x the scrape interval. Using
// anything else here (an earlier draft used 2x) would have the fleet list
// call a host offline while the alerting engine still considers it up --
// two views of the same state disagreeing with no way for a user to tell
// which is right.
const SCRAPE_INTERVAL_S = 60;
const STALE_THRESHOLD_S = 3 * SCRAPE_INTERVAL_S;

function hostStatus(
  host: Host,
  now: Date = new Date(),
): { severity: "ok" | "critical"; label: string } {
  if (host.last_seen === null) {
    return { severity: "critical", label: "offline" };
  }
  const ageMs = now.getTime() - new Date(host.last_seen).getTime();
  if (!Number.isFinite(ageMs) || ageMs > STALE_THRESHOLD_S * 1000) {
    return { severity: "critical", label: "offline" };
  }
  return { severity: "ok", label: "online" };
}

function HostCell({ row }: { row: HostRow }) {
  const status = hostStatus(row);
  return (
    <div className="host-cell">
      <Badge severity={status.severity}>{status.label}</Badge>
      <div className="host-cell-name">{row.hostname}</div>
      <div className="host-cell-site">{row.site_name ?? ABSENT}</div>
    </div>
  );
}

function CpuCell({ row, range }: { row: HostRow; range: Range }) {
  return (
    <StackedSparkline
      bands={row.cpu}
      label={`CPU trend, ${RANGE_LABEL[range]}`}
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
      label={`Memory trend, ${RANGE_LABEL[range]}`}
    />
  );
}

function lastValue(values: number[]): number | null {
  return values.length > 0 ? values[values.length - 1]! : null;
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
        label={`Traffic trend, ${RANGE_LABEL[range]}`}
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
  const { mount, pct, others } = row.fullest;
  const label = others > 0 ? `${mount} +${others}` : mount;
  return <Meter value={pct} max={100} label={label} />;
}

// Uptime under 300s is the most interesting row on the page (a host that
// rebooted four minutes ago), so it carries the warning severity -- but
// never colour alone: Badge always pairs its dot with a word, here the
// duration text itself.
const UPTIME_WARNING_THRESHOLD_S = 300;

function UptimeCell({ row }: { row: HostRow }) {
  const text = duration(row.uptime_s);
  if (row.uptime_s !== null && row.uptime_s < UPTIME_WARNING_THRESHOLD_S) {
    return <Badge severity="warning">{text}</Badge>;
  }
  return <>{text}</>;
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
