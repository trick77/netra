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
import { Meter } from "../../ui/Meter";
import type { Column } from "../../ui/Table";
import { ABSENT } from "../../lib/format";
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

// The container_key is the stable identity (it survives a rename); the name
// is what an operator reads. A container with no name still has a key, so
// the compose identity behind that key is always shown rather than being a
// fallback that silently changes what the primary line means from row to row.
//
// "project / service" rather than the raw "project/service": those two halves
// are what the host tab used to spend two whole columns on, and spacing them
// is what lets a reader separate them at a glance without those columns.
function NameCell({ row }: { row: ContainerRow }) {
  const { project, service } = composeIdentity(row.container_key);
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
      <div className="host-cell-site mono">
        {project === ABSENT ? service : `${project} / ${service}`}
      </div>
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
   * Both current lists group -- the fleet by host, a host page by compose
   * project -- and a list grouped by host carries the hostname in its group
   * header, so nothing reaches this today. It stays because a flat fleet list
   * is the obvious next variant and this column is the only thing it needs.
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
}

export function containerColumns({
  showHost = false,
  cpuMax,
  memMax,
  range = "24h",
  ranges,
}: ContainerColumnsOptions = {}): Column<ContainerRow>[] {
  const columns: Column<ContainerRow>[] = [
    {
      key: "container",
      header: "Container",
      cell: (row) => <NameCell row={row} />,
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
    cell: (row) => <span className="mono">{row.image ?? ABSENT}</span>,
    sortValue: (row) => row.image ?? null,
  });

  // The trend columns appear only when someone fetched the metrics.
  if (cpuMax !== undefined || memMax !== undefined) {
    columns.push({
      key: "cpu",
      header: "CPU",
      cell: (row) =>
        row.cpu === undefined || row.cpu.length === 0 ? (
          ABSENT
        ) : (
          <ContainerChart
            row={row}
            metric="cpu"
            values={row.cpu}
            max={cpuMax ?? 1}
            range={range}
            ranges={ranges}
          />
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
