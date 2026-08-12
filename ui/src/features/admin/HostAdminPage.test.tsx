import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  HostAdminPage,
  SETUP_SCRIPT_URL,
  HUB_URL_PLACEHOLDER,
} from "./HostAdminPage";
import * as api from "../../lib/api";

vi.mock("../../lib/api", async () => {
  const actual = await vi.importActual<typeof api>("../../lib/api");
  return {
    ...actual,
    getHosts: vi.fn(),
    getSites: vi.fn(),
    getConfig: vi.fn(),
    createHost: vi.fn(),
    rotateHostToken: vi.fn(),
    deleteHost: vi.fn(),
  };
});

const getHosts = vi.mocked(api.getHosts);
const getSites = vi.mocked(api.getSites);
const createHost = vi.mocked(api.createHost);
const rotateHostToken = vi.mocked(api.rotateHostToken);
const deleteHost = vi.mocked(api.deleteHost);

const host: api.Host = {
  id: 7,
  hostname: "web-01",
  site_id: 1,
  last_seen: null,
  cpu_total: null,
  mem_used: null,
  mem_total: null,
  uptime_s: null,
  threads: null,
};

const site: api.Site = {
  id: 1,
  provider_id: null,
  name: "zrh1",
  facility: null,
  address: null,
  latitude: null,
  longitude: null,
  country_code: null,
  timezone: null,
};

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  getHosts.mockResolvedValue([]);
  getSites.mockResolvedValue([site]);
  vi.mocked(api.getConfig).mockResolvedValue({ hub_url: "" });
});

describe("HostAdminPage", () => {
  it("offers host creation from the empty state — that screen is the onboarding", async () => {
    const user = userEvent.setup();
    render(<HostAdminPage />);

    expect(await screen.findByText("No hosts yet")).toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: "Add the first host" }),
    );

    expect(screen.getByLabelText("Hostname")).toBeInTheDocument();
  });

  it("lists hosts, and says never rather than a dash for one that has not reported", async () => {
    getHosts.mockResolvedValue([host]);
    render(<HostAdminPage />);

    expect(await screen.findByText("web-01")).toBeInTheDocument();
    expect(screen.getByText("never")).toBeInTheDocument();
    // The list carries site_id only; the name comes from the sites join.
    expect(screen.getByText("zrh1")).toBeInTheDocument();
  });

  it("creates a host and shows the token exactly once, then loses it for good", async () => {
    const user = userEvent.setup();
    createHost.mockResolvedValue({
      id: 8,
      hostname: "db-01",
      site_id: null,
      last_seen: null,
      token: "tok_abcdef",
    });
    render(<HostAdminPage />);

    await user.click(
      await screen.findByRole("button", { name: "Add the first host" }),
    );
    await user.type(screen.getByLabelText("Hostname"), "db-01");
    getHosts.mockResolvedValue([{ ...host, id: 8, hostname: "db-01" }]);
    await user.click(screen.getByRole("button", { name: "Create host" }));

    expect(await screen.findByText("tok_abcdef")).toBeInTheDocument();
    expect(createHost).toHaveBeenCalledWith("db-01", null);
    expect(screen.getByText(/shown once/i)).toBeInTheDocument();

    // Dismissing is destructive by design: the hub stores only the hash, so
    // there is nothing to re-fetch and nothing must have kept a copy.
    await user.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() =>
      expect(screen.queryByText("tok_abcdef")).not.toBeInTheDocument(),
    );
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain(
      "tok_abcdef",
    );
    expect(window.location.href).not.toContain("tok_abcdef");
  });

  it("builds the paste-ready setup line, with the hub URL left as an obvious gap", async () => {
    const user = userEvent.setup();
    createHost.mockResolvedValue({
      id: 8,
      hostname: "db-01",
      site_id: null,
      last_seen: null,
      token: "tok_abcdef",
    });
    render(<HostAdminPage />);

    await user.click(
      await screen.findByRole("button", { name: "Add the first host" }),
    );
    await user.type(screen.getByLabelText("Hostname"), "db-01");
    await user.click(screen.getByRole("button", { name: "Create host" }));

    const command = await screen.findByTestId("setup-command");
    expect(command.textContent).toBe(
      `curl -fsSL ${SETUP_SCRIPT_URL} | sh -s -- \\\n  --hub-url ${HUB_URL_PLACEHOLDER} --token tok_abcdef`,
    );

    // The placeholder is a gap the operator must close, and the page says so.
    expect(
      screen.getByText(/replace it with the address agents reach/i),
    ).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Hub URL"));
    await user.type(
      screen.getByLabelText("Hub URL"),
      "https://netra.example.org",
    );
    expect(screen.getByTestId("setup-command").textContent).toContain(
      "--hub-url https://netra.example.org --token tok_abcdef",
    );
  });

  it("rotates a token and says the old one is already dead", async () => {
    const user = userEvent.setup();
    getHosts.mockResolvedValue([host]);
    rotateHostToken.mockResolvedValue({ id: 7, token: "tok_rotated" });
    render(<HostAdminPage />);

    await user.click(
      await screen.findByRole("button", { name: "Rotate token" }),
    );

    expect(await screen.findByText("tok_rotated")).toBeInTheDocument();
    expect(rotateHostToken).toHaveBeenCalledWith(7);
    expect(
      screen.getByText(/previous token stopped working/i),
    ).toBeInTheDocument();
  });

  it("requires a second click to delete, and never deletes on the first", async () => {
    const user = userEvent.setup();
    getHosts.mockResolvedValue([host]);
    render(<HostAdminPage />);

    await user.click(await screen.findByRole("button", { name: "Delete" }));
    expect(deleteHost).not.toHaveBeenCalled();

    getHosts.mockResolvedValue([]);
    await user.click(screen.getByRole("button", { name: "Confirm delete" }));
    expect(deleteHost).toHaveBeenCalledWith(7);
    expect(await screen.findByText("No hosts yet")).toBeInTheDocument();
  });

  it("surfaces the hub's own message when a create is refused", async () => {
    const user = userEvent.setup();
    createHost.mockRejectedValue(
      new api.ApiError(409, "host already exists at that site"),
    );
    render(<HostAdminPage />);

    await user.click(
      await screen.findByRole("button", { name: "Add the first host" }),
    );
    await user.type(screen.getByLabelText("Hostname"), "web-01");
    await user.click(screen.getByRole("button", { name: "Create host" }));

    expect(
      await screen.findByText("host already exists at that site"),
    ).toBeInTheDocument();
  });

  it("reports a failed load rather than rendering the empty state as onboarding", async () => {
    getHosts.mockRejectedValue(new api.ApiError(500, "boom"));
    render(<HostAdminPage />);

    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    expect(screen.queryByText("No hosts yet")).not.toBeInTheDocument();
  });

  // Two rotations mint two tokens; the hub keeps only the newer hash and the
  // display shows whichever answers last, so the first is unusable and
  // unrecoverable and the agent holding the old one is locked out.
  it("does not mint a second token while the first rotation is in flight", async () => {
    getHosts.mockResolvedValue([host]);
    let release: (v: { id: number; token: string }) => void = () => {};
    rotateHostToken.mockImplementation(
      () => new Promise((resolve) => (release = resolve)),
    );
    render(<HostAdminPage />);
    await screen.findByText("web-01");

    const button = screen.getAllByRole("button", { name: /rotate token/i })[0]!;
    await userEvent.click(button);
    await userEvent.click(button);

    expect(rotateHostToken).toHaveBeenCalledTimes(1);
    release({ id: host.id, token: "t" });
  });

  // The browser reaches the hub on loopback, so it cannot know the name
  // agents post to; only the hub does. Without this the operator retyped
  // their hub URL by hand on every mint, and NETRA_HUB_URL was read by
  // nothing at all.
  it("seeds the setup command with the hub's configured URL", async () => {
    const user = userEvent.setup();
    vi.mocked(api.getConfig).mockResolvedValue({
      hub_url: "https://netra.example.org",
    });
    createHost.mockResolvedValue({
      id: 8,
      hostname: "db-01",
      site_id: null,
      last_seen: null,
      token: "tok_abcdef",
    });
    render(<HostAdminPage />);

    await user.click(
      await screen.findByRole("button", { name: "Add the first host" }),
    );
    await user.type(screen.getByLabelText("Hostname"), "db-01");
    await user.click(screen.getByRole("button", { name: "Create host" }));

    const command = await screen.findByTestId("setup-command");
    expect(command.textContent).toContain(
      "--hub-url https://netra.example.org",
    );
  });
});
