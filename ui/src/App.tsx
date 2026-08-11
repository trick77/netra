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
  routePath,
  useLocation,
  withParam,
  type Route,
} from "./lib/router";
import { rangeWindow, type Range } from "./lib/range";
import { EmptyState } from "./ui/EmptyState";
import {
  FleetPage,
  buildHostRows,
  type Density,
  type Entity,
} from "./features/fleet/FleetPage";
import { HostPage, type HostTab } from "./features/host/HostPage";
import { ContainerPage } from "./features/container/ContainerPage";
import {
  EventsPage,
  DEFAULT_FILTERS,
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
import { CircleSlash } from "lucide-react";

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
      <header className="topnav">
        <a className="brand" href="/">
          netra
        </a>
        <nav className="nav">
          <NavLink href="/" active={route.name === "fleet"}>
            Overview
          </NavLink>
          <NavLink href="/events" active={route.name === "events"}>
            Events
          </NavLink>
          <NavLink href="/admin/hosts" active={route.name === "admin"}>
            Hosts
          </NavLink>
          <NavLink href="/settings" active={route.name === "settings"}>
            Settings
          </NavLink>
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
    // Absolute and off-origin URLs belong to the browser. A relative one
    // resolves against the current page, which is exactly what routing it
    // means.
    const url = new URL(href, window.location.origin);
    if (url.origin !== window.location.origin) return;

    event.preventDefault();
    go(url.pathname + url.search);
  };
}

function NavLink({
  href,
  active,
  children,
}: {
  href: string;
  active: boolean;
  children: React.ReactNode;
}) {
  return (
    <a href={href} aria-current={active ? "page" : undefined}>
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
      return <HostScreen hostId={route.hostId} tab={route.tab} go={go} />;
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

function FleetScreen({ search, go }: { search: string; go: Go }) {
  // Entity, density and range all live in the URL: a fleet view someone
  // sends must arrive as the view they were looking at, not as whatever the
  // recipient's browser last remembered.
  const params = new URLSearchParams(search);
  const entity: Entity =
    params.get("entity") === "containers" ? "containers" : "hosts";
  const density: Density =
    params.get("view") === "cards" ? "cards" : loadView();
  const range = rangeFromSearch(search, loadRange());

  const poll = usePoll(async () => {
    const [hosts, sites] = await Promise.all([getHosts(), getSites()]);
    return { hosts, sites, at: new Date().toISOString() };
  }, POLL_MS);
  useAuthRedirect(poll.error, go, { name: "fleet" });

  const rows = useMemo(
    () => buildHostRows(poll.data?.hosts ?? [], poll.data?.sites ?? []),
    [poll.data],
  );

  // replace, not push: a range or a density toggle is a way of looking at
  // this page, not a different place. Pushing one entry per toggle turns
  // Back into an undo of fiddling rather than a way out of the page.
  const setParam = (key: string, value: string) =>
    go("/" + withParam(search, key, value), { replace: true });

  return (
    <FleetPage
      rows={rows}
      entity={entity}
      onEntityChange={(next) =>
        setParam("entity", next === "containers" ? "containers" : "")
      }
      density={density}
      onDensityChange={(next) => setParam("view", next)}
      range={range}
      onRangeChange={(next: Range) => setParam("range", next)}
      checkedAt={poll.data?.at ?? null}
    />
  );
}

function HostScreen({
  hostId,
  tab,
  go,
}: {
  hostId: string;
  tab: HostTab;
  go: Go;
}) {
  return (
    <HostPage
      hostId={hostId}
      tab={tab}
      onTabChange={(next: HostTab) => go(`/hosts/${hostId}/${next}`)}
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
  const range = rangeFromSearch(search, loadRange());

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
      metrics={poll.data.metrics as MetricsResponse}
      range={range}
      onRangeChange={(next: Range) =>
        go(
          routePath({ name: "container", hostId, key: containerKey }) +
            withParam(search, "range", next),
          { replace: true },
        )
      }
    />
  );
}

function EventsScreen({ search, go }: { search: string; go: Go }) {
  const filters = useMemo(
    () => ({ ...DEFAULT_FILTERS, ...filtersFromQuery(search) }),
    [search],
  );

  const poll = usePoll(async () => {
    const [events, hosts] = await Promise.all([getEvents(), getHosts()]);
    return { events, hosts };
  }, POLL_MS);
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
      onFiltersChange={(next) =>
        go("/events" + filtersToQuery(next), { replace: true })
      }
    />
  );
}

export type { Site };
