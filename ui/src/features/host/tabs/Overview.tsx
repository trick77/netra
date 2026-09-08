// The Overview tab: what this machine is doing right now, as figures, plus
// what needs attention.
//
// It used to be ten charts poured into one, two or three columns -- and four
// of them were the System tab's panels drawn a second time. Reading it meant
// reading ten charts, and the numbers an operator actually scans for (what
// the CPU is at, how full the busiest disk is, how much traffic is moving)
// were stated nowhere: they had to be read off a silhouette. The tiles are
// the other half of that. Each one prints the reading and carries the
// window's trend behind it, and each one links to the panel that draws the
// same column in full, on the tab that owns it.
//
// What is deliberately NOT here any more: the per-core stack, the CPU time
// breakdown and the memory trend stack, all three of which the System tab
// already draws (cpu-cores, cpu-time-breakdown, host-memory); the memory
// meters, whose reading is the Memory tile and whose threshold moved with it;
// the Inventory facts, which are three counts of things that have their own
// tabs; and the sensor cards, which are hardware facts and are now on System.
import type { ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { AttentionBand } from "../../../ui/AttentionBand";
import type {
  ConditionRow,
  HostDetail,
  MetricsResponse,
  Unit,
} from "../../../lib/api";
import {
  ABSENT,
  binaryBytes,
  bytes,
  duration,
  percent,
  relative,
} from "../../../lib/format";
import { FLAP_THRESHOLD, isReporting, osLabel } from "../../../lib/host";
import { Badge, type Severity } from "../../../ui/Badge";
import { Panel } from "./Panel";
import { Meter } from "../../../ui/Meter";
import { StatTile } from "../../../ui/StatTile";
import type { Range } from "../../../lib/range";
// The kind vocabulary and the disk thresholds it carries, both the hub's.
// This page and the fleet page must agree on when a filesystem is worth
// mentioning, or a host warns in one place and reads clean in the other (#92)
// -- and the way they agree now is that neither of them decides.
import {
  diskThresholds,
  EMPTY_CATALOGUE,
  kindLabel,
  type Catalogue,
} from "../../fleet/conditions";
// The tiles' own module: what each one reads, what it says when the column is
// absent, and when it earns a status hue. Also the home of latest(),
// current() and filesystemRows(), which moved there with it -- both files
// need them, and a second copy of either is how this page and its tiles would
// come to answer differently about one column.
import {
  current,
  filesystemRows,
  latest,
  overviewTiles,
} from "./overviewTiles";
import type { FilesystemRow, Tile } from "./overviewTiles";
// The two charts this tab keeps, drawn through the same spec machinery the
// System and Network tabs use. Rendered from their slugs rather than rebuilt
// here so there is one title, one `about`, one /hosts/{id}/chart/<slug> URL
// and one enlarge behaviour per chart across every tab -- a second spelling
// of "Traffic" is exactly how the fleet row and this page came to disagree
// about one host before.
import { SpecPanel } from "./Graphs";
import { specForSlug, type PanelSpec } from "../chartSpecs";

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
}

// When a host counts as stale rather than merely late. Imported, never
// restated: this used to be its own five-minute constant while hostStatus()
// used three, so a host last seen four minutes ago had its own header call it
// offline, its traffic gauges blanked, and its fleet row marked critical --
// above a panel saying nothing needed attention. lib/host.ts anchors the
// number to the product's alerting rule; there is one definition of down.

// Worst first, and the only two needsAttention() emits: `ok` is not a
// condition and `neutral` is not a severity anything here can be at. A
// severity missing from this list would drop its rows silently, which is why
// it is written out rather than derived from the data.
const ATTENTION_SEVERITIES = ["critical", "warning"] as const;

const SEVERITY_WORD: Record<Severity, string> = {
  critical: "Critical",
  warning: "Warning",
  ok: "OK",
  neutral: "Unknown",
};

const SEVERITY_CLASS: Record<Severity, string> = {
  critical: "st-crit",
  warning: "st-warn",
  ok: "st-ok",
  neutral: "",
};

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
  const severityOf = (row: ConditionRow): Severity =>
    row.severity === "critical" ? "critical" : "warning";
  const detailOf = (row: ConditionRow): Record<string, unknown> => {
    if (typeof row.detail !== "object" || row.detail === null) return {};
    if (Array.isArray(row.detail)) return {};
    return row.detail as Record<string, unknown>;
  };
  const numberOf = (v: unknown): number | null =>
    typeof v === "number" && Number.isFinite(v) ? v : null;
  const textOf = (v: unknown): string | null =>
    typeof v === "string" && v !== "" ? v : null;
  // "— not measured since 4 h ago". Said rather than hidden: the page used to
  // drop a mount whose reading had stopped moving, which silently retired the
  // condition on it. The hub will not make that call at all, because a hung
  // NFS export and an unmounted volume are indistinguishable from where it
  // stands, so the reader is told the number beside it is old.
  const staleNote = (row: ConditionRow): string =>
    row.stale ? ` — not measured since ${relative(row.measured_ts, now)}` : "";
  const carry = (row: ConditionRow) => ({
    severity: severityOf(row),
    since: row.opened_ts,
    sinceAtLeast: row.opened_at_least,
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
    const detail = detailOf(row);
    const mount = textOf(detail.mount) ?? row.subject;
    const pct = numberOf(detail.pct) ?? 0;
    const free = numberOf(detail.free);
    // "was", not "is", once the host has stopped reporting. The severity is
    // unchanged and deliberately so -- a 96 % disk on a machine that is off is
    // still a 96 % disk, and it is worth fixing before the machine comes back.
    // Only the tense moves, because the figure is now the last one anybody
    // measured rather than a statement about this minute.
    out.push({
      ...carry(row),
      what: `${mount} ${reporting ? "is" : "was"} ${percent(pct)} full${
        free === null ? "" : ` — ${bytes(free)} free`
      }${staleNote(row)}`,
    });
  }

  // Drives, by the rule the Storage tab's own table uses -- the hub's copy of
  // it now. This panel used to read clean on a host whose Drives table was
  // showing a critical, failing disk one tab away.
  //
  // One line per device rather than one per host: two disks with pending
  // sectors are two things to replace.
  for (const row of rowsOf("drive")) {
    const detail = detailOf(row);
    const device = textOf(detail.device) ?? row.subject;
    const text = textOf(detail.text) ?? "";
    out.push({
      ...carry(row),
      what: `${device} — ${text}${staleNote(row)}`,
      // Deliberately none. SMART attributes are counters with no zero
      // baseline, sampled hourly: the first non-zero reading netra holds is
      // when netra started LOOKING, not when the sector went bad.
      since: null,
    });
  }

  // The units stay derived HERE, and that is not an oversight. The hub keeps
  // ONE failed-units condition per host, because the fleet list counts hosts;
  // this panel lists the units themselves, and it also lists the ones
  // RESTARTING repeatedly, which is a rate off the event log and not a
  // condition at all. Neither is a threshold anybody could disagree with the
  // hub about, so neither is the duplication this change deleted.
  for (const unit of input.units ?? []) {
    if (unit.state === "failed") {
      out.push({
        severity: "warning",
        what: `${unit.unit_name} failed`,
        // systemd's own timestamp for entering this state, which is the same
        // source the hub dates its failed-units condition from.
        since: unit.since,
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

/**
 * The reporting agent, identified exactly: its version and the commit it was
 * built from.
 *
 * buildinfo.Commit() is already the SHORT sha, so nothing is truncated here
 * -- and it is "unknown" for a binary built without the ldflags stamp, which
 * is a real state (a `go build` from a working tree) and not a value worth
 * printing. An unstamped build falls back to the version alone rather than
 * reading "0.4.1 · unknown", which looks like a bug in netra rather than a
 * fact about how that agent was compiled.
 *
 * A version with no commit at all is still the answer when that is all the
 * host sent; a host that reported neither reads as absent, never as an empty
 * string.
 */
function agentBuild(host: HostDetail): string {
  const version = host.agent_version;
  const commit = host.build_commit;
  if (version === null) return commit ?? ABSENT;
  if (commit === null || commit === "" || commit === "unknown") return version;
  return `${version} · ${commit}`;
}

/**
 * Where this host is, as labelled facts for the System strip -- straight from
 * what its own agent reported.
 *
 * Empty when the agent reported none of the three, which is what makes the
 * caller's spread a no-op rather than three em dashes: a host whose operator
 * set no AGENT_LOCATION is not missing an address, it was never given one.
 * Reporting ANY of them is the test, not each field on its own -- an agent
 * that sends a provider and no facility has a gap somebody meant to fill,
 * and a labelled strip is exactly where a gap gets marked.
 *
 * Location leads, ahead of the provider, reversing the fleet row's order on
 * purpose. The row is scanned across a fleet, where the provider is what
 * tells one host from the next; this page is about one machine the reader has
 * already chosen, and where it is is what they came to read.
 */
function locationFacts(host: HostDetail): [string, ReactNode][] {
  const reported = [host.location, host.provider, host.facility].some(
    (part) => typeof part === "string" && part !== "",
  );
  if (!reported) return [];
  return [
    ["Location", host.location ?? ABSENT],
    ["Provider", host.provider ?? ABSENT],
    ["Facility", host.facility ?? ABSENT],
  ];
}

/** The same pairs as Facts, written label-above-value four across instead of
 * label-beside-value down a column -- the host's System card, which spans the
 * page and was spending 170px of the best position on it to say eight short
 * things.
 *
 * A key/value pair is still a dl: the wrapper div around each dt/dd is what
 * the HTML spec calls a name-value group, and it is a real grid item here
 * rather than the `display: contents` box that broke Facts' separators. No
 * nth-of-type selector depends on it.
 *
 * Values do not wrap. A processor model is the one string long enough to
 * need two lines, and letting it take them makes the block a different
 * height on every host in the fleet -- so it ellipsizes and carries the full
 * text as a title. See .sysstrip. */
function FactStrip({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="sysstrip">
      {rows.map(([key, value]) => (
        <div className="f" key={key}>
          <dt>{key}</dt>
          <dd title={typeof value === "string" ? value : undefined}>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

/**
 * The two charts this tab keeps, resolved once at module load.
 *
 * Resolved here rather than inside the render, and thrown for rather than
 * defaulted: a slug that names no spec is a rename that missed this page, and
 * the failure it produces at render time is an empty card that looks like a
 * host with no data. resolveGroups() in chartSpecs.ts throws at module load
 * for the same reason and says so at length.
 */
function requireSpec(slug: string): PanelSpec {
  const spec = specForSlug(slug);
  if (spec === undefined) {
    throw new Error(`Overview: no chart spec for slug "${slug}"`);
  }
  return spec;
}

const LOAD_SPEC = requireSpec("load-averages");
// The SUMMED pair, not the per-interface stack the Network tab draws: this
// page asks what the box is moving, and which cable moved it is that tab's
// question. See host-traffic-total in chartSpecs.ts.
const TRAFFIC_SPEC = requireSpec("host-traffic-total");

/**
 * Twelve tracks split between two cards, by how many tiles each holds.
 *
 * Clamped at 3, so a card with one tile is still wide enough for its heading
 * and its tile, and the other card never takes more than 9 -- past that its
 * own tiles start looking stretched rather than generous. Both empty is the
 * degenerate case and splits evenly; neither card renders then anyway.
 */
function splitRow(left: number, right: number): [number, number] {
  const total = left + right;
  if (total === 0) return [6, 6];
  const raw = Math.round((12 * left) / total);
  const span = Math.min(9, Math.max(3, raw));
  return [span, 12 - span];
}

/** Where a tile leads: the panel that draws the same column in full. Built
 * here rather than in overviewTiles.ts because the host id is a routing fact
 * and the tiles are a data one -- see lib/router.ts for the shape. */
function chartHref(hostId: number, slug: string): string {
  return `/hosts/${hostId}/chart/${slug}`;
}

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
  /** Where a tile navigates to. Client-side, matching Tabs and StatFigure;
   * the href stays real either way. */
  onOpenChart?: (slug: string) => void;
  /** Injected by tests so "last reported" is deterministic. */
  now?: Date;
}

/** One card of tiles. The heading is the card's, the tiles are its body, and
 * the grid inside reflows on its own -- there is no per-tile placement to
 * keep in step with a breakpoint, which is the whole reason the mosaic
 * replaced the hand-placed columns. */
function TileCard({
  title,
  tiles,
  span,
  hostId,
  onOpenChart,
}: {
  title: string;
  tiles: Tile[];
  span: number;
  hostId: number;
  onOpenChart?: (slug: string) => void;
}) {
  if (tiles.length === 0) return null;
  return (
    <div className="mo" style={{ gridColumn: `span ${span}` }}>
      <Panel label={title} title={title}>
        <div className="tiles">
          {tiles.map((tile) => (
            <StatTile
              key={tile.key}
              label={tile.label}
              value={tile.value}
              unit={tile.unit}
              sub={tile.sub}
              values={tile.values}
              color={tile.color}
              severity={tile.severity}
              href={
                tile.slug === undefined
                  ? undefined
                  : chartHref(hostId, tile.slug)
              }
              onSelect={
                tile.slug === undefined || onOpenChart === undefined
                  ? undefined
                  : () => onOpenChart(tile.slug as string)
              }
            />
          ))}
        </div>
      </Panel>
    </div>
  );
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
  onOpenChart,
  now,
}: OverviewProps) {
  // The host, not just the metrics: filesystemRows prefers the hub's stored
  // per-mount gauge, which is what keeps this card readable on a machine that
  // is switched off. See its docstring, and the fleet's fullestFilesystem for
  // the same choice made for the same reason one page up.
  const filesystems = filesystemRows(filesystemMetrics, host);
  const attention = needsAttention({
    host,
    hostMetrics,
    conditions,
    catalogue,
    units,
    now,
  });

  const tiles = overviewTiles({
    host,
    hostMetrics,
    filesystemMetrics,
    netMetrics,
    // The hub's disk thresholds, for the Disk tile's own colour: the tile has
    // to judge a mount that is perfectly healthy, which no condition covers.
    thresholds: diskThresholds(catalogue),
    now,
  });

  // Each row's twelve tracks, split between its two cards in proportion to
  // how many tiles each holds.
  //
  // Fixed spans looked right on a host that reports everything and wrong on
  // every other: a swapless VM has one Memory pressure tile, and at a fixed
  // half-width that tile stretched across 640px of card beside a Network
  // card packed with three. Sized by content the same host reads 3 / 9 and
  // the tiles either side come out the same width -- which is the actual
  // goal, since a tile is a fixed thing and only the card around it is
  // elastic.
  const [systemSpan, kernelSpan] = splitRow(
    tiles.system.length,
    tiles.kernel.length,
  );
  const [pressureSpan, networkSpan] = splitRow(
    tiles.pressure.length,
    tiles.network.length,
  );

  // Read once and used twice -- the summary line and the strip's Uptime row.
  // Two copies of this expression is how the two come to disagree on a host
  // whose metric and whose host row say different things.
  const uptime = latest(hostMetrics, "uptime_s") ?? host.uptime_s;

  // Only the two families these two specs name. The Overview does not fetch
  // the other seven and a spec that quietly read one would draw on a page
  // that never asked for it -- see the note on `extra` in SpecPanel.
  const sources = { host: hostMetrics, net: netMetrics ?? null };

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
      {/* Out of the flow, like the attention band -- but not because it has to
          be read first. It is the machine's identity card, and it was asked
          for at the top right of the page. The columns cannot give it one:
          the head of the last .cardcol is a third of the page wide, and this
          card is eight facts laid out four across -- at that width the strip
          wraps to two facts a row and the card is taller than the chart
          beside it. Full width above the columns is the position it can
          actually hold.

          Shut by default, and that is the change here. Writing the eight
          facts label-above-value four across took the block from ~170px to
          ~115px, which fixed the content and left the frame: a card header, a
          border, 16px of body padding top and bottom and a bottom margin --
          more chrome than the facts it wrapped, in the best position on the
          page, for identity that is read once when the page opens and then
          scrolled past. So the summary IS the card now, one line carrying the
          five facts read in passing, and the labelled eight-fact strip is
          what opens underneath. Nothing is dropped; three facts stop being
          permanently on screen.

          A native <details>, no mirrored useState -- the same shape the fleet
          attention band uses, and for the reason spelled out there: the
          element owns the open state, and the disclosed content has to live
          INSIDE it or a screen reader is told "expanded" and handed nothing.

          <section aria-label="System"> stays. With the card header gone the
          word "System" is painted nowhere, so the accessible name is the only
          thing left carrying it -- and it is what every test on this card
          finds the card through. See .sysfold. */}
      <section aria-label="System">
        <details className="sysfold">
          {/* Five facts, and an absent one is simply not written rather than
              given an em dash. That is the rule the Temperature card and the
              fleet list's site line already follow: a dash is a placeholder
              for a value that should be there and is missing, and a VPS that
              never reported a cpu_model is not missing anything. The labelled
              strip below keeps ABSENT, because a labelled table is where a
              gap does need a mark. */}
          <summary>
            {/* What the word "Details" used to say out loud, kept for the
                readers who only ever heard it.

                The facts on this line ARE the summary's accessible name, and
                a host can have none of them: an agent in a container that
                reports no os_name, kernel, cpu_model, memory_total or uptime
                leaves the line empty, and a screen reader then announces an
                unlabelled collapsed disclosure. Visually hidden text rather
                than aria-label, because a label REPLACES the name -- every
                host would be announced as "System details" and the five facts
                a sighted reader gets for free would be gone. This prefixes
                them instead, and stands alone when there are none. */}
            <span className="sr-only">System details</span>
            {/* The disclosure mark, and the whole of it: the word "Details"
                that used to sit at the right end was a label on a control
                that a chevron says without spending a fact's worth of line
                on it.

                Leading rather than trailing, matching the group toggle in
                ui/Table.tsx: a mark at the start of the line is read before
                the line, which is the order "this opens" has to be learned
                in. One icon rotated in two states, never two icons.

                A bare <svg>, deliberately NOT wrapped in a span: the dot
                separator is drawn by `summary span + span::before`, so a
                leading span would hand the OS name a dot with nothing to
                its left. aria-hidden because <details>/<summary> announces
                the open state itself -- a screen reader that also read the
                icon would hear the control twice. */}
            <ChevronRight className="chev" aria-hidden="true" />
            {/* Guarded like the other four. os_name is nullable in the store
                -- ingest.go NULLIFs it, for an agent in a container that
                cannot read the host's /etc/os-release -- and osLabel(null) is
                ABSENT, so an unguarded span left a host with nothing else to
                say showing a summary line of one em dash. The separators are
                drawn from the SECOND span onward, so whichever fact ends up
                first simply takes no leading dot. */}
            {host.os_name !== null && (
              <span className="strong">{osLabel(host.os_name)}</span>
            )}
            {host.kernel !== null && <span>{host.kernel}</span>}
            {/* The one value long enough to push this line onto a second row
                by itself: cpu_model is the raw string -- "AMD EPYC 7402P
                24-Core Processor", not the short marketing name. It gives way
                first and carries its full text as a title, the same treatment
                .sysstrip dd gives it for the same reason. */}
            {host.cpu_model !== null && (
              <span className="cpu" title={host.cpu_model}>
                {host.cpu_model}
              </span>
            )}
            {host.memory_total !== null && (
              <span>{binaryBytes(host.memory_total)}</span>
            )}
            {/* Guarded like the three above it, and for the same reason:
                uptime_s is nullable on both the metric and the host row, and
                duration(null) is ABSENT -- so an unguarded span writes
                "up —" on a host that never reported one, which is the exact
                placeholder this line does not write. */}
            {uptime !== null && (
              <span className="dim">up {duration(uptime)}</span>
            )}
          </summary>
          <div className="body">
            <FactStrip
              rows={[
                // Where the machine is, ahead of what it is. An operator
                // reading this card top to bottom asks where this box sits
                // and whose it is before they ask which processor it has --
                // and the page could answer neither: it printed the site
                // name, an internal label from a table filled in by hand,
                // while the host's own agent had been reporting the real
                // answer on every metadata post.
                //
                // Written as labelled facts rather than as the one joined
                // line the fleet row prints. A labelled strip is where a gap
                // does need a mark (see the summary above), so an agent that
                // reports a provider and no facility says so here, where the
                // same dash in a fleet row would be one of forty.
                ...locationFacts(host),
                ["OS", osLabel(host.os_name)],
                ["Kernel", host.kernel ?? ABSENT],
                ["Architecture", host.arch ?? ABSENT],
                ["Processor", host.cpu_model ?? ABSENT],
                [
                  "Cores",
                  host.cores === null
                    ? ABSENT
                    : `${host.cores} cores · ${host.threads ?? ABSENT} threads`,
                ],
                // The machine's installed RAM, which this page never stated.
                // memory_total was blank on every host until the agent started
                // sending it, and its only reader since has been the memory
                // chart's fallback denominator -- so the fact itself, the one
                // an operator asks for first when sizing anything, was
                // collected and never shown.
                ["Memory", binaryBytes(host.memory_total)],
                ["Uptime", duration(uptime)],
                // The exact binary that is reporting, not just its release.
                //
                // "0.4.1" does not identify a build: it is whatever was last
                // tagged, and the agent in front of you may be a rebuild, a
                // patched branch, or the same tag from before a fix landed. The
                // commit is what makes the answer exact, and it is the first
                // thing anyone asks when a host reports something the code is
                // not supposed to be able to report. Both were already collected
                // (buildinfo.Version and buildinfo.Commit) and served on
                // HostDetail; only the version was ever shown.
                ["Agent", agentBuild(host)],
              ]}
            />
          </div>
        </details>
      </section>

      {/* Placed on a grid, not poured into columns.
        The cards used to flow into hand-assigned columns keyed off
        matchMedia: which column a card landed in was a JS decision that had
        to be kept in step with a CSS breakpoint, and the two disagreeing put
        a card in a column nobody could see. A 12-track grid needs neither --
        a card states its own span and the browser does the rest, and below
        1100px every span collapses to the full width in CSS alone. */}
      <div className="mosaic">
        <TileCard
          title="System metrics"
          tiles={tiles.system}
          span={systemSpan}
          hostId={host.id}
          onOpenChart={onOpenChart}
        />
        <TileCard
          title="Kernel"
          tiles={tiles.kernel}
          span={kernelSpan}
          hostId={host.id}
          onOpenChart={onOpenChart}
        />
        <TileCard
          title="Memory pressure"
          tiles={tiles.pressure}
          span={pressureSpan}
          hostId={host.id}
          onOpenChart={onOpenChart}
        />
        <TileCard
          title="Network"
          tiles={tiles.network}
          span={networkSpan}
          hostId={host.id}
          onOpenChart={onOpenChart}
        />

        {/* Traffic on the left, under the Network tiles it belongs to, and
          the load average on the right under the kernel ones. The reading
          order of the row above is the reading order of this one.

          "Traffic" and "Load averages", not "Network load" and "System
          load": those are the titles the Network and System tabs draw this
          subject under, and one chart with two names is how a reader comes to
          think they are looking at two things.

          Load averages IS the System tab's spec. Traffic is not the Network
          tab's any more -- that tab draws host-traffic per interface and this
          panel draws host-traffic-total, the summed pair -- but the title is
          shared deliberately, for the reason above: they are the same
          quantity at two levels of detail, and the stack's outer edge there
          is this line. */}
        <div className="mo" style={{ gridColumn: "span 6" }}>
          <SpecPanel
            spec={TRAFFIC_SPEC}
            sources={sources}
            range={range}
            fetchFamily={fetchFamily}
          />
        </div>
        <div className="mo" style={{ gridColumn: "span 6" }}>
          <SpecPanel
            spec={LOAD_SPEC}
            sources={sources}
            range={range}
            fetchFamily={fetchFamily}
          />
        </div>

        {/* The one meter left on this page, and it stays a meter: a
          filesystem is a bounded quantity with a fill line, which is the one
          shape a bar says better than a figure. Every other reading here is
          a rate or a level and is a tile. */}
        <div className="mo" style={{ gridColumn: "span 12" }}>
          <Panel label="Disk" title="Disk">
            {filesystems.length === 0 ? (
              <p className="note">No filesystems have been read yet.</p>
            ) : (
              <div className="fs-list">
                {filesystems.map((fs) => (
                  <Meter
                    key={fs.label}
                    label={fs.label}
                    // df's Use%: used / (used + free), never used / total. total
                    // includes the root reserve, which is neither in use nor
                    // allocatable, so dividing by it reports a full disk as less
                    // full than df does -- and df's number is the one the
                    // operator has already seen over SSH. Same definition the
                    // fleet's disk column uses, so the two cannot disagree about
                    // one filesystem.
                    value={fs.used}
                    max={
                      fs.used === null || fs.free === null
                        ? null
                        : fs.used + fs.free
                    }
                    // No severity passed: Meter's own 70/95 colours the
                    // fill and the figure, the rule every other bar, meter
                    // and sparkline in the app is read by. A bar answers
                    // "what does this number say", and 97% says the same
                    // thing on a 4 TB array as on a 250 GB root.
                    //
                    // The compound rule -- high enough AND with little enough
                    // left, diskSeverityFor in fleet/conditions.ts -- answers
                    // the OTHER question, "is this worth someone's
                    // attention", and it still governs the attention list
                    // above. Putting the fill on it as well made red
                    // unreachable on any volume over roughly 400 GB, since
                    // critical there also needs under 20 GiB free: a 97%
                    // filesystem drew amber. The two questions are allowed to
                    // disagree -- a full disk with real headroom draws a red
                    // bar and is still not in the list.
                    formatValue={() =>
                      `${bytes(fs.used)} used · ${bytes(fs.free)} free · ${bytes(fs.total)} size`
                    }
                  />
                ))}
              </div>
            )}
          </Panel>
        </div>
      </div>
    </>
  );
}
