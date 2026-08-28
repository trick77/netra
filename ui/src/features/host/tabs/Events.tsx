// This host's slice of the events log. An event is an instant (spec §6);
// the "what is firing right now" view is the Alerts tab that lands with
// the Stage 2 engine, so nothing here pretends to hold current state.
import type { Event } from "../../../lib/api";
import { ABSENT, absolute, relative } from "../../../lib/format";
import { Badge, type Severity } from "../../../ui/Badge";
import type { Column } from "../../../ui/Table";
import { Inventory } from "./Inventory";
import { messageOf } from "../../events/message";
import { PackageRunFold } from "../../events/PackageRunFold";

// Only these two carry a status tint. A package upgrade is not a
// colour-coded emergency, so every other event is a neutral chip.
const STATED_SEVERITIES: Severity[] = ["warning", "critical"];

/**
 * The severity the emitting collector stated, or null.
 *
 * The events table has no severity column at all -- it stores type,
 * subject and the collector's own detail JSON. Deriving a severity from
 * the type would mean this UI inventing a judgement no collector made, so
 * the only accepted source is an explicit `severity` in detail, and only
 * when it is one of the two the design admits.
 */
export function eventSeverity(event: Event): Severity | null {
  const detail = event.detail;
  if (detail === null || typeof detail !== "object") return null;
  const stated = (detail as Record<string, unknown>).severity;
  if (typeof stated !== "string") return null;
  const match = STATED_SEVERITIES.find((s) => s === stated);
  return match ?? null;
}

/**
 * How the two admitted severities ORDER, which is not how they alphabetise:
 * "critical" sorts before "warning" as a string, which is right by accident
 * in one direction and wrong in the other. An event the collector stated
 * nothing about returns null and sorts last either way -- it is not the
 * calmest event on the page, it is an event with no judgement on it.
 */
const SEVERITY_RANK: Record<Severity, number> = {
  neutral: 0,
  ok: 1,
  warning: 2,
  serious: 3,
  critical: 4,
};

const COLUMNS: Column<Event>[] = [
  {
    key: "ts",
    header: "When",
    cell: (row) => <span title={absolute(row.ts)}>{relative(row.ts)}</span>,
    // The instant, not the "3 minutes ago" the cell prints: the two order
    // identically and only one of them still does after the string is
    // translated or the wording changes.
    sortValue: (row) => Date.parse(row.ts),
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
    key: "severity",
    header: "Severity",
    cell: (row) => {
      const severity = eventSeverity(row);
      return severity === null ? (
        ""
      ) : (
        <Badge severity={severity}>{severity}</Badge>
      );
    },
    sortValue: (row) => {
      const severity = eventSeverity(row);
      return severity === null ? null : SEVERITY_RANK[severity];
    },
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
      searchText={(row) =>
        [row.type, row.subject, messageOf(row)].filter(Boolean).join(" ")
      }
    />
  );
}
