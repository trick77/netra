import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ApiError,
  getContainers,
  getConditions,
  getFleetContainers,
  getEvents,
  getHost,
  getHosts,
  getMetrics,
  type Container,
  type Event,
  type Host,
  type MetricsResponse,
} from "./lib/api";
import { POLL_MS, usePoll } from "./lib/poll";
import {
  rangeFromSearch,
  useLocation,
  withParam,
  type Route,
} from "./lib/router";
import { clampRange, rangeWindow, EVENT_LIMITS, type Range } from "./lib/range";
import { RANGE_KEY, writePref } from "./lib/prefs";
import { EmptyState } from "./ui/EmptyState";
import {
  FleetPage,
  FLEET_RANGE,
  type Entity,
  type FleetFilter,
} from "./features/fleet/FleetPage";
import {
  catalogueOf,
  diskThresholds,
  EMPTY_CATALOGUE,
  isConditionKind,
} from "./features/fleet/conditions";
import { isContainerStateKind } from "./features/container/state";
import {
  buildRows,
  fetchFleetContainerTrends,
  fetchFleetTrends,
} from "./features/fleet/hostTrends";
import type { ContainerRow } from "./features/fleet/FleetContainers";
import {
  HostPage,
  RANGE_VALUES as HOST_RANGE_VALUES,
  type HostTab,
} from "./features/host/HostPage";
import {
  ContainerPage,
  CONTAINER_RANGE_VALUES,
} from "./features/container/ContainerPage";
import {
  EventsPage,
  EVENT_RANGE_VALUES,
  filtersFromQuery,
  filtersToQuery,
} from "./features/events/EventsPage";
import { SettingsPage, loadRange } from "./features/settings/SettingsPage";
import { LoginPage } from "./features/auth/LoginPage";
import { HostAdminPage } from "./features/admin/HostAdminPage";
import {
  Bell,
  CircleSlash,
  KeyRound,
  LayoutGrid,
  LogOut,
  Plus,
  Server,
  Settings2,
  type LucideIcon,
} from "lucide-react";

/**
 * The composition root: it owns the URL, the polling, and the one decision
 * every page shares -- what to do when the session has expired.
 *
 * Pages take their data as props and navigate through callbacks. That is
 * what let five of them be written in parallel, and it is also what keeps
 * them testable without a router or a fetch mock; this file is the only
 * place that knows both.
 */
export default function App() {
  const { route, search, navigate } = useLocation();

  const go = useCallback(
    (to: string, options?: { replace?: boolean }) => navigate(to, options),
    [navigate],
  );

  const onClick = useDelegatedNavigation(go);

  useDocumentTitle(route);

  return (
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions
    <div className="app" onClick={onClick}>
      {/* A keyboard user should not have to walk the nav and the toolbar on
          every page to reach the thing they came for. Visible only when
          focused, which is the one moment it is useful. */}
      <a className="skip" href="#main">
        Skip to content
      </a>
      {/* Still a <header>, and still the banner landmark: only its position
          moved -- across the top, where the wordmark, the glyphs and the one
          action the app has read as one line. HostPage's own header is
          disambiguated from this one by accessible name, not by there being
          exactly one. */}
      <header className="topbar">
        {/* The wordmark is the link home. The rail had none -- 56px did not
            hold one -- and its first item carried "/" instead; a bar has the
            width, and a product that never says its own name reads as a page
            someone else built. */}
        <a className="wordmark" href="/">
          Netra
        </a>
        <div className="spacer" />
        {/* Named because it is not the only nav landmark on a page -- Tabs
            renders one too -- and "navigation" twice over tells a screen
            reader user nothing about which is which. */}
        <nav className="nav" aria-label="Primary">
          <div className="navgroup">
            {/* The bar carries no labels, so each glyph has to name its
                destination on its own. A dial said "utilisation", which is a
                reading the fleet page takes rather than the thing it lists;
                a server says hosts. Containers get the four tiles the page
                actually renders. */}
            {/* A detail page marks the list it came out of. Neither of these
                lit at all on a host or a container page, so the bar said
                "nowhere" on the two pages a reader spends the most time in --
                and the way back to the list was the one thing they needed it
                to point at. */}
            <NavLink
              href="/"
              icon={Server}
              active={here(route.name, "fleet", "host")}
            >
              Hosts
            </NavLink>
            {/* Its own route now, so this splits on the route name like every
                other entry. While the entity lived in the query string this
                had to read the parameter, and both entries lit at once on any
                URL that lost it. */}
            <NavLink
              href="/containers"
              icon={LayoutGrid}
              active={here(route.name, "containers", "container")}
            >
              Containers
            </NavLink>
            <NavLink
              href="/events"
              icon={Bell}
              active={here(route.name, "events")}
            >
              Events
            </NavLink>
            {/* "Agents", not "Hosts", now that the list of machines is called
                Hosts: this is where an agent's token is minted and the
                command that installs it is shown, which is the fleet's other
                half rather than a second list of the same machines. Two
                entries both reading "Hosts" would have been the rail
                contradicting itself. */}
            <NavLink
              href="/admin/hosts"
              icon={KeyRound}
              active={here(route.name, "admin")}
            >
              Agents
            </NavLink>
            {/* Settings and sign out sat in a second group at the FOOT of the
                rail, with a rule above them, because Settings is the one
                destination here that is not about the fleet. Glyphs and no
                words could not carry that distinction: what a reader saw was
                two icons marooned at the bottom of an empty column, far enough
                from the other four to read as a different control rather than
                as the rest of the same list. One run, in reading order. */}
            <NavLink
              href="/settings"
              icon={Settings2}
              active={here(route.name, "settings")}
            >
              Settings
            </NavLink>
            {/* A form, not a link: POST /logout is what clears the cookie, and
                a GET would let a prefetch or a link-scanner sign someone out.
                Posting for real also means sign-out still works when the
                bundle does not -- the same reason the login page it lands on
                is server-rendered. */}
            <form method="post" action="/logout" className="navform">
              <button type="submit" data-tip="Sign out">
                <LogOut aria-hidden="true" />
                <span className="sr-only">Sign out</span>
              </button>
            </form>
          </div>
        </nav>
        {/* The one action the app has, at the end of the line where an action
            belongs. It goes to Agents, which is where a token is minted and
            the install command is shown -- that page keeps its own button
            until adding a host is a dialog this can open. */}
        <div className="topbar-sep" aria-hidden="true" />
        <a className="btn addhost" href="/admin/hosts">
          <Plus aria-hidden="true" />
          Add host
        </a>
      </header>
      <main id="main" tabIndex={-1}>
        <Screen route={route} search={search} go={go} />
      </main>
    </div>
  );
}

type Go = (to: string, options?: { replace?: boolean }) => void;

/**
 * One click handler for every internal link in the app, delegated at the
 * root.
 *
 * The pages render plain anchors -- the fleet list into host detail, the
 * attention band into a host, the host tab bar, the container links -- and
 * every one of them must keep working as an anchor: middle-click, copy-link
 * and bookmark are not optional in a monitoring tool people paste URLs from.
 * Delegating here means none of those components needs a navigate callback
 * threaded down to it, and a link added later is routed without anyone
 * remembering to wire it.
 *
 * It steps aside for everything the browser owns: modifier keys, non-primary
 * buttons, target=_blank, download, and any anchor pointing off-origin.
 */
function useDelegatedNavigation(go: Go) {
  return (event: React.MouseEvent) => {
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
    const anchor = (event.target as HTMLElement | null)?.closest?.("a");
    if (!anchor) return;
    const href = anchor.getAttribute("href");
    if (href === null || anchor.hasAttribute("download")) return;
    if (anchor.target && anchor.target !== "_self") return;
    // Resolved against the CURRENT document, not the origin: a relative href
    // means "from here", and resolving "?entity=containers" against the root
    // would send it to the wrong page.
    const url = new URL(href, window.location.href);
    if (url.origin !== window.location.origin) return;

    // A hash on the page you are already on is the browser's job -- that is
    // how an in-page jump works. Routing it dropped the fragment and
    // navigated instead: this wave's own skip link threw a keyboard user
    // onto the fleet overview from any other page, and did nothing at all on
    // the fleet overview itself.
    if (
      url.hash !== "" &&
      url.pathname === window.location.pathname &&
      url.search === window.location.search
    ) {
      return;
    }

    event.preventDefault();
    go(url.pathname + url.search + url.hash);
  };
}

/**
 * The tab's title, per page.
 *
 * index.html ships one static <title>netra</title> and nothing ever changed
 * it, so five open tabs of this app were five tabs reading "netra" -- and a
 * bookmark or a history entry carried no more than that either.
 *
 * The detail pages are deliberately NOT here: their title is a hostname or a
 * container key, which App does not have until the poll lands. They keep the
 * bare product name rather than flashing "netra" and then a name a moment
 * later, and naming themselves is the page's own job to take on later.
 */
const PAGE_TITLES: Partial<Record<Route["name"], string>> = {
  fleet: "All hosts",
  containers: "All containers",
  events: "Events",
  admin: "Agents",
  settings: "Settings",
  login: "Log in",
};

function useDocumentTitle(route: Route) {
  const page = PAGE_TITLES[route.name];
  useEffect(() => {
    document.title = page === undefined ? "netra" : `${page} — netra`;
  }, [page]);
}

/**
 * Which of the two "you are here" states an entry is in.
 *
 * "page" is this URL. "true" is the list a detail page came out of: the bar
 * marks Hosts while the reader is on /hosts/3, and announcing THAT link as
 * the current PAGE tells a screen-reader user the one link they need -- the
 * way back to the list -- is the page they are already on. "true" is aria's
 * word for "current within this set, but not this URL". Same fill either
 * way; the distinction is only ever spoken.
 */
type Here = "page" | "true" | null;

/**
 * Where the reader is, relative to one bar entry: the entry's own page, a
 * detail page under it, or somewhere else entirely.
 */
function here(
  current: Route["name"],
  page: Route["name"],
  ...under: Route["name"][]
): Here {
  if (current === page) return "page";
  return under.includes(current) ? "true" : null;
}

function NavLink({
  href,
  active,
  icon: Icon,
  children,
}: {
  href: string;
  active: Here;
  icon: LucideIcon;
  // A string, not a node: it has to survive into data-tip as well as into
  // the hidden label, and only one of those can hold markup.
  children: string;
}) {
  return (
    // data-tip is the visible label. It is duplicated in .sr-only rather
    // than read off the attribute because a tooltip drawn in CSS is invisible
    // to assistive tech, and the link would otherwise have no accessible name
    // at all.
    <a
      href={href}
      aria-current={active === null ? undefined : active}
      data-tip={children}
    >
      <Icon aria-hidden="true" />
      <span className="sr-only">{children}</span>
    </a>
  );
}

function Screen({
  route,
  search,
  go,
}: {
  route: Route;
  search: string;
  go: Go;
}) {
  switch (route.name) {
    case "fleet":
      return <FleetScreen entity="hosts" search={search} go={go} />;
    case "containers":
      return <FleetScreen entity="containers" search={search} go={go} />;
    case "host":
      return (
        <HostScreen
          hostId={route.hostId}
          tab={route.tab}
          search={search}
          go={go}
        />
      );
    case "container":
      return (
        <ContainerScreen
          hostId={route.hostId}
          containerKey={route.key}
          search={search}
          go={go}
        />
      );
    case "events":
      return <EventsScreen search={search} go={go} />;
    case "settings":
      return <SettingsPage />;
    case "admin":
      return <HostAdminPage />;
    case "login":
      return <LoginPage onSuccess={() => go("/")} />;
    case "notFound":
      return (
        <EmptyState
          icon={CircleSlash}
          title="No such page"
          body={`Nothing is served at ${route.path}.`}
        />
      );
  }
}

/**
 * A 401 means the session expired, which is a routing decision rather than
 * something to render: every screen hands its poll error here, and the one
 * that is a 401 sends the browser to the login page. replace, not push, so
 * Back does not walk straight into the page that just rejected them.
 */
function useAuthRedirect(error: Error | null, go: Go, route: Route) {
  useEffect(() => {
    if (
      error instanceof ApiError &&
      error.status === 401 &&
      route.name !== "login"
    ) {
      go("/login", { replace: true });
    }
  }, [error, go, route.name]);
}

/**
 * The range a screen should show, and the one thing to call when it changes.
 *
 * Three rules, in one place because every screen wants all three and the
 * pages used to disagree about each of them:
 *
 * - The URL wins. An explicit ?range= is a link someone sent, and a link
 *   exists to override what the recipient's browser happens to remember.
 * - Otherwise the remembered choice applies. Nothing used to write it back,
 *   so "the last range I picked" was never a thing the app knew: every page
 *   fell back to its own hardcoded literal, and the range appeared to
 *   scatter as you moved around.
 * - It is then clamped to what THIS page offers, because the pages offer
 *   different sets and the clamp has to happen before the fetch -- clamped
 *   inside a page, the toolbar would say 24h while the hub was asked for 7d.
 *
 * The write is deliberately unclamped: what gets remembered is what the
 * user actually clicked, so returning to a page that offers it shows it
 * again rather than the narrowed version some other page had to display.
 */
function rangeParam(
  search: string,
  offered: readonly Range[],
  setParam: (key: string, value: string) => void,
): [Range, (next: Range) => void] {
  const range = clampRange(rangeFromSearch(search, loadRange()), offered);
  const chooseRange = (next: Range) => {
    writePref(RANGE_KEY, next);
    setParam("range", next);
  };
  return [range, chooseRange];
}

/**
 * replace, not push: a range or filter change is a way of looking
 * at this page, not a different place. Pushing one entry per toggle turns
 * Back into an undo of fiddling rather than a way out of the page.
 */
function paramSetter(path: string, search: string, go: Go) {
  return (key: string, value: string) =>
    go(path + withParam(search, key, value), { replace: true });
}

function FleetScreen({
  entity,
  search,
  go,
}: {
  entity: Entity;
  search: string;
  go: Go;
}) {
  // Entity lives in the PATH now: a fleet view someone sends must arrive as
  // the view they were looking at, and two lists this different are two
  // pages. The window does not, because there is only one -- every row is
  // drawn over FLEET_RANGE.
  const params = new URLSearchParams(search);
  // The URL the entity used to live in, kept working. Somebody's bookmark or
  // a link in a chat still says ?entity=containers, and 404 -- or worse, a
  // silent fall back to the host list -- is the wrong answer to it.
  const stale = params.get("entity") === "containers" && entity === "hosts";
  useEffect(() => {
    if (stale)
      go("/containers" + withParam(search, "entity", ""), {
        replace: true,
      });
  }, [stale, search, go]);
  // What is wrong is a view of this page like any other, so it is a link:
  // "the fleet, filtered to failed units" is a URL someone can paste into a
  // chat. An unrecognised value is "all" rather than a filter that silently
  // matches nothing -- see isConditionKind.
  //
  // Read against the entity, because both vocabularies have a "silent" and
  // the entity is the page it is read on. `/containers?attn=silent` is a
  // silent container; the same word on `/` is a silent host. The severity
  // segments are hosts-only: every condition netra ranks that way is
  // host-level.
  const attnParam = params.get("attn") ?? "";
  // Every link this page builds stays on the page it was built from -- the
  // attention filter is a view of THIS list, not a way back to the other one.
  const base = entity === "containers" ? "/containers" : "/";
  const setParam = paramSetter(base, search, go);
  const range = FLEET_RANGE;

  const poll = usePoll(
    async () => {
      // No /sites, and no /providers either. Both were fetched whole on
      // every poll tick to resolve a place name per row; the host list now
      // carries what its own agents reported, so the fleet asks for one
      // thing instead of three.
      const hosts = await getHosts();

      // Three requests' worth of work in ONE wave, where this used to be two
      // fan-outs of one request per host per family, the second waiting on
      // the first. fetchFleetTrends owns the family list and the reason each
      // family is on it.
      //
      // The container listing is one request too, and was the last thing on
      // this page that still grew with the fleet: it asked each host in turn,
      // so every host added a request to every sixty-second tick, forever,
      // for a listing one WHERE clause answers. See getFleetContainers.
      //
      // The listings are fetched HERE rather than left to FleetPage, which
      // only fetches when nothing was injected -- and this page always
      // injects its rows, so the Containers tab sat empty claiming no host in
      // the fleet had ever reported one.
      const [trends, containerTrends, listings, conditions] = await Promise.all(
        [
          // threads, not cores: the per-core samples are one per logical CPU
          // (the N in /proc/stat's cpuN), and on an SMT host the two differ by
          // a factor of two. fetchFleetTrends reads it off each host.
          fetchFleetTrends(hosts, range),
          fetchFleetContainerTrends(hosts, range),
          // Caught rather than thrown, for the reason the per-host fan-out used
          // allSettled: a container listing that fails must not take the host
          // list down with it. The rows the fleet already has are the point of
          // the page, and the counts chip says what is missing.
          getFleetContainers(hosts.map((host) => host.id)).catch(() => null),
          // What is WRONG with the fleet, decided by the hub. This replaced a
          // fleet-wide drives listing that existed for one reason -- no cell
          // drew it, the drive condition was derived from it here -- and the
          // request is smaller for it: open conditions are bounded by what is
          // actually broken rather than by how many disks the fleet has.
          //
          // Caught on its own, like the container listing: a failing call must
          // not take down the host list that already rendered. Null leaves the
          // page with no conditions AND no catalogue, which every reader
          // degrades honestly on -- see EMPTY_CATALOGUE.
          getConditions().catch(() => null),
        ],
      );

      const containers: ContainerRow[] = [];
      hosts.forEach((host) => {
        const listing = listings?.get(host.id);
        if (listing === undefined) return;
        // The list and its metrics together: a container row with no trend
        // renders as text, which is what the whole list was before.
        const byKey = containerTrends.trends.get(host.id);
        for (const container of listing) {
          const trend = byKey?.get(container.container_key);
          containers.push({
            ...container,
            host_id: host.id,
            hostname: host.hostname,
            // "Gone" is measured against the HOST's own last report, never
            // against the wall clock: an offline host must not mark every
            // container on it gone. Nor must a host whose agent cannot see
            // cgroup scopes, which reports host samples and no container
            // ones. See containerIsGone.
            host_last_seen: host.last_seen,
            host_containers_capability: host.capabilities?.containers,
            // The denominators the two saturation cells are read against.
            // cpu_pct is percent of ONE core and mem_used is bytes, so
            // neither means anything until it is set against the machine --
            // and only the party that fanned these calls out knows which
            // machine that is. Same argument as hostname above.
            //
            // `threads`, not `cores`: cores lives on HostDetail, which the
            // fleet never fetches, and the fleet host row already labels
            // threads "cores".
            host_threads: host.threads,
            host_mem_total: host.mem_total,
            // Still per ROW, and now the same window on every one of them:
            // this list spans hosts, and one request answered all of them
            // from one plan. It was per row because it had to be -- N
            // separate requests could each be clamped differently, and a row
            // labelled with another host's times was a real risk.
            window: containerTrends.window,
            cpu: trend?.cpu ?? [],
            mem: trend?.mem ?? [],
            mem_limit_bytes: trend?.memLimit ?? null,
          });
        }
      });
      // A host that could not be asked is not a host running nothing, so the
      // count says how many are missing rather than quietly under-reporting.
      //
      // One request now answers for the whole fleet, so the failure is all or
      // nothing: either every host was asked or none was. The count is still
      // a count of hosts rather than a boolean, because that is what the chip
      // above states and a partial answer stopped being possible, not the
      // reason for saying how many are missing.
      const unreachable = listings === null ? hosts.length : 0;

      return {
        hosts,
        trends,
        containers,
        conditions,
        unreachable,
        at: new Date().toISOString(),
      };
    },
    POLL_MS,
    [range],
  );
  useAuthRedirect(poll.error, go, { name: "fleet" });

  // The kind vocabulary the hub serves, and the disk thresholds with it.
  //
  // EMPTY_CATALOGUE when the call failed or has not landed: everything
  // downstream degrades honestly on it rather than falling back to a copy of
  // the hub's rules, which is the duplication this whole change deleted.
  const catalogue = useMemo(
    () =>
      poll.data?.conditions
        ? catalogueOf(poll.data.conditions.kinds)
        : EMPTY_CATALOGUE,
    [poll.data?.conditions],
  );

  const rows = useMemo(
    () =>
      buildRows(
        poll.data?.hosts ?? [],
        poll.data?.trends ?? new Map(),
        // For the Disk cell's ranking only. The conditions themselves are the
        // hub's; this is what lets the meter colour a mount no condition
        // covers, which is most of them.
        diskThresholds(catalogue),
      ),
    [poll.data, catalogue],
  );

  // Resolved HERE rather than off the query string alone, because a kind is
  // only a kind if the hub says so -- and the catalogue arrives with the
  // conditions. An unrecognised value is "all", which includes every value
  // before the first response lands: one poll of the unfiltered fleet beats a
  // link that silently filters to nothing.
  const attention: FleetFilter =
    entity === "containers"
      ? isContainerStateKind(attnParam)
        ? attnParam
        : "all"
      : attnParam === "critical" || attnParam === "warning"
        ? attnParam
        : isConditionKind(catalogue, attnParam)
          ? attnParam
          : "all";

  // The same guard HostScreen makes, for the same reason. This screen is
  // remounted by every navigation back to it, so its first render has no
  // data -- and an overview handed zero hosts does not look empty, it looks
  // ANSWERED: "no hosts need attention", "0 of 0 known", the fleet's own
  // "no hosts yet" empty state. Every one of those is a claim about a fleet
  // nobody has asked about yet, and they were all on screen for the length
  // of the first request before the real numbers replaced them.
  if (poll.loading && poll.data === null)
    return <p className="note">Loading…</p>;

  return (
    <FleetPage
      rows={rows}
      entity={entity}
      // The other list is a page now, so switching entity is navigation and
      // not a parameter write. The filter does not travel with it: an
      // attention kind is read against one vocabulary, and carrying "silent"
      // across would land on the other page's meaning of the word.
      onEntityChange={(next) => go(next === "containers" ? "/containers" : "/")}
      attention={attention}
      // "" clears the parameter -- withParam drops an empty value, so the
      // unfiltered fleet is the bare URL rather than /?attn=all.
      onAttentionChange={(next) => setParam("attn", next === "all" ? "" : next)}
      // Built from the CURRENT query string and THIS page's path, so the list
      // the reader is on survives a cmd-click or a copied link -- withParam
      // drops the value when it is empty, which is how "all" becomes the bare
      // URL rather than ?attn=all.
      attentionHref={(next) =>
        base + withParam(search, "attn", next === "all" ? "" : next)
      }
      conditionRows={poll.data?.conditions?.conditions ?? []}
      catalogue={catalogue}
      containers={poll.data?.containers}
      containerError={
        poll.data && poll.data.unreachable > 0
          ? `${poll.data.unreachable} host${poll.data.unreachable === 1 ? "" : "s"} could not be asked for containers`
          : null
      }
      // All or nothing, like the containers: one request answers for the whole
      // fleet, so either it was asked or it was not. A page that silently
      // shows no conditions because the call failed is exactly the "green
      // because nobody looked" failure this engine exists to prevent, so it
      // says so instead.
      conditionError={
        poll.data && poll.data.conditions === null
          ? "netra could not be asked what is wrong"
          : null
      }
    />
  );
}

function HostScreen({
  hostId,
  tab,
  search,
  go,
}: {
  hostId: string;
  tab: HostTab;
  search: string;
  go: Go;
}) {
  // The tab is the path; the range is a query parameter on it, so switching
  // tabs carries the range along and a link to one tab can pin a window.
  const setParam = paramSetter(`/hosts/${hostId}/${tab}`, search, go);
  const [range, chooseRange] = rangeParam(search, HOST_RANGE_VALUES, setParam);

  // Host detail was the one screen that handed its poll error nowhere, so an
  // expired session left it sitting on data it could no longer refresh while
  // every other screen went to the login page. It fetches on a tick now, so
  // it has an error to hand over on every tick as well.
  const [pollError, setPollError] = useState<Error | null>(null);
  useAuthRedirect(pollError, go, { name: "host", hostId, tab });

  return (
    <HostPage
      // A different host is a different page. HostPage polls its record and
      // its tab families now, and usePoll deliberately keeps the last good
      // data across a dependency change -- so without a remount the previous
      // host's inventory would sit under the new host's name until the new
      // fetch lands, which is the one kind of wrong netra must never be. The
      // tab and the range live in the URL, so a remount resets nothing the
      // reader chose.
      key={hostId}
      hostId={hostId}
      tab={tab}
      onTabChange={(next: HostTab) => go(`/hosts/${hostId}/${next}${search}`)}
      range={range}
      onRangeChange={chooseRange}
      onPollError={setPollError}
    />
  );
}

function ContainerScreen({
  hostId,
  containerKey,
  search,
  go,
}: {
  hostId: string;
  containerKey: string;
  search: string;
  go: Go;
}) {
  const setParam = paramSetter(
    `/containers/${encodeURIComponent(hostId)}/${encodeURIComponent(containerKey)}`,
    search,
    go,
  );
  const [range, chooseRange] = rangeParam(
    search,
    CONTAINER_RANGE_VALUES,
    setParam,
  );

  // One family=container response at another range, for an enlarged chart
  // that wants a wider window than the page. Same call the poll makes, so
  // the dialog and the page ask the hub the same question; useCallback on
  // hostId alone because it reaches four panels and a new identity per
  // render would restart the fetch inside any open dialog.
  const fetchContainerMetrics = useCallback(
    (next: Range) => {
      const window = rangeWindow(next);
      return getMetrics(hostId, {
        family: "container",
        from: window.from,
        to: window.to,
        step: window.step,
      }) as Promise<MetricsResponse>;
    },
    [hostId],
  );

  const poll = usePoll(
    async () => {
      const window = rangeWindow(range);
      const [host, containers, metrics] = await Promise.all([
        getHost(hostId),
        getContainers(hostId),
        getMetrics(hostId, {
          family: "container",
          from: window.from,
          to: window.to,
          step: window.step,
        }),
      ]);
      return { host, containers, metrics };
    },
    POLL_MS,
    [hostId, range],
  );
  useAuthRedirect(poll.error, go, {
    name: "container",
    hostId,
    key: containerKey,
  });

  const container = poll.data?.containers.find(
    (c: Container) => c.container_key === containerKey,
  );

  if (poll.loading && poll.data === null)
    return <p className="note">Loading…</p>;
  if (poll.data === null || container === undefined) {
    return (
      <EmptyState
        icon={CircleSlash}
        title="No such container"
        body={`${containerKey} is not among the containers this host reported.`}
      />
    );
  }

  // Narrowed once, above the JSX: the guard higher up has already ruled null
  // out, but a closure passed as a prop is checked on its own.
  const hostRow = poll.data.host;

  return (
    <ContainerPage
      container={container}
      host={{
        id: hostRow.id,
        hostname: hostRow.hostname,
        // The clock "gone" is measured against, so a host that is merely
        // offline does not offer to purge everything it runs.
        last_seen: hostRow.last_seen,
        // For the same rule the lists apply: a host that cannot collect
        // containers at all reports no container samples, and nothing on it
        // is gone. See containerIsGone.
        capabilities: hostRow.capabilities,
      }}
      containerNetwork={poll.data.host.capabilities?.container_network}
      metrics={poll.data.metrics as MetricsResponse}
      range={range}
      onRangeChange={chooseRange}
      fetchMetrics={fetchContainerMetrics}
      // The page's subject is gone once purged, so the only sensible place
      // to be afterwards is the host it ran on.
      onPurged={() => go(`/hosts/${hostRow.id}/containers`)}
    />
  );
}

function EventsScreen({ search, go }: { search: string; go: Go }) {
  // The same three rules rangeParam applies, spelled out here because the
  // range arrives as one of the filters rather than on its own: the URL
  // wins, the remembered choice is the fallback, and the result is clamped
  // to what this page offers -- it widens, so a 6h shows as 24h here rather
  // than collapsing to 1h and an empty log.
  //
  // rangeFromSearch, not filtersFromQuery's own check, for the URL half:
  // filtersFromQuery only recognises the four ranges this page OFFERS, so a
  // link carrying ?range=6h was discarded outright and the reader's
  // remembered choice applied instead -- the one thing a sent link exists to
  // override. Clamped, that link shows 24h, which is what it meant.
  const filters = useMemo(
    () =>
      filtersFromQuery(
        search,
        clampRange(rangeFromSearch(search, loadRange()), EVENT_RANGE_VALUES),
      ),
    [search],
  );

  // The window is taken INSIDE the callback, not memoised on the range.
  // rangeWindow reads the clock when it is called, so a value computed once
  // per range change would pin `to` at that instant and the log would stop
  // advancing -- a page that looks live and is frozen. HostPage and
  // hostTrends take it inside their effect for the same reason.
  //
  // filters.range is the third argument for a different reason: usePoll keeps
  // `fn` in a ref and deliberately leaves it out of the effect deps, so
  // without naming the range here a click on 7d would move the picker and
  // change nothing on screen until the next 60-second tick.
  const poll = usePoll(
    async () => {
      const window = rangeWindow(filters.range);
      const [events, hosts] = await Promise.all([
        getEvents({
          since: window.from,
          until: window.to,
          limit: EVENT_LIMITS[filters.range],
        }),
        getHosts(),
      ]);
      // A full page back means the server had more and cut them off. Every
      // filter on the page except the range runs over what arrived, so the
      // page has to be told -- otherwise a severity floor that hides nothing
      // it was shown still looks like it found nothing at all.
      return {
        events,
        hosts,
        truncated: events.length >= EVENT_LIMITS[filters.range],
      };
    },
    POLL_MS,
    [filters.range],
  );
  useAuthRedirect(poll.error, go, { name: "events" });

  return (
    <EventsPage
      events={(poll.data?.events ?? []) as Event[]}
      truncated={poll.data?.truncated ?? false}
      hosts={(poll.data?.hosts ?? []).map((h: Host) => ({
        id: h.id,
        hostname: h.hostname,
      }))}
      filters={filters}
      // replace, not push: a filter edit is not a place, and pushing one
      // entry per keystroke turns Back into an undo of typing rather than a
      // way out of the page.
      onFiltersChange={(next) => {
        // The range half of a filter change is also a preference, so it is
        // remembered as well as put in the URL -- that is what carries it to
        // the next page you open.
        if (next.range !== filters.range) writePref(RANGE_KEY, next.range);
        go("/events?" + filtersToQuery(next), { replace: true });
      }}
    />
  );
}
