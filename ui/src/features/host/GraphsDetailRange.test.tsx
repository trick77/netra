import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HostPage } from "./HostPage";
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
    // The window has to CONTAIN the sample below: a point outside it grids
    // to all null, every band drops out, and the panel correctly renders
    // "not collected" -- with no chart to enlarge.
    window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T01:00:00Z" },
    requested_window: {
      from: "2025-08-10T00:00:00Z",
      to: "2025-08-10T01:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: ["cpu_user", "cpu_system", "cpu_iowait", "cpu_steal"],
    series: [{ key: {}, points: [[1_754_784_000_000, 4, 1, 0, 0]] }],
    truncated: false,
  } as unknown as api.MetricsResponse;
}

beforeEach(() => {
  vi.clearAllMocks();
  // The page's range comes from the remembered preference, so it is pinned
  // here rather than assumed: these tests are about the dialog DIFFERING
  // from the page, and a dialog sitting at the page's own range correctly
  // fetches nothing at all.
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
 * The reported bug, end to end on the page it was reported against.
 *
 * "Clicking on a graph and changing the range should only change the chart's
 * range, currently it does all other things too." It did: the dialog's
 * picker was the PAGE's setter, so one click refetched all seven families
 * for twenty panels and moved the toolbar behind the dialog.
 */
describe("enlarging a graph and widening it", () => {
  async function openFirstChart() {
    render(<HostPage hostId={7} tab="graphs" onTabChange={() => {}} />);
    await screen.findByText("System");
    const enlarge = screen.getAllByRole("button", { name: /Enlarge/ })[0]!;
    await userEvent.click(enlarge);
    return screen.getByRole("dialog");
  }

  it("leaves the page's own range control where it was", async () => {
    const dialog = await openFirstChart();

    await userEvent.click(within(dialog).getByRole("button", { name: "24h" }));
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "24h" }),
      ).toHaveAttribute("aria-pressed", "true"),
    );

    await userEvent.click(screen.getByRole("button", { name: "Close" }));

    // The toolbar is still on the range the page was loaded with. It used to
    // follow the dialog.
    expect(screen.getByRole("button", { name: "1h" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("refetches one family, not the whole tab", async () => {
    const dialog = await openFirstChart();
    await waitFor(() => expect(api.getMetrics).toHaveBeenCalled());

    // Seven families load the tab; anything beyond that is the dialog's.
    const beforeWiden = vi.mocked(api.getMetrics).mock.calls.length;

    await userEvent.click(within(dialog).getByRole("button", { name: "24h" }));
    await waitFor(() =>
      expect(vi.mocked(api.getMetrics).mock.calls.length).toBe(beforeWiden + 1),
    );

    // And it asked for a WIDER window than the page's, for one family only.
    const last = vi.mocked(api.getMetrics).mock.calls.at(-1)!;
    expect(last[1].step).toBe("5m");
  });
});
