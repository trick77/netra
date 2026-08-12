import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { HostDetail, MetricsResponse } from "../../lib/api";

// The only file in this feature that talks to the network, so the only one
// that mocks it: the four tab components take plain props and are tested
// without any async at all.
vi.mock("../../lib/api", () => ({
  getHost: vi.fn(),
  getContainers: vi.fn(),
  getFilesystems: vi.fn(),
  getAddresses: vi.fn(),
  getPackages: vi.fn(),
  getUnits: vi.fn(),
  getMetrics: vi.fn(),
  getEvents: vi.fn(),
}));

import * as api from "../../lib/api";
import { HostPage, HOST_TABS, hostTabHref } from "./HostPage";

const host: HostDetail = {
  id: 7,
  hostname: "kessel",
  site_id: 3,
  last_seen: new Date().toISOString(),
  cpu_total: 12,
  mem_used: 4_000_000_000,
  mem_total: 8_000_000_000,
  uptime_s: 86_400,
  site_name: "Zurich",
  provider_name: "Hetzner",
  fingerprint: "fp",
  host_type: "vps",
  agent_version: "0.4.1",
  go_version: "go1.25",
  build_commit: "abc1234",
  kernel: "6.8.0-31-generic",
  os_name: "Ubuntu 24.04",
  arch: "amd64",
  cpu_model: "EPYC 7003",
  cores: 4,
  threads: 8,
  memory_total: 8_000_000_000,
  latitude: null,
  longitude: null,
  created_at: "2026-01-01T00:00:00Z",
  capabilities: { docker: "ok", smart: "not permitted" },
};

function metrics(family: string): MetricsResponse {
  return {
    family,
    tier: "raw",
    step_s: 60,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T01:00:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T01:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: ["cpu_total"],
    series: [{ key: {}, points: [[1_754_784_000_000, 4]] }],
    truncated: false,
  };
}

beforeEach(() => {
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

describe("hostTabHref", () => {
  it("is the URL contract Wave 5's router has to honour", () => {
    expect(hostTabHref(7, "graphs")).toBe("/hosts/7/graphs");
    expect(hostTabHref("7", "overview")).toBe("/hosts/7/overview");
  });
});

describe("HostPage", () => {
  it("renders every tab as a link to its own URL, with no More menu", async () => {
    render(<HostPage hostId={7} tab="overview" onTabChange={() => {}} />);
    await screen.findByRole("heading", { name: "kessel" });

    for (const tab of HOST_TABS) {
      expect(screen.getByRole("link", { name: tab.label })).toHaveAttribute(
        "href",
        `/hosts/7/${tab.id}`,
      );
    }
    expect(screen.queryByText(/more/i)).toBeNull();
  });

  it("hands the router the tab id instead of navigating itself", async () => {
    const onTabChange = vi.fn();
    render(<HostPage hostId={7} tab="overview" onTabChange={onTabChange} />);
    await screen.findByRole("heading", { name: "kessel" });

    await userEvent.click(screen.getByRole("link", { name: "Graphs" }));
    expect(onTabChange).toHaveBeenCalledWith("graphs");
  });

  it("keeps one header and one range control across tabs, and the range survives the swap", async () => {
    const { rerender } = render(
      <HostPage hostId={7} tab="overview" onTabChange={() => {}} />,
    );
    await screen.findByRole("heading", { name: "kessel" });
    // Scoped to the header: the same facts also appear in Overview's
    // System card, and it is the header's copy that must never change.
    const header = within(screen.getByRole("banner", { name: "Host summary" }));
    expect(header.getByText(/Zurich/)).toBeInTheDocument();
    expect(header.getByText(/Ubuntu 24\.04/)).toBeInTheDocument();
    expect(header.getByText(/6\.8\.0-31-generic/)).toBeInTheDocument();
    expect(header.getByText(/amd64/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "24h" }));

    rerender(<HostPage hostId={7} tab="graphs" onTabChange={() => {}} />);
    await screen.findByText("System");
    expect(screen.getByRole("heading", { name: "kessel" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "24h" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    // One control, not one per panel: the Graphs tab draws many charts and
    // every one of them is driven by the header's range.
    expect(screen.getAllByRole("group")).toHaveLength(1);
  });

  it("asks the hub for absolute times only", async () => {
    render(<HostPage hostId={7} tab="graphs" onTabChange={() => {}} />);
    await waitFor(() => expect(api.getMetrics).toHaveBeenCalled());
    for (const call of vi.mocked(api.getMetrics).mock.calls) {
      expect(call[1].from).toMatch(/^\d{4}-\d{2}-\d{2}T/);
      expect(call[1].to).toMatch(/^\d{4}-\d{2}-\d{2}T/);
    }
  });

  it("scopes the events tab to this host", async () => {
    render(<HostPage hostId={7} tab="events" onTabChange={() => {}} />);
    await waitFor(() => expect(api.getEvents).toHaveBeenCalled());
    expect(vi.mocked(api.getEvents).mock.calls[0][0]).toMatchObject({
      host: 7,
    });
  });

  // The fleet list's Uptime column carried a "rebooted N ago" warning. When
  // that column was removed, the comment left in its place claimed the
  // warning "lives on there too, as the header's own status" -- and nothing
  // did: hostStatus() has no reboot branch and grepping "rebooted" matched
  // only the comment asserting it. This is that warning, restored where the
  // comment said it was.
  it("warns in the header about a host that rebooted minutes ago", async () => {
    vi.mocked(api.getHost).mockResolvedValue({ ...host, uptime_s: 100 });

    render(<HostPage hostId={7} tab="overview" onTabChange={() => {}} />);

    const header = await screen.findByRole("banner", { name: "Host summary" });
    expect(within(header).getByText(/rebooted .* ago/)).toBeInTheDocument();
  });

  // "rebooted", not the duration alone. Badge's dot is aria-hidden, so a
  // screen reader hearing "1 m 40 s" cannot tell this host from one up for
  // "266 d 6 h", and a deuteranope sees only a hue change -- the state would
  // ride on colour alone. A duration is not a severity.
  it("says the word rather than colouring a bare duration", async () => {
    vi.mocked(api.getHost).mockResolvedValue({ ...host, uptime_s: 100 });

    render(<HostPage hostId={7} tab="overview" onTabChange={() => {}} />);

    const badge = await screen.findByText(/rebooted/);
    expect(badge.textContent).toMatch(/rebooted 1 m 40 s ago/);
    expect(badge.className).toContain("badge");
  });

  // A host up for a day is the overwhelmingly common case, and a badge on
  // every header would spend the reader's first stop on a non-event. It also
  // must not displace the reporting status beside it.
  it("says nothing about a host that has been up for a day", async () => {
    render(<HostPage hostId={7} tab="overview" onTabChange={() => {}} />);

    const header = await screen.findByRole("banner", { name: "Host summary" });
    expect(within(header).queryByText(/rebooted/)).toBeNull();
    expect(within(header).getByText("online")).toBeInTheDocument();
  });
});
