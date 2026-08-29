// The column of mini-charts down the right of an enlarged chart, one per
// window, and the enlarged view's range picker.
//
// It replaces a row of Segmented buttons, and the reason is what a range
// button could never say. "30d" is a label; it tells a reader the window
// exists and nothing about whether anything happened in it. Answering "when
// did this start" through that control means picking a window, waiting,
// reading, picking the next one, and holding all of them in your head --
// which is the work an operator opened the chart to avoid.
//
// Drawn, the whole ladder is one glance. A step that appears in the 30d tile
// and not in the 7d one dates itself; a fan that has been climbing for a
// year is a shape in the 12mo tile and is invisible in every window narrower
// than it. The tile is then also the button for looking closer, so the thing
// a reader noticed is the thing they click.
import { Chart, markFor } from "./Chart";
import type { OverlaySeries } from "./Overlay";
import { scaleFor } from "./scale";
import { peak } from "./ChartDetail";
import { SPARK_HEIGHT, SPARK_WIDTH } from "./size";
import type { Range } from "../../lib/range";
import type { DetailData } from "./Enlargeable";

/** One tile's data and the state of the request that fetched it. */
export interface RailEntry {
  data: DetailData | null;
  loading: boolean;
  error: string | null;
}

export interface RangeRailProps {
  /** The ladder, ascending. */
  ranges: readonly Range[];
  /** The window the big chart is currently showing, or undefined when the
   * dialog was opened at a range the ladder does not carry -- the fleet's
   * 12h. No tile is pressed then, which is true: none of them is what is on
   * screen. */
  active: Range | undefined;
  onPick: (range: Range) => void;
  /** Each range's series. A range still in flight, or one that failed, has
   * no `data` and draws an empty tile with a word in it rather than a
   * chart. */
  entries: Partial<Record<Range, RailEntry>>;
  /** The mark, forwarded so a tile draws what the big chart draws. A
   * memory tile is a stack and a traffic tile is mirrored, because those
   * ARE those charts -- a rail of plain lines would preview a chart that
   * does not exist. */
  filled?: boolean;
  stacked?: boolean;
  mirrored?: boolean;
  /** The scaling policy, forwarded for the same reason and applied by the
   * same function the big figure uses. See scale.ts. */
  autoScale?: boolean;
  min?: number;
  max?: number;
  /** Names the tiles: "CPU over the last 7d". */
  title: string;
}

export function RangeRail({
  ranges,
  active,
  onPick,
  entries,
  filled,
  stacked,
  mirrored,
  autoScale,
  min,
  max,
  title,
}: RangeRailProps) {
  return (
    <div className="cd-rail" role="group" aria-label={`${title}, by window`}>
      {ranges.map((range) => {
        const entry = entries[range];
        const series = entry?.data?.series ?? null;
        return (
          <button
            key={range}
            type="button"
            className="cd-rail-tile"
            aria-pressed={range === active}
            aria-label={`${title} over the last ${range}`}
            onClick={() => onPick(range)}
          >
            <span className="c">
              {series && series.length > 0 ? (
                <Tile
                  series={series}
                  filled={filled}
                  stacked={stacked}
                  mirrored={mirrored}
                  autoScale={autoScale}
                  min={min}
                  max={max}
                />
              ) : (
                <span className="n">
                  {entry?.error ? "failed" : entry?.loading ? "…" : "no data"}
                </span>
              )}
            </span>
            {/* Under the chart, not beside it. Beside cost the tile 3.2em of
                its width to a label that is at most four characters, and the
                chart is the thing being read -- the word is how you say which
                window it is afterwards. */}
            <span className="r">{range}</span>
          </button>
        );
      })}
    </div>
  );
}

/**
 * One tile's chart.
 *
 * Drawn at exactly the fleet sparkline's size, not at one of its own: the
 * two are the same picture asking the same question -- is anything happening
 * in this window -- and a reader moves between them, so a tile of a different
 * shape would read as a different chart.
 *
 * No grid, no spine, no labels and no cursor: at that size every piece of
 * furniture would be louder than the data. It is the same argument Sparkline
 * makes, and this draws through Chart rather than Sparkline for one reason --
 * Sparkline draws a single line, and a tile has to be able to be a stack or a
 * mirrored pair.
 */
function Tile({
  series,
  filled,
  stacked,
  mirrored,
  autoScale,
  min,
  max,
}: {
  series: OverlaySeries[];
  filled?: boolean;
  stacked?: boolean;
  mirrored?: boolean;
  autoScale?: boolean;
  min?: number;
  max?: number;
}) {
  // `series` is passed as the refetched set as well as the shown one: a tile
  // is only ever drawn from its OWN window's data, so the ceiling it is
  // fitted to has to account for that window's peak. Held to the opening
  // window's ceiling, the burst that makes a wide tile worth clicking is
  // drawn outside the box -- linePath never clamps -- and the tile looks
  // quiet.
  const span = scaleFor(series, series, {
    autoScale,
    min,
    max,
    stacked,
    mirrored,
  });
  return (
    <Chart
      series={series}
      width={SPARK_WIDTH}
      height={SPARK_HEIGHT}
      min={span.min ?? 0}
      max={span.max ?? peak(series, stacked, mirrored)}
      mark={markFor({ filled, stacked, mirrored })}
      pad={1}
    />
  );
}
