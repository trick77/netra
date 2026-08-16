import { useCallback, useEffect, useMemo } from "react";
import {
  ApiError,
  getContainers,
  getEvents,
  getHost,
  getHosts,
  getMetrics,
  getSites,
  type Container,
  type Event,
  type Host,
  type MetricsResponse,
  type Site,
} from "./lib/api";
import { POLL_MS, usePoll } from "./lib/poll";
import {
  rangeFromSearch,
  useLocation,
  withParam,
  type Route,
} from "./lib/router";
import { clampRange, rangeWindow, EVENT_LIMITS, type Range } from "./lib/range";
import { DENSITY_KEY, RANGE_KEY, writePref } from "./lib/prefs";
import { EmptyState } from "./ui/EmptyState";
import {
  FleetPage,
  FLEET_RANGE_VALUES,
  type Density,
  type Entity,
} from "./features/fleet/FleetPage";
import {
  isConditionKind,
  type AttentionFilter,
} from "./features/fleet/conditions";
import {
  buildRows,
  fetchContainerTrends,
  fetchHostTrends,
  type HostTrends,
} from "./features/fleet/hostTrends";
import type { ContainerRow } from "./features/fleet/FleetContainers";
import {
  HostPage,
  RANGE_VALUES as HOST_RANGE_VALUES,
  type HostTab,
} from "./features/host/HostPage";
import { ChartPage } from "./features/host/ChartPage";
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
import {
  SettingsPage,
  loadRange,
  loadView,
} from "./features/settings/SettingsPage";
import { LoginPage } from "./features/auth/LoginPage";
import { HostAdminPage } from "./features/admin/HostAdminPage";
import {
  Bell,
  CircleSlash,
  Gauge,
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
          moved. HostPage's own header is disambiguated from this one by
          accessible name, not by there being exactly one. */}
      <header className="siderail">
        <a className="brand" href="/">
          netra
        </a>
        {/* Named because it is not the only nav landmark on a page -- Tabs
            renders one too -- and "navigation" twice over tells a screen
            reader user nothing about which is which. */}
        <nav className="nav" aria-label="Primary">
          <div className="navgroup">
            {/* A dial for the fleet reading and a bell for things that
                happened: the two marks that have to say "monitoring tool"
                rather than "admin panel". The other two are a server and a
                cog in every set worth considering. */}
            <NavLink href="/" icon={Gauge} active={route.name === "fleet"}>
              Overview
            </NavLink>
            <NavLink
              href="/events"
              icon={Bell}
              active={route.name === "events"}
            >
              Events
            </NavLink>
            <NavLink
              href="/admin/hosts"
              icon={Server}
              active={route.name === "admin"}
            >
              Hosts
            </NavLink>
          </div>
          {/* Settings is the only destination here that is not about the
              fleet, so it sits apart -- pushed to the foot of the rail with a
              rule above it rather than filed as a fourth peer. */}
          <div className="navgroup navgroup-end">
            <NavLink
              href="/settings"
              icon={Settings2}
              active={route.name === "settings"}
            >
              Settings
            </NavLink>
          </div>
        </nav>
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

function NavLink({
  href,
  active,
  icon: Icon,
  children,
}: {
  href: string;
  active: boolean;
  icon: LucideIcon;
  children: React.ReactNode;
}) {
  return (
    <a href={href} aria-current={active ? "page" : undefined}>
      {/* Decorative: the word beside it is the accessible name, and an icon
          that repeats the label would read it twice. */}
      <Icon aria-hidden="true" />
      {children}
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
      return <FleetScreen search={search} go={go} />;
    case "host":
      return (
        <HostScreen
          hostId={route.hostId}
          tab={route.tab}
          search={search}
          go={go}
        />
      );
    case "chart":
      return (
        <ChartScreen
          hostId={route.hostId}
          slug={route.slug}
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
 * replace, not push: a range, density or filter change is a way of looking
 * at this page, not a different place. Pushing one entry per toggle turns
 * Back into an undo of fiddling rather than a way out of the page.
 */
function paramSetter(path: string, search: string, go: Go) {
  return (key: string, value: string) =>
    go(path + withParam(search, key, value), { replace: true });
}

function FleetScreen({ search, go }: { search: string; go: Go }) {
  // Entity, density and range all live in the URL: a fleet view someone
  // sends must arrive as the view they were looking at, not as whatever the
  // recipient's browser last remembered.
  const params = new URLSearchParams(search);
  const entity: Entity =
    params.get("entity") === "containers" ? "containers" : "hosts";
  // Both directions, not just one: ?view=table used to be ignored whenever
  // the recipient's browser remembered "cards", which is exactly the case a
  // shared link exists to override.
  const viewParam = params.get("view");
  const density: Density =
    viewParam === "cards" || viewParam === "table" ? viewParam : loadView();
  // What is wrong is a view of this page like any other, so it is a link:
  // "the fleet, filtered to failed units" is a URL someone can paste into a
  // chat. An unrecognised value is "all" rather than a filter that silently
  // matches nothing -- see isConditionKind.
  const attnParam = params.get("attn") ?? "";
  const attention: AttentionFilter =
    attnParam === "critical" || attnParam === "warning"
      ? attnParam
      : isConditionKind(attnParam)
        ? attnParam
        : "all";
  const setParam = paramSetter("/", search, go);
  const [range, chooseRange] = rangeParam(search, FLEET_RANGE_VALUES, setParam);

  const poll = usePoll(
    async () => {
      const [hosts, sites] = await Promise.all([getHosts(), getSites()]);
      // The trends are a fan-out -- four families per host, five where the
      // host is small enough to be worth a per-core stack -- because the
      // read API is per-host by construction and this page's whole premise
      // is the last 24 hours rather than one instant. fetchHostTrends owns
      // the list and the reason each family is on it. Settled
      // independently: one host answering 500 costs that host's sparklines,
      // not the fleet's.
      const settled = await Promise.allSettled(
        hosts.map(
          async (host) =>
            [
              host.id,
              // threads, not cores: the per-core samples are one per logical
              // CPU (the N in /proc/stat's cpuN), and on an SMT host the two
              // differ by a factor of two.
              await fetchHostTrends(host.id, range, undefined, host.threads),
            ] as const,
        ),
      );
      const trends = new Map(
        settled
          .filter(
            (r): r is PromiseFulfilledResult<readonly [number, HostTrends]> =>
              r.status === "fulfilled",
          )
          .map((r) => r.value),
      );

      // The fleet-wide container list is the same shape of fan-out, and for
      // the same reason: there is no /api/v1/containers, only the per-host
      // route. It is fetched HERE rather than left to FleetPage, which only
      // fetches when nothing was injected -- and this page always injects
      // its rows, so the Containers tab sat empty claiming no host in the
      // fleet had ever reported one.
      const perHost = await Promise.allSettled(
        hosts.map(async (host) => {
          // The list and its metrics together: a container row with no
          // trend renders as text, which is what the whole list was before.
          const [list, trends] = await Promise.all([
            getContainers(host.id),
            fetchContainerTrends(host.id, range),
          ]);
          return list.map((container) => {
            const trend = trends.get(container.container_key);
            return {
              ...container,
              host_id: host.id,
              hostname: host.hostname,
              cpu: trend?.cpu ?? [],
              mem: trend?.mem ?? [],
              mem_limit_bytes: trend?.memLimit ?? null,
            };
          });
        }),
      );
      const containers = perHost
        .filter((r) => r.status === "fulfilled")
        .flatMap((r) => (r as PromiseFulfilledResult<ContainerRow[]>).value);
      // A host that could not be asked is not a host running nothing, so the
      // count says how many are missing rather than quietly under-reporting.
      const unreachable = perHost.filter((r) => r.status === "rejected").length;

      return {
        hosts,
        sites,
        trends,
        containers,
        unreachable,
        at: new Date().toISOString(),
      };
    },
    POLL_MS,
    [range],
  );
  useAuthRedirect(poll.error, go, { name: "fleet" });

  const rows = useMemo(
    () =>
      buildRows(
        poll.data?.hosts ?? [],
        poll.data?.sites ?? [],
        poll.data?.trends ?? new Map(),
      ),
    [poll.data],
  );

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
      onEntityChange={(next) =>
        setParam("entity", next === "containers" ? "containers" : "")
      }
      density={density}
      // Both halves, here rather than inside FleetPage: the preference is
      // what makes the choice survive leaving the page, and the URL is what
      // makes it survivable as a link. One write path for one key.
      onDensityChange={(next) => {
        writePref(DENSITY_KEY, next);
        setParam("view", next);
      }}
      range={range}
      onRangeChange={chooseRange}
      attention={attention}
      // "" clears the parameter -- withParam drops an empty value, so the
      // unfiltered fleet is the bare URL rather than /?attn=all.
      onAttentionChange={(next) => setParam("attn", next === "all" ? "" : next)}
      // Built from the CURRENT query string, so the density, entity and range
      // the reader is on survive a cmd-click or a copied link -- withParam
      // drops the value when it is empty, which is how "all" becomes the bare
      // fleet URL rather than ?attn=all.
      attentionHref={(next) =>
        "/" + withParam(search, "attn", next === "all" ? "" : next)
      }
      checkedAt={poll.data?.at ?? null}
      containers={poll.data?.containers}
      containerError={
        poll.data && poll.data.unreachable > 0
          ? `${poll.data.unreachable} host${poll.data.unreachable === 1 ? "" : "s"} could not be asked for containers`
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

  return (
    <HostPage
      hostId={hostId}
      tab={tab}
      onTabChange={(next: HostTab) => go(`/hosts/${hostId}/${next}${search}`)}
      range={range}
      onRangeChange={chooseRange}
    />
  );
}

/**
 * One chart, with its own URL.
 *
 * The range is a query parameter for the same reason every other page's
 * filters are (spec 9): the view someone is looking at should be a link they
 * can send, and "this chart over the last 7 days" is the whole message.
 */
function ChartScreen({
  hostId,
  slug,
  search,
  go,
}: {
  hostId: string;
  slug: string;
  search: string;
  go: Go;
}) {
  const setParam = paramSetter(`/hosts/${hostId}/chart/${slug}`, search, go);
  const [range, chooseRange] = rangeParam(search, HOST_RANGE_VALUES, setParam);
  const back = backTarget(hostId, search);

  return (
    <ChartPage
      hostId={hostId}
      slug={slug}
      range={range}
      onRangeChange={chooseRange}
      // Back to the page this chart was opened from, carrying the range with
      // it. history.back() would be wrong for someone who arrived by link:
      // there is nothing behind them.
      onBack={() => go(back.path)}
      backLabel={back.label}
    />
  );
}

/**
 * Where Back goes, and what it says.
 *
 * `?from=` because this page has more than one way in. It was the Graphs
 * tab alone, and Back was hardcoded to it; the fleet row's traffic cell now
 * links here too, and sending that reader to a host's Graphs tab drops them
 * on a page they were never on, one level deeper than where they started.
 *
 * A whitelist, never the raw value as a path: `from` arrives from the URL,
 * and go() would happily route to whatever is in it.
 *
 * No `from` at all keeps the old behaviour exactly -- every existing link,
 * and every one a reader has already sent, still goes back to the tab.
 */
const BACK_TARGETS: Record<
  string,
  (hostId: string) => { path: string; label: string }
> = {
  fleet: () => ({ path: "/", label: "Back to fleet" }),
  overview: (hostId) => ({
    path: `/hosts/${hostId}/overview`,
    label: "Back to overview",
  }),
};

function backTarget(hostId: string, search: string) {
  // Stripped, not forwarded: `from` says how the reader got HERE, and
  // carrying it onto the fleet URL would leave a parameter that page has no
  // use for sitting in the address bar of a link they might then send.
  const params = new URLSearchParams(search);
  const from = params.get("from") ?? "";
  params.delete("from");
  const rest = params.toString();
  const query = rest ? `?${rest}` : "";
  const target = BACK_TARGETS[from]?.(hostId) ?? {
    path: `/hosts/${hostId}/graphs`,
    label: "Back to graphs",
  };
  return { path: target.path + query, label: target.label };
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

  return (
    <ContainerPage
      container={container}
      host={{ id: poll.data.host.id, hostname: poll.data.host.hostname }}
      containerNetwork={poll.data.host.capabilities?.container_network}
      metrics={poll.data.metrics as MetricsResponse}
      range={range}
      onRangeChange={chooseRange}
      fetchMetrics={fetchContainerMetrics}
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
      return { events, hosts };
    },
    POLL_MS,
    [filters.range],
  );
  useAuthRedirect(poll.error, go, { name: "events" });

  return (
    <EventsPage
      events={(poll.data?.events ?? []) as Event[]}
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

export type { Site };
