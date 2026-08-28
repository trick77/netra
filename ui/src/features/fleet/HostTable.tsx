import { Table, type TableProps } from "../../ui/Table";
import { FleetEmptyState } from "./fleetEmptyState";
import { hostColumns, type HostRow, type Range } from "./hostColumns";

export interface HostTableProps {
  rows: readonly HostRow[];
  range: Range;
  /** The severity rail down a row's leading edge -- which hosts to look at,
   * read from the edge of the table before any cell is. Passed straight to
   * Table, which owns how a rail is drawn. */
  severity?: TableProps<HostRow>["rowSeverity"];
  /** True when this list is empty because something is filtering it rather
   * than because the hub has no hosts -- see FleetEmptyState. */
  filtered?: boolean;
}

/**
 * The rendering of the fleet list. It owns layout and nothing else: every
 * cell comes from hostColumns(). There is deliberately no per-column special
 * case in here -- a column that needed one would belong in hostColumns.
 *
 * Rows are keyed by host id rather than by position. The page above sorts
 * and filters this list, and two hosts in different sites may share a
 * hostname; with index keys React would reuse a row's DOM for a different
 * host, carrying one host's chart state onto another's data.
 */
export function HostTable({
  rows,
  range,
  severity,
  filtered = false,
}: HostTableProps) {
  if (rows.length === 0) {
    // An empty <table> renders as a bare header rail, which reads as a
    // loading glitch rather than as "this hub has no hosts yet".
    return <FleetEmptyState filtered={filtered} />;
  }

  return (
    <Table
      columns={hostColumns(range)}
      rows={rows}
      rowKey={(row) => row.id}
      rowSeverity={severity}
    />
  );
}
