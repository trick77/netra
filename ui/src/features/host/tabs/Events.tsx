// This host's slice of the events log. An event is an instant (spec §6);
// the "what is firing right now" view is the Alerts tab that lands with
// the Stage 2 engine, so nothing here pretends to hold current state.
import type { Event } from "../../../lib/api";
import { ABSENT, absolute, relative } from "../../../lib/format";
import { Badge, type Severity } from "../../../ui/Badge";
import type { Column } from "../../../ui/Table";
import { Inventory } from "./Inventory";

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

/** A one-line rendering of the collector's detail JSON. Passed through as
 * stored -- its shape is the emitting collector's, not this UI's -- so
 * anything but a flat object is simply not summarised. */
function detailSummary(detail: unknown): string {
  if (detail === null || typeof detail !== "object") return "";
  return Object.entries(detail as Record<string, unknown>)
    .filter(([key]) => key !== "severity")
    .map(([key, value]) => `${key} ${String(value)}`)
    .join(" · ");
}

const COLUMNS: Column<Event>[] = [
  {
    key: "ts",
    header: "When",
    cell: (row) => <span title={absolute(row.ts)}>{relative(row.ts)}</span>,
  },
  {
    key: "type",
    header: "Type",
    // A bare .badge, not a Badge: a neutral chip takes no status tint, and
    // a type is a category rather than a judgement.
    cell: (row) => <span className="badge">{row.type}</span>,
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
  },
  { key: "subject", header: "Subject", cell: (row) => row.subject ?? ABSENT },
  { key: "detail", header: "Detail", cell: (row) => detailSummary(row.detail) },
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
      rowKey={(row) => String(row.id)}
      searchText={(row) =>
        [row.type, row.subject, detailSummary(row.detail)]
          .filter(Boolean)
          .join(" ")
      }
    />
  );
}
