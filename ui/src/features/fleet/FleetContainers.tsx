import { Boxes } from "lucide-react";
import { EmptyState } from "../../ui/EmptyState";
import { Table, type Column } from "../../ui/Table";
import { Badge } from "../../ui/Badge";
import { ABSENT } from "../../lib/format";
import type { Container } from "../../lib/api";

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
};

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
}: { showHost?: boolean } = {}): Column<ContainerRow>[] {
  const columns: Column<ContainerRow>[] = [
    {
      key: "container",
      header: "Container",
      cell: (row) => <NameCell row={row} />,
    },
  ];
  if (showHost) {
    columns.push({
      key: "host",
      header: "Host",
      cell: (row) => <a href={`#/hosts/${row.host_id}`}>{row.hostname}</a>,
    });
  }
  columns.push({
    key: "image",
    header: "Image",
    // An image with no tag on the wire is not `:latest` -- inventing one
    // would name a version the agent never reported.
    //
    // `.mono` is an identifier face (spec 3.1: --font-mono for versions,
    // addresses, commands and identifiers). index.css does not define it
    // yet -- see the task report. `.tnum` would be wrong here: that is
    // tabular figures, for columns of digits that must line up.
    cell: (row) => <span className="mono">{row.image ?? ABSENT}</span>,
  });
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
          href={`#/containers/${row.host_id}/${row.container_key}`}
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
}

export function FleetContainers({ rows, showHost }: FleetContainersProps) {
  if (rows.length === 0) {
    // An empty <table> renders as a bare header rail, which reads as a
    // loading glitch rather than as "nothing here" -- same reasoning as
    // HostTable's empty state.
    return (
      <EmptyState
        icon={Boxes}
        title="No containers"
        body="No host in this fleet has reported a container."
      />
    );
  }

  return (
    <Table
      columns={containerColumns({ showHost })}
      rows={rows}
      // Two hosts can run the same container_key, so identity is the pair.
      rowKey={(row) => `${row.host_id}:${row.container_key}`}
    />
  );
}
