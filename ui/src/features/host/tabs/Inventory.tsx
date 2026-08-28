// The inventory family: Containers, Mounts, Interfaces, Network, Packages
// and Units are ONE searchable-list component differing only in columns. Each
// list shows first_seen/last_seen only where the schema actually has them
// -- an empty "last seen" column would be a claim netra cannot back.
import { useMemo, useState, type ReactNode } from "react";
import { Inbox } from "lucide-react";
import type {
  Address,
  Container,
  Drive,
  Filesystem,
  Iface,
  Pkg,
  Unit,
  MetricsResponse,
} from "../../../lib/api";
import { purgeContainer } from "../../../lib/api";
import { ABSENT, bytes, duration } from "../../../lib/format";
import {
  driveFindings,
  driveKind,
  drivePowerOnHours,
  driveSeverity,
  driveSeverityRank,
  driveTemperature,
  driveTempAttrId,
  driveWearPct,
  temperatureFromRaw,
} from "../smart";
import { hostContainerNote } from "../../../lib/containers";
import { hostDriveNote } from "../../../lib/drives";
import { FLAP_THRESHOLD } from "../../../lib/host";
import { Badge, type Severity } from "../../../ui/Badge";
import { Input } from "../../../ui/Control";
import { EmptyState } from "../../../ui/EmptyState";
import { Table, type Column, type TableProps } from "../../../ui/Table";
import { Meter } from "../../../ui/Meter";
import { When } from "../../../ui/When";
import { rangeLabel, type Range } from "../../../lib/range";
import { RANGE_VALUES } from "../ranges";
import { griddedValues } from "../../../lib/metrics";
import { Sparkline } from "../../../ui/charts/Sparkline";
import { Enlargeable, type DetailData } from "../../../ui/charts/Enlargeable";
import {
  composeIdentity,
  containerColumns,
  ContainerGroupTotals,
  containerSeverity,
  lastReported,
  trendScales,
  type ContainerRow,
} from "../../container/columns";

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
  /** What an empty list means, for a list where "empty" is not "nothing was
   * collected". Units is the case: it shows only what needs attention, so an
   * empty one is a healthy host, and the default copy ("This host has reported
   * no units") would be a plain falsehood about a host running 400 of them.
   * Only the no-rows state is overridden -- "nothing matches your filter" says
   * the same thing for every list. */
  emptyTitle?: string;
  emptyBody?: string;
  /** Splits the list into labelled groups. Handed straight to Table -- see
   * its own note for why grouping has to live there rather than here. */
  groupBy?: TableProps<T>["groupBy"];
  /** Marks a row that needs attention, drawn as a rail down its leading
   * edge. Handed straight to Table; see its note. */
  rowSeverity?: TableProps<T>["rowSeverity"];
  /** Print `label` as a visible heading above the toolbar, in the same
   * .grouphead the chart groups below use.
   *
   * Off by default, because a tab holding ONE list needs no heading -- the
   * tab is the heading. It is on where a tab stacks two, which the Network
   * tab now does: Interfaces above Addresses, told apart otherwise only by
   * the word inside a search box. */
  heading?: boolean;
}

export function Inventory<T>({
  label,
  columns,
  rows,
  rowKey,
  searchText,
  controls,
  notice,
  emptyTitle,
  emptyBody,
  groupBy,
  rowSeverity,
  heading = false,
}: InventoryProps<T>) {
  const [query, setQuery] = useState("");
  const needle = query.trim().toLowerCase();
  const visible = needle
    ? rows.filter((row) => searchText(row).toLowerCase().includes(needle))
    : rows;

  // A collapsible group must be forced open while a filter is on: `visible`
  // is already narrowed, so a group still standing is a group with a hit in
  // it, and a hit inside a closed group is a hit the reader cannot see. Only
  // this component knows whether the search box holds anything, so only it
  // can say so.
  //
  // Memoised, and on the flag rather than the string: callers hoist their
  // groupBy to module scope precisely because Table memoises its partition on
  // that object's identity, and spreading a fresh one every keystroke would
  // throw that away -- on the list that is being re-filtered per keystroke.
  const filtering = needle !== "";
  const grouping = useMemo(
    () =>
      groupBy === undefined
        ? undefined
        : { ...groupBy, forceExpanded: filtering },
    [groupBy, filtering],
  );

  return (
    <section aria-label={label}>
      {heading && <h3 className="grouphead">{label}</h3>}
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
            rows.length === 0
              ? (emptyTitle ?? "Nothing collected yet")
              : "Nothing matches"
          }
          body={
            rows.length === 0
              ? (emptyBody ??
                `This host has reported no ${label.toLowerCase()}.`)
              : `No ${label.toLowerCase()} match “${query}”.`
          }
        />
      ) : (
        // Grouped over what SURVIVED the filter: the search narrows the list
        // and the groups describe what is left, so a filter that empties a
        // group removes the group rather than leaving an empty heading.
        <Table
          columns={columns}
          rows={visible}
          rowKey={rowKey}
          rowSeverity={rowSeverity}
          groupBy={grouping}
        />
      )}
    </section>
  );
}

// --- Containers -----------------------------------------------------------

// The row shape, the column set and composeIdentity all live in
// features/container/columns now, beside the detail page the rows link to.
// This tab used to carry a second, independent CONTAINER_COLUMNS -- Project,
// Service, Name, Image, Agent -- which had drifted away from the fleet's list
// on every axis that matters and, worst, had no anchor in it: from a host's
// own Containers tab the container detail page was unreachable.
//
// composeIdentity is re-exported because it is this module's published name
// for the project/service split and is imported and tested as such.
export { composeIdentity };

// Hoisted for the same reason FleetContainers hoists its own: Table
// memoises the partition on the groupBy identity, so a stable object is what
// lets that memo ever hold.
//
// By compose project: on one host, what belongs together is a stack. A
// container whose key has no slash has no project -- the agent could not read
// the Docker socket -- and "" puts it in Table's trailing unnamed group
// rather than in among the named stacks.
const BY_PROJECT = {
  key: (row: ContainerRow) => {
    const { project } = composeIdentity(row.container_key);
    return project === ABSENT ? "" : project;
  },
  label: (key: string, group: readonly ContainerRow[]) => (
    <>
      <span>{projectName(key)}</span>
      <span className="groupcount">
        {" · "}
        {group.length} container{group.length === 1 ? "" : "s"}
      </span>
    </>
  ),
  labelText: (key: string) => projectName(key),
  // Open by default now -- see Table's own note. A list that arrives showing
  // nothing but headings has not summarised itself, it has hidden itself. The
  // disclosure stays for the reader who wants to fold a noisy stack away, and
  // the summary below is what a folded one keeps saying.
  collapsible: true,
  // The one definition of what a group of containers is using, shared with
  // the fleet's list so the two cannot come to disagree.
  summary: (_key: string, group: readonly ContainerRow[]) => (
    <ContainerGroupTotals rows={group} />
  ),
};

const projectName = (key: string) => (key === "" ? "No compose project" : key);

export function Containers({
  rows,
  host,
  metrics = null,
  range = "24h",
  capabilities,
  onPurged,
}: {
  rows: readonly Container[];
  /**
   * The host these containers belong to.
   *
   * Not decoration: the link to `/containers/{host_id}/{key}` is built from
   * the id, so a tab handed only `Container[]` -- which is what this was --
   * structurally could not offer one. The route knows the id and HostPage
   * knows the name, so both come down from there rather than being
   * re-derived here.
   */
  host: { id: number; hostname: string; last_seen: string | null };
  /** A family=container response for this host. */
  metrics?: MetricsResponse | null;
  range?: Range;
  /** The host's own capability map. Only `containers` is read, and only to
   * say why this list is empty or unnamed -- "This host has reported no
   * containers" is true of a host running none and of a host whose agent
   * cannot see the ones it runs. */
  capabilities?: Record<string, string>;
  /**
   * Called after a container has been purged, so the page can refetch its
   * listing. This tab does not own the fetch and must not pretend to: it is
   * handed `rows`, and a local copy edited in place would disagree with the
   * next poll.
   */
  onPurged?: () => void;
}) {
  // Which row is one click from being purged, and which one is in flight.
  // The two-step confirm is the app's existing pattern -- the host admin
  // table's Delete / Confirm delete pair -- rather than a dialog this app
  // has nowhere else.
  const [confirming, setConfirming] = useState<number | null>(null);
  const [purging, setPurging] = useState<number | null>(null);
  const [purgeError, setPurgeError] = useState<string | null>(null);

  // The same trends the fleet's container list shows, for the same reason: a
  // list of containers with no time in it says what is there and nothing
  // about what any of it is doing.
  const byKey = new Map(
    (metrics?.series ?? []).map((series, index) => [
      series.key.container,
      {
        cpu: griddedValues(metrics, index, "cpu_pct"),
        mem: griddedValues(metrics, index, "mem_used"),
        // mem_limit_bytes, matching ContainerRow. The two lists used to call
        // one quantity by two names, which is how they drifted.
        mem_limit_bytes: lastReported(
          griddedValues(metrics, index, "mem_limit"),
        ),
      },
    ]),
  );

  const charted: ContainerRow[] = rows.map((row) => ({
    ...row,
    host_id: host.id,
    hostname: host.hostname,
    // What "gone" is measured against: a container whose host went quiet
    // with it has not gone anywhere, and neither has one on a host that
    // cannot collect containers at all. See containerIsGone.
    host_last_seen: host.last_seen,
    host_containers_capability: capabilities?.containers,
    window: metrics?.window ?? null,
    ...byKey.get(row.container_key),
  }));

  async function onPurge(row: ContainerRow) {
    setPurgeError(null);
    if (confirming !== row.id) {
      setConfirming(row.id);
      return;
    }
    setConfirming(null);
    setPurging(row.id);
    try {
      await purgeContainer(host.id, row.id);
      onPurged?.();
    } catch (err) {
      setPurgeError(err instanceof Error ? err.message : String(err));
    } finally {
      setPurging(null);
    }
  }

  // trendScales, not a second copy of the same loop: shared ceilings across
  // the list are what make a column readable downwards, and there is one
  // definition of them.
  const columns = containerColumns({
    // Grouped by project below, so the name cell stops repeating it.
    groupedByProject: true,
    range,
    // This page's own windows, so a chart enlarged out of a row cannot ask
    // for one the toolbar above it could not express.
    ranges: RANGE_VALUES,
    // The purge action, which the FLEET list deliberately does not get --
    // see ContainerColumnsOptions.onPurge.
    onPurge: (row: ContainerRow) => void onPurge(row),
    purgeConfirming: confirming,
    purgeBusy: purging,
    ...(metrics === null ? {} : trendScales(charted)),
  });

  const note = hostContainerNote(capabilities);

  return (
    <Inventory
      label="Containers"
      columns={columns}
      rows={charted}
      // The same pair the fleet list keys on, so "what a container row is"
      // has one answer.
      rowKey={(row) => `${row.host_id}:${row.container_key}`}
      // The same rail the fleet's container list draws, from the same
      // function: one container row, one definition of "needs attention".
      rowSeverity={containerSeverity}
      searchText={(row) =>
        [row.container_key, row.name, row.image].filter(Boolean).join(" ")
      }
      notice={
        note === null && purgeError === null ? undefined : (
          <>
            {note === null ? null : <p className="note">{note}</p>}
            {/* A failed purge is reported where the button is, not swallowed:
                the row is still there afterwards and without this the click
                simply appeared to do nothing. */}
            {purgeError === null ? null : (
              <p className="note">Purge failed: {purgeError}</p>
            )}
          </>
        )
      }
      groupBy={BY_PROJECT}
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
  {
    key: "label",
    header: "Label",
    cell: (row) => row.label,
    sortValue: (row) => row.label,
  },
  {
    key: "mountpoint",
    header: "Mountpoint",
    cell: (row) => row.mountpoint ?? ABSENT,
    sortValue: (row) => row.mountpoint ?? null,
  },
  // The three byte columns sort on the RAW bytes, never on the "1.4 TB" the
  // cell prints: those strings collate "999 GB" above "1.4 TB", which is the
  // one thing a size column must not do.
  {
    key: "size",
    header: "Size",
    cell: (row) => bytes(row.total),
    sortValue: (row) => row.total,
  },
  {
    key: "used",
    header: "Used",
    cell: (row) => bytes(row.used),
    sortValue: (row) => row.used,
  },
  {
    key: "free",
    header: "Free",
    cell: (row) => bytes(row.free),
    sortValue: (row) => row.free,
  },
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
        // Wrapped, for the reason .disk-cell and .mem-cell are: Meter brings
        // its own .mrow, a 1fr/92px row with padding and a rule of its own,
        // and all three are wrong inside a <td> that already has them. This
        // cell had no such scope, so every mount's bar sat under a second
        // horizontal rule and its percentage was stranded 92px from the bar
        // it belonged to.
        <div className="usage-cell">
          <Meter
            value={(row.used / (row.used + row.free)) * 100}
            max={100}
            label={row.label}
          />
        </div>
      ),
    // The same Use% the meter is drawn from, computed once here rather than
    // read back off the bar. A mount the metrics have not answered for draws
    // ABSENT and sorts as unknown, not as empty.
    sortValue: (row) =>
      row.used === null || row.free === null || row.used + row.free === 0
        ? null
        : (row.used / (row.used + row.free)) * 100,
  },
];

/**
 * The Storage tab's mount table.
 *
 * Called Mounts rather than Filesystems because it is no longer a tab of its
 * own: the tab is Storage, and it carries this table and the disk charts.
 * Splitting them was the same "the format is not the subject" mistake the
 * Graphs tab made.
 */
export function Mounts({
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
      // "Mounts", not "Filesystems". The Storage tab stacks two tables and
      // then a chart group already called Filesystems, and two identical
      // headings on one page is a page that cannot be scanned. This table is
      // one row per MOUNT -- label, mountpoint, usage -- while the charts
      // below plot the filesystems those mounts sit on.
      label="Mounts"
      heading
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

/**
 * The hub's scope classification, as a pill beside the address it describes.
 *
 * A column of its own put an address and its classification two columns
 * apart, which is two saccades to answer "is this box reachable from the
 * internet". Every scope wears one, not only `private`: with a pill on some
 * rows and nothing on others, "no pill" would mean both "public" and "the hub
 * could not classify this", and those are different facts.
 *
 * public carries the warn hue because a publicly routable address on a host
 * is the notable one -- the others are the quiet default. It is a status
 * TEXT colour, not a fill: the pill is an annotation, not a severity badge.
 *
 * The values come from AddressScope (internal/hub/store/scope.go), which
 * covers RFC 1918, fc00::/7, link-local and 169.254/16. An unrecognised
 * string still renders, in the neutral style -- the hub is free to add a
 * class without this file being taught it first.
 */
function ScopePill({ scope }: { scope: string | null }) {
  if (scope === null) return null;
  return <span className={`badge scope scope-${scope}`}>{scope}</span>;
}

const ADDRESS_COLUMNS: Column<Address>[] = [
  {
    key: "iface",
    header: "Interface",
    cell: (row) => row.iface,
    sortValue: (row) => row.iface,
  },
  {
    key: "address",
    header: "Address",
    cell: (row) => (
      <span className="addr-cell">
        <span className="ident">{row.address}</span>
        <ScopePill scope={row.scope} />
      </span>
    ),
    // The address itself, not the scope pill beside it. Table's string
    // comparison is numeric-aware, which is what keeps 10.0.0.2 above
    // 10.0.0.10 -- a plain lexical sort puts them the other way round.
    sortValue: (row) => row.address,
  },
  {
    key: "family",
    header: "Family",
    cell: (row) => FAMILY_NAME[row.family] ?? String(row.family),
    // The number, so v4 groups before v6. The printed name would order by
    // whatever FAMILY_NAME happens to call them.
    sortValue: (row) => row.family,
  },
  // The Description column moved to the Interfaces table above, and only the
  // column did: Address.description still arrives, and is still searched
  // below, so typing an alias here still finds its addresses.
  //
  // The alias is `ip link set dev eth0 alias "uplink to core-sw1"` -- a fact
  // about the INTERFACE. On an address-keyed table it was printed once per
  // address, so a host with a v4, a v6 and a link-local on eth0 showed the
  // same sentence three times and invited the reader to wonder which address
  // it described. It describes none of them.
  //
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
    // The instant, not the relative phrase When prints -- see the Events
    // tab's When column for the same argument.
    sortValue: (row) => Date.parse(row.first_seen),
  },
  {
    key: "last_seen",
    sortValue: (row) => Date.parse(row.last_seen),
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
      heading
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

// --- Drives ---------------------------------------------------------------

/**
 * The drive's overall state, as a badge on its device name.
 *
 * Same shape as the link-state and scope pills: the thing and its
 * classification in one cell. A healthy drive says so rather than showing
 * nothing -- on a table whose whole point is advance warning, a blank cell and
 * "we checked, it is fine" must not look the same.
 */
function DriveHealthPill({ drive }: { drive: Drive }) {
  const severity = driveSeverity(drive);
  if (drive.attributes.length === 0) {
    // Not a verdict about the hardware: there is nothing to judge. The
    // readings have aged out under retention while the drive's row is still
    // inside the prune's longer horizon, so the Last read column is the only
    // thing left that can speak for it.
    return <span className="badge drive-unread">not read</span>;
  }
  const label = severity === "ok" ? "healthy" : severity;
  return <span className={`badge drive drive-${severity}`}>{label}</span>;
}

/**
 * A drive's temperature series out of the smart family.
 *
 * The family carries EVERY attribute of every drive on the host -- the read
 * API has no key filter -- so the series this row wants is picked by device
 * and attribute id, the same pair the hub keys them on. attr_id arrives as
 * text (s.attr_id::text in read/family.go), so the comparison is made in
 * strings rather than by coercing the response.
 */
function driveTempSeries(
  res: MetricsResponse | null,
  drive: Drive,
): (number | null)[] {
  if (res === null) return [];
  const wanted = String(driveTempAttrId(drive));
  const index = res.series.findIndex(
    (s) => s.key.device === drive.device && s.key.attr_id === wanted,
  );
  if (index === -1) return [];
  // Through the same mask the cell's current reading uses. The hub returns
  // smartctl's raw 48-bit field verbatim, so an unmasked ATA series draws a
  // flat line at a hundred and twenty billion beside a cell reading 28 °C.
  const kind = driveKind(drive);
  return griddedValues(res, index, "raw").map((v) =>
    temperatureFromRaw(v, kind),
  );
}

/**
 * The Temp cell: the current reading, and the movement behind it.
 *
 * A temperature is only interesting as a movement -- 47 °C is fine on an
 * NVMe under load and alarming on an idle spinning disk -- which is the same
 * argument the Overview tab's sensor rows make, and the reason the sparkline
 * belongs in this cell rather than in a panel below the table. This is where
 * the number already is.
 *
 * FREE-SCALED PER ROW, no shared extent. Every drive draws between its own
 * min and max, so a disk moving two degrees still has a shape. The trade is
 * that two rows with identical silhouettes are not at the same temperature,
 * and the reading beside each line is what carries magnitude -- exactly the
 * call SensorList makes, and for the same reason: a shared axis across an
 * NVMe at 47 °C and a platter at 34 flattens both.
 */
function DriveTempCell({
  row,
  values,
  window: answered = null,
  range,
  fetchFamily,
}: {
  row: Drive;
  values: (number | null)[];
  /** The window the values were answered for, for the enlarged view's time
   * axis. Absent, no axis is drawn rather than a guessed one. */
  window?: { from: string; to: string } | null;
  range?: Range;
  fetchFamily?: (family: string, range: Range) => Promise<MetricsResponse>;
}) {
  const now = driveTemperature(row);
  if (now === null) return ABSENT;

  // .val, the name every other cell in the app gives the number beside its
  // chart or meter -- .mem-cell .val, .usage-cell .val, .sensor-row .val.
  const reading = <span className="val">{now} °C</span>;
  // No history is not an error: SMART is hourly, so a drive first seen this
  // hour has a reading and no line yet, and the number is still the answer.
  if (values.length === 0) {
    return <span className="temp-cell">{reading}</span>;
  }

  // The dialog can only refetch where the page gave it a fetcher. Without
  // one it still opens on the data already drawn -- which is the whole point
  // of Enlargeable's fetchSeries being optional -- so the line is never
  // withheld over a range change the caller cannot serve.
  const fetchSeries = async (next: Range): Promise<DetailData> => {
    if (fetchFamily === undefined) return { series: [], window: null };
    const res = await fetchFamily("smart", next);
    // Re-picked by device and attr_id rather than by the index this row had,
    // for the reason SensorList re-picks by name: a drive that stopped
    // reporting shifts every series after it, and the widened chart would
    // silently become another disk's.
    return {
      series: [
        {
          name: `${row.device} temperature`,
          color: "var(--s1)",
          values: driveTempSeries(res, row),
        },
      ],
      window: res.window,
    };
  };

  return (
    <span className="temp-cell">
      <Enlargeable
        title={`Temperature · ${row.device}`}
        label={`Enlarge temperature for ${row.device}`}
        className="inline"
        series={[
          { name: `${row.device} temperature`, color: "var(--s1)", values },
        ]}
        // Free-scaled in the dialog too, matching the sparkline that was
        // clicked. A zero floor draws a drive living between 37 and 49 °C as
        // a flat line across the top quarter of the chart, which is a
        // strictly worse picture than the 110px cell it was opened from.
        autoScale
        // Filled, like the Sparkline it was opened from, and for the reason
        // the Overview sensor rows pass it: the small chart draws an area, so
        // a dialog drawing a bare line would say less than the 110px cell.
        // Honest because the chart free-scales -- the fill's bottom edge is
        // the coolest reading in the window, not an axis decision.
        filled
        fmt={(n) => (n === null ? ABSENT : `${Math.round(n)} °C`)}
        window={answered}
        range={range}
        ranges={RANGE_VALUES}
        fetchSeries={fetchFamily === undefined ? undefined : fetchSeries}
      >
        <Sparkline
          values={values}
          color="var(--s1)"
          width={110}
          height={24}
          label={
            range === undefined
              ? `${row.device} temperature trend`
              : `${row.device} temperature trend, ${rangeLabel(range)}`
          }
        />
      </Enlargeable>
      {reading}
    </span>
  );
}

function driveColumns(
  metrics: MetricsResponse | null,
  range?: Range,
  fetchFamily?: (family: string, range: Range) => Promise<MetricsResponse>,
): Column<Drive>[] {
  return DRIVE_COLUMNS.map((column) =>
    column.key === "temperature"
      ? {
          ...column,
          // Just "Temp". The line covers the range chosen at the top of the
          // page like every other chart on it, so a header saying "hourly"
          // described the sampling cadence in the one place a reader looks
          // for what is being shown -- and at the 24h range what is shown is
          // 24 hours. The sampling cadence is not a column heading.
          header: "Temp",
          // Wide enough for the line at the size it was drawn plus the
          // reading. Charts shrink to fit rather than widening the page
          // (svg.spark, max-width:100%), so without a width of its own the
          // column takes the share the table hands it -- and the model and
          // serial beside it are long. The line came out 40px wide and
          // unreadable, which is a chart in name only.
          width: "190px",
          cell: (row: Drive) => (
            <DriveTempCell
              row={row}
              values={driveTempSeries(metrics, row)}
              window={metrics?.window ?? null}
              range={range}
              fetchFamily={fetchFamily}
            />
          ),
        }
      : column,
  );
}

const DRIVE_COLUMNS: Column<Drive>[] = [
  {
    key: "device",
    header: "Drive",
    cell: (row) => (
      <span className="addr-cell">
        <span className="ident">{row.device}</span>
        <DriveHealthPill drive={row} />
      </span>
    ),
    // The device name, not the health pill beside it -- Findings is the
    // column for ordering by state.
    sortValue: (row) => row.device,
  },
  {
    key: "model",
    header: "Model",
    cell: (row) => row.model ?? ABSENT,
    sortValue: (row) => row.model ?? null,
  },
  {
    key: "serial",
    header: "Serial",
    cell: (row) =>
      row.serial === null ? (
        ABSENT
      ) : (
        <span className="ident">{row.serial}</span>
      ),
    sortValue: (row) => row.serial ?? null,
  },
  {
    key: "temperature",
    header: "Temp",
    align: "right",
    cell: (row) => {
      const c = driveTemperature(row);
      return c === null ? ABSENT : `${c} °C`;
    },
    // Degrees, so the order survives driveColumns() swapping this cell for
    // the sparkline version: that spread keeps everything it does not
    // override, and the reading is the same either way.
    sortValue: (row) => driveTemperature(row),
  },
  {
    key: "power_on",
    header: "Power on",
    align: "right",
    // Hours as the drive counts them, rendered as a duration: 43800 is a
    // number nobody converts in their head, and "5 y" is the fact -- this
    // disk has been spinning for five years.
    cell: (row) => {
      const hours = drivePowerOnHours(row);
      return hours === null ? ABSENT : duration(hours * 3600);
    },
    // Hours, not the "5 y" the cell prints: duration() rounds to a unit, so
    // ordering on its string would tie every drive between five and six
    // years and then break the tie alphabetically.
    sortValue: (row) => drivePowerOnHours(row),
  },
  {
    key: "wear",
    header: "Wear",
    align: "right",
    // NVMe only, and absent rather than guessed elsewhere -- see driveWearPct
    // for why an ATA drive has no comparable figure.
    cell: (row) => {
      const pct = driveWearPct(row);
      return pct === null ? ABSENT : `${pct}%`;
    },
    // An ATA drive has no figure here, so it sorts as unknown rather than as
    // a drive with no wear on it.
    sortValue: (row) => driveWearPct(row),
  },
  {
    key: "last_seen",
    // The staleness cue the health pill cannot give.
    //
    // When this drive's newest reading was taken.
    //
    // Every other cell in the row -- temperature, wear, findings -- is the
    // newest reading rendered as a current fact. Nothing else on the row says
    // how old that is, and a host whose agent stopped an hour ago looks
    // exactly like one reporting now.
    //
    // devices.last_seen rather than the newest attribute's own ts: the two are
    // the same instant, but smart_attributes is dropped at 90 days by its
    // retention policy and the devices row outlives it, so a drive whose
    // readings have aged out still has a date rather than a dash.
    header: "Last read",
    cell: (row) => <When iso={row.last_seen} />,
    sortValue: (row) => Date.parse(row.last_seen),
  },
  {
    key: "findings",
    header: "Findings",
    // By SEVERITY, not by the text of the first finding: this column exists
    // so a reader can pull the drives in trouble to one end of the table, and
    // "3 pending sectors" alphabetises next to nothing useful. A drive with
    // no findings still ranks (as ok) rather than sorting away as unknown --
    // "we checked, it is fine" is a reading here, which is the same argument
    // DriveHealthPill makes about not leaving a healthy drive blank.
    sortValue: (row) => driveSeverityRank(row),
    // The reason the table exists. Every finding, worst first, in the words an
    // operator would use -- "3 pending sectors", not "attribute 197 = 3".
    cell: (row) => {
      const findings = driveFindings(row);
      if (findings.length === 0) return ABSENT;
      return (
        <span className="findings">
          {findings.map((f) => (
            <span key={f.text} className={`finding finding-${f.severity}`}>
              {f.text}
            </span>
          ))}
        </span>
      );
    },
  },
];

/**
 * The host's physical disks and what SMART says about them.
 *
 * netra has collected SMART since the agent was written -- the hub stores it
 * on its own hypertable with 90-day retention and serves it as a metric family
 * -- and until this table nothing rendered any of it. The only thing the UI
 * said about disks was the Device availability panel, which reads
 * collector_samples.ok: whether smartctl RAN, not what it found. A drive
 * reporting reallocated sectors and a drive in perfect health looked
 * identical.
 *
 * Rows carry a severity rail as well as a badge, because severity riding on
 * colour alone fails the reader who cannot see it -- the same rule the fleet
 * list follows.
 */
export function Drives({
  rows,
  capabilities,
  metrics = null,
  range,
  fetchFamily,
}: {
  rows: readonly Drive[];
  capabilities?: Record<string, string>;
  /** The smart family, for the temperature column's history. Optional: the
   * table is a complete answer without it, and was one before the column
   * could draw. */
  metrics?: MetricsResponse | null;
  range?: Range;
  fetchFamily?: (family: string, range: Range) => Promise<MetricsResponse>;
}) {
  // Above the list rather than instead of it, exactly as Containers does:
  // "what was collected" and "what stopped the rest" are two facts, and a
  // drive that answered does not make the one that could not be scanned
  // stop mattering.
  const note = hostDriveNote(capabilities);

  return (
    <Inventory
      label="Drives"
      heading
      columns={driveColumns(metrics, range, fetchFamily)}
      rows={rows}
      rowKey={(row) => row.device}
      searchText={(row) =>
        [
          row.device,
          row.model,
          row.serial,
          ...driveFindings(row).map((f) => f.text),
        ]
          .filter(Boolean)
          .join(" ")
      }
      rowSeverity={(row) => {
        const severity = driveSeverity(row);
        return severity === "ok" ? null : severity;
      }}
      notice={note === null ? undefined : <p className="note">{note}</p>}
      emptyTitle="No drives reported"
      // The plain fact only. Why it is empty now comes from the agent's own
      // capability, in the notice above -- which also fires on a list that
      // has rows in it, where an empty-state body never would.
      emptyBody="No drive on this host has reported SMART data."
    />
  );
}

// --- Interfaces -----------------------------------------------------------

/**
 * The link state, as a pill on the interface name.
 *
 * The kernel's own word, uppercased by nothing: "lowerlayerdown" is rendered
 * "lower down" because that is the same fact in the width a table column has,
 * and every other value is short enough to print as-is.
 *
 * up is green and down/lowerlayerdown red, from the FIXED status palette --
 * this is genuinely a health reading and not a category. "unknown" is muted
 * and is neither: it is what a virtual device reports (wg0, lo, a bridge),
 * every time, and colouring it as a problem would paint a healthy host's
 * loopback red forever.
 */
const LINK_STATE_LABEL: Record<string, string> = {
  lowerlayerdown: "lower down",
};

function LinkStatePill({ state }: { state: string | null }) {
  if (state === null) return null;
  const bad = state === "down" || state === "lowerlayerdown";
  const cls = state === "up" ? "link-up" : bad ? "link-down" : "link-unknown";
  return (
    <span className={`badge link ${cls}`}>
      {LINK_STATE_LABEL[state] ?? state}
    </span>
  );
}

const INTERFACE_COLUMNS: Column<Iface>[] = [
  {
    key: "iface",
    // The state rides the name, the way the scope pill rides the address:
    // one cell carrying a thing and its classification, rather than two
    // columns the eye has to pair up. It also makes the down links findable
    // by scanning a single column.
    header: "Interface",
    cell: (row) => (
      <span className="addr-cell">
        <span className="ident">{row.iface}</span>
        <LinkStatePill state={row.oper_state} />
      </span>
    ),
    // The name, not the link state riding on it. Ordering this column by
    // state would make the one column a reader scans for a device name stop
    // being alphabetical, which is what it is scanned for.
    sortValue: (row) => row.iface,
  },
  {
    key: "speed",
    header: "Speed",
    align: "right",
    // Absent, not "0 Mb/s". A virtual device has no link speed and a down one
    // refuses to report its, and both arrive as null by the collector's own
    // decision (ifaceSpeed in addresses.go) -- printing a zero would put an
    // unplugged NIC and a wg0 in the same bucket as a 10 Gb link that is
    // somehow idle.
    cell: (row) =>
      row.speed_mbps === null ? ABSENT : linkSpeed(row.speed_mbps),
    // Megabits, not the "10 Gb/s" the cell prints: linkSpeed picks a unit, so
    // the string sorts "1 Gb/s" above "100 Mb/s". A device with no speed to
    // report stays unknown rather than sorting in at zero -- the same call
    // the cell makes about not printing one.
    sortValue: (row) => row.speed_mbps,
  },
  {
    key: "duplex",
    header: "Duplex",
    cell: (row) => row.duplex ?? ABSENT,
    sortValue: (row) => row.duplex ?? null,
  },
  {
    key: "mtu",
    header: "MTU",
    align: "right",
    cell: (row) => (row.mtu === null ? ABSENT : String(row.mtu)),
    // The number, so 9000 sorts above 1500 rather than below it.
    sortValue: (row) => row.mtu,
  },
  {
    key: "mac",
    header: "MAC",
    cell: (row) =>
      row.mac === null ? ABSENT : <span className="ident">{row.mac}</span>,
    sortValue: (row) => row.mac ?? null,
  },
  {
    key: "description",
    header: "Description",
    cell: (row) => row.description ?? ABSENT,
    sortValue: (row) => row.description ?? null,
  },
  {
    key: "last_seen",
    // "Last changed", for the reason the addresses table's column says so:
    // the collector reports on change only, so a healthy host that keeps its
    // links stops touching these rows and the timestamp drifts into the past
    // the longer it stays healthy.
    header: "Last changed",
    cell: (row) => <When iso={row.last_seen} />,
    sortValue: (row) => Date.parse(row.last_seen),
  },
];

/**
 * Mbit/s as the operator reads it: 1000 is a gigabit link, not "1000".
 *
 * Decimal, not binary, and that is not a slip -- link speeds are decimal by
 * definition (a "1 Gb/s" NIC is 10^9 bits), unlike the byte counts elsewhere
 * on this page.
 */
function linkSpeed(mbps: number): string {
  if (mbps >= 1000 && mbps % 1000 === 0) return `${mbps / 1000} Gb/s`;
  return `${mbps} Mb/s`;
}

/**
 * The links, above the addresses on them.
 *
 * A separate table rather than more columns on Addresses, because an
 * interface with NO address is exactly what an operator is looking for -- a
 * failed bond, an unplugged spare NIC -- and an address-keyed table cannot
 * hold one. Before this it appeared nowhere in netra at all.
 */
export function Interfaces({ rows }: { rows: readonly Iface[] }) {
  return (
    <Inventory
      label="Interfaces"
      heading
      columns={INTERFACE_COLUMNS}
      rows={rows}
      rowKey={(row) => row.iface}
      searchText={(row) =>
        [row.iface, row.oper_state, row.duplex, row.mac, row.description]
          .filter(Boolean)
          .join(" ")
      }
    />
  );
}

// --- Packages -------------------------------------------------------------

const PACKAGE_COLUMNS: Column<Pkg>[] = [
  {
    key: "name",
    header: "Name",
    cell: (row) => row.name,
    sortValue: (row) => row.name,
  },
  {
    key: "version",
    header: "Version",
    cell: (row) => row.version,
    // Table's numeric-aware string compare, which gets "1.9" under "1.10"
    // right and makes no claim to understand an epoch or a "~rc1" suffix.
    // Ordering Debian versions properly is dpkg's job, not a column's.
    sortValue: (row) => row.version,
  },
  {
    key: "arch",
    header: "Arch",
    cell: (row) => row.arch,
    sortValue: (row) => row.arch,
  },
  {
    key: "format",
    header: "Format",
    cell: (row) => row.format,
    sortValue: (row) => row.format,
  },
  {
    key: "size",
    header: "Size",
    align: "right",
    cell: (row) => bytes(row.size_bytes),
    // Bytes, not the formatted string -- see the filesystem table's own size
    // columns for what that costs.
    sortValue: (row) => row.size_bytes,
  },
  {
    key: "first_seen",
    header: "First seen",
    cell: (row) => <When iso={row.first_seen} />,
    sortValue: (row) => Date.parse(row.first_seen),
  },
  {
    key: "last_seen",
    header: "Last seen",
    cell: (row) => <When iso={row.last_seen} />,
    sortValue: (row) => Date.parse(row.last_seen),
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
  {
    key: "unit",
    header: "Unit",
    cell: (row) => row.unit_name,
    sortValue: (row) => row.unit_name,
  },
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
    // The state's own word, not the severity the badge is tinted from:
    // UNIT_SEVERITY collapses several states onto one tint, so ordering by it
    // would shuffle rows a reader can see are different. This list is already
    // filtered to what needs attention and is short, so alphabetical is
    // enough to bring the failed units together.
    sortValue: (row) => row.state ?? null,
  },
  {
    key: "substate",
    header: "Substate",
    cell: (row) => row.substate ?? ABSENT,
    sortValue: (row) => row.substate ?? null,
  },
  {
    // The reason a unit that reads active/running is in this table at all. A
    // service that runs a few minutes, dies and comes back is healthy at
    // nearly every scrape, so without this column its row looks like a
    // mistake -- a green badge in a list of things that need attention.
    key: "restarts",
    header: "Restarts (1h)",
    cell: (row) =>
      row.restarts_1h >= FLAP_THRESHOLD ? (
        <Badge severity="warning">{row.restarts_1h}</Badge>
      ) : (
        row.restarts_1h
      ),
    // The count, badge or no badge: the threshold decides how the number is
    // drawn, not how it orders.
    sortValue: (row) => row.restarts_1h,
  },
  {
    key: "since",
    header: "Since",
    cell: (row) => <When iso={row.since} />,
    // Nullable, unlike the other timestamps in these tables: a unit systemd
    // has no state-change instant for has no age, and Date.parse(null!) would
    // sort it in as NaN rather than as the unknown it is.
    sortValue: (row) => (row.since === null ? null : Date.parse(row.since)),
  },
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
      emptyTitle="Nothing needs attention"
      emptyBody="Every systemd service on this host is running normally. Units appear here when one fails or falls into a restart loop."
    />
  );
}
