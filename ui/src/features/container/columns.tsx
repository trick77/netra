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
// There is deliberately no state, health, restart-count or uptime column:
// none of those reach the wire or the schema (container_samples carries CPU
// and memory only). A column showing "running" for every row would be an
// assertion netra cannot make. ContainerPage's deriveState() badge does not
// come here either: it needs sample timestamps, which the host page has and
// the fleet fan-out does not, so it would appear on one list and not the
// other -- the exact asymmetry this module exists to remove.
import { Badge } from "../../ui/Badge";
import { Meter, severityFromPercent } from "../../ui/Meter";
import type { Column } from "../../ui/Table";
import { ABSENT, bytes, percent } from "../../lib/format";
import type { Container } from "../../lib/api";
import { type Range } from "../../lib/range";
import { ContainerChart } from "./ContainerChart";

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
};

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
 * `limit` is null unless EVERY container in the group has one. A group of
 * four where three are capped has no ceiling to be a percentage of, and
 * summing only the three that do would draw a meter against a denominator
 * smaller than the numerator can reach. That is Meter's own rule -- no bar
 * against an invented total -- applied to a group.
 */
export function containerGroupTotals(rows: readonly ContainerRow[]): {
  cpu: number | null;
  mem: number | null;
  limit: number | null;
} {
  let cpu: number | null = null;
  let mem: number | null = null;
  let limit: number | null = 0;
  for (const row of rows) {
    const c = lastReported(row.cpu);
    if (c !== null) cpu = (cpu ?? 0) + c;
    const m = lastReported(row.mem);
    if (m !== null) mem = (mem ?? 0) + m;
    const l = row.mem_limit_bytes ?? null;
    if (l === null) limit = null;
    else if (limit !== null) limit += l;
  }
  return { cpu, mem, limit };
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
export function ContainerGroupTotals({
  rows,
}: {
  rows: readonly ContainerRow[];
}) {
  const { cpu, mem, limit } = containerGroupTotals(rows);
  return (
    <>
      <span className="gstat">
        <span className="lbl">CPU</span>
        <span className="val">{percent(cpu)}</span>
      </span>
      <span className="gstat">
        <span className="lbl">Mem</span>
        <span className="val">{bytes(mem)}</span>
        {/* Always emitted, even empty: they are tracks of the header's grid,
            and a group with no ceiling still has to line its meter up with
            the groups that have one. */}
        <span className="of">{limit === null ? "" : `/ ${bytes(limit)}`}</span>
        <span className="gmeter">
          {limit === null || mem === null ? null : (
            // Meter, not a bar of this file's own: the severity thresholds and
            // the >100% clamp are decisions that must not exist twice. Its
            // reading is blanked because the bytes and the ceiling are already
            // printed to its left -- a third number saying the same ratio is
            // the clutter, not the information. Which is also why a group that
            // has reported no memory draws nothing here rather than a Meter:
            // with no value Meter falls back to printing the absent marker,
            // and the header already carries one where the bytes would be.
            <Meter value={mem} max={limit} formatValue={() => ""} />
          )}
        </span>
      </span>
    </>
  );
}

// The container_key is the stable identity (it survives a rename); the name
// is what an operator reads. A container with no name still has a key, so
// the compose identity behind that key is always shown rather than being a
// fallback that silently changes what the primary line means from row to row.
//
// "project / service" rather than the raw "project/service": those two halves
// are what the host tab used to spend two whole columns on, and spacing them
// is what lets a reader separate them at a glance without those columns.
/**
 * Is the compose identity already legible from the container's own name?
 *
 * Compared on letters and digits only, so the separator compose happened to
 * use ("immich-server", "immich_server", "immichserver") does not decide
 * whether a line of type appears. Equality, never a substring test: "redis"
 * inside "redis-sentinel" is a different service, and treating one as the
 * other would hide the fact that they differ.
 */
function nameSaysIt(
  name: string | null,
  project: string,
  service: string,
): boolean {
  if (name === null) return false;
  const norm = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, "");
  const n = norm(name);
  return n === norm(service) || n === norm(project + service);
}

function NameCell({
  row,
  groupedByProject,
}: {
  row: ContainerRow;
  groupedByProject: boolean;
}) {
  const { project, service } = composeIdentity(row.container_key);

  // In a list already grouped BY project, the identity line drops the project:
  // it is the heading these rows sit under, and repeating it on every one of
  // them spends the widest column in the table saying what the reader was just
  // told. And it drops the line entirely when the name already carries the
  // service -- "immich-server" over "server" is one fact printed twice. What
  // survives is the case the line exists for: a compose-generated name
  // ("monitoring_grafana_1"), or a container renamed away from its service.
  //
  // Only when the list groups by project. The fleet groups by HOST, where the
  // project is not in any heading and this line is the only place it appears
  // at all.
  const identity = groupedByProject
    ? nameSaysIt(row.name ?? null, project === ABSENT ? "" : project, service)
      ? null
      : service
    : project === ABSENT
      ? service
      : `${project} / ${service}`;

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
          {row.name ?? ABSENT}
        </a>
        {/* netra's own agent runs as a container on most hosts; unlabelled it
            reads as a workload someone deployed. Neutral, never severity="ok":
            "agent" is an identity, not a health state, and green would assert
            a state netra does not collect. */}
        {row.is_agent ? <Badge>agent</Badge> : null}
      </div>
      {identity === null ? null : (
        <div className="host-cell-site mono">{identity}</div>
      )}
    </div>
  );
}

/**
 * The memory cell: the trend, plus the reading against the container's own
 * ceiling when it has one.
 *
 * The meter is the only filled colour a container row can honestly carry --
 * every other candidate (state, health, restarts) is uncollected -- and it
 * answers the one question a memory sparkline cannot: how close is this to
 * being OOM-killed. A container running unlimited gets no bar rather than a
 * bar against an invented denominator, which is Meter's own rule.
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
  if (row.mem === undefined || row.mem.length === 0) return <>{ABSENT}</>;
  const limit = row.mem_limit_bytes ?? null;
  return (
    <div className="mem-cell">
      {/* The chart is the button that enlarges it -- only the chart, not the
          meter beside it: the meter answers "how close to being killed" at a
          glance and has nothing bigger to show. */}
      <ContainerChart
        row={row}
        metric="mem"
        values={row.mem}
        window={row.window ?? null}
        // Against its OWN limit when it has one -- that is what "how close to
        // being killed" means -- and against the list's largest container when
        // it does not, so the unlimited ones stay comparable with each other.
        max={limit ?? memMax}
        range={range}
        ranges={ranges}
      />
      {/* The percentage alone, with no label: the bytes are already the
          sparkline's subject, and a table cell has no room for
          "1.8 GB of 2.0 GB". */}
      {limit === null ? null : (
        <Meter value={lastReported(row.mem)} max={limit} />
      )}
    </div>
  );
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
  /**
   * The list groups by compose project, so the name cell stops repeating it.
   *
   * The host page's tab sets this; the fleet's does not, because it groups by
   * host and the project appears nowhere else on its rows. See NameCell.
   */
  groupedByProject?: boolean;
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
}

export function containerColumns({
  showHost = false,
  groupedByProject = false,
  cpuMax,
  memMax,
  range = "24h",
  ranges,
}: ContainerColumnsOptions = {}): Column<ContainerRow>[] {
  const columns: Column<ContainerRow>[] = [
    {
      key: "container",
      header: "Container",
      cell: (row) => <NameCell row={row} groupedByProject={groupedByProject} />,
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
        <a href={`/hosts/${row.host_id}/overview`}>{row.hostname}</a>
      ),
      sortValue: (row) => row.hostname,
    });
  }
  columns.push({
    key: "image",
    header: "Image",
    // An image with no tag on the wire is not `:latest` -- inventing one
    // would name a version the agent never reported.
    //
    // `.mono` is an identifier face (spec 3.1: --font-mono for versions,
    // addresses, commands and identifiers). `.tnum` would be wrong here:
    // that is tabular figures, for columns of digits that must line up.
    //
    // `.imgcell` is what stops it shouting. `.mono` sets a FAMILY and nothing
    // else, so this cell inherited the body's --text-ui (15px) while every
    // other piece of text in the row -- the identity line, the memory
    // reading, the group totals -- is stepped down to --text-label. A
    // mono face at 15px also carries a wider advance and a taller x-height
    // than the sans beside it, so the longest, least urgent string in the
    // row ("ghcr.io/immich-app/immich-server:v1.119.1") was drawn as the
    // loudest. It is a version, read when something is wrong with a version.
    cell: (row) => <span className="imgcell mono">{row.image ?? ABSENT}</span>,
    sortValue: (row) => row.image ?? null,
  });

  // The trend columns appear only when someone fetched the metrics.
  if (cpuMax !== undefined || memMax !== undefined) {
    columns.push({
      key: "cpu",
      header: "CPU",
      // The chart, and the reading it ends on beside it. Memory has carried
      // its number since it got a meter; CPU was a shape with no value at
      // all, so "which container is busiest" could be sorted but not read.
      // The number is --muted (.cval): the sparkline is the subject and this
      // annotates it, the same relationship .traffic-rates has on the fleet
      // row.
      cell: (row) =>
        row.cpu === undefined || row.cpu.length === 0 ? (
          ABSENT
        ) : (
          <div className="ccell">
            <ContainerChart
              row={row}
              metric="cpu"
              values={row.cpu}
              window={row.window ?? null}
              max={cpuMax ?? 1}
              range={range}
              ranges={ranges}
            />
            {/* Nothing, not a dash, when the series reported no value at
                all: the same rule the fleet row's absent readings follow --
                a mark asserts netra looked and found a number it could not
                print, and the gap in the sparkline beside it already says
                the container reported nothing. */}
            {lastReported(row.cpu) === null ? null : (
              <span className="cval tnum">
                {percent(lastReported(row.cpu))}
              </span>
            )}
          </div>
        ),
      // The latest reported percentage, same rule as Memory below. Without
      // it CPU was the one column in this set that could not answer its own
      // question -- "which container is busiest" -- while Container, Image
      // and Memory all sorted, which is the kind of gap this whole module
      // exists to close.
      sortValue: (row) => lastReported(row.cpu),
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
): "warning" | "serious" | "critical" | null {
  const limit = row.mem_limit_bytes ?? null;
  if (limit === null || limit <= 0) return null;
  const used = lastReported(row.mem);
  if (used === null) return null;
  const severity = severityFromPercent((used / limit) * 100);
  return severity === "ok" ? null : severity;
}
