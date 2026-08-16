import { useEffect, useRef, useState } from "react";
import {
  ApiError,
  getContainers,
  getHosts,
  getSites,
  type Container,
  type Host,
  type Site,
} from "../../lib/api";
import { ABSENT, byterate, relative } from "../../lib/format";
import { Input } from "../../ui/Control";
import { Segmented } from "../../ui/Segmented";
import { StatTile } from "../../ui/StatTile";
import { Tabs } from "../../ui/Tabs";
import { AttentionBand, type Condition } from "./AttentionBand";
import { fleetConditions, hostsNeedingAttention } from "./conditions";
import { FleetContainers, type ContainerRow } from "./FleetContainers";
import { HostCards } from "./HostCards";
import { HostTable } from "./HostTable";
import type { HostRow, Range } from "./hostColumns";
import { isReporting } from "../../lib/host";
import { buildRows } from "./hostTrends";
import { DENSITY_KEY, readPref, writePref } from "../../lib/prefs";

/** What you are looking at (spec 4.5's first axis). */
export type Entity = "hosts" | "containers";
/** How densely (spec 4.5's second axis). Independent of the entity. */
export type Density = "table" | "cards";

/** Spec 4.5: density is remembered per browser and defaulted in Settings.
 * The key lives in lib/prefs now and is re-exported here because Settings
 * (Task 19) and this page's tests import it from this module -- what matters
 * is that there is one key, not a second one that silently disagrees. */
export { DENSITY_KEY };

/**
 * Joins the fleet list to its site names and produces the rows both the
 * table and the card grid render from.
 *
 * `GET /api/v1/hosts` carries `site_id` and no name; the per-host detail
 * call that does carry one would be an N+1 across the whole fleet, so the
 * site list is fetched once and joined here by id.
 *
 * The chart series come back empty here: this path has the host list and
 * the site names, not the per-host metrics. App's poll fetches those and
 * builds the same rows with them (see hostTrends).
 */
export function buildHostRows(hosts: Host[], sites: Site[]): HostRow[] {
  // One builder, in hostTrends: this page's self-fetching path and App's
  // polling path must not be able to disagree about what a row is. Without
  // trends every series is empty, which renders as a gap and as the absent
  // marker -- the truth, since not fetched is not zero.
  return buildRows(hosts, sites, new Map());
}

/**
 * "/" focuses the filter, the way it does in every tool where the first
 * thing you do on a list is narrow it.
 *
 * It deliberately does nothing while a field already has focus -- otherwise
 * typing a path into any input on the page would jump the cursor -- and it
 * leaves modified keypresses alone, because ctrl+/ and the browser's own
 * shortcuts are not ours to take.
 */
function useSlashToFocus() {
  const ref = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey) {
        return;
      }
      const active = document.activeElement;
      const typing =
        active instanceof HTMLInputElement ||
        active instanceof HTMLTextAreaElement ||
        active instanceof HTMLSelectElement ||
        (active instanceof HTMLElement && active.isContentEditable);
      if (typing) return;
      event.preventDefault();
      ref.current?.focus();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  return ref;
}

/** The mobile breakpoint, matching index.css's own. */
const NARROW = "(max-width: 640px)";

/**
 * True below the mobile breakpoint. matchMedia rather than a resize
 * listener, because the browser already knows the answer and re-asking it on
 * every resize event is work for nothing; the listener fires only when the
 * answer changes.
 *
 * Guarded because jsdom has no matchMedia unless a test installs one, and a
 * missing one must mean "not narrow" rather than a thrown render.
 */
function useIsNarrow(): boolean {
  const [narrow, setNarrow] = useState(
    () => window.matchMedia?.(NARROW).matches ?? false,
  );

  useEffect(() => {
    const query = window.matchMedia?.(NARROW);
    if (!query) return;
    const onChange = (e: MediaQueryListEvent) => setNarrow(e.matches);
    setNarrow(query.matches);
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);

  return narrow;
}

function readStoredDensity(): Density | null {
  // readPref is the guarded read (lib/prefs): storage can be unavailable
  // (private mode, blocked cookies), and a remembered preference is a
  // nicety -- losing it must not blank the page.
  const stored = readPref(DENSITY_KEY);
  return stored === "cards" || stored === "table" ? stored : null;
}

// They live in ./ranges now, so the container list and the host columns can
// read them without importing this page. Re-exported because App.tsx and
// this page's own tests have always taken them from here.
export { FLEET_RANGES, FLEET_RANGE_VALUES } from "./ranges";
import { FLEET_RANGES } from "./ranges";

const DENSITIES: { value: Density; label: string }[] = [
  { value: "table", label: "Table" },
  { value: "cards", label: "Cards" },
];

export interface FleetPageProps {
  /** Injected rows. When omitted the page fetches its own. */
  rows?: readonly HostRow[];
  /** Injected containers. When omitted the page fetches its own. */
  containers?: readonly ContainerRow[];
  /**
   * Conditions for the attention band.
   *
   * Derived from the rows by default -- see conditions.ts. It used to
   * default to [], with a note that computing them was a separate alerting
   * workstream, which meant the band was dead code and this page said
   * "nothing needs attention" beside a host whose own page was showing three
   * OOM kills in red.
   *
   * Still injectable, for tests and for a future alerting engine that will
   * know things a row cannot.
   */
  conditions?: Condition[];
  entity?: Entity;
  density?: Density;
  /**
   * When the fleet was last read, for the all-clear line. Spec 4.3: the
   * quiet line exists to confirm the check RAN, so a page handed its data
   * from outside (Wave 5's poller, or a test) must be able to say when.
   */
  checkedAt?: string | null;
  /** Injectable so tests are deterministic instead of racing the clock. */
  now?: Date;
  /**
   * The controlled half of this page's state. Each of these is optional and
   * each falls back to the internal state below, because the page is also
   * rendered on its own in tests: passing them hands the URL the authority
   * instead, which is what makes a filtered, ranged view a link someone can
   * send (spec 9).
   */
  /** Set by a caller that fetched the containers itself, when some hosts
   * could not be asked. Partial data must say it is partial: a list quietly
   * missing three hosts looks exactly like three hosts running none. */
  containerError?: string | null;
  range?: Range;
  onRangeChange?: (range: Range) => void;
  onEntityChange?: (entity: Entity) => void;
  onDensityChange?: (density: Density) => void;
}

export function FleetPage({
  rows,
  containers,
  conditions: injectedConditions,
  entity: controlledEntity = "hosts",
  density: controlledDensity,
  checkedAt: injectedCheckedAt,
  containerError: injectedContainerError,
  now = new Date(),
  range: controlledRange,
  onRangeChange,
  onEntityChange,
  onDensityChange,
}: FleetPageProps) {
  const [localEntity, setLocalEntity] = useState<Entity>(controlledEntity);
  const [localDensity, setLocalDensity] = useState<Density>(
    () => controlledDensity ?? readStoredDensity() ?? "table",
  );
  const [localRange, setLocalRange] = useState<Range>(controlledRange ?? "24h");

  // Controlled when the caller supplies both the value and the setter,
  // uncontrolled otherwise. Half a pair is a value that cannot change, so
  // the setter is what decides.
  const entity = onEntityChange ? controlledEntity : localEntity;
  const setEntity = onEntityChange ?? setLocalEntity;
  const density = onDensityChange
    ? (controlledDensity ?? localDensity)
    : localDensity;
  const setDensity = onDensityChange ?? setLocalDensity;
  const range = onRangeChange ? (controlledRange ?? localRange) : localRange;
  const setRange = onRangeChange ?? setLocalRange;
  const [filter, setFilter] = useState("");
  // Below the mobile breakpoint cards are automatic, not a preference (spec
  // 4.5): a six-column host table does not survive 390px, and a stored
  // "table" choice must not be able to produce a page that scrolls
  // sideways. The stored preference is left alone -- it is what the browser
  // goes back to at a width where it applies.
  const narrow = useIsNarrow();
  const filterRef = useSlashToFocus();
  const effectiveDensity: Density = narrow ? "cards" : density;

  const [fetchedRows, setFetchedRows] = useState<HostRow[] | null>(null);
  const [fetchedContainers, setFetchedContainers] = useState<
    ContainerRow[] | null
  >(null);
  const [error, setError] = useState<string | null>(null);
  const [fetchedContainerError, setFetchedContainerError] = useState<
    string | null
  >(null);
  const [fetchedCheckedAt, setFetchedCheckedAt] = useState<string | null>(null);

  const injected = rows !== undefined;

  useEffect(() => {
    if (injected) return;
    let live = true;
    void (async () => {
      let hosts: Host[];
      try {
        const [fetchedHosts, sites] = await Promise.all([
          getHosts(),
          getSites(),
        ]);
        hosts = fetchedHosts;
        if (!live) return;
        setFetchedRows(buildHostRows(hosts, sites));
        setFetchedCheckedAt(new Date().toISOString());
      } catch (err) {
        if (!live) return;
        setError(describe(err));
        return;
      }
      // There is no fleet-wide container endpoint, only the per-host one, so
      // the fleet list is a fan-out -- and it is caught separately: a failing
      // container call must not claim the host list that already rendered
      // could not be loaded. Containers simply stay unknown.
      // allSettled, not all: one host answering 500 must not take every
      // other host's containers off the page. A rejected batch left the
      // whole list empty and the tile reading "-", which says the fleet runs
      // no containers rather than that one host could not be asked.
      const settled = await Promise.allSettled(
        hosts.map(async (host) => {
          const list = await getContainers(host.id);
          return list.map((container: Container) => ({
            ...container,
            host_id: host.id,
            hostname: host.hostname,
          }));
        }),
      );
      if (!live) return;
      const rows = settled
        .filter(
          (r): r is PromiseFulfilledResult<ContainerRow[]> =>
            r.status === "fulfilled",
        )
        .flatMap((r) => r.value);
      const failed = settled.filter((r) => r.status === "rejected").length;
      setFetchedContainers(rows);
      // Partial data must say it is partial: a list quietly missing three
      // hosts' containers looks exactly like three hosts running none.
      setFetchedContainerError(
        settled.some((r) => r.status === "rejected")
          ? `${failed} host${failed === 1 ? "" : "s"} could not be asked for containers`
          : null,
      );
    })();
    return () => {
      // Wave 5's usePoll will own refresh; this only stops a late response
      // from writing into an unmounted page.
      live = false;
    };
  }, [injected]);

  const checkedAt = injectedCheckedAt ?? fetchedCheckedAt;
  const hostRows = rows ?? fetchedRows ?? [];
  const containerRows = containers ?? fetchedContainers ?? [];
  const containerError = injectedContainerError ?? fetchedContainerError;
  // Distinguishes "this fleet runs no containers" from "not fetched yet":
  // the tile may only say 0 for the first.
  const containersKnown =
    containers !== undefined || fetchedContainers !== null;

  const needle = filter.trim().toLowerCase();
  const visibleHosts = hostRows.filter(
    (row) =>
      needle === "" ||
      row.hostname.toLowerCase().includes(needle) ||
      (row.site_name ?? "").toLowerCase().includes(needle),
  );
  const visibleContainers = containerRows.filter(
    (row) =>
      needle === "" ||
      (row.name ?? "").toLowerCase().includes(needle) ||
      (row.image ?? "").toLowerCase().includes(needle) ||
      row.hostname.toLowerCase().includes(needle),
  );

  const reporting = hostRows.filter((row) => isReporting(row, now)).length;
  // Derived from the rows unless a caller supplied its own. Computed over
  // hostRows rather than visibleHosts on purpose: a filter is someone
  // looking for one machine, and hiding a critical host because its name
  // does not match what was typed is exactly how an overview lies.
  const shown = injectedConditions ?? fleetConditions(hostRows, now);
  const troubled = hostsNeedingAttention(shown);

  return (
    <>
      {error !== null ? (
        <p className="note" role="alert">
          The fleet could not be loaded: {error}
        </p>
      ) : null}
      {containerError !== null ? (
        <p className="note" role="alert">
          The hosts loaded, but their containers did not: {containerError}
        </p>
      ) : null}

      {shown.length > 0 ? (
        <>
          {/* The count above the band, in the same place the all-clear line
              sits when there is nothing wrong -- so the page answers "how
              much of my fleet is in trouble" in one line whichever state it
              is in, rather than only when the answer is none. */}
          <p className="allclear">
            <strong>
              {troubled} of {hostRows.length} host
              {hostRows.length === 1 ? "" : "s"}
            </strong>{" "}
            need{troubled === 1 ? "s" : ""} attention
            {checkedAt === null ? "" : ` · checked ${relative(checkedAt, now)}`}
          </p>
          <AttentionBand conditions={shown} />
        </>
      ) : (
        // Not a green "all clear" card: a permanently present banner is one
        // people stop reading. One quiet line that still confirms the check
        // ran (spec 4.3).
        <p className="allclear">
          All {reporting} host{reporting === 1 ? "" : "s"} reporting · nothing
          needs attention
          {checkedAt === null ? "" : ` · checked ${relative(checkedAt, now)}`}
        </p>
      )}

      <div className="tiles">
        {/* The first two tiles count a set the page can show, and sit
            directly above the tabs that show it -- so they are the control
            they already looked like. Their hrefs match the tabs' own, which
            is what keeps them bookmarkable and what makes clicking one the
            same act as clicking the tab. */}
        <StatTile
          label="Hosts reporting"
          value={reporting}
          detail={`of ${hostRows.length} known`}
          href="/"
          onSelect={() => setEntity("hosts")}
        />
        <StatTile
          label="Containers"
          value={containersKnown ? containerRows.length : ABSENT}
          detail="across the fleet"
          href="/?entity=containers"
          onSelect={() => setEntity("containers")}
        />
        {/* No href: fleet traffic is a rate, not a set, so there is no list
            of it to go to. A tile that looks clickable and does nothing is
            worse than one that plainly is not. */}
        <StatTile
          label="Fleet traffic"
          value={fleetTraffic(hostRows, now)}
          // ingress + egress, the words this app already uses for the two
          // directions: Graphs.tsx names its bands that ("not rx and tx --
          // the direction is the point of this chart"), and both traffic
          // sparklines announce themselves as "Ingress and egress over time".
          // "inbound + outbound" was a third spelling of the same pair, on
          // the one tile summarising all of them.
          // "latest sample" was only true at 1h; at the wider ranges the
          // number was a five-minute average from a quarter of an hour ago.
          // It is a gauge off host_current now, so "right now" is accurate
          // at every range -- which is the point of the change.
          detail="ingress + egress, right now"
        />
      </div>

      <Tabs
        items={[
          { id: "hosts", label: "Hosts", href: "/" },
          {
            id: "containers",
            label: "Containers",
            href: "/?entity=containers",
          },
        ]}
        active={entity}
        // Hand-rolled routing arrives in Wave 5; until then the tab is local
        // state and the href is what makes it bookmarkable once it does.
        onChange={(id) => setEntity(id as Entity)}
      />

      <div className="toolbar">
        <Input
          ref={filterRef}
          type="search"
          value={filter}
          placeholder={
            entity === "hosts" ? "Filter hosts" : "Filter containers"
          }
          aria-label={entity === "hosts" ? "Filter hosts" : "Filter containers"}
          onChange={(e) => setFilter(e.target.value)}
        />
        <div className="spacer" />
        <Segmented options={FLEET_RANGES} value={range} onChange={setRange} />
        {/* Density is a hosts-only axis: a card grid of 247 containers is
            not useful (spec 4.5). */}
        {entity === "hosts" ? (
          <Segmented
            options={DENSITIES}
            value={effectiveDensity}
            // The store write happens here only when nothing controls this
            // page. Controlled, the screen owns both halves -- writing the
            // preference and putting it in the URL -- and a second write
            // from inside would be two paths to one key.
            onChange={(next) => {
              setDensity(next);
              if (onDensityChange === undefined) writePref(DENSITY_KEY, next);
            }}
          />
        ) : null}
      </div>

      {entity === "hosts" ? (
        effectiveDensity === "table" ? (
          <HostTable rows={visibleHosts} range={range} />
        ) : (
          <HostCards rows={visibleHosts} range={range} />
        )
      ) : (
        <FleetContainers
          rows={visibleContainers}
          // showHost, even though the list groups by host and every row sits
          // under its hostname. The group header used to be enough because it
          // was always in view; now that a group can be shut and opened, a
          // reader who unfolds a host running thirteen containers scrolls its
          // heading off the top and is left with a page of rows that do not
          // say where they run. The column costs width and repeats the
          // heading; a container whose host cannot be identified costs more.
          showHost
          loaded={containersKnown}
          range={range}
          // hostRows, not visibleHosts: the point of the note is a host that
          // contributed NO container rows, and the filter is about the rows
          // that are there. A host filtered out of the list is exactly the
          // host whose absence still needs explaining.
          hosts={hostRows}
          // `rows` above is already filtered, so FleetContainers cannot tell
          // "the fleet has none" from "your search matched none" -- and the
          // capability note must not answer the second.
          filtered={needle !== ""}
        />
      )}
    </>
  );
}

// An ApiError's status is the difference between "the hub said no" and "the
// browser could not reach it", and the reader needs to know which.
function describe(err: unknown): string {
  return err instanceof ApiError
    ? `${err.message} (HTTP ${err.status})`
    : String(err);
}

// Sums the current inbound and outbound rate across the fleet. A fleet whose
// hosts have reported no rate at all has an UNKNOWN throughput, not a
// throughput of nothing, so this returns the absent marker rather than
// "0 b/s" -- which would read as a fleet with the network down.
//
// The scalars from host_current, not the end of the sparkline series. Off
// the series this number moved whenever the range moved: the range picks the
// step, the step picks the storage tier, and 1h answered the raw
// instantaneous rate where 6h and 24h answered a five-minute average that
// had ended a quarter of an hour earlier. The tile said "latest sample" and
// meant it at exactly one of the three settings.
//
// It also stopped jittering between polls at a FIXED range: on the 60s grid
// a scrape landing more than about half a bucket before `now` fell into the
// second-to-last slot, the old trailing-null check skipped that host, and it
// silently left the fleet total until its next post. A gauge has no grid and
// no last slot.
//
// A host that is not reporting is skipped entirely. The gauge is the one
// thing about it that does NOT go absent when its agent dies -- host_current
// keeps the last written pair, and the upsert's coalesce is there to make
// sure it keeps it -- so without this the tile would count a machine that
// has been powered off for a week at its final rate. The trailing-null check
// this replaced did that job by accident; isReporting does it on purpose,
// and it is the same predicate the "Hosts reporting" tile directly above
// uses, so the two tiles cannot disagree about which hosts exist right now.
function fleetTraffic(rows: readonly HostRow[], now: Date): string {
  let total = 0;
  let any = false;
  for (const row of rows) {
    if (!isReporting(row, now)) continue;
    for (const rate of [row.net_rx_bytes, row.net_tx_bytes]) {
      // A host that has never reported traffic -- or whose net collector is
      // off -- contributes nothing rather than a zero, so it cannot drag the
      // fleet's throughput down towards "the network is quiet".
      if (rate == null) continue;
      any = true;
      total += rate;
    }
  }
  // byterate, never bitrate. rx_bytes/tx_bytes are BYTES per second --
  // network.go divides a byte delta by the elapsed seconds -- so bitrate()
  // labelled the fleet's throughput "Mb/s" while every host row beside it
  // said MB/s, off by a factor of eight and entirely plausible. This is the
  // third copy of that bug: #51 fixed the fleet row's traffic cell and the
  // host overview's traffic card and missed the tile above both of them.
  return any ? byterate(total) : ABSENT;
}
