// The card that wraps a chart with a title, unit, latest value, legend and
// (when applicable) either a "not collected" state or a clamped-window
// notice. `unavailable` is a product requirement, not a nicety: three
// metric families (IP statistics, ICMP statistics, ICMP informational) have
// no columns in the schema at all, and netra's collector contract is that
// something which cannot run says why -- this panel is where that
// contract reaches the UI.
import { ABSENT } from "../../lib/format";
import { extent } from "./geometry";
import type { OverlaySeries } from "./Overlay";
import { Overlay } from "./Overlay";

/** A single plotted series. Re-exported as `Band` because that is the name
 * the task brief uses for ChartPanel's `series` prop. */
export type Band = OverlaySeries;

export interface ChartPanelProps {
  title: string;
  unit?: string;
  series?: Band[];
  max?: number;
  fmt?: (n: number | null) => string;
  /** The sentence from windowNotice() (lib/metrics.ts) explaining a served
   * window clamped by retention or materialisation lag. Rendered verbatim
   * so a clamped range never reads as missing data. */
  notice?: string | null;
  /** The reason this metric family has no data at all, e.g. "no ICMP
   * columns in the schema". Presence of this prop switches the whole panel
   * to the dashed not-collected state instead of drawing an empty chart. */
  unavailable?: string;
  width?: number;
  height?: number;
  highlight?: string;
}

export function ChartPanel({
  title,
  unit,
  series = [],
  max,
  fmt,
  notice,
  unavailable,
  width = 260,
  height = 64,
  highlight,
}: ChartPanelProps) {
  if (unavailable !== undefined) {
    return (
      <section className="smp na" aria-label={`${title}, not collected`}>
        <div className="t">
          <h4>{title}</h4>
        </div>
        <div className="box">
          <span>Not collected</span>
          <span>{unavailable}</span>
        </div>
      </section>
    );
  }

  const { max: autoMax } = extent(series.flatMap((s) => s.values));
  const effectiveMax = max ?? autoMax;

  // The value at the LATEST bucket, trailing nulls included. Filtering the
  // nulls out first and taking the last survivor reported the last value
  // that ever arrived: a host that stopped reporting two buckets ago drew
  // its hole correctly and then printed "43" in bold beside it as the
  // current reading. "The agent is down" must never render as "CPU is at
  // 43" -- absent is absent, never the last number we happen to have.
  const latest = series[0]?.values.at(-1) ?? null;
  const nowText = fmt ? fmt(latest) : (latest?.toString() ?? ABSENT);
  // With more than one series the headline is series[0]'s alone, so it says
  // whose it is. A Network panel printing rx's number under a bare unit
  // reads as the panel's total.
  const nowLabel = series.length > 1 ? series[0]?.name : undefined;

  return (
    <section className="smp" aria-label={`${title} chart`}>
      <div className="t">
        <h4>{title}</h4>
        <span className="now">
          {nowLabel ? `${nowLabel} ` : ""}
          {nowText}
        </span>
        {unit && <span className="u">{unit}</span>}
      </div>
      <div className="chartwrap">
        <Overlay
          series={series}
          max={effectiveMax}
          width={width}
          height={height}
          highlight={highlight}
          label={`${title} over time`}
        />
      </div>
      {notice && <p className="note">{notice}</p>}
    </section>
  );
}
