import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { FleetPage } from "./FleetPage";
import type { HostRow } from "./hostColumns";

function row(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    site_id: null,
    window: null,
    last_seen: new Date().toISOString(),
    cpu_total: null,
    mem_used: null,
    mem_total: null,
    uptime_s: 900,
    threads: null,
    cpu: [],
    reporting: [],
    mem: [],
    rx: [],
    tx: [],
    rxPeak: [],
    txPeak: [],
    net_rx_bytes: null,
    net_tx_bytes: null,
    fullest: null,
    disk: [],
    oomKills: null,
    dropped: null,
    postFailures: null,
    ...overrides,
  };
}

/** Installs a matchMedia that answers `narrow` for the mobile breakpoint. */
function stubViewport(narrow: boolean) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      matches: narrow && query.includes("max-width"),
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("FleetPage at any viewport", () => {
  // The card grid is gone, and with it the width-dependent branch: the fleet
  // is the table everywhere. Narrow screens scroll it sideways, which is a
  // deliberate trade for one rendering of the list rather than two that have
  // to be kept saying the same thing.
  it("renders the table below the mobile breakpoint", () => {
    stubViewport(true);

    render(<FleetPage rows={[row()]} />);

    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(document.querySelectorAll(".hcard")).toHaveLength(0);
  });

  it("renders the table on a wide viewport", () => {
    stubViewport(false);

    render(<FleetPage rows={[row()]} />);

    expect(screen.getByRole("table")).toBeInTheDocument();
  });

  // jsdom has no matchMedia unless a test installs one, and neither do some
  // embedded browsers. A missing one must never be a thrown render.
  it("renders when the browser has no matchMedia at all", () => {
    vi.stubGlobal("matchMedia", undefined);

    render(<FleetPage rows={[row()]} />);

    expect(screen.getByRole("table")).toBeInTheDocument();
  });

  // The window is fixed, so there is no control to change it -- see
  // FLEET_RANGE. A picker here would be the reader setting up the question
  // before they can ask it.
  it("offers no range picker", () => {
    stubViewport(false);

    render(<FleetPage rows={[row()]} />);

    expect(screen.queryByRole("button", { name: /^24h$/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^6h$/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /cards/i })).toBeNull();
  });
});

describe("keyboard access", () => {
  // The first thing anyone does with a list of hosts is narrow it.
  it("focuses the filter on /", async () => {
    stubViewport(false);
    render(<FleetPage rows={[row()]} />);
    const filter = screen.getByRole("searchbox", { name: /filter hosts/i });

    await userEvent.keyboard("/");

    expect(filter).toHaveFocus();
  });

  // Otherwise typing a path into any field on the page would rip the cursor
  // out of it mid-word.
  it("leaves / alone while something is already being typed into", async () => {
    stubViewport(false);
    render(<FleetPage rows={[row({ hostname: "web-01" })]} />);
    const filter = screen.getByRole("searchbox", { name: /filter hosts/i });
    await userEvent.click(filter);

    await userEvent.keyboard("var/log");

    expect(filter).toHaveValue("var/log");
  });
});
