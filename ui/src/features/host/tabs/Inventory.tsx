// The inventory family: Containers, Filesystems, Network, Packages and
// Units are ONE searchable-list component differing only in columns. Each
// list shows first_seen/last_seen only where the schema actually has them
// -- an empty "last seen" column would be a claim netra cannot back.
import { useState, type ReactNode } from "react";
import { Inbox } from "lucide-react";
import type {
  Address,
  Container,
  Filesystem,
  Pkg,
  Unit,
  MetricsResponse,
} from "../../../lib/api";
import { ABSENT, absolute, bytes, relative } from "../../../lib/format";
import { hostContainerNote } from "../../../lib/containers";
import { Badge, type Severity } from "../../../ui/Badge";
import { Input } from "../../../ui/Control";
import { EmptyState } from "../../../ui/EmptyState";
import { Table, type Column } from "../../../ui/Table";
import { Meter } from "../../../ui/Meter";
import { Sparkline } from "../../../ui/charts/Sparkline";
import { rangeLabel, type Range } from "../../../lib/range";
import { griddedValues } from "../../../lib/metrics";
import { ContainerChart } from "../../container/ContainerChart";
import { containerTrends } from "../../fleet/hostTrends";
import { RANGE_VALUES } from "../ranges";

export interface InventoryProps<T> {
  /** Names the list for assistive tech and for the empty state. */
  label: string;
  columns: readonly Column<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  /** Everything a search should match, flattened by the caller -- this
   * component never guesses which fields of an unknown row are text. */
  searchText: (row: T) => string;
  /** List-specific filters (Packages' 30-day toggle), rendered beside the
   * search box. The caller applies them to `rows`; this component only
   * gives them a home so every list's toolbar looks the same. */
  controls?: ReactNode;
  /** Why this list is short, when the agent said so. Rendered above the list
   * rather than in place of it: the two facts are "what was collected" and
   * "what stopped the rest being collected", and a list that is partly there
   * needs both. */
  notice?: ReactNode;
}

export function Inventory<T>({
  label,
  columns,
  rows,
  rowKey,
  searchText,
  controls,
  notice,
}: InventoryProps<T>) {
  const [query, setQuery] = useState("");
  const needle = query.trim().toLowerCase();
  const visible = needle
    ? rows.filter((row) => searchText(row).toLowerCase().includes(needle))
    : rows;

  return (
    <section aria-label={label}>
      <div className="toolbar">
        <Input
          type="search"
          aria-label={`Filter ${label}`}
          placeholder={`Filter ${label.toLowerCase()}`}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        {controls}
      </div>
      {notice}
      {visible.length === 0 ? (
        <EmptyState
          icon={Inbox}
          title={
            rows.length === 0 ? "Nothing collected yet" : "Nothing matches"
          }
          body={
            rows.length === 0
              ? `This host has reported no ${label.toLowerCase()}.`
              : `No ${label.toLowerCase()} match “${query}”.`
          }
        />
      ) : (
        <Table columns={columns} rows={visible} rowKey={rowKey} />
      )}
    </section>
  );
}

/** A timestamp reads relative, with the absolute time on hover (spec §9). */
function When({ iso }: { iso: string | null }) {
  if (iso === null) return <>{ABSENT}</>;
  return <span title={absolute(iso)}>{relative(iso)}</span>;
}

// --- Containers -----------------------------------------------------------

/**
 * container_key is the compose identity, "project/service" -- the agent
 * refuses to send the Docker id for it (see
 * internal/agent/collector/containers_test.go), because that id changes on
 * every `compose up -d` and keying history on it would orphan every series
 * the container has. The list therefore shows project and service, and the
 * hub's surrogate row id appears nowhere.
 */
export function composeIdentity(key: string): {
  project: string;
  service: string;
} {
  const slash = key.indexOf("/");
  if (slash === -1) return { project: ABSENT, service: key };
  return { project: key.slice(0, slash), service: key.slice(slash + 1) };
}

const CONTAINER_COLUMNS: Column<Container>[] = [
  {
    key: "project",
    header: "Project",
    cell: (row) => composeIdentity(row.container_key).project,
  },
  {
    key: "service",
    header: "Service",
    cell: (row) => composeIdentity(row.container_key).service,
  },
  { key: "name", header: "Name", cell: (row) => row.name ?? ABSENT },
  { key: "image", header: "Image", cell: (row) => row.image ?? ABSENT },
  {
    key: "agent",
    header: "Agent",
    cell: (row) => (row.is_agent ? <Badge severity="ok">agent</Badge> : ""),
  },
];

type ContainerWithTrend = Container & {
  cpu?: (number | null)[];
  mem?: (number | null)[];
  memLimit?: number | null;
};

export function Containers({
  hostId,
  rows,
  metrics = null,
  range = "24h",
  capabilities,
}: {
  /** Whose containers these are. The trend cells enlarge into a chart that
   * refetches at another range, and family=container is per-host. */
  hostId: number | string;
  rows: readonly Container[];
  /** A family=container response for this host. */
  metrics?: MetricsResponse | null;
  range?: Range;
  /** The host's own capability map. Only `containers` is read, and only to
   * say why this list is empty or unnamed -- "This host has reported no
   * containers" is true of a host running none and of a host whose agent
   * cannot see the ones it runs. */
  capabilities?: Record<string, string>;
}) {
  // The same trends the fleet's container list shows, for the same reason: a
  // list of containers with no time in it says what is there and nothing
  // about what any of it is doing. Read through the fleet's own function
  // rather than reimplemented beside it, so the two lists and the enlarged
  // view opened from either cannot disagree about which columns these are.
  const byKey = containerTrends(metrics);

  const charted: ContainerWithTrend[] = rows.map((row) => ({
    ...row,
    ...byKey.get(row.container_key),
  }));

  // Shared ceilings across the list, so the column can be read down rather
  // than each row filling its own box regardless of magnitude.
  let cpuMax = 0;
  let memMax = 0;
  for (const row of charted) {
    for (const v of row.cpu ?? []) if (v !== null && v > cpuMax) cpuMax = v;
    for (const v of row.mem ?? []) if (v !== null && v > memMax) memMax = v;
  }

  const columns: Column<ContainerWithTrend>[] = [...CONTAINER_COLUMNS];
  if (metrics !== null) {
    columns.push({
      key: "cpu",
      header: "CPU",
      cell: (row) =>
        !row.cpu?.length ? (
          ABSENT
        ) : (
          <ContainerChart
            hostId={hostId}
            containerKey={row.container_key}
            containerName={row.name ?? row.container_key}
            metric="cpu"
            values={row.cpu}
            max={cpuMax || 1}
            range={range}
            ranges={RANGE_VALUES}
          />
        ),
    });
    columns.push({
      key: "memory",
      header: "Memory",
      cell: (row) =>
        !row.mem?.length ? (
          ABSENT
        ) : (
          <ContainerChart
            hostId={hostId}
            containerKey={row.container_key}
            containerName={row.name ?? row.container_key}
            metric="mem"
            values={row.mem}
            // Its own limit when it has one; the list's largest otherwise.
            max={row.memLimit ?? memMax ?? 1}
            range={range}
            ranges={RANGE_VALUES}
          />
        ),
    });
  }

  const note = hostContainerNote(capabilities);

  return (
    <Inventory
      label="Containers"
      columns={columns}
      rows={charted}
      rowKey={(row) => row.container_key}
      searchText={(row) =>
        [row.container_key, row.name, row.image].filter(Boolean).join(" ")
      }
      notice={note === null ? undefined : <p className="note">{note}</p>}
    />
  );
}

// --- Filesystems ----------------------------------------------------------

// The inventory row carries a label, a mountpoint and a device id; size,
// used and free live in the metrics family, so the sizes are joined in by
// label. A filesystem the metrics have not answered for renders the absent
// marker rather than a zero -- "not measured" is not "empty".
type FilesystemRow = Filesystem & {
  total: number | null;
  used: number | null;
  free: number | null;
};

const FILESYSTEM_COLUMNS: Column<FilesystemRow>[] = [
  { key: "label", header: "Label", cell: (row) => row.label },
  {
    key: "mountpoint",
    header: "Mountpoint",
    cell: (row) => row.mountpoint ?? ABSENT,
  },
  { key: "size", header: "Size", cell: (row) => bytes(row.total) },
  { key: "used", header: "Used", cell: (row) => bytes(row.used) },
  { key: "free", header: "Free", cell: (row) => bytes(row.free) },
  {
    key: "usage",
    header: "Usage",
    // used / (used + free), which is df's Use% -- NOT used / total, because
    // total includes the root reserve and would report a full disk as less
    // full than df does. Same definition as the fleet list's disk meter.
    cell: (row) =>
      row.used === null || row.free === null || row.used + row.free === 0 ? (
        ABSENT
      ) : (
        <Meter
          value={(row.used / (row.used + row.free)) * 100}
          max={100}
          label={row.label}
        />
      ),
  },
];

export function Filesystems({
  rows,
  metrics = null,
}: {
  rows: readonly Filesystem[];
  metrics?: MetricsResponse | null;
}) {
  const sizes = new Map(
    (metrics?.series ?? []).map((series, index) => [
      series.key.filesystem,
      {
        total: lastOf(metrics, index, "total"),
        used: lastOf(metrics, index, "used"),
        free: lastOf(metrics, index, "free"),
      },
    ]),
  );

  const joined: FilesystemRow[] = rows.map((row) => ({
    ...row,
    total: sizes.get(row.label)?.total ?? null,
    used: sizes.get(row.label)?.used ?? null,
    free: sizes.get(row.label)?.free ?? null,
  }));

  return (
    <Inventory
      label="Filesystems"
      columns={FILESYSTEM_COLUMNS}
      rows={joined}
      rowKey={(row) => String(row.id)}
      searchText={(row) => `${row.label} ${row.mountpoint ?? ""}`}
    />
  );
}

/** The latest non-null value of a column, or null if it never reported. */
function lastOf(
  res: MetricsResponse | null,
  index: number,
  base: string,
): number | null {
  const values = griddedValues(res, index, base);
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i];
    if (v !== null && v !== undefined) return v;
  }
  return null;
}

// --- Network --------------------------------------------------------------

// host_addresses.family holds the AF_* number the kernel uses, not a name.
const FAMILY_NAME: Record<number, string> = { 4: "IPv4", 6: "IPv6" };

const ADDRESS_COLUMNS: Column<Address>[] = [
  { key: "iface", header: "Interface", cell: (row) => row.iface },
  { key: "address", header: "Address", cell: (row) => row.address },
  {
    key: "family",
    header: "Family",
    cell: (row) => FAMILY_NAME[row.family] ?? String(row.family),
  },
  { key: "scope", header: "Scope", cell: (row) => row.scope ?? ABSENT },
  // The interface alias -- ip link set dev eth0 alias "uplink to core-sw1"
  // -- which the agent now reports. It was already searchable here and
  // shown nowhere, so a host whose operator had labelled every NIC still
  // presented a table of bare kernel names.
  //
  // Empty is ABSENT rather than a blank cell: `ip` reports no alias as an
  // absent attribute, and the store writes it as NULL (families.go), so
  // the distinction survives the round trip.
  {
    key: "description",
    header: "Description",
    cell: (row) => row.description ?? ABSENT,
  },
  // The VRF column is gone rather than blank. sysfs cannot identify a VRF
  // master -- drivers/net/vrf.c sets no DEVTYPE -- so the addresses
  // collector writes vrfUnknown ("") for every interface on every host, by
  // its own documented decision (internal/agent/collector/addresses.go).
  // A column that is structurally incapable of holding a value teaches
  // readers that this table has nothing in it.
  //
  // host_addresses.vrf, the read API's field and Address.vrf all stay: the
  // data is merely unobtainable through sysfs, and a collector that speaks
  // rtnetlink IFLA_INFO_KIND would fill it without a schema change. It is
  // still searchable below for the same reason.
  {
    key: "first_seen",
    header: "First seen",
    cell: (row) => <When iso={row.first_seen} />,
  },
  {
    key: "last_seen",
    // "Last changed", not "Last seen", because that is what the column holds.
    //
    // The hub does bump last_seen on every upsert
    // (families.go's UpsertHostAddresses), but the upsert only runs when the
    // address set CHANGES: the collector compares a fingerprint against the
    // previous scrape and returns nothing when they match, and Addresses --
    // unlike Packages -- has no periodic floor that re-sends an unchanged set
    // anyway. So a healthy host that keeps the same IPs never touches these
    // rows again, and the timestamp drifts further into the past the longer
    // it stays healthy. Headed "Last seen" that read as the host having gone
    // quiet; it is the exact opposite.
    //
    // The packages table keeps "Last seen" and is not the same bug: its
    // collector re-emits the whole inventory when the daily confirmation
    // falls due, changed or not, so there the timestamp really is a
    // last-seen.
    header: "Last changed",
    cell: (row) => <When iso={row.last_seen} />,
  },
];

export function Network({ rows }: { rows: readonly Address[] }) {
  return (
    <Inventory
      label="Addresses"
      columns={ADDRESS_COLUMNS}
      rows={rows}
      rowKey={(row) => `${row.iface}/${row.address}`}
      searchText={(row) =>
        [row.iface, row.address, row.scope, row.vrf, row.description]
          .filter(Boolean)
          .join(" ")
      }
    />
  );
}

// --- Packages -------------------------------------------------------------

const PACKAGE_COLUMNS: Column<Pkg>[] = [
  { key: "name", header: "Name", cell: (row) => row.name },
  { key: "version", header: "Version", cell: (row) => row.version },
  { key: "arch", header: "Arch", cell: (row) => row.arch },
  { key: "format", header: "Format", cell: (row) => row.format },
  {
    key: "size",
    header: "Size",
    align: "right",
    cell: (row) => bytes(row.size_bytes),
  },
  {
    key: "first_seen",
    header: "First seen",
    cell: (row) => <When iso={row.first_seen} />,
  },
  {
    key: "last_seen",
    header: "Last seen",
    cell: (row) => <When iso={row.last_seen} />,
  },
];

/**
 * Whether this package row appeared inside the window -- the "what changed
 * before this broke" question.
 *
 * first_seen is the only usable signal: an install or an upgrade writes a
 * new (name, version) row and so a new first_seen, while last_seen is
 * refreshed by every scrape and would mark every installed package as
 * changed.
 */
export function changedSince(row: Pkg, now: Date, days: number): boolean {
  const cutoff = now.getTime() - days * 24 * 60 * 60 * 1000;
  return new Date(row.first_seen).getTime() >= cutoff;
}

const CHANGED_WINDOW_DAYS = 30;

export function Packages({
  rows,
  now = new Date(),
}: {
  rows: readonly Pkg[];
  /** Injected by tests so the 30-day window is deterministic. */
  now?: Date;
}) {
  const [onlyChanged, setOnlyChanged] = useState(false);
  const visible = onlyChanged
    ? rows.filter((row) => changedSince(row, now, CHANGED_WINDOW_DAYS))
    : rows;

  return (
    <Inventory
      label="Packages"
      columns={PACKAGE_COLUMNS}
      rows={visible}
      rowKey={(row) => `${row.name}/${row.arch}/${row.version}`}
      searchText={(row) => `${row.name} ${row.version} ${row.arch}`}
      controls={
        <label>
          <input
            type="checkbox"
            checked={onlyChanged}
            onChange={(e) => setOnlyChanged(e.target.checked)}
          />{" "}
          changed in the last {CHANGED_WINDOW_DAYS} days
        </label>
      }
    />
  );
}

// --- Units ----------------------------------------------------------------

// systemd's state vocabulary, mapped to the status palette. Anything not
// listed stays neutral rather than being guessed at: "reloading" is not a
// problem, and inventing a colour for an unknown state would say it is.
const UNIT_SEVERITY: Record<string, Severity> = {
  active: "ok",
  failed: "critical",
  inactive: "neutral",
};

const UNIT_COLUMNS: Column<Unit>[] = [
  { key: "unit", header: "Unit", cell: (row) => row.unit_name },
  {
    key: "state",
    header: "State",
    cell: (row) =>
      row.state === null ? (
        ABSENT
      ) : (
        <Badge severity={UNIT_SEVERITY[row.state] ?? "neutral"}>
          {row.state}
        </Badge>
      ),
  },
  {
    key: "substate",
    header: "Substate",
    cell: (row) => row.substate ?? ABSENT,
  },
  { key: "since", header: "Since", cell: (row) => <When iso={row.since} /> },
];

export function Units({ rows }: { rows: readonly Unit[] }) {
  return (
    <Inventory
      label="Units"
      columns={UNIT_COLUMNS}
      rows={rows}
      rowKey={(row) => String(row.id)}
      searchText={(row) =>
        [row.unit_name, row.state, row.substate].filter(Boolean).join(" ")
      }
    />
  );
}
