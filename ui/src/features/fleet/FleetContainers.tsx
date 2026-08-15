import { Boxes } from "lucide-react";
import { EmptyState } from "../../ui/EmptyState";
import { Table, type Column } from "../../ui/Table";
import { Badge } from "../../ui/Badge";
import { ABSENT } from "../../lib/format";
import type { Container } from "../../lib/api";
import { fleetContainerNotes, type CapableHost } from "../../lib/containers";
import { Sparkline } from "../../ui/charts/Sparkline";
import { rangeLabel, type Range } from "../../lib/range";

/**
 * A container as the fleet sees it: what `GET /api/v1/hosts/{id}/containers`
 * returns plus the host it came from. The host is carried on the row rather
 * than looked up while rendering -- there is no fleet-wide container
 * endpoint (only the per-host one), so whoever fanned the calls out already
 * knows which host each response belongs to and is the only party that
 * cannot get it wrong.
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
  /** The container's own memory ceiling, or null when it runs unlimited. */
  mem_limit_bytes?: number | null;
};

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
 * The one container column definition, parameterised by whether the Host
 * column is present. Spec 4.5: the fleet-wide container overview is "the
 * same container row plus a Host column", so "every postgres in the fleet"
 * and "everything on this host" must be one component -- the host-detail
 * Inventory tab renders this same list with `showHost: false`.
 *
 * There is deliberately no state, health, restart-count or uptime column:
 * none of those reach the wire or the schema (container_samples carries CPU
 * and memory only). A column showing "running" for every row would be an
 * assertion netra cannot make.
 */
export function containerColumns({
  showHost = false,
  cpuMax,
  memMax,
  range = "24h",
}: {
  showHost?: boolean;
  /** Only for the charts' accessible names -- this file never resolves a
   * range into a query. */
  range?: Range;
  /**
   * Shared ceilings for the sparklines, computed across the whole list by
   * `trendScales` below. Per-row auto-scaling would draw an idle container
   * and a saturated one with the identical silhouette -- the same reading
   * the host list's CPU column carries a fixed ceiling to avoid -- and a
   * list exists to be compared down its columns.
   */
  cpuMax?: number;
  memMax?: number;
} = {}): Column<ContainerRow>[] {
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
    sortValue: (row) => row.image ?? null,
    // An image with no tag on the wire is not `:latest` -- inventing one
    // would name a version the agent never reported.
    //
    // `.mono` is an identifier face (spec 3.1: --font-mono for versions,
    // addresses, commands and identifiers). index.css does not define it
    // yet -- see the task report. `.tnum` would be wrong here: that is
    // tabular figures, for columns of digits that must line up.
    cell: (row) => <span className="mono">{row.image ?? ABSENT}</span>,
  });

  // The trend columns appear only when someone fetched the metrics. A list
  // that did not ask for them renders as it always did rather than growing
  // two columns of permanent gaps.
  if (cpuMax !== undefined || memMax !== undefined) {
    columns.push({
      key: "cpu",
      header: "CPU",
      cell: (row) =>
        row.cpu === undefined || row.cpu.length === 0 ? (
          ABSENT
        ) : (
          <Sparkline
            values={row.cpu}
            max={cpuMax}
            min={0}
            color="var(--s1)"
            label={`CPU trend, ${rangeLabel(range)}`}
          />
        ),
    });
    columns.push({
      key: "memory",
      header: "Memory",
      cell: (row) =>
        row.mem === undefined || row.mem.length === 0 ? (
          ABSENT
        ) : (
          <Sparkline
            values={row.mem}
            // Against its OWN limit when it has one -- that is what "how
            // close to being killed" means -- and against the list's
            // largest container when it does not, so the unlimited ones
            // stay comparable with each other.
            max={row.mem_limit_bytes ?? memMax}
            min={0}
            color="var(--s2)"
            label={`Memory trend, ${rangeLabel(range)}`}
          />
        ),
    });
  }

  return columns;
}

// The container_key is the stable identity (it survives a rename); the name
// is what an operator reads. A container with no name still has a key, so
// the key is always shown rather than being a fallback that silently
// changes what the primary line means from row to row.
function NameCell({ row }: { row: ContainerRow }) {
  return (
    <div className="host">
      <div>
        <a
          className="name"
          href={`/containers/${row.host_id}/${encodeURIComponent(row.container_key)}`}
        >
          {row.name ?? ABSENT}
        </a>
        <div className="site mono">{row.container_key}</div>
      </div>
      {/* netra's own agent runs as a container on most hosts; unlabelled it
          reads as a workload someone deployed. */}
      {row.is_agent ? <Badge>agent</Badge> : null}
    </div>
  );
}

export interface FleetContainersProps {
  rows: readonly ContainerRow[];
  showHost?: boolean;
  /** False while the container fan-out has not answered yet. */
  loaded?: boolean;
  /** Passed through to the charts' accessible names. */
  range?: Range;
  /**
   * The fleet's hosts, for their `containers` capability alone. A host that
   * reports `no-cgroup-scopes` contributes no rows at all, so nothing in
   * `rows` can explain the gap -- the explanation has to come from the hosts
   * the rows are missing from. Optional: a caller that has no host list gets
   * the same view as before.
   */
  hosts?: readonly CapableHost[];
  /**
   * Whether `rows` has been narrowed by the page's search box.
   *
   * `rows` arrives already filtered, so an empty one means either "the fleet
   * has none" or "your search matched none" -- and this component cannot tell
   * them apart on its own. It matters because of the capability notes below:
   * filtering a 19-container fleet down to nothing would otherwise answer with
   * "no cgroup scopes … re-run setup-agent.sh", turning a search term into a
   * specific instruction to go and reconfigure a host.
   */
  filtered?: boolean;
}

export function FleetContainers({
  rows,
  showHost,
  loaded = true,
  range = "24h",
  hosts,
  filtered = false,
}: FleetContainersProps) {
  // A search that matched nothing says nothing about the fleet, so it gets no
  // explanation of the fleet: with rows filtered away, a note would be read as
  // the answer to "where are my containers" when the answer is "you typed a
  // filter". Notes stay on a list that still has rows -- there they annotate
  // what is shown rather than replacing it.
  const filteredToNothing = filtered && rows.length === 0;

  // Only once the fan-out has answered. While it is still running the list is
  // short for a reason that has nothing to do with any capability, and
  // "incomplete" would be a different and wronger sentence than "not read
  // yet".
  const notes =
    loaded && !filteredToNothing
      ? fleetContainerNotes(hosts ?? [], { partial: rows.length > 0 })
      : [];

  if (rows.length === 0) {
    // An empty <table> renders as a bare header rail, which reads as a
    // loading glitch rather than as "nothing here" -- same reasoning as
    // HostTable's empty state.
    // "None reported" and "not read yet" are different facts, and only one
    // of them is a statement about the fleet. Saying the first while the
    // fetch had never run told an operator their fleet ran no containers
    // when nobody had looked.
    return loaded ? (
      // With a capability to show, the empty state stops being a shrug. "No
      // host has reported a container" is true of a fleet running none and of
      // a fleet whose agents cannot see the ones it runs, and only the second
      // is something to fix.
      <EmptyState
        icon={Boxes}
        title={notes.length > 0 ? "No containers collected" : "No containers"}
        body={
          notes.length > 0
            ? notes.join(" ")
            : "No host in this fleet has reported a container."
        }
      />
    ) : (
      <EmptyState
        icon={Boxes}
        title="Containers not read yet"
        body="The fleet's containers are still being fetched, one host at a time."
      />
    );
  }

  // The trend columns appear only when the rows carry trends, and their
  // ceilings are shared across the list so the sparklines can be compared
  // down the column.
  const charted = rows.some((row) => row.cpu !== undefined);
  const scales = charted ? trendScales(rows) : {};

  return (
    <>
      {/* Above the table, not below it: a list that is short by a whole host
          looks complete, and an operator who has already read it has no
          reason to keep scrolling. */}
      {notes.map((note) => (
        <p className="note" key={note}>
          {note}
        </p>
      ))}
      <Table
        columns={containerColumns({ showHost, range, ...scales })}
        rows={rows}
        // Two hosts can run the same container_key, so identity is the pair.
        rowKey={(row) => `${row.host_id}:${row.container_key}`}
      />
    </>
  );
}
