// This host's slice of the events log. An event is an instant (spec §6);
// the "what is firing right now" view is the Alerts tab that lands with
// the Stage 2 engine, so nothing here pretends to hold current state.
import type { Event } from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import { Badge, type Severity } from "../../../ui/Badge";
import type { Column } from "../../../ui/Table";
import { EventTime } from "../../../ui/When";
import { Inventory } from "./Inventory";
import { messageOf } from "../../events/message";
import { PackageRunFold } from "../../events/PackageRunFold";
import { SEVERITY_RANK, severityOf } from "../../events/severity";

// This tab used to carry its own `eventSeverity`, which accepted only a
// severity the collector had STATED and gave everything else no mark at all.
// It is gone, and the fleet log's severityOf is the one rule: the same row
// rated `info` on /events and blank here is a difference a reader finds by
// clicking between the two, and neither answer explains the other.

/** The tint each severity word takes. `info` is neutral -- a real state that
 * is simply not severe, which is Badge's documented use for it -- so the cell
 * is the same shape in every row. See EventsPage's SeverityMark. */
const SEVERITY_TINT: Record<string, Severity> = {
  critical: "critical",
  warning: "warning",
  info: "neutral",
};

const COLUMNS: Column<Event>[] = [
  {
    key: "ts",
    header: "When",
    // The exact local instant, with the age on hover -- see EventTime. The
    // fleet log reads the same way; a timestamp that means one thing on
    // /events and another on this tab is two clocks.
    cell: (row) => <EventTime iso={row.ts} />,
    // The instant, not the string the cell prints: the two order identically
    // and only one of them still does after the locale changes.
    sortValue: (row) => Date.parse(row.ts),
  },
  {
    key: "severity",
    header: "Severity",
    cell: (row) => {
      const severity = severityOf(row);
      return <Badge severity={SEVERITY_TINT[severity]}>{severity}</Badge>;
    },
    sortValue: (row) => SEVERITY_RANK[severityOf(row)],
  },
  {
    key: "type",
    header: "Type",
    // A bare .badge, not a Badge: a neutral chip takes no status tint, and
    // a type is a category rather than a judgement.
    cell: (row) => <span className="badge">{row.type}</span>,
    sortValue: (row) => row.type,
  },
  {
    key: "subject",
    header: "Subject",
    cell: (row) => row.subject ?? ABSENT,
    // The field, not the ABSENT dash the cell falls back to: a subjectless
    // event has no subject to order among the real ones.
    sortValue: (row) => row.subject ?? null,
  },
  // What happened, rather than the detail JSON's keys and values spelled out.
  // Subject stays its own column: it is what the table is scanned and sorted
  // by, and the message repeats it inside a sentence rather than replacing it.
  {
    key: "message",
    header: "Event",
    cell: (row) => (
      <>
        {messageOf(row) || ABSENT}
        <PackageRunFold event={row} />
      </>
    ),
    // The sentence the cell renders, so the order matches what is on screen.
    // The fold beneath it is a disclosure, not part of the reading.
    sortValue: (row) => messageOf(row) || null,
  },
];

export interface EventsProps {
  events: readonly Event[] | null;
}

export function Events({ events }: EventsProps) {
  // Newest first: the log is read to answer "what just happened", and the
  // hub's ordering is not part of its contract.
  const rows = [...(events ?? [])].sort(
    (a, b) => new Date(b.ts).getTime() - new Date(a.ts).getTime(),
  );

  return (
    <Inventory
      label="Events"
      columns={COLUMNS}
      rows={rows}
      rowKey={(row) => row.id}
      // The same rail the fleet log draws, so the two lists mark trouble the
      // same way. info draws none: a list where every row is marked has
      // marked nothing.
      rowSeverity={(row) => {
        const severity = severityOf(row);
        return severity === "info" ? null : severity;
      }}
      defaultSort={{ key: "ts", dir: "desc" }}
      searchText={(row) =>
        [row.type, row.subject, messageOf(row)].filter(Boolean).join(" ")
      }
    />
  );
}
