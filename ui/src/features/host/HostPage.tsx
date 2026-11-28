// Host detail: a header that never changes across tabs, then the tab bar,
// then the active tab. This is the only file in the feature that talks to
// the read API -- the tabs take plain props, so a tab renders identically
// whether its data came from a fetch, a poll or a fixture.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  getAddresses,
  getConditions,
  getContainers,
  getEvents,
  getFilesystems,
  getDrives,
  getArrays,
  getHost,
  getInterfaces,
  getMetrics,
  getPackages,
  getUnits,
  type Address,
  type ConditionsResponse,
  type Container,
  type Drive,
  type MdArray,
  type Event,
  type Filesystem,
  type Iface,
  type MetricsResponse,
  type Pkg,
  type Unit,
} from "../../lib/api";
import { Badge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { HostMutes } from "./HostMutes";
import { useMutes } from "./useMutes";
import { Segmented } from "../../ui/Segmented";
import { Tabs } from "../../ui/Tabs";
import { When } from "../../ui/When";
import { duration } from "../../lib/format";
import { hostStatus } from "../../lib/host";
import { catalogueOf, EMPTY_CATALOGUE } from "../fleet/conditions";
import { ChartCursorProvider } from "../../ui/charts/cursor";
import {
  clampRange,
  rangeWindow,
  rangeStepMs,
  EVENT_LIMITS,
  type Range,
} from "../../lib/range";
import { POLL_MS, pollMsFor, usePoll } from "../../lib/poll";
import { loadRange } from "../settings/SettingsPage";
import { RANGE_OPTIONS, RANGE_VALUES } from "./ranges";
import { Events } from "./tabs/Events";
import {
  NetworkGraphs,
  StorageGraphs,
  SystemGraphs,
  timedEvents,
} from "./tabs/Graphs";
import { AgentTab } from "./tabs/AgentTab";
import { LimitsCard } from "./tabs/LimitsCard";
import { Sensors } from "./tabs/Sensors";
import {
  Containers,
  Drives,
  Interfaces,
  Network,
  Packages,
  Units,
} from "./tabs/Inventory";
import { Overview } from "./tabs/Overview";
import { useTabTitle } from "../../lib/title";

export type HostTab =
  | "overview"
  | "system"
  | "network"
  | "storage"
  | "containers"
  | "packages"
  | "events"
  | "agent";

/** How many of the window's events the marked tabs ask for. */
const MARKER_EVENTS = 500;

/** The tabs that draw the window's events as markers on their lead charts.
 * The Overview's Events card is the log, not markers: it reads `events`. */
const MARKED_TABS: ReadonlySet<HostTab> = new Set([
  "system",
  "network",
  "agent",
  // The drive rows draw only the md events of the arrays on each drive.
  "storage",
]);

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
  // What is installed. The units needing attention are not here: what is
  // running is the System tab's, which they head.
  { id: "packages", label: "Packages" },
  { id: "events", label: "Events" },
  // Last, at the quiet end of the bar: the agent's own health is the tab you
  // open when you doubt what the other tabs are telling you.
  { id: "agent", label: "Agent" },
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
  /** The md arrays: Storage's Arrays section, the Overview's sync line. */
  arrays: MdArray[] | null;
  /** Everything the hub says is wrong, fleet-wide -- narrowed to this host
   * where the band reads it. One endpoint answers for the whole fleet, so
   * there is nothing per-host to ask for. null when the call failed, which
   * the band must not read as "nothing is wrong". */
  conditions: ConditionsResponse | null;
  addresses: Address[] | null;
  interfaces: Iface[] | null;
  packages: Pkg[] | null;
  units: Unit[] | null;
  /** The window's events, the log: the Events tab and the Overview card. */
  events: Event[] | null;
  /** The window's events as the marked tabs' lead charts place them. A slot
   * of its own so neither use draws the other's list: capped and on the
   * series' cadence here, the full table and every poll there. */
  markerEvents: Event[] | null;
}

/**
 * The families drawn over the page's window: every metric family, the log
 * and the markers. They are held per range, apart from the inventories,
 * because one answer is only true of the range it was asked for.
 *
 * One store for all of them used to merge each answer over the last, so a
 * tab last opened at 7d drew its 7d charts after a switch to 12mo, until the
 * 12mo answer landed and the picture jumped. Keyed by range, a tab shows the
 * range on screen or its loading state, never another range's chart.
 */
const WINDOWED = new Set<keyof TabData>([
  "hostMetrics",
  "hostSnmpMetrics",
  "hostProtoMetrics",
  "filesystemMetrics",
  "agentMetrics",
  "sensorMetrics",
  "netMetrics",
  "diskIoMetrics",
  "smartMetrics",
  "collectorMetrics",
  "coreMetrics",
  "containerMetrics",
  "events",
  "markerEvents",
]);

/**
 * How many ranges' families stay cached: the one on screen and the one
 * before it, so going back is instant. A 30d tab is ~4 MB of JSON; every
 * range on the picker cached would hold that seven times for nothing.
 */
const CACHED_RANGES = 2;

/** One range's cached families, and the tabs whose answer they hold. */
export interface WindowedEntry {
  families: Partial<TabData>;
  tabs: readonly HostTab[];
}

export type WindowedCache = Partial<Record<Range, WindowedEntry>>;

/**
 * One tab's answer, its windowed families merged into the cache under its
 * range, which becomes the newest entry. Older ranges past CACHED_RANGES drop
 * out (string keys keep insertion order, and none of these is an integer).
 */
export function mergeWindowed(
  cache: WindowedCache,
  range: Range,
  tab: HostTab,
  families: Partial<TabData>,
): WindowedCache {
  const prev = cache[range];
  const merged: Partial<TabData> = { ...prev?.families };
  for (const [family, value] of Object.entries(families)) {
    if (value !== undefined && WINDOWED.has(family as keyof TabData)) {
      (merged as Record<string, unknown>)[family] = value;
    }
  }
  const tabs = prev?.tabs.includes(tab)
    ? prev.tabs
    : [...(prev?.tabs ?? []), tab];
  const order = (Object.keys(cache) as Range[]).filter((r) => r !== range);
  const kept = CACHED_RANGES > 1 ? order.slice(-(CACHED_RANGES - 1)) : [];
  const next: WindowedCache = {};
  for (const r of kept) next[r] = cache[r];
  next[range] = { families: merged, tabs };
  return next;
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
  arrays: null,
  conditions: null,
  addresses: null,
  interfaces: null,
  packages: null,
  units: null,
  events: null,
  markerEvents: null,
};

/**
 * A per-request failure yields null instead of rejecting the whole load.
 * One family the hub cannot answer must not blank the seven panels beside
 * it -- and a null reaches the tab as "nothing to draw", which is exactly
 * what it means. A failure of the host detail itself is different: that
 * one is surfaced as an error, because without it there is no page.
 */
/**
 * The step one family is asked for over a range: the range's own, except
 * SMART at 30d.
 *
 * SMART is stored hourly with no aggregate, and the family returns every
 * attribute of every drive. 30d's hourly step is 720 points per attribute,
 * which a ten-disk NAS (~28 attributes each) takes past the hub's 200k cap.
 * At 6h the hub buckets it (read.resample), 120 points each, still four a
 * day for a temperature curve. 3mo and 12mo already ask 24h.
 */
export function familyStep(family: string, range: Range, step: string): string {
  return family === "smart" && range === "30d" ? "6h" : step;
}

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
  /** Called with this page's host-record poll error, and with null when a
   * later poll succeeds. The screen above routes on it -- a 401 is the
   * session expiring, which is a routing decision rather than something to
   * render, and this page is the only one that had no way to report it. */
  onPollError?: (error: Error | null) => void;
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
  onPollError,
}: HostPageProps) {
  // The host record, on the same 60-second tick as every other screen. It
  // used to be fetched once and never again, while the badge it drives reads
  // the clock at every render: a page left open past STALE_THRESHOLD_MS then
  // called a host that had been posting all along "offline", blanked its
  // traffic gauges and told the Overview it last reported four minutes ago --
  // on the next render of any kind, which is a tab click. A record judged
  // against a live clock has to move with it.
  const hostPoll = usePoll(() => getHost(hostId), POLL_MS, [hostId]);
  const host = hostPoll.data;
  // The host, then the tab: two tabs of one host still read apart. A rename
  // from the agents page shows on the next poll.
  useTabTitle(host?.hostname, HOST_TABS.find((t) => t.id === tab)?.label);
  // One read for the header's list and every alert line's mute, so a mute
  // set on a line shows in the header at once and the other way round.
  const mutes = useMutes(Number(hostId));
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
  // The inventories (no window), and the windowed families per range: see
  // WINDOWED. `data` further down is the two read together for the range the
  // tab is drawn at.
  const [inventory, setInventory] = useState<TabData>(NO_DATA);
  const [windowed, setWindowed] = useState<WindowedCache>({});

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
        step: familyStep(family, next, window.step),
      });
    },
    [hostId],
  );

  // The events of one range, as markers, for an enlarged lead chart whose
  // rail has left the page's range. The same query the tab's markers use.
  const fetchEvents = useCallback(
    async (next: Range) => {
      const window = rangeWindow(next);
      return timedEvents(
        await getEvents({
          host: hostId,
          since: window.from,
          until: window.to,
          limit: MARKER_EVENTS,
        }),
      );
    },
    [hostId],
  );

  // When the series last came down, and for which tab and range. A ref, not
  // state: nothing renders from it, and writing it must not cost a render on
  // every poll.
  //
  // Written once an answer has been ACCEPTED (the effect below the merge),
  // never when a request goes out. A run usePoll then drops -- superseded by
  // a refresh, or StrictMode's discarded first effect in development -- had
  // already marked the key fetched, the run that replaced it found nothing
  // due, and the tab showed "not collected" for families nobody had asked
  // for until the slow timer came round.
  const seriesFetch = useRef({ key: "", at: 0 });

  // The active tab's own families, on that same tick. usePoll owns the
  // cancellation this effect used to hand-roll, and the window is resolved
  // INSIDE the call rather than once per range change, so each poll slides
  // `to` forward instead of pinning it at the instant the tab was opened --
  // the same reason EventsScreen gives in App.tsx.
  const tabPoll = usePoll(
    async (): Promise<{
      families: Partial<TabData>;
      series: { key: string; at: number } | null;
      /** Which host, tab and range this answer is for. */
      key: string;
      /** The range and the tab alone, which file its windowed families. */
      range: Range;
      tab: HostTab;
    }> => {
      const window = rangeWindow(range);
      // The SERIES, and only the series, slow down with the range.
      //
      // A chart cannot change faster than its own buckets -- the hub returns
      // whole materialised buckets, so at 24h the newest point moves every
      // five minutes and at 7d every hour -- and a tab asks for up to twelve
      // families at once. Asking again every minute redraws the same picture.
      //
      // What rides along in this same poll does NOT follow that argument:
      // units, containers, filesystems, drives, addresses, interfaces,
      // packages and events are inventories with no buckets at all. A unit
      // that fails, a container that exits, an event that lands -- those are
      // as fresh as the poll that fetched them, and the header directly above
      // says "last seen 20 s ago" while it does it. So the poll keeps running
      // at POLL_MS and only the metric families skip a turn.
      //
      // Keyed on the tab as well as the range: a family the tab in front of
      // you has never fetched must not wait out a five-minute timer to appear
      // for the first time.
      const key = `${hostId}:${tab}:${range}`;
      const at = Date.now();
      const due =
        key !== seriesFetch.current.key ||
        at - seriesFetch.current.at >= pollMsFor(rangeStepMs(range));
      // `undefined` rather than the previous response: what is already on
      // screen lives in `data`, and the merge below leaves a family it is not
      // told about alone. Returning null would blank the very charts this is
      // saving a request for.
      const metrics = (family: string) =>
        due
          ? orNull(
              getMetrics(hostId, {
                family,
                from: window.from,
                to: window.to,
                step: familyStep(family, range, window.step),
              }),
            )
          : Promise.resolve(undefined);

      // The log, an inventory: every poll. The same table the fleet log
      // uses. A flat 500 is fine at 1h and a silent truncation at 7d, which
      // this page offers: one host's dist-upgrade plus a week of ordinary
      // churn reaches it, and the rows lost are the oldest -- exactly the
      // ones someone widening the window is looking for.
      const eventLog = () =>
        orNull(
          getEvents({
            host: hostId,
            since: window.from,
            until: window.to,
            limit: EVENT_LIMITS[range],
          }),
        );

      async function load(): Promise<Partial<TabData>> {
        switch (tab) {
          case "overview": {
            // What the four readings, the Filesystems table and the Docker
            // CPU panel draw, and nothing else: the agent and drive families
            // used to ride along here for other tabs and drew nothing on this
            // one. units stays: the attention band lists the failed ones, and
            // the ones restarting repeatedly. The conditions are what the band
            // renders, so this page and the fleet cannot disagree about one
            // host. The container family answers empty on a host without
            // Docker, and the panel then draws nothing. The Events card is the
            // log the Events tab shows, as fresh as this poll.
            const [
              hostMetrics,
              filesystemMetrics,
              netMetrics,
              containerMetrics,
              filesystems,
              units,
              conditions,
              events,
              arrays,
            ] = await Promise.all([
              metrics("host"),
              metrics("filesystem"),
              metrics("net"),
              metrics("container"),
              orNull(getFilesystems(hostId)),
              orNull(getUnits(hostId)),
              orNull(getConditions()),
              eventLog(),
              // For the one line saying an array is being checked or
              // rebuilt, which is what keeps a host's disks busy for hours.
              orNull(getArrays(hostId)),
            ]);
            return {
              hostMetrics,
              filesystemMetrics,
              netMetrics,
              containerMetrics,
              filesystems,
              units,
              conditions,
              events,
              arrays,
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
            // sensor joined these two when the Temperature, Fan and Power
            // cards moved here off the Overview: what a chip is measuring is
            // a System fact, and this is the tab that draws it now.
            // units: the services systemd runs are the system's, and this is
            // the tab that says whether one of them is in trouble.
            const [hostMetrics, coreMetrics, sensorMetrics, units] =
              await Promise.all([
                metrics("host"),
                metrics("cpu_core"),
                metrics("sensor"),
                orNull(getUnits(hostId)),
              ]);
            return {
              hostMetrics,
              coreMetrics,
              sensorMetrics,
              units,
            };
          }
          case "agent": {
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
              arrays,
              filesystemMetrics,
              diskIoMetrics,
              smartMetrics,
            ] = await Promise.all([
              orNull(getFilesystems(hostId)),
              // The physical disks under those mounts. orNull, like every
              // other family: a hub too old to serve the route leaves the
              // table empty and the rest of the tab intact.
              orNull(getDrives(hostId)),
              // The md arrays over those drives. orNull: a hub too old to
              // serve the route draws no Arrays section.
              orNull(getArrays(hostId)),
              metrics("filesystem"),
              metrics("disk_io"),
              // The SMART readings over time, which is what turns the Drives
              // table's temperature from a number into a movement. The family
              // carries every attribute of every drive -- the read API takes no
              // key filter -- but at an hourly cadence that is a few hundred
              // points, and it is the only route to this history. From 30d
              // on the hub buckets it to a coarser step (familyStep), or a
              // month of every attribute ran into maxPoints.
              metrics("smart"),
            ]);
            return {
              filesystems,
              drives,
              arrays,
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
          case "events":
            return { events: await eventLog() };
        }
      }

      // The tabs whose lead charts carry event markers fetch the window's
      // events beside their series, and at the series' cadence: a marker is
      // placed on a bucket, so it cannot move faster than the buckets do.
      // Capped well under the Events tab's own limit -- a marker per event
      // on every lead chart is a picture of the window, not the log.
      const [families, markerEvents] = await Promise.all([
        load(),
        MARKED_TABS.has(tab) && due
          ? orNull(
              getEvents({
                host: hostId,
                since: window.from,
                until: window.to,
                limit: MARKER_EVENTS,
              }),
            )
          : Promise.resolve(undefined),
      ]);
      return {
        families:
          markerEvents === undefined ? families : { ...families, markerEvents },
        series: due ? { key, at } : null,
        key,
        range,
        tab,
      };
    },
    POLL_MS,
    [hostId, tab, range],
  );

  // Merged rather than replaced: a tab fetches only the families its own
  // panels draw, so the families a previously visited tab loaded have to
  // survive the switch. usePoll has already dropped the late answer of a
  // superseded run -- a response about a tab or a range nobody is on any
  // more -- which is what the cancelled flag here used to do.
  //
  // A family whose value is `undefined` is one this poll deliberately did not
  // ask for -- a series whose buckets cannot have moved yet -- so it is
  // skipped rather than merged: spreading it would write undefined over the
  // chart already on screen.
  //
  // While rendering, once per answer, rather than in an effect: the page never
  // renders an answer's tab without its families.
  const loaded = tabPoll.data;
  const [mergedFrom, setMergedFrom] = useState(loaded);
  if (loaded !== mergedFrom) {
    setMergedFrom(loaded);
    if (loaded !== null) {
      setInventory((prev) => {
        const next = { ...prev };
        for (const [family, value] of Object.entries(loaded.families)) {
          if (value !== undefined && !WINDOWED.has(family as keyof TabData)) {
            (next as Record<string, unknown>)[family] = value;
          }
        }
        return next;
      });
      setWindowed((prev) =>
        mergeWindowed(prev, loaded.range, loaded.tab, loaded.families),
      );
    }
  }

  // The range the tab is DRAWN at, which trails the picked one while the
  // picked one has nothing to draw for this tab yet.
  //
  // Switching to a range the cache has not seen used to blank every view
  // that cannot say "loading" -- the Events tab claimed nothing had been
  // collected, the sensor cards vanished and popped back. Drawing the old
  // range's families beside the new one's was the original fault. So the tab
  // keeps its WHOLE previous picture, one range, until the new range's answer
  // for this tab lands, and then swaps in one go; the picker already shows
  // the new choice, with a note that it is loading.
  const asked = windowed[range]?.tabs.includes(tab) ?? false;
  const [shownRange, setShownRange] = useState(range);
  if (shownRange !== range && (asked || windowed[shownRange] === undefined)) {
    setShownRange(range);
  }
  const drawn =
    asked || windowed[shownRange] === undefined ? range : shownRange;
  const data = useMemo<TabData>(
    () => ({ ...inventory, ...windowed[drawn]?.families }),
    [inventory, windowed, drawn],
  );

  // The window's events as chart markers, for the lead charts of a tab.
  const markers = useMemo(
    () => timedEvents(data.markerEvents),
    [data.markerEvents],
  );

  // Whether the tab in front of the reader has had its first answer yet.
  // Until it has, a family with nothing in it is loading -- not "not
  // collected", which is a statement about the host this page cannot make
  // before the hub has been asked. Never while an older range is drawn in
  // full: that picture is complete, only not the one picked.
  const pending =
    drawn === range && loaded?.key !== `${hostId}:${tab}:${range}`;
  const switching = drawn !== range;

  // After commit, so only an answer usePoll accepted counts as fetched.
  useEffect(() => {
    if (loaded?.series) seriesFetch.current = loaded.series;
  }, [loaded]);

  useEffect(() => {
    onPollError?.(hostPoll.error);
  }, [hostPoll.error, onPollError]);

  // Both halves: the header's record and the tab's families are two polls,
  // and anything asking for the page again is asking for all of it, not half.
  // usePoll's refresh is stable, so this identity is too -- it is handed to
  // the Containers tab as onPurged and to the load-failure retry.
  const refreshHost = hostPoll.refresh;
  const refreshTab = tabPoll.refresh;
  const refresh = useCallback(() => {
    refreshHost();
    refreshTab();
  }, [refreshHost, refreshTab]);

  // Only while there is nothing to show. A poll that fails after the page
  // has rendered leaves the last good record in place -- usePoll's own rule,
  // and the right one here: replacing a host's page with an error because one
  // refresh timed out loses everything the reader was looking at.
  if (hostPoll.error !== null && host === null) {
    return (
      <div className="hostpage">
        <p className="note">
          This host could not be loaded: {hostPoll.error.message}
        </p>
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
      {/* A failed poll leaves the last good record on screen, which is right
          -- but silently, everything below would be a frozen page that looks
          live, and the poll that would have unfrozen it is the one that just
          failed. The numbers stay and the page says they have stopped moving;
          how stale they are is the header's own "last seen". */}
      {hostPoll.error !== null && (
        <p className="note" role="alert">
          These readings have stopped refreshing: {hostPoll.error.message}
        </p>
      )}
      {/* The header is identical on every tab -- it is what you are
          looking at, not what you are looking at it through. */}
      <header className="hosthead" aria-label="Host summary">
        {/* No mark beside the name. The page used to wear the rail's Hosts
            glyph here, which said "this is a host" next to a name that says
            so already, and the fleet row this page is opened from carries
            none. The rail's own highlight says where the reader is. */}
        {/* The name and everything that qualifies it, as one block. */}
        <div className="hostident">
          {/* The title's own line, and everything that qualifies it. These were
              siblings of .hostident, so the row centred them against the whole
              block -- which left the badge, the last-seen time and the range
              control 11.8px low. In a row whose tallest item is the h1 they
              centre on the title itself. */}
          <div className="hostbar">
            {/* Ink, not .hostname's accent: the fleet row prints the name in
                --ink and the serif, and this is the same name one click later.
                Accent on the fleet page is spent on the clause saying what is
                wrong, and a title in it read as the page's one alarm. */}
            <h1 className="serif">{host.hostname}</h1>
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
            {/* The same `When` every Last seen column renders, so the header
              agrees with the fleet row the reader just came from: the
              absolute instant on hover, and "never" rather than a dash for a
              host that has not reported once -- this header used to be the
              one place that drew the dash. */}
            {/* The uptime as a figure, where the Uptime chart's rising
              diagonal used to say the same less directly. The same guard as
              the reboot chip: a host the hub is not hearing from has no
              current uptime to state, and a recent boot says it in the chip. */}
            {status.severity !== "critical" &&
              host.uptime_s !== null &&
              !recentlyBooted && (
                <span className="meta">up {duration(host.uptime_s)}</span>
              )}
            <span className="meta">
              last seen <When iso={host.last_seen} never />
            </span>
            {/* One range control for the whole page: every chart on every tab
              is drawn from it, so no tab may grow one of its own. */}
            <Segmented
              options={RANGE_OPTIONS}
              value={range}
              onChange={(value) => setRange(value)}
            />
            {/* While the tab still shows the previous range in full: says
              so, rather than let an old axis pass for the picked range. */}
            {switching && (
              <span className="meta" role="status">
                loading {range}…
              </span>
            )}
            {/* No Refresh button beside it. Both halves of the page poll every
              POLL_MS, which is the agent's own reporting interval, so the button
              asked the hub for numbers that could not have changed yet and its
              reward was a header that looked identical. (The metric series skip
              a turn while their buckets cannot have moved -- see the tab poll --
              but a press would not have moved them either.) What it was
              really for is stated instead: "last seen" says how fresh the
              record is, and the note above says so out loud when a poll has
              failed. `refresh` stays -- the load-failure "Try again" is a
              retry a reader genuinely has to ask for, and the Containers tab
              calls it after a purge. */}
            {/* The mutes in force, on the title's own line, pushed to its
              right end. Set on the alert lines of the Overview tab; listed
              here too, since a permanent mute on an alert not open right now
              has no line of its own. Nothing at all when nothing is muted. */}
            <HostMutes mutes={mutes} />
          </div>
        </div>
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

      {/* One cursor per tab: hovering any chart draws the crosshair at the
          same moment in every other chart of the tab. Keyed so a tab switch
          starts without one. */}
      <ChartCursorProvider key={tab}>
        {tab === "overview" && (
          <Overview
            host={host}
            hostMetrics={data.hostMetrics}
            filesystemMetrics={data.filesystemMetrics}
            netMetrics={data.netMetrics}
            units={data.units}
            conditions={(data.conditions?.conditions ?? []).filter(
              (row) => row.host_id === host.id,
            )}
            // null is "netra could not be asked", which the panel must not
            // render as "nothing is wrong" -- it draws nothing at all when the
            // list is empty, so an offline host with a full disk and a failing
            // drive would show no panel whatsoever. The same line the fleet page
            // draws for the same failure.
            conditionsUnavailable={data.conditions === null}
            catalogue={
              data.conditions
                ? catalogueOf(data.conditions.kinds)
                : EMPTY_CATALOGUE
            }
            range={drawn}
            fetchFamily={fetchFamily}
            events={data.events}
            filesystems={data.filesystems}
            containerMetrics={data.containerMetrics}
            arrays={data.arrays}
            mutes={mutes}
          />
        )}
        {tab === "system" && (
          <>
            {/* First: a failed service is the question this tab can answer
              before any chart. On a healthy host it is one line. */}
            <Units
              rows={data.units}
              pending={pending}
              // undefined is a hub too old to say: assume systemd, as before.
              systemd={host.services_total !== null}
            />
            <SystemGraphs
              host={data.hostMetrics}
              cpuCore={data.coreMetrics}
              range={drawn}
              fetchFamily={fetchFamily}
              events={markers}
              fetchEvents={fetchEvents}
              threads={host.threads}
              pending={pending}
            />
            {/* Below the charts and above the limits: hardware readings after
              the software ones, and still charts rather than the ceilings the
              card below states. Renders nothing at all on a host with no
              sensors, which is most of a cloud fleet. */}
            <Sensors
              sensorMetrics={data.sensorMetrics}
              range={drawn}
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
        {tab === "agent" && (
          <AgentTab
            host={host}
            sources={{
              collector: data.collectorMetrics,
              agent: data.agentMetrics,
              range: drawn,
              fetchFamily,
              events: markers,
              fetchEvents,
              pending,
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
              // Reporting is judged on the hub's clock, as the fleet does.
              received_at: host.received_at,
              // The two denominators the saturation cells read against. This
              // page holds a HostDetail, which names them cores/memory_total
              // where the fleet's list row says threads/mem_total -- the same
              // box's CPUs and RAM under two names. The row calls each one
              // thing, so the mapping happens here rather than in the cell.
              threads: host.threads,
              mem_total: host.memory_total,
            }}
            metrics={data.containerMetrics ?? null}
            range={drawn}
            // A purged container is gone from the inventory, so the page
            // refetches rather than editing a copy of the list it does not own.
            onPurged={refresh}
            // Why the list is empty, or why its rows are named after 64 hex
            // digits. The agent is the only party that knows, and it said so.
            capabilities={host.capabilities}
          />
        )}
        {/* The charts first now: three charts and a row per interface are
          short enough that the inventory tables below are still one scroll
          away, and how the network is behaving is what the tab opens for. */}
        {tab === "network" && (
          <>
            <NetworkGraphs
              host={data.hostMetrics}
              hostSnmp={data.hostSnmpMetrics}
              hostProto={data.hostProtoMetrics}
              net={data.netMetrics}
              range={drawn}
              fetchFamily={fetchFamily}
              events={markers}
              fetchEvents={fetchEvents}
              pending={pending}
            />
            <Interfaces rows={data.interfaces ?? []} />
            <Network rows={data.addresses ?? []} />
          </>
        )}
        {tab === "storage" && (
          <>
            {/* Filesystems first: "how full is it" is what people open this
              tab for. Then a row per drive, how hard it is working, and its
              SMART health last -- the hardware under those mounts. */}
            <StorageGraphs
              filesystem={data.filesystemMetrics}
              diskIo={data.diskIoMetrics}
              filesystems={data.filesystems}
              lastSeen={host.last_seen}
              pending={pending}
              range={drawn}
              fetchFamily={fetchFamily}
              arrays={data.arrays}
              events={markers}
            />
            <Drives
              rows={data.drives ?? []}
              capabilities={host.capabilities}
              metrics={data.smartMetrics}
              range={drawn}
              fetchFamily={fetchFamily}
            />
          </>
        )}
        {tab === "packages" && <Packages rows={data.packages ?? []} />}
        {tab === "events" && <Events events={data.events} />}
      </ChartCursorProvider>
    </div>
  );
}
