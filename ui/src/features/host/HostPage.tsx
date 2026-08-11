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
  getHost,
  getMetrics,
  getPackages,
  getUnits,
  type Address,
  type Container,
  type Event,
  type Filesystem,
  type HostDetail,
  type MetricsResponse,
  type Pkg,
  type Unit,
} from "../../lib/api";
import { Badge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { Segmented } from "../../ui/Segmented";
import { Tabs } from "../../ui/Tabs";
import { ABSENT, relative } from "../../lib/format";
import { hostStatus } from "../../lib/host";
import { rangeWindow, type Range } from "../../lib/range";
import { Events } from "./tabs/Events";
import { Graphs } from "./tabs/Graphs";
import {
  Containers,
  Filesystems,
  Network,
  Packages,
  Units,
} from "./tabs/Inventory";
import { Overview } from "./tabs/Overview";

export type HostTab =
  | "overview"
  | "graphs"
  | "containers"
  | "filesystems"
  | "network"
  | "packages"
  | "units"
  | "events";

/** The bar is built to take a ninth tab (Alerts, with the Stage 2 engine)
 * without relayout; it wraps at narrow widths rather than scrolling, and
 * there is deliberately no overflow menu -- a hidden tab is an unused tab. */
export const HOST_TABS: readonly { id: HostTab; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "graphs", label: "Graphs" },
  { id: "containers", label: "Containers" },
  { id: "filesystems", label: "Filesystems" },
  { id: "network", label: "Network" },
  { id: "packages", label: "Packages" },
  { id: "units", label: "Units" },
  { id: "events", label: "Events" },
];

/**
 * Every tab is a URL. Wave 5 owns the router; this function owns the shape
 * of the path, so the router and the tab bar cannot disagree about it.
 */
export function hostTabHref(hostId: number | string, tab: HostTab): string {
  return `/hosts/${hostId}/${tab}`;
}

// The windows this page OFFERS. The type and the resolution are lib/range's:
// the hub rejects relative times outright, and one module converting them
// means one place to be wrong.
const RANGE_OPTIONS: { value: Range; label: string }[] = [
  { value: "1h", label: "1h" },
  { value: "6h", label: "6h" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
];

interface TabData {
  hostMetrics: MetricsResponse | null;
  filesystemMetrics: MetricsResponse | null;
  agentMetrics: MetricsResponse | null;
  sensorMetrics: MetricsResponse | null;
  netMetrics: MetricsResponse | null;
  diskIoMetrics: MetricsResponse | null;
  collectorMetrics: MetricsResponse | null;
  containers: Container[] | null;
  filesystems: Filesystem[] | null;
  addresses: Address[] | null;
  packages: Pkg[] | null;
  units: Unit[] | null;
  events: Event[] | null;
}

const NO_DATA: TabData = {
  hostMetrics: null,
  filesystemMetrics: null,
  agentMetrics: null,
  sensorMetrics: null,
  netMetrics: null,
  diskIoMetrics: null,
  collectorMetrics: null,
  containers: null,
  filesystems: null,
  addresses: null,
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
}

export function HostPage({ hostId, tab, onTabChange }: HostPageProps) {
  const [host, setHost] = useState<HostDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  // The time range is shared state across tabs: it lives here, above the
  // tab body, so switching tabs never resets it.
  const [range, setRange] = useState<Range>("6h");
  const [data, setData] = useState<TabData>(NO_DATA);
  const [reloads, setReloads] = useState(0);

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
          ] = await Promise.all([
            metrics("host"),
            metrics("filesystem"),
            metrics("agent"),
            metrics("sensor"),
            orNull(getContainers(hostId)),
            orNull(getUnits(hostId)),
          ]);
          return {
            hostMetrics,
            filesystemMetrics,
            agentMetrics,
            sensorMetrics,
            containers,
            units,
          };
        }
        case "graphs": {
          const [
            hostMetrics,
            netMetrics,
            diskIoMetrics,
            filesystemMetrics,
            collectorMetrics,
          ] = await Promise.all([
            metrics("host"),
            metrics("net"),
            metrics("disk_io"),
            metrics("filesystem"),
            metrics("collector"),
          ]);
          return {
            hostMetrics,
            netMetrics,
            diskIoMetrics,
            filesystemMetrics,
            collectorMetrics,
          };
        }
        case "containers":
          return { containers: await orNull(getContainers(hostId)) };
        case "filesystems":
          return { filesystems: await orNull(getFilesystems(hostId)) };
        case "network":
          return { addresses: await orNull(getAddresses(hostId)) };
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
                limit: 500,
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
        <span className="meta">
          {[host.site_name, host.os_name, host.kernel, host.arch]
            .map((part) => part ?? ABSENT)
            .join(" · ")}
        </span>
        <Badge severity={status.severity}>{status.label}</Badge>
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
          containers={data.containers}
          units={data.units}
        />
      )}
      {tab === "graphs" && (
        <Graphs
          host={data.hostMetrics}
          net={data.netMetrics}
          diskIo={data.diskIoMetrics}
          filesystem={data.filesystemMetrics}
          collector={data.collectorMetrics}
        />
      )}
      {tab === "containers" && <Containers rows={data.containers ?? []} />}
      {tab === "filesystems" && <Filesystems rows={data.filesystems ?? []} />}
      {tab === "network" && <Network rows={data.addresses ?? []} />}
      {tab === "packages" && <Packages rows={data.packages ?? []} />}
      {tab === "units" && <Units rows={data.units ?? []} />}
      {tab === "events" && <Events events={data.events} />}
    </div>
  );
}
