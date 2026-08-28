// Host detail: a header that never changes across tabs, then the tab bar,
// then the active tab. This is the only file in the feature that talks to
// the read API -- the tabs take plain props, so a tab renders identically
// whether its data came from a fetch, a poll or a fixture.
import { useCallback, useEffect, useState } from "react";
import {
  getAddresses,
  getContainers,
  getEvents,
  getFilesystems,
  getDrives,
  getHost,
  getInterfaces,
  getMetrics,
  getPackages,
  getUnits,
  type Address,
  type Container,
  type Drive,
  type Event,
  type Filesystem,
  type HostDetail,
  type Iface,
  type MetricsResponse,
  type Pkg,
  type Unit,
} from "../../lib/api";
import { Badge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { Segmented } from "../../ui/Segmented";
import { Tabs } from "../../ui/Tabs";
import { ABSENT, duration, relative } from "../../lib/format";
import { hostStatus } from "../../lib/host";
import {
  clampRange,
  rangeWindow,
  EVENT_LIMITS,
  type Range,
} from "../../lib/range";
import { loadRange } from "../settings/SettingsPage";
import { RANGE_OPTIONS, RANGE_VALUES } from "./ranges";
import { Events } from "./tabs/Events";
import { NetworkGraphs, StorageGraphs, SystemGraphs } from "./tabs/Graphs";
import { CollectorsTab } from "./tabs/CollectorsTab";
import { LimitsCard } from "./tabs/LimitsCard";
import {
  Containers,
  Drives,
  Interfaces,
  Mounts,
  Network,
  Packages,
  Units,
} from "./tabs/Inventory";
import { Overview } from "./tabs/Overview";

export type HostTab =
  | "overview"
  | "system"
  | "network"
  | "storage"
  | "containers"
  | "packages"
  | "units"
  | "collectors"
  | "events";

/** The bar is built to take a tenth tab (Alerts, with the Stage 2 engine)
 * without relayout; it wraps at narrow widths rather than scrolling, and
 * there is deliberately no overflow menu -- a hidden tab is an unused tab.
 *
 * There is no Graphs tab. It named a RENDERING FORMAT rather than a subject,
 * and the cost was that every subject was split across two tabs: Network held
 * the address table while every network chart lived in Graphs, and
 * Filesystems held the mount table while the disk charts lived in Graphs.
 * Answering "what is this box's network doing" meant visiting both and
 * knowing which half was where.
 *
 * So the charts moved to the tab that owns the subject, Filesystems widened
 * into Storage (the mounts and the disks are one subject, and keeping them
 * apart would rebuild the split being removed), and the count is unchanged.
 * parseRoute still answers the old /graphs and /filesystems URLs. */
export const HOST_TABS: readonly { id: HostTab; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "system", label: "System" },
  { id: "network", label: "Network" },
  { id: "storage", label: "Storage" },
  { id: "containers", label: "Containers" },
  { id: "packages", label: "Packages" },
  { id: "units", label: "Units" },
  // Between the inventories and the log, at the quiet end of the bar: it is
  // the tab you open when you doubt what the other tabs are telling you.
  { id: "collectors", label: "Collectors" },
  { id: "events", label: "Events" },
];

/**
 * Every tab is a URL. Wave 5 owns the router; this function owns the shape
 * of the path, so the router and the tab bar cannot disagree about it.
 */
export function hostTabHref(hostId: number | string, tab: HostTab): string {
  return `/hosts/${hostId}/${tab}`;
}

// The windows this page OFFERS live in ./ranges -- the Graphs tab needs them
// too, and importing them from here would be a cycle. Re-exported because
// the screen above clamps a remembered range to this set before fetching:
// clamping inside would leave the fetch on 30d while the toolbar showed 7d.
export { RANGE_OPTIONS, RANGE_VALUES };

interface TabData {
  hostMetrics: MetricsResponse | null;
  hostSnmpMetrics: MetricsResponse | null;
  hostProtoMetrics: MetricsResponse | null;
  filesystemMetrics: MetricsResponse | null;
  agentMetrics: MetricsResponse | null;
  sensorMetrics: MetricsResponse | null;
  netMetrics: MetricsResponse | null;
  diskIoMetrics: MetricsResponse | null;
  smartMetrics: MetricsResponse | null;
  collectorMetrics: MetricsResponse | null;
  coreMetrics: MetricsResponse | null;
  containerMetrics: MetricsResponse | null;
  containers: Container[] | null;
  filesystems: Filesystem[] | null;
  drives: Drive[] | null;
  addresses: Address[] | null;
  interfaces: Iface[] | null;
  packages: Pkg[] | null;
  units: Unit[] | null;
  events: Event[] | null;
}

const NO_DATA: TabData = {
  hostMetrics: null,
  hostSnmpMetrics: null,
  hostProtoMetrics: null,
  filesystemMetrics: null,
  agentMetrics: null,
  sensorMetrics: null,
  netMetrics: null,
  diskIoMetrics: null,
  smartMetrics: null,
  collectorMetrics: null,
  coreMetrics: null,
  containerMetrics: null,
  containers: null,
  filesystems: null,
  drives: null,
  addresses: null,
  interfaces: null,
  packages: null,
  units: null,
  events: null,
};

/**
 * A per-request failure yields null instead of rejecting the whole load.
 * One family the hub cannot answer must not blank the seven panels beside
 * it -- and a null reaches the tab as "nothing to draw", which is exactly
 * what it means. A failure of the host detail itself is different: that
 * one is surfaced as an error, because without it there is no page.
 */
function orNull<T>(p: Promise<T>): Promise<T | null> {
  return p.then(
    (v) => v,
    () => null,
  );
}

export interface HostPageProps {
  hostId: number | string;
  /** The active tab, from the URL. Wave 5 builds the router; this page
   * only reports which tab was asked for and never navigates itself. */
  tab: HostTab;
  onTabChange: (tab: HostTab) => void;
  /** The range and its setter, when a screen owns them. Given both, this
   * page is controlled: the choice lives in the URL and in the remembered
   * preference, so it survives leaving the page. Given neither -- the
   * standalone case -- it keeps its own, starting at the remembered
   * default. Half a pair is a value that cannot change, so the setter is
   * what decides. */
  range?: Range;
  onRangeChange?: (range: Range) => void;
}

// A host that came up inside the last five minutes is the most interesting
// thing its header can say -- an unannounced reboot explains gaps in every
// chart below it -- so it carries the warning severity. The same threshold
// the fleet list's Uptime cell used before that column was removed.
const RECENT_BOOT_S = 300;

export function HostPage({
  hostId,
  tab,
  onTabChange,
  range: controlledRange,
  onRangeChange,
}: HostPageProps) {
  const [host, setHost] = useState<HostDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  // The time range is shared state across tabs: switching tabs never resets
  // it. It used to be held here and hardcoded to "6h", which meant this page
  // had never heard of the range you picked on the fleet and Settings'
  // stored default did nothing at all. Controlled, it now lives one level up
  // -- in the URL and in the preference -- and the guarantee about tabs is
  // unchanged, since it still sits above the tab body either way.
  const [localRange, setLocalRange] = useState<Range>(
    () => controlledRange ?? clampRange(loadRange(), RANGE_VALUES),
  );
  const range = onRangeChange ? (controlledRange ?? localRange) : localRange;
  const setRange = onRangeChange ?? setLocalRange;
  const [data, setData] = useState<TabData>(NO_DATA);
  const [reloads, setReloads] = useState(0);

  // One family at one range, for an enlarged chart that wants a different
  // window from the page's. It resolves the window exactly the way the tab
  // load below does -- rangeWindow, then getMetrics -- so a widened dialog
  // and a widened page ask the hub the same question.
  //
  // Unlike the tab load it does NOT swallow failures into null: the dialog
  // has a chart on screen already and can say the new range failed to load,
  // where a tab with nothing yet can only render the absence.
  //
  // useCallback on hostId alone: it is passed to Graphs, which hands it to
  // every panel, and a new identity per render would restart the fetch
  // effect inside each open dialog.
  const fetchFamily = useCallback(
    (family: string, next: Range) => {
      const window = rangeWindow(next);
      return getMetrics(hostId, {
        family,
        from: window.from,
        to: window.to,
        step: window.step,
      });
    },
    [hostId],
  );

  useEffect(() => {
    let cancelled = false;
    // A different host is a different page: without this, the previous
    // host's inventory would sit under the new host's name until its own
    // fetch lands, which is the one kind of wrong netra must never be.
    setHost(null);
    setData(NO_DATA);
    getHost(hostId).then(
      (h) => {
        if (!cancelled) setHost(h);
      },
      (e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      },
    );
    return () => {
      cancelled = true;
    };
  }, [hostId, reloads]);

  useEffect(() => {
    let cancelled = false;
    const window = rangeWindow(range);
    const metrics = (family: string) =>
      orNull(
        getMetrics(hostId, {
          family,
          from: window.from,
          to: window.to,
          step: window.step,
        }),
      );

    async function load(): Promise<Partial<TabData>> {
      switch (tab) {
        case "overview": {
          const [
            hostMetrics,
            filesystemMetrics,
            agentMetrics,
            sensorMetrics,
            containers,
            units,
            coreMetrics,
            netMetrics,
          ] = await Promise.all([
            metrics("host"),
            metrics("filesystem"),
            metrics("agent"),
            metrics("sensor"),
            orNull(getContainers(hostId)),
            orNull(getUnits(hostId)),
            // The headline Processor chart is a per-core stack, the same one
            // the fleet row for this host draws.
            metrics("cpu_core"),
            // Traffic: the overview summarised every subsystem except the
            // one most likely to explain a problem.
            metrics("net"),
          ]);
          return {
            hostMetrics,
            filesystemMetrics,
            agentMetrics,
            sensorMetrics,
            containers,
            units,
            coreMetrics,
            netMetrics,
          };
        }
        // Each subject tab fetches the families ITS panels draw, rather than
        // all nine. The Graphs tab fetched everything because it drew
        // everything; a Storage tab pulling the ICMP MIB would be paying for
        // a panel it does not have.
        case "system": {
          // No collector or agent family any more: the four panels that read
          // them are the Collectors tab's now, and this tab would be paying
          // for two requests nothing on it draws. hostMetrics is what the
          // Limits card reads, and it was already being fetched.
          const [hostMetrics, coreMetrics] = await Promise.all([
            metrics("host"),
            metrics("cpu_core"),
          ]);
          return {
            hostMetrics,
            coreMetrics,
          };
        }
        case "collectors": {
          const [collectorMetrics, agentMetrics] = await Promise.all([
            metrics("collector"),
            metrics("agent"),
          ]);
          return {
            collectorMetrics,
            agentMetrics,
          };
        }
        case "network": {
          const [
            addresses,
            interfaces,
            hostMetrics,
            hostSnmpMetrics,
            hostProtoMetrics,
            netMetrics,
          ] = await Promise.all([
            orNull(getAddresses(hostId)),
            // orNull, like every other family: a hub too old to serve this
            // route leaves the table empty and the rest of the tab intact.
            orNull(getInterfaces(hostId)),
            metrics("host"),
            // The IP and ICMP MIBs, and the TCP/UDP volume counters, live in
            // their own families because they live in their own tables -- a
            // continuous aggregate cannot gain a column. See
            // 0003_host_proto_samples.sql. The fragmentation panels read
            // host and host_snmp TOGETHER, which is why both are here.
            metrics("host_snmp"),
            metrics("host_proto"),
            metrics("net"),
          ]);
          return {
            addresses,
            interfaces,
            hostMetrics,
            hostSnmpMetrics,
            hostProtoMetrics,
            netMetrics,
          };
        }
        case "storage": {
          // The inventory row carries a label, a mountpoint and a device id
          // and nothing else -- size, used and free live in the metrics
          // family. Fetching both is what makes this tab answer the question
          // anyone opens it for: how full is that disk.
          const [
            filesystems,
            drives,
            filesystemMetrics,
            diskIoMetrics,
            smartMetrics,
          ] = await Promise.all([
            orNull(getFilesystems(hostId)),
            // The physical disks under those mounts. orNull, like every
            // other family: a hub too old to serve the route leaves the
            // table empty and the rest of the tab intact.
            orNull(getDrives(hostId)),
            metrics("filesystem"),
            metrics("disk_io"),
            // The SMART readings over time, which is what turns the Drives
            // table's temperature from a number into a movement. The family
            // carries every attribute of every drive -- the read API takes no
            // key filter -- but at an hourly cadence that is a few hundred
            // points, and it is the only route to this history.
            metrics("smart"),
          ]);
          return {
            filesystems,
            drives,
            filesystemMetrics,
            diskIoMetrics,
            smartMetrics,
          };
        }
        case "containers": {
          // The list and its metrics, so the tab can show what each
          // container is doing rather than only that it exists.
          const [containers, containerMetrics] = await Promise.all([
            orNull(getContainers(hostId)),
            metrics("container"),
          ]);
          return { containers, containerMetrics };
        }
        case "packages":
          return { packages: await orNull(getPackages(hostId)) };
        case "units":
          return { units: await orNull(getUnits(hostId)) };
        case "events":
          return {
            events: await orNull(
              getEvents({
                host: hostId,
                since: window.from,
                until: window.to,
                // The same table the fleet log uses. A flat 500 is fine at
                // 1h and a silent truncation at 7d, which this page offers:
                // one host's dist-upgrade plus a week of ordinary churn
                // reaches it, and the rows lost are the oldest -- exactly the
                // ones someone widening the window is looking for.
                limit: EVENT_LIMITS[range],
              }),
            ),
          };
      }
    }

    load().then((loaded) => {
      // A response that arrived after the tab or the range changed is
      // about a question nobody is asking any more.
      if (!cancelled) setData((prev) => ({ ...prev, ...loaded }));
    });

    return () => {
      cancelled = true;
    };
  }, [hostId, tab, range, reloads]);

  const refresh = useCallback(() => setReloads((n) => n + 1), []);

  if (error !== null) {
    return (
      <div className="hostpage">
        <p className="note">This host could not be loaded: {error}</p>
        <Button variant="secondary" onClick={refresh}>
          Try again
        </Button>
      </div>
    );
  }

  if (host === null) {
    return (
      <div className="hostpage">
        <p className="note">Loading…</p>
      </div>
    );
  }

  const status = hostStatus(host);
  // Only while the host is still answering. uptime_s is host_current's LAST
  // REPORTED value, not a live clock: a machine that booted and then died
  // stays at "40 s" in the database forever, and rendering that as "rebooted
  // 40 s ago" beside an "offline" badge is a stale reading dressed as a fresh
  // one -- the same absent-as-a-fact inversion this warning exists to expose.
  // A host the hub is not hearing from has no current uptime to state.
  const recentlyBooted =
    status.severity !== "critical" &&
    host.uptime_s !== null &&
    host.uptime_s < RECENT_BOOT_S;

  return (
    // "hostpage", NOT "host": .host is the host CELL -- a flex row with a
    // gap, used in the fleet and container lists -- and wearing it here laid
    // the header, the tab bar and the entire small-multiples grid out side
    // by side in a 359px strip down the right of the page.
    <div className="hostpage">
      {/* The header is identical on every tab -- it is what you are
          looking at, not what you are looking at it through. */}
      <header className="hosthead" aria-label="Host summary">
        <h1 className="serif">{host.hostname}</h1>
        {/* The site alone. OS, kernel and arch used to sit here too, and all
            three are printed a few centimetres below in the Overview tab's
            System card, which owns them -- the header was spending its most
            prominent line restating the card.  The site is the one of the four
            that appears nowhere else on host detail, so it stays.

            Rendered conditionally rather than as `?? ABSENT`: on an unsited
            host that put a lone em dash under the hostname with nothing beside
            it to say what was missing, which reads as a rendering fault rather
            than as "no site". .hosthead is a wrapping flex row, so the badge
            simply takes the space back. */}
        {host.site_name !== null && (
          <span className="meta">{host.site_name}</span>
        )}
        <Badge severity={status.severity}>{status.label}</Badge>
        {/* Beside the reporting status, not instead of it: the two answer
            different questions, and a host can be online AND four minutes
            into a boot it did not announce. This warning used to live in the
            fleet list's Uptime cell; when that column was removed the comment
            left behind claimed it had moved to "the header's own status",
            which was not true of any code -- hostStatus() has no reboot
            branch. This is that warning, restored where the comment said it
            was.

            "rebooted", not the duration alone. A chip's tint says nothing to
            a screen reader, so one hearing "1 m 40 s" cannot tell this host
            from one up for "266 d 6 h", and a deuteranope sees only a hue
            change -- the state would ride on colour alone, which is precisely
            what putting a WORD in the chip prevents. A duration is not a
            severity. */}
        {recentlyBooted && (
          <Badge severity="warning">
            rebooted {duration(host.uptime_s)} ago
          </Badge>
        )}
        <span className="meta">
          last seen{" "}
          {host.last_seen === null ? ABSENT : relative(host.last_seen)}
        </span>
        {/* One range control for the whole page: every chart on every tab
            is drawn from it, so no tab may grow one of its own. */}
        <Segmented
          options={RANGE_OPTIONS}
          value={range}
          onChange={(value) => setRange(value)}
        />
        <Button variant="ghost" onClick={refresh}>
          Refresh
        </Button>
      </header>

      <Tabs
        items={HOST_TABS.map((item) => ({
          id: item.id,
          label: item.label,
          href: hostTabHref(hostId, item.id),
        }))}
        active={tab}
        onChange={(id) => onTabChange(id as HostTab)}
      />

      {tab === "overview" && (
        <Overview
          host={host}
          hostMetrics={data.hostMetrics}
          filesystemMetrics={data.filesystemMetrics}
          agentMetrics={data.agentMetrics}
          sensorMetrics={data.sensorMetrics}
          coreMetrics={data.coreMetrics}
          netMetrics={data.netMetrics}
          containers={data.containers}
          units={data.units}
          range={range}
          fetchFamily={fetchFamily}
        />
      )}
      {tab === "system" && (
        <>
          <SystemGraphs
            host={data.hostMetrics}
            cpuCore={data.coreMetrics}
            range={range}
            fetchFamily={fetchFamily}
          />
          {/* At the FOOT of the tab, below the charts. Four bars that do not
              move on a healthy host are not what this tab opens with -- the
              reader came for what CPU, memory and the kernel have been doing,
              and the ceilings are the reference they read those against. It
              renders nothing at all on a host whose agent reports no limits.
          */}
          <LimitsCard host={host} hostMetrics={data.hostMetrics} />
        </>
      )}
      {tab === "collectors" && (
        <CollectorsTab
          host={host}
          sources={{
            collector: data.collectorMetrics,
            agent: data.agentMetrics,
            range,
            fetchFamily,
          }}
        />
      )}
      {tab === "containers" && (
        <Containers
          rows={data.containers ?? []}
          // The id AND the name: the rows link to
          // /containers/{host_id}/{key}, and this component is the only
          // party that has both. Without them the tab could not offer a
          // link at all, which is exactly what it did for a long time.
          // last_seen too: a container is "gone" only when its HOST kept
          // reporting and it stopped, so the tab cannot decide that without
          // the host's own clock. See containerIsGone.
          host={{
            id: host.id,
            hostname: host.hostname,
            last_seen: host.last_seen,
          }}
          metrics={data.containerMetrics ?? null}
          range={range}
          // A purged container is gone from the inventory, so the page
          // refetches rather than editing a copy of the list it does not own.
          onPurged={refresh}
          // Why the list is empty, or why its rows are named after 64 hex
          // digits. The agent is the only party that knows, and it said so.
          capabilities={host.capabilities}
        />
      )}
      {/* Tables first on both of these, then the charts.
          What EXISTS, then how it behaves: the table reads instantly at any
          window height, and it is also the shorter answer -- a reader who
          came to check an MTU or a mount point is done without scrolling
          past six charts to reach it. */}
      {tab === "network" && (
        <>
          <Interfaces rows={data.interfaces ?? []} />
          <Network rows={data.addresses ?? []} />
          <NetworkGraphs
            host={data.hostMetrics}
            hostSnmp={data.hostSnmpMetrics}
            hostProto={data.hostProtoMetrics}
            net={data.netMetrics}
            range={range}
            fetchFamily={fetchFamily}
          />
        </>
      )}
      {tab === "storage" && (
        <>
          {/* Mounts first: "how full is it" is what people open this tab
              for. Drives is the hardware under those mounts, and reads as
              the answer to a question the first table has just raised. */}
          <Mounts
            rows={data.filesystems ?? []}
            metrics={data.filesystemMetrics ?? null}
          />
          <Drives
            rows={data.drives ?? []}
            capabilities={host.capabilities}
            metrics={data.smartMetrics}
            range={range}
            fetchFamily={fetchFamily}
          />
          <StorageGraphs
            filesystem={data.filesystemMetrics}
            diskIo={data.diskIoMetrics}
            range={range}
            fetchFamily={fetchFamily}
          />
        </>
      )}
      {tab === "packages" && <Packages rows={data.packages ?? []} />}
      {tab === "units" && <Units rows={data.units ?? []} />}
      {tab === "events" && <Events events={data.events} />}
    </div>
  );
}
