// The Agent tab: the agent's own health.
//
// Hub latency leads, scrape duration and connect failures are ledger rows,
// and every collector is a row of its own: whether it is working, how often
// it failed in the window, and how long it takes. The collector family was
// fetched for this tab all along and drawn by nothing.
import type { HostDetail, MetricsResponse } from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import {
  carriesColumn,
  griddedValues,
  meanValue,
  optionalValues,
  peakValue,
  seriesCells,
} from "../../../lib/metrics";
import { Badge } from "../../../ui/Badge";
import { Table, type Column } from "../../../ui/Table";
import { Sparkline } from "../../../ui/charts/Sparkline";
import { AgentGraphs, type GraphsProps } from "./Graphs";
import { Panel } from "./Panel";
import { LOADING } from "./resolveSpec";

export interface AgentTabProps {
  host: HostDetail;
  /** The families this tab draws. Named as GraphsProps names them -- `host`
   * there is the host METRICS family, which is why the host itself cannot
   * simply be spread in beside them. */
  sources: GraphsProps;
}

interface CollectorRow {
  name: string;
  /** The agent's own word for a collector that is not fully working, when
   * it has one -- "not permitted", "no-devices". */
  capability?: string;
  /** Null for a collector the agent reports as off, with no samples. */
  failures: number | null;
  /** The newest sample failed. */
  failing: boolean;
  /** Why it is off, or the last error code it reported. */
  reason: string | null;
  mean: number | null;
  peak: number | null;
  trend: (number | null)[];
}

function lastOf<T>(values: readonly T[], keep: (v: T) => boolean): T | null {
  for (let i = values.length - 1; i >= 0; i--) {
    if (keep(values[i]!)) return values[i]!;
  }
  return null;
}

/**
 * One row per collector the family answered for, then one per capability the
 * agent reports as not working that has no series of its own -- a collector
 * that never ran is the row a reader most needs, and it has no samples.
 */
export function collectorRows(
  res: MetricsResponse | null | undefined,
  capabilities: Record<string, string>,
): CollectorRow[] {
  const rows: CollectorRow[] = [];
  const rolled = res != null && carriesColumn(res, "failure_count");
  for (let i = 0; i < (res?.series.length ?? 0); i++) {
    const r = res!;
    const name = r.series[i]!.key.collector ?? `collector ${i}`;
    const durations = griddedValues(r, i, "duration_ms");
    const peaks = carriesColumn(r, "duration_ms_max")
      ? optionalValues(r, i, "duration_ms_max")
      : durations;
    let failures = 0;
    let failing = false;
    if (rolled) {
      const counts = optionalValues(r, i, "failure_count");
      for (const v of counts) failures += v ?? 0;
      failing = (lastOf(counts, (v) => v !== null) ?? 0) > 0;
    } else if (r.columns.includes("ok")) {
      const ok = seriesCells(r, i, "ok");
      for (const v of ok) if (v === false) failures++;
      failing = lastOf(ok, (v) => typeof v === "boolean") === false;
    }
    const code = r.columns.includes("error_code")
      ? lastOf(seriesCells(r, i, "error_code"), (v) => typeof v === "string")
      : null;
    rows.push({
      name,
      failures,
      failing,
      reason: failures > 0 && typeof code === "string" ? code : null,
      mean: meanValue(durations),
      peak: peakValue(peaks),
      trend: durations,
    });
  }
  // The agent's stated reason wins over a clean sample record: a collector
  // that runs and returns partial data ("not permitted") is not fine, and
  // this is the one place that says why its charts are missing.
  for (const row of rows) {
    const state = capabilities[row.name];
    if (state !== undefined && state !== "ok") row.capability = state;
  }
  const named = new Set(rows.map((row) => row.name));
  for (const [name, state] of Object.entries(capabilities)) {
    if (state === "ok" || named.has(name)) continue;
    rows.push({
      name,
      failures: null,
      failing: false,
      reason: state,
      mean: null,
      peak: null,
      trend: [],
    });
  }
  return rows;
}

const ms = (v: number | null) =>
  v === null
    ? ABSENT
    : `${v < 10 ? Math.round(v * 10) / 10 : Math.round(v)} ms`;

const COLUMNS: Column<CollectorRow>[] = [
  {
    key: "name",
    header: "Collector",
    cell: (row) => row.name,
    sortValue: (row) => row.name,
  },
  {
    key: "status",
    header: "Status",
    // A word in the badge: severity is never carried by the hue alone.
    cell: (row) =>
      row.failures === null ? (
        <span className="ledger-note">{row.reason}</span>
      ) : row.failing ? (
        <Badge severity="critical">failing</Badge>
      ) : row.capability !== undefined ? (
        <Badge severity="warning">{row.capability}</Badge>
      ) : row.failures > 0 ? (
        <Badge severity="warning">failed at times</Badge>
      ) : (
        <Badge severity="ok">ok</Badge>
      ),
    // Failing now above never ran: the red row is the one to act on first,
    // and a collector the agent switched off says why in its own words.
    sortValue: (row) =>
      row.failing
        ? 3
        : row.failures === null
          ? 2
          : row.capability !== undefined || row.failures > 0
            ? 1
            : 0,
  },
  {
    key: "failures",
    header: "Failed",
    align: "right",
    cell: (row) =>
      row.failures === null ? null : (
        <>
          {row.failures}
          {row.reason !== null && (
            <small className="ledger-note"> {row.reason}</small>
          )}
        </>
      ),
    sortValue: (row) => row.failures,
  },
  {
    key: "mean",
    header: "Mean",
    align: "right",
    cell: (row) => (row.failures === null ? null : ms(row.mean)),
    sortValue: (row) => row.mean,
  },
  {
    key: "peak",
    header: "Peak",
    align: "right",
    cell: (row) => (row.failures === null ? null : ms(row.peak)),
    sortValue: (row) => row.peak,
  },
  {
    key: "trend",
    header: "Trend",
    cell: (row) =>
      row.trend.length === 0 ? null : (
        <Sparkline
          values={row.trend}
          width={140}
          height={24}
          color="var(--ink-2)"
          label={`${row.name} duration over time`}
        />
      ),
  },
];

export function AgentTab({ host, sources }: AgentTabProps) {
  const rows = collectorRows(sources.collector, host.capabilities);
  return (
    <>
      <AgentGraphs {...sources} />
      <Panel label="Collectors" title="Collectors">
        {rows.length === 0 ? (
          // Only an answer can say there are none: before it, or when the hub
          // could not be asked, the page knows nothing about the host.
          <p className="note">
            {sources.pending || sources.collector === undefined
              ? LOADING
              : sources.collector === null
                ? "The hub did not answer for the collectors."
                : "The agent reported no collectors."}
          </p>
        ) : (
          <Table
            columns={COLUMNS}
            rows={rows}
            rowKey={(row) => row.name}
            // The ones in trouble first: a collector failing now, then one
            // that never ran, then one that failed in the window.
            defaultSort={{ key: "status", dir: "desc" }}
            rowSeverity={(row) =>
              row.failing
                ? "critical"
                : (row.failures ?? 0) > 0 || row.capability !== undefined
                  ? "warning"
                  : null
            }
          />
        )}
      </Panel>
    </>
  );
}
