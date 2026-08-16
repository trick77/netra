import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { HostPage } from "./HostPage";
import { ChartPage } from "./ChartPage";
import * as api from "../../lib/api";

vi.mock("../../lib/api", async () => {
  const actual = await vi.importActual<typeof api>("../../lib/api");
  return {
    ...actual,
    getHost: vi.fn(),
    getMetrics: vi.fn(),
    getContainers: vi.fn(),
    getFilesystems: vi.fn(),
    getAddresses: vi.fn(),
    getPackages: vi.fn(),
    getUnits: vi.fn(),
    getEvents: vi.fn(),
  };
});

const host = {
  id: 7,
  hostname: "kessel",
  site_id: null,
  last_seen: "2026-08-10T01:00:00Z",
  cpu_total: 12,
  mem_used: 4e9,
  mem_total: 8e9,
  uptime_s: 86_400,
  net_rx_bytes: null,
  net_tx_bytes: null,
  threads: 4,
  capabilities: {},
} as unknown as api.HostDetail;

function metrics(family: string): api.MetricsResponse {
  return {
    family,
    tier: "raw",
    step_s: 60,
    // The window has to CONTAIN the samples below, or every band grids to
    // all-null and the panel renders "not collected" with no chart at all.
    window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T01:00:00Z" },
    requested_window: {
      from: "2025-08-10T00:00:00Z",
      to: "2025-08-10T01:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: ["cpu_user", "cpu_system", "cpu_iowait", "cpu_steal"],
    series: [
      {
        key: {},
        points: [
          [1_754_784_000_000, 4, 1, 0, 0],
          [1_754_784_060_000, 5, 1, 0, 0],
        ],
      },
    ],
    truncated: false,
  } as unknown as api.MetricsResponse;
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  localStorage.setItem("netra.range", "1h");
  vi.mocked(api.getHost).mockResolvedValue(host);
  vi.mocked(api.getMetrics).mockImplementation((_id, params) =>
    Promise.resolve(metrics(params.family)),
  );
  vi.mocked(api.getContainers).mockResolvedValue([]);
  vi.mocked(api.getFilesystems).mockResolvedValue([]);
  vi.mocked(api.getAddresses).mockResolvedValue([]);
  vi.mocked(api.getPackages).mockResolvedValue([]);
  vi.mocked(api.getUnits).mockResolvedValue([]);
  vi.mocked(api.getEvents).mockResolvedValue([]);
});

/**
 * The Graphs tab used to open a chart in a modal, and this file used to
 * protect the bug that caused: "clicking on a graph and changing the range
 * should only change the chart's range, currently it does all other things
 * too" -- the dialog's picker was the PAGE's setter, so one click refetched
 * seven families for twenty panels.
 *
 * A chart is its own page now, which answers that structurally: there is no
 * page behind it to re-range, and the window someone is looking at is a link
 * they can send. What is left to pin is that the tab hands off correctly and
 * that the page stays as cheap as the dialog was.
 */
describe("opening a graph from the tab", () => {
  it("makes each panel a link to that chart's own page", async () => {
    render(<HostPage hostId={7} tab="graphs" onTabChange={() => {}} />);
    await screen.findByText("System");

    // The slug, not the title: a title is UI copy and moves.
    const link = screen.getByRole("link", { name: /CPU time breakdown/i });
    // With the range the tab is showing: the link is the view someone is
    // looking at, and Back carries the page's query string home again.
    expect(link).toHaveAttribute(
      "href",
      "/hosts/7/chart/cpu-time-breakdown?range=1h",
    );
  });

  // A real anchor, so middle-click, copy-link and bookmark all work. App
  // delegates the plain-left-click case at the root, which is why there is
  // no onClick here to test.
  it("uses an anchor rather than a button, so the URL is reachable", async () => {
    render(<HostPage hostId={7} tab="graphs" onTabChange={() => {}} />);
    await screen.findByText("System");

    expect(screen.queryAllByRole("button", { name: /Enlarge/ })).toHaveLength(
      0,
    );
    expect(screen.getAllByRole("link").length).toBeGreaterThan(0);
  });

  // The property the old dialog was fixed to have, restated for the page:
  // looking at one chart must not cost what looking at the whole tab costs.
  it("costs one family, not the whole tab", async () => {
    render(
      <ChartPage
        hostId="7"
        slug="cpu-time-breakdown"
        range="1h"
        onRangeChange={() => {}}
        onBack={() => {}}
      />,
    );

    await waitFor(() => expect(api.getMetrics).toHaveBeenCalled());
    const families = new Set(
      vi.mocked(api.getMetrics).mock.calls.map((c) => c[1].family),
    );
    // One family. The several calls are the range strip's thumbnails, which
    // are the same chart over other windows.
    expect([...families]).toEqual(["host"]);
  });

  it("says so plainly when the slug names no chart", () => {
    render(
      <ChartPage
        hostId="7"
        slug="no-such-chart"
        range="1h"
        onRangeChange={() => {}}
        onBack={() => {}}
      />,
    );
    expect(screen.getByText(/No such chart/i)).toBeInTheDocument();
  });
});
