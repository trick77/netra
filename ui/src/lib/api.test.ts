import { describe, expect, it, vi, afterEach, type Mock } from "vitest";
import {
  getHosts,
  getMetrics,
  createHost,
  deleteHost,
  createSite,
  patchSite,
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
      site_id: null,
      last_seen: null,
      token: "t",
    });

    await createHost("web-04", 3);

    const [url, init] = (fetch as unknown as Mock).mock.calls[0];
    expect(url).toBe("/api/v1/hosts");
    expect(init.method).toBe("POST");
    expect(init.headers["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body)).toEqual({ hostname: "web-04", site_id: 3 });
  });

  // CreateSite takes a name and a provider and nothing else; a site with no
  // provider sends null rather than omitting the key, so the hub's decode
  // sees a nil pointer either way.
  it("creates a site with an explicit null provider", async () => {
    mockFetch(201, { id: 2, name: "fsn1", provider_id: null });

    await createSite("fsn1");

    const [url, init] = (fetch as unknown as Mock).mock.calls[0];
    expect(url).toBe("/api/v1/sites");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body)).toEqual({ name: "fsn1", provider_id: null });
  });

  // PatchSite leaves every column the body does not mention alone, which is
  // what keeps a manually set coordinate safe from a caller that only meant
  // to set an address -- so the patch must carry exactly the changed fields
  // and no others. It answers 204, which request() must not try to parse.
  it("patches only the fields it is given, and parses no body out of the 204", async () => {
    (globalThis.fetch as unknown as Mock) = vi.fn().mockResolvedValue({
      ok: true,
      status: 204,
      json: () =>
        Promise.reject(new SyntaxError("Unexpected end of JSON input")),
    });

    await expect(
      patchSite(2, { facility: "DC15", latitude: 47.37 }),
    ).resolves.toBeUndefined();

    const [url, init] = (fetch as unknown as Mock).mock.calls[0];
    expect(url).toBe("/api/v1/sites/2");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(init.body)).toEqual({
      facility: "DC15",
      latitude: 47.37,
    });
  });
});
