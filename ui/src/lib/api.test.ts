import { describe, expect, it, vi, afterEach, type Mock } from "vitest";
import {
  getFleetContainers,
  getFleetMetrics,
  getHosts,
  getMetrics,
  createHost,
  deleteHost,
  ApiError,
} from "./api";

function mockFetch(status: number, body: unknown) {
  return vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          status,
          headers: { "content-type": "application/json" },
        }),
    ),
  );
}
afterEach(() => vi.unstubAllGlobals());

describe("api", () => {
  it("sends credentials so the session cookie reaches RequireAdmin", async () => {
    mockFetch(200, []);
    await getHosts();
    const [, init] = (fetch as unknown as Mock).mock.calls[0];
    expect(init.credentials).toBe("same-origin");
  });

  // A 401 is a routing decision (show login), not a crash. It must be
  // distinguishable from a 500 by type, not by parsing a message.
  it("throws ApiError carrying the status", async () => {
    mockFetch(401, { error: "unauthorized" });
    await expect(getHosts()).rejects.toMatchObject({ status: 401 });
    await expect(getHosts()).rejects.toBeInstanceOf(ApiError);
  });

  // The from/to values here are RFC 3339 and unix milliseconds because those
  // are the only two forms parseTime accepts (internal/hub/httpapi/read.go).
  // This test is also the module's worked example, and it used to pass
  // "-24h"/"now" -- shapes the hub answers with a 400, invisible here because
  // fetch is stubbed, and contradicting getMetrics' own doc comment.
  it("passes the metrics query through verbatim", async () => {
    mockFetch(200, { family: "host", tier: "raw", columns: [], series: [] });
    await getMetrics(1, {
      family: "host",
      from: "2026-08-10T00:00:00Z",
      to: String(Date.UTC(2026, 7, 11)),
      step: "5m",
    });
    const [url] = (fetch as unknown as Mock).mock.calls[0];
    expect(url).toContain("/api/v1/hosts/1/metrics?");
    expect(url).toContain("family=host");
    expect(url).toContain("from=2026-08-10T00%3A00%3A00Z");
    expect(url).toContain("step=5m");
  });

  // The fleet route asks about several hosts at once and answers with ONE
  // header beside a per-host series list. Splitting it back into the per-host
  // shape here, and only here, is what lets every reader downstream --
  // griddedValues, memoryBands, containerTrends -- stay unchanged: they still
  // take a MetricsResponse and never learn how it was fetched.
  it("splits a fleet response back into one response per host", async () => {
    mockFetch(200, {
      family: "host",
      tier: "5m",
      step_s: 300,
      window: { from: "a", to: "b" },
      requested_window: { from: "a", to: "b" },
      warnings: [],
      key_columns: [],
      columns: ["cpu_total_avg"],
      truncated: false,
      hosts: [
        { host_id: 1, series: [{ key: {}, points: [[0, 5]] }] },
        { host_id: 2, series: [] },
      ],
    });

    const byHost = await getFleetMetrics([1, 2], {
      family: "host",
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-11T00:00:00Z",
      step: "5m",
      columns: ["cpu_total"],
    });

    const [url] = (fetch as unknown as Mock).mock.calls[0];
    expect(url).toContain("/api/v1/metrics?");
    expect(url).toContain("hosts=1%2C2");
    expect(url).toContain("columns=cpu_total");

    // Both hosts present, and each carrying the SHARED header -- a caller
    // reading tier or columns off one host's response must get the answer for
    // the whole request.
    expect(byHost.get(1)?.columns).toEqual(["cpu_total_avg"]);
    expect(byHost.get(1)?.tier).toBe("5m");
    expect(byHost.get(1)?.series[0].points).toEqual([[0, 5]]);
    // A host that reported nothing is present with no series, never absent:
    // silence and "never asked" have to stay distinguishable.
    expect(byHost.get(2)?.series).toEqual([]);
    expect(byHost.get(2)?.tier).toBe("5m");
  });

  // The same split for the fleet's container listing, which the overview used
  // to fetch one host at a time.
  it("splits a fleet container response into one list per host", async () => {
    mockFetch(200, {
      hosts: [
        { host_id: 1, containers: [{ container_key: "proj/web" }] },
        { host_id: 2, containers: [] },
      ],
    });

    const byHost = await getFleetContainers([1, 2]);

    const [url] = (fetch as unknown as Mock).mock.calls[0];
    expect(url).toContain("/api/v1/containers?");
    expect(url).toContain("hosts=1%2C2");

    expect(byHost.get(1)?.[0].container_key).toBe("proj/web");
    // Present with an empty list, never absent: "runs none" and "never asked"
    // have to stay distinguishable, exactly as they do for the series.
    expect(byHost.get(2)).toEqual([]);
  });

  // An empty fleet asks nothing. Without the guard the id list joins to "",
  // toQueryString keeps the empty value rather than dropping it, and the hub
  // answers `?hosts=` with a 400 -- so a hub with no hosts registered and the
  // overview open would take one guaranteed-400 request every poll tick,
  // forever, with the page's catch hiding it.
  it("asks nothing when there are no hosts", async () => {
    mockFetch(200, { hosts: [] });

    expect(await getFleetContainers([])).toEqual(new Map());
    expect(fetch as unknown as Mock).not.toHaveBeenCalled();
  });

  // DELETE /api/v1/hosts/{id} answers 204 with no body at all. Calling
  // res.json() on that throws a SyntaxError, which would surface as a failed
  // delete even though the host is gone.
  it("does not try to parse a body out of a 204", async () => {
    (globalThis.fetch as unknown as Mock) = vi.fn().mockResolvedValue({
      ok: true,
      status: 204,
      json: () =>
        Promise.reject(new SyntaxError("Unexpected end of JSON input")),
    });

    await expect(deleteHost(7)).resolves.toBeUndefined();
  });

  // The write calls are the only ones that send a body; without the header
  // the hub's json.Decode still works, but nothing states the content type
  // and a proxy is free to mangle it.
  it("sends JSON writes with a method, a body and a content type", async () => {
    mockFetch(201, {
      id: 4,
      hostname: "web-04",
      last_seen: null,
      token: "t",
    });

    await createHost("web-04");

    const [url, init] = (fetch as unknown as Mock).mock.calls[0];
    expect(url).toBe("/api/v1/hosts");
    expect(init.method).toBe("POST");
    expect(init.headers["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body)).toEqual({ hostname: "web-04" });
  });
});
