import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";
import * as api from "./lib/api";
import { ApiError } from "./lib/api";

vi.mock("./lib/api", async () => {
  const actual = await vi.importActual<typeof api>("./lib/api");
  return {
    ...actual,
    getHosts: vi.fn(),
    getSites: vi.fn(),
    getEvents: vi.fn(),
    getHost: vi.fn(),
    getContainers: vi.fn(),
    getMetrics: vi.fn(),
    getFilesystems: vi.fn(),
    getAddresses: vi.fn(),
    getPackages: vi.fn(),
    getUnits: vi.fn(),
  };
});

const host: api.Host = {
  id: 3,
  hostname: "web-01",
  site_id: null,
  last_seen: new Date().toISOString(),
  cpu_total: 4,
  mem_used: 1,
  mem_total: 8,
  uptime_s: 900,
  threads: null,
};

function goTo(path: string) {
  window.history.replaceState(null, "", path);
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.getHosts).mockResolvedValue([host]);
  vi.mocked(api.getSites).mockResolvedValue([]);
  vi.mocked(api.getEvents).mockResolvedValue([]);
  // Every call the host page fans out to. A vi.fn() with no resolved value
  // returns undefined, and the page's orNull() then reads .then on it --
  // which surfaces as an unhandled rejection rather than a failed test.
  vi.mocked(api.getContainers).mockResolvedValue([]);
  vi.mocked(api.getFilesystems).mockResolvedValue([]);
  vi.mocked(api.getAddresses).mockResolvedValue([]);
  vi.mocked(api.getPackages).mockResolvedValue([]);
  vi.mocked(api.getUnits).mockResolvedValue([]);
  goTo("/");
});

afterEach(() => {
  goTo("/");
});

describe("App routing", () => {
  it("renders the fleet overview at the root", async () => {
    render(<App />);

    expect(await screen.findByText("web-01")).toBeInTheDocument();
  });

  // The overview is remounted by every navigation back to it, so its first
  // render has no data. Rendering the page anyway showed "No hosts yet" and
  // a fleet of zeros until the request landed -- an answer about a fleet
  // nobody had been asked about yet.
  it("does not claim an empty fleet before the first response lands", async () => {
    let release: (value: (typeof host)[]) => void = () => {};
    vi.mocked(api.getHosts).mockReturnValue(
      new Promise((resolve) => {
        release = resolve;
      }),
    );

    render(<App />);

    expect(screen.queryByText("No hosts yet")).not.toBeInTheDocument();
    expect(screen.getByText("Loading…")).toBeInTheDocument();

    release([host]);
    expect(await screen.findByText("web-01")).toBeInTheDocument();
  });

  // The hub serves index.html for any path that is not a file, so a deep
  // link is a real URL a reload must survive. This is the app's half of
  // that contract.
  it("renders a deep link straight from the address bar", async () => {
    vi.mocked(api.getHost).mockResolvedValue({
      ...host,
      site_name: null,
      os: null,
      kernel: null,
      arch: null,
      virtualization: null,
      agent_version: null,
      capabilities: [],
    } as unknown as api.HostDetail);
    vi.mocked(api.getMetrics).mockResolvedValue({
      family: "host",
      tier: "raw",
      step_s: 60,
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T01:00:00Z" },
      requested_window: {
        from: "2026-08-10T00:00:00Z",
        to: "2026-08-10T01:00:00Z",
      },
      warnings: [],
      key_columns: [],
      columns: [],
      series: [],
      truncated: false,
    });
    goTo("/hosts/3/graphs");

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "web-01" }),
    ).toBeInTheDocument();
  });

  it("says so rather than rendering an empty shell for an unknown path", async () => {
    goTo("/nowhere");

    render(<App />);

    expect(await screen.findByText(/no such page/i)).toBeInTheDocument();
  });

  // Every link in this app is a real anchor -- middle-click and copy-link
  // have to work -- and one delegated handler at the root routes them, so a
  // link added to any page is routed without being wired to anything.
  it("routes a plain anchor rendered by a page, without a full page load", async () => {
    render(<App />);
    const link = await screen.findByRole("link", { name: "web-01" });

    await userEvent.click(link);

    await waitFor(() =>
      expect(window.location.pathname).toBe("/hosts/3/overview"),
    );
  });

  // A 401 is the session having expired. It is a routing decision, not
  // something to render: leaving the reader on an empty overview with no
  // explanation is the failure here.
  it("sends an expired session to the login page", async () => {
    vi.mocked(api.getHosts).mockRejectedValue(
      new ApiError(401, "unauthorized"),
    );

    render(<App />);

    await waitFor(() => expect(window.location.pathname).toBe("/login"));
  });

  // Filters and ranges live in the URL so a filtered view is a link someone
  // can send.
  it("keeps the fleet range in the URL", async () => {
    goTo("/?range=6h");

    render(<App />);

    expect(await screen.findByText("web-01")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "6h" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  // The fleet offers three windows; Settings can store five, and a link can
  // carry any of them. Being handed one this page does not offer must leave
  // the page working rather than blank -- the range still reaches the charts
  // and their labels, it simply has no button to light up.
  it("still renders when handed a range it does not offer", async () => {
    goTo("/?range=30d");

    render(<App />);

    expect(await screen.findByText("web-01")).toBeInTheDocument();
  });

  // A toggle is a way of looking at this page, not a different place:
  // pushing an entry per click would turn Back into an undo of fiddling.
  it("replaces rather than pushes when a view toggle changes", async () => {
    render(<App />);
    await screen.findByText("web-01");
    const before = window.history.length;

    await userEvent.click(screen.getByRole("button", { name: "1h" }));

    await waitFor(() => expect(window.location.search).toContain("range=1h"));
    expect(window.history.length).toBe(before);
  });
});
