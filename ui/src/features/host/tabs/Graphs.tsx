// The Graphs tab: small multiples. ONE ChartPanel component, N instances,
// uniform size (nothing here overrides its width/height), one range
// control -- the header's -- driving all of them. Adding a family later is
// a row in PANELS, not a new component.
//
// The specs themselves live in ../chartSpecs, because the chart page resolves
// a slug against the same list.
import type { MetricsResponse } from "../../../lib/api";
import type { Range } from "../../../lib/range";
import { windowNotice } from "../../../lib/metrics";
import { ChartPanel } from "../../../ui/charts/ChartPanel";
import { RANGE_VALUES } from "../ranges";
import {
  SYSTEM,
  NETWORK,
  STORAGE,
  bandsFor,
  familyFor,
  missingReason,
  type Family,
  type PanelSpec,
} from "../chartSpecs";

export interface GraphsProps {
  host?: MetricsResponse | null;
  hostSnmp?: MetricsResponse | null;
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
  /** The host these panels belong to, for the per-chart page links. Widened
   * to match HostPage, which takes an id from the URL as a string and from a
   * fetched host as a number. */
  hostId?: number | string;
}

function Panel({
  spec,
  res,
  range,
  fetchFamily,
  hostId,
}: {
  spec: PanelSpec;
  res: MetricsResponse | null;
  range?: Range;
  fetchFamily?: (family: Family, range: Range) => Promise<MetricsResponse>;
  hostId?: number | string;
}) {
  const series = bandsFor(spec, res);

  // The same bandsFor the page uses, over a response for one family at one
  // other range -- so an enlarged chart draws its wider window exactly as
  // the small one drew its narrower one, counters, stacks and all. Rebuilt
  // per render rather than memoised: useDetailRange only calls it when its
  // own range actually differs from the page's, which is at most once per
  // click on a picker nobody clicks in a loop.
  const fetchSeries = fetchFamily
    ? async (next: Range) => {
        const answered = await fetchFamily(familyFor(spec), next);
        return {
          series: bandsFor(spec, answered),
          window: answered.window ?? null,
        };
      }
    : undefined;
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
      // The chart's own page. Every panel here has a slug, so every panel
      // here is a link; the dialog is what a chart without a page still
      // gets.
      // Carrying the range, because the link is meant to be the view someone
      // is actually looking at: without it a panel opened at 30d rendered its
      // page at the default range, and ChartScreen's Back -- which forwards
      // the page's own query string -- then dropped the tab back to the
      // default too, the opposite of the "carrying the range with it" its
      // comment claims.
      href={
        hostId === undefined
          ? undefined
          : `/hosts/${encodeURIComponent(String(hostId))}/chart/${spec.slug}` +
            (range === undefined ? "" : `?range=${encodeURIComponent(range)}`)
      }
      title={spec.title}
      unit={spec.unit}
      series={series}
      max={spec.max}
      fmt={spec.fmt}
      stacked={spec.stacked}
      mirrored={spec.mirrored}
      // A 32-core legend is longer than the chart it explains. Suppressed
      // with legend, not highlight: the latter also dims every other series
      // to 35% and washed the whole stack out.
      legend={series.length <= 6}
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
      ranges={RANGE_VALUES}
    />
  );
}

function Group({
  title,
  specs,
  sources,
}: {
  title: string;
  specs: PanelSpec[];
  sources: GraphsProps;
}) {
  const { range, fetchFamily, hostId } = sources;
  return (
    <>
      <h3 className="grouphead">{title}</h3>
      <div className="sm">
        {specs.map((spec) => (
          <Panel
            // Keyed by slug, not title: the slug is the stable identity, and
            // two panels could in principle share a title.
            key={spec.slug}
            spec={spec}
            res={sources[spec.source] ?? null}
            range={range}
            fetchFamily={fetchFamily}
            hostId={hostId}
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
      [
        props.host,
        props.hostSnmp,
        props.net,
        props.diskIo,
        props.filesystem,
        props.collector,
        props.agent,
      ]
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
      <Group title="Network" specs={NETWORK} sources={props} />
      <Group title="Storage" specs={STORAGE} sources={props} />
    </div>
  );
}
