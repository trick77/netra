import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
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
    getProviders: vi.fn(),
    createSite: vi.fn(),
    patchSite: vi.fn(),
  };
});

const getHosts = vi.mocked(api.getHosts);
const getSites = vi.mocked(api.getSites);
const createHost = vi.mocked(api.createHost);
const rotateHostToken = vi.mocked(api.rotateHostToken);
const deleteHost = vi.mocked(api.deleteHost);
const getProviders = vi.mocked(api.getProviders);
const createSite = vi.mocked(api.createSite);
const patchSite = vi.mocked(api.patchSite);

const host: api.Host = {
  id: 7,
  hostname: "web-01",
  site_id: 1,
  last_seen: null,
  cpu_total: null,
  mem_used: null,
  mem_total: null,
  uptime_s: null,
  net_rx_bytes: null,
  net_tx_bytes: null,
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
  getProviders.mockResolvedValue([]);
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
    // Scoped to the host's own row: the Sites section below lists the same
    // name, so an unscoped query matches in two places.
    const row = screen.getByRole("row", { name: /web-01/ });
    expect(within(row).getByText("zrh1")).toBeInTheDocument();
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
  // their hub URL by hand on every mint, and BACKEND_HUB_URL was read by
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

describe("SitesSection", () => {
  it("creates a site with a null provider when the hub has none", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([]);
    createSite.mockResolvedValue({ ...site, id: 2, name: "fsn1" });
    render(<HostAdminPage />);

    await user.click(
      await screen.findByRole("button", { name: "Add the first site" }),
    );
    await user.type(screen.getByLabelText("Site name"), "fsn1");
    await user.click(screen.getByRole("button", { name: "Create site" }));

    await waitFor(() => expect(createSite).toHaveBeenCalledWith("fsn1", null));
  });

  // Provider creation has no UI either, so a picker with nothing in it is a
  // control that reads as broken on every fresh hub.
  it("hides the provider select when the hub has no providers", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([]);
    render(<HostAdminPage />);

    await user.click(
      await screen.findByRole("button", { name: "Add the first site" }),
    );

    expect(screen.getByLabelText("Site name")).toBeInTheDocument();
    expect(screen.queryByLabelText("Provider")).not.toBeInTheDocument();
  });

  it("offers the provider select once there is a provider to pick", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([]);
    getProviders.mockResolvedValue([{ id: 3, name: "Hetzner" }]);
    createSite.mockResolvedValue({ ...site, id: 2, name: "fsn1" });
    render(<HostAdminPage />);

    await user.click(
      await screen.findByRole("button", { name: "Add the first site" }),
    );
    await user.type(screen.getByLabelText("Site name"), "fsn1");
    await user.selectOptions(
      await screen.findByLabelText("Provider"),
      "Hetzner",
    );
    await user.click(screen.getByRole("button", { name: "Create site" }));

    await waitFor(() => expect(createSite).toHaveBeenCalledWith("fsn1", 3));
  });

  // The unique index is on (provider_id, name). The hub's own message names
  // the conflict, and the operator can only fix what the page tells them.
  it("surfaces a duplicate-name conflict from the hub", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([]);
    createSite.mockRejectedValue(
      new api.ApiError(409, "site already exists for that provider"),
    );
    render(<HostAdminPage />);

    await user.click(
      await screen.findByRole("button", { name: "Add the first site" }),
    );
    await user.type(screen.getByLabelText("Site name"), "zrh1");
    await user.click(screen.getByRole("button", { name: "Create site" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "site already exists for that provider",
    );
  });

  // The site list feeds both the sites table and the host form's dropdown. A
  // create that did not refresh it left the operator creating a site they
  // then could not select without reloading the page.
  it("offers a newly created site on the host form without a reload", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([]);
    createSite.mockResolvedValue({ ...site, id: 2, name: "fsn1" });
    render(<HostAdminPage />);

    await user.click(
      await screen.findByRole("button", { name: "Add the first site" }),
    );
    await user.type(screen.getByLabelText("Site name"), "fsn1");
    getSites.mockResolvedValue([{ ...site, id: 2, name: "fsn1" }]);
    await user.click(screen.getByRole("button", { name: "Create site" }));

    await user.click(
      await screen.findByRole("button", { name: "Add the first host" }),
    );
    const select = screen.getByLabelText("Site") as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual([
      "No site",
      "fsn1",
    ]);
  });

  // PatchSite writes whatever it is given, so "" would store an empty string
  // over a NULL rather than clear the column -- and every reader downstream
  // tests for null. Blanking a field must therefore send nothing at all.
  it("patches only the fields that changed, and omits one the operator blanked", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([
      { ...site, facility: "DC15", country_code: "CH" },
    ]);
    patchSite.mockResolvedValue(undefined);
    render(<HostAdminPage />);

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.type(screen.getByLabelText("Latitude"), "47.37");
    await user.clear(screen.getByLabelText("Facility"));
    await user.click(screen.getByRole("button", { name: "Save site" }));

    await waitFor(() =>
      expect(patchSite).toHaveBeenCalledWith(site.id, { latitude: 47.37 }),
    );
  });

  // Number.parseFloat("47.37N") is 47.37: a typo that silently becomes a
  // plausible coordinate puts a marker somewhere nobody chose.
  it("refuses a coordinate that is not a number, and sends nothing", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([site]);
    render(<HostAdminPage />);

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.type(screen.getByLabelText("Latitude"), "47.37N");
    await user.click(screen.getByRole("button", { name: "Save site" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Latitude must be a number.",
    );
    expect(patchSite).not.toHaveBeenCalled();
  });

  // A trailing zero is what a coordinate copied out of a provider's
  // datacenter page carries, and String(Number.parseFloat("47.370")) is
  // "47.37" -- so a validator that round-trips the string rejects a
  // perfectly good coordinate.
  it("accepts a coordinate written with trailing zeros", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([site]);
    patchSite.mockResolvedValue(undefined);
    render(<HostAdminPage />);

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.type(screen.getByLabelText("Latitude"), "47.370");
    await user.type(screen.getByLabelText("Longitude"), "-8.50");
    await user.click(screen.getByRole("button", { name: "Save site" }));

    await waitFor(() =>
      expect(patchSite).toHaveBeenCalledWith(site.id, {
        latitude: 47.37,
        longitude: -8.5,
      }),
    );
  });

  // The list feeds the sites table AND the host form's dropdown. Emptying it
  // on a failed re-read the instant after a write succeeded reads as
  // "everything I had is gone".
  it("keeps the sites it has when the refresh after a write fails", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([site]);
    patchSite.mockResolvedValue(undefined);
    render(<HostAdminPage />);

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.type(screen.getByLabelText("Facility"), "DC15");
    getSites.mockRejectedValue(new api.ApiError(503, "unavailable"));
    await user.click(screen.getByRole("button", { name: "Save site" }));

    await waitFor(() => expect(patchSite).toHaveBeenCalled());
    expect(screen.getByText("zrh1")).toBeInTheDocument();
  });

  // The hub answers 400 "no fields to update" on an empty patch, which is a
  // true statement about the request and a confusing one about what the
  // operator did.
  it("sends no request at all when nothing was changed", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([site]);
    render(<HostAdminPage />);

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Save site" }));

    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "Save site" }),
      ).not.toBeInTheDocument(),
    );
    expect(patchSite).not.toHaveBeenCalled();
  });

  // An empty list is the onboarding copy, and it reads as "this hub is new".
  // A failed read standing in for it invites a create the hub then rejects as
  // a duplicate of a site the page never saw.
  it("says the site list failed to load instead of showing the empty state", async () => {
    getSites.mockRejectedValue(new api.ApiError(503, "database unavailable"));
    render(<HostAdminPage />);

    expect(await screen.findByText("database unavailable")).toBeInTheDocument();
    expect(screen.queryByText("No sites yet")).not.toBeInTheDocument();
  });

  // The shape check catches "47.37N" and misses "473.7" -- a dropped decimal
  // point being the likelier typo, and the one that lands in the sea.
  it("refuses a coordinate that is off the globe", async () => {
    const user = userEvent.setup();
    getSites.mockResolvedValue([site]);
    render(<HostAdminPage />);

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.type(screen.getByLabelText("Latitude"), "473.7");
    await user.click(screen.getByRole("button", { name: "Save site" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Latitude must be between -90 and 90.",
    );
    expect(patchSite).not.toHaveBeenCalled();
  });

  // A site with no coordinates renders "none"; 0,0 is a real place in the
  // Gulf of Guinea and must render as a coordinate, not as an absence.
  it("treats 0,0 as a location rather than as no location", async () => {
    getSites.mockResolvedValue([{ ...site, latitude: 0, longitude: 0 }]);
    render(<HostAdminPage />);

    expect(await screen.findByText("0, 0")).toBeInTheDocument();
  });
});

// `.section` is a baseline flex ROW (index.css), so anything nested inside it
// is laid out as another column beside the heading rather than under it. That
// is how the whole Sites UI -- cards, toolbar and table -- came to render
// sideways, long after the identical defect was fixed on `Hosts` two functions
// away in the same file, with no test to notice the one that was missed.
//
// jsdom computes no flex layout, so this pins the NESTING that causes it: a
// heading row holds a heading and at most its hint, and the section body is a
// sibling. Every `.section` on the page is checked, because being forgotten is
// the failure mode.
describe("heading rows", () => {
  it("keeps every .section a heading row, with the section body as its sibling", async () => {
    getHosts.mockResolvedValue([host]);
    const { container } = render(<HostAdminPage />);

    // Rendered, and not empty: without this the loop below would pass on a
    // page that has drawn nothing yet, which is no assertion at all.
    // Twice: the host row names its site, and the Sites table lists it again.
    expect(await screen.findAllByText("zrh1")).toHaveLength(2);
    expect(container.querySelectorAll("table")).toHaveLength(2);

    const sections = [...container.querySelectorAll(".section")];
    expect(sections).toHaveLength(2);

    for (const section of sections) {
      // Keyed by the heading text rather than asserted bare: a failure has to
      // say WHICH section leaked, and what leaked into it.
      const heading = section.querySelector("h2")?.textContent;
      const strays = [...section.children]
        .map((el) => el.tagName)
        .filter((tag) => tag !== "H2" && tag !== "SPAN");

      expect({ heading, strays }).toEqual({ heading, strays: [] });
      expect({
        heading,
        body: section.querySelector("table, .card, .toolbar"),
      }).toEqual({ heading, body: null });
    }
  });
});
