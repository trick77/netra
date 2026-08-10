import { describe, expect, it, vi, afterEach, type Mock } from "vitest";
import { getHosts, getMetrics, ApiError } from "./api";

function mockFetch(status: number, body: unknown) {
  return vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
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

  it("passes the metrics query through verbatim", async () => {
    mockFetch(200, { family: "host", tier: "raw", columns: [], series: [] });
    await getMetrics(1, { family: "host", from: "-24h", to: "now" });
    const [url] = (fetch as unknown as Mock).mock.calls[0];
    expect(url).toContain("/api/v1/hosts/1/metrics?");
    expect(url).toContain("family=host");
  });
});
