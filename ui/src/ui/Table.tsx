import {
  useId,
  useLayoutEffect,
  useMemo,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { ChevronRight } from "lucide-react";
import { ABSENT } from "../lib/format";

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
  rowSeverity?: (row: T) => "warning" | "critical" | null;
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
     * A reading for the WHOLE group, placed in the group header's own CELLS,
     * keyed by column.
     *
     * The point of a collapsed group: shut, a heading that says only
     * "immich · 4 containers" answers what is in there and nothing about what
     * any of it is doing, so closing one costs the reader the very thing the
     * list existed to show. This is what makes collapsed the honest default
     * rather than a way of hiding data.
     *
     * IN THE COLUMNS, which is the whole difference from the `summary` this
     * replaces. That drew the group's figures as spans floating after its
     * name, so a heading's memory total and the memory readings underneath it
     * sat in different places and could not be compared by eye. Returning
     * `{ cpu: <...>, memory: <...> }` puts the group's own bars directly over
     * the rows' bars, on the same ten cells and the same denominator, and a
     * folded group then says exactly what an open one would, one line shorter.
     *
     * The header spans every column up to the FIRST one named here; the rest
     * become real `<td>`s carrying that column's width. A groupBy that returns
     * nothing keeps the single full-width heading every other grouped table
     * has.
     *
     * Handed the group's rows, not a precomputed value, because only the
     * caller knows what summing its own rows means -- and it is given the
     * rows that are actually IN the group, which under a filter is the rows
     * that survived it.
     */
    cells?: (key: string, rows: readonly T[]) => Record<string, ReactNode>;
    /**
     * Whether a group arrives OPEN, decided per group from its own rows.
     *
     * Unset, every collapsible group arrives open, which is what the argument
     * under `collapsible` demands of a heading that says only what is in it.
     * A caller whose heading is a full row -- see `cells` -- can reverse that
     * for the groups with nothing worth opening, because such a heading no
     * longer hides anything: it carries the group's own readings and the worst
     * state in it.
     *
     * LATCHED ON FIRST SIGHT, never re-evaluated. The fleet re-polls every
     * sixty seconds and several container states are derived per render, so a
     * predicate consulted on every pass would fold and unfold groups under a
     * reader who touched nothing. A group that goes bad after being seeded
     * clean stays as it is until someone opens it -- and its heading is
     * already carrying the badge that says it went bad.
     */
    defaultOpen?: (key: string, rows: readonly T[]) => boolean;
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
     * fold a noisy host away. Only lists that carry `cells` should ask for
     * this -- see above.
     *
     * THAT ARGUMENT IS NOW NARROWER THAN IT READS, and `defaultOpen` is the
     * exception it did not anticipate. It was written against a heading that
     * said "immich · 4 containers" -- a name and a count, which is indeed
     * hiding rather than summarising. A heading built from `cells` is a row:
     * the group's CPU and memory bars sit in the CPU and Memory columns over
     * the same denominators as the rows beneath, with the worst state in the
     * group beside the name. Folding a group whose heading says all of that,
     * and which contains nothing wrong, hides nothing a reader was going to
     * act on. Folding one that IS wrong would, which is why the predicate
     * decides per group rather than a flag deciding for all of them.
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

/** What each rail hue means, in a word. Only the two severities a rail is
 * ever drawn for -- see rowSeverity. */
const SEVERITY_WORD: Record<"warning" | "critical", string> = {
  warning: "Warning",
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

  // Whether each group is open, seeded once per key and then owned by the
  // reader.
  //
  // A map rather than the set of closed keys this replaces, because there are
  // now two ways a group can start: open, which is every collapsible group's
  // default, or folded, which `defaultOpen` decides from the group's own rows.
  // A set of exceptions cannot express both without also encoding which
  // default it is an exception TO.
  //
  // SEEDED ON FIRST SIGHT AND NEVER RE-SEEDED. That is the whole reason the
  // seeding happens in a memo keyed on the groups rather than inline: the
  // fleet re-polls every sixty seconds, and several of the states a caller
  // decides on are derived per render, so consulting the predicate again would
  // move groups under a reader who touched nothing.
  //
  // A group that disappears (a project whose last container went away) leaves
  // a stale key behind, which costs a string and means the group comes back
  // the way it was left.
  const [choice, setChoice] = useState<ReadonlyMap<string, boolean>>(new Map());
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

  // Seed a group's open state the first time it is seen, and only then.
  //
  // In an effect rather than during render because it writes state, and keyed
  // on the group list so it runs when a group appears rather than on every
  // poll. Groups already in `choice` are left exactly as they are -- that is
  // what makes this a seed and not a reset, and it is why a reader's fold
  // survives the next sixty-second refetch.
  //
  // LAYOUT effect, not a passive one. `choice` starts empty and the row loop
  // below falls back to open, so a passive effect lets the browser paint every
  // group expanded and then fold them a frame later -- a full-height jump on
  // exactly the two lists whose point is that a quiet stack is already folded
  // when the reader arrives. useLayoutEffect runs after the DOM is written and
  // before the paint, so the expanded state never reaches the screen.
  const defaultOpen = groupBy?.defaultOpen;
  useLayoutEffect(() => {
    if (groups === null || defaultOpen === undefined) return;
    setChoice((prev) => {
      let next: Map<string, boolean> | null = null;
      for (const [key, rowsInGroup] of groups) {
        if (prev.has(key)) continue;
        next ??= new Map(prev);
        next.set(key, defaultOpen(key, rowsInGroup));
      }
      // The same map when nothing was new, so this never re-renders on a poll
      // that changed no group.
      return next ?? prev;
    });
  }, [groups, defaultOpen]);

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
            // Open unless something says otherwise: the reader's own choice
            // first, then the seed, then the default every collapsible group
            // has always had.
            const open = !collapsible || forced || (choice.get(key) ?? true);
            const bodyId = `${tableId}-${key}`;
            // The heading's own cells, and how far the name is allowed to
            // span: up to the first column the caller filled. A caller that
            // fills none keeps the full-width heading every other grouped
            // table has, which is what makes this backward compatible.
            const headCells = groupBy?.cells?.(key, rowsInGroup) ?? null;
            const headSpan =
              headCells === null
                ? columns.length
                : Math.max(
                    1,
                    columns.findIndex((col) => col.key in headCells) === -1
                      ? columns.length
                      : columns.findIndex((col) => col.key in headCells),
                  );
            return (
              <tbody key={key} id={bodyId}>
                <tr className="grouprow">
                  {/* A header for the rows below it, so scope is rowgroup
                      rather than col -- it names the group, not a column. */}
                  <th scope="rowgroup" colSpan={headSpan}>
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
                            setChoice((prev) => {
                              const next = new Map(prev);
                              next.set(key, !(prev.get(key) ?? true));
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
                    </div>
                  </th>
                  {/* The columns the heading did not span, each carrying its
                      own width so the group's bars land over the rows' bars.
                      cellStyle, not bare tds: the width lives on the column
                      and all three rows of it -- header, heading, body -- have
                      to agree or the alignment this exists for is lost. */}
                  {headCells !== null
                    ? columns.slice(headSpan).map((col) => (
                        <td key={col.key} style={cellStyle(col)}>
                          {headCells[col.key] ?? null}
                        </td>
                      ))
                    : null}
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
            {dimAbsent(col.cell(row))}
          </td>
        ))}
      </tr>
    );
  }
}

/**
 * Dims a cell that has nothing to report.
 *
 * A column where most rows have no value -- Description on a fleet where
 * nobody has run `ip link set ... alias` -- draws a stack of dashes at full
 * data contrast, and the eye reads the stack before the rows that DO say
 * something. The marker still has to be legible ("we have no value" is a
 * fact), so it drops to --muted rather than to the annotation grey.
 *
 * Done here, once, rather than at the sixty-odd `?? ABSENT` call sites: the
 * marker is a plain string by design -- format's helpers build it into longer
 * ones -- and a cell whose ENTIRE output is that string is exactly the case
 * worth dimming. A cell that merely contains it, "– · 8 GiB", is a real
 * reading with half of it missing and keeps its contrast.
 */
function dimAbsent(cell: ReactNode): ReactNode {
  return cell === ABSENT ? <span className="absent">{ABSENT}</span> : cell;
}
