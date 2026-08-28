import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";
import * as api from "./lib/api";
import { ApiError } from "./lib/api";

vi.mock("./lib/api", async () => {
  const actual = await vi.importActual<typeof api>("./lib/api");
  return {
    ...actual,
    getHosts: vi.fn(),
    getEvents: vi.fn(),
    getHost: vi.fn(),
    getContainers: vi.fn(),
    getMetrics: vi.fn(),
    getFilesystems: vi.fn(),
    getAddresses: vi.fn(),
    getPackages: vi.fn(),
    getUnits: vi.fn(),
    getConfig: vi.fn(),
  };
});

const host: api.Host = {
  id: 3,
  hostname: "web-01",
  last_seen: new Date().toISOString(),
  cpu_total: 4,
  mem_used: 1,
  mem_total: 8,
  uptime_s: 900,
  net_rx_bytes: null,
  net_tx_bytes: null,
  threads: null,
};

function goTo(path: string) {
  window.history.replaceState(null, "", path);
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.getHosts).mockResolvedValue([host]);
  vi.mocked(api.getEvents).mockResolvedValue([]);
  // Every call the host page fans out to. A vi.fn() with no resolved value
  // returns undefined, and the page's orNull() then reads .then on it --
  // which surfaces as an unhandled rejection rather than a failed test.
  vi.mocked(api.getContainers).mockResolvedValue([]);
  vi.mocked(api.getFilesystems).mockResolvedValue([]);
  vi.mocked(api.getAddresses).mockResolvedValue([]);
  vi.mocked(api.getPackages).mockResolvedValue([]);
  vi.mocked(api.getUnits).mockResolvedValue([]);
  // The host admin page's own two calls. Unmocked they fall through to the
  // real fetch, so a test that only asks which nav link is current would make
  // a network request in jsdom and settle after the test had finished.
  vi.mocked(api.getConfig).mockResolvedValue({ hub_url: "http://hub.test" });
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

  // The fleet draws every row over one fixed window, so it has no picker and
  // takes no range from the URL. A link carrying one is not an error: it is
  // ignored, and the page still renders the fleet.
  it("ignores a range in the URL on the fleet", async () => {
    goTo("/?range=6h");

    render(<App />);

    expect(await screen.findByText("web-01")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "6h" })).toBeNull();
    expect(screen.queryByRole("button", { name: "24h" })).toBeNull();
  });

  // A toggle is a way of looking at this page, not a different place:
  // pushing an entry per click would turn Back into an undo of fiddling.
  it("replaces rather than pushes when a range toggle changes", async () => {
    goTo("/hosts/3/graphs");
    render(<App />);
    await screen.findByRole("button", { name: "6h" });
    const before = window.history.length;

    await userEvent.click(screen.getByRole("button", { name: "1h" }));

    await waitFor(() => expect(window.location.search).toContain("range=1h"));
    expect(window.history.length).toBe(before);
  });
});

/**
 * The range used to scatter: every page fell back to its own hardcoded
 * literal -- the host page to "6h", the fleet to "24h" -- because nothing
 * ever wrote a choice back. Which range you got depended on where you were
 * rather than on what you had picked.
 */
describe("the range sticks", () => {
  function hostDetail() {
    return {
      ...host,
      os: null,
      kernel: null,
      arch: null,
      virtualization: null,
      agent_version: null,
      capabilities: [],
    } as unknown as api.HostDetail;
  }

  function noMetrics() {
    return {
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
    };
  }

  beforeEach(() => {
    localStorage.clear();
    vi.mocked(api.getHost).mockResolvedValue(hostDetail());
    vi.mocked(api.getMetrics).mockResolvedValue(
      noMetrics() as unknown as api.MetricsResponse,
    );
  });

  function pressed(name: string) {
    return screen.getByRole("button", { name }).getAttribute("aria-pressed");
  }

  it("carries a host page choice to the next page that offers one", async () => {
    goTo("/hosts/3/graphs");
    const hostPage = render(<App />);
    await screen.findByRole("button", { name: "1h" });

    await userEvent.click(screen.getByRole("button", { name: "1h" }));
    await waitFor(() => expect(localStorage.getItem("netra.range")).toBe("1h"));

    // Unmounted, not left on screen: a second App beside the first would put
    // two "1h" buttons in the document and the query below could not tell
    // which page it was asking about.
    hostPage.unmount();
    goTo("/hosts/3/graphs");
    render(<App />);

    // 6h is the literal this page used to hardcode -- the assertion is that
    // it no longer wins over what was actually picked.
    await waitFor(() => expect(pressed("1h")).toBe("true"));
    expect(pressed("6h")).toBe("false");
  });

  // The events page offers 30d and the host page does not. The host page has
  // to show something it can press, but the CHOICE is not overwritten --
  // coming back to a page that offers 30d shows 30d again, not the narrowed
  // version some other page had to display.
  it("shows a wider choice clamped without forgetting it", async () => {
    localStorage.setItem("netra.range", "30d");
    goTo("/hosts/3/graphs");

    render(<App />);
    await screen.findByRole("button", { name: "7d" });

    expect(pressed("7d")).toBe("true");
    expect(localStorage.getItem("netra.range")).toBe("30d");
  });

  // A link is the one thing that must beat the preference: it exists to show
  // someone the view you were looking at, not the view they last chose.
  it("lets an explicit link win over the remembered choice", async () => {
    localStorage.setItem("netra.range", "24h");
    goTo("/hosts/3/graphs?range=1h");

    render(<App />);
    await screen.findByRole("button", { name: "1h" });

    expect(pressed("1h")).toBe("true");
  });

  // The events page reads its range as one of the filters rather than on its
  // own, and its own parser only recognises the four windows it OFFERS -- so
  // a link carrying 6h was discarded outright and the reader's remembered
  // choice applied instead, which is the one thing a sent link exists to
  // override. Every other page honours the same 6h (clamped); this one
  // silently dropped it.
  it("lets a link win on the events page too, clamping rather than discarding", async () => {
    localStorage.setItem("netra.range", "30d");
    goTo("/events?range=6h");

    render(<App />);
    await screen.findByRole("button", { name: "24h" });

    // 24h, not 30d: the link asked for 6h, which this page widens to 24h.
    expect(pressed("24h")).toBe("true");
    expect(pressed("30d")).toBe("false");
  });

  // The store is user-editable and also whatever an older build wrote.
  it("falls back to the default rather than erroring on a stored nonsense", async () => {
    localStorage.setItem("netra.range", "99y");
    goTo("/hosts/3/graphs");

    render(<App />);

    await screen.findByRole("button", { name: "24h" });
    expect(pressed("24h")).toBe("true");
  });
});

// The rail is the one piece of chrome on every page, so it is also the one
// place a broken href strands a keyboard user with no way out.
describe("the nav rail", () => {
  const DESTINATIONS = [
    ["Fleet", "/"],
    ["Events", "/events"],
    ["Hosts", "/admin/hosts"],
    ["Settings", "/settings"],
  ] as const;

  it("offers every destination, each pointing at its own route", async () => {
    render(<App />);
    await screen.findByText("web-01");

    const rail = within(screen.getByRole("navigation", { name: "Primary" }));
    for (const [label, href] of DESTINATIONS) {
      expect(rail.getByRole("link", { name: label })).toHaveAttribute(
        "href",
        href,
      );
    }
  });

  // aria-current is what tells a screen reader which of four identical-looking
  // links is the page it is already on; the highlight alone says it to nobody.
  it.each(DESTINATIONS)("marks %s as the current page", async (label, path) => {
    goTo(path);

    render(<App />);
    const rail = within(
      await screen.findByRole("navigation", { name: "Primary" }),
    );

    expect(rail.getByRole("link", { name: label })).toHaveAttribute(
      "aria-current",
      "page",
    );
    // Exactly one, or "you are here" means nothing.
    expect(
      rail
        .getAllByRole("link")
        .filter((a) => a.getAttribute("aria-current") === "page"),
    ).toHaveLength(1);
  });

  // Sign-out is the one rail item that is not a link, and the distinction is
  // load-bearing: POST /logout is what clears the cookie, and a GET would let
  // anything that follows links end the session. The form's method and action
  // are the whole feature, so they are what this pins.
  it("signs out by posting to the route that clears the session", async () => {
    render(<App />);
    await screen.findByText("web-01");

    const rail = within(screen.getByRole("navigation", { name: "Primary" }));
    const button = rail.getByRole("button", { name: "Sign out" });

    expect(button).toHaveAttribute("type", "submit");
    const form = button.closest("form");
    expect(form).toHaveAttribute("action", "/logout");
    expect(form?.getAttribute("method")?.toLowerCase()).toBe("post");
  });

  // The rail now precedes the content in DOM order on every page, so the skip
  // link is the only thing between a keyboard user and walking the whole nav.
  it("keeps a skip link that resolves to the main region", async () => {
    render(<App />);
    await screen.findByText("web-01");

    expect(
      screen.getByRole("link", { name: "Skip to content" }),
    ).toHaveAttribute("href", "#main");
    expect(screen.getByRole("main")).toHaveAttribute("id", "main");
  });
});

// The chart page has more than one way in, so Back has more than one place
// to go. It was hardcoded to the host's Graphs tab -- correct while that
// tab was the only entrance, and wrong the moment the fleet row's traffic
// cell started linking here: it dropped the reader a level DEEPER than
// where they started, on a page they had never seen.
describe("the old chart-page URL", () => {
  beforeEach(() => {
    vi.mocked(api.getHost).mockResolvedValue({
      ...host,
    } as unknown as api.HostDetail);
    vi.mocked(api.getMetrics).mockResolvedValue({
      family: "net",
      tier: "raw",
      step_s: 60,
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T01:00:00Z" },
      requested_window: {
        from: "2026-08-10T00:00:00Z",
        to: "2026-08-10T01:00:00Z",
      },
      warnings: [],
      key_columns: ["iface"],
      columns: ["rx_bytes", "tx_bytes"],
      series: [],
      truncated: false,
    } as unknown as api.MetricsResponse);
  });

  // Every chart opens in a dialog now, so a chart has no URL of its own to
  // land on. The links readers have already sent must still arrive somewhere
  // true rather than on "no such page": the subject tab that draws that
  // chart. host-traffic is a network panel, so Network is the answer -- it
  // used to be Graphs for every slug alike, because Graphs held them all.
  it("lands on the tab drawing that chart rather than a not-found page", async () => {
    goTo("/hosts/3/chart/host-traffic?range=6h&from=fleet");
    render(<App />);

    // The tabs are real links carrying aria-current, not ARIA tabs -- see
    // Tabs.tsx.
    const network = await screen.findByRole("link", { name: "Network" });
    expect(network).toHaveAttribute("aria-current", "page");
  });
});
