import { Boxes } from "lucide-react";
import { EmptyState } from "../../ui/EmptyState";
import { Table } from "../../ui/Table";
import {
  containerColumns,
  trendScales,
  type ContainerRow,
} from "../container/columns";
import {
  fleetContainerNotes,
  fleetContainersBlocked,
  type CapableHost,
} from "../../lib/containers";
import { type Range } from "../../lib/range";

// The row shape and the column set live in features/container/columns, with
// the page they link to. This file is the fleet's framing around them: the
// three empty states, the capability notes, and the grouping by host.
//
// Re-exported because App and FleetPage build these rows and scale them, and
// moving one definition should not move every import of it.
export {
  trendScales,
  containerColumns,
  type ContainerRow,
} from "../container/columns";

export interface FleetContainersProps {
  rows: readonly ContainerRow[];
  /**
   * Adds the Host column.
   *
   * Off by default and unset by the fleet page, because this list groups by
   * host: the hostname is already the group header, and already a link to
   * that host, so a Host column repeats it on every row. The prop stays for
   * a caller that wants the flat reading.
   */
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
  showHost = false,
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
    if (!loaded) {
      return (
        <EmptyState
          icon={Boxes}
          title="Containers not read yet"
          body="The fleet's containers are still being fetched, one host at a time."
        />
      );
    }

    // A filter that matched nothing is not a fact about the fleet, and the
    // fleet-wide sentence below would be answering a question nobody asked.
    if (filteredToNothing) {
      return (
        <EmptyState
          icon={Boxes}
          title="No containers match"
          body="No container in this fleet matches the filter."
        />
      );
    }

    // Only a capability that means NOTHING was collected may replace the
    // empty state, and that is `no-cgroup-scopes` alone. A fleet of hosts
    // with no Docker installed reports `no-docker-socket` with an empty list
    // and is perfectly healthy -- announcing "No containers collected" over
    // it turns a fleet that simply runs none into a fault, and throws away
    // the only true sentence there is about it.
    return fleetContainersBlocked(hosts ?? []) ? (
      <EmptyState
        icon={Boxes}
        title="No containers collected"
        body={notes.join(" ")}
      />
    ) : (
      <>
        {/* Beside the empty state rather than instead of it: both facts hold
            -- the fleet reported none, and the names would have been raw ids
            if it had. */}
        {notes.map((note) => (
          <p className="note" key={note}>
            {note}
          </p>
        ))}
        <EmptyState
          icon={Boxes}
          title="No containers"
          body="No host in this fleet has reported a container."
        />
      </>
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
        groupBy={BY_HOST}
      />
    </>
  );
}

// Hoisted, not an inline literal: Table memoises the partition on the
// groupBy identity, and a fresh object every render recomputes it every
// render. Neither half closes over anything, so there is nothing to capture.
//
// By host_id, never by hostname: two hosts in different sites may share a
// hostname (see HostTable), and grouping on the name would merge two
// machines into one group and file one host's containers under the other
// host's link. It is the same reason rowKey is a pair.
const BY_HOST = {
  key: (row: ContainerRow) => String(row.host_id),
  label: (_key: string, group: readonly ContainerRow[]) => (
    <HostGroup rows={group} />
  ),
};

/** A group header that is also the way into the host it names. */
function HostGroup({ rows }: { rows: readonly ContainerRow[] }) {
  // Every row in the group carries the same host by construction, so the
  // first one is as good as any -- and a group is never empty.
  const { host_id, hostname } = rows[0];
  return (
    <>
      <a href={`/hosts/${host_id}/overview`}>{hostname}</a>
      {/* A real text node, not a CSS ::before: the separator is the only
          thing between two facts, and read aloud "db-011 container" is not
          the sentence. */}
      <span className="groupcount">
        {" · "}
        {rows.length} container{rows.length === 1 ? "" : "s"}
      </span>
    </>
  );
}
