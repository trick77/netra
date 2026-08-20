import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { FleetPage, DENSITY_KEY } from "./FleetPage";
import type { HostRow } from "./hostColumns";

function row(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    site_id: null,
    site_name: null,
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

describe("FleetPage below the mobile breakpoint", () => {
  // Cards are automatic here, not a preference (spec §4.5): a six-column
  // host table does not survive 390px, so a stored "table" choice must not
  // be able to produce a page that scrolls sideways.
  it("renders cards even when the stored preference is table", () => {
    localStorage.setItem(DENSITY_KEY, "table");
    stubViewport(true);

    render(<FleetPage rows={[row()]} />);

    expect(screen.queryByRole("table")).toBeNull();
    expect(document.querySelectorAll(".hcard")).toHaveLength(1);
  });

  // The preference is not overwritten -- it is what the browser goes back
  // to at a width where it applies.
  it("keeps honouring the stored preference on a wide viewport", () => {
    localStorage.setItem(DENSITY_KEY, "table");
    stubViewport(false);

    render(<FleetPage rows={[row()]} />);

    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(localStorage.getItem(DENSITY_KEY)).toBe("table");
  });

  // A toggle that says "Table" while the page shows cards is a control
  // lying about the thing it controls.
  it("shows the toggle in the state the page is actually in", () => {
    localStorage.setItem(DENSITY_KEY, "table");
    stubViewport(true);

    render(<FleetPage rows={[row()]} />);

    expect(screen.getByRole("button", { name: /cards/i })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  // jsdom has no matchMedia unless a test installs one, and neither do some
  // embedded browsers. Missing must mean "not narrow", never a thrown
  // render.
  it("renders when the browser has no matchMedia at all", () => {
    vi.stubGlobal("matchMedia", undefined);

    render(<FleetPage rows={[row()]} />);

    expect(screen.getByRole("table")).toBeInTheDocument();
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
