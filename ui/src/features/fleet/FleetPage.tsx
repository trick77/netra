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
import { ABSENT, bitrate, relative } from "../../lib/format";
import { Input } from "../../ui/Control";
import { Segmented } from "../../ui/Segmented";
import { StatTile } from "../../ui/StatTile";
import { Tabs } from "../../ui/Tabs";
import { AttentionBand, type Condition } from "./AttentionBand";
import { AllHostsOverlay, fromHostRows } from "./AllHostsOverlay";
import { FleetContainers, type ContainerRow } from "./FleetContainers";
import { HostCards } from "./HostCards";
import { HostTable } from "./HostTable";
import type { HostRow, Range } from "./hostColumns";
import { isReporting } from "../../lib/host";
import { buildRows } from "./hostTrends";

/** What you are looking at (spec 4.5's first axis). */
export type Entity = "hosts" | "containers";
/** How densely (spec 4.5's second axis). Independent of the entity. */
export type Density = "table" | "cards";

/** Spec 4.5: density is remembered per browser and defaulted in Settings.
 * Exported so Settings (Task 19) writes the same key rather than a second
 * one that silently disagrees with this page. */
export const DENSITY_KEY = "netra.fleet.density";

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
  try {
    const stored = window.localStorage.getItem(DENSITY_KEY);
    return stored === "cards" || stored === "table" ? stored : null;
  } catch {
    // Storage can be unavailable (private mode, blocked cookies). A
    // remembered preference is a nicety; losing it must not blank the page.
    return null;
  }
}

const RANGES: { value: Range; label: string }[] = [
  { value: "1h", label: "1h" },
  { value: "6h", label: "6h" },
  { value: "24h", label: "24h" },
];

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
   * Conditions for the attention band. Nothing computes these yet -- the
   * alerting engine is a separate Stage 2 workstream -- so the default is
   * "nothing is wrong", which renders the quiet all-clear line rather than
   * a permanent green banner (spec 4.3).
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
  conditions = [],
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

      {conditions.length > 0 ? (
        <AttentionBand conditions={conditions} />
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
        <StatTile
          label="Hosts reporting"
          value={reporting}
          detail={`of ${hostRows.length} known`}
        />
        <StatTile
          label="Containers"
          value={containersKnown ? containerRows.length : ABSENT}
          detail="across the fleet"
        />
        <StatTile
          label="Fleet traffic"
          value={fleetTraffic(hostRows)}
          detail="inbound + outbound, latest sample"
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
        <Segmented options={RANGES} value={range} onChange={setRange} />
        {/* Density is a hosts-only axis: a card grid of 247 containers is
            not useful (spec 4.5). */}
        {entity === "hosts" ? (
          <Segmented
            options={DENSITIES}
            value={effectiveDensity}
            onChange={(next) => {
              setDensity(next);
              try {
                window.localStorage.setItem(DENSITY_KEY, next);
              } catch {
                // See readStoredDensity: an unavailable store costs the
                // preference, never the page.
              }
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
          showHost
          loaded={containersKnown}
        />
      )}

      {/* The overlay compares hosts, so it belongs to the host view: under a
          container list it would answer a question nobody asked. */}
      {entity === "hosts" ? (
        <AllHostsOverlay hosts={fromHostRows(visibleHosts)} />
      ) : null}
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

// Sums the latest inbound and outbound rate across the fleet. A fleet whose
// hosts have reported no rate at all has an UNKNOWN throughput, not a
// throughput of nothing, so this returns the absent marker rather than
// "0 b/s" -- which would read as a fleet with the network down.
function fleetTraffic(rows: readonly HostRow[]): string {
  let total = 0;
  let any = false;
  for (const row of rows) {
    for (const series of [row.rx, row.tx]) {
      // `== null` covers both an empty series and a trailing gap: a host
      // that stopped reporting contributes nothing to the fleet total
      // rather than its last known rate, which would keep counting traffic
      // for a host that is down.
      const last = series.at(-1);
      if (last == null) continue;
      any = true;
      total += last;
    }
  }
  return any ? bitrate(total) : ABSENT;
}
