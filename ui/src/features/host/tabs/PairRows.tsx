// One row per in/out pair -- an interface -- on one shared scale.
//
// A stack of six interfaces told them apart by a band edge and a legend; as
// rows, the interface is its name at the start of the line, and a sixth
// interface adds a row rather than two more unlabelled layers. One scale for
// every row, so a quiet link reads as quiet next to a busy one.
import type { MetricsResponse } from "../../../lib/api";
import type { Range } from "../../../lib/range";
import { RAIL_RANGES } from "../../../lib/range";
import { ABSENT } from "../../../lib/format";
import { latestValue, peakValue } from "../../../lib/metrics";
import { Chart } from "../../../ui/charts/Chart";
import { Enlargeable } from "../../../ui/charts/Enlargeable";
import { useMeasuredWidth } from "../../../ui/charts/useMeasuredWidth";
import type { Band } from "../../../ui/charts/ChartPanel";
import type { Family, PanelGroup } from "../chartSpecs";
import type { GraphsProps } from "./Graphs";
import { resolveSpec } from "./resolveSpec";

const ROW_HEIGHT = 44;

/** The tallest thing a band draws: its envelope where it has one. */
export function bandPeak(band: Band | undefined): number {
  if (band === undefined) return 0;
  return (
    peakValue(band.band && band.band.length > 0 ? band.band : band.values) ?? 0
  );
}

/** "eth0 in" -> "eth0": the pair's key, the band's own suffix dropped. */
function pairName(band: Band): string {
  return band.name.replace(/ (in|out)$/, "");
}

export function PairRows({
  group,
  sources,
  fetchFamily,
  range,
}: {
  group: PanelGroup;
  sources: GraphsProps;
  fetchFamily?: (family: Family, range: Range) => Promise<MetricsResponse>;
  range?: Range;
}) {
  const { ref, width } = useMeasuredWidth<HTMLDivElement>(420);
  return (
    <section className="pairrows" aria-label={group.title}>
      <h3 className="grouphead">
        {group.title} <small>one scale for every row</small>
      </h3>
      {group.specs.map((spec) => {
        const view = resolveSpec(spec, sources, fetchFamily);
        const pairs: [Band, Band | undefined][] = [];
        for (let i = 0; i < view.series.length; i += 2)
          pairs.push([view.series[i]!, view.series[i + 1]]);
        if (pairs.length === 0)
          return (
            <p className="note" key={spec.slug}>
              {view.unavailable}
            </p>
          );
        const peak = {
          up: Math.max(...pairs.map(([up]) => bandPeak(up))),
          down: Math.max(...pairs.map(([, down]) => bandPeak(down))),
        };
        // bytes() already names the magnitude, so a byte rate needs only its
        // "/s": "52.9 MB/s", not "52.9 MB B/s".
        const fmt = (v: number | null) => {
          const text = spec.fmt ? spec.fmt(v) : v === null ? ABSENT : String(v);
          if (v === null || spec.unit === undefined) return text;
          return spec.unit === "B/s" ? `${text}/s` : `${text} ${spec.unit}`;
        };
        return (
          <div className="tablewrap" key={spec.slug}>
            <table>
              <thead>
                <tr>
                  <th scope="col">Interface</th>
                  <th scope="col" style={{ textAlign: "right" }}>
                    In
                  </th>
                  <th scope="col" style={{ textAlign: "right" }}>
                    Out
                  </th>
                  <th scope="col" style={{ width: "55%" }}>
                    In above, out below
                  </th>
                </tr>
              </thead>
              <tbody>
                {pairs.map(([up, down], i) => {
                  const name = pairName(up);
                  // The whole spec at another range, narrowed to this pair:
                  // the enlarged row keeps its range picker.
                  const fetchSeries = view.fetchSeries
                    ? async (next: Range) => {
                        const wide = await view.fetchSeries!(next);
                        return {
                          ...wide,
                          series: wide.series.filter(
                            (b) => pairName(b) === name,
                          ),
                        };
                      }
                    : undefined;
                  return (
                    <tr key={name}>
                      <th scope="row">{name}</th>
                      {/* The latest bucket, a gap included: a link on a host
                          that stopped reporting has no current rate. */}
                      <td style={{ textAlign: "right" }}>
                        {fmt(latestValue(up.values))}
                      </td>
                      <td style={{ textAlign: "right" }}>
                        {fmt(down ? latestValue(down.values) : null)}
                      </td>
                      <td>
                        <div ref={i === 0 ? ref : undefined}>
                          <Enlargeable
                            title={`${spec.title}: ${name}`}
                            unit={spec.unit}
                            series={down ? [up, down] : [up]}
                            fmt={spec.fmt}
                            mirrored
                            window={view.res?.window ?? null}
                            range={range}
                            ranges={RAIL_RANGES}
                            fetchSeries={fetchSeries}
                            label={`${name}: open the chart`}
                          >
                            <Chart
                              series={down ? [up, down] : [up]}
                              width={width}
                              height={ROW_HEIGHT}
                              max={Math.max(peak.up, peak.down)}
                              mark="mirror"
                              mirrorPeak={peak}
                              label={`${name} traffic over time`}
                              gapRule
                            />
                          </Enlargeable>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        );
      })}
    </section>
  );
}
