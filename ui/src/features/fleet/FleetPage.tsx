import { useEffect, useRef, useState } from "react";
import {
  ApiError,
  getConditions,
  getFleetContainers,
  getHosts,
  type Container,
  type ConditionRow,
  type ConditionsResponse,
  type Host,
} from "../../lib/api";
import { ABSENT, byterate } from "../../lib/format";
import { Input } from "../../ui/Control";
import { Segmented } from "../../ui/Segmented";
import { StatFigure, StatRail } from "../../ui/StatRail";
import { AttentionCounts } from "./AttentionCounts";
import { AttentionBand } from "../../ui/AttentionBand";
import { ATTENTION_CAP, containerAttention } from "./containerAttention";
import {
  catalogueOf,
  EMPTY_CATALOGUE,
  filterKind,
  fleetConditions,
  groupByHost,
  groupByKind,
  hostsNeedingAttention,
  isConditionKind,
  kindLabel,
  kindSeverity,
  type AttentionFilter,
  type Catalogue,
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
   * What the HUB says is wrong, straight off /api/v1/conditions.
   *
   * These were derived here from whatever rows the page had fetched, which is
   * why four of the five kinds could not say when they began. The rows are
   * rendered into Conditions rather than computed -- see fleetConditions --
   * and the only thing this page still decides for itself is the never-seen
   * host, which the hub deliberately refuses to judge.
   *
   * Defaults to [] rather than to a derivation: with no answer from the hub
   * there is nothing to say, and `conditionError` below is what says so.
   */
  conditionRows?: readonly ConditionRow[];
  /**
   * The kind vocabulary, from the same response.
   *
   * Empty by default. Everything that reads it degrades honestly on an empty
   * one -- an unrecognised filter is "all", a kind with no entry keeps its own
   * name -- rather than falling back to a copy of the hub's table.
   */
  catalogue?: Catalogue;
  /**
   * Fully-formed conditions, bypassing the render step.
   *
   * For tests, and for anything that wants to put a specific list on screen
   * without a wire shape behind it.
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
  /** Set by a caller that asked the hub what is wrong and could not get an
   * answer. With no conditions every row below reads clean -- a fleet that
   * goes green because nobody looked is the exact failure the engine exists to
   * prevent, so it is said rather than looked like. */
  conditionError?: string | null;
  onEntityChange?: (entity: Entity) => void;
}

export function FleetPage({
  rows,
  containers,
  conditionRows,
  catalogue = EMPTY_CATALOGUE,
  conditions: injectedConditions,
  entity: controlledEntity = "hosts",
  attention: controlledAttention,
  onAttentionChange,
  attentionHref = (next) => (next === "all" ? "/" : `/?attn=${next}`),
  containerError: injectedContainerError,
  conditionError: injectedConditionError,
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
  const [fetchedConditions, setFetchedConditions] =
    useState<ConditionsResponse | null>(null);
  const [fetchedConditionError, setFetchedConditionError] = useState<
    string | null
  >(null);

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
      } catch (err) {
        if (!live) return;
        setError(describe(err));
        return;
      }
      // Two fleet-wide requests, together rather than one after the other --
      // they answer different questions and neither needs the other, so
      // awaiting them in sequence would make the container list wait a whole
      // round trip it has no reason to. App.tsx's polling path runs them in
      // one wave for the same reason.
      //
      // Each is caught on its own: a failing listing must not claim the host
      // list that already rendered could not be loaded. Containers simply stay
      // unknown; the conditions stay empty, and the note beneath the head is
      // what says so -- a fleet that quietly reads clean because netra could
      // not be asked is worse than one that admits it.
      const ids = hosts.map((host) => host.id);
      const [answer, listings] = await Promise.all([
        getConditions().catch(() => null),
        getFleetContainers(ids).catch(() => null),
      ]);
      if (!live) return;
      setFetchedConditions(answer ?? null);
      setFetchedConditionError(
        answer === null || answer === undefined
          ? "netra could not be asked what is wrong"
          : null,
      );

      const rows = hosts.flatMap((host) =>
        (listings?.get(host.id) ?? []).map((container: Container) => ({
          ...container,
          host_id: host.id,
          hostname: host.hostname,
          // The same denominators App's own poll attaches. This path builds
          // rows too -- it is the one a page rendered on its own takes -- and
          // omitting them here would leave half the fleet's containers with no
          // bar for a reason nothing on screen could explain.
          host_threads: host.threads,
          host_mem_total: host.mem_total,
        })),
      );
      setFetchedContainers(rows);
      // Missing data must say it is missing: an empty list looks exactly like
      // a fleet running no containers. The count is of hosts because that is
      // what the message states -- one request now answers for all of them, so
      // it is every host or none rather than the three it could once be.
      setFetchedContainerError(
        listings === null
          ? `${hosts.length} host${hosts.length === 1 ? "" : "s"} could not be asked for containers`
          : null,
      );
    })();
    return () => {
      // Wave 5's usePoll will own refresh; this only stops a late response
      // from writing into an unmounted page.
      live = false;
    };
  }, [injected]);

  const hostRows = rows ?? fetchedRows ?? [];
  const containerRows = containers ?? fetchedContainers ?? [];
  const containerError = injectedContainerError ?? fetchedContainerError;
  const conditionError = injectedConditionError ?? fetchedConditionError;
  const kindCatalogue =
    fetchedConditions !== null
      ? catalogueOf(fetchedConditions.kinds)
      : catalogue;
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
  // The hub's rows, rendered. Built over hostRows rather than visibleHosts on
  // purpose: a filter is someone looking for one machine, and hiding a
  // critical host because its name does not match what was typed is exactly
  // how an overview lies.
  const shown =
    injectedConditions ??
    fleetConditions(
      conditionRows ?? fetchedConditions?.conditions ?? [],
      hostRows,
      kindCatalogue,
      now,
    );
  const troubled = hostsNeedingAttention(shown);
  const groups = groupByHost(shown);
  const byHost = new Map<string, HostGroup>(groups.map((g) => [g.hostId, g]));
  const kinds = groupByKind(shown);
  // KindGroup counts hosts; a tile counts rows. One map rather than a
  // second component drawing the same chips.
  //
  // group.severity, not kindSeverity(group.kind): a kind enters at one
  // severity and groupByKind hands back the worst one actually on the fleet,
  // so a filesystem chip is a warning until some host crosses and it is not.
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
  // exactly: a severity added later that is neither would otherwise count a
  // host in `troubled` and leave it unreachable from either segment.
  const warningHosts = groups.filter((g) => hasSeverity(g, false)).length;

  // One `attn` param, read against the entity on screen. A container kind
  // reaching the host paths would be filterKind()'s problem and a host kind
  // reaching the container paths would match nothing, so each side narrows
  // the raw value to its own vocabulary and falls back to "all".
  const hostAttention: AttentionFilter =
    entity === "hosts" &&
    (attention === "critical" ||
      attention === "warning" ||
      isConditionKind(kindCatalogue, attention))
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
  const activeKind = filterKind(kindCatalogue, hostAttention);
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

  // The band's rows, over every container rather than the filtered ones -- see
  // the band's own note. The sentence is built here because only this page
  // knows how it routes to a container and to a host.
  const containerAttentionRows = containerAttention(containerRows, {
    now,
    sentence: (row, why) => (
      <>
        <a
          href={`/containers/${row.host_id}/${encodeURIComponent(row.container_key)}`}
        >
          {row.name ?? row.container_key}
        </a>
        {" on "}
        <a href={`/hosts/${row.host_id}/overview`}>{row.hostname}</a>
        {" — "}
        {why}
      </>
    ),
  });

  // Where an overflow line points: the worst kind present at that severity, so
  // "+ 12 more warnings" lands on the filter a reader would have chosen. The
  // tiles are already in rank order, so the first match is the worst.
  const worstKindAt = (severity: string) =>
    containerTiles.find((tile) => tile.severity === severity)?.kind ?? "all";

  const attentionContainers =
    containerKind === null
      ? visibleContainers
      : visibleContainers.filter(
          (row) =>
            containerStates.get(`${row.host_id}:${row.container_key}`)?.kind ===
            containerKind,
        );

  // The mark on the row itself, in place of the ordering that used to lift a
  // troubled host to the top: the row says which hosts to look at without
  // moving any of them. Only the severities that mean something is wrong --
  // a mark on every row would say nothing.
  //
  // It was a rail down the leading edge and a pill beside the hostname for
  // one commit, which is one fact drawn twice in one row. The pill won: a
  // rail can only be a hue, and needed a screen-reader-only word planted in
  // the first cell to survive without colour. See HostTable's `severity`.
  // Whether the hub raised `sporadic` on this host, read off the same groups
  // rowSeverity reads. The badge used to be counted in the browser over
  // whatever range the reader had picked, which made it a fact about the range
  // as much as about the host.
  const rowSporadic = (row: HostRow): boolean =>
    byHost.get(String(row.id))?.conditions.some((c) => c.kind === "sporadic") ??
    false;

  const rowSeverity = (row: HostRow): "warning" | "critical" | null => {
    const worst = byHost.get(String(row.id))?.worst.severity;
    if (worst === "critical" || worst === "warning") {
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
    // A named wrapper rather than a fragment: what is scoped to .fleet is the
    // list's own spacing and the toolbar above it. The list itself is the
    // framed, striped box every other table in the app draws -- it used to be
    // flush with the page instead, so which table style you got depended on
    // which page you were on.
    <div className="fleet">
      {error !== null ? (
        <p className="note" role="alert">
          The hosts could not be loaded: {error}
        </p>
      ) : null}
      {containerError !== null ? (
        <p className="note" role="alert">
          The hosts loaded, but their containers did not: {containerError}
        </p>
      ) : null}
      {conditionError !== null && conditionError !== undefined ? (
        <p className="note" role="alert">
          The hosts loaded, but {conditionError} — nothing below is judged, so a
          row with no mark on it means netra could not look rather than that it
          looked and found nothing.
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

      {/* The page says what it is, and what narrows it sits on the same line.
          The heading used to be a sentence -- "5 hosts, 1 needs attention" --
          which under a bar that already names the product said the fleet's
          size a second time and then said what the chips below it say. It
          names the LIST now, which is also what makes the two lists two
          pages: each is a title and a URL rather than one route read two
          ways. */}
      <div className="pagehead">
        <h1>{entity === "containers" ? "All containers" : "All hosts"}</h1>
      </div>

      {/* The ambient figures, on a rail rather than in cards. They were three
          cards with 28px numbers sitting directly under three ATTENTION cards
          with 28px numbers -- six cards of equal weight, so the fleet's
          problems and its inventory shouted the same and nothing said which
          to read first. The attention row is what this page is for; these are
          context for it, and are set as context. See StatRail. */}
      <StatRail>
        {/* The first two figures count a set that has a page of its own, so
            they are links to it -- the same two destinations the bar's first
            two glyphs carry. Real hrefs, which is what keeps them
            bookmarkable and what makes cmd-click work. */}
        <StatFigure
          value={reporting}
          // Pluralised: a one-host fleet read "of 1 hosts reporting".
          label={`of ${hostRows.length} host${
            hostRows.length === 1 ? "" : "s"
          } reporting`}
          href="/"
          onSelect={() => setEntity("hosts")}
        />
        {/* On both pages now. It used to be dropped on the containers view
              because the heading there had just counted them; the heading
              counts nothing any more, so this is the only thing on either
              page that says how many containers the fleet runs -- including
              the absent marker when nobody has answered yet. */}
        <StatFigure
          value={containersKnown ? containerRows.length : ABSENT}
          label="containers"
          href="/containers"
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
        {/* "12 s since last check" used to close this rail. It is gone: the
              page repolls on its own every POLL_MS, so the figure counted up
              to a minute and reset forever, and a reader who never has to act
              on it stops reading the line it sits in. What it was really
              confirming -- that the check RAN -- is said by the readings
              themselves going stale, and by a host's own "last seen". */}
      </StatRail>

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
      {/* What is wrong, in sentences, above the inventory rather than
          distributed through it. The same band the host page's Overview draws
          -- one component, because two renderings of one vocabulary is how
          this page and that one came to disagree about a host (#92).

          Over ALL the container rows, never the filtered ones: the band says
          what is wrong with the fleet, and a reader typing in the search box
          is narrowing what they are LOOKING for, not changing what is true.
          Same rule the counts line above it already follows.

          Capped, with a linked overflow: a fleet's band is unbounded where a
          host's is not, and one lossy host can put a series-gap row here for
          every container it runs. */}
      {entity === "containers" ? (
        <AttentionBand
          rows={containerAttentionRows}
          now={now}
          cap={ATTENTION_CAP}
          overflow={(severity, hidden) => (
            <a href={attentionHref(worstKindAt(severity) as FleetFilter)}>
              + {hidden} more {severity}
              {hidden === 1 ? "" : "s"}
            </a>
          )}
        />
      ) : null}

      {/* The row of controls that sit over the list: the severity segments on
          the left, the filter on the right. The filter used to hang off the
          right end of the title, which put it a block away from the segments
          it composes with; both narrow the same list, so both belong on the
          line directly above it, and the field keeps the same right edge it
          had. The toolbar is drawn whether or not there are segments -- an
          empty left half is a row with the filter in it, which is the row the
          list needs, and a control that moves up a block on a healthy fleet
          is a control the reader has to find twice.

          The segments are hosts only: every condition netra has is
          host-level, so on the containers list this control would offer three
          segments that all show the same rows. And only once something is
          wrong, since three segments reading "All 5 / Critical 0 / Warning 0"
          offer two choices that lead nowhere.

          The key that focuses the filter is said where the field is. "/" has
          focused it since useSlashToFocus was written and nothing on screen
          mentioned it, which makes a shortcut a thing you either already know
          or never learn. aria-hidden: it is a hint about the keyboard, and a
          screen-reader user reaching this field has not typed "/" to get
          here. */}
      <div className="toolbar">
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
                      kindSeverity(kindCatalogue, activeKind)) === "critical"
                  ? "critical"
                  : "warning"
            }
            onChange={(next) => setAttention(next as AttentionFilter)}
          />
        ) : null}
        <div className="spacer" />
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
            : ` with ${kindLabel(kindCatalogue, activeKind).toLowerCase()}`}{" "}
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
          {stateKindLabel(containerKind)} ·{" "}
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
          severity={rowSeverity}
          sporadic={rowSporadic}
          now={now}
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
