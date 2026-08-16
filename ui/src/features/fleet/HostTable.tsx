import { Table } from "../../ui/Table";
import { FleetEmptyState } from "./fleetEmptyState";
import {
  hostColumns,
  type AttentionView,
  type HostRow,
  type Range,
} from "./hostColumns";

export interface HostTableProps {
  rows: readonly HostRow[];
  range: Range;
  /** Present when the list is answering "what is wrong" rather than "how is
   * the fleet" -- see AttentionView. Passed straight through: which columns
   * a question needs is hostColumns' decision, not this file's. */
  attention?: AttentionView;
  /** True when this list is empty because something is filtering it rather
   * than because the hub has no hosts -- see FleetEmptyState. */
  filtered?: boolean;
}

/**
 * The table rendering of the fleet list. It owns layout and nothing else:
 * every cell comes from hostColumns(), which HostCards renders from too, so
 * the two cannot drift apart. There is deliberately no per-column special
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
  attention,
  filtered = false,
}: HostTableProps) {
  if (rows.length === 0) {
    // An empty <table> renders as a bare header rail, which reads as a
    // loading glitch rather than as "this hub has no hosts yet".
    return <FleetEmptyState filtered={filtered} />;
  }

  return (
    <Table
      columns={hostColumns(range, attention)}
      rows={rows}
      rowKey={(row) => row.id}
    />
  );
}
