import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BarSearch } from "./CommandPalette";
import * as api from "../lib/api";

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof api>("../lib/api");
  return { ...actual, getHosts: vi.fn(), getFleetContainers: vi.fn() };
});

const getHosts = vi.mocked(api.getHosts);
const getFleetContainers = vi.mocked(api.getFleetContainers);

function host(id: number, hostname: string): api.Host {
  return {
    id,
    hostname,
    last_seen: null,
    cpu_total: null,
    mem_used: null,
    mem_total: null,
    uptime_s: null,
    net_rx_bytes: null,
    net_tx_bytes: null,
    threads: null,
  };
}

function container(key: string, name: string | null): api.Container {
  return {
    id: 1,
    container_key: key,
    name,
    image: null,
    is_agent: false,
    last_seen: "2026-01-01T00:00:00Z",
    docker_state: "running",
    health: null,
    state_since: null,
    restart_count: null,
    labels: null,
    started_at: null,
    restarts_window: 0,
    recreates_window: 0,
    last_restart: null,
    restarts_window_seconds: 3600,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  getHosts.mockResolvedValue([host(7, "db-011"), host(9, "web-01")]);
  getFleetContainers.mockResolvedValue(
    new Map([
      [7, [container("pg", "postgres"), container("9f2c1ab3", null)]],
      [9, [container("nginx", "nginx")]],
    ]),
  );
});

/** Opens the palette the way a reader does, and waits for the inventory. */
async function open(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    screen.getByRole("button", { name: "Search hosts and containers" }),
  );
  expect(await screen.findByRole("option", { name: /db-011/ })).toBeVisible();
}

describe("the bar's search", () => {
  it("opens on the button and on Meta+K — the bar is reachable both ways", async () => {
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);

    expect(screen.queryByRole("dialog")).toBeNull();

    await user.keyboard("{Meta>}k{/Meta}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("opens on Control+K too, so the other chord is not a dead key", async () => {
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);

    await user.keyboard("{Control>}k{/Control}");

    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("lists the hosts before anything is typed — the shorter half of the inventory", async () => {
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);
    await open(user);

    const names = screen.getAllByRole("option").map((o) => o.textContent);
    expect(names).toEqual(["db-011", "web-01"]);
  });

  it("finds a container by name, and by the host it runs on", async () => {
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);
    await open(user);
    const field = screen.getByRole("combobox");

    await user.type(field, "postgres");
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "postgresdb-011",
    ]);

    await user.clear(field);
    await user.type(field, "db-011");
    // The host itself, then everything on it.
    expect(screen.getAllByRole("option")).toHaveLength(3);
  });

  it("falls back to the container key when Docker gave no name", async () => {
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);
    await open(user);

    await user.type(screen.getByRole("combobox"), "9f2c");

    expect(
      screen.getByRole("option", { name: /9f2c1ab3/ }),
    ).toBeInTheDocument();
  });

  it("navigates to the active row on Enter", async () => {
    const go = vi.fn();
    const user = userEvent.setup();
    render(<BarSearch go={go} />);
    await open(user);

    await user.type(screen.getByRole("combobox"), "nginx");
    await user.keyboard("{Enter}");

    expect(go).toHaveBeenCalledWith("/containers/9/nginx");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("moves the cursor with the arrow keys, and says which row it is on", async () => {
    const go = vi.fn();
    const user = userEvent.setup();
    render(<BarSearch go={go} />);
    await open(user);
    const field = screen.getByRole("combobox");

    expect(field).toHaveAttribute("aria-activedescendant", "pal-opt-0");
    await user.keyboard("{ArrowDown}");
    expect(field).toHaveAttribute("aria-activedescendant", "pal-opt-1");

    await user.keyboard("{Enter}");
    expect(go).toHaveBeenCalledWith("/hosts/9/overview");
  });

  it("closes on Escape and hands focus back to the control that opened it", async () => {
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);
    await open(user);

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Search hosts and containers" }),
    ).toHaveFocus();
  });

  it("says so when nothing matches, quoting what was typed", async () => {
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);
    await open(user);

    await user.type(screen.getByRole("combobox"), "zzz");

    expect(screen.getByText("No match for “zzz”.")).toBeInTheDocument();
    expect(screen.queryAllByRole("option")).toHaveLength(0);
  });

  it("still lists the hosts when the container request fails", async () => {
    getFleetContainers.mockRejectedValue(new Error("boom"));
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);
    await open(user);

    await user.type(screen.getByRole("combobox"), "db");

    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "db-011",
    ]);
  });

  it("reports a fleet it could not fetch at all", async () => {
    getHosts.mockRejectedValue(new Error("boom"));
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);
    await user.click(
      screen.getByRole("button", { name: "Search hosts and containers" }),
    );

    expect(
      await screen.findByText("Could not load the inventory."),
    ).toBeInTheDocument();
  });

  it("caps each kind and counts what it left out", async () => {
    getHosts.mockResolvedValue(
      Array.from({ length: 11 }, (_, i) => host(i + 1, `web-${i + 1}`)),
    );
    getFleetContainers.mockResolvedValue(new Map());
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);
    await user.click(
      screen.getByRole("button", { name: "Search hosts and containers" }),
    );

    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(8));
    expect(screen.getByText(/3 more matches/)).toBeInTheDocument();
  });

  it("links each row, so a row can be opened in its own tab", async () => {
    const user = userEvent.setup();
    render(<BarSearch go={vi.fn()} />);
    await open(user);

    expect(screen.getByRole("option", { name: /db-011/ })).toHaveAttribute(
      "href",
      "/hosts/7/overview",
    );
  });
});
