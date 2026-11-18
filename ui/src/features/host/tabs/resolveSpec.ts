// Everything a view of one spec needs, read off the page's families.
//
// A module of its own because two views draw a spec -- the panel (Graphs)
// and the ledger row (Ledger) -- and each imports the other's neighbour.
import type { MetricsResponse } from "../../../lib/api";
import type { Range } from "../../../lib/range";
import { peak } from "../../../ui/charts/ChartDetail";
import {
  REFERENCE_HEADROOM,
  bandsFor,
  ceilingOf,
  familiesFor,
  missingReason,
  noCeilingReason,
  sourcesFor,
  type Family,
  type PanelSpec,
} from "../chartSpecs";
import type { GraphsProps } from "./Graphs";

/**
 * The bands, how to fetch them at another range, the scale and the reason a
 * spec cannot be drawn. Shared by the panel and the ledger row, so a series
 * reads the same in both and opens into the same enlarged chart.
 */
export function resolveSpec(
  spec: PanelSpec,
  sources: GraphsProps,
  fetchFamily?: (family: Family, range: Range) => Promise<MetricsResponse>,
) {
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

  // The pair at BOTH sizes: mean as the line, the bucket's peak as a pale
  // envelope under it. With the envelope at both sizes the line means one
  // thing everywhere, and enlarging a chart no longer changes which quantity
  // the mark is. bandsFor only builds an envelope for the specs that can
  // carry one, so every other panel is handed exactly what it already had.
  const series = bandsFor(spec, res, { withPeakBand: true, extra });

  // The same bandsFor over a response at another range, for the enlarged
  // view alone -- so a wider window draws exactly as the small one did,
  // counters, stacks and all. EVERY family the spec names, not only its own:
  // a dialog that widened a cross-source panel by fetching one family would
  // drop the other's bands. The primary is sourcesFor()[0] by construction,
  // and its window is the one the axis is drawn against.
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
  // marking it. Without it a stack scales to its own running total and every
  // host draws as full. Load's rule is the host's thread count instead, from
  // the host record.
  const threads =
    spec.threadsRule && sources.threads ? sources.threads : undefined;
  const reference =
    threads ?? (res && spec.ceiling ? ceilingOf(spec.ceiling(res)) : undefined);
  // Headroom above the rule so it reads as a limit rather than as the top
  // border of the plot. A thread count is a rule the data may cross, so the
  // scale is whichever is higher: a load of 40 on an 8-thread host must not
  // be cut off at 8.
  const max =
    threads !== undefined
      ? Math.max(threads, peak(series, spec.stacked, spec.mirrored)) *
        REFERENCE_HEADROOM
      : reference !== undefined
        ? reference * REFERENCE_HEADROOM
        : (spec.max ?? undefined);

  // An empty band list has two causes -- this tier does not carry the
  // columns, or nothing has been fetched yet -- and a spec that declares a
  // ceiling and cannot find one is a third. None of them is "the host
  // reported nothing", so the view says which one it is.
  const noCeiling =
    series.length > 0 &&
    res !== null &&
    spec.ceiling !== undefined &&
    reference === undefined
      ? noCeilingReason(spec)
      : undefined;
  // Keyed off the PRIMARY response alone: a cross-source panel whose foreign
  // family has not arrived still has its own bands to draw.
  //
  // An absent family is two different facts: the tab's first answer has not
  // arrived (loading), or it arrived without this family -- the hub did not
  // answer for it. Neither is "the host collected nothing", and the first is
  // not a failure at all.
  const loading =
    series.length === 0 && res === null && sources.pending === true;
  const unavailable =
    series.length > 0
      ? noCeiling
      : res === null
        ? loading
          ? LOADING
          : "No data has been read for this family yet."
        : missingReason(spec, res, extra);

  return {
    res,
    series,
    fetchSeries,
    reference,
    max,
    noCeiling,
    unavailable,
    loading,
  };
}

/** What an empty family says while it is still being asked for. */
export const LOADING = "Asking the hub for this window.";
