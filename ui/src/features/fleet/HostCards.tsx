import { hostColumns, type HostRow, type Range } from "./hostColumns";
import { FleetEmptyState } from "./fleetEmptyState";

// The one column a card renders as its header rather than as a tile -- a
// card names itself at the top instead of in a first cell.
const HEADER_COLUMN_KEY = "host";

export interface HostCardsProps {
  rows: readonly HostRow[];
  range: Range;
}

/**
 * The card rendering of the fleet list, and the reason Column<T> keeps
 * `header` a plain string: here it is a tile label, not a table heading.
 *
 * The first column (Host) becomes the card's header -- a card names itself
 * at the top rather than in a first cell -- and every remaining column
 * becomes one tile in the card's grid. Nothing is selected by key or index:
 * a column added to hostColumns() appears here without this file changing,
 * which is the entire point of the shared definition.
 *
 * The grid is two-up in CSS (.hcard .grid), so the four metric columns the
 * design specifies land as the 2x2 it asks for. A fifth column simply wraps
 * onto a third row rather than being dropped -- dropping it is the failure
 * mode this contract exists to prevent.
 */
export function HostCards({ rows, range }: HostCardsProps) {
  if (rows.length === 0) {
    // The table's empty state, not a copy of it.
    return <FleetEmptyState />;
  }

  // The header column is found by key, never by position: a column added at
  // the front of hostColumns would otherwise silently become the card title
  // instead of a tile.
  const columns = hostColumns(range);
  const host = columns.find((col) => col.key === HEADER_COLUMN_KEY);
  const metrics = columns.filter((col) => col.key !== HEADER_COLUMN_KEY);

  return (
    <div className="cards">
      {rows.map((row) => (
        // The article carries the hostname as its accessible name. The
        // table gets its structure free from <th scope="col">; an unnamed
        // article gives a screen-reader user a nameless region holding a
        // nameless div -- and cards are automatic below the mobile
        // breakpoint, so this is not an opt-in path anyone chose.
        <article className="hcard" key={row.id} aria-label={row.hostname}>
          <header>{host?.cell(row)}</header>
          <div className="grid">
            {metrics.map((col) => (
              <div className="m" key={col.key}>
                <div className="mk">{col.header}</div>
                {col.cell(row)}
              </div>
            ))}
          </div>
        </article>
      ))}
    </div>
  );
}
