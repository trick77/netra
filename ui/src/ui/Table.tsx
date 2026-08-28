import {
  useId,
  useMemo,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { ChevronRight } from "lucide-react";

/**
 * A single column definition for Table.
 *
 * It was once shared with a host card grid, so that a row and a card
 * rendered from the same Column[] and could not drift apart. That grid is
 * gone; two of the constraints it imposed are kept deliberately, because
 * both earn their place without it:
 *
 *  - `key` is a plain string, not `keyof T`: computed columns (a status
 *    badge, a sparkline) have no backing field on the row, and typing this
 *    to `keyof T` would make those columns impossible to express.
 *  - `header` is a plain string, not ReactNode: it is read verbatim by the
 *    sort control's accessible name and by every test that finds a column by
 *    its title, and a node would be unusable in both.
 */
export interface Column<T> {
  key: string;
  header: string;
  /** CSS width (e.g. "120px"). */
  width?: string;
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
   * The severity of a row, drawn as a rail down its leading edge.
   *
   * A list exists to be scanned, and a reader scanning one is asking "which
   * of these should I look at" before they read any cell. The badge inside
   * the Host cell answers that only once the eye has already stopped on the
   * row; a rail answers it from the edge of the table, at a glance, down the
   * whole column at once.
   *
   * Colour is never the whole answer -- every row this marks also carries a
   * badge with a word in it, which is what a reader who cannot separate amber
   * from red is reading. The rail is a second channel on a fact already
   * stated, never the only one.
   *
   * Returning null (or omitting the prop) draws no rail, which is what an
   * ordinary row gets: a table where every row is marked has marked nothing.
   */
  rowSeverity?: (row: T) => "warning" | "serious" | "critical" | null;
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
    /**
     * A reading for the WHOLE group, drawn at the right end of its header.
     *
     * The point of a collapsed group: shut, a heading that says only
     * "immich · 4 containers" answers what is in there and nothing about what
     * any of it is doing, so closing one costs the reader the very thing the
     * list existed to show. A summary is what makes collapsed the honest
     * default rather than a way of hiding data.
     *
     * Handed the group's rows, not a precomputed value, because only the
     * caller knows what summing its own rows means -- and it is given the
     * rows that are actually IN the group, which under a filter is the rows
     * that survived it.
     */
    summary?: (key: string, rows: readonly T[]) => ReactNode;
    /**
     * The group's name as plain text, for the disclosure button's accessible
     * name.
     *
     * `label` returns a node -- a link, a count, sometimes a badge -- and the
     * key is not always readable either: the fleet groups on `host_id`, so its
     * key is "7". Neither can name a control. Falls back to the key, which is
     * right for the lists whose key IS the name.
     */
    labelText?: (key: string, rows: readonly T[]) => string;
    /**
     * Makes every group a disclosure, OPEN to begin with.
     *
     * It began closed, and the argument was that the lists which group are the
     * long ones -- fourteen containers in four stacks opening onto four lines
     * instead of eighteen. What that actually shipped was a Containers tab
     * that arrives showing nothing at all: a column of hostnames, no
     * containers, and no clue that the data is one click away per host. A list
     * that hides its own contents on arrival has not summarised them, it has
     * hidden them.
     *
     * Open by default, and the disclosure stays for the reader who wants to
     * fold a noisy host away. Only lists that carry a `summary` should ask for
     * this -- see above.
     */
    collapsible?: boolean;
    /**
     * Forces every group open, without discarding what the reader had opened.
     *
     * This is what a search box sets. Rows arrive already filtered, so a group
     * still standing is a group with a hit in it, and a hit inside a closed
     * group is a hit the reader cannot see. Clearing the filter drops back to
     * the state they left, because that state was never overwritten.
     */
    forceExpanded?: boolean;
  };
  /**
   * The order this list is worth reading in, before anyone clicks a header.
   *
   * Names a column's `key` and a direction, and only seeds the initial state:
   * the reader's first click on any header takes it from there, and nothing
   * puts it back. A list whose most useful order is not alphabetical -- the
   * packages list, where "what changed last" is the question -- should say so
   * rather than make every reader sort it by hand on arrival.
   */
  defaultSort?: SortState;
}

export type SortState = { key: string; dir: "asc" | "desc" };

/** What each rail hue means, in a word. Only the three severities a rail is
 * ever drawn for -- see rowSeverity. */
const SEVERITY_WORD: Record<"warning" | "serious" | "critical", string> = {
  warning: "Warning",
  serious: "Serious",
  critical: "Critical",
};

export function Table<T>({
  columns,
  rows,
  rowKey,
  rowSeverity,
  groupBy,
  defaultSort,
}: TableProps<T>) {
  // Uncontrolled: every caller wants the same click-to-sort behaviour, and
  // threading identical state through each of them buys nothing. defaultSort
  // seeds it once; a caller that changes the prop later is not answered,
  // because by then the order on screen is the reader's, not the caller's.
  const [sort, setSort] = useState<SortState | null>(defaultSort ?? null);
  // Which direction the current column's cycle began in. See toggle.
  const [cycleStart, setCycleStart] = useState<SortState["dir"]>(
    defaultSort?.dir ?? "asc",
  );

  // The groups the reader has CLOSED, not the ones they opened. Collapsible
  // groups start open, so the empty set is the initial state and no group key
  // has to exist before it can be tracked -- the same property the inverse set
  // had when they started closed, which is why this is a rename of the state
  // and not a new one. A group that disappears (a project whose last container
  // went away) leaves a stale key behind, which costs a string and means the
  // group comes back folded the way it was left.
  const [closed, setClosed] = useState<ReadonlySet<string>>(new Set());
  // One id per table instance; each group header's aria-controls points at
  // its own tbody, built from it.
  const tableId = useId();

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

  // Three stops per column, and the cycle starts where the column already
  // stands: ascending for one the reader clicked, and the caller's own
  // direction for the column defaultSort seeded. Starting the seeded column
  // at "asc" regardless would make its FIRST click the third stop -- the
  // packages list, which arrives newest-changed first, would answer a click
  // on that header by clearing the sort and dropping back to the server's
  // name order, and oldest-first would cost three clicks.
  const toggle = (key: string) => {
    if (sort === null || sort.key !== key) {
      setCycleStart("asc");
      setSort({ key, dir: "asc" });
      return;
    }
    if (sort.dir === cycleStart) {
      setSort({ key, dir: cycleStart === "asc" ? "desc" : "asc" });
      return;
    }
    // Last stop clears it, back to the order the caller gave -- which for the
    // fleet is the server's own ordering, and is a state a reader otherwise
    // cannot get back to without a reload.
    setSort(null);
  };

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
                    // textContent -- where every test reading a column name
                    // would find "Host".
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
          groups.map(([key, rowsInGroup]) => {
            const collapsible = groupBy?.collapsible === true;
            // The filter WINS over the reader's choice while it is on, and
            // `closed` is never written while it is on (the toggle below
            // refuses), so clearing the box restores exactly what they had.
            const forced = groupBy?.forceExpanded === true;
            const open = !collapsible || forced || !closed.has(key);
            const bodyId = `${tableId}-${key}`;
            return (
              <tbody key={key} id={bodyId}>
                <tr className="grouprow">
                  {/* A header for the rows below it, so scope is rowgroup
                      rather than col -- it names the group, not a column. */}
                  <th scope="rowgroup" colSpan={columns.length}>
                    <div className="ghead">
                      {/* The button holds ONLY the chevron; the label sits
                          beside it in normal flow. It has to, because a group
                          label is allowed to contain a link -- the fleet's
                          hostname is one -- and an anchor inside a button is
                          neither valid nor operable. The whole header is still
                          the click target: .gtoggle::before stretches over the
                          cell, and the label's own link is raised above it.
                          See index.css. */}
                      {collapsible ? (
                        <button
                          type="button"
                          className="gtoggle"
                          aria-expanded={open}
                          aria-controls={bodyId}
                          onClick={() => {
                            // Nothing while the filter forces every group
                            // open: the group cannot move, and recording a
                            // click that changed nothing would hand the
                            // reader a different list when they clear the
                            // box -- the one thing forceExpanded promises
                            // not to do.
                            if (forced) return;
                            setClosed((prev) => {
                              const next = new Set(prev);
                              if (next.has(key)) next.delete(key);
                              else next.add(key);
                              return next;
                            });
                          }}
                        >
                          <ChevronRight className="chev" aria-hidden="true" />
                          {/* The button's accessible name. The heading beside
                              it is a heading, not a label for this control,
                              and "immich" alone would not say what the button
                              does. */}
                          <span className="sr-only">
                            {groupBy?.labelText?.(key, rowsInGroup) ?? key}
                          </span>
                        </button>
                      ) : null}
                      <span className="glabel">
                        {groupBy?.label(key, rowsInGroup)}
                      </span>
                      {groupBy?.summary ? (
                        <span className="gsummary">
                          {groupBy.summary(key, rowsInGroup)}
                        </span>
                      ) : null}
                    </div>
                  </th>
                </tr>
                {/* Not rendered at all rather than hidden with CSS: a closed
                    group's rows are off the page for a screen reader and for
                    ctrl-F alike, which is what "collapsed" means. */}
                {open ? rowsInGroup.map(bodyRow) : null}
              </tbody>
            );
          })
        )}
      </table>
    </div>
  );

  function bodyRow(row: T, index: number) {
    // The rail rides the row's class, not a cell's: it is a statement about
    // the row, and index.css paints it as an inset shadow on the first cell so
    // it needs no column of its own and cannot shift the layout.
    const severity = rowSeverity?.(row) ?? null;
    return (
      <tr
        key={rowKey ? rowKey(row, index) : index}
        className={severity === null ? undefined : `rail rail-${severity}`}
      >
        {columns.map((col, i) => (
          <td key={col.key} style={cellStyle(col)}>
            {/* The word the colour stands for, for a reader who gets no
                colour. The rail is a hue and nothing else, and the row's own
                badge cannot be relied on to carry the severity: a fleet host
                railed for OOM kills says "online" in its badge, or says
                nothing at all. Said once, in the first cell, where a row is
                read from. */}
            {i === 0 && severity !== null ? (
              <span className="sr-only">{SEVERITY_WORD[severity]}</span>
            ) : null}
            {col.cell(row)}
          </td>
        ))}
      </tr>
    );
  }
}
