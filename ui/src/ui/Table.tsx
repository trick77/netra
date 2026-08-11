import type { CSSProperties, ReactNode } from "react";

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
}

export interface TableProps<T> {
  columns: readonly Column<T>[];
  rows: readonly T[];
  /** Stable row identity for React's list reconciliation. Falls back to
   * the row's index when omitted, which is fine for static data but will
   * misbehave once rows are sorted or filtered — callers with either
   * should always pass this. */
  rowKey?: (row: T, index: number) => string | number;
}

export function Table<T>({ columns, rows, rowKey }: TableProps<T>) {
  const cellStyle = (col: Column<T>): CSSProperties | undefined => {
    if (!col.width && !col.align) return undefined;
    return {
      ...(col.width ? { width: col.width } : {}),
      ...(col.align ? { textAlign: col.align } : {}),
    };
  };

  return (
    <table>
      <thead>
        <tr>
          {columns.map((col) => (
            <th key={col.key} scope="col" style={cellStyle(col)}>
              {col.header}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((row, index) => (
          <tr key={rowKey ? rowKey(row, index) : index}>
            {columns.map((col) => (
              <td key={col.key} style={cellStyle(col)}>
                {col.cell(row)}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
