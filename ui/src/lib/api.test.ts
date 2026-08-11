import { describe, expect, it, vi, afterEach, type Mock } from "vitest";
import { getHosts, getMetrics, ApiError } from "./api";

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
});
