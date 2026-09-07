import { Table, type TableProps } from "../../ui/Table";
import { FleetEmptyState } from "./fleetEmptyState";
import { hostColumns, type HostRow, type Range } from "./hostColumns";

export interface HostTableProps {
  rows: readonly HostRow[];
  range: Range;
  /**
   * How bad the worst thing on this host is -- which hosts to look at.
   *
   * This used to draw a coloured rail down the row's leading edge AS WELL as
   * the pill beside the hostname, which is the same fact stated twice in one
   * row: a hue with no word, and a word with the same hue in its dot. The
   * rail was the older of the two and the weaker -- it could only be a
   * colour, so it needed a screen-reader-only word smuggled into the first
   * cell to mean anything without hue, and by the time a row said "critical"
   * in a pill that span was announcing the severity a second time.
   *
   * The pill is what is left, and it is the better mark: it names the
   * severity in a word every reader gets, and it sits beside the hostname
   * rather than at an edge the eye passes on its way in. Table still owns a
   * rail for the tables whose rows carry no such word (see rowSeverity there
   * -- the host page's inventory tables still use it, and the fleet's own
   * container list rails on memory pressure, which no badge in that row
   * states).
   *
   * Typed off Table's own prop so the two cannot drift apart while a caller
   * still hands this straight to a column.
   */
  severity?: TableProps<HostRow>["rowSeverity"];
  /** The instant the page is reading itself at -- the same one `severity` was
   * derived against, so a row's pill cannot judge a host still reporting by
   * one clock and its conditions by another. */
  now?: Date;
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
  now,
  filtered = false,
}: HostTableProps) {
  if (rows.length === 0) {
    // An empty <table> renders as a bare header rail, which reads as a
    // loading glitch rather than as "this hub has no hosts yet".
    return <FleetEmptyState filtered={filtered} />;
  }

  return (
    <Table
      columns={hostColumns(range, severity, now)}
      rows={rows}
      rowKey={(row) => row.id}
    />
  );
}
