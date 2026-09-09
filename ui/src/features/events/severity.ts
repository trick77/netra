// How severe one event is, and how severities order.
//
// Its own file rather than an export from EventsPage, for the reason
// message.ts already gives about messageOf: both the fleet log and a host's
// Events tab need it and neither owns it. It used to live in EventsPage on the
// argument that it was "that page's judgement" -- which stopped being true the
// moment the host tab had to render the same three words the same way.
import type { Event, EventSeverity } from "../../lib/api";
import type { Severity } from "../../ui/Badge";

export type { EventSeverity };

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

/** The Badge tint each severity word takes, for the two lists that draw it.
 *
 * Keyed on EventSeverity rather than on `string`, and that is the point: a
 * fourth severity is then a compile error here instead of an `undefined` that
 * Badge quietly absorbs into `neutral` and ships as an untinted grey dot.
 *
 * `info` IS neutral -- a real state that is simply not severe, which is the
 * second use Badge documents for it -- so the cell is the same shape in every
 * row. */
export const SEVERITY_TINT: Record<EventSeverity, Severity> = {
  info: "neutral",
  warning: "warning",
  critical: "critical",
};

/** The rail a row of this severity draws, and null for the ones that draw
 * none. A table where every row is marked has marked nothing, so `info` is
 * deliberately unmarked -- the Severity cell already names it. */
export function railSeverity(
  severity: EventSeverity,
): "warning" | "critical" | null {
  return severity === "info" ? null : severity;
}

/**
 * Severity of one event: read off the row, not worked out here.
 *
 * This used to derive it -- the emitter's `severity` detail key first, then
 * mdraid's device count, then a table of state words -- on the argument that
 * the column defaults to `info`, so a row that stated nothing was
 * indistinguishable from one that said "this is routine".
 *
 * That argument has expired. Producers no longer write the detail key at all,
 * the wire field is the only channel, and the ingest gate refuses agents too
 * old to set it -- so every row that reaches here HAS stated a severity, and
 * `info` means the emitter called it routine. The other two branches of the
 * read layer's union are answered too: the hub derives a unit's severity from
 * its state (systemdstate.NotableSQL) and a package upgrade is always info, so
 * `severity` is populated for all three (internal/hub/read/events.go).
 *
 * Reading it is now the stronger choice rather than the lazy one: it is the
 * same value alerting acts on, and alerting cannot call into this file. A rule
 * that lives only in the browser is a rule that disagrees with the one that
 * pages someone.
 */
export function severityOf(event: Event): EventSeverity {
  return event.severity;
}
