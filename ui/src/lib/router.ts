import { useCallback, useEffect, useState } from "react";
import { isRange, type Range } from "./range";
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
  /**
   * One chart, on its own page.
   *
   * /hosts/3/chart/interface-throughput rather than .../graphs/... on
   * purpose: `graphs` is a tab, and a fourth segment under it reads as a
   * sub-tab. It is also the safer URL -- parseRoute already accepts
   * /hosts/3/graphs and ignores anything after it, so a chart route hung
   * there would be one missing check away from silently rendering the tab.
   */
  | { name: "chart"; hostId: string; slug: string }
  | { name: "container"; hostId: string; key: string }
  | { name: "events" }
  | { name: "settings" }
  | { name: "admin" }
  | { name: "login" }
  | { name: "notFound"; path: string };

const HOST_TABS = new Set<HostTab>([
  "overview",
  "graphs",
  "containers",
  "filesystems",
  "network",
  "packages",
  "units",
  "events",
]);

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
    if (tab === "chart" && parts[3] !== undefined && parts.length === 4) {
      return { name: "chart", hostId, slug: parts[3] };
    }
    // A known tab and NOTHING after it. The length check is the point: this
    // used to accept /hosts/3/graphs/anything and render the graphs tab,
    // quietly swallowing the trailing segment.
    if (HOST_TABS.has(tab as HostTab) && parts.length === 3) {
      return { name: "host", hostId, tab: tab as HostTab };
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
    case "chart":
      return `/hosts/${encodeURIComponent(route.hostId)}/chart/${encodeURIComponent(route.slug)}`;
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
