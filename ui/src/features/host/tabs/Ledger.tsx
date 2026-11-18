// A row per series: name, now, mean, peak and a small trend.
//
// The fleet page's "one figure and one trend" applied to metrics. A tab's few
// lead charts carry the questions a reader opens it for; everything else is
// a row here, where it costs a line instead of a 112px panel and still opens
// into the full chart on a click.
import { useState } from "react";
import type { MetricsResponse } from "../../../lib/api";
import type { Range } from "../../../lib/range";
import { RAIL_RANGES } from "../../../lib/range";
import { ABSENT } from "../../../lib/format";
import { latestValue, meanValue, peakValue } from "../../../lib/metrics";
import { Table, type Column } from "../../../ui/Table";
import { Sparkline } from "../../../ui/charts/Sparkline";
import { Enlargeable } from "../../../ui/charts/Enlargeable";
import type { Band } from "../../../ui/charts/ChartPanel";
import type { Family, PanelGroup, PanelSpec } from "../chartSpecs";
import type { GraphsProps } from "./Graphs";
import { resolveSpec } from "./resolveSpec";

/** The trend cell's size: a row, not a panel. */
const TREND_WIDTH = 160;
const TREND_HEIGHT = 24;

interface SeriesRow {
  kind: "series";
  key: string;
  group: string;
  name: string;
  now: number | null;
  mean: number | null;
  peak: number | null;
  /** Nothing but zero, or nothing at all, across the window. */
  zero: boolean;
  values: (number | null)[];
  spec: PanelSpec;
  /** The whole spec's bands, for the enlarged chart a row opens into. */
  series: Band[];
  window: { from: string; to: string } | null;
  fetchSeries?: (range: Range) => Promise<{
    series: Band[];
    window: { from: string; to: string } | null;
  }>;
}

interface NoteRow {
  kind: "note";
  key: string;
  group: string;
  name: string;
  note: string;
}

interface FoldRow {
  kind: "fold";
  key: string;
  group: string;
  count: number;
  open: boolean;
}

type Row = SeriesRow | NoteRow | FoldRow;

function stats(values: readonly (number | null)[], band?: (number | null)[]) {
  return {
    // The latest bucket, a gap included: a host that stopped reporting has
    // no current rate, and its last one printed as "now" would say the
    // traffic is steady. The panels' headlines read the same bucket.
    now: latestValue(values),
    mean: meanValue(values),
    // The envelope where there is one: the max of a mean line understates
    // the bucket's own peak at every rolled-up tier.
    peak: peakValue([...values, ...(band ?? [])]),
  };
}

/**
 * The row's name. A spec with one series is its title; a spec named like
 * its group needs only the series' own name ("major faults" under Memory
 * pressure); otherwise both.
 */
function rowName(spec: PanelSpec, group: string, band: Band, count: number) {
  if (count === 1) return spec.title;
  if (spec.title === group) return band.name;
  return `${spec.title} · ${band.name}`;
}

export interface LedgerProps {
  groups: PanelGroup[];
  sources: GraphsProps;
  /**
   * Fold each group's rows that read zero all window into one line. For
   * the protocol counters, most of which are zero on a healthy host and
   * would otherwise be the table.
   */
  fold?: boolean;
  fetchFamily?: (family: Family, range: Range) => Promise<MetricsResponse>;
  range?: Range;
}

export function Ledger({
  groups,
  sources,
  fold = false,
  fetchFamily,
  range,
}: LedgerProps) {
  const [opened, setOpened] = useState<ReadonlySet<string>>(new Set());

  const rows: Row[] = [];
  for (const group of groups) {
    const live: Row[] = [];
    const quiet: Row[] = [];
    for (const spec of group.specs) {
      const view = resolveSpec(spec, sources, fetchFamily);
      if (view.series.length === 0) {
        // A family still being asked for is not quiet: folding it would say
        // "stayed at zero or went unreported" before the hub was asked.
        (view.loading ? live : quiet).push({
          kind: "note",
          key: spec.slug,
          group: group.title,
          name: spec.title,
          note: view.unavailable ?? "Not collected.",
        });
        continue;
      }
      for (const band of view.series) {
        const s = stats(band.values, band.band);
        const row: SeriesRow = {
          kind: "series",
          key: `${spec.slug}:${band.name}`,
          group: group.title,
          name: rowName(spec, group.title, band, view.series.length),
          ...s,
          zero: band.values.every((v) => v === null || v === 0),
          values: band.values,
          spec,
          series: view.series,
          window: view.res?.window ?? null,
          ...(view.fetchSeries ? { fetchSeries: view.fetchSeries } : {}),
        };
        (fold && row.zero ? quiet : live).push(row);
      }
    }
    rows.push(...live);
    if (!fold) {
      rows.push(...quiet);
      continue;
    }
    if (quiet.length === 0) continue;
    const open = opened.has(group.title);
    if (open) rows.push(...quiet);
    rows.push({
      kind: "fold",
      key: `${group.title}:fold`,
      group: group.title,
      count: quiet.length,
      open,
    });
  }

  const toggle = (group: string) =>
    setOpened((prev) => {
      const next = new Set(prev);
      if (next.has(group)) next.delete(group);
      else next.add(group);
      return next;
    });

  const show = (row: SeriesRow, v: number | null) => {
    const text = row.spec.fmt
      ? row.spec.fmt(v)
      : v === null
        ? ABSENT
        : String(v);
    return v === null || row.spec.unit === undefined
      ? text
      : `${text} ${row.spec.unit}`;
  };

  const columns: Column<Row>[] = [
    {
      key: "name",
      header: "Metric",
      cell: (row) => {
        if (row.kind === "fold")
          return (
            <span className="ledger-fold">
              {row.open
                ? ""
                : `${row.count} ${row.count === 1 ? "row" : "rows"} stayed at zero or went unreported · `}
              <button
                type="button"
                className="linkbtn"
                onClick={() => toggle(row.group)}
              >
                {row.open ? "hide the quiet rows" : "show"}
              </button>
            </span>
          );
        return row.name;
      },
    },
    {
      key: "now",
      header: "Now",
      align: "right",
      cell: (row) =>
        row.kind === "series" ? (
          show(row, row.now)
        ) : row.kind === "note" ? (
          <span className="ledger-note">{row.note}</span>
        ) : null,
    },
    {
      key: "mean",
      header: "Mean",
      align: "right",
      cell: (row) => (row.kind === "series" ? show(row, row.mean) : null),
    },
    {
      key: "peak",
      header: "Peak",
      align: "right",
      cell: (row) => (row.kind === "series" ? show(row, row.peak) : null),
    },
    {
      key: "trend",
      header: "Trend",
      width: `${TREND_WIDTH + 16}px`,
      cell: (row) =>
        row.kind === "series" ? (
          <Enlargeable
            title={row.spec.title}
            about={row.spec.about}
            unit={row.spec.unit}
            series={row.series}
            fmt={row.spec.fmt}
            stacked={row.spec.stacked}
            mirrored={row.spec.mirrored}
            tickBase={row.spec.tickBase}
            window={row.window}
            range={range}
            ranges={RAIL_RANGES}
            fetchSeries={row.fetchSeries}
            label={`${row.name}: open the chart`}
          >
            <Sparkline
              values={row.values}
              width={TREND_WIDTH}
              height={TREND_HEIGHT}
              // Ink: a ledger row has no legend, so a hue would name nothing.
              color="var(--ink-2)"
              label={`${row.name} over time`}
            />
          </Enlargeable>
        ) : null,
    },
  ];

  return (
    <div className="ledger">
      <Table
        columns={columns}
        rows={rows}
        rowKey={(row) => row.key}
        groupBy={{
          key: (row) => row.group,
          label: (key) => key,
          order: (key) => groups.findIndex((g) => g.title === key),
        }}
      />
    </div>
  );
}
