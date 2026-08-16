// What an event SAYS, in a sentence.
//
// Both event views used to render the bare `subject` column and drop `detail`
// on the floor, which left a row reading "package · web01 · curl" -- the fact
// that curl went from 8.5.0 to 8.5.0-2 was fetched, and then not shown. A log
// whose rows do not say what happened is a list of nouns.
//
// This lives in its own module rather than beside either page because both
// need it and neither owns it. severityOf stays in EventsPage: it is that
// page's filter vocabulary, and the host tab deliberately judges severity more
// narrowly.
import type { Event } from "../../lib/api";

/** The known event types, which is also the order a type filter offers them.
 *
 * Hardcoded, unlike everything else here, because the type dropdown is built
 * from the types present in the CURRENT response. That was invisible while
 * mdraid was the only emitter; with three it collapses -- selecting "package"
 * makes the server return package rows only, so the other two vanish from the
 * dropdown and the reader cannot switch without first clearing the filter.
 * The list is unioned with whatever actually arrived, so an emitter added
 * later still appears. */
export const KNOWN_EVENT_TYPES = ["mdraid", "package", "unit"] as const;

function fields(event: Event): Record<string, unknown> {
  // `detail` is `unknown` in lib/api.ts on purpose -- its shape is the
  // emitting collector's, not the API's -- so it is narrowed rather than
  // cast, and anything that is not a plain object simply says nothing.
  if (typeof event.detail !== "object" || event.detail === null) return {};
  if (Array.isArray(event.detail)) return {};
  return event.detail as Record<string, unknown>;
}

/** A detail value as a string, or "" when it is absent or not a scalar. The
 * hub drops null keys rather than sending them (jsonb_strip_nulls), so an
 * absent key and an empty string mean the same thing here: nothing to say. */
function text(fields: Record<string, unknown>, key: string): string {
  const value = fields[key];
  if (typeof value === "string") return value;
  if (typeof value === "number") return String(value);
  return "";
}

/** The generic rendering: every detail key, in the order the emitter wrote
 * them. This is what the host tab showed for every event before the specific
 * cases below existed, and it stays as the fallback so a type added later is
 * never a blank cell -- worse than terse is empty. */
function everyField(fields: Record<string, unknown>): string {
  return Object.entries(fields)
    .filter(([key]) => key !== "severity")
    .map(([key, value]) => `${key} ${String(value)}`)
    .join(" · ");
}

function packageMessage(name: string, f: Record<string, unknown>): string {
  const from = text(f, "from_version");
  const to = text(f, "to_version");
  switch (text(f, "action")) {
    case "install":
      return to ? `${name} installed ${to}` : `${name} installed`;
    case "remove":
      return from ? `${name} removed (${from})` : `${name} removed`;
    case "upgrade":
      // An upgrade with only one side is still an upgrade; saying so beats
      // rendering "undefined → 8.5.0-2".
      if (from && to) return `${name} upgraded ${from} → ${to}`;
      return to ? `${name} upgraded to ${to}` : `${name} upgraded`;
    default:
      return everyField(f) || name;
  }
}

function unitMessage(name: string, f: Record<string, unknown>): string {
  const state = text(f, "state");
  const previous = text(f, "previous_state");
  const substate = text(f, "substate");

  // Recovery reads better as what it is than as a state transition. The hub
  // only emits a unit event when one side of it is `failed`, so "not failed
  // now" and "failed before" is exactly a recovery.
  if (state !== "failed" && previous === "failed") {
    return `${name} recovered to ${state || "an ordinary state"}`;
  }
  if (state === "failed") {
    // The substate is systemd's reason word -- exit-code, timeout, signal --
    // and is the first thing anyone wants after "it failed".
    const why = substate && substate !== "failed" ? ` (${substate})` : "";
    return `${name} entered failed${why}`;
  }
  return previous ? `${name} ${previous} → ${state}` : `${name} ${state}`;
}

function mdraidMessage(name: string, f: Record<string, unknown>): string {
  const state = text(f, "state");
  if (!state) return everyField(f) || name;

  // level and raid_disks describe the array; degraded counts the missing
  // members. "1 of 2 devices" is the number an operator acts on.
  const level = text(f, "level");
  const disks = Number(f["raid_disks"]);
  const degraded = Number(f["degraded"]);

  const parts: string[] = [];
  if (level) parts.push(level);
  if (Number.isFinite(disks) && disks > 0) {
    parts.push(
      Number.isFinite(degraded) && degraded > 0
        ? `${disks - degraded} of ${disks} devices`
        : `${disks} devices`,
    );
  }
  const sync = text(f, "sync_action");
  if (sync && sync !== "idle") parts.push(sync);

  return parts.length === 0
    ? `${name} ${state}`
    : `${name} ${state} — ${parts.join(", ")}`;
}

/**
 * One line saying what this event was.
 *
 * Returns "" only when the emitter sent nothing to say, which the callers
 * render as the absent marker rather than as a gap.
 */
export function messageOf(event: Event): string {
  const f = fields(event);
  const subject = event.subject ?? "";

  switch (event.type) {
    case "package":
      return packageMessage(subject, f);
    case "unit":
      return unitMessage(subject, f);
    case "mdraid":
      return mdraidMessage(subject, f);
    default: {
      // An unrecognised type still has a subject and a detail blob, and both
      // belong on the row. This is the pre-existing rendering, kept.
      const rest = everyField(f);
      if (subject && rest) return `${subject} — ${rest}`;
      return subject || rest;
    }
  }
}
