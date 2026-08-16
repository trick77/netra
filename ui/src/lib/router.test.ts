import { describe, expect, it } from "vitest";
import { parseRoute, rangeFromSearch, routePath, withParam } from "./router";

describe("parseRoute", () => {
  it("routes the paths the spec names", () => {
    expect(parseRoute("/")).toEqual({ name: "fleet" });
    expect(parseRoute("/hosts/3/graphs")).toEqual({
      name: "host",
      hostId: "3",
      tab: "graphs",
    });
    expect(parseRoute("/containers/3/web%2Fapi")).toEqual({
      name: "container",
      hostId: "3",
      key: "web/api",
    });
    expect(parseRoute("/events")).toEqual({ name: "events" });
    expect(parseRoute("/settings")).toEqual({ name: "settings" });
    expect(parseRoute("/admin/hosts")).toEqual({ name: "admin" });
    expect(parseRoute("/login")).toEqual({ name: "login" });
  });

  // The URL a human types, and the one an external link is most likely to
  // carry. A 404 here would be a link that looks right and does nothing.
  it("treats a bare host path as that host's default tab", () => {
    expect(parseRoute("/hosts/3")).toEqual({
      name: "host",
      hostId: "3",
      tab: "overview",
    });
  });

  // A tab name that does not exist must not reach HostPage, which would
  // render an empty shell with a tab bar highlighting nothing.
  it("does not invent a tab it does not have", () => {
    expect(parseRoute("/hosts/3/alerts").name).toBe("notFound");
  });

  // Container keys are compose project/service, so they contain a slash.
  // Round-tripping through the path is the whole reason routePath encodes.
  it("round-trips a route through its own path", () => {
    const route = {
      name: "container" as const,
      hostId: "7",
      key: "netra/hub",
    };

    expect(parseRoute(routePath(route))).toEqual(route);
  });
});

describe("query state", () => {
  // The URL is user-editable and old links carry old values. An
  // unrecognised range must not reach a page that would resolve it into an
  // Invalid Date and ask the hub for NaN.
  it("falls back rather than trusting a range out of the URL", () => {
    expect(rangeFromSearch("?range=7d", "24h")).toBe("7d");
    expect(rangeFromSearch("?range=99y", "24h")).toBe("24h");
    expect(rangeFromSearch("", "24h")).toBe("24h");
  });

  it("writes one parameter without disturbing the others", () => {
    expect(withParam("?host=3&range=1h", "range", "7d")).toBe(
      "?host=3&range=7d",
    );
  });

  // An empty filter in the URL is noise that makes two identical views look
  // like different links.
  it("drops a parameter set to empty", () => {
    expect(withParam("?host=3&q=", "host", "")).toBe("?q=");
    expect(withParam("?host=3", "host", "")).toBe("");
  });
});

describe("the chart route", () => {
  it("parses a chart page and round-trips it", () => {
    const route = parseRoute("/hosts/3/chart/interface-throughput");
    expect(route).toEqual({
      name: "chart",
      hostId: "3",
      slug: "interface-throughput",
    });
    expect(routePath(route)).toBe("/hosts/3/chart/interface-throughput");
  });

  // The trap this route was moved off /graphs/ to avoid, pinned here because
  // the old parser accepted a fourth segment under a known tab and silently
  // rendered the tab -- so a link to a chart that no longer exists would
  // have looked like it worked.
  it("does not swallow a trailing segment under a tab", () => {
    expect(parseRoute("/hosts/3/graphs/interface-throughput")).toEqual({
      name: "notFound",
      path: "/hosts/3/graphs/interface-throughput",
    });
  });

  it("still parses a bare tab", () => {
    expect(parseRoute("/hosts/3/graphs")).toEqual({
      name: "host",
      hostId: "3",
      tab: "graphs",
    });
  });

  it("has no chart without a slug", () => {
    expect(parseRoute("/hosts/3/chart")).toEqual({
      name: "notFound",
      path: "/hosts/3/chart",
    });
  });

  it("encodes a slug that needs it", () => {
    expect(routePath({ name: "chart", hostId: "3", slug: "a/b" })).toBe(
      "/hosts/3/chart/a%2Fb",
    );
  });
});
