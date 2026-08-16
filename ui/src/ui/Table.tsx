import { useMemo, useState, type CSSProperties, type ReactNode } from "react";

/**
 * A single column definition shared by Table (this file) and, in later
 * tasks, the host card grid — a row and a card render from the same
 * Column[] so they cannot drift apart.
 *
 * Design rule: every REQUIRED field must be meaningful to both a table row
 * and a card tile. Table-only affordances (width, align) are optional so
 * the card grid can simply ignore them.
 *
 *  - `key` is a plain string, not `keyof T`: computed columns (a status
 *    badge, a sparkline) have no backing field on the row, and typing this
 *    to `keyof T` would make those columns impossible to express.
 *  - `header` is a plain string, not ReactNode: the card grid renders it as
 *    a label in `.hcard .mk` and may need it verbatim in a `title`/
 *    aria-label. A ReactNode header would be unusable there, so this field
 *    stays string on purpose even though it's the one most likely to see a
 *    "can I pass a node" request later.
 */
export interface Column<T> {
  key: string;
  header: string;
  /** CSS width (e.g. "120px"); table-only, ignored by the card grid. */
  width?: string;
  /** Table-only text alignment; ignored by the card grid. */
  align?: "left" | "center" | "right";
  cell: (row: T) => ReactNode;
  /**
   * Makes this column sortable, and says what to sort on.
   *
   * A separate accessor rather than reading the cell: a cell is a sparkline,
   * a badge or a meter, and none of those has an order. Returning null puts
   * a row in the "unknown" group, which always sorts last regardless of
   * direction -- a host with no uptime reading is not the shortest-lived
   * host on the page, and flipping the arrow must not promote it to the top.
   */
  sortValue?: (row: T) => string | number | null;
}

export interface TableProps<T> {
  columns: readonly Column<T>[];
  rows: readonly T[];
  /** Stable row identity for React's list reconciliation. Falls back to
   * the row's index when omitted, which is fine for static data but will
   * misbehave once rows are sorted or filtered — callers with either
   * should always pass this. */
  rowKey?: (row: T, index: number) => string | number;
  /**
   * Splits the rows into labelled groups, each its own `<tbody>` under a
   * header row.
   *
   * A long flat list answers "what is here" and nothing about what belongs
   * with what: 200 containers under one header rail is a wall. Grouping is
   * the answer, and it belongs here rather than in each caller because a
   * group has to interact with sorting -- see below -- and only this
   * component knows the sort state.
   *
   * `key` returning "" puts a row in the trailing unnamed group: a container
   * with no compose project is not a member of a project called "", and it
   * must not sort in among the named ones.
   */
  groupBy?: {
    key: (row: T) => string;
    label: (key: string, rows: readonly T[]) => ReactNode;
    /**
     * What to ORDER the groups by, when that is not the key itself.
     *
     * Identity and order are usually the same string and this can be left
     * out. They come apart whenever the key is an id: the fleet groups
     * containers by `host_id`, because two hosts in different sites may share
     * a hostname and grouping on the name would merge them -- but ordering by
     * that id lists the hosts in registration order under headings that read
     * as names, beside a Hosts tab the API returns alphabetically. The group
     * a row belongs to and the place that group sits are two questions.
     *
     * The empty key still sorts last regardless: "no group" is not a value to
     * be ordered among the real ones.
     */
    order?: (key: string, rows: readonly T[]) => string | number;
  };
}

type SortState = { key: string; dir: "asc" | "desc" };

export function Table<T>({ columns, rows, rowKey, groupBy }: TableProps<T>) {
  // Uncontrolled: every caller wants the same click-to-sort behaviour, and
  // threading identical state through each of them buys nothing.
  const [sort, setSort] = useState<SortState | null>(null);

  const sorted = useMemo(() => {
    if (sort === null) return rows;
    const col = columns.find((c) => c.key === sort.key);
    if (col?.sortValue === undefined) return rows;
    const read = col.sortValue;
    const sign = sort.dir === "asc" ? 1 : -1;
    // Sorting a COPY: mutating the caller's array in place would reorder
    // state it still owns, and React would not know it had changed.
    return [...rows].sort((a, b) => {
      const x = read(a);
      const y = read(b);
      // Unknown sorts last in BOTH directions -- see sortValue's doc.
      if (x === null && y === null) return 0;
      if (x === null) return 1;
      if (y === null) return -1;
      if (typeof x === "string" || typeof y === "string") {
        // localeCompare with numeric: "host-2" before "host-10", which is
        // what a reader scanning hostnames expects.
        return (
          sign *
          String(x).localeCompare(String(y), undefined, { numeric: true })
        );
      }
      return sign * (x - y);
    });
  }, [rows, columns, sort]);

  // Partitioning the ALREADY sorted list is what makes "sort within a group"
  // fall out for free: a stable partition of a sorted list leaves every
  // bucket in that same order. Sorting each group separately afterwards would
  // be the same answer computed twice.
  //
  // The groups themselves are ordered by `order` -- the key when the caller
  // gave none -- rather than by first appearance, so two renders of the same
  // data read the same way. The unnamed group ("") sorts last whatever its
  // order value, the way an unknown sortValue does.
  const groups = useMemo(() => {
    if (groupBy === undefined) return null;
    const byKey = new Map<string, T[]>();
    for (const row of sorted) {
      const key = groupBy.key(row);
      const existing = byKey.get(key);
      if (existing) existing.push(row);
      else byKey.set(key, [row]);
    }
    const order = groupBy.order ?? ((key: string) => key);
    return [...byKey.entries()].sort(([a, ra], [b, rb]) => {
      if (a === b) return 0;
      if (a === "") return 1;
      if (b === "") return -1;
      const x = order(a, ra);
      const y = order(b, rb);
      if (typeof x === "number" && typeof y === "number") return x - y;
      // numeric, so "web-2" comes before "web-10" -- the same collation the
      // column sort uses, because a reader scanning names expects one rule.
      return String(x).localeCompare(String(y), undefined, { numeric: true });
    });
  }, [sorted, groupBy]);

  const toggle = (key: string) =>
    setSort((prev) =>
      prev === null || prev.key !== key
        ? { key, dir: "asc" }
        : prev.dir === "asc"
          ? { key, dir: "desc" }
          : // Third click clears it, back to the order the caller gave --
            // which for the fleet is the server's own ordering, and is a
            // state a reader otherwise cannot get back to without a reload.
            null,
    );

  const cellStyle = (col: Column<T>): CSSProperties | undefined => {
    if (!col.width && !col.align) return undefined;
    return {
      ...(col.width ? { width: col.width } : {}),
      ...(col.align ? { textAlign: col.align } : {}),
    };
  };

  // The wrapper is what keeps a wide table from widening the PAGE. Without
  // it the Events and Inventory tables push the document sideways below the
  // mobile breakpoint, which moves the nav and every other element on the
  // page with them; the rule existed in the stylesheet with nothing emitting
  // the class.
  return (
    <div className="tablewrap">
      <table>
        <thead>
          <tr>
            {columns.map((col) => (
              <th
                key={col.key}
                scope="col"
                style={cellStyle(col)}
                aria-sort={
                  sort?.key === col.key
                    ? sort.dir === "asc"
                      ? "ascending"
                      : "descending"
                    : undefined
                }
              >
                {col.sortValue === undefined ? (
                  col.header
                ) : (
                  <button
                    type="button"
                    className="th-sort"
                    // The arrow is drawn by CSS from this attribute rather
                    // than rendered as a text node: it is decoration, the
                    // state it shows is already on the th as aria-sort, and
                    // as a DOM node it lands inside the header's own
                    // textContent -- where every test and the card grid
                    // reading a column name would find "Host".
                    data-sort={sort?.key === col.key ? sort.dir : "none"}
                    onClick={() => toggle(col.key)}
                  >
                    {col.header}
                  </button>
                )}
              </th>
            ))}
          </tr>
        </thead>
        {groups === null ? (
          <tbody>{sorted.map(bodyRow)}</tbody>
        ) : (
          groups.map(([key, rowsInGroup]) => (
            <tbody key={key}>
              <tr className="grouprow">
                {/* A header for the rows below it, so scope is rowgroup
                    rather than col -- it names the group, not a column. */}
                <th scope="rowgroup" colSpan={columns.length}>
                  {groupBy?.label(key, rowsInGroup)}
                </th>
              </tr>
              {rowsInGroup.map(bodyRow)}
            </tbody>
          ))
        )}
      </table>
    </div>
  );

  function bodyRow(row: T, index: number) {
    return (
      <tr key={rowKey ? rowKey(row, index) : index}>
        {columns.map((col) => (
          <td key={col.key} style={cellStyle(col)}>
            {col.cell(row)}
          </td>
        ))}
      </tr>
    );
  }
}
