// The events log (spec 6). An event is an instant; alerts -- intervals --
// arrive with the engine that defines them, so this page is the log and
// nothing else.
//
// Filter state and its setter are props, not internal state: every filter
// belongs in the URL so a filtered log is shareable, and Wave 5 owns the
// router that puts it there. filtersToQuery/filtersFromQuery below are this
// page's half of that contract -- the serialization lives with the type it
// serializes, and nothing here touches history or location.
import { Inbox } from "lucide-react";
import { Badge } from "../../ui/Badge";
import { Card } from "../../ui/Card";
import { EmptyState } from "../../ui/EmptyState";
import { Input, Select } from "../../ui/Control";
import { Segmented } from "../../ui/Segmented";
import type { Event } from "../../lib/api";
import type { Range } from "../../lib/range";
import { ABSENT, absolute, relative } from "../../lib/format";

// The windows this page OFFERS: the log reaches back further than a metrics
// chart does, because events are sparse and "what happened this week" is the
// question this page is asked. The type itself is lib/range's.

export const EVENT_RANGES: { value: Range; label: string }[] = [
  { value: "1h", label: "1h" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "30d", label: "30d" },
];

/** The same set as the bare values clampRange takes. */
export const EVENT_RANGE_VALUES: readonly Range[] = EVENT_RANGES.map(
  (o) => o.value,
);

/** Derived, never received: the events table carries type, subject and
 * detail, and no severity column at all. */
export type EventSeverity = "critical" | "warning" | "info";

const SEVERITIES: EventSeverity[] = ["critical", "warning", "info"];

// The states an emitter puts in its own detail JSON. mdraid is the only
// emitter today (agent/collector/mdraid.go marshals the array state), so
// this is deliberately small: a table of invented severities for types the
// hub never emits would be a taxonomy nobody wrote.
const CRITICAL_STATES = ["degraded", "failed", "faulty"];
const WARNING_STATES = ["recovering", "resync", "resyncing", "rebuilding"];

function detailOf(event: Event): Record<string, unknown> | null {
  // `detail` is `unknown` in lib/api.ts on purpose -- its shape is the
  // emitting collector's, not the API's -- so it is narrowed rather than
  // cast, and anything that is not a plain object simply says nothing.
  if (typeof event.detail !== "object" || event.detail === null) return null;
  if (Array.isArray(event.detail)) return null;
  return event.detail as Record<string, unknown>;
}

/**
 * Severity of one event, derived from what its emitter said about itself.
 * An emitter that states a severity outright is believed; otherwise the
 * state word it reported decides. Everything else is info -- a package
 * upgrade is a fact, not an emergency, and colouring it as one is how a log
 * stops being read.
 */
export function severityOf(event: Event): EventSeverity {
  const detail = detailOf(event);
  if (detail === null) return "info";

  const declared = detail["severity"];
  if (declared === "critical" || declared === "warning") return declared;

  const state = detail["state"];
  if (typeof state === "string") {
    if (CRITICAL_STATES.includes(state)) return "critical";
    if (WARNING_STATES.includes(state)) return "warning";
  }
  return "info";
}

export interface EventFilters {
  search: string;
  /** A host id as a string, matching the option values; "" means every host. */
  host: string;
  type: string;
  severity: EventSeverity | "";
  range: Range;
}

export const DEFAULT_FILTERS: EventFilters = {
  search: "",
  host: "",
  type: "",
  severity: "",
  range: "24h",
};

/** The filters as a query string, omitting anything at its default: a URL
 * that spells out every default is noise, and the absent key already means
 * the default.
 *
 * The RANGE is the exception and is always written. Since it became a
 * remembered preference, an absent `range` no longer means "24h" -- it means
 * "whatever the reader's browser remembers", which is precisely the thing a
 * sent link exists to override. Omitting it when it happened to equal the
 * sender's default would silently hand the recipient a different window. */
export function filtersToQuery(filters: EventFilters): string {
  const usp = new URLSearchParams();
  for (const key of Object.keys(DEFAULT_FILTERS) as (keyof EventFilters)[]) {
    if (key === "range") continue;
    if (filters[key] !== DEFAULT_FILTERS[key]) usp.set(key, filters[key]);
  }
  usp.set("range", filters.range);
  return usp.toString();
}

/** The inverse. An unknown range or severity falls back to its default
 * rather than being trusted: a hand-edited URL must not be able to put the
 * page in a state its controls cannot express.
 *
 * `fallbackRange` is what a URL carrying no range resolves to -- the
 * screen passes the remembered preference, already clamped to the set this
 * page offers. It defaults to the static one so the function stays usable
 * on its own. */
export function filtersFromQuery(
  query: string,
  fallbackRange: Range = DEFAULT_FILTERS.range,
): EventFilters {
  const usp = new URLSearchParams(query);
  const range = usp.get("range");
  const severity = usp.get("severity");
  return {
    search: usp.get("search") ?? DEFAULT_FILTERS.search,
    host: usp.get("host") ?? DEFAULT_FILTERS.host,
    type: usp.get("type") ?? DEFAULT_FILTERS.type,
    severity: SEVERITIES.includes(severity as EventSeverity)
      ? (severity as EventSeverity)
      : DEFAULT_FILTERS.severity,
    range: EVENT_RANGES.some((r) => r.value === range)
      ? (range as Range)
      : fallbackRange,
  };
}

/**
 * The client-side half of the filtering. The RANGE is deliberately not
 * applied here: it is the server's window (api.ts's since/until on
 * GET /api/v1/events), so rows outside it were never fetched, and
 * re-filtering by it would silently hide rows whenever the two notions of
 * "now" disagreed.
 */
export function applyFilters(
  events: readonly Event[],
  filters: EventFilters,
): Event[] {
  const needle = filters.search.trim().toLowerCase();
  return events.filter((event) => {
    if (filters.host !== "" && String(event.host_id) !== filters.host) {
      return false;
    }
    if (filters.type !== "" && event.type !== filters.type) return false;
    if (filters.severity !== "" && severityOf(event) !== filters.severity) {
      return false;
    }
    if (needle === "") return true;
    return [event.subject ?? "", event.type, event.hostname].some((field) =>
      field.toLowerCase().includes(needle),
    );
  });
}

/** Only critical and warning carry a status tint (spec 6). Info is the word
 * on its own -- a log where every row is decorated has no emphasis left for
 * the row that needs it. */
function SeverityMark({ severity }: { severity: EventSeverity }) {
  // No class: index.css has no general muted-inline class, and "info" needs
  // no decoration -- it is the absence of a status, stated in a word.
  if (severity === "info") return <span>info</span>;
  return (
    <Badge severity={severity === "critical" ? "critical" : "warning"}>
      {severity}
    </Badge>
  );
}

/** The type is a bare `.badge`: with no `st-*` class it takes the neutral
 * chip ground, which is what a category needs and costs no new class. */
function TypeChip({ type }: { type: string }) {
  return <span className="badge">{type}</span>;
}

export interface EventsPageProps {
  events: readonly Event[];
  hosts: readonly { id: number; hostname: string }[];
  filters: EventFilters;
  onFiltersChange: (filters: EventFilters) => void;
  /** Injectable so relative timestamps are deterministic in tests. */
  now?: Date;
}

export function EventsPage({
  events,
  hosts,
  filters,
  onFiltersChange,
  now = new Date(),
}: EventsPageProps) {
  // Every control hands back the WHOLE filter object, never a patch: the
  // URL carries all five, and a caller that had to merge partials would
  // eventually drop one.
  const set = <K extends keyof EventFilters>(key: K, value: EventFilters[K]) =>
    onFiltersChange({ ...filters, [key]: value });

  // The types on offer are the types that arrived, plus whichever one is
  // selected -- a hardcoded list would offer filters that match nothing.
  const types = [
    ...new Set(
      [...events.map((e) => e.type), filters.type].filter((t) => t !== ""),
    ),
  ].sort();

  const rows = applyFilters(events, filters);

  return (
    <>
      <div className="section">
        <h2>Events</h2>
        <span className="hint">
          What happened, when. An event is an instant, not a state.
        </span>
      </div>

      <div className="toolbar">
        <label htmlFor="ev-search">Search</label>
        <Input
          id="ev-search"
          type="search"
          placeholder="subject, type or host"
          value={filters.search}
          onChange={(e) => set("search", e.target.value)}
        />

        <label htmlFor="ev-host">Host</label>
        <Select
          id="ev-host"
          value={filters.host}
          onChange={(e) => set("host", e.target.value)}
        >
          <option value="">All hosts</option>
          {hosts.map((host) => (
            <option key={host.id} value={String(host.id)}>
              {host.hostname}
            </option>
          ))}
        </Select>

        <label htmlFor="ev-type">Type</label>
        <Select
          id="ev-type"
          value={filters.type}
          onChange={(e) => set("type", e.target.value)}
        >
          <option value="">All types</option>
          {types.map((type) => (
            <option key={type} value={type}>
              {type}
            </option>
          ))}
        </Select>

        <label htmlFor="ev-severity">Severity</label>
        <Select
          id="ev-severity"
          value={filters.severity}
          onChange={(e) =>
            set("severity", e.target.value as EventSeverity | "")
          }
        >
          <option value="">All severities</option>
          {SEVERITIES.map((severity) => (
            <option key={severity} value={severity}>
              {severity}
            </option>
          ))}
        </Select>

        <span className="spacer" />
        <Segmented
          options={EVENT_RANGES}
          value={filters.range}
          onChange={(range) => set("range", range)}
        />
      </div>

      <Card>
        {rows.length === 0 ? (
          <EmptyState
            icon={Inbox}
            title="No events"
            body="Nothing in this window matches these filters. Widen the range, or clear a filter."
          />
        ) : (
          // role="list" on a div rather than a <ul>: index.css styles
          // `.evrow` as a grid and carries no list reset, so a real <ul>
          // would arrive with markers and an indent. The row keeps its
          // semantics without the page inventing a class.
          <div role="list">
            {rows.map((event) => (
              <div className="evrow" role="listitem" key={event.id}>
                <time dateTime={event.ts} title={absolute(event.ts)}>
                  {relative(event.ts, now)}
                </time>
                <div>
                  <SeverityMark severity={severityOf(event)} />{" "}
                  <TypeChip type={event.type} />{" "}
                  <a href={`/hosts/${event.host_id}`}>{event.hostname}</a>{" "}
                  {/* A subjectless event is one about the host as a whole
                      (0001_init.sql), which is a fact worth marking rather
                      than an empty cell. */}
                  <span>{event.subject ?? ABSENT}</span>
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>
    </>
  );
}
