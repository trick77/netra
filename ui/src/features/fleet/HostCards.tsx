import { FleetEmptyState } from "./fleetEmptyState";
import { sortRows } from "../../ui/Table";
import type { MarkKind } from "../../ui/SeverityMark";
import {
  byAttention,
  CpuCell,
  DiskCell,
  HostCell,
  LastSeenCell,
  MemoryCell,
  TrafficCell,
  hostColumns,
  marksOf,
  type FindingsOf,
  type HostRow,
  type Range,
} from "./hostColumns";

/**
 * How the card view orders its hosts. A card grid has no column headers to
 * click, so the order is one control above it; each option reads the same
 * sortValue the table's column sorts on, so "CPU" means the same order in
 * both views.
 */
export type CardSort =
  "status" | "name" | "cpu" | "memory" | "disk" | "traffic";

export const CARD_SORTS: readonly { value: CardSort; label: string }[] = [
  { value: "status", label: "Status" },
  { value: "name", label: "Name" },
  { value: "cpu", label: "CPU" },
  { value: "memory", label: "Memory" },
  { value: "disk", label: "Filesystem" },
  { value: "traffic", label: "Traffic" },
];

export interface HostCardsProps {
  rows: readonly HostRow[];
  range: Range;
  severity?: (row: HostRow) => "warning" | "critical" | null;
  sporadic?: (row: HostRow) => boolean;
  findings?: FindingsOf;
  now?: Date;
  filtered?: boolean;
  sort?: CardSort;
}

/**
 * The fleet as one card per host: the default view.
 *
 * The card's parts ARE the table's cells -- HostCell, the four readings in
 * their card layout, LastSeenCell -- so the two views cannot say different
 * things about a host. That was the reason the earlier card grid was
 * removed: it was a second rendering kept in step by hand. A card buys the
 * trends the card's whole width, which is where they become readable; the
 * table stays one click away for comparing a column down the fleet.
 */
export function HostCards({
  rows,
  range,
  severity,
  sporadic,
  findings,
  now,
  filtered = false,
  sort = "status",
}: HostCardsProps) {
  if (rows.length === 0) {
    return <FleetEmptyState filtered={filtered} />;
  }

  const markFor = marksOf(rows, severity, sporadic, now);
  const ordered = orderCards(rows, sort, range, markFor);

  return (
    <div className="hcards">
      {ordered.map((row) => {
        const mark = markFor(row);
        return (
          <article
            key={row.id}
            className={mark === null ? "hcard" : `hcard ${mark}`}
            aria-label={row.hostname}
          >
            <header className="hcard-head">
              <HostCell
                row={row}
                worst={severity}
                sporadic={sporadic}
                findings={findings}
                now={now}
              />
              <span className="hcard-seen">
                <LastSeenCell row={row} now={now} />
              </span>
            </header>
            <CpuCell row={row} range={range} layout="card" />
            <MemoryCell row={row} range={range} layout="card" />
            <DiskCell row={row} range={range} layout="card" />
            <TrafficCell row={row} range={range} layout="card" />
          </article>
        );
      })}
    </div>
  );
}

/**
 * Status first is the default, in the order the table's "Needs attention"
 * group uses (byAttention): critical, then warning, then the rest in the
 * API's order. Every other choice is that column's own sortValue through
 * Table's own sortRows -- names ascending, readings descending, a host with
 * no reading last -- so "CPU" means the same order in both views.
 */
export function orderCards(
  rows: readonly HostRow[],
  sort: CardSort,
  range: Range,
  mark: (row: HostRow) => MarkKind | null,
): readonly HostRow[] {
  if (sort === "status") return byAttention(rows, mark);
  return sortRows(rows, hostColumns(range), {
    key: sort === "name" ? "host" : sort,
    dir: sort === "name" ? "asc" : "desc",
  });
}
