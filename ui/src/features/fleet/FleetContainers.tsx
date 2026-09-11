import { useMemo } from "react";
import { Boxes } from "lucide-react";
import { EmptyState } from "../../ui/EmptyState";
import { Table } from "../../ui/Table";
import {
  composeIdentity,
  containerColumns,
  containerGroupCells,
  containerGroupWorst,
  GroupBytes,
  containerSeverity,
  trendScales,
  type ContainerRow,
} from "../container/columns";
import { stateKindLabel } from "../container/state";
import { Badge } from "../../ui/Badge";
import { ABSENT } from "../../lib/format";
import {
  fleetContainerNotes,
  fleetContainersBlocked,
  type CapableHost,
} from "../../lib/containers";
import { RAIL_RANGES, type Range } from "../../lib/range";

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
   * NOBODY SETS THIS ANY MORE, and the prop is kept only so a future list that
   * shows containers from several hosts WITHOUT grouping by host has somewhere
   * to ask. The fleet page set it, and the repetition was defended on the
   * grounds that an opened group's heading scrolls off the top -- true, and
   * not worth eighty-four rows restating what four headings say, in the widest
   * table on the page. The heading is one scroll away.
   *
   * A caller that groups by host should leave this off: the group header is
   * the answer, and a column beside it is the same answer again.
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
  /** The clock the Status column reads, so a test can pin it. */
  now?: Date;
}

export function FleetContainers({
  rows,
  now = new Date(),
  showHost = false,
  loaded = true,
  range = "24h",
  hosts,
  filtered = false,
}: FleetContainersProps) {
  // Before the empty-state returns below, because it is a hook. Memoised, and
  // on the flag alone: BY_STACK is hoisted precisely so Table's partition memo
  // can hold on its identity, and a fresh object every render would undo
  // that.
  const grouping = useMemo(
    () => ({ ...BY_STACK, forceExpanded: filtered }),
    [filtered],
  );
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
          body="The containers are still being fetched, one host at a time."
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
          body="No container on any host matches the filter."
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
          body="No host has reported a container."
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
      {/* .ctr-list is the type scale the two container lists share -- see
          index.css. The host tab (Inventory.tsx) wraps its list the same way. */}
      <div className="ctr-list">
        <Table
          columns={containerColumns({
            showHost,
            range,
            // The fleet's own windows: it stops at 24h, where a host page
            // goes to 7d.
            ranges: RAIL_RANGES,
            now,
            ...scales,
          })}
          rows={rows}
          // Two hosts can run the same container_key, so identity is the pair.
          rowKey={(row) => `${row.host_id}:${row.container_key}`}
          // Which of eighty-four rows to look at, readable from the edge of
          // the table. Memory against the container's own limit, through the
          // same function the meter in the row uses, so the two marks cannot
          // disagree.
          rowSeverity={containerSeverity}
          // `rows` arrives already filtered, so a host still standing is a host
          // with a hit on it -- and a hit inside a closed group is a hit the
          // reader cannot see. This is the only signal here that a filter is on;
          // it is why `filtered` was already a prop.
          groupBy={grouping}
        />
      </div>
    </>
  );
}

// Hoisted, not an inline literal: Table memoises the partition on the
// groupBy identity, so a stable object is what lets that memo hold. (It
// still misses once a sort is active: Table's sort memo also depends on
// `columns`, and the call below builds a fresh array every render.) Neither
// half closes over anything, so there is nothing to capture.
//
// Grouped by (host, compose project) -- a STACK, which is what somebody
// deployed and what fails together. Eighty-four containers are not eighty-four
// things; they are a dozen stacks, and a list that says so is a list a reader
// can hold in their head.
//
// The host is half the key rather than the whole of it, and that is the part
// worth explaining. A compose project running on two machines is two
// deployments, not one -- but the decisive reason is the DENOMINATOR: a
// group's heading reports its share of the machine's cores and RAM, and a
// group spanning three machines has no cores to be a share of. Keying on the
// pair is what keeps every heading answerable.
//
// host_id and not hostname, for the reason rowKey is a pair: two hosts in
// different sites may share a hostname (see HostTable), and grouping on the
// name would file one machine's containers under another's link.
//
// ORDERED by hostname then project, which is a different question from
// identity. On the raw key the groups came out in host registration order
// under headings that read as names, beside a Hosts tab the API returns
// alphabetically -- two tabs of one page ordering the same hosts differently
// looks arbitrary, and more so with every host added. Ordering this way also
// keeps a host's stacks contiguous, so "by host" survives as a reading of this
// list rather than needing a second view of it.
const BY_STACK = {
  // A separator that cannot occur in either half, so "7" + "web" and "7web"
  // cannot collide.
  key: (row: ContainerRow) => `${row.host_id} ${projectOf(row)}`,
  order: (_key: string, group: readonly ContainerRow[]) => {
    const project = projectOf(group[0]);
    // The unnamed group last WITHIN its host rather than at the end of the
    // page: it is that host's containers, and exiling them under a different
    // machine's stacks would be worse than the empty-key rule Table applies
    // to a single grouping level.
    return `${group[0].hostname} ${project === "" ? "￿" : project}`;
  },
  label: (_key: string, group: readonly ContainerRow[]) => (
    <StackGroup rows={group} />
  ),
  // The key is "7\0web", which names nothing -- hence labelText.
  labelText: (_key: string, group: readonly ContainerRow[]) =>
    `${projectName(projectOf(group[0]))} on ${group[0].hostname}`,
  collapsible: true,
  cells: (_key: string, group: readonly ContainerRow[]) =>
    containerGroupCells(group),
  // Folded when there is nothing in it worth opening. Safe only because the
  // heading is a full row -- the group's own CPU and memory bars over the same
  // denominators as the rows beneath, and the worst state in it beside the
  // name. See Table's own note, which this reverses on exactly that ground.
  defaultOpen: (_key: string, group: readonly ContainerRow[]) =>
    containerGroupWorst(group) !== null,
};

/** The compose project a row belongs to, or "" for a container with none. */
function projectOf(row: ContainerRow): string {
  const { project } = composeIdentity(row.container_key);
  return project === ABSENT ? "" : project;
}

const projectName = (key: string) => (key === "" ? "No compose project" : key);

/** A group header naming the stack, the host it runs on, and what is wrong. */
function StackGroup({ rows }: { rows: readonly ContainerRow[] }) {
  // Every row in the group shares a host and a project by construction, so
  // the first one is as good as any -- and a group is never empty.
  const { host_id, hostname } = rows[0];
  const worst = containerGroupWorst(rows);
  return (
    <>
      <span className="proj">{projectName(projectOf(rows[0]))}</span>
      {/* The host names the GROUP rather than every row. Eighty-four rows
          restating what a dozen headings say is the widest column in the
          table spent on the answer the reader was just given -- the same
          argument the Host column's own note made before it was removed. */}
      <span className="onhost">
        {" on "}
        <a className="hostname" href={`/hosts/${host_id}/overview`}>
          {hostname}
        </a>
      </span>
      {/* A real text node, not a CSS ::before: the separator is the only
          thing between two facts, and read aloud "db-011 container" is not
          the sentence. */}
      <span className="groupcount">
        {" · "}
        {rows.length} container{rows.length === 1 ? "" : "s"}
      </span>
      <GroupBytes rows={rows} />
      {/* What a folded group needs to be honest. No badge when nothing is
          wrong, which is itself the signal that folding it cost nothing. */}
      {worst === null ? null : (
        <Badge severity={worst.state.severity}>
          {worst.count} {stateKindLabel(worst.state.kind)}
        </Badge>
      )}
    </>
  );
}
