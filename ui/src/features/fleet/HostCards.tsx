import { Server } from "lucide-react";
import { EmptyState } from "../../ui/EmptyState";
import { hostColumns, type HostRow, type Range } from "./hostColumns";

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
    // Same empty state as the table's, deliberately: which density toggle a
    // browser happens to remember must not change what an empty fleet says.
    return (
      <EmptyState
        icon={Server}
        title="No hosts yet"
        body="Once an agent reports in, its host appears here."
      />
    );
  }

  const [host, ...metrics] = hostColumns(range);

  return (
    <div className="cards">
      {rows.map((row) => (
        <article className="hcard" key={row.id}>
          <header>{host!.cell(row)}</header>
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
