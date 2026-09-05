// Host detail: a header that never changes across tabs, then the tab bar,
// then the active tab. This is the only file in the feature that talks to
// the read API -- the tabs take plain props, so a tab renders identically
// whether its data came from a fetch, a poll or a fixture.
import { useCallback, useEffect, useRef, useState } from "react";
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
import { hostTabForSlug } from "../../lib/router";
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
import { NetworkGraphs, StorageGraphs, SystemGraphs } from "./tabs/Graphs";
import { CollectorsTab } from "./tabs/CollectorsTab";
import { LimitsCard } from "./tabs/LimitsCard";
import { Sensors } from "./tabs/Sensors";
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
import { Server } from "lucide-react";

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

  // When the series last came down, and for which tab and range. A ref, not
  // state: nothing renders from it, and writing it must not cost a render on
  // every poll.
  const seriesFetch = useRef({ key: "", at: 0 });

  // The active tab's own families, on that same tick. usePoll owns the
  // cancellation this effect used to hand-roll, and the window is resolved
  // INSIDE the call rather than once per range change, so each poll slides
  // `to` forward instead of pinning it at the instant the tab was opened --
  // the same reason EventsScreen gives in App.tsx.
  const tabPoll = usePoll(
    async (): Promise<Partial<TabData>> => {
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
      if (due) seriesFetch.current = { key, at };
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
                step: window.step,
              }),
            )
          : Promise.resolve(undefined);

      async function load(): Promise<Partial<TabData>> {
        switch (tab) {
          case "overview": {
            // Six requests, down from eight. The tab stopped drawing the
            // per-core stack (cpu_core), the sensor cards (sensor) and the
            // Inventory counts (containers) when they moved or were dropped,
            // and a page that keeps fetching what it no longer draws is three
            // round trips a reader pays for and never sees. units stays: the
            // attention band counts the failed ones. drives is here for the
            // same reason and was the one signal missing -- a host with a
            // failing disk read clean on this tab while its Storage tab
            // showed the drive in red, because the listing was fetched only
            // when that tab was opened.
            const [
              hostMetrics,
              filesystemMetrics,
              agentMetrics,
              units,
              drives,
              netMetrics,
            ] = await Promise.all([
              metrics("host"),
              metrics("filesystem"),
              metrics("agent"),
              orNull(getUnits(hostId)),
              orNull(getDrives(hostId)),
              // Traffic: the overview summarised every subsystem except the
              // one most likely to explain a problem.
              metrics("net"),
            ]);
            return {
              hostMetrics,
              filesystemMetrics,
              agentMetrics,
              units,
              drives,
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
            // sensor joined these two when the Temperature, Fan and Power
            // cards moved here off the Overview: what a chip is measuring is
            // a System fact, and this is the tab that draws it now.
            const [hostMetrics, coreMetrics, sensorMetrics] = await Promise.all(
              [metrics("host"), metrics("cpu_core"), metrics("sensor")],
            );
            return {
              hostMetrics,
              coreMetrics,
              sensorMetrics,
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

      return load();
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
  useEffect(() => {
    const loaded = tabPoll.data;
    if (loaded === null) return;
    setData((prev) => {
      const next = { ...prev };
      for (const [family, value] of Object.entries(loaded)) {
        if (value !== undefined) {
          (next as Record<string, unknown>)[family] = value;
        }
      }
      return next;
    });
  }, [tabPoll.data]);

  useEffect(() => {
    onPollError?.(hostPoll.error);
  }, [hostPoll.error, onPollError]);

  // Both halves: the header's record and the tab's families are two polls,
  // and anything asking for the page again is asking for all of it, not half.
  // usePoll's refresh is stable, so this identity is too -- it is handed to
  // the Containers tab as onPurged and to the load-failure retry.
  const refresh = useCallback(() => {
    hostPoll.refresh();
    tabPoll.refresh();
  }, [hostPoll.refresh, tabPoll.refresh]);

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
        {/* The rail's own mark for hosts. A detail page has no rail
            destination of its own, so it wears its parent's. It stays outside
            .hostident, and is centred on the title's line by .hosthead. */}
        <span className="pageicon">
          <Server aria-hidden="true" />
        </span>
        {/* The name and everything that qualifies it, as one block. */}
        <div className="hostident">
          {/* The title's own line, and everything that qualifies it. These were
              siblings of .hostident, so the row centred them against the whole
              block -- which left the badge, the last-seen time and the range
              control 11.8px low, and the page's own mark with them. In a row
              whose tallest item is the h1 they centre on the title itself. */}
          <div className="hostbar">
            <h1 className="serif hostname">{host.hostname}</h1>
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
          </div>
          {/* Nothing under the title. Where the host is, its OS, kernel and
            arch are all printed a few centimetres below in the Overview tab's
            System card, which owns them -- the header was spending a line
            restating the card. */}
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

      {tab === "overview" && (
        <Overview
          host={host}
          hostMetrics={data.hostMetrics}
          filesystemMetrics={data.filesystemMetrics}
          agentMetrics={data.agentMetrics}
          netMetrics={data.netMetrics}
          units={data.units}
          drives={data.drives}
          range={range}
          fetchFamily={fetchFamily}
          // A tile's href is /hosts/{id}/chart/<slug>, which the router
          // already resolves to whichever tab draws that panel. This is that
          // same walk done client-side, so a plain click switches tabs
          // instead of reloading the app -- and cmd-click still gets the real
          // URL, which lands in the same place.
          onOpenChart={(slug) => onTabChange(hostTabForSlug(slug))}
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
          {/* Below the charts and above the limits: hardware readings after
              the software ones, and still charts rather than the ceilings the
              card below states. Renders nothing at all on a host with no
              sensors, which is most of a cloud fleet. */}
          <Sensors
            sensorMetrics={data.sensorMetrics}
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
