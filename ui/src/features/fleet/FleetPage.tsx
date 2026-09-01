import { useEffect, useRef, useState } from "react";
import {
  ApiError,
  getContainers,
  getHosts,
  type Container,
  type Host,
} from "../../lib/api";
import { ABSENT, byterate } from "../../lib/format";
import { Input } from "../../ui/Control";
import { Segmented } from "../../ui/Segmented";
import { StatFigure, StatRail } from "../../ui/StatRail";
import { AttentionCounts } from "./AttentionCounts";
import { SinceLastCheck } from "./SinceLastCheck";
import {
  filterKind,
  fleetConditions,
  groupByHost,
  groupByKind,
  hostsNeedingAttention,
  isConditionKind,
  kindLabel,
  kindSeverity,
  type AttentionFilter,
  type Condition,
  type HostGroup,
} from "./conditions";
import { FleetContainers, type ContainerRow } from "./FleetContainers";
import { containerState } from "../container/columns";
import {
  FILTERABLE_STATE_KINDS,
  isContainerStateKind,
  stateKindLabel,
  stateKindSeverity,
  type ContainerStateKind,
} from "../container/state";
import { HostTable } from "./HostTable";
import { hostLocation, type HostRow } from "./hostColumns";
import { isReporting } from "../../lib/host";
import { buildRows } from "./hostTrends";
import { FLEET_RANGE } from "./ranges";

/** What you are looking at (spec 4.5's first axis). */
export type Entity = "hosts" | "containers";

/**
 * What `?attn=` can carry, over both entities.
 *
 * One param, read against whichever set the entity names. The URL already
 * says which entity is on screen, so `?entity=containers&attn=silent` is a
 * silent CONTAINER and `?attn=silent` on the hosts tab is a silent host --
 * unambiguous without a prefix, and the one word the two vocabularies share
 * keeps its meaning across a tab switch rather than resetting the filter.
 */
export type FleetFilter = AttentionFilter | ContainerStateKind;

/**
 * Produces the rows the table renders from.
 *
 * Nothing is joined. `GET /api/v1/hosts` carries everything a row states
 * about where a host is, reported by that host's own agent -- this used to
 * fetch the sites table whole to resolve a name by `site_id`, and then the
 * providers table on top of it.
 *
 * The chart series come back empty here: this path has the host list and
 * nothing else, not the per-host metrics. App's poll fetches those and
 * builds the same rows with them (see hostTrends).
 */
export function buildHostRows(hosts: Host[]): HostRow[] {
  // One builder, in hostTrends: this page's self-fetching path and App's
  // polling path must not be able to disagree about what a row is. Without
  // trends every series is empty, which renders as a gap and as the absent
  // marker -- the truth, since not fetched is not zero.
  return buildRows(hosts, new Map());
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

// They live in ./ranges now, so the container list and the host columns can
// read them without importing this page. Re-exported because App.tsx and
// this page's own tests have always taken them from here.
export { FLEET_RANGE } from "./ranges";

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
  /**
   * Which part of what is wrong the reader asked for: everything, one
   * severity, or one condition kind.
   *
   * One value rather than a severity and a kind side by side, because they
   * are one question with one answer -- and because the Segmented that shows
   * it must always have exactly one option pressed. Picking a kind presses
   * the severity that kind is at; picking a severity clears the kind.
   */
  attention?: FleetFilter;
  onAttentionChange?: (next: FleetFilter) => void;
  /**
   * Where a filter link points.
   *
   * The default writes the filter and nothing else, which is right for a page
   * rendered on its own. A page whose URL is in charge passes one built from
   * the current query string instead: cmd-click and copy-link go to the href
   * rather than through onAttentionChange, and "/?attn=disk" would land the
   * recipient on a fleet with the entity reset -- exactly what App's own
   * comment says a shared fleet view must not do.
   */
  attentionHref?: (next: FleetFilter) => string;
  /**
   * When the fleet was last read, for the rail's "since last check" figure.
   * Spec 4.3: the page has to confirm the check RAN, so a page handed its
   * data from outside (Wave 5's poller, or a test) must be able to say when.
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
  onEntityChange?: (entity: Entity) => void;
}

export function FleetPage({
  rows,
  containers,
  conditions: injectedConditions,
  entity: controlledEntity = "hosts",
  attention: controlledAttention,
  onAttentionChange,
  attentionHref = (next) => (next === "all" ? "/" : `/?attn=${next}`),
  checkedAt: injectedCheckedAt,
  containerError: injectedContainerError,
  now = new Date(),
  onEntityChange,
}: FleetPageProps) {
  const [localEntity, setLocalEntity] = useState<Entity>(controlledEntity);
  const [localAttention, setLocalAttention] = useState<FleetFilter>(
    controlledAttention ?? "all",
  );

  // Controlled when the caller supplies both the value and the setter,
  // uncontrolled otherwise. Half a pair is a value that cannot change, so
  // the setter is what decides.
  const entity = onEntityChange ? controlledEntity : localEntity;
  const setEntity = onEntityChange ?? setLocalEntity;
  const attention = onAttentionChange
    ? (controlledAttention ?? "all")
    : localAttention;
  const setAttention = onAttentionChange ?? setLocalAttention;
  const [filter, setFilter] = useState("");
  // The one window every row is drawn over. Not a preference and not in the
  // URL: see FLEET_RANGE.
  const range = FLEET_RANGE;
  const filterRef = useSlashToFocus();

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
        hosts = await getHosts();
        if (!live) return;
        setFetchedRows(buildHostRows(hosts));
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
      // What the row actually prints, so typing "OVH" or "Roubaix" narrows
      // the list -- a filter that cannot find what is on screen is broken.
      // The facility is matched too though the line omits it: an operator who
      // set AGENT_FACILITY=RBX2 will search for RBX2.
      (hostLocation(row) ?? "").toLowerCase().includes(needle) ||
      (row.facility ?? "").toLowerCase().includes(needle),
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
  const groups = groupByHost(shown);
  const byHost = new Map<string, HostGroup>(groups.map((g) => [g.hostId, g]));
  const kinds = groupByKind(shown);
  // KindGroup counts hosts; a tile counts rows. One map rather than a
  // second component drawing the same chips.
  const hostTiles = kinds.map((group) => ({
    kind: group.kind,
    label: group.label,
    severity: group.severity,
    ids: group.hostIds,
  }));
  // "Has something critical", not "is worst-critical", and the two buckets
  // therefore OVERLAP -- a host that is both silent and short of disk is in
  // both. They stop adding up to `troubled` and that is the trade, taken on
  // purpose: a kind is always a subset of the severity it is at, so picking
  // "failed units" from the counts line can press the Warning segment without
  // the two contradicting each other. Partitioned by worst, they could not --
  // thirty-one warned hosts that are also silent showed "Warning 0" pressed
  // above thirty-one rows.
  //
  // Each count still answers a question a reader actually asks: how many
  // machines have something critical on them.
  const hasSeverity = (group: HostGroup, critical: boolean): boolean =>
    group.conditions.some((c) => (c.severity === "critical") === critical);
  const criticalHosts = groups.filter((g) => hasSeverity(g, true)).length;
  // Everything that is not critical rather than severity === "warning"
  // exactly: `serious` is a severity the type allows and nothing currently
  // emits, and a host that started emitting it would otherwise be counted in
  // `troubled` and be unreachable by either segment.
  const warningHosts = groups.filter((g) => hasSeverity(g, false)).length;

  // One `attn` param, read against the entity on screen. A container kind
  // reaching the host paths would be filterKind()'s problem and a host kind
  // reaching the container paths would match nothing, so each side narrows
  // the raw value to its own vocabulary and falls back to "all".
  const hostAttention: AttentionFilter =
    entity === "hosts" &&
    (attention === "critical" ||
      attention === "warning" ||
      isConditionKind(attention))
      ? attention
      : "all";
  const containerKind: ContainerStateKind | null =
    entity === "containers" &&
    isContainerStateKind(attention) &&
    FILTERABLE_STATE_KINDS.includes(attention)
      ? attention
      : null;

  const matchesAttention = (row: HostRow): boolean => {
    const group = byHost.get(String(row.id));
    if (group === undefined) return false;
    if (activeKind !== null) {
      return group.conditions.some((c) => c.kind === activeKind);
    }
    return hasSeverity(group, hostAttention === "critical");
  };

  // From the filter itself, never from the conditions on screen: the last
  // host carrying a kind can recover between the link being sent and being
  // opened, and a page that cannot name the filter it is applying reads as
  // broken ("Showing 0 of 100 hosts with").
  const activeKind = filterKind(hostAttention);
  const filtered = hostAttention === "all";
  const attentionHosts = filtered
    ? visibleHosts
    : visibleHosts.filter(matchesAttention);

  // The container list's own counts line. Computed over containerRows rather
  // than the filtered ones, for the reason the host conditions are: a search
  // is someone looking for one container, and hiding a silent one because its
  // name does not match what was typed is how a counts line lies.
  //
  // The state is derived once per row here and reused for the filter below --
  // deriveState is cheap, but a fleet fan-out is several hundred rows and
  // calling it twice per row per render for the same answer is waste.
  const containerStates = new Map(
    containerRows.map((row) => [
      `${row.host_id}:${row.container_key}`,
      containerState(row, now),
    ]),
  );
  const containerTiles = FILTERABLE_STATE_KINDS.map((kind) => {
    const ids = containerRows
      .filter(
        (row) =>
          containerStates.get(`${row.host_id}:${row.container_key}`)?.kind ===
          kind,
      )
      .map((row) => `${row.host_id}:${row.container_key}`);
    return {
      kind,
      label: stateKindLabel(kind),
      severity: stateKindSeverity(kind),
      ids,
    };
  }).filter((tile) => tile.ids.length > 0);

  const attentionContainers =
    containerKind === null
      ? visibleContainers
      : visibleContainers.filter(
          (row) =>
            containerStates.get(`${row.host_id}:${row.container_key}`)?.kind ===
            containerKind,
        );

  // The mark on the row itself, in place of the ordering that used to lift a
  // troubled host to the top: a rail down the leading edge says which hosts
  // to look at without moving any of them. Only the severities that mean
  // something is wrong -- a rail on every row would say nothing.
  const railSeverity = (
    row: HostRow,
  ): "warning" | "serious" | "critical" | null => {
    const worst = byHost.get(String(row.id))?.worst.severity;
    if (worst === "critical" || worst === "serious" || worst === "warning") {
      return worst;
    }
    return null;
  };

  // The rows keep the order the API returned them in -- hostname order -- and
  // a troubled host is not lifted above a healthy one. Finding a machine you
  // came looking for beats being shown the worst one first, and the severity
  // is already legible in the row itself. The Table's own column sort still
  // takes over the moment a reader clicks a header.

  return (
    // A named wrapper rather than a fragment: this page's list is flush with
    // the page instead of framed as a card, and its toolbar closes the
    // chrome with a rule -- both are scoped to .fleet so the host page and
    // the container detail keep the card treatment they share with every
    // other table in the app.
    <div className="fleet">
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

      {/* What used to be a band of one block per host. Fifty warned hosts
          made fifty blocks, capped at twenty, with an overflow line that was
          not even a link -- so the conditions moved into the list below and
          this is what is left above it: one chip per KIND, which is one chip
          per problem however many machines have it.

          It sits directly under the head, which names the page and states
          its figures on one line: what is wrong reads as the first thing
          ABOUT the fleet rather than the first thing on the screen. The
          summary sentence that used to be here ("2 of 4 hosts need
          attention · 2 problems · checked 0 s ago") said in prose what the
          chips and the figures above them already say in figures.

          Nothing takes its place on a healthy fleet: an empty attention row
          IS the all-clear, and the rail underneath still confirms the check
          ran. A line that only ever reads "nothing needs attention" is a line
          people stop reading, which is the same reason the band it replaced
          was not a green card. */}

      {/* The ambient figures, on a rail rather than in cards.
          They were three cards with 28px numbers sitting directly under
          three ATTENTION cards with 28px numbers -- six cards of equal
          weight, so the fleet's problems and its inventory shouted the same
          and nothing said which to read first. The attention row is what
          this page is for; these are context for it, and are set as
          context. See StatRail. */}
      {/* The page says what it is. The fleet list had no heading at all: the
          first thing on it was a row of problem tiles, which reads as a
          dashboard fragment rather than as the page a bookmark lands on --
          and left the ambient figures under it with nothing to be subordinate
          TO.

          It names the LIST, not the route: the two entities are one route
          with a different query string and the rail marks them as two
          destinations, so a heading fixed at "Fleet" would contradict the
          rail on the containers view and mislabel the page for a screen
          reader landing on it. */}
      {/* The title and the figures are ONE line: the page names itself on
          the left, and what it currently holds reads off the right end of
          the same line. Stacked they were two bands of chrome above a list
          that had not started yet, which is what the chips below them are
          for. */}
      <div className="fleethead">
        <h1 className="fleettitle">
          {entity === "containers" ? "Containers" : "Fleet"}
        </h1>
        <StatRail>
          {/* The first two figures count a set the page can show, and sit
            directly above the tabs that show it -- so they are the control
            they already looked like. Their hrefs match the tabs' own, which
            is what keeps them bookmarkable and what makes clicking one the
            same act as clicking the tab. */}
          <StatFigure
            value={reporting}
            // Pluralised, like the all-clear sentence directly above it: a
            // one-host fleet read "of 1 hosts reporting" against a line already
            // saying "All 1 host reporting".
            label={`of ${hostRows.length} host${
              hostRows.length === 1 ? "" : "s"
            } reporting`}
            href="/"
            onSelect={() => setEntity("hosts")}
          />
          <StatFigure
            value={containersKnown ? containerRows.length : ABSENT}
            label="containers"
            href="/?entity=containers"
            onSelect={() => setEntity("containers")}
          />
          {/* No href: fleet traffic is a rate, not a set, so there is no list
            of it to go to. A figure that looks clickable and does nothing is
            worse than one that plainly is not. */}
          <StatFigure
            value={fleetTraffic(hostRows, now)}
            // "in + out", the words this app already uses for the two
            // directions: Graphs.tsx names its bands that ("not rx and tx --
            // the direction is the point of this chart"), and both traffic
            // sparklines announce themselves as "Traffic in and out over
            // time".
            //
            // The "right now" that used to qualify this is gone with the card
            // that had room for it. It stays true -- the number is a gauge off
            // host_current, not the latest bucket of a range -- and the rail
            // has no line for a qualifier that repeats for all three figures.
            label="in + out"
          />
          {/* The age of the poll, in the place the fleet's other ambient
            figures are read. It used to be the tail of a summary sentence
            above the page; the sentence is gone and this is the fact on it
            that was not said twice.

            Its own component because it is the only figure here that has to
            move between renders: computed from this page's `now` it read
            "0 s" forever, since the poll landing is both what sets the
            timestamp and what repaints the page. It owns a clock, and the
            reasons for `duration` over `relative` and for omitting rather
            than dashing moved there with it. */}
          <SinceLastCheck checkedAt={checkedAt} now={now} />
        </StatRail>
      </div>

      {/* One counts line, reading whichever entity is on screen. The host
          conditions and the container states are different vocabularies over
          different rows, and a line showing both at once would be counting
          two things in one row of chips. `as` on the way out: AttentionCounts
          is structural over any kind, and what comes back is one of the
          kinds this page just handed it. */}
      {entity === "hosts" && shown.length > 0 ? (
        <AttentionCounts
          kinds={hostTiles}
          active={activeKind}
          href={(next) => attentionHref(next as FleetFilter)}
          onSelect={(next) => setAttention(next as FleetFilter)}
        />
      ) : null}
      {entity === "containers" && containerTiles.length > 0 ? (
        <AttentionCounts
          kinds={containerTiles}
          active={containerKind}
          href={(next) => attentionHref(next as FleetFilter)}
          onSelect={(next) => setAttention(next as FleetFilter)}
        />
      ) : null}

      <div className="toolbar">
        {/* The key that focuses this field, said where the field is. "/" has
            focused the filter since useSlashToFocus was written and nothing
            on screen mentioned it, which makes a shortcut a thing you either
            already know or never learn. aria-hidden: it is a hint about the
            keyboard, and a screen-reader user reaching this field has not
            typed "/" to get here. */}
        <div className="filterbox">
          <Input
            ref={filterRef}
            type="search"
            value={filter}
            placeholder={
              entity === "hosts" ? "Filter hosts" : "Filter containers"
            }
            aria-label={
              entity === "hosts" ? "Filter hosts" : "Filter containers"
            }
            onChange={(e) => setFilter(e.target.value)}
          />
          <span className="kbd" aria-hidden="true">
            /
          </span>
        </div>
        {/* Left of the spacer, beside the filter it composes with: both of
            them change WHICH hosts are in the list. Hosts only -- every
            condition netra has is host-level, so on the Containers tab this
            control would offer three segments that all show the same list. */}
        {entity === "hosts" && troubled > 0 ? (
          <Segmented
            options={[
              { value: "all", label: `All ${hostRows.length}` },
              { value: "critical", label: `Critical ${criticalHosts}` },
              { value: "warning", label: `Warning ${warningHosts}` },
            ]}
            // A kind is a severity's subset, so the segment that contains it
            // stays pressed while it is chosen -- Segmented's contract is
            // exactly one pressed option, and a kind chosen from the counts
            // line above must not leave all three looking unselected.
            // A kind is a severity's subset, so the segment that contains it
            // stays pressed while it is chosen -- Segmented's contract is
            // exactly one pressed option, and a kind chosen from the counts
            // line above must not leave all three looking unselected. The
            // observed severity when the kind is present (disk warns at 90%
            // and turns critical at 95%), its entry severity when every host
            // carrying it has recovered.
            value={
              activeKind === null
                ? attention
                : (kinds.find((k) => k.kind === activeKind)?.severity ??
                      kindSeverity(activeKind)) === "critical"
                  ? "critical"
                  : "warning"
            }
            onChange={(next) => setAttention(next as AttentionFilter)}
          />
        ) : null}
        <div className="spacer" />
      </div>

      {entity === "hosts" && !filtered ? (
        // Says what was left out, and how to stop leaving it out. The band's
        // own overflow line was the counter-example: "+30 more hosts" with
        // nothing to click.
        <p className="countline">
          Showing <strong>{attentionHosts.length}</strong> of {hostRows.length}{" "}
          host{hostRows.length === 1 ? "" : "s"}
          {activeKind === null
            ? ` with something ${attention}`
            : ` with ${kindLabel(activeKind).toLowerCase()}`}{" "}
          ·{" "}
          <a
            href={attentionHref("all")}
            onClick={(event) => {
              if (
                event.defaultPrevented ||
                event.button !== 0 ||
                event.metaKey ||
                event.ctrlKey ||
                event.shiftKey ||
                event.altKey
              ) {
                return;
              }
              event.preventDefault();
              setAttention("all");
            }}
          >
            show all
          </a>
        </p>
      ) : null}

      {entity === "containers" && containerKind !== null ? (
        // The same escape, for the same reason, and it must not be gated on
        // the chip being there: the counts line drops a kind once nothing
        // carries it, so opening a link to `?attn=silent` after the last
        // silent container recovered would otherwise leave an empty list, no
        // chip, and no control anywhere to clear the filter.
        <p className="countline">
          Showing <strong>{attentionContainers.length}</strong> of{" "}
          {containerRows.length} container
          {containerRows.length === 1 ? "" : "s"} with{" "}
          {stateKindLabel(containerKind).toLowerCase()} ·{" "}
          <a
            href={attentionHref("all")}
            onClick={(event) => {
              if (
                event.defaultPrevented ||
                event.button !== 0 ||
                event.metaKey ||
                event.ctrlKey ||
                event.shiftKey ||
                event.altKey
              ) {
                return;
              }
              event.preventDefault();
              setAttention("all");
            }}
          >
            show all
          </a>
        </p>
      ) : null}

      {entity === "hosts" ? (
        <HostTable
          rows={attentionHosts}
          range={range}
          severity={railSeverity}
          filtered={hostRows.length > 0}
        />
      ) : (
        <FleetContainers
          rows={attentionContainers}
          // No showHost. The column repeated the group heading on every row --
          // eighty-four rows saying what four headings already said, in the
          // widest table on the page. It was added because an opened group's
          // heading scrolls off the top, which is true and is not worth the
          // repetition: the heading is one scroll away, and the rows it
          // annotates carry a name, an image and two charts that are what a
          // reader came for. See the note in FleetContainers.
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
          // The status chips narrow the list exactly as the search box does,
          // so an empty result after picking one is "your filter matched
          // nothing", never "this fleet runs no containers".
          filtered={needle !== "" || containerKind !== null}
          now={now}
        />
      )}
    </div>
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
