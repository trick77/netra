import { useCallback, useEffect, useState } from "react";
import { isRange, type Range } from "./range";
import {
  COLLECTOR_GROUPS,
  NETWORK_GROUPS,
  STORAGE_GROUPS,
} from "../features/host/chartSpecs";
import type { HostTab } from "../features/host/HostPage";

/**
 * A path router, not a hash one.
 *
 * The hub serves index.html for every path that is not a real file
 * (internal/hub/web/embed.go), which is what makes real URLs work: a reload
 * of /hosts/3/graphs comes back as the app rather than a 404. Hash routing
 * would have made every deep link a fragment the server never sees, and the
 * diagnosis drawer's evidence links are meant to point at the tab holding
 * the data.
 *
 * It is about sixty lines because that is all this app needs. A router
 * library would bring a matcher, a data layer and a transition model, none
 * of which appear in the spec.
 */
export type Route =
  | { name: "fleet" }
  | { name: "host"; hostId: string; tab: HostTab }
  | { name: "container"; hostId: string; key: string }
  | { name: "events" }
  | { name: "settings" }
  | { name: "admin" }
  | { name: "login" }
  | { name: "notFound"; path: string };

const HOST_TABS = new Set<HostTab>([
  "overview",
  "system",
  "network",
  "storage",
  "containers",
  "packages",
  "units",
  "collectors",
  "events",
]);

/**
 * Tabs that used to exist, and where their content went.
 *
 * /hosts/3/graphs and /hosts/3/filesystems are links people have sent each
 * other, and 404 is the wrong answer to a page that still exists under
 * another name. Graphs was split across three subject tabs, so there is no
 * single right target -- System is where the CPU and memory panels went,
 * which is what most Graphs links were about.
 *
 * Kept indefinitely rather than for a deprecation window: the map costs two
 * lines and the alternative is a dead link in someone's runbook.
 */
const RENAMED_TABS: Record<string, HostTab> = {
  graphs: "system",
  filesystems: "storage",
};

/**
 * The subject tab that draws a given panel slug.
 *
 * Derived from the groups rather than listed here, so a panel moved between
 * tabs takes its old /chart/ links with it and nobody has to remember this
 * file exists. An unknown slug -- a link to a panel that has since been
 * removed -- lands on System, which is where the old behaviour sent
 * everything.
 */
/**
 * A slug that no longer exists, and the panel that absorbed it.
 *
 * The group walk below cannot find a retired slug and falls through to
 * System, which for a link about the network is a worse answer than the one
 * this file used to give. Resolved here rather than left as a stale entry in
 * chartSpecs: the panel is genuinely gone, and a spec kept alive only to keep
 * a URL working is a panel somebody will eventually draw.
 */
const RETIRED_SLUGS: Record<string, string> = {
  // Traffic draws one stacked in/out pair per interface now, which is what
  // this panel was for.
  "interface-throughput": "host-traffic",
};

function hostTabForSlug(retiring: string): HostTab {
  const slug = RETIRED_SLUGS[retiring] ?? retiring;
  if (NETWORK_GROUPS.some((g) => g.specs.some((s) => s.slug === slug))) {
    return "network";
  }
  if (STORAGE_GROUPS.some((g) => g.specs.some((s) => s.slug === slug))) {
    return "storage";
  }
  if (COLLECTOR_GROUPS.some((g) => g.specs.some((s) => s.slug === slug))) {
    return "collectors";
  }
  return "system";
}

// decodeURIComponent throws URIError on a malformed escape, and /hosts/50%
// is a URL a browser will happily send. There is no error boundary in this
// app, so the throw during render was a white page -- in place of the "no
// such page" state that exists for precisely this. An undecodable segment is
// passed through raw: it will not match a route, which is the answer.
function decodeSegment(part: string): string {
  try {
    return decodeURIComponent(part);
  } catch {
    return part;
  }
}

export function parseRoute(pathname: string): Route {
  const parts = pathname.split("/").filter(Boolean).map(decodeSegment);

  if (parts.length === 0) return { name: "fleet" };

  if (parts[0] === "hosts" && parts[1] !== undefined) {
    const hostId = parts[1];
    const tab = parts[2];
    // A bare /hosts/3 is the host page on its default tab rather than a 404:
    // it is the URL a human types, and the one an external link is most
    // likely to carry.
    if (tab === undefined) return { name: "host", hostId, tab: "overview" };
    // /hosts/3/chart/<slug> was a real page once. Every chart opens in a
    // dialog now, so the URL has nowhere of its own to land -- but it is a
    // link a reader may have sent, and 404 is the wrong answer to it. It
    // goes to whichever subject tab now draws that panel, which is a better
    // answer than it used to give: it landed on Graphs regardless of which
    // chart was named, and Graphs is where everything was.
    if (tab === "chart" && parts[3] !== undefined && parts.length === 4) {
      return { name: "host", hostId, tab: hostTabForSlug(parts[3]) };
    }
    // A known tab and NOTHING after it. The length check is the point: this
    // used to accept /hosts/3/graphs/anything and render the graphs tab,
    // quietly swallowing the trailing segment.
    if (HOST_TABS.has(tab as HostTab) && parts.length === 3) {
      return { name: "host", hostId, tab: tab as HostTab };
    }
    const renamed = RENAMED_TABS[tab];
    if (renamed !== undefined && parts.length === 3) {
      return { name: "host", hostId, tab: renamed };
    }
    return { name: "notFound", path: pathname };
  }

  if (parts[0] === "containers" && parts[1] && parts[2]) {
    return { name: "container", hostId: parts[1], key: parts[2] };
  }

  if (parts.length === 1) {
    if (parts[0] === "events") return { name: "events" };
    if (parts[0] === "settings") return { name: "settings" };
    if (parts[0] === "login") return { name: "login" };
  }
  if (parts[0] === "admin" && parts[1] === "hosts" && parts.length === 2) {
    return { name: "admin" };
  }

  return { name: "notFound", path: pathname };
}

export function routePath(route: Route): string {
  switch (route.name) {
    case "fleet":
      return "/";
    case "host":
      return `/hosts/${encodeURIComponent(route.hostId)}/${route.tab}`;
    case "container":
      return `/containers/${encodeURIComponent(route.hostId)}/${encodeURIComponent(route.key)}`;
    case "events":
      return "/events";
    case "settings":
      return "/settings";
    case "admin":
      return "/admin/hosts";
    case "login":
      return "/login";
    case "notFound":
      return route.path;
  }
}

export interface Location {
  route: Route;
  /** The query string, "?" included, or "" — pages own their own filters and
   * round-trip them through here (spec §9: filters live in the URL, so a
   * filtered view is a link someone can send). */
  search: string;
  navigate: (to: string, options?: { replace?: boolean }) => void;
}

export function useLocation(): Location {
  const read = () => window.location.pathname + window.location.search;
  const [href, setHref] = useState(read);

  useEffect(() => {
    // Back and forward are navigation the app did not initiate, so the
    // component tree has to be told. Without this, the address bar moves and
    // the page does not.
    const onPop = () => setHref(read());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const navigate = useCallback(
    (to: string, options?: { replace?: boolean }) => {
      // replace, for a change that is not a place: a filter edit or a range
      // change should not put an entry in the history for every keystroke,
      // or Back becomes an undo of typing rather than a way out of the page.
      if (options?.replace) window.history.replaceState(null, "", to);
      else window.history.pushState(null, "", to);
      setHref(to);
    },
    [],
  );

  const [pathname, search] = splitHref(href);
  return { route: parseRoute(pathname), search, navigate };
}

function splitHref(href: string): [string, string] {
  const i = href.indexOf("?");
  return i === -1 ? [href, ""] : [href.slice(0, i), href.slice(i)];
}

/**
 * Reads a range out of a query string, falling back rather than trusting it.
 * The URL is user-editable and is also whatever an old link happens to
 * carry; an unrecognised range must not reach a page that would resolve it
 * into an Invalid Date.
 */
export function rangeFromSearch(search: string, fallback: Range): Range {
  const value = new URLSearchParams(search).get("range");
  return isRange(value) ? value : fallback;
}

/** Writes one query parameter, preserving the others and dropping empties. */
export function withParam(search: string, key: string, value: string): string {
  const params = new URLSearchParams(search);
  if (value === "") params.delete(key);
  else params.set(key, value);
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}
