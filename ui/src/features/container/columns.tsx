// The ONE container row definition. Both surfaces that list containers render
// exactly these columns:
//
//   - the fleet-wide Containers tab (features/fleet/FleetContainers.tsx),
//     which groups by host, and
//   - a host's own Containers tab (features/host/tabs/Inventory.tsx), which
//     groups by compose project.
//
// Spec 4.5 calls the fleet view "the same container row plus a Host column",
// so "every postgres in the fleet" and "everything on this host" have to be
// one definition. They were two: this module is the fleet's column set and
// the host tab's CONTAINER_COLUMNS merged back together, after the host tab
// had drifted into a different column list, a different agent badge, no
// sorting and -- worst -- no link to the container detail page at all.
//
// WHAT IS NOT A COLUMN, and why. This note used to say there was "deliberately
// no health, restart-count or uptime column: none of those reach the wire or
// the schema (container_samples carries CPU and memory only)". Both halves were
// false: migration 0012 put docker_state, health, state_ts, restart_count and
// labels on every listing row, and container_samples has carried net_rx/net_tx,
// io_read/io_write and the cpu and memory splits since 0001_init.sql. The
// comment survived long enough to persuade readers the data did not exist,
// which is the most expensive thing a comment can do.
//
//   - HEALTH is the Status column's word already. `unhealthy` reaches it
//     through deriveState; a second column reading "healthy" on four hundred
//     rows and "none" on most of the rest is an inventory of HEALTHCHECK
//     adoption, not a reading. `starting` is the one health value Status could
//     not carry, and now it can -- see STARTING_STUCK_S in state.ts, which
//     needs the start time this row finally has.
//
//   - RESTARTS are a MARK beside the name, drawn only above zero, so a column
//     of blanks with a heading over it never happens. The figure is
//     restarts_window -- summed from the restart event log over 24h (migration
//     0019) -- falling back to Docker's cumulative counter, which resets on
//     recreate and is why a column headed "Restarts" would read as "recently"
//     and be wrong for a two-year-old container.
//
//   - UPTIME is in the derivation rather than on the row: a column would be
//     blank on every host whose socket refuses inspect, and "up 41 d" on a
//     healthy container is not a thing anyone scans a column for. It appears
//     as a mark only while it is SHORT, where "this came up just now" is the
//     fact worth seeing beside a container that is misbehaving.
//
//   - NET RX/TX and DISK I/O have no denominator. A rate has no ceiling to
//     fill a bar against, so it cannot join the two saturation columns below,
//     and that is the same reason the fleet host row draws traffic as a pair
//     of rates rather than a metric-cell. They are one array literal away in
//     hostTrends.ts and the container detail page already charts all four.
//
// The Status column is not that column. It says nothing Docker told us -- it
// reports what netra MEASURED: samples arriving, samples stopped, memory near
// its limit, a hole in the series. It calls deriveState (features/container/
// state.ts), the same function the detail page's header badge calls, so a
// container reads the same on the list it sits in and on the page that list
// links to. The old note here said the badge could not come to the lists
// because it needs sample timestamps the fleet fan-out lacks; it does not
// lack them. containers.last_seen rides on every listing row -- it advances
// only while samples arrive -- and both lists already build the cpu/mem
// series a ContainerRow carries.
//
// last_seen is also what containerIsGone below is derived from. That is a
// FACT measured against the host and it is what the purge action is offered
// on -- but it is no longer a pill of its own beside the Status column. It
// feeds deriveState, which says "Gone" once, in the words the rest of the
// column uses.
import type { ReactNode } from "react";
import { Badge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { Reading } from "../../ui/Reading";
import { When } from "../../ui/When";
import { Meter, severityFromPercent, trendColor } from "../../ui/Meter";
import { NowReading } from "../../ui/NowReading";
import { METRIC_CELL_STYLE } from "../../ui/charts/size";
import type { Column } from "../../ui/Table";
import {
  ABSENT,
  absolute,
  binaryBytes,
  bytes,
  percent,
} from "../../lib/format";
import type { Container } from "../../lib/api";
import { rangeMs, type Range } from "../../lib/range";
import { ContainerChart } from "./ContainerChart";
import { hasInteriorGaps } from "../../lib/metrics";
import { hostStatus } from "../../lib/host";
import { containerSamplesBlocked } from "../../lib/containers";
import {
  deriveState,
  stateKindRank,
  STARTING_STUCK_S,
  UPTIME_MARK_S,
  uptimeSeconds,
  type DerivedState,
} from "./state";

/**
 * A container as a list sees it: what `GET /api/v1/hosts/{id}/containers`
 * returns plus the host it came from. The host is carried on the row rather
 * than looked up while rendering -- there is no fleet-wide container
 * endpoint (only the per-host one), so whoever fanned the calls out already
 * knows which host each response belongs to and is the only party that
 * cannot get it wrong.
 *
 * host_id is not decoration: the link to container detail is built from it,
 * which is why a host page holding only `Container[]` could not offer one.
 */
export type ContainerRow = Container & {
  host_id: number;
  hostname: string;
  /** The window the hub answered for this row's host, for the enlarged
   * view's time axis. Absent, the dialog draws no time axis rather than
   * inventing one. */
  window?: { from: string; to: string } | null;
  /** CPU percent and memory bytes over the window, when they have been
   * fetched. Absent (not empty) when nobody asked for them: an empty series
   * draws a gap, which is the truth for a container that reported nothing,
   * and the wrong thing to say about a list that never requested metrics. */
  cpu?: (number | null)[];
  mem?: (number | null)[];
  /** The container's own memory ceiling, or null when it runs unlimited.
   * One name for one quantity: the host tab used to call this `memLimit`. */
  mem_limit_bytes?: number | null;
  /**
   * When the HOST this container runs on last reported anything.
   *
   * Carried on the row because "gone" is measured against it -- see
   * containerIsGone. Both lists already hold it: the host page has the host
   * it is about, and the fleet fan-out has the host each listing came from.
   */
  host_last_seen?: string | null;
  /**
   * The host's `containers` capability, when the agent reported one.
   *
   * Also for containerIsGone: `no-cgroup-scopes` means no container sample
   * can land on that host at all, while its host samples keep arriving. See
   * lib/containers.ts.
   */
  host_containers_capability?: string | undefined;
  /**
   * The host's logical CPUs, for the CPU cell's denominator.
   *
   * `threads`, not `cores`: `cores` exists only on HostDetail, which the fleet
   * never fetches, and the fleet host row already prints "of N cores" from
   * threads. One wording across both lists, whatever the field is called.
   *
   * Carried on the row for the same reason hostname and host_last_seen are:
   * the party that fanned out the per-host calls is the only one that cannot
   * attribute a container to the wrong machine.
   */
  host_threads?: number | null;
  /**
   * The host's RAM, for the memory bar on a container with no limit.
   *
   * `Host.mem_total` (the fleet list's gauge) and `HostDetail.memory_total`
   * (the host page's inventory) are the same box's memory under two names; the
   * row calls it one thing.
   */
  host_mem_total?: number | null;
};

/**
 * A list row's state, in the detail page's own words.
 *
 * Every input deriveState wants is already on the row: `last_seen` advances
 * only while samples arrive, `host_last_seen` is what containerIsGone
 * measures against, and both lists build the cpu/mem series a ContainerRow
 * carries. A row without trends simply cannot reach the two states that need
 * them -- it says Reporting, Silent or Host offline, which is what it knows.
 */
export function containerState(
  row: ContainerRow,
  now: Date = new Date(),
  range?: Range,
): DerivedState {
  const lastSeen = Date.parse(row.last_seen);
  // A host whose cgroup mount is unreadable keeps posting host samples while
  // no container sample can land, so last_seen ages forever and every
  // container on it would read Silent -- beside a hostContainerNote saying
  // the opposite, and with containerIsGone suppressed for this same reason.
  // Nothing was reported, which is what the row then says.
  const blocked = containerSamplesBlocked(row.host_containers_capability);
  return deriveState({
    lastSampleMs: blocked || Number.isNaN(lastSeen) ? null : lastSeen,
    // lastReported, not the latest bucket: the last READING, the way the
    // detail page reads it and the way mem_limit_bytes is already built. Off
    // the latest bucket, one empty trailing bucket hid memory pressure here
    // while the page one click away still reported it.
    memUsed: lastReported(row.mem),
    memLimit: row.mem_limit_bytes ?? null,
    // Interior gaps only. Every container younger than the fleet's 24h grid
    // carries leading nulls, and calling those a gap warns on a large share
    // of a healthy fleet for having been created yesterday.
    gap: row.cpu ? hasInteriorGaps(row.cpu) : false,
    now,
    hostState:
      row.host_last_seen === undefined
        ? undefined
        : hostStatus({ last_seen: row.host_last_seen }, now),
    gone: containerIsGone(row),
    // Docker's own answers ride on the listing row, so the lists reach
    // "Unhealthy" without a per-surface rule -- the same reason this function
    // exists at all.
    dockerState: row.docker_state,
    health: row.health,
    // The restart log's own count, which a list CAN now answer -- it rides
    // the row from the events table (migration 0019) rather than being
    // differenced out of a series the fleet's tier does not carry. So "a hole
    // in the series usually means a restart, but no restart count is
    // available for this range" is no longer the best netra can say about a
    // gap; deriveState already writes the two better sentences.
    //
    // ONLY when the count's window is the range the gap was found in. The
    // sentences deriveState builds from this say "in this window", and the
    // window they mean is the CHART's -- so handing them a fixed 24h count
    // for a 30d list makes "the container did not restart" a claim about
    // thirty days that was measured over one, and a 1h list would blame a
    // one-hour hole on a restart twenty hours before it. Mismatched, the row
    // says the honest thing instead: no count is available for this range.
    // The detail page has no such problem -- it counts over the samples it
    // actually drew -- and this is what keeps the two surfaces from stating
    // different things about one gap.
    //
    // Undefined -- an older payload, or a caller building rows by hand --
    // still means null, and the wording falls back exactly as it did.
    restartsInWindow: restartsForRange(row, range),
    // What separates a container legitimately booting from one wedged in its
    // healthcheck. Null on any host whose agent cannot inspect, where the
    // branch correctly does not fire.
    startedAtMs: row.started_at ? Date.parse(row.started_at) : null,
  });
}

/**
 * The row's restart count, but only when it answers for the range asked about.
 *
 * The hub sums the restart log over a window of its own choosing and says
 * which on the row (`restarts_window_seconds`, 24h today). A list drawing a
 * 30-day chart and a list drawing a one-hour chart both get that same figure,
 * and only one of them can honestly call it "this window".
 *
 * Equal, not "close enough": these are the discrete ranges of RAIL_RANGES, so
 * there is no near-miss to accommodate, and a rule that accepted one would
 * have to decide how wrong is acceptable. Null when they differ, which is the
 * case deriveState already has words for.
 *
 * No range at all -- a caller with no chart behind it -- is the same answer
 * for the same reason: nothing here says what window the gap was found in.
 */
function restartsForRange(row: ContainerRow, range?: Range): number | null {
  if (range === undefined) return null;
  const windowSeconds = row.restarts_window_seconds;
  if (windowSeconds == null || windowSeconds <= 0) return null;
  if (rangeMs(range) !== windowSeconds * 1000) return null;
  return row.restarts_window ?? null;
}

/**
 * Can a container sample land on this host at all?
 *
 * Read by containerIsGone and by containerState, because both would
 * otherwise blame a container for a silence that belongs to its host's
 * agent -- one by offering to purge it, the other by calling it Silent.
 *
 * Re-exported from lib/containers rather than answered from a set kept here.
 * This module used to hold its own `NO_CONTAINER_SAMPLES = {"no-cgroup-scopes"}`
 * beside lib's own list of blocking values, and the two drifted the moment a
 * third value was added: `docker-socket-silent` reached the panels, which went
 * quiet, and missed these lists, which kept ageing every row past
 * GONE_AFTER_S and offering a Purge for a container that was still running.
 * One definition, or the next value does it again.
 */
export { containerSamplesBlocked };

/**
 * How far a container's last sample may lag its HOST's before the row reads
 * as gone.
 *
 * Fifteen minutes, not the detail page's three (SILENT_AFTER_S): that badge
 * watches a live sample stream on one container, while this is an inventory
 * listing refetched on its own schedule, and a threshold near the scrape
 * interval makes rows flicker in and out of "gone" at every refetch boundary.
 */
export const GONE_AFTER_S = 15 * 60;

/**
 * Has this container stopped being reported while its host kept reporting?
 *
 * MEASURED AGAINST THE HOST, never against the wall clock, and that is the
 * whole design. A container's last_seen only advances while samples arrive,
 * so a host that is offline -- or rebooting, or on the far side of a network
 * problem -- drags every container on it into the past at once. Against
 * now(), all of them would read "gone" and each would be offered a purge
 * button that deletes a live container's history. Against the host's own
 * last report, an offline host marks nothing gone: the whole machine went
 * quiet, which the host page says for itself.
 *
 * A host that has never reported, or a row whose timestamps do not parse,
 * reports false: there is nothing to measure, and the wrong direction to
 * fail in is the one that offers to delete something.
 */
export function containerIsGone(
  row: ContainerRow,
  goneAfterS: number = GONE_AFTER_S,
): boolean {
  // A host whose cgroup mount is gone -- or whose Docker socket has stopped
  // naming anything -- keeps reporting host samples while no container sample
  // can land, so every container on it would age past the window together, and
  // be offered a purge that deletes a still-running container's history. The
  // lists already say what is wrong there (hostContainerNote); this must not
  // contradict them with a Gone badge.
  if (containerSamplesBlocked(row.host_containers_capability)) {
    return false;
  }
  if (row.host_last_seen === undefined || row.host_last_seen === null) {
    return false;
  }
  const host = Date.parse(row.host_last_seen);
  const seen = Date.parse(row.last_seen);
  if (Number.isNaN(host) || Number.isNaN(seen)) return false;
  return host - seen > goneAfterS * 1000;
}

/**
 * The compose identity behind a container_key.
 *
 * container_key is the compose identity, "project/service" -- the agent
 * refuses to send the Docker id for it (see
 * internal/agent/collector/containers_test.go), because that id changes on
 * every `compose up -d` and keying history on it would orphan every series
 * the container has. A key with no slash is a container the agent could not
 * read compose labels for, so it has a service and no project.
 */
export function composeIdentity(key: string): {
  project: string;
  service: string;
} {
  const slash = key.indexOf("/");
  if (slash === -1) return { project: ABSENT, service: key };
  return { project: key.slice(0, slash), service: key.slice(slash + 1) };
}

/**
 * The latest non-null value, or null when the series never reported.
 *
 * NOT lib/metrics.ts's latestValue(), which is the LATEST BUCKET including a
 * trailing null. The two answer different questions and only this one is
 * right for a memory reading beside a limit: a container does not stop
 * having a ceiling, or stop using memory, because the newest bucket has not
 * materialised yet.
 */
export function lastReported(
  values: readonly (number | null)[] | undefined,
): number | null {
  if (values === undefined) return null;
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i];
    if (v !== null && v !== undefined) return v;
  }
  return null;
}

/**
 * The shared ceilings for a list's trend columns.
 *
 * Computed across every row rather than per row, because a column of
 * independently scaled sparklines compares nothing: each one fills its own
 * box, so the busiest container and the idlest draw the same picture. CPU is
 * a percentage that can exceed 100 (a container using two cores reports
 * 200), so the ceiling is the list's own peak rather than a fixed 100.
 */
export function trendScales(rows: readonly ContainerRow[]): {
  cpuMax: number;
  memMax: number;
} {
  let cpuMax = 0;
  let memMax = 0;
  for (const row of rows) {
    for (const v of row.cpu ?? []) if (v !== null && v > cpuMax) cpuMax = v;
    for (const v of row.mem ?? []) if (v !== null && v > memMax) memMax = v;
  }
  // A zero ceiling would divide by zero in the geometry; 1 draws a flat line
  // on the floor, which is what "nothing happened" looks like.
  return { cpuMax: cpuMax || 1, memMax: memMax || 1 };
}

/**
 * What a GROUP of containers is using, right now.
 *
 * Both lists group -- a host page by compose project, the fleet by host --
 * and both collapse those groups by default, so a group header has to answer
 * what its rows would have answered. One definition for both, for the same
 * reason the column set is one definition: two lists summing the same
 * quantity two ways is the drift this module exists to prevent.
 *
 * The latest REPORTED reading per container (lastReported, not the last
 * bucket), summed. A container whose newest bucket has not materialised has
 * not stopped using memory, and dropping it from the total would make the
 * group's figure dip every time the grid ticks over.
 *
 * The DENOMINATOR is the host's, never a sum of the containers' own limits.
 * A group is a share of one machine: "this stack is holding 64 % of the box"
 * is a fact an operator acts on, while "64 % of the limits its containers
 * happen to declare" is a number about a configuration. Summing limits also
 * could not be done honestly -- a group where three of four are capped has no
 * ceiling the fourth can be counted against -- which is why the old shape
 * returned null for the whole group in that very common case, and drew no bar
 * at all.
 *
 * That is also what makes the group key (host, project) rather than project:
 * a stack spread over three machines has no cores and no RAM to be a share OF,
 * and its heading would fall back to the bare sums this replaced.
 */
export function containerGroupReading(rows: readonly ContainerRow[]): {
  cpuPct: number | null;
  memPct: number | null;
  memBytes: number | null;
  threads: number | null;
  memTotal: number | null;
} {
  let cpu: number | null = null;
  let mem: number | null = null;
  for (const row of rows) {
    const c = lastReported(row.cpu);
    if (c !== null) cpu = (cpu ?? 0) + c;
    const m = lastReported(row.mem);
    if (m !== null) mem = (mem ?? 0) + m;
  }
  // Every row in a group shares a host by construction, so the first one's
  // denominators are the group's.
  const first = rows[0];
  const threads =
    first?.host_threads != null && first.host_threads > 0
      ? first.host_threads
      : null;
  const memTotal =
    first?.host_mem_total != null && first.host_mem_total > 0
      ? first.host_mem_total
      : null;

  return {
    cpuPct: cpu !== null && threads !== null ? cpu / threads : null,
    memPct: mem !== null && memTotal !== null ? (mem / memTotal) * 100 : null,
    memBytes: mem,
    threads,
    memTotal,
  };
}

/**
 * The worst thing wrong inside a group, and how many rows carry it.
 *
 * What lets a folded group be honest: a heading that says "nothing here needs
 * you" has to be able to say the opposite. `reporting` and `no-samples` are
 * not "wrong" -- the first is healthy and the second is nobody having looked
 * -- so a group of those returns null and is safe to arrive folded.
 */
export function containerGroupWorst(
  rows: readonly ContainerRow[],
  now: Date = new Date(),
): { state: DerivedState; count: number } | null {
  let worst: DerivedState | null = null;
  for (const row of rows) {
    const state = containerState(row, now);
    if (state.kind === "reporting" || state.kind === "no-samples") continue;
    if (worst === null || stateKindRank(state.kind) < stateKindRank(worst.kind))
      worst = state;
  }
  if (worst === null) return null;
  const kind = worst.kind;
  const count = rows.filter(
    (row) => containerState(row, now).kind === kind,
  ).length;
  return { state: worst, count };
}

/**
 * The group total as a group header draws it: CPU, then memory.
 *
 * CPU is a percentage of ONE core, which is what the per-container column
 * already shows, so a stack on two busy cores reads 200% here. Not divided by
 * the host's core count: this component is rendered by the fleet list too,
 * where the rows in one group come from one host and the rows in the next
 * come from another, and a number that means "of this host's cores" would
 * mean something different in every group of the same column.
 *
 * A group that has reported nothing renders the absent marker rather than a
 * zero -- the same distinction every cell in this module keeps.
 */
export function containerGroupCells(
  rows: readonly ContainerRow[],
): Record<string, ReactNode> {
  const { cpuPct, memPct, threads, memTotal } = containerGroupReading(rows);

  // The same block a row draws, minus the silhouette: there is no series to
  // sum, and a heading is a reading of now rather than a history. Everything
  // else is identical -- the same ten cells, the same 70/95, the same
  // denominator -- which is the entire point of putting it in the column.
  return {
    cpu:
      cpuPct === null ? null : (
        <div className="metric-cell" style={METRIC_CELL_STYLE}>
          <NowReading
            pct={cpuPct}
            label="CPU for this group"
            under={`of ${threads} core${threads === 1 ? "" : "s"}`}
          />
        </div>
      ),
    memory:
      memPct === null || memTotal === null ? null : (
        <div className="metric-cell" style={METRIC_CELL_STYLE}>
          <NowReading
            pct={memPct}
            label="Memory for this group"
            // The bare form, with no ` host` suffix: a heading is always a
            // share of one machine, so there is nothing to tell it apart
            // from. A row says "of 62.6 GiB host" only because the row beside
            // it might be saying "of 4.0 GB" about a limit.
            under={`of ${binaryBytes(memTotal)}`}
          />
        </div>
      ),
  };
}

/**
 * The group's memory in bytes, for its heading: " · 7.9 GB" after the
 * container count. A percentage says how full the machine is and says
 * nothing about how big the stack is; on a page listing several hosts those
 * are different questions.
 *
 * In the heading's label rather than in a cell of its own. It sat in the
 * Image column while there was one; the image is a line under the name now,
 * and a figure alone in the heading of an empty column read as a stray. Both
 * lists' headings render this, so the bytes appear the same way on each.
 * Nothing when the group has reported nothing -- the same distinction every
 * cell in this module keeps.
 */
export function GroupBytes({ rows }: { rows: readonly ContainerRow[] }) {
  const { memBytes } = containerGroupReading(rows);
  return memBytes === null ? null : (
    <span className="groupcount">
      {" · "}
      {bytes(memBytes)}
    </span>
  );
}

function NameCell({
  row,
  state,
  now,
}: {
  row: ContainerRow;
  /** The row's derived state, passed in rather than re-derived: the restart
   * mark takes its severity from it, and two calls to containerState against
   * two different clocks could disagree inside one row. */
  state: DerivedState;
  /** The same clock the state was derived against, for the uptime mark. Two
   * clocks in one cell is the bug this parameter exists to prevent: the
   * badge could read `starting` off one instant while the mark measured the
   * age against another. */
  now: Date;
}) {
  // No identity line under the name. There was one -- "project / service"
  // in the fleet, the bare service on the host tab -- and in both lists the
  // project is already the heading the row sits under, so the line spent the
  // widest column in the table repeating it in a smaller face. The service
  // alone was little better: "redis" under "authelia-redis" is the name
  // again. The full key is on the detail page the name links to.
  return (
    <div className="host-cell">
      <div className="host-cell-top">
        {/* An anchor, not a row click handler: middle-click, copy-link and
            bookmark all have to work. container_key is "project/service", and
            router.ts splits the path on "/" BEFORE decoding it, so the key
            must stay percent-encoded or the route falls through to notFound. */}
        <a
          className="host-cell-name"
          href={`/containers/${row.host_id}/${encodeURIComponent(row.container_key)}`}
        >
          {/* The key, not ABSENT, when there is no name: the identity line
              that used to print it is gone, and a row reading "--" with
              nothing under it is a container with no way to tell it from the
              next unnamed one. Absence of a name is not absence of a
              container. */}
          {row.name ?? row.container_key}
        </a>
        {/* netra's own agent runs as a container on most hosts; unlabelled it
            reads as a workload someone deployed. Neutral, never severity="ok":
            "agent" is an identity, not a health state, and green would assert
            a state netra does not collect. */}
        {row.is_agent ? <Badge label>agent</Badge> : null}
        {/* Docker's `starting` health, which reaches nothing else. Status
            carries `unhealthy` through deriveState, and a container wedged
            in its healthcheck long enough becomes a state of its own -- but
            under that threshold, and on any host whose agent cannot report a
            start time to measure the threshold against, this badge is the
            only thing that says a container is not ready yet. Warning rather
            than neutral: a container that is starting is a container not
            serving, which is worth a mark even when it is legitimate. */}
        {row.health === "starting" && state.kind !== "starting" ? (
          <Badge severity="warning">starting</Badge>
        ) : null}
        <RestartMark row={row} state={state} />
        <UptimeMark row={row} now={now} />
        {/* No "gone" pill here any more. It stood beside a Status column that
            said "Silent" about the same container in the same instant, and
            the two were not two opinions: gone measures last_seen against the
            host's last report and silent measures it against the clock, so
            every gone row was also a silent one. Gone is a state now
            (state.ts) and the Status column says it, once. containerIsGone
            still gates the purge column -- same predicate, one voice. */}
      </div>
      {/* The image, under the name -- the same line the fleet prints a host's
          location on, in the same class, so the two lists set their second
          line at one size and one distance. It was a column of its own, and
          the widest string in the row ("ghcr.io/immich-app/immich-server:
          v1.119.1") owned a column read only when something is wrong with a
          version; under the name it costs no height, because the sparkline
          sets the row's, and the columns that carry a reading get the width.
          Sans, not the identifier face: it is read as a name, not diffed
          character by character. No tag on the wire is not `:latest` --
          inventing one would name a version the agent never reported. And no
          line at all when there is no image, rather than a dash under every
          unnamed row. */}
      {row.image === null ? null : (
        <div className="host-cell-site">{row.image}</div>
      )}
    </div>
  );
}

/**
 * What a container's memory is measured AGAINST, and what to call it.
 *
 * Two denominators, and they are two different claims, so one function answers
 * both and every caller renders the same words for the same case.
 *
 *   - A real mem_limit is "how close is this to being OOM-killed". <= 0, not
 *     just null: Docker writes 0 for "no limit" and the rest of this codebase
 *     already reads it that way (containerSeverity, ContainerPage's
 *     memLimit > 0). Taken as a ceiling it drew a meter with no fill under a
 *     line reading "of 0 B" -- an unlimited container asserting a limit of
 *     nothing.
 *
 *   - The HOST's RAM is "how much of this machine is it holding", which on a
 *     fleet where almost nothing sets a limit is the only bar that can be
 *     drawn at all. Before this, those rows were a silhouette with no figure
 *     and no bar -- the same gap the CPU column had before it was given a
 *     reading.
 *
 * The ` host` suffix is what keeps the two apart on a row where either is
 * possible. A group heading prints the bare form, because a heading is always
 * a share of one machine and has nothing to be told apart from.
 *
 * Decimal bytes for a container's own limit and BINARY for host RAM, which
 * looks like an inconsistency and is not: every other memory figure in a
 * container row is decimal (the sparkline's values, the group totals) while
 * the fleet's host memory is binary because its stack is drawn against a
 * binary ceiling. Each figure keeps the units of the thing it is a share of.
 */
export function memDenominator(row: ContainerRow): {
  denom: number | null;
  under: string | null;
  isHostShare: boolean;
} {
  const limit =
    row.mem_limit_bytes != null && row.mem_limit_bytes > 0
      ? row.mem_limit_bytes
      : null;
  if (limit !== null) {
    return { denom: limit, under: `of ${bytes(limit)}`, isHostShare: false };
  }
  const host =
    row.host_mem_total != null && row.host_mem_total > 0
      ? row.host_mem_total
      : null;
  if (host !== null) {
    return {
      denom: host,
      under: `of ${binaryBytes(host)} host`,
      isHostShare: true,
    };
  }
  return { denom: null, under: null, isHostShare: false };
}

/** The displayed name, falling back to the key every other cell falls back to. */
function nameOf(row: ContainerRow): string {
  return row.name ?? row.container_key;
}

/**
 * The memory cell: the trend, then the bar and the figure it ends on.
 *
 * The fleet host row's own composition -- metric-cell, NowReading, SegmentBar
 * -- rather than a Meter of this list's own. Those were two shapes for one
 * job, and NowReading's docstring named this list as the surface still drawing
 * the older one.
 *
 * The bar judges 70/95 like every other reading in the app, the share-of-host
 * case included: NowReading leaves severity unset everywhere, deliberately,
 * because a bar answers "what does this number say". The row's RAIL is what
 * stays limit-only (see containerSeverity) -- holding 64 % of a box you were
 * given warns in the bar without striping the row.
 */
function MemoryCell({
  row,
  memMax,
  range,
  ranges,
}: {
  row: ContainerRow;
  memMax: number;
  range: Range;
  ranges?: readonly Range[];
}) {
  // Wrapped, for the reason When's absent branch is: Table dims a cell whose
  // own output IS the marker string, and this one's is an element, so the
  // dimming cannot reach it from there.
  if (row.mem === undefined || row.mem.length === 0)
    return <span className="absent">{ABSENT}</span>;

  const { denom, under } = memDenominator(row);
  const used = lastReported(row.mem);
  const pct = denom !== null && used !== null ? (used / denom) * 100 : null;
  const ownLimit =
    row.mem_limit_bytes != null && row.mem_limit_bytes > 0
      ? row.mem_limit_bytes
      : null;

  return (
    <div className="metric-cell" style={METRIC_CELL_STYLE}>
      {/* The chart is the button that enlarges it -- only the chart, not the
          bar beside it: the bar answers "how close to being killed" at a
          glance and has nothing bigger to show. */}
      <ContainerChart
        row={row}
        metric="mem"
        values={row.mem}
        window={row.window ?? null}
        // Against its OWN limit when it has one -- that is what "how close to
        // being killed" means -- and against the list's largest container when
        // it does not, so the unlimited ones stay comparable with each other.
        // NOT the host total: scaling every unlimited container against the
        // machine would flatten all of them into the floor of the chart.
        max={ownLimit ?? memMax}
        range={range}
        ranges={ranges}
        color={trendColor(pct)}
      />
      {pct !== null && under !== null && (
        <NowReading
          pct={pct}
          label={`Memory now, ${nameOf(row)}`}
          under={under}
        />
      )}
    </div>
  );
}

/**
 * The CPU cell: the trend, then how much of the HOST it is using.
 *
 * cpu_pct is percent of ONE core, so a container at 150 % means nothing until
 * it is set against the machine: 150 % of a 4-thread VPS is most of it and of
 * a 32-thread box it is noise. Dividing by threads is what makes the column
 * comparable across a mixed fleet, and it is exactly what the fleet host row
 * does with cpu_total.
 *
 * With NO denominator the figure still prints but the bar does not. A
 * SegmentBar clamps at ten lit cells, so it would report a container at 300 %
 * as merely saturated -- an assertion nobody measured -- while the raw
 * percentage remains a true thing to say.
 */
function CpuCell({
  row,
  cpuMax,
  range,
  ranges,
}: {
  row: ContainerRow;
  cpuMax: number;
  range: Range;
  ranges?: readonly Range[];
}) {
  if (row.cpu === undefined || row.cpu.length === 0)
    return <span className="absent">{ABSENT}</span>;

  const busy = lastReported(row.cpu);
  const threads =
    row.host_threads != null && row.host_threads > 0 ? row.host_threads : null;
  const pct = busy !== null && threads !== null ? busy / threads : null;

  return (
    <div className="metric-cell" style={METRIC_CELL_STYLE}>
      <ContainerChart
        row={row}
        metric="cpu"
        values={row.cpu}
        window={row.window ?? null}
        max={cpuMax}
        range={range}
        ranges={ranges}
        color={trendColor(pct)}
      />
      {pct !== null ? (
        <NowReading
          pct={pct}
          label={`CPU now, ${nameOf(row)}`}
          under={`of ${threads} core${threads === 1 ? "" : "s"}`}
        />
      ) : (
        busy !== null && <Reading value={String(Math.round(busy))} unit="%" />
      )}
    </div>
  );
}

/**
 * How often Docker has restarted this container, as a mark beside its name.
 *
 * Drawn only above zero. A literal 0 on four hundred healthy rows is noise
 * wearing the shape of a fact, and it is why this is a mark and not a column:
 * a column would be a stack of blanks under a heading.
 *
 * Two different numbers can reach it and the TITLE is the only place they are
 * told apart, because printing them identically is the lie. The windowed count
 * is a sum over the restart event log (migration 0019) and means "in the last
 * day". The fallback is Docker's cumulative counter, which resets when a
 * container is recreated and so says nothing about when.
 *
 * The severity echoes the row's STATE rather than inventing a restart
 * threshold: nobody can say how many restarts is too many, but a container
 * Docker reports as restarting is already critical for a reason the Status
 * column states.
 */
function RestartMark({
  row,
  state,
}: {
  row: ContainerRow;
  state: DerivedState;
}) {
  const windowed = row.restarts_window ?? null;
  const n = windowed ?? row.restart_count ?? null;
  if (n === null || n <= 0) return null;

  const plural = n === 1 ? "" : "s";
  const title =
    windowed !== null
      ? `${n} restart${plural} in the last ${restartWindowLabel(row)}`
      : `${n} restart${plural} since this container was created -- Docker's ` +
        `cumulative counter, which resets when a container is recreated`;

  return (
    <span
      className={state.kind === "restarting" ? "rst st-crit" : "rst"}
      title={title}
    >
      <span className="g" aria-hidden="true">
        &#8635;
      </span>
      {n}
    </span>
  );
}

/**
 * How long ago this container came up, as a mark beside its name -- and only
 * while that is recent.
 *
 * Nothing at all above UPTIME_MARK_S, nothing when the host's agent could not
 * report a start time, and nothing once the row stops reporting -- see
 * uptimeSeconds, which owns that last rule. Same shape as RestartMark: no
 * column, no dashes, no heading over a stack of blanks. A container that has
 * been up for six weeks says what it has to say by staying silent.
 *
 * Amber under STARTING_STUCK_S, where the container is young enough that the
 * Status column may be about to change its mind about it. Above that and
 * under the hour it is a plain annotation on the name.
 *
 * One unit, not `duration`'s two: "up 4 m 12 s" beside a name is precision
 * nobody asked for, and the mark exists to be read at a glance rather than
 * measured. The exact instant is in the title for the reader who wants it.
 */
function UptimeMark({ row, now }: { row: ContainerRow; now: Date }) {
  const age = uptimeSeconds({
    startedAt: row.started_at,
    lastSeen: row.last_seen,
    now,
  });
  if (age === null || age >= UPTIME_MARK_S) return null;

  return (
    <span
      className={age < STARTING_STUCK_S ? "upmark fresh" : "upmark"}
      title={`Started ${absolute(row.started_at)}`}
    >
      up {coarseAge(age)}
    </span>
  );
}

/**
 * The largest whole unit of `seconds`, and nothing below it.
 *
 * The hours branch is unreachable while UPTIME_MARK_S is an hour -- the caller
 * has already returned null by then. It stays because it is what makes this
 * function correct for whatever the threshold becomes: raising UPTIME_MARK_S
 * without it would print "up 240 m", and a threshold is a number someone will
 * change without reading the formatter below it.
 */
function coarseAge(seconds: number): string {
  if (seconds >= 3600) return `${Math.floor(seconds / 3600)} h`;
  if (seconds >= 60) return `${Math.floor(seconds / 60)} m`;
  return `${seconds} s`;
}

/** The window the restart count was taken over, in words. */
function restartWindowLabel(row: ContainerRow): string {
  const secs = row.restarts_window_seconds;
  if (secs == null || secs <= 0) return "24 h";
  const hours = Math.round(secs / 3600);
  return hours === 24 ? "24 h" : `${hours} h`;
}

export interface ContainerColumnsOptions {
  /**
   * Adds the Host column.
   *
   * Set by the fleet list, which does group by host and so does carry the
   * hostname in its group header -- but that header scrolls away once a
   * collapsible group is opened, and a row thirteen deep then says nothing
   * about the machine it runs on. See FleetContainers' own note.
   *
   * The host page's tab leaves it off: every row there is on the one host the
   * page is about.
   */
  showHost?: boolean;
  /** Only for the charts' accessible names -- this file never resolves a
   * range into a query. */
  range?: Range;
  /**
   * Shared ceilings for the sparklines, computed across the whole list by
   * `trendScales` above. Per-row auto-scaling would draw an idle container
   * and a saturated one with the identical silhouette -- the same reading
   * the host list's CPU column carries a fixed ceiling to avoid -- and a
   * list exists to be compared down its columns.
   *
   * Both absent means nobody fetched metrics, and the trend columns do not
   * appear at all: a list that did not ask for them renders as it always did
   * rather than growing two columns of permanent gaps.
   */
  cpuMax?: number;
  memMax?: number;
  /**
   * The ranges the PAGE behind this list offers, for the chart a reader
   * enlarges out of a trend cell. The two lists sit on pages with different
   * sets -- the fleet stops at 24h, a host page goes to 7d -- so the dialog
   * must not be able to ask for a window its own page could not express.
   */
  ranges?: readonly Range[];
  /**
   * Adds the purge action, on gone rows only.
   *
   * The FLEET list deliberately leaves this off. It is a fan-out over every
   * host in the estate, several hundred rows deep, and a per-row button that
   * deletes a container's history is at its most dangerous in exactly that
   * list -- the rows are far from the host they belong to and a mis-click has
   * nothing to undo it. Purging is done where the container belongs: its
   * host's own tab, or its detail page.
   *
   * Called with the row; the caller owns the confirmation and the refetch.
   */
  onPurge?: (row: ContainerRow) => void;
  /** Which row is one click from being purged, by container id. The
   * two-step confirm is the app's existing pattern -- see the host admin
   * table's Delete / Confirm delete pair. */
  purgeConfirming?: number | null;
  /** Set while a purge request for that row is in flight. */
  purgeBusy?: number | null;
  /** Injectable so the Status column is deterministic in tests -- it is the
   * one column whose value moves on its own. */
  now?: Date;
}

export function containerColumns({
  showHost = false,
  cpuMax,
  memMax,
  range = "24h",
  ranges,
  onPurge,
  purgeConfirming = null,
  purgeBusy = null,
  now = new Date(),
}: ContainerColumnsOptions = {}): Column<ContainerRow>[] {
  const columns: Column<ContainerRow>[] = [
    {
      key: "container",
      header: "Container",
      cell: (row) => (
        <NameCell row={row} state={containerState(row, now, range)} now={now} />
      ),
      // The displayed name, falling back to the key the cell falls back to,
      // so the order matches what a reader sees rather than an id behind it.
      sortValue: (row) => row.name ?? row.container_key,
    },
  ];
  if (showHost) {
    columns.push({
      key: "host",
      header: "Host",
      cell: (row) => (
        <a className="hostname" href={`/hosts/${row.host_id}/overview`}>
          {row.hostname}
        </a>
      ),
      sortValue: (row) => row.hostname,
    });
  }
  columns.push({
    key: "status",
    header: "Status",
    // deriveState's own words, not a second set: this is the same function
    // the detail page's header badge calls, so a container reads the same on
    // the list it is in and on the page it links to.
    // The `why` rides on a wrapper rather than on Badge: Badge takes no
    // title, and giving every badge in the app one to serve this cell is a
    // wider change than the cell is worth.
    cell: (row) => {
      const state = containerState(row, now, range);
      return (
        <span title={state.why}>
          <Badge severity={state.severity}>{state.label}</Badge>
        </span>
      );
    },
    // Worst first, by kind rather than by the label's alphabet: sorting a
    // status column is a reader asking which rows to look at.
    sortValue: (row) => stateKindRank(containerState(row, now, range).kind),
  });
  // The trend columns appear only when someone fetched the metrics.
  if (cpuMax !== undefined || memMax !== undefined) {
    columns.push({
      key: "cpu",
      header: "CPU",
      // The fleet row's own cell: the silhouette in the reading's severity
      // hue, the segmented bar, the figure, and what it is a share OF.
      cell: (row) => (
        <CpuCell row={row} cpuMax={cpuMax ?? 1} range={range} ranges={ranges} />
      ),
      // The SHARE of the host, not the raw percentage, so the column orders
      // the way the bars in it read. Sorting on cpu_pct ranked a container
      // using 90 % of one core above one using 600 % of a 32-thread box --
      // the same mistake the fleet's own memory column documents fixing, and
      // it put the wrong container at the top of "what is busiest".
      //
      // A row with no denominator sorts as unknown rather than by its raw
      // figure: it cannot be compared with the rows that have one, and Table
      // puts nulls last in both directions.
      sortValue: (row) => {
        const busy = lastReported(row.cpu);
        const threads =
          row.host_threads != null && row.host_threads > 0
            ? row.host_threads
            : null;
        return busy === null || threads === null ? null : busy / threads;
      },
    });
    columns.push({
      key: "memory",
      header: "Memory",
      cell: (row) => (
        <MemoryCell
          row={row}
          memMax={memMax ?? 1}
          range={range}
          ranges={ranges}
        />
      ),
      // Bytes, not percent of limit: sorting on the percentage would drop
      // every unlimited container into the unknown group, which on most
      // fleets is nearly all of them. The latest reported byte count is
      // always there and always means the same thing.
      sortValue: (row) => lastReported(row.mem),
    });
  }

  // When a container was last SEEN, which is a different question from what
  // the trend columns answer: they show the window a reader chose, and this
  // is the only column that keeps saying something once a container has
  // stopped reporting entirely.
  columns.push({
    key: "last_seen",
    header: "Last seen",
    cell: (row) => <When iso={row.last_seen} />,
    // The instant, not the formatted string: "6 days ago" sorts
    // alphabetically, which puts 6 days before 7 minutes.
    sortValue: (row) => Date.parse(row.last_seen),
  });

  if (onPurge !== undefined) {
    columns.push({
      key: "purge",
      header: "",
      // Only on a gone row. A running container's row has nothing to purge:
      // the next scrape would recreate it, minus its history.
      cell: (row) =>
        !containerIsGone(row) ? null : (
          <Button
            small
            variant={purgeConfirming === row.id ? "danger" : undefined}
            busy={purgeBusy === row.id}
            disabled={purgeBusy !== null}
            onClick={() => onPurge(row)}
            // The consequence, where the click is, rather than in a
            // paragraph above the table nobody reads twice.
            title={
              purgeConfirming === row.id
                ? "Deletes this container's row and its stored CPU and memory history"
                : undefined
            }
          >
            {purgeConfirming === row.id ? "Confirm purge" : "Purge"}
          </Button>
        ),
    });
  }

  return columns;
}

/**
 * How close a container is to being OOM-killed, as a row severity.
 *
 * The rail this feeds and the meter inside the row read the SAME number
 * through the same function (Meter's severityFromPercent), so they cannot
 * disagree -- an amber bar on a row with a red rail would be worse than
 * either mark alone.
 *
 * Memory only, and against the container's OWN limit. CPU is deliberately not
 * consulted: a container pinned at 100% of a core is doing its job, and
 * netra has no idea what that container is for -- railing it would mark the
 * busiest row on every honest fleet. Being near a limit that will kill you is
 * the one thing this list knows is bad.
 *
 * An unlimited container has no denominator and so no severity, which is
 * Meter's own rule: a bar against an invented ceiling is a lie.
 */
export function containerSeverity(
  row: ContainerRow,
): "warning" | "critical" | null {
  const limit = row.mem_limit_bytes ?? null;
  if (limit === null || limit <= 0) return null;
  const used = lastReported(row.mem);
  if (used === null) return null;
  const severity = severityFromPercent((used / limit) * 100);
  return severity === "ok" ? null : severity;
}
