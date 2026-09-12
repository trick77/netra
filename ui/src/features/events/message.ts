// What an event SAYS, in a sentence.
//
// Both event views used to render the bare `subject` column and drop `detail`
// on the floor, which left a row reading "package · web01 · curl" -- the fact
// that curl went from 8.5.0 to 8.5.0-2 was fetched, and then not shown. A log
// whose rows do not say what happened is a list of nouns.
//
// This lives in its own module rather than beside either page because both
// need it and neither owns it. severityOf is beside it in ./severity for the
// same reason: it used to sit in EventsPage as "that page's judgement", which
// stopped being true the moment the host tab had to rate a row the same way.
import type { Event } from "../../lib/api";
import { duration } from "../../lib/format";

/** The types the kmsg collector emits, grouped by what they are about:
 * storage first, because that is what a fleet's kernel log is mostly made of,
 * then memory, hardware, the kernel itself, and link state last. The type
 * dropdown sorts, so this order is documentation rather than presentation.
 *
 * Mirrors kmsgClasses in agent/collector/kmsg.go. A type missing here still
 * renders -- the dropdown unions this list with whatever arrived -- but it
 * falls back to the generic detail dump instead of the kernel's own sentence,
 * so the two lists are kept in step deliberately. */
export const KERNEL_EVENT_TYPES = [
  "disk_error",
  "ata_error",
  "scsi_error",
  "nvme_error",
  "md_fail",
  "fs_error",
  "oom_kill",
  "hw_error",
  "thermal",
  "kernel_fault",
  "link_change",
] as const;

/** The condition kinds the hub opens and clears, which reach this log as
 * transitions.
 *
 * The kinds ScanConditions actually PRODUCES. This was narrower than the Kind
 * constants in internal/hub/conditions while `sporadic` and `drive` were
 * declared there with no observer behind them -- listing them then would have
 * put two options in the type dropdown that return an empty log with no
 * explanation. Both have observers now (scanReporting and scanDrives), so both
 * are here.
 *
 * A kind missing from this list still renders -- the dropdown unions it with
 * whatever arrived -- but falls through to the generic detail dump, which for
 * a transition reads "root — transition opened · severity warning" instead of
 * a sentence. So this grows when an observer does, not when a constant does. */
export const CONDITION_EVENT_TYPES = [
  "silent",
  "sporadic",
  "disk",
  "failed-units",
  "drive",
  // The deviation kinds. `temperature` is a condition and is not the kernel's
  // `thermal` a few lines up, which is an occurrence: the kernel says it
  // throttled at a moment, this says a sensor has been outside its own normal
  // range since one. Both belong in the dropdown, under the names their
  // producers use.
  "temperature",
  "processes",
  "load",
] as const;

/** The known event types, which is also the order a type filter offers them.
 *
 * Hardcoded, unlike everything else here, because the type dropdown is built
 * from the types present in the CURRENT response. That was invisible while
 * mdraid was the only emitter; with three it collapses -- selecting "package"
 * makes the server return package rows only, so the other two vanish from the
 * dropdown and the reader cannot switch without first clearing the filter.
 * The list is unioned with whatever actually arrived, so an emitter added
 * later still appears. */
export const KNOWN_EVENT_TYPES = [
  "mdraid",
  "package",
  "unit",
  "hub",
  // Derived by the hub from a container's RestartCount and StartedAt
  // (internal/hub/store/containerrestarts.go).
  "container_restart",
  "container_recreate",
  ...CONDITION_EVENT_TYPES,
  ...KERNEL_EVENT_TYPES,
] as const;

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
 * emitter stated, the two counts describing an apt run's truncation, and the
 * two keys saying which clock dated a container event. */
const NOT_FACTS = new Set([
  "severity",
  "run_size",
  "more",
  "ts_source",
  "observed_ts",
]);

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
 * How many packages of this event's apt run were left out of the run's own
 * rows, or 0.
 *
 * One `apt upgrade` writes one event per package, all sharing a timestamp, so
 * a dist-upgrade would otherwise fill the whole page and push out the array
 * that went degraded underneath it. The hub keeps the first few of each run
 * and puts the remainder here (packageRunRows in hub/read/events.go).
 *
 * Deliberately NOT "how many the hub did not send", which is the same number
 * only most of the time. Every row of a run shares one timestamp, so the outer
 * LIMIT can cut through a run: when it does, fewer rows arrive than the run
 * kept and the true count of absent packages is higher than this. The marker
 * rides the run's first row precisely so it survives that cut -- an
 * understated count on the oldest row of a page beats no count at all.
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

/** A container that came back up, in words.
 *
 * The hub writes `from`/`to` (Docker's RestartCount before and after) and
 * `delta` for both types, plus `image_from`/`image_to` when a redeploy
 * changed the image. It also writes `ts_source` and `observed_ts`, which say
 * WHICH CLOCK dated the row and are left out here on purpose: the When cell
 * already carries the instant, and "ts_source docker_started_at" in the
 * middle of a sentence is a debugging note, not a fact about the container.
 *
 * `container_recreate` covers shapes the hub cannot tell apart from the wire
 * (the Docker id never reaches it): a redeploy, and a `docker restart` or
 * `docker start` of the same container -- which Docker does not count but
 * does reset the counter for (moby daemon/start.go, ResetRestartManager).
 * Only a changed image proves a redeploy; everything else is called a
 * restart, with the counter's move shown when it moved. */
function containerMessage(
  type: string,
  name: string,
  f: Record<string, unknown>,
): string {
  // Not text(): a RestartCount of 0 is the common case and "" would send
  // every fresh container to the fallback dump.
  const from = f["from"];
  const to = f["to"];
  if (typeof from !== "number" || typeof to !== "number") {
    return everyField(f) || name;
  }

  if (type === "container_restart") {
    const delta = count(f, "delta");
    const times = delta > 1 ? ` ${delta}×` : "";
    return `${name} restarted${times} (restart count ${from} → ${to})`;
  }
  const imageFrom = text(f, "image_from");
  const imageTo = text(f, "image_to");
  if (imageFrom && imageTo) {
    return `${name} redeployed, image ${imageFrom} → ${imageTo}`;
  }
  if (from !== to) return `${name} restarted (restart count ${from} → ${to})`;
  return `${name} restarted`;
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
/** The array_state words that all mean "nothing is wrong with this array".
 *
 * Mirrors healthyStates in agent/collector/mdraid.go, which is what decides
 * whether an event is emitted at all. Kept in step deliberately: a word the
 * collector treats as healthy and this renders verbatim would show a row
 * reading "md3 write-pending" that no operator can act on. */
const HEALTHY_ARRAY_STATES = [
  "clean",
  "active",
  "active-idle",
  "write-pending",
  "read-auto",
];

function normalizeArrayState(state: string): string {
  return HEALTHY_ARRAY_STATES.includes(state) ? "clean" : state;
}

/** The word describing an array's condition, for the sentence this file
 * builds. It does NOT decide severity: that is the collector's judgement now
 * (severityOf in agent/collector/mdraid.go), travels in the event's own
 * field, and is read straight off it. */
function mdraidCondition(f: Record<string, unknown>): { word: string } {
  const degraded = Number(f["degraded"]);
  const missing = Number.isFinite(degraded) && degraded > 0;
  // A whole array is described by array_state, normalized: the kernel toggles
  // it between `active` and `clean` as the superblock dirty bit moves, so the
  // raw word says only whether a write happened to be in flight when the
  // sample was taken -- which is not a fact about the array. Every healthy
  // spelling therefore renders as "clean", matching what the collector
  // compares on (agent/collector/mdraid.go compareKey).
  //
  // An event carrying no state at all still has nothing to report, and the
  // caller falls back to spelling the detail out.
  if (!missing) {
    return { word: normalizeArrayState(text(f, "state")) };
  }

  // sync_action is idle / resync / recover / check / repair. The first two of
  // the repair verbs mean the array is actively rebuilding onto a spare, which
  // is bad but self-healing; idle with members missing means nothing is being
  // done about it, and that is the one to wake someone for.
  const sync = text(f, "sync_action");
  const rebuilding =
    sync === "recover" || sync === "resync" || sync === "repair";
  return { word: rebuilding ? "rebuilding" : "degraded" };
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

/** What a kernel-log event says.
 *
 * The message is the kernel's own sentence, and it is rendered VERBATIM rather
 * than reworded. An operator searching for the string their monitoring or a
 * mailing list gave them has to find it here, and a paraphrase of
 * "blk_update_request: I/O error, dev sdd, sector 13211246" is both longer and
 * less useful than the line itself.
 *
 * The subject is already its own column, so it is not repeated in front of the
 * message the way mdraid's array name is -- the kernel line already names the
 * device inside the sentence.
 *
 * `count` and `suppressed` are the collector's folding, and both are shown:
 * they are the difference between "sdd threw an error" and "sdd threw four
 * hundred", which is the whole diagnosis. */
function kernelMessage(subject: string, f: Record<string, unknown>): string {
  const message = text(f, "message");
  if (!message) return subject || everyField(f);

  const count = Number(f["count"]);
  const suppressed = Number(f["suppressed"]);

  const notes: string[] = [];
  if (Number.isFinite(count) && count > 1) notes.push(`×${count}`);
  if (Number.isFinite(suppressed) && suppressed > 0) {
    notes.push(`${suppressed} more since`);
  }

  return notes.length === 0 ? message : `${message} (${notes.join(", ")})`;
}

/**
 * One line saying what this event was.
 *
 * Returns "" only when the emitter sent nothing to say, which the callers
 * render as the absent marker rather than as a gap.
 */
/** What a hub delivery event says.
 *
 * One event per outage, written by the agent when delivery resumes -- which is
 * the only place that knows an outage was ONE outage. post_failures_total
 * rides every buffered scrape, so a twenty-minute outage replays to the hub as
 * a staircase, and anything counting those deltas would report twenty
 * incidents for one.
 *
 * The sentence says what it COST, because that is the whole question. An
 * outage the ring absorbed is not a problem -- the samples arrived, late --
 * and "buffered and replayed" is what stops a reader going to look for damage
 * that is not there. One that lost samples says so instead, and carries the
 * severity to match.
 *
 * A rejected token gets its own sentence rather than a shared one. "The hub
 * was away and came back" and "this agent is not allowed to talk to the hub"
 * are different problems with different fixes, and only one of them resolves
 * itself. */
function hubMessage(f: Record<string, unknown>): string {
  const lost = count(f, "lost");
  const secs = Math.round(count(f, "outage_ms") / 1000);
  // duration() rather than a bare seconds figure: the log already prints ages
  // that way, so "19 m" sits in the same column as "2 h 4 m".
  const lasted = duration(secs);

  const scrapes = (n: number) => `${n} ${n === 1 ? "scrape" : "scrapes"}`;

  if (text(f, "reason") === "token-rejected") {
    return lost > 0
      ? `Hub rejected this agent's token — ${scrapes(lost)} discarded`
      : "Hub rejected this agent's token";
  }
  // The hub answered and refused the body, so it was never unreachable and
  // must not be described as though it were. A duration would be meaningless
  // here too: nothing was waiting for the hub to come back.
  if (text(f, "reason") === "rejected") {
    return lost > 0
      ? `Hub refused a batch — ${scrapes(lost)} discarded`
      : "Hub refused a batch";
  }
  if (lost > 0) {
    return `Hub unreachable for ${lasted} — ${lost} ${lost === 1 ? "scrape" : "scrapes"} lost`;
  }
  return `Hub unreachable for ${lasted} — buffered and replayed`;
}

/** What a condition transition says.
 *
 * These are the hub's own judgements arriving in the log: a disk crossed its
 * threshold, a host went quiet, units started failing. The sentence names the
 * KIND rather than restating the numbers, because the row beside it already
 * carries the subject and the condition list carries the figures -- what the
 * log adds is WHEN it started and stopped.
 *
 * A cleared row says how long it was open, which is the fact only the log
 * holds: the condition row is gone from the open set by then, and the
 * attention list never knew the duration at all. */
function conditionMessage(
  type: string,
  subject: string,
  f: Record<string, unknown>,
): string {
  const what = CONDITION_LABELS[type] ?? type;
  const named = subject ? `${what} — ${subject}` : what;

  // Getting WORSE is its own transition, and it has to read as one. The hub
  // writes it when a condition's severity rises -- a disk that opened at 91%
  // and reached 97% -- and without a sentence of its own it would print
  // identically to the row that opened it hours earlier, which is the silence
  // this event was added to end.
  if (text(f, "transition") === "escalated") {
    const from = text(f, "from");
    return from ? `${named} worsened from ${from}` : `${named} worsened`;
  }

  if (text(f, "transition") === "cleared") {
    // count() is the guard, not duration(): duration(0) is "0 s" rather than
    // "", so testing the formatted string would have made the bare sentence
    // unreachable and printed "cleared after 0 s" for an event that carried no
    // duration at all.
    const openMs = count(f, "open_ms");
    const open = openMs > 0 ? duration(Math.round(openMs / 1000)) : "";
    // "no longer reported" is not a recovery, and conflating them is how a
    // fleet goes green because nobody is looking at it. See resolved_reason
    // in 0016_conditions.sql.
    const how =
      text(f, "reason") === "vanished" ? "no longer reported" : "cleared";
    return open ? `${named} ${how} after ${open}` : `${named} ${how}`;
  }

  // A deviation NAMES ITS NUMBERS, where the five older kinds do not, and the
  // difference is not inconsistency.
  //
  // "Filesystem nearly full — /var" is a complete thought: everyone knows what
  // full means, and the threshold behind it is the same 90% on every host in
  // the fleet. "Temperature above normal — drivetemp/sda" is not. Above WHAT?
  // The threshold was calibrated from that one drive's own history, so it is a
  // different number on every subject and it exists nowhere else a reader can
  // look -- the condition row is gone from the open set once this clears, and
  // the baseline it was measured against is rebuilt daily. If the log does not
  // carry the figures, nothing does.
  const value = reading(f, "value");
  if (value !== "") {
    // `normal` first, then `p99`, because the event log is HISTORY and the key
    // was renamed when the threshold stopped being a percentile.
    //
    // Every deviation event written before that rename carries `p99`, and they
    // do not stop existing: the log's default window is 24 h and its retention
    // is 90 days, so they are on screen for months. Reading only the new key
    // renders "54.2 C, against a normal under " -- a sentence that stops in the
    // middle, which is worse than the terse fallback below because it looks
    // like the number is missing rather than the key.
    //
    // It is not only old rows, either. 0021 starts the state empty, so every
    // deviation subject re-warms before it can be judged again -- a week for
    // the host kinds -- and any condition still open across that window keeps
    // the detail it opened with.
    const normal = reading(f, "normal") || reading(f, "p99");
    // "for that hour", because the normal is the hour's, and an event log that
    // shows the same sensor raised against 46 C at 09:00 and 62 C at 14:00
    // needs to say why. Only when the detail carries an hour -- an event
    // written under the single-average design has none and must not claim one.
    const hourly = text(f, "hour") === "" ? "" : " for that hour";
    const against =
      text(f, "source") === "device"
        ? `its own limit of ${reading(f, "crit")}`
        : normal === ""
          ? ""
          : `a normal under ${normal}${hourly}`;
    if (against === "") return `${named} — ${value}`;
    return `${named} — ${value}, against ${against}`;
  }

  return named;
}

/** One deviation figure with its unit, or "" when the detail did not carry it.
 *
 * Rounded to one decimal for the reason the fleet list rounds: an average arrives as
 * 46.039215686274510 and printing that suggests the threshold is known to
 * fifteen figures, when it is a moving average over 60-second samples. */
function reading(fields: Record<string, unknown>, key: string): string {
  const value = fields[key];
  if (typeof value !== "number" || !Number.isFinite(value)) return "";
  const rounded = Math.round(value * 10) / 10;
  const shown = Number.isInteger(rounded)
    ? String(rounded)
    : rounded.toFixed(1);
  const unit = text(fields, "unit");
  return unit === "" ? shown : `${shown} ${unit}`;
}

/** The kinds as sentences. Mirrors CONDITION_KIND_INFO in fleet/conditions.ts,
 * which names the same kinds for the attention list -- one vocabulary, so a
 * reader who followed an ?attn= link recognises what the log calls it. */
const CONDITION_LABELS: Record<string, string> = {
  silent: "Stopped reporting",
  sporadic: "Reporting sporadically",
  disk: "Filesystem nearly full",
  "failed-units": "Failed units",
  drive: "Drive errors",
  temperature: "Temperature above normal",
  processes: "Process count above normal",
  load: "Load above normal",
};

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
    case "hub":
      // No subject: a delivery outage is about the host as a whole.
      return hubMessage(f);
    case "container_restart":
    case "container_recreate":
      return containerMessage(event.type, subject, f);
    case "silent":
    case "sporadic":
    case "disk":
    case "failed-units":
    case "drive":
    case "temperature":
    case "processes":
    case "load":
      return conditionMessage(event.type, subject, f);
    default: {
      // Widened deliberately: the tuple is `as const` so the dropdown keeps
      // its order and its literal types, and `event.type` is a plain string
      // off the wire.
      if ((KERNEL_EVENT_TYPES as readonly string[]).includes(event.type)) {
        return kernelMessage(subject, f);
      }
      // An unrecognised type still has a subject and a detail blob, and both
      // belong on the row. This is the pre-existing rendering, kept.
      const rest = everyField(f);
      if (subject && rest) return `${subject} — ${rest}`;
      return subject || rest;
    }
  }
}
