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
import { ABSENT, duration, relative } from "../../lib/format";
import { hostStatus } from "../../lib/host";
import { clampRange, rangeWindow, type Range } from "../../lib/range";
import { loadRange } from "../settings/SettingsPage";
import { RANGE_OPTIONS, RANGE_VALUES } from "./ranges";
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

// The windows this page OFFERS live in ./ranges -- the Graphs tab needs them
// too, and importing them from here would be a cycle. Re-exported because
// the screen above clamps a remembered range to this set before fetching:
// clamping inside would leave the fetch on 30d while the toolbar showed 7d.
export { RANGE_OPTIONS, RANGE_VALUES };

interface TabData {
  hostMetrics: MetricsResponse | null;
  filesystemMetrics: MetricsResponse | null;
  agentMetrics: MetricsResponse | null;
  sensorMetrics: MetricsResponse | null;
  netMetrics: MetricsResponse | null;
  diskIoMetrics: MetricsResponse | null;
  collectorMetrics: MetricsResponse | null;
  coreMetrics: MetricsResponse | null;
  containerMetrics: MetricsResponse | null;
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
  coreMetrics: null,
  containerMetrics: null,
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
        case "graphs": {
          const [
            hostMetrics,
            netMetrics,
            diskIoMetrics,
            filesystemMetrics,
            collectorMetrics,
            coreMetrics,
            agentMetrics,
          ] = await Promise.all([
            metrics("host"),
            metrics("net"),
            metrics("disk_io"),
            metrics("filesystem"),
            metrics("collector"),
            metrics("cpu_core"),
            metrics("agent"),
          ]);
          return {
            hostMetrics,
            netMetrics,
            diskIoMetrics,
            filesystemMetrics,
            collectorMetrics,
            coreMetrics,
            agentMetrics,
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
        case "filesystems": {
          // The inventory row carries a label, a mountpoint and a device id
          // and nothing else -- size, used and free live in the metrics
          // family. Fetching both is what makes this tab answer the question
          // anyone opens it for: how full is that disk.
          const [filesystems, filesystemMetrics] = await Promise.all([
            orNull(getFilesystems(hostId)),
            metrics("filesystem"),
          ]);
          return { filesystems, filesystemMetrics };
        }
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

            "rebooted", not the duration alone. Badge's dot is aria-hidden, so
            a screen reader hearing "1 m 40 s" cannot tell this host from one
            up for "266 d 6 h", and a deuteranope sees only a hue change --
            the state would ride on colour alone, which is precisely what
            pairing a dot with a WORD prevents. A duration is not a
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
        />
      )}
      {tab === "graphs" && (
        <Graphs
          host={data.hostMetrics}
          net={data.netMetrics}
          diskIo={data.diskIoMetrics}
          filesystem={data.filesystemMetrics}
          collector={data.collectorMetrics}
          cpuCore={data.coreMetrics}
          agent={data.agentMetrics}
          range={range}
          fetchFamily={fetchFamily}
        />
      )}
      {tab === "containers" && (
        <Containers
          rows={data.containers ?? []}
          metrics={data.containerMetrics ?? null}
          range={range}
          // Why the list is empty, or why its rows are named after 64 hex
          // digits. The agent is the only party that knows, and it said so.
          capabilities={host.capabilities}
        />
      )}
      {tab === "filesystems" && (
        <Filesystems
          rows={data.filesystems ?? []}
          metrics={data.filesystemMetrics ?? null}
        />
      )}
      {tab === "network" && <Network rows={data.addresses ?? []} />}
      {tab === "packages" && <Packages rows={data.packages ?? []} />}
      {tab === "units" && <Units rows={data.units ?? []} />}
      {tab === "events" && <Events events={data.events} />}
    </div>
  );
}
