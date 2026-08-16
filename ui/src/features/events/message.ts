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
    .filter(([key]) => !NOT_FACTS.has(key))
    .map(([key, value]) => `${key} ${String(value)}`)
    .join(" · ");
}

/** Detail keys that are instructions to this UI rather than facts about the
 * event, and so have no place in a sentence describing it: the severity an
 * emitter stated, and the two counts describing an apt run's truncation. */
const NOT_FACTS = new Set(["severity", "run_size", "more"]);

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

/** A positive count, or 0. Used for the two run keys, which the hub omits
 * entirely on an ordinary run rather than sending a zero. */
function count(fields: Record<string, unknown>, key: string): number {
  const value = fields[key];
  return typeof value === "number" && value > 0 ? value : 0;
}

/**
 * How many packages of this event's apt run the hub did NOT send, or 0.
 *
 * One `apt upgrade` writes one event per package, all sharing a timestamp, so
 * a dist-upgrade would otherwise fill the whole page and push out the array
 * that went degraded underneath it. The hub keeps the first few of each run
 * and puts the remainder here (packageRunRows in hub/read/events.go).
 *
 * Deliberately not folded into messageOf: the row still says what ITS package
 * did, and the count is a separate affordance beside that sentence -- a link
 * to where the rest actually live, not a clause in the middle of a sentence
 * about curl.
 */
export function packagesOmitted(event: Event): number {
  if (event.type !== "package") return 0;
  return count(fields(event), "more");
}

/** How many packages the whole run touched, or 0 when the run was small
 * enough to be shown in full. The fold's tooltip: the row says how many are
 * hidden, this says how many there were. */
export function packageRunSize(event: Event): number {
  if (event.type !== "package") return 0;
  return count(fields(event), "run_size");
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
    // The substate is appended when it says something the state does not.
    // It usually does not: a failed unit's SubState is itself `failed`
    // (internal/agent/collector/systemd_test.go), and the reason word --
    // exit-code, timeout, signal -- is systemd's Result property, which the
    // agent does not collect. So this reads as a bare "entered failed" today
    // and only gains a parenthesis if a unit type ever reports otherwise.
    const why = substate && substate !== "failed" ? ` (${substate})` : "";
    return `${name} entered failed${why}`;
  }
  return previous ? `${name} ${previous} → ${state}` : `${name} ${state}`;
}

/** Whether an md array is missing members, and whether it is rebuilding them.
 *
 * `state` is deliberately NOT consulted. It is sysfs `array_state`
 * (agent/collector/mdraid.go), whose vocabulary is clear / inactive /
 * suspended / readonly / read-auto / clean / active / write-pending /
 * active-idle -- and "degraded" is not among them. The kernel reports a
 * half-dead raid1 as `clean`, because clean is about consistency, not about
 * how many disks are left. The repo's own fixture says so:
 * collector/testdata/mdraid/degraded reads array_state=clean, degraded=1,
 * sync_action=recover.
 *
 * So the only honest source for "is this array in trouble" is the device
 * count, and the only source for "is it fixing itself" is sync_action. Both
 * the sentence and the severity are derived here, once, so they cannot
 * disagree about the same array. */
function mdraidCondition(f: Record<string, unknown>): {
  word: string;
  severity: "critical" | "warning" | null;
} {
  const degraded = Number(f["degraded"]);
  const missing = Number.isFinite(degraded) && degraded > 0;
  // A whole array is described by whatever array_state said, with no
  // substitute invented for it: an event carrying no state at all has nothing
  // to report, and the caller falls back to spelling the detail out.
  if (!missing) return { word: text(f, "state"), severity: null };

  // sync_action is idle / resync / recover / check / repair. The first two of
  // the repair verbs mean the array is actively rebuilding onto a spare, which
  // is bad but self-healing; idle with members missing means nothing is being
  // done about it, and that is the one to wake someone for.
  const sync = text(f, "sync_action");
  const rebuilding =
    sync === "recover" || sync === "resync" || sync === "repair";
  return {
    word: rebuilding ? "rebuilding" : "degraded",
    severity: rebuilding ? "warning" : "critical",
  };
}

/** The severity of an mdraid event, or null when the array is whole.
 *
 * Exported for EventsPage's severityOf, which otherwise judges an event by
 * matching words in its detail against a table -- a table that, for mdraid,
 * lists states the kernel has never emitted and so never fired. A degraded
 * array rendered as "info". */
export function mdraidSeverity(event: Event): "critical" | "warning" | null {
  if (event.type !== "mdraid") return null;
  return mdraidCondition(fields(event)).severity;
}

function mdraidMessage(name: string, f: Record<string, unknown>): string {
  const { word } = mdraidCondition(f);
  if (!word) return everyField(f) || name;

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

  // A repair verb still earns a word when the array is WHOLE -- a scheduled
  // check or scrub on a healthy array is worth seeing. When members are
  // missing the word is already "rebuilding", and repeating sync_action after
  // it says the same thing twice.
  const sync = text(f, "sync_action");
  if (sync && sync !== "idle" && word !== "rebuilding") parts.push(sync);

  return parts.length === 0
    ? `${name} ${word}`
    : `${name} ${word} — ${parts.join(", ")}`;
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
