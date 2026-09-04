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
import type { HostDetail, MetricsResponse, Unit } from "../../../lib/api";
import { counterIncrease, griddedValues } from "../../../lib/metrics";
import {
  ABSENT,
  binaryBytes,
  bytes,
  duration,
  percent,
  relative,
} from "../../../lib/format";
import {
  FLAP_THRESHOLD,
  isReporting,
  osLabel,
  STALE_THRESHOLD_MS,
} from "../../../lib/host";
import { Badge, type Severity } from "../../../ui/Badge";
import { Panel } from "./Panel";
import { Meter } from "../../../ui/Meter";
import { StatTile } from "../../../ui/StatTile";
import type { Range } from "../../../lib/range";
// The fleet band's thresholds, imported rather than written out again. The
// comment on them in fleet/conditions.ts spells out why: this page and that
// one must agree on when a filesystem is worth mentioning, or a host warns
// in one place and reads clean in the other.
import { diskState } from "../../fleet/conditions";
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
}

// When a host counts as stale rather than merely late. Imported, never
// restated: this used to be its own five-minute constant while hostStatus()
// used three, so a host last seen four minutes ago had its own header call it
// offline, its traffic gauges blanked, and its fleet row marked critical --
// above a panel saying nothing needed attention. lib/host.ts anchors the
// number to the product's alerting rule; there is one definition of down.

// Worst first, and the only three needsAttention() emits: `ok` is not a
// condition and `neutral` is not a severity anything here can be at. A
// severity missing from this list would drop its rows silently, which is why
// it is written out rather than derived from the data.
const ATTENTION_SEVERITIES = ["critical", "serious", "warning"] as const;

const SEVERITY_WORD: Record<Severity, string> = {
  critical: "Critical",
  serious: "Serious",
  warning: "Warning",
  ok: "OK",
  neutral: "Unknown",
};

const SEVERITY_CLASS: Record<Severity, string> = {
  critical: "st-crit",
  serious: "st-serious",
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
  agentMetrics: MetricsResponse | null;
  hostMetrics?: MetricsResponse | null;
  filesystems: FilesystemRow[];
  units: Unit[] | null;
  now?: Date;
}): Attention[] {
  const out: Attention[] = [];
  const now = input.now ?? new Date();

  const dropped = latest(input.agentMetrics, "buffer_dropped_total");
  if (dropped !== null && dropped > 0) {
    // The agent's ring buffer overflowed: samples that were collected were
    // never delivered. Nothing else netra reports is more important, and
    // no chart can show it, because the missing data is the evidence.
    out.push({
      severity: "critical",
      what: `${dropped} ${dropped === 1 ? "sample" : "samples"} dropped before delivery — this host's history has holes`,
    });
  }

  // The kernel killed something to stay alive. This is the one memory fact
  // no chart can carry: mem_used is back to normal by the time anyone
  // looks, precisely BECAUSE the kill happened, so a host that OOM-killed
  // its database at 04:00 shows a calm memory panel at 09:00. It belongs
  // here, as an event that occurred, and not on an axis.
  //
  // The increase across the window, never the raw total: oom_kill_total is
  // cumulative since boot, so a host that killed one process a year ago
  // would otherwise carry a permanent badge. counterIncrease returns null
  // when no usable pair exists, which is "we cannot say" and stays silent
  // -- distinct from 0, which is a host confirming nothing happened.
  const oomKills = counterIncrease(
    griddedValues(input.hostMetrics ?? null, 0, "oom_kill_total"),
  );
  if (oomKills !== null && oomKills > 0) {
    out.push({
      severity: "critical",
      what: `${oomKills} OOM ${oomKills === 1 ? "kill" : "kills"} in this window — the kernel killed processes to reclaim memory`,
    });
  }

  // The increase across the window, for the same reason as the OOM block
  // above: post_failures_total is cumulative for the whole life of the agent
  // PROCESS and is deliberately never reset by a success (see the comment on
  // postFailures in internal/agent/client/client.go), and the agent re-sends
  // it on every scrape. Read with latest() it was the permanent badge the OOM
  // comment warns about -- one hub restart pinned "1 failed deliveries" to
  // the page forever, even though the ring buffer replayed those samples the
  // moment the hub came back and nothing was actually lost.
  //
  // counterDeltas drops a negative step, so the counter going back to zero on
  // an agent restart is skipped rather than counted as a huge recovery.
  const failures = counterIncrease(
    griddedValues(input.agentMetrics, 0, "post_failures_total"),
  );
  if (failures !== null && failures > 0) {
    out.push({
      severity: "warning",
      what: `${failures} failed ${failures === 1 ? "delivery" : "deliveries"} to the hub in this window`,
    });
  }

  // `critical`, not `serious`, and that is the fleet page's word for this
  // exact fact: hostConditions() in fleet/conditions.ts has always rated a
  // host that stopped reporting `critical`. The two pages used to print
  // different severities for one condition, so the same host read "serious"
  // here and "critical" one click up -- the kind of disagreement the shared
  // disk thresholds below exist to prevent, in the one place a constant
  // could not fix it. `serious` remains a Badge severity; nothing else that
  // uses it changed.
  if (input.host.last_seen === null) {
    out.push({ severity: "critical", what: "never reported" });
  } else {
    const age = now.getTime() - new Date(input.host.last_seen).getTime();
    if (age > STALE_THRESHOLD_MS) {
      out.push({
        severity: "critical",
        what: `last reported ${relative(input.host.last_seen, now)}`,
      });
    }
  }

  for (const fs of input.filesystems) {
    // df's Use% and the bytes behind it, judged by the same rule the fleet
    // page uses -- see diskState in fleet/conditions.ts. total is not the
    // denominator -- see filesystemRows above. Used only to decide whether to
    // warn; the card itself still shows bytes.
    const disk = diskState(fs.used, fs.free);
    if (disk === null || disk.severity === null) continue;
    out.push({
      severity: disk.severity,
      what: `${fs.label} is ${percent(disk.pct)} full — ${bytes(fs.free)} free`,
    });
  }

  for (const unit of input.units ?? []) {
    if (unit.state === "failed") {
      out.push({ severity: "warning", what: `${unit.unit_name} failed` });
    } else if (flapping(unit)) {
      out.push({
        severity: "warning",
        what: `${unit.unit_name} restarted ${unit.restarts_1h} times in the last hour`,
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
const TRAFFIC_SPEC = requireSpec("host-traffic");

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
  agentMetrics: MetricsResponse | null;
  /** family=net for this host, one series per interface. */
  netMetrics?: MetricsResponse | null;
  units: Unit[] | null;
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
  agentMetrics,
  units,
  range,
  fetchFamily,
  onOpenChart,
  now,
}: OverviewProps) {
  const filesystems = filesystemRows(filesystemMetrics);
  const attention = needsAttention({
    host,
    agentMetrics,
    hostMetrics,
    filesystems,
    units,
    now,
  });

  const tiles = overviewTiles({
    host,
    hostMetrics,
    filesystemMetrics,
    netMetrics,
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
      {attention.length > 0 && (
        <section className="attn" aria-label="Needs attention">
          {/* The severity is a heading over the rows at that severity, said
              once, rather than a chip repeated on every one of them. Three
              critical rows do not need the word "critical" three times, and
              the chip that used to carry it printed the raw severity literal
              in lowercase. The dot on each row is the mark; the heading above
              it is the word §3.3 requires, and it is a real heading so a
              screen reader reaches the rows through it.

              The fleet list groups the same way, and this page has to match
              it -- see #92, where the two disagreed about one host. */}
          {ATTENTION_SEVERITIES.map((severity) => {
            // Stable partition, so the written order of needsAttention()
            // survives inside each severity: reporting still leads, which is
            // the whole reason it is written first.
            const rows = attention.filter((a) => a.severity === severity);
            if (rows.length === 0) return null;
            return (
              <div key={severity}>
                <h3 className={`attn-sev ${SEVERITY_CLASS[severity]}`}>
                  {SEVERITY_WORD[severity]}{" "}
                  <span className="n">{rows.length}</span>
                </h3>
                <ul className="attn-list">
                  {rows.map((a, index) => (
                    <li className="attn-row" key={index}>
                      <span
                        className={`dot ${SEVERITY_CLASS[severity]}`}
                        aria-hidden="true"
                      />
                      <span className="what">{a.what}</span>
                    </li>
                  ))}
                </ul>
              </div>
            );
          })}
        </section>
      )}
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
          load": those are the titles the Network and System tabs draw these
          same two specs under, and one chart with two names is how a reader
          comes to think they are looking at two things. */}
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
              <p className="note">No filesystem samples in this window.</p>
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
