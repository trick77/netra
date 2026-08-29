// Small multiples: ONE ChartPanel component, N instances, uniform size
// (nothing here overrides its width/height), one range control -- the
// header's -- driving all of them.
//
// This used to be THE Graphs tab, which is why the file is still called that.
// There is no Graphs tab now: "Graphs" named a rendering format rather than a
// subject and split every subject across two tabs -- addresses here, network
// charts there. What is left is the machinery, rendered by whichever subject
// tab owns the panels. See GROUP_SLUGS in ../chartSpecs for the grouping.
//
// The specs themselves live in ../chartSpecs, because the chart page resolves
// a slug against the same list.
import type { MetricsResponse } from "../../../lib/api";
import type { Range } from "../../../lib/range";
import { windowNotice } from "../../../lib/metrics";
import { ChartPanel } from "../../../ui/charts/ChartPanel";
import { RAIL_RANGES } from "../../../lib/range";
import {
  COLLECTOR_GROUPS,
  NETWORK_GROUPS,
  REFERENCE_HEADROOM,
  STORAGE_GROUPS,
  SYSTEM_GROUPS,
  bandsFor,
  ceilingOf,
  familiesFor,
  missingReason,
  noCeilingReason,
  sourcesFor,
  type Family,
  type PanelGroup,
  type PanelSpec,
} from "../chartSpecs";

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
   * Loads ONE family at another range, for an enlarged chart alone.
   *
   * The page keeps fetching all seven families at the page's range, as it
   * always has. This exists so a single dialog can ask for a longer window
   * without dragging the other nineteen panels along -- which is what the
   * dialog's picker did when it was wired to the page's setter.
   */
  fetchFamily?: (family: Family, range: Range) => Promise<MetricsResponse>;
}

/**
 * One spec, drawn.
 *
 * Exported, and named for what it takes rather than for what it is: the
 * Overview draws two of these (load averages and traffic) beside its tiles,
 * and rebuilding either there would give one chart two titles, two `about`
 * strings and two enlarge behaviours. The name-clash note above still holds
 * -- tabs/Panel.tsx is a card wrapper and this is a chart -- and SpecPanel
 * is the half that says which one it is.
 */
export function SpecPanel({
  spec,
  sources,
  range,
  fetchFamily,
}: {
  spec: PanelSpec;
  /** Every family the page has fetched. A single-source panel reads one of
   * them; a cross-source panel (the fragmentation pair) reads two. */
  sources: GraphsProps;
  range?: Range;
  fetchFamily?: (family: Family, range: Range) => Promise<MetricsResponse>;
}) {
  const res = sources[spec.source] ?? null;
  // Only the families this spec's bases actually name. Handing bandsFor the
  // whole props object would work and would also let a spec quietly read a
  // family it never declared, which is the sort of thing that goes unnoticed
  // until a panel draws on a page that does not fetch it.
  const extra: Partial<Record<PanelSpec["source"], MetricsResponse | null>> =
    {};
  for (const source of sourcesFor(spec)) {
    extra[source] = sources[source] ?? null;
  }

  const series = bandsFor(spec, res, { extra });
  // The enlarged view has room for the pair -- mean as the line, the
  // bucket's peak as a pale envelope under it -- and the 260px panel does
  // not: two marks in that space are a smear, so it draws the peak alone.
  // bandsFor only builds an envelope for the specs that can carry one (a
  // mirrored rate chart at a rollup tier), so every other panel is handed
  // exactly what it already had.
  // Wherever the tier has a max column, at every window: bandsFor asks that
  // question and nothing else. There was a 48-hour floor here, the
  // reference's own -- see the note in fleet/hostTrends for what dropping it
  // costs on the ceiling.
  const detailSeries = bandsFor(spec, res, {
    withPeakBand: true,
    extra,
  });

  // The same bandsFor the panel uses, over a response for one family at one
  // other range -- so an enlarged chart draws its wider window exactly as
  // the small one drew its narrower one, counters, stacks and all. With the
  // envelope, because this feeds the DIALOG: without it, widening the range
  // would quietly drop the pair back to a single mark. Rebuilt per render
  // rather than memoised: useDetailRange only calls it when its own range
  // actually differs from the page's, which is at most once per click on a
  // picker nobody clicks in a loop.
  //
  // EVERY family the spec names, not only its own: a dialog that widened a
  // cross-source panel by fetching one family would drop the other's bands,
  // which is strictly less than the 260px panel it was opened from. The
  // primary is sourcesFor()[0] by construction, and its window is the one
  // the axis is drawn against.
  const fetchSeries = fetchFamily
    ? async (next: Range) => {
        const wanted = sourcesFor(spec);
        const answers = await Promise.all(
          familiesFor(spec).map((family) => fetchFamily(family, next)),
        );
        const widened: Partial<Record<PanelSpec["source"], MetricsResponse>> =
          {};
        wanted.forEach((source, i) => {
          widened[source] = answers[i];
        });
        const primary = answers[0];
        return {
          series: bandsFor(spec, primary, {
            withPeakBand: true,
            extra: widened,
          }),
          window: primary.window ?? null,
        };
      }
    : undefined;
  // A ceiling the DATA carries -- memory's mem_total -- and the dashed rule
  // marking it, exactly as the chart page reads them. The panel used to pass
  // spec.max alone, which is undefined for host-memory: ChartPanel then fell
  // back to the stack's own running total, so every host's Memory panel drew
  // a full box with no rule on it. That is the always-full reading this spec
  // carries a ceiling to prevent.
  const reference =
    res && spec.ceiling ? ceilingOf(spec.ceiling(res)) : undefined;
  // An empty band list has two causes and they are not the same fact: this
  // tier does not carry the columns (the rollups drop most per-state
  // columns), or nothing has been fetched yet. Either way an empty chart
  // asserts "the host reported nothing", which spec 7.6 forbids -- so the
  // panel says which one it is instead of drawing a blank box.
  //
  // A spec that declares a ceiling and cannot find one is the third cause,
  // and it is not "no data": the bands are there, the scale to read them
  // against is not. The chart page refuses this case outright rather than
  // auto-scaling it, and the tab has to refuse it too -- drawing it here
  // while declining it there would leave the panel and the page it links to
  // disagreeing about the same host.
  const noCeiling =
    series.length > 0 &&
    res !== null &&
    spec.ceiling !== undefined &&
    reference === undefined
      ? noCeilingReason(spec)
      : undefined;
  // Keyed off the PRIMARY response alone: a cross-source panel whose foreign
  // family has not arrived still has its own bands to draw, and calling that
  // "no data" would blank a panel that has four of its six.
  const unavailable =
    series.length > 0
      ? noCeiling
      : res === null
        ? "No data has been read for this family yet."
        : missingReason(spec, res, extra);

  // No legend is built here: Overlay (inside ChartPanel) already renders
  // one as soon as a panel carries two or more bands, which is exactly the
  // point at which colour alone stops carrying identity.
  return (
    <ChartPanel
      title={spec.title}
      // Undefined for a spec that has none, which is what keeps the glyph off
      // the panels whose title already says everything. The host Overview tab
      // builds its ChartPanels by hand and passes nothing at all, so it stays
      // clean by construction rather than by an exception here.
      about={spec.about}
      detailSeries={detailSeries}
      // "Not collected" would be a lie here: the bands exist, the scale to
      // read them against does not, and a reader sent looking for a broken
      // collector would find a perfectly healthy one.
      unavailableHeadline={
        noCeiling === undefined ? undefined : "No scale to draw against"
      }
      unit={spec.unit}
      series={series}
      // Headroom above the rule so it reads as a limit rather than as the
      // top border of the plot -- the same 1.08 the fleet cell, the host
      // overview's memory card and the chart page all use.
      max={
        reference !== undefined
          ? reference * REFERENCE_HEADROOM
          : (spec.max ?? undefined)
      }
      reference={reference}
      min={spec.min}
      tickBase={spec.tickBase}
      fmt={spec.fmt}
      stacked={spec.stacked}
      mirrored={spec.mirrored}
      // A 32-core legend is longer than the chart it explains. Suppressed
      // with legend, not highlight: the latter also dims every other series
      // to 35% and washed the whole stack out.
      //
      // Twelve rather than six for a mirrored stack: its bands come in
      // pairs, so six was three interfaces, and naming which interface
      // carried the traffic is the whole reason that panel is drawn per
      // interface rather than summed. Six pairs still fits.
      legend={series.length <= (spec.mirrored && spec.stacked ? 12 : 6)}
      // Boolean panels join the per-core stack in having no VALUE axis:
      // their 0 and 1 are states, and ticking them would label a chart
      // "up" / 0.5 / "down". Both keep their time axis.
      hideAxis={spec.hideAxis ?? spec.boolean}
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
      ranges={RAIL_RANGES}
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
  return (
    <>
      <h3 className="grouphead">{group.title}</h3>
      <div className="sm">
        {group.specs.map((spec) => (
          <SpecPanel
            // Keyed by slug, not title: the slug is the stable identity, and
            // two panels could in principle share a title.
            key={spec.slug}
            spec={spec}
            sources={sources}
            range={range}
            fetchFamily={fetchFamily}
          />
        ))}
      </div>
    </>
  );
}

/**
 * The panels of one subject tab, with the window notice above them.
 *
 * The notice is deduplicated across families: every family answering the same
 * clamped window says the same sentence, and saying it once is the difference
 * between a statement and wallpaper. It is scoped to the families THIS tab
 * draws -- a Storage tab announcing that the ICMP family was clamped is
 * telling the reader about a page they are not on.
 */
export function PanelGroups({
  groups,
  sources,
}: {
  groups: PanelGroup[];
  sources: GraphsProps;
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

  return (
    <div>
      {notices.map((notice) => (
        <p className="note" key={notice}>
          {notice}
        </p>
      ))}
      {groups.map((group) => (
        <Group key={group.title} group={group} sources={sources} />
      ))}
    </div>
  );
}

// The three subject tabs. Thin on purpose: what differs between them is which
// groups they draw, and that lives in chartSpecs beside the panels.
export function SystemGraphs(props: GraphsProps) {
  return <PanelGroups groups={SYSTEM_GROUPS} sources={props} />;
}

/** The agent's own panels, which the Collectors tab draws under its
 * capability list. Same shape as the three subject tabs: what differs is the
 * groups, and those live in chartSpecs. */
export function CollectorGraphs(props: GraphsProps) {
  return <PanelGroups groups={COLLECTOR_GROUPS} sources={props} />;
}

export function NetworkGraphs(props: GraphsProps) {
  return <PanelGroups groups={NETWORK_GROUPS} sources={props} />;
}

export function StorageGraphs(props: GraphsProps) {
  return <PanelGroups groups={STORAGE_GROUPS} sources={props} />;
}
