import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { HostDetail, MetricsResponse } from "../../lib/api";
import { ABSENT } from "../../lib/format";

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
  net_rx_bytes: null,
  net_tx_bytes: null,
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
    expect(hostTabHref(7, "system")).toBe("/hosts/7/system");
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

    await userEvent.click(screen.getByRole("link", { name: "System" }));
    expect(onTabChange).toHaveBeenCalledWith("system");
  });

  it("keeps one header and one range control across tabs, and the range survives the swap", async () => {
    const { rerender } = render(
      <HostPage hostId={7} tab="overview" onTabChange={() => {}} />,
    );
    await screen.findByRole("heading", { name: "kessel" });
    // The site is all the header states about the machine. OS, kernel and
    // arch were here too until the System card was found to be printing the
    // same three a few centimetres below; the site is the one of the four
    // that card does not carry.
    const header = within(screen.getByRole("banner", { name: "Host summary" }));
    expect(header.getByText(/Zurich/)).toBeInTheDocument();
    expect(header.queryByText(/Ubuntu 24\.04/)).toBeNull();
    expect(header.queryByText(/6\.8\.0-31-generic/)).toBeNull();
    expect(header.queryByText(/amd64/)).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "24h" }));

    rerender(<HostPage hostId={7} tab="system" onTabChange={() => {}} />);
    await screen.findByText("Resources");
    expect(screen.getByRole("heading", { name: "kessel" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "24h" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    // One control, not one per panel: a subject tab draws many charts and
    // every one of them is driven by the header's range.
    expect(screen.getAllByRole("group")).toHaveLength(1);
  });

  it("asks the hub for absolute times only", async () => {
    render(<HostPage hostId={7} tab="system" onTabChange={() => {}} />);
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

  // A host with no site is the common case on a fresh install, and the header
  // used to answer it with a bare em dash: a placeholder with no label beside
  // it to say which fact was missing, which reads as a rendering fault. The
  // line is simply not there instead.
  it("says nothing at all about the site of a host that has none", async () => {
    vi.mocked(api.getHost).mockResolvedValue({ ...host, site_name: null });

    render(<HostPage hostId={7} tab="overview" onTabChange={() => {}} />);

    const header = await screen.findByRole("banner", { name: "Host summary" });
    // The one .meta left is "last seen", which carries its own label -- an
    // em dash under a word that says what is missing is not the same thing.
    const meta = [...header.querySelectorAll(".meta")];
    expect(meta).toHaveLength(1);
    expect(meta[0].textContent).toMatch(/^last seen/);
    expect(meta[0].textContent).not.toContain(ABSENT);
    // The rest of the header is untouched by the site being absent.
    expect(within(header).getByText("online")).toBeInTheDocument();
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

  // uptime_s is host_current's LAST REPORTED value, not a live clock: a
  // machine that booted and then died keeps that 100 in the database for
  // ever. Announcing "rebooted 1 m 40 s ago" beside an "offline" badge dates
  // a stale reading to now -- the absent-as-a-fact inversion this warning
  // exists to expose, committed by the warning itself.
  it("says nothing about a host whose last uptime was reported days ago", async () => {
    vi.mocked(api.getHost).mockResolvedValue({
      ...host,
      uptime_s: 100,
      last_seen: new Date(Date.now() - 3 * 24 * 3600 * 1000).toISOString(),
    });

    render(<HostPage hostId={7} tab="overview" onTabChange={() => {}} />);

    const header = await screen.findByRole("banner", { name: "Host summary" });
    expect(within(header).getByText("offline")).toBeInTheDocument();
    expect(within(header).queryByText(/rebooted/)).toBeNull();
  });

  // This page fetched its record once and never again, while the badge beside
  // the hostname judges that record against the clock at every render. Left
  // open past the three-scrape threshold, the next render of any kind -- a tab
  // click is enough -- called a host that had been posting the whole time
  // offline, blanked its traffic gauges and told the Overview it had last
  // reported four minutes ago. A record judged against a live clock has to
  // move with it.
  it("keeps a host that is still posting online after the page has been open past the stale threshold", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      vi.mocked(api.getHost).mockImplementation(() =>
        Promise.resolve({ ...host, last_seen: new Date().toISOString() }),
      );

      const { rerender } = render(
        <HostPage hostId={7} tab="overview" onTabChange={() => {}} />,
      );
      const header = await screen.findByRole("banner", {
        name: "Host summary",
      });
      expect(within(header).getByText("online")).toBeInTheDocument();

      // Five minutes, well past the 180s in lib/host.ts.
      await act(async () => {
        vi.advanceTimersByTime(5 * 60_000);
      });
      // The tab click that used to expose it: nothing re-rendered this page
      // between polls, so the frozen record only reached the badge when
      // something else made it paint.
      rerender(<HostPage hostId={7} tab="system" onTabChange={() => {}} />);
      await screen.findByText("Resources");

      expect(within(header).getByText("online")).toBeInTheDocument();
      expect(within(header).queryByText("offline")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  // The other half of the same fact: the fix must not be "never say offline".
  // A host whose record stops advancing while someone watches its page is
  // exactly what the badge is for.
  it("says offline once the record it polls stops advancing", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      vi.mocked(api.getHost).mockResolvedValue({
        ...host,
        last_seen: new Date().toISOString(),
      });

      render(<HostPage hostId={7} tab="overview" onTabChange={() => {}} />);
      const header = await screen.findByRole("banner", {
        name: "Host summary",
      });
      expect(within(header).getByText("online")).toBeInTheDocument();

      await act(async () => {
        vi.advanceTimersByTime(5 * 60_000);
      });

      expect(within(header).getByText("offline")).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  // The charts under the header are on the same tick, for the same reason:
  // a page that looks live and is frozen is the bug, not just the badge.
  it("refetches the active tab's families on the tick", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      render(<HostPage hostId={7} tab="system" onTabChange={() => {}} />);
      await waitFor(() => expect(api.getMetrics).toHaveBeenCalled());
      const before = vi.mocked(api.getMetrics).mock.calls.length;

      await act(async () => {
        vi.advanceTimersByTime(60_000);
      });

      expect(vi.mocked(api.getMetrics).mock.calls.length).toBeGreaterThan(
        before,
      );
    } finally {
      vi.useRealTimers();
    }
  });

  // usePoll keeps the last good record when a poll fails, which is right --
  // but silently it is a frozen page that looks live, and Refresh would
  // change nothing visible. The numbers stay and the page says so.
  it("says the readings stopped refreshing rather than replacing the page", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      vi.mocked(api.getHost)
        .mockResolvedValueOnce(host)
        .mockRejectedValue(new Error("network down"));

      render(<HostPage hostId={7} tab="overview" onTabChange={() => {}} />);
      await screen.findByRole("heading", { name: "kessel" });

      await act(async () => {
        vi.advanceTimersByTime(60_000);
      });

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "stopped refreshing",
      );
      // Still the host's page, not an error page: the hostname and the
      // readings under it are what the reader was looking at.
      expect(
        screen.getByRole("heading", { name: "kessel" }),
      ).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  // Host detail was the one screen that handed its poll error nowhere, so an
  // expired session left it on data it could no longer refresh while every
  // other screen went to the login page.
  it("hands a poll failure to the screen above it", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const failure = new Error("unauthorized");
      vi.mocked(api.getHost)
        .mockResolvedValueOnce(host)
        .mockRejectedValue(failure);
      const onPollError = vi.fn();

      render(
        <HostPage
          hostId={7}
          tab="overview"
          onTabChange={() => {}}
          onPollError={onPollError}
        />,
      );
      await screen.findByRole("heading", { name: "kessel" });

      await act(async () => {
        vi.advanceTimersByTime(60_000);
      });

      expect(onPollError).toHaveBeenCalledWith(failure);
    } finally {
      vi.useRealTimers();
    }
  });
});
