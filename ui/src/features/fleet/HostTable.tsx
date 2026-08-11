import { Server } from "lucide-react";
import { EmptyState } from "../../ui/EmptyState";
import { Table } from "../../ui/Table";
import { hostColumns, type HostRow, type Range } from "./hostColumns";

export interface HostTableProps {
  rows: readonly HostRow[];
  range: Range;
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
export function HostTable({ rows, range }: HostTableProps) {
  if (rows.length === 0) {
    // An empty <table> renders as a bare header rail, which reads as a
    // loading glitch rather than as "this hub has no hosts yet". The fleet
    // page's own empty state is the onboarding path, so it says so.
    return (
      <EmptyState
        icon={Server}
        title="No hosts yet"
        body="Once an agent reports in, its host appears here."
      />
    );
  }

  return (
    <Table columns={hostColumns(range)} rows={rows} rowKey={(row) => row.id} />
  );
}
