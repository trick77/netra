// The charts of a subject tab, drawn from the specs in ../chartSpecs.
//
// A tab's groups come in three forms (PanelGroup.form): a few LEAD charts the
// tab is opened for, large, with the window's events on them; a LEDGER, one
// table row per remaining series; and plain small multiples, for the tabs not
// yet given a lead. One range control -- the header's -- drives all of them.
//
// This used to be THE Graphs tab, which is why the file is still called that.
// There is no Graphs tab now: "Graphs" named a rendering format rather than a
// subject. See GROUP_SLUGS in ../chartSpecs for the grouping.
import { useMemo, type ReactNode } from "react";
import type { Event, Filesystem, MetricsResponse } from "../../../lib/api";
import type { TimedEvent } from "../../../ui/charts/cursor";
import { messageOf } from "../../events/message";
import type { Range } from "../../../lib/range";
import { windowNotice } from "../../../lib/metrics";
import { perCoreBands } from "../../../lib/bands";
import { ChartPanel } from "../../../ui/charts/ChartPanel";
import { HeatStrip } from "../../../ui/charts/HeatStrip";
import { RAIL_RANGES } from "../../../lib/range";
import {
  AGENT_GROUPS,
  NETWORK_GROUPS,
  STORAGE_GROUPS,
  SYSTEM_GROUPS,
  sourcesFor,
  type Family,
  type PanelGroup,
  type PanelSpec,
} from "../chartSpecs";
import { Ledger } from "./Ledger";
import { PairRows } from "./PairRows";
import { DriveRows, FilesystemTable } from "./Storage";
import { resolveSpec } from "./resolveSpec";

export interface GraphsProps {
  host?: MetricsResponse | null;
  hostSnmp?: MetricsResponse | null;
  hostProto?: MetricsResponse | null;
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
   * Loads ONE family at another range, for an enlarged chart alone, so a
   * single dialog can ask for a longer window without dragging every other
   * chart on the tab along.
   */
  fetchFamily?: (family: Family, range: Range) => Promise<MetricsResponse>;
  /** The window's events, drawn on the lead charts (PanelSpec.lead) only. */
  events?: readonly TimedEvent[];
  /** The host's thread count, for the specs that rule against it. */
  threads?: number | null;
  /** The mount inventory, for the Storage tab's Filesystems table. */
  filesystems?: readonly Filesystem[] | null;
  /** The host's last_seen, dating each mount's stored gauge. */
  lastSeen?: string | null;
  /** The tab's first answer has not arrived: an empty family is loading,
   * not uncollected. */
  pending?: boolean;
}

/**
 * The page's events as chart markers: the moment, the severity the hub
 * stated, and the same sentence the event log prints for it.
 */
export function timedEvents(
  events: readonly Event[] | null | undefined,
): TimedEvent[] {
  if (!events) return [];
  const out: TimedEvent[] = [];
  for (const e of events) {
    const at = Date.parse(e.ts);
    if (Number.isFinite(at))
      out.push({ at, severity: e.severity, label: messageOf(e) });
  }
  return out;
}

/** A lead chart's height: a chart a reader opens the tab for, not a tile. */
const LEAD_HEIGHT = 180;

/**
 * One spec, drawn.
 *
 * Exported, and named for what it takes rather than for what it is: the
 * Overview draws specs beside its readings, and rebuilding one there would
 * give one chart two titles, two `about` strings and two enlarge behaviours.
 */
export function SpecPanel({
  spec,
  sources,
  range,
  fetchFamily,
  height,
}: {
  spec: PanelSpec;
  /** Every family the page has fetched. A single-source panel reads one of
   * them; a cross-source panel reads two. */
  sources: GraphsProps;
  range?: Range;
  fetchFamily?: (family: Family, range: Range) => Promise<MetricsResponse>;
  /** Taller for a lead chart; ChartPanel's own default otherwise. */
  height?: number;
}) {
  const {
    res,
    series,
    fetchSeries,
    reference,
    max,
    noCeiling,
    unavailable,
    loading,
  } = resolveSpec(spec, sources, fetchFamily);

  // No legend is built here: Overlay (inside ChartPanel) already renders
  // one as soon as a panel carries two or more bands, which is exactly the
  // point at which colour alone stops carrying identity.
  return (
    <ChartPanel
      title={spec.title}
      // Undefined for a spec that has none, which is what keeps the glyph off
      // the panels whose title already says everything.
      about={spec.about}
      // "Not collected" would be a lie here: the bands exist, the scale to
      // read them against does not, and a reader sent looking for a broken
      // collector would find a perfectly healthy one.
      unavailableHeadline={
        loading
          ? "Loading"
          : noCeiling === undefined
            ? undefined
            : "No scale to draw against"
      }
      unit={spec.unit}
      series={series}
      max={max}
      reference={reference}
      min={spec.min}
      tickBase={spec.tickBase}
      fmt={spec.fmt}
      stacked={spec.stacked}
      mirrored={spec.mirrored}
      filled={spec.filled}
      // A long legend is longer than the chart it explains. Twelve rather
      // than six for a mirrored stack: its bands come in pairs.
      legend={series.length <= (spec.mirrored && spec.stacked ? 12 : 6)}
      // Boolean panels have no VALUE axis: their 0 and 1 are states. They
      // keep their time axis.
      hideAxis={spec.hideAxis ?? spec.boolean}
      // No per-panel notice: the window statement is about the RANGE, and it
      // is rendered once, above the groups.
      notice={null}
      unavailable={unavailable}
      window={res?.window ?? null}
      range={range}
      fetchSeries={fetchSeries}
      // Only what the host page's own fetcher will serve.
      ranges={RAIL_RANGES}
      events={spec.lead ? sources.events : undefined}
      {...(height !== undefined ? { height } : {})}
    />
  );
}

function Group({
  group,
  sources,
}: {
  group: PanelGroup;
  sources: GraphsProps;
}) {
  const { range, fetchFamily } = sources;
  const lead = group.form === "lead";
  return (
    <>
      <h3 className="grouphead">{group.title}</h3>
      <div className={lead ? "sm lead" : "sm"}>
        {group.specs.map((spec) => (
          <SpecPanel
            // Keyed by slug, not title: the slug is the stable identity.
            key={spec.slug}
            spec={spec}
            sources={sources}
            range={range}
            fetchFamily={fetchFamily}
            {...(lead ? { height: LEAD_HEIGHT } : {})}
          />
        ))}
      </div>
    </>
  );
}

/**
 * The charts of one subject tab, with the window notice above them.
 *
 * The notice is deduplicated across families and scoped to the families THIS
 * tab draws. Consecutive ledger groups share one table, each a heading row in
 * it: two tables one under the other would be two column layouts for one
 * kind of row.
 */
export function PanelGroups({
  groups,
  sources,
  afterLead,
  fold = false,
  ledgerTitle,
}: {
  groups: PanelGroup[];
  sources: GraphsProps;
  /** Drawn under the first lead charts: the per-core strip. */
  afterLead?: ReactNode;
  /** Fold the ledger's all-zero rows (see Ledger). */
  fold?: boolean;
  /** A heading over the ledger, where its groups need one name. */
  ledgerTitle?: string;
}) {
  const shown = new Set(
    groups.flatMap((g) => g.specs.flatMap((spec) => sourcesFor(spec))),
  );
  const notices = [
    ...new Set(
      [...shown]
        .map((source) => sources[source] ?? null)
        .map((res) => (res ? windowNotice(res) : null))
        .filter((n): n is string => n !== null),
    ),
  ];

  // Runs of consecutive ledger groups, each drawn as one table.
  const blocks: (PanelGroup | PanelGroup[])[] = [];
  for (const group of groups) {
    const last = blocks.at(-1);
    if (group.form === "ledger" && Array.isArray(last)) last.push(group);
    else blocks.push(group.form === "ledger" ? [group] : group);
  }

  return (
    <div>
      {notices.map((notice) => (
        <p className="note" key={notice}>
          {notice}
        </p>
      ))}
      {blocks.map((block, i) =>
        Array.isArray(block) ? (
          <div key={block.map((g) => g.title).join("|")}>
            {ledgerTitle !== undefined && (
              <h3 className="grouphead">{ledgerTitle}</h3>
            )}
            <Ledger
              groups={block}
              sources={sources}
              fold={fold}
              range={sources.range}
              fetchFamily={sources.fetchFamily}
            />
          </div>
        ) : block.form === "pairs" ? (
          <PairRows
            key={block.title}
            group={block}
            sources={sources}
            range={sources.range}
            fetchFamily={sources.fetchFamily}
          />
        ) : block.form === "filesystems" ? (
          <FilesystemTable
            key={block.title}
            rows={sources.filesystems ?? []}
            metrics={sources.filesystem ?? null}
            range={sources.range}
            lastSeen={sources.lastSeen}
            fetchFamily={sources.fetchFamily}
          />
        ) : block.form === "drives" ? (
          <DriveRows
            key={block.title}
            group={block}
            sources={sources}
            range={sources.range}
          />
        ) : (
          <div key={block.title}>
            <Group group={block} sources={sources} />
            {i === 0 && block.form === "lead" && afterLead}
          </div>
        ),
      )}
    </div>
  );
}

/**
 * Every core as a row of one ink, brighter where busier: which core, and
 * when, at a glance on any core count. The cores in their numeric order --
 * the response's order is the key's text, where core 10 follows core 1.
 */
export function PerCore({ cores }: { cores: MetricsResponse | null }) {
  // Per response, not per render: the strip memoises its cells on these
  // rows, and the shared cursor re-renders this tab on every pointer move.
  const rows = useMemo(
    () =>
      perCoreBands(cores)
        .map((band, i) => ({
          name: band.name,
          values: band.values,
          order: Number(/\d+$/.exec(band.name)?.[0] ?? i),
        }))
        .sort((a, b) => a.order - b.order),
    [cores],
  );
  if (rows.length === 0) return null;
  return (
    <section className="percore" aria-label="CPU per core">
      <h3 className="grouphead">
        Per core{" "}
        <small>
          {rows.length} {rows.length === 1 ? "thread" : "threads"}, brighter is
          busier
        </small>
      </h3>
      <HeatStrip
        rows={rows}
        label={`Busy per core, ${rows.length} threads`}
        window={cores?.window ?? null}
      />
    </section>
  );
}

// The subject tabs. Thin on purpose: what differs between them is which
// groups they draw, and that lives in chartSpecs beside the panels.
export function SystemGraphs(props: GraphsProps) {
  return (
    <PanelGroups
      groups={SYSTEM_GROUPS}
      sources={props}
      afterLead={<PerCore cores={props.cpuCore ?? null} />}
    />
  );
}

/** The agent's own panels, which the Collectors tab draws under its
 * capability list. */
export function AgentGraphs(props: GraphsProps) {
  return <PanelGroups groups={AGENT_GROUPS} sources={props} />;
}

export function NetworkGraphs(props: GraphsProps) {
  return (
    <PanelGroups
      groups={NETWORK_GROUPS}
      sources={props}
      fold
      ledgerTitle="Protocol counters"
    />
  );
}

export function StorageGraphs(props: GraphsProps) {
  return <PanelGroups groups={STORAGE_GROUPS} sources={props} />;
}
