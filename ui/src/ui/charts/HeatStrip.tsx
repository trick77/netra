// Many like series as rows of cells in one ink, brighter where busier.
//
// Built for the per-core CPU picture. A stack of thirty-two cores in
// thirty-two hues repainted every band when the count changed and had no
// legend past six; as rows of one ink, which core and when is a position on
// the page, and "how busy" is the one thing the brightness says.
import { useMemo, type ReactNode } from "react";
import { useMeasuredWidth } from "./useMeasuredWidth";
import { indexAt, useSharedCursor, type Window } from "./cursor";

export interface HeatRow {
  name: string;
  /** Percent, 0-100. A null cell is not drawn: no reading is not idle. */
  values: (number | null)[];
}

export interface HeatStripProps {
  rows: HeatRow[];
  label: string;
  /** The answered window, so the shared cursor lands on the right column. */
  window?: Window | null;
  rowHeight?: number;
}

/** Cells narrower than this smear into each other; fold buckets instead. */
const MIN_CELL = 3;
/** The faintest drawn cell: an idle core is still a row you can see. */
const FLOOR = 0.06;

/** The column a bucket is folded into: one formula for the cells and the
 * cursor, so the line cannot drift off the cell it marks. */
function columnOf(j: number, buckets: number, cols: number): number {
  return Math.min(cols - 1, Math.floor((j / buckets) * cols));
}

export function HeatStrip({
  rows,
  label,
  window = null,
  rowHeight = 7,
}: HeatStripProps) {
  const { ref, width } = useMeasuredWidth<HTMLDivElement>(600);
  const shared = useSharedCursor();
  const buckets = rows.reduce((n, r) => Math.max(n, r.values.length), 0);
  const cols = Math.max(1, Math.min(buckets, Math.floor(width / MIN_CELL)));
  const cell = width / cols;
  const height = rows.length * rowHeight;
  /** The column a bucket falls in. */

  // The cells, folded and drawn once per change of data or width -- not on
  // every pointer move, which re-renders this strip through the shared
  // cursor. An unchanged element array is one React does not walk again.
  // The peak of the buckets a column covers: a burst must survive the fold,
  // which is what the strip is for.
  const cells = useMemo(() => {
    const out: ReactNode[] = [];
    rows.forEach((r, ri) => {
      const folded: (number | null)[] = new Array(cols).fill(null);
      r.values.forEach((v, j) => {
        if (v === null) return;
        const c = columnOf(j, buckets, cols);
        if (folded[c] === null || v > (folded[c] as number)) folded[c] = v;
      });
      folded.forEach((v, c) => {
        if (v === null) return;
        out.push(
          <rect
            key={`${ri}:${c}`}
            data-cell
            x={c * cell}
            y={ri * rowHeight}
            width={Math.max(1, cell - 0.5)}
            height={rowHeight - 1}
            fill="var(--ink)"
            fillOpacity={
              FLOOR + (1 - FLOOR) * Math.min(1, Math.max(0, v / 100))
            }
          >
            <title>{`${r.name}: ${Math.round(v)}%`}</title>
          </rect>,
        );
      });
    });
    return out;
  }, [rows, cols, cell, buckets, rowHeight]);

  // On the CENTRE of the column holding the hovered bucket, so the line sits
  // over the cell that bucket was folded into rather than drifting from it
  // toward the right edge.
  const at = shared?.at ?? null;
  const index = indexAt(at, window, buckets);
  const cursorX =
    index === null ? null : (columnOf(index, buckets, cols) + 0.5) * cell;

  return (
    <div ref={ref} className="heatstrip">
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={label}
      >
        {cells}
        {cursorX !== null && (
          <line
            data-cursor
            x1={cursorX}
            x2={cursorX}
            y1={0}
            y2={height}
            stroke="var(--accent)"
            strokeWidth={1}
          />
        )}
      </svg>
    </div>
  );
}
