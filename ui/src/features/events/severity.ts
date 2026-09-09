// How severe one event is, and how severities order.
//
// Its own file rather than an export from EventsPage, for the reason
// message.ts already gives about messageOf: both the fleet log and a host's
// Events tab need it and neither owns it. It used to live in EventsPage on the
// argument that it was "that page's judgement" -- which stopped being true the
// moment the host tab had to render the same three words the same way.
import type { Event } from "../../lib/api";
import { mdraidSeverity } from "./message";

/** Derived here, not read off the row.
 *
 * `events` HAS carried a severity column since 0015_event_severity.sql, and
 * the API returns it -- so this is a choice, not a gap. The column defaults to
 * `info`, which makes a row that stated nothing indistinguishable from one
 * that said "this is routine", and the two other branches of the read layer's
 * union (package_events, systemd_unit_events) have no column at all and get a
 * derivation anyway. One rule applied here beats three sources agreeing by
 * coincidence. */
export type EventSeverity = "critical" | "warning" | "info";

/** Severity as a number, so a filter can be a THRESHOLD rather than an
 * equality. Selecting "warning" has to mean "warning and worse": an operator
 * narrowing to warning and thereby hiding every critical event is the opposite
 * of what the control looks like it does, and it only became load-bearing when
 * warning became the default. Higher is worse. */
export const SEVERITY_RANK: Record<EventSeverity, number> = {
  info: 0,
  warning: 1,
  critical: 2,
};

// The states an emitter puts in its own detail JSON.
//
// Deliberately small. It was small originally because mdraid was the only
// emitter (agent/collector/mdraid.go marshals the array state) and a table of
// invented severities for types the hub never emits would be a taxonomy nobody
// wrote. It stays small for the same reason under the wider log: the hub now
// also sends package and unit events, and the ones that are serious say so
// outright in a `severity` key rather than relying on a word matched here.
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
 *
 * mdraid is asked separately, and BEFORE the table above, because for mdraid
 * the table has never once fired. The words in it -- degraded, faulty,
 * recovering, rebuilding -- are not values sysfs `array_state` can take, and
 * that is the field the collector puts in `state`. The kernel calls a raid1
 * with one disk left `clean`, so every real degraded array this log has ever
 * shown was rendered as "info". See mdraidSeverity.
 */
export function severityOf(event: Event): EventSeverity {
  const detail = detailOf(event);
  if (detail === null) return "info";

  const declared = detail["severity"];
  if (declared === "critical" || declared === "warning") return declared;

  const array = mdraidSeverity(event);
  if (array !== null) return array;

  const state = detail["state"];
  if (typeof state === "string") {
    if (CRITICAL_STATES.includes(state)) return "critical";
    if (WARNING_STATES.includes(state)) return "warning";
  }
  return "info";
}
