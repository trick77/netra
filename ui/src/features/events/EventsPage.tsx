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
import { Table, type Column } from "../../ui/Table";
import { EventTime } from "../../ui/When";
import type { Event } from "../../lib/api";
import type { Range } from "../../lib/range";
import { ABSENT } from "../../lib/format";
import { KNOWN_EVENT_TYPES, messageOf } from "./message";
import { PackageRunFold } from "./PackageRunFold";
// Re-exported below, so a link written against EventsPage still resolves.
import {
  SEVERITY_RANK,
  SEVERITY_TINT,
  railSeverity,
  severityOf,
  type EventSeverity,
} from "./severity";

export { severityOf, type EventSeverity } from "./severity";

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

/** What the dropdown offers, which is NOT every severity.
 *
 * `info` is missing on purpose. The filter is a threshold, so "info and worse"
 * selects precisely what "All severities" already selects -- two options, one
 * behaviour, and a reader who picks the wrong one learns nothing from the
 * result. It stays a valid value in a URL, because a link written before this
 * existed should still open the page it named. */
const SEVERITY_CHOICES: EventSeverity[] = ["critical", "warning"];

export interface EventFilters {
  search: string;
  /** A host id as a string, matching the option values; "" means every host. */
  host: string;
  type: string;
  severity: EventSeverity | "";
  range: Range;
}

/** The page opens on what needs attention.
 *
 * severity defaults to `warning`, not to everything: the log is dominated by
 * info rows -- every package install, every link change -- and a reader who
 * has to filter before the page says anything stops opening it. Info is one
 * dropdown away, and the filter is a threshold, so nothing worse than warning
 * is ever hidden by it. */
export const DEFAULT_FILTERS: EventFilters = {
  search: "",
  host: "",
  type: "",
  severity: "warning",
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
    if (key === "range" || key === "severity") continue;
    if (filters[key] !== DEFAULT_FILTERS[key]) usp.set(key, filters[key]);
  }
  usp.set("range", filters.range);
  // SEVERITY is written for the same reason as range, and became so for a
  // different one: "" means every severity, and omitting a key resolves it to
  // the DEFAULT, which is now `warning`. Left out, a link sent by a reader
  // looking at all severities would open filtered for the recipient -- the
  // one state a shared link could no longer express.
  usp.set("severity", filters.severity);
  return usp.toString();
}

/** A severity out of a URL, reduced to something the dropdown can show.
 *
 * A missing key (null) is the default; "" and any offered severity are kept;
 * the lowest severity collapses to "" because as a threshold it means the same
 * thing; anything else is a hand-edited URL and falls back to the default. */
function normalizeSeverityFilter(raw: string | null): EventSeverity | "" {
  if (raw === null) return DEFAULT_FILTERS.severity;
  if (raw === "" || raw === "info") return "";
  return SEVERITY_CHOICES.includes(raw as EventSeverity)
    ? (raw as EventSeverity)
    : DEFAULT_FILTERS.severity;
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
    // "" is a VALID value here -- every severity -- and must not fall through
    // to the default the way an unknown word does, or the link that spells out
    // "all severities" would open filtered. Only a MISSING key means default.
    //
    // `info` normalises to "" rather than being kept. As a threshold the two
    // select identical rows, which is why the dropdown offers only one of
    // them -- and a value with no matching <option> leaves the select rendered
    // blank, so a link written before this change would open a control showing
    // nothing while the list behaved as "all severities".
    severity: normalizeSeverityFilter(severity),
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
    if (
      filters.severity !== "" &&
      SEVERITY_RANK[severityOf(event)] < SEVERITY_RANK[filters.severity]
    ) {
      return false;
    }
    if (needle === "") return true;
    // The message is searched as well as the subject, so a version string or
    // a systemd state word is findable -- those live only in the detail JSON,
    // and the row shows them.
    return [
      event.subject ?? "",
      event.type,
      event.hostname,
      messageOf(event),
    ].some((field) => field.toLowerCase().includes(needle));
  });
}

/** Every severity is drawn the same way: a dotted Badge, in a column of its
 * own.
 *
 * It used to be two shapes in one place -- critical and warning got a Badge,
 * info got the bare word "info" with no chip and no dot -- on the argument
 * that a log where every row is decorated has no emphasis left. The argument
 * was right about emphasis and wrong about where to spend it. Run down a
 * column, two shapes for one field read as two different KINDS of fact rather
 * than as three steps of one, and the eye has to re-parse each row to work out
 * which it is looking at. The emphasis the argument wanted is now the row rail
 * (Table's rowSeverity), which no info row draws, so the quiet rows are still
 * quiet without the severity cell changing shape underneath them.
 *
 * The tint comes from SEVERITY_TINT rather than a ternary here: keyed on
 * EventSeverity, a fourth severity is a compile error instead of an undefined
 * that Badge absorbs into a grey dot nobody asked for. */
function SeverityMark({ severity }: { severity: EventSeverity }) {
  return <Badge severity={SEVERITY_TINT[severity]}>{severity}</Badge>;
}

/** The type is a bare `.badge`: with no `st-*` class it takes the neutral
 * chip ground, which is what a category needs and costs no new class. */
function TypeChip({ type }: { type: string }) {
  return <span className="badge">{type}</span>;
}

/** The log's columns, in the order the row used to run them together.
 *
 * `now` is a parameter rather than a module constant so the hover age on the
 * When cell moves with the page's clock instead of freezing at import time. */
function columns(now: Date): Column<Event>[] {
  return [
    {
      key: "ts",
      header: "When",
      cell: (event) => <EventTime iso={event.ts} now={now} />,
      // The instant, not the rendered string: only one of the two still sorts
      // correctly once the wording or the locale changes.
      sortValue: (event) => Date.parse(event.ts),
    },
    {
      key: "severity",
      header: "Severity",
      cell: (event) => <SeverityMark severity={severityOf(event)} />,
      sortValue: (event) => SEVERITY_RANK[severityOf(event)],
    },
    {
      key: "type",
      header: "Type",
      cell: (event) => <TypeChip type={event.type} />,
      sortValue: (event) => event.type,
    },
    {
      key: "host",
      header: "Host",
      cell: (event) => (
        <a className="evhost" href={`/hosts/${event.host_id}`}>
          {event.hostname}
        </a>
      ),
      sortValue: (event) => event.hostname,
    },
    {
      // What happened, in words. A subjectless event with nothing in its
      // detail is one about the host as a whole (0001_init.sql), which is a
      // fact worth marking rather than an empty cell.
      key: "message",
      header: "Event",
      cell: (event) => (
        <>
          {messageOf(event) || ABSENT}
          <PackageRunFold event={event} />
        </>
      ),
      sortValue: (event) => messageOf(event) || null,
    },
  ];
}

export interface EventsPageProps {
  events: readonly Event[];
  hosts: readonly { id: number; hostname: string }[];
  filters: EventFilters;
  onFiltersChange: (filters: EventFilters) => void;
  /** Whether the server returned as many rows as were asked for, so older
   * events inside the window were cut off before this page ever saw them.
   *
   * It matters because every filter here except the range is applied to rows
   * ALREADY FETCHED. The server sends the newest N and knows nothing about the
   * severity floor, so on a noisy fleet a critical event from twenty hours ago
   * can be truncated away by a few hundred info rows in front of it -- and the
   * page would otherwise say "nothing matches these filters", which is not
   * what happened. */
  truncated?: boolean;
  /** Injectable so relative timestamps are deterministic in tests. */
  now?: Date;
}

export function EventsPage({
  events,
  hosts,
  filters,
  onFiltersChange,
  truncated = false,
  now = new Date(),
}: EventsPageProps) {
  // Every control hands back the WHOLE filter object, never a patch: the
  // URL carries all five, and a caller that had to merge partials would
  // eventually drop one.
  const set = <K extends keyof EventFilters>(key: K, value: EventFilters[K]) =>
    onFiltersChange({ ...filters, [key]: value });

  // The types on offer are the ones the hub is known to emit, plus any that
  // actually arrived.
  //
  // The known set has to be there. Filtering by type narrows the RESPONSE, so
  // a list built from the response alone empties itself: pick "package" and
  // the server returns package rows only, so "mdraid" and "unit" disappear
  // from the dropdown and there is no way back to them without clearing the
  // filter first. Deriving from the response is still worth keeping alongside
  // it, so an emitter added to the hub shows up here without a UI change.
  const types = [
    ...new Set(
      [...KNOWN_EVENT_TYPES, ...events.map((e) => e.type), filters.type].filter(
        (t) => t !== "",
      ),
    ),
  ].sort();

  const rows = applyFilters(events, filters);

  return (
    <>
      {/* The same head every page draws: the name of the page and nothing
          else. No glyph -- the bar carries the marks now, and a page that
          repeats its own is saying the same thing twice.

          The free-text box used to sit off the right end here. It is a FILTER,
          and every other control that narrows this page lives one line down; a
          box doing the same job as the four beside it, parked up in the title
          rail, reads as a search over something larger than the list it
          actually narrows. */}
      <div className="pagehead">
        <h1>Events</h1>
      </div>
      <p className="pagesub">
        What happened, when. An event is an instant, not a state.
      </p>

      {/* Everything that narrows the log, on one line: three selects by
          field, then the free text, then the window. The text box sits
          immediately left of the range because those two are the pair a
          reader reaches for together -- "this word, this week". */}
      <div className="toolbar">
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
          {SEVERITY_CHOICES.map((severity) => (
            <option key={severity} value={severity}>
              {/* "and worse" spelled out: the control filters by threshold,
                  and a bare "warning" reads as an equality. critical IS the
                  worst, so it needs no suffix. */}
              {severity === "critical" ? severity : `${severity} and worse`}
            </option>
          ))}
        </Select>

        <span className="spacer" />
        <div className="filterbox">
          <Input
            id="ev-search"
            type="search"
            placeholder="Filter events"
            aria-label="Filter events"
            value={filters.search}
            onChange={(e) => set("search", e.target.value)}
          />
        </div>
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
            body={
              truncated
                ? "This window held more events than one page can carry, so the oldest were cut off before any filter ran. Narrow the range, or filter by host or type."
                : "Nothing in this window matches these filters. Widen the range, or clear a filter."
            }
          />
        ) : (
          // The same Table every other list on the site is built from, rather
          // than the bespoke two-column grid this page used to draw.
          //
          // Five fields were being run together into one line of prose --
          // severity chip, type chip, hostname link, then the sentence -- so
          // nothing lined up down the page and none of it could be sorted.
          // Headed columns give the eye a fixed left edge per field, and they
          // come with click-to-sort for free.
          <Table
            columns={columns(now)}
            rows={rows}
            rowKey={(event) => event.id}
            // The rail, not the badge, is what a reader scanning for trouble
            // actually follows: it marks the row from the table's edge. See
            // Table's rowSeverity.
            rowSeverity={(event) => railSeverity(severityOf(event))}
            defaultSort={{ key: "ts", dir: "desc" }}
          />
        )}
      </Card>
    </>
  );
}
