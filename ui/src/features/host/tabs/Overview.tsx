// The Overview tab: what this machine is doing right now, plus what needs
// attention.
//
// The four readings the host's card on the main page shows -- CPU, memory,
// the fullest filesystem, traffic -- drawn by the very cells that card uses,
// so a reading here cannot disagree with the one a page up. Beside them the
// window's events, the likely causes, and every mount; the heaviest
// containers on a host that runs any. Everything else is a chart or a row on
// the tab that owns its subject.
import type { ReactNode } from "react";
import { AttentionBand } from "../../../ui/AttentionBand";
import type {
  ConditionRow,
  Event,
  Filesystem,
  HostDetail,
  MetricsResponse,
  Unit,
} from "../../../lib/api";
import { binaryBytes, bytes, percent, relative } from "../../../lib/format";
import { FLAP_THRESHOLD, isReporting, osLabel } from "../../../lib/host";
import type { Severity } from "../../../ui/Badge";
import { Panel } from "./Panel";
import { SeverityMark } from "../../../ui/SeverityMark";
import { When } from "../../../ui/When";
import { ChartPanel } from "../../../ui/charts/ChartPanel";
import { containerBands, containerStackTotal } from "../../../lib/bands";
import { latestValue } from "../../../lib/metrics";
import { containerTrends } from "../../../lib/containers";
import {
  EVENT_LIMITS,
  RAIL_RANGES,
  rangeLabel,
  type Range,
} from "../../../lib/range";
import { messageOf, NOTIFICATION_EVENT_TYPE } from "../../events/message";
import { AlertMute } from "../AlertMute";
import { MUTABLE_EVENT_TYPES, type MuteTarget } from "../mutes";
import type { Mutes } from "../useMutes";
import {
  CpuCell,
  DiskCell,
  MemoryCell,
  TrafficCell,
} from "../../fleet/hostColumns";
import { buildRows, hostTrendsFrom } from "../../fleet/hostTrends";
import { FilesystemTable } from "./Storage";
// The kind vocabulary and the disk thresholds it carries, both the hub's.
// This page and the fleet page must agree on when a filesystem is worth
// mentioning, or a host warns in one place and reads clean in the other (#92)
// -- and the way they agree now is that neither of them decides.
import {
  deviationReading,
  diskThresholds,
  EMPTY_CATALOGUE,
  fields,
  kindLabel,
  num,
  severityOf,
  staleNote,
  str,
  type Catalogue,
} from "../../fleet/conditions";

// The latest bucket's value, trailing null included, is lib/metrics.ts's
// latestValue(). This page used to scan backwards for the last non-null,
// which reported a host that stopped an hour ago at the final rate it ever
// sent: "the agent is down" rendered as "traffic is steady at 2 MB/s". The
// fleet's traffic cell has always read the latest bucket, so the same dead
// host read as absent there and as busy here -- which is why the rule now has
// exactly one spelling instead of a copy per page.

export interface Attention {
  severity: Severity;
  what: ReactNode;
  /**
   * When this started -- host_conditions.opened_ts, walked once when the hub
   * opened the condition.
   *
   * This band said WHAT was wrong and never for how long, because nothing in
   * the browser could know: a derivation reading the current row has no memory
   * of when it first became true. The rows the hub decides carry it now.
   *
   * Null for the rows this page still derives itself -- a failed unit and a
   * flapping one -- and for a kind whose onset is genuinely unknowable.
   */
  since?: string | null;
  /**
   * `since` is a floor rather than a moment: the hub's walk back through the
   * series hit the end of what is retained, so the row says "over 7 d" instead
   * of naming a bucket where nothing happened.
   */
  sinceAtLeast?: boolean;
  /**
   * The alert a mute on this line silences. Only lines a notification stands
   * behind carry one: a hub condition, or the line saying the host stopped
   * reporting (the `silent` condition, which the line itself does not read).
   * "never reported" and a flapping unit notify nobody, so offer nothing.
   */
  mute?: MuteTarget;
}

// When a host counts as stale rather than merely late. Imported, never
// restated: this used to be its own five-minute constant while hostStatus()
// used three, so a host last seen four minutes ago had its own header call it
// offline, its traffic gauges blanked, and its fleet row marked critical --
// above a panel saying nothing needed attention. lib/host.ts anchors the
// number to the product's alerting rule; there is one definition of down.

/**
 * What is wrong right now. Current state must not sit behind a tab, so this
 * is derived here from the same responses the cards above it render -- there
 * is no second source that could disagree with them.
 *
 * The order is the order it is written in, and this function sorts nothing:
 * a reader who looks twice finds the same rows in the same places. That is
 * the fleet's rule too -- see the note on hostConditions() in
 * fleet/conditions.ts.
 *
 * What renders it DOES group by severity, with a stable partition, so the
 * written order survives inside each group. Grouping is presentation; this
 * list stays as written so the presentation can change without the data
 * moving underneath it.
 */
export function needsAttention(input: {
  host: HostDetail;
  hostMetrics?: MetricsResponse | null;
  /**
   * What the HUB says is wrong with this host, straight off
   * /api/v1/conditions -- this host's rows only.
   *
   * Everything on this list except the units used to be worked out here, from
   * whatever the page had fetched, against thresholds written out in
   * TypeScript. That is what made the fleet page and this page disagree about
   * one host (#92), reconciled by hand and by comment, and it is why not one
   * row could say how long it had been true.
   *
   * The hub keeps a condition per MOUNT and per DEVICE, which is this band's
   * own granularity rather than the fleet's: two disks with pending sectors
   * are two things to replace, and this panel is a list of what to do.
   *
   * An empty list is not "nothing is wrong" on its own, and this type cannot
   * tell the two apart -- `conditionsUnavailable` beside it is what does, for
   * the reason `units: null` used to draw a line.
   */
  conditions: readonly ConditionRow[];
  /** The kind vocabulary, for naming a kind this file has no sentence for. */
  catalogue: Catalogue;
  units: Unit[] | null;
  now?: Date;
}): Attention[] {
  const out: Attention[] = [];
  const now = input.now ?? new Date();

  // Dropped samples, OOM kills and failed hub deliveries used to raise
  // attention rows here. They are events now, and this list is for states.
  //
  // Each read a counter's increase across the window, so each said something
  // that changed when the reader changed the range -- and none of them could
  // say when it happened, which is the tell. A hub outage is one `hub` event
  // written by the agent when delivery resumes; dropped samples are that same
  // event at critical, since the ring only overflows while the hub is away;
  // and an OOM kill is already a critical kmsg event that names the process
  // it killed, which is more than this row ever said.

  // A host that has NEVER reported, said by the page and by nothing else.
  //
  // The hub refuses to raise this and is right to: admin.CreateHost inserts
  // the row and hands over a token, and the operator installs the agent
  // minutes or hours later. A critical condition in that gap -- with an event
  // in the log an alerting engine reads -- says a machine has stopped talking
  // when it has not started yet. A page states what is true now and forgets
  // it, which is exactly what this fact wants.
  if (input.host.last_seen === null) {
    out.push({ severity: "critical", what: "never reported", since: null });
  }

  const byKind = new Map<string, ConditionRow[]>();
  for (const row of input.conditions) {
    const existing = byKind.get(row.kind);
    if (existing) existing.push(row);
    else byKind.set(row.kind, [row]);
  }
  const rowsOf = (kind: string): ConditionRow[] => byKind.get(kind) ?? [];
  const carry = (row: ConditionRow) => ({
    severity: severityOf(row),
    since: row.opened_ts,
    sinceAtLeast: row.opened_at_least,
    mute: { kind: row.kind, subject: row.subject, open: true },
  });

  // Whether the host is talking, from last_seen and NOT from the hub's
  // `silent` condition.
  //
  // The condition would be the tidier source and it is the wrong one: the
  // evaluator deliberately declines to judge silence for the first fourteen
  // minutes after a hub restart (conditions.WarmUp), so for that window a
  // genuinely dead host carries no `silent` row. Reading the tense off it
  // would print "/var IS 96% full" on a machine that has been off since
  // Tuesday, while the fleet row's own pill -- still computed from last_seen
  // -- said offline beside it. That is #92's disagreement rebuilt on a timer.
  //
  // The hub's refusal is right for the LOG, where a false outage is permanent.
  // A page states what is true now and forgets it, so it can just look.
  const reporting = isReporting(input.host, now);

  // Reporting first, because it qualifies everything below it.
  if (!reporting && input.host.last_seen !== null) {
    out.push({
      severity: "critical",
      what: `last reported ${relative(input.host.last_seen, now)}`,
      // The onset IS last_seen, which the sentence already names. Printing it
      // twice in one line would read as two different facts.
      since: null,
      // What notifies about it is the hub's `silent` condition, absent in
      // its warm-up: muting it then still works, only not until resolved.
      mute: {
        kind: "silent",
        subject: rowsOf("silent")[0]?.subject ?? "",
        open: rowsOf("silent").length > 0,
      },
    });
  }
  const sporadic = rowsOf("sporadic")[0];
  if (sporadic !== undefined) {
    out.push({
      ...carry(sporadic),
      what: "reporting sporadically — gaps in the last few hours",
      // A rate has no onset: the gaps ARE the condition, and naming the first
      // of them would date it to a scrape the host happened to miss.
      since: null,
    });
  }

  // One line per MOUNT, not the fullest one. The fleet page collapses because
  // there the unit of interest is the machine; here it is the thing to go and
  // fix.
  for (const row of rowsOf("disk")) {
    const detail = fields(row);
    const mount = str(detail.mount) ?? row.subject;
    const pct = num(detail.pct) ?? 0;
    const free = num(detail.free);
    // "was", not "is", once the host has stopped reporting. The severity is
    // unchanged and deliberately so -- a 96 % disk on a machine that is off is
    // still a 96 % disk, and it is worth fixing before the machine comes back.
    // Only the tense moves, because the figure is now the last one anybody
    // measured rather than a statement about this minute.
    out.push({
      ...carry(row),
      what: `${mount} ${reporting ? "is" : "was"} ${percent(pct)} full${
        free === null ? "" : ` — ${bytes(free)} free`
      }${staleNote(row, now)}`,
    });
  }

  // Drives, by the rule the Storage tab's own table uses -- the hub's copy of
  // it now. This panel used to read clean on a host whose Drives table was
  // showing a critical, failing disk one tab away.
  //
  // One line per device rather than one per host: two disks with pending
  // sectors are two things to replace.
  for (const row of rowsOf("drive")) {
    const detail = fields(row);
    const device = str(detail.device) ?? row.subject;
    const text = str(detail.text) ?? "";
    out.push({
      ...carry(row),
      what: `${device} — ${text}${staleNote(row, now)}`,
      // Deliberately none. SMART attributes are counters with no zero
      // baseline, sampled hourly: the first non-zero reading netra holds is
      // when netra started LOOKING, not when the sector went bad.
      since: null,
    });
  }

  // The deviation kinds, in the fleet page's own words, one line per subject.
  // A host-wide kind has no subject, so the catalogue's label names it.
  for (const kind of ["temperature", "processes", "load"]) {
    for (const row of rowsOf(kind)) {
      const name = kindLabel(input.catalogue, kind).toLowerCase();
      const reading = deviationReading(row);
      if (reading === null) {
        // A row whose detail lost its reading still appears, as the catch-all
        // below prints it.
        out.push({
          ...carry(row),
          what: row.subject === "" ? name : `${row.subject} — ${name}`,
        });
        continue;
      }
      const subject = row.subject === "" ? name : row.subject;
      out.push({
        ...carry(row),
        what: `${subject} ${reporting ? "is" : "was"} ${reading}${staleNote(row, now)}`,
      });
    }
  }

  // The units stay derived HERE, and that is not an oversight. The hub keeps
  // ONE failed-units condition per host, because the fleet list counts hosts;
  // this panel lists the units themselves, and it also lists the ones
  // RESTARTING repeatedly, which is a rate off the event log and not a
  // condition at all. Neither is a threshold anybody could disagree with the
  // hub about, so neither is the duplication this change deleted.
  const failedUnits = rowsOf("failed-units")[0];
  for (const unit of input.units ?? []) {
    if (unit.state === "failed") {
      out.push({
        severity: "warning",
        what: `${unit.unit_name} failed`,
        // systemd's own timestamp for entering this state, which is the same
        // source the hub dates its failed-units condition from.
        since: unit.since,
        // One condition for every failed unit, so one mute for all of them.
        mute: {
          kind: "failed-units",
          subject: failedUnits?.subject ?? "",
          open: failedUnits !== undefined,
          reach: "Mutes every failed unit on this host.",
        },
      });
    } else if (flapping(unit)) {
      out.push({
        severity: "warning",
        what: `${unit.unit_name} restarted ${unit.restarts_1h} times in the last hour`,
        // A rate over the last hour, which is a window rather than an onset.
        since: null,
      });
    }
  }

  // Anything the hub raised that this file has no sentence for still appears,
  // named by the catalogue. Dropping it would be a host page reading clean
  // because the browser did not recognise what was wrong with it -- the exact
  // failure the whole engine exists to end, reintroduced by an incomplete
  // switch statement.
  const written = new Set([
    "silent",
    "sporadic",
    "disk",
    "drive",
    "failed-units",
    "temperature",
    "processes",
    "load",
  ]);
  for (const [kind, rows] of byKind) {
    if (written.has(kind)) continue;
    for (const row of rows) {
      const name = kindLabel(input.catalogue, kind).toLowerCase();
      out.push({
        ...carry(row),
        what: row.subject === "" ? name : `${row.subject} — ${name}`,
      });
    }
  }

  return out;
}

/**
 * Whether a unit is stuck in a restart loop.
 *
 * Repetition is a RATE, so it is measured by counting transitions rather than
 * by inspecting the current state. Two tempting shortcuts are both wrong:
 *
 * - `substate === "auto-restart"` is one sighting, not a rate. It is also the
 *   gap BETWEEN attempts, which at the default RestartSec=100ms a 60-second
 *   scrape will essentially never land in.
 * - "how long has it been in auto-restart" is worse: `since` advances on every
 *   state CHANGE, and a flapping unit changes state constantly, so its age in
 *   the current state is near zero exactly when it is flapping hardest.
 *
 * The unit this catches is the one nothing else can: a service that runs for a
 * few minutes, dies, and comes back looks perfectly healthy at almost every
 * scrape, and systemd never escalates it to `failed` because it does not trip
 * the start limit. Only its history gives it away.
 */
function flapping(unit: Unit): boolean {
  return unit.restarts_1h >= FLAP_THRESHOLD;
}

/** U+2002 around the facts' middot: a plain space reads cramped beside it.
 * Spelled as a code point because the literal trips no-irregular-whitespace. */
const EN_SPACE = String.fromCharCode(0x2002);

/**
 * The machine in one line: what it is, where it is, whose it is.
 *
 * Read once when the page opens and then scrolled past, so it is one line of
 * plain text rather than a card or a collapsed strip. A fact the agent never
 * reported is simply not written: a dash is a placeholder for a value that
 * should be there, and a VPS that never reported a cpu_model is not missing
 * anything.
 */
function factsOf(host: HostDetail): string[] {
  const out: string[] = [];
  if (host.os_name !== null) out.push(osLabel(host.os_name));
  if (host.kernel !== null) out.push(host.kernel);
  if (host.arch !== null) out.push(host.arch);
  if (host.cpu_model !== null) out.push(host.cpu_model);
  if (host.cores !== null)
    out.push(
      host.threads !== null && host.threads !== host.cores
        ? // A comma, not a middot: the dot separates facts on this line,
          // and cores and threads are one fact.
          `${host.cores} cores, ${host.threads} threads`
        : `${host.cores} ${host.cores === 1 ? "core" : "cores"}`,
    );
  if (host.memory_total !== null) out.push(binaryBytes(host.memory_total));
  for (const part of [host.location, host.provider, host.facility])
    if (typeof part === "string" && part !== "") out.push(part);
  if (host.agent_version !== null) out.push(`agent ${host.agent_version}`);
  return out;
}

/** How many events the Overview's card lists before it hands over to the
 * Events tab. */
const EVENTS_SHOWN = 8;

export interface OverviewProps {
  host: HostDetail;
  hostMetrics: MetricsResponse | null;
  filesystemMetrics: MetricsResponse | null;
  /** family=net for this host, one series per interface. */
  netMetrics?: MetricsResponse | null;
  units: Unit[] | null;
  /** This host's open conditions, as the hub decided them. Read by the
   * attention panel; the disk tile takes its thresholds off the catalogue
   * beside it. */
  conditions?: readonly ConditionRow[];
  catalogue?: Catalogue;
  /** The conditions call failed. An empty list then means "netra could not
   * look", never "nothing is wrong", and the panel says so rather than
   * vanishing -- which is what an empty list makes it do. */
  conditionsUnavailable?: boolean;
  /** The range this page is showing. Seeds the picker in every chart
   * enlarged out of this tab. */
  range?: Range;
  /** One family at one range, for an enlarged chart alone -- HostPage's
   * fetchFamily. Without it the enlarged charts carry no picker, which is
   * what a caller that cannot refetch one family should get. */
  fetchFamily?: (family: string, range: Range) => Promise<MetricsResponse>;
  /** The window's events, newest first or not: the card sorts them. */
  events?: readonly Event[] | null;
  /** The mount inventory, for the Filesystems table. */
  filesystems?: readonly Filesystem[] | null;
  /** family=container, for the Containers CPU panel. Empty on a host
   * without Docker, which then draws no panel at all. */
  containerMetrics?: MetricsResponse | null;
  /** Injected by tests so "last reported" is deterministic. */
  now?: Date;
  /** The host's mutes. Without them no line offers a mute. */
  mutes?: Mutes;
}

/**
 * The window's events, newest first, in the event log's own words: the
 * likely cause of whatever the readings above are showing. The rest are one
 * click away on the Events tab.
 *
 * A critical event of a type that notifies carries a mute. It silences the
 * next ones of that type and subject: this one has been sent already.
 */
function EventsCard({
  events,
  hostId,
  range,
  mutes,
}: {
  events: readonly Event[] | null | undefined;
  hostId: number;
  range?: Range;
  mutes?: Mutes;
}) {
  // Notifications are left to the Events tab: each follows the event it
  // reports, and here they would take half the slots meant for causes.
  const sorted = (events ?? [])
    .filter((e) => e.type !== NOTIFICATION_EVENT_TYPE)
    .sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts));
  // Whether the hub's page was full, counted before the filter.
  const full =
    range !== undefined && (events?.length ?? 0) >= EVENT_LIMITS[range];
  return (
    <Panel label="Events" title="Events">
      {events === null || events === undefined ? (
        <p className="note">No events have been read yet.</p>
      ) : sorted.length === 0 ? (
        <p className="note">
          Nothing happened on this host in the{" "}
          {range ? rangeLabel(range) : "window"}.
        </p>
      ) : (
        <>
          <ul className="evlist">
            {sorted.slice(0, EVENTS_SHOWN).map((e) => (
              <li key={e.id}>
                <When iso={e.ts} />
                <EventGlyph severity={e.severity} />
                <span>{messageOf(e)}</span>
                {mutes !== undefined &&
                  e.severity === "critical" &&
                  MUTABLE_EVENT_TYPES.includes(e.type) && (
                    <AlertMute
                      hostId={hostId}
                      target={{
                        kind: e.type,
                        subject: e.subject ?? "",
                        open: false,
                      }}
                      mutes={mutes}
                    />
                  )}
              </li>
            ))}
          </ul>
          {sorted.length > EVENTS_SHOWN && (
            <a className="more" href={`/hosts/${hostId}/events`}>
              {/* A full page is the hub's limit, not the window's count:
                  there may be more than it returned. */}
              {full
                ? `Over ${sorted.length - EVENTS_SHOWN} more on the Events tab`
                : `${sorted.length - EVENTS_SHOWN} more on the Events tab`}
            </a>
          )}
        </>
      )}
    </Panel>
  );
}

/** Severity by shape as well as hue, the same marks the charts draw. */
function EventGlyph({ severity }: { severity: Event["severity"] }) {
  if (severity === "info")
    return <span className="evinfo" role="img" aria-label="info" />;
  return <SeverityMark kind={severity} />;
}

export function Overview({
  host,
  hostMetrics,
  netMetrics,
  filesystemMetrics,
  units,
  conditions = [],
  catalogue = EMPTY_CATALOGUE,
  conditionsUnavailable = false,
  range,
  fetchFamily,
  events,
  filesystems,
  containerMetrics = null,
  now,
  mutes,
}: OverviewProps) {
  const attention = needsAttention({
    host,
    hostMetrics,
    conditions,
    catalogue,
    units,
    now,
  }).map((row) =>
    row.mute === undefined || mutes === undefined
      ? row
      : {
          ...row,
          action: (
            <AlertMute hostId={host.id} target={row.mute} mutes={mutes} />
          ),
        },
  );

  // The host's row from the fleet page, built the way the fleet builds it,
  // so the four readings below are the fleet's own cells: one figure and
  // one trend each, the same severity rule, the same fullest mount. A
  // reading here and on the host card one page up cannot disagree.
  const row = buildRows(
    [host],
    new Map([
      [
        host.id,
        hostTrendsFrom(
          hostMetrics,
          netMetrics ?? null,
          filesystemMetrics,
          null,
        ),
      ],
    ]),
    diskThresholds(catalogue),
  )[0]!;
  const facts = factsOf(host);
  // The cells' enlarged charts open at the page's range.
  const at: Range = range ?? "1h";
  const docker = containerBands(containerTrends(containerMetrics), "cpu");

  return (
    <>
      {/* Above the cards, not among them.
          A card inside .cardcols only ever reaches the top of ONE column, at
          a third or a half of the page's width -- and under the old balanced
          flow it did not even reach that reliably: what is wrong with the
          host sat eighth, below the disk meters. It is the first thing on
          this tab that must be read, so it is lifted out of the columns
          entirely and spans the page. The System card below it is hoisted the
          same way for a different reason -- see there.

          Present only when something is wrong, the same rule the fleet band
          follows: a permanently visible "All clear" box in the best position
          on the page is a box people stop reading, and it costs that position
          on every healthy host. When there is nothing to say this tab says
          nothing -- the line that used to sit here reported that a check had
          run and found nothing, which is the one thing a reader can already
          see, in the position the real answer occupies on every other host.
          AttentionCounts does the same on the fleet page. */}
      {/* The one case where saying nothing is the lie. "Nothing is wrong" and
          "netra could not be asked what is wrong" render identically as an
          absent panel, and only one of them is a fact about this host. */}
      {conditionsUnavailable && (
        <p className="note" role="alert">
          netra could not be asked what is wrong with this host — nothing below
          is judged, so an empty panel means it could not look rather than that
          it looked and found nothing.
        </p>
      )}
      {/* One band, shared with the fleet's container list. Two renderings of
          one vocabulary is how this page and the fleet came to disagree about
          a single host (#92). */}
      <AttentionBand rows={attention} now={now ?? new Date()} />
      {/* The machine in one line, under the name: what it is, where it is.
          Its uptime is in the page's header. */}
      {facts.length > 0 && (
        <section aria-label="System">
          {/* The dot is text, not a ::before: a copied line then reads
              "Debian 13 · 6.12 · amd64" rather than a fact per line. It
              ends the fact before it, like punctuation, and so do both en
              spaces: a wrapped line ends on a dot and opens on the next
              fact, never on a dot or an indent. */}
          <p className="hostfacts">
            {facts.map((fact, i) => (
              <span key={i} className="fact" title={fact}>
                <span className="txt">{fact}</span>
                {i < facts.length - 1 && (
                  <span className="sep" aria-hidden="true">
                    {`${EN_SPACE}·${EN_SPACE}`}
                  </span>
                )}
              </span>
            ))}
          </p>
        </section>
      )}

      {/* The four readings the host's card on the main page shows, drawn by
          the same cells: CPU, memory, the fullest filesystem and traffic. */}
      <div className="readings">
        <div className="reading">
          <CpuCell row={row} range={at} layout="card" current />
        </div>
        <div className="reading">
          <MemoryCell row={row} range={at} layout="card" />
        </div>
        <div className="reading">
          <DiskCell row={row} range={at} layout="card" />
        </div>
        <div className="reading">
          <TrafficCell row={row} range={at} layout="card" />
        </div>
      </div>

      {/* What happened, beside where the disks stand: the likely causes next
          to the most common thing to go and fix. */}
      <div className="ovgrid">
        <EventsCard
          events={events}
          hostId={host.id}
          range={range}
          mutes={mutes}
        />
        {/* A card like the Events card beside it: one box title style. */}
        <Panel label="Filesystems" title="Filesystems" className="fstable">
          <FilesystemTable
            rows={filesystems ?? null}
            metrics={filesystemMetrics}
            range={range}
            lastSeen={host.last_seen}
            fetchFamily={fetchFamily}
          />
        </Panel>
      </div>

      {/* The heaviest containers, the same panel the Containers tab leads
          with, on a host that runs any. */}
      {docker.length > 0 && (
        <ChartPanel
          title="Docker CPU"
          about="The five heaviest containers on this host, the rest as one band. Percent of ONE core, as docker stats reports it."
          fmt={percent}
          // The stack's height, every container's together: band 0's alone
          // would print one container's share as Docker's.
          nowValue={latestValue(containerStackTotal(docker))}
          series={docker}
          stacked
          legend
          height={150}
          window={containerMetrics?.window ?? null}
          range={range}
          ranges={RAIL_RANGES}
          fetchSeries={
            fetchFamily
              ? async (next: Range) => {
                  const res = await fetchFamily("container", next);
                  return {
                    series: containerBands(containerTrends(res), "cpu"),
                    window: res.window ?? null,
                  };
                }
              : undefined
          }
        />
      )}
    </>
  );
}
