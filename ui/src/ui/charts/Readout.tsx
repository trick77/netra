// What the hovered panel says about the bucket under the pointer.
//
// Every series by name, with the swatch carrying its identity and the text in
// ink: the hue is a key into the chart, never the reading itself. A bucket
// with a peak envelope says so beside its mean, and a stack adds its total,
// which no single band states.
import { ABSENT } from "../../lib/format";
import type { ChartSeries } from "./Chart";
import { timeLabel } from "./ticks";
import type { TimedEvent, Window } from "./cursor";

/** A readout taller than the panel it floats over hides the chart it
 * describes; the enlarged view's table names every series. */
const MAX_ROWS = 8;

export interface ReadoutProps {
  series: ChartSeries[];
  index: number;
  /** The moment the bucket stands for, epoch ms. */
  at: number;
  window: Window;
  fmt?: (n: number | null) => string;
  /** Add the bucket's total: a stack's height is a reading of its own. */
  total?: boolean;
  /** Where the crosshair sits, 0-1 across the plot, so the box can sit on
   * the side with room rather than over the bucket it describes. */
  fraction: number;
  /** Events close to the cursor, listed under the readings: the likely cause
   * of what the cursor rests on. */
  events?: readonly TimedEvent[];
}

export function Readout({
  series,
  index,
  at,
  window,
  fmt,
  total = false,
  fraction,
  events = [],
}: ReadoutProps) {
  const show = (v: number | null) =>
    fmt ? fmt(v) : v === null ? ABSENT : String(v);
  const span = Date.parse(window.to) - Date.parse(window.from);

  let sum: number | null = null;
  if (total) {
    for (const s of series) {
      const v = s.values[index] ?? null;
      if (v !== null) sum = (sum ?? 0) + v;
    }
  }

  // Beside the crosshair, on whichever side has more room.
  const side =
    fraction > 0.5
      ? { right: `${(1 - fraction) * 100}%` }
      : { left: `${fraction * 100}%` };

  return (
    <div className="readout" style={side} role="status">
      <div className="rt">{timeLabel(new Date(at), span)}</div>
      {series.slice(0, MAX_ROWS).map((s) => {
        const v = s.values[index] ?? null;
        const p = s.band?.[index] ?? null;
        return (
          <div className="rr" key={s.name}>
            <span>
              <i style={{ background: s.color }} />
              {s.name}
            </span>
            <span>
              <b>{v === null ? "no reading" : show(v)}</b>
              {p !== null && p !== v && <small>peak {show(p)}</small>}
            </span>
          </div>
        );
      })}
      {series.length > MAX_ROWS && (
        <div className="rr more">
          {series.length - MAX_ROWS} more in the enlarged view
        </div>
      )}
      {total && sum !== null && (
        <div className="rr">
          <span>total</span>
          <b>{show(sum)}</b>
        </div>
      )}
      {events.map((e, i) => (
        <div className={`rr ev ${e.severity}`} key={i}>
          <span>{timeLabel(new Date(e.at), span)}</span>
          <b>{e.label}</b>
        </div>
      ))}
    </div>
  );
}
