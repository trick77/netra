import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  HostAdminPage,
  SETUP_SCRIPT_URL,
  HUB_URL_EXAMPLE,
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

  // getConfig resolves to hub_url: "" here (the beforeEach default), which is
  // an unconfigured hub. The page must render NO command rather than one that
  // is runnable and wrong: a command whose only defect is its hostname copies,
  // runs and succeeds, and posts that host's metrics to whoever owns the name.
  it("renders no setup line at all until the hub URL is known", async () => {
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

    // The token is still shown -- it is minted and unrecoverable, so it has to
    // be. It is only the install command that waits.
    expect(await screen.findByText("tok_abcdef")).toBeInTheDocument();
    expect(screen.queryByTestId("setup-command")).not.toBeInTheDocument();
    expect(
      screen.getByText(/set the hub url to get the install command/i),
    ).toBeInTheDocument();

    // The example lives in the placeholder attribute, where the DOM will not
    // submit it and it cannot end up in a copied command.
    const field = screen.getByLabelText("Hub URL");
    expect(field).toHaveValue("");
    expect(field).toHaveAttribute("placeholder", HUB_URL_EXAMPLE);

    // Closing the gap is what produces the command.
    await user.type(field, "https://netra.example.org");
    expect(await screen.findByTestId("setup-command")).toHaveTextContent(
      `--hub-url https://netra.example.org --token tok_abcdef`,
    );
    expect(screen.getByTestId("setup-command").textContent).toBe(
      `curl -fsSL ${SETUP_SCRIPT_URL} | sh -s -- \\\n  --hub-url https://netra.example.org --token tok_abcdef`,
    );
    expect(
      screen.queryByText(/set the hub url to get the install command/i),
    ).not.toBeInTheDocument();
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

    // A configured hub URL is not a gap, so the page must not call it one.
    expect(
      screen.queryByText(/set the hub url to get the install command/i),
    ).not.toBeInTheDocument();
  });

  // The config read happens once, on mount. Dismissing a panel used to reset
  // the field to a hardcoded constant, which threw the configured value away
  // for the rest of the session -- so the SECOND mint of a sitting was the one
  // that shipped a wrong hostname, and only for operators who had configured
  // the hub correctly.
  it("keeps the configured hub URL across a dismiss, and forgets per-mint edits", async () => {
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
    await screen.findByTestId("setup-command");

    // An edit that applies to this one install and should not outlive it.
    await user.clear(screen.getByLabelText("Hub URL"));
    await user.type(screen.getByLabelText("Hub URL"), "https://one-off.test");
    await user.click(screen.getByRole("button", { name: "Done" }));

    createHost.mockResolvedValue({
      id: 9,
      hostname: "db-02",
      site_id: null,
      last_seen: null,
      token: "tok_second",
    });
    // getHosts is still mocked empty, so this is the empty state's button
    // again; the regex covers both labels so the test is about the hub URL,
    // not about which onboarding copy is showing.
    await user.click(
      await screen.findByRole("button", { name: /add (the first )?host/i }),
    );
    await user.type(screen.getByLabelText("Hostname"), "db-02");
    await user.click(screen.getByRole("button", { name: "Create host" }));

    const second = await screen.findByTestId("setup-command");
    expect(second.textContent).toContain("--hub-url https://netra.example.org");
    expect(second.textContent).not.toContain("one-off.test");
  });
});
