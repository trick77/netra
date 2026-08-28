import { useCallback, useEffect, useState } from "react";
import { MapPin, ServerCog } from "lucide-react";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { Input, Select } from "../../ui/Control";
import { EmptyState } from "../../ui/EmptyState";
import { Table, type Column } from "../../ui/Table";
import { relative } from "../../lib/format";
import {
  createHost,
  createSite,
  deleteHost,
  getHosts,
  getProviders,
  getSites,
  patchSite,
  rotateHostToken,
  type Host,
  type Provider,
  type Site,
  type SitePatch,
  getConfig,
} from "../../lib/api";

/**
 * Where setup-agent.sh actually lives, mirroring setupScriptURL in
 * internal/hub/httpapi/ui.go -- and deliberately NOT derived from the hub's
 * own origin. The hub serves no /setup-agent.sh: on loopback that path falls
 * through to the UI mount and 303s to the login page, which `curl -fsSL`
 * follows to a 200, so -f never trips and an HTML page is piped to sh.
 */
export const SETUP_SCRIPT_URL =
  "https://raw.githubusercontent.com/trick77/netra/master/setup-agent.sh";

/**
 * Shown in the Hub URL field's `placeholder` attribute and NOWHERE else. The
 * browser reaches this hub on loopback, so nothing this page can observe
 * reveals the address AGENTS use -- and a plausible-looking wrong value is
 * worse than an obvious gap, because the command it renders sends an agent
 * token to whoever owns that name.
 *
 * It used to seed the field as a real value, which made that gap look filled:
 * the page rendered a complete, runnable, wrong command. As a `placeholder`
 * attribute it is greyed, never submitted, and gone the moment the operator
 * types -- an example the DOM itself refuses to treat as a value.
 * BACKEND_HUB_URL lives server-side; getConfig() is the only source of a real one.
 */
export const HUB_URL_EXAMPLE = "https://netra.example.com";

/** A token that exists in this component's state and nowhere else. */
type Minted = {
  hostname: string;
  token: string;
  rotated: boolean;
};

function setupCommand(hubURL: string, token: string): string {
  // setup-agent.sh accepts --token, so the operator copies one line instead of
  // transcribing a secret into a flag by hand. Byte-identical to the command
  // token.gohtml renders, so the two UIs cannot drift apart.
  return `curl -fsSL ${SETUP_SCRIPT_URL} | sh -s -- \\\n  --hub-url ${hubURL} --token ${token}`;
}

function message(err: unknown): string {
  // ApiError.message is the hub's own `error` field -- "a host with that name
  // already exists at that site" is the operator's mistake to fix, and they
  // can only fix it if the page says which one it was.
  return err instanceof Error ? err.message : "Something went wrong.";
}

/**
 * The token panel. Shown exactly once per mint, and dismissing it is final:
 * the hub stores only a SHA-256 hash (internal/hub/admin), so there is no
 * endpoint that could hand the token back. A UI that loses it can only
 * rotate, never recover -- which is why nothing here writes it to storage,
 * the URL or a log.
 */
function TokenPanel({
  minted,
  hubURL,
  onHubURLChange,
  onDismiss,
}: {
  minted: Minted;
  hubURL: string;
  onHubURLChange: (value: string) => void;
  onDismiss: () => void;
}) {
  return (
    <Card title={minted.hostname}>
      <p className="note" role="status">
        <strong>This token is shown once.</strong> It is stored only as a
        SHA-256 hash, so it cannot be displayed again. If you lose it, rotate
        the host to mint a new one.
        {minted.rotated
          ? " The previous token stopped working the moment this one was minted."
          : ""}
      </p>

      <h4>Token</h4>
      <pre className="cmd">{minted.token}</pre>

      <h4>Install the agent</h4>
      <div className="field">
        <label htmlFor="hub-url">Hub URL</label>
        <Input
          id="hub-url"
          value={hubURL}
          placeholder={HUB_URL_EXAMPLE}
          onChange={(e) => onHubURLChange(e.target.value)}
        />
      </div>

      {hubURL === "" ? (
        // No command at all until there is a hub URL to put in it. A command
        // that is correct except for its hostname is the worst of the three
        // states: it copies, it runs, it succeeds -- and it posts that host's
        // metrics to a stranger. An empty field is a gap the operator closes;
        // a filled-in guess is one they never know is there.
        <p className="note">
          <strong>Set the hub URL to get the install command.</strong> It is the
          address agents reach this hub on, which is not the address your
          browser is using -- you are on loopback. Set{" "}
          <code>BACKEND_HOSTNAME</code> in the hub&apos;s <code>.env</code> to
          have it filled in here automatically.
        </p>
      ) : (
        <>
          <p>
            Run this on <code>{minted.hostname}</code>:
          </p>
          <pre className="cmd" data-testid="setup-command">
            {setupCommand(hubURL, minted.token)}
          </pre>
        </>
      )}

      <Button variant="primary" onClick={onDismiss}>
        Done
      </Button>
    </Card>
  );
}

/** The site fields an edit can change, held as the strings the inputs carry. */
type SiteDraft = {
  name: string;
  provider_id: string;
  facility: string;
  address: string;
  latitude: string;
  longitude: string;
  country_code: string;
  timezone: string;
};

/** The empty string for every column the site has not got. */
function draftOf(site: Site): SiteDraft {
  return {
    name: site.name,
    provider_id: site.provider_id === null ? "" : String(site.provider_id),
    facility: site.facility ?? "",
    address: site.address ?? "",
    latitude: site.latitude === null ? "" : String(site.latitude),
    longitude: site.longitude === null ? "" : String(site.longitude),
    country_code: site.country_code ?? "",
    timezone: site.timezone ?? "",
  };
}

/**
 * Builds the patch from the fields the operator actually changed, and throws
 * on a coordinate that is not a number.
 *
 * A blank field is omitted, never sent as "". PatchSite writes every field it
 * is given verbatim (internal/hub/admin/dimensions.go), so "" would store an
 * empty string over a NULL rather than clear the column -- and every
 * `?? ABSENT` reader downstream, the fleet page's site cell among them, tests
 * for null and would go on rendering a value that is no longer there. The hub
 * offers no way to clear a column at all; blanking one here is therefore a
 * no-op, which the form says out loud rather than silently pretending to have
 * done it.
 */
function sitePatchOf(site: Site, draft: SiteDraft): SitePatch {
  const patch: SitePatch = {};
  const current = draftOf(site);

  const text = (
    key: "name" | "facility" | "address" | "country_code" | "timezone",
  ) => {
    const value = draft[key].trim();
    if (value === "" || value === current[key]) return;
    patch[key] = value;
  };
  text("name");
  text("facility");
  text("address");
  text("country_code");
  text("timezone");

  const coordinate = (key: "latitude" | "longitude") => {
    const value = draft[key].trim();
    if (value === "" || value === current[key]) return;
    // Number.parseFloat("47.37N") is 47.37, so the whole string is checked
    // and not just the number it starts with: a typo that silently becomes a
    // plausible coordinate moves a marker on the map to a place nobody chose.
    //
    // Checked by shape rather than by round-tripping String(parseFloat(v)),
    // which rejects "47.370" and "8.50" -- the trailing zero being exactly
    // what a coordinate copied out of a provider's page carries.
    const label = key === "latitude" ? "Latitude" : "Longitude";
    if (!/^[+-]?\d+(\.\d+)?$/.test(value)) {
      throw new Error(`${label} must be a number.`);
    }
    const parsed = Number.parseFloat(value);
    // Shape alone catches "47.37N" and misses "473.7" -- a dropped decimal
    // point being the likelier typo, and the one that puts a marker in the
    // sea. A coordinate outside the globe is not a coordinate.
    const limit = key === "latitude" ? 90 : 180;
    if (parsed < -limit || parsed > limit) {
      throw new Error(`${label} must be between -${limit} and ${limit}.`);
    }
    // "47.370" and 47.37 are the same coordinate written two ways, so a
    // reformatted field is not a change and must not turn a no-op save into
    // a request.
    if (parsed === site[key]) return;
    patch[key] = parsed;
  };
  coordinate("latitude");
  coordinate("longitude");

  if (draft.provider_id !== "" && draft.provider_id !== current.provider_id) {
    patch.provider_id = Number(draft.provider_id);
  }

  return patch;
}

/**
 * Site administration. The hub has had POST and PATCH since the admin API
 * landed; until now the UI only read the list to fill the host form's
 * dropdown, so a new hub offered "No site" and nothing else and the only way
 * to create one was curl.
 *
 * Nothing here is required. A hub whose operator ignores this section behaves
 * exactly as it did: a host may be created with no site, and only a site's
 * name is mandatory.
 *
 * Two things the hub cannot do, so neither can this: a site cannot be deleted
 * (there is no DELETE route), and a host cannot be moved between sites once
 * created (there is no endpoint, and the unique index is on
 * (site_id, hostname)). A site created after its hosts cannot be applied to
 * them.
 */
/**
 * The site list's columns.
 *
 * Every column that holds a value sorts; Actions does not, because a button
 * has no order. Each sortValue returns null where the cell prints "none" --
 * a site with no facility is not a site whose facility is called "none", and
 * Table already puts the unknowns at one end whichever way the arrow points.
 */
function siteColumns(
  providerName: (id: number | null) => string | null,
  editing: number | null,
  startEdit: (site: Site) => void,
): Column<Site>[] {
  return [
    {
      key: "name",
      header: "Site",
      cell: (site) => site.name,
      sortValue: (site) => site.name,
    },
    {
      key: "provider",
      header: "Provider",
      cell: (site) => providerName(site.provider_id) ?? "none",
      sortValue: (site) => providerName(site.provider_id),
    },
    {
      key: "facility",
      header: "Facility",
      cell: (site) => site.facility ?? "none",
      sortValue: (site) => site.facility,
    },
    {
      key: "country",
      header: "Country",
      cell: (site) => site.country_code ?? "none",
      sortValue: (site) => site.country_code,
    },
    {
      key: "coordinates",
      header: "Coordinates",
      /* 0,0 is a real place in the Gulf of Guinea, so the test is against
         null and not against falsiness. */
      cell: (site) =>
        site.latitude === null || site.longitude === null
          ? "none"
          : `${site.latitude}, ${site.longitude}`,
      // By LATITUDE, which is the half of the pair that orders sites the way
      // an operator thinks of them -- north to south. A pair rendered as one
      // string would sort "9.1, 2.0" above "10.4, 2.0", and there is no
      // single number a coordinate honestly reduces to.
      sortValue: (site) => site.latitude,
    },
    {
      key: "actions",
      header: "Actions",
      cell: (site) => (
        <div className="toolbar">
          <Button
            small
            disabled={editing !== null && editing !== site.id}
            onClick={() => startEdit(site)}
          >
            Edit
          </Button>
        </div>
      ),
    },
  ];
}

function SitesSection({
  sites,
  providers,
  readError,
  onChanged,
}: {
  sites: Site[];
  providers: Provider[];
  /** Why the last read of the site list failed, or null. */
  readError: string | null;
  onChanged: () => Promise<void>;
}) {
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [providerId, setProviderId] = useState("");
  const [editing, setEditing] = useState<number | null>(null);
  const [draft, setDraft] = useState<SiteDraft | null>(null);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  // Provider creation has no UI either, so on a fresh hub an always-visible
  // provider picker is an empty control that reads as broken. Absent, the site
  // is simply created without one -- which is what a nullable provider_id is
  // for.
  const hasProviders = providers.length > 0;
  // A site that HAS a provider the list does not carry -- the read failed, or
  // one was created behind this page's back -- must not render as "none",
  // which is a statement that the site has no provider at all rather than
  // that this page could not name it.
  const providerName = (id: number | null) => {
    if (id === null) return null;
    return providers.find((p) => p.id === id)?.name ?? `#${id}`;
  };

  function startEdit(site: Site) {
    setFormError(null);
    setCreating(false);
    setEditing(site.id);
    setDraft(draftOf(site));
  }

  // The mirror of startEdit's setCreating(false): the two forms are
  // alternatives, and a stale error from one of them is not about the other.
  function startCreate() {
    setFormError(null);
    stopEdit();
    setCreating(true);
  }

  function stopEdit() {
    setEditing(null);
    setDraft(null);
  }

  async function onCreate() {
    setBusy(true);
    setFormError(null);
    try {
      await createSite(
        name.trim(),
        providerId === "" ? null : Number(providerId),
      );
      setCreating(false);
      setName("");
      setProviderId("");
      // Refreshes the list this section renders AND the host form's dropdown,
      // which is the same state: a site created here is selectable there
      // without a reload.
      await onChanged();
    } catch (err) {
      setFormError(message(err));
    } finally {
      setBusy(false);
    }
  }

  async function onSave(site: Site) {
    if (draft === null) return;
    setBusy(true);
    setFormError(null);
    try {
      const patch = sitePatchOf(site, draft);
      // An empty patch is not an error the hub should have to report: it
      // answers 400 "no fields to update", which is a true statement about the
      // request and a confusing one about what the operator did.
      if (Object.keys(patch).length > 0) {
        await patchSite(site.id, patch);
        await onChanged();
      }
      stopEdit();
    } catch (err) {
      setFormError(message(err));
    } finally {
      setBusy(false);
    }
  }

  const edited =
    editing === null ? undefined : sites.find((s) => s.id === editing);

  return (
    <>
      {/* A heading ROW, with the cards, the toolbar and the table as its
          siblings -- the same note as on `Hosts` below. `.section` is a
          baseline flex line, so everything nested inside it was laid out as
          another column of that row: the whole Sites UI sat beside its own
          title instead of under it. */}
      <div className="section">
        <h2>Sites</h2>
      </div>

      {readError ? (
        <p className="error" role="alert">
          {readError}
        </p>
      ) : null}
      {formError ? (
        <p className="error" role="alert">
          {formError}
        </p>
      ) : null}

      {creating ? (
        <Card title="New site">
          <div className="field">
            <label htmlFor="new-site-name">Site name</label>
            <Input
              id="new-site-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
            />
          </div>

          {hasProviders ? (
            <>
              <div className="field">
                <label htmlFor="new-site-provider">Provider</label>
                <Select
                  id="new-site-provider"
                  value={providerId}
                  onChange={(e) => setProviderId(e.target.value)}
                >
                  <option value="">No provider</option>
                  {providers.map((p) => (
                    <option key={p.id} value={String(p.id)}>
                      {p.name}
                    </option>
                  ))}
                </Select>
              </div>
            </>
          ) : null}

          <p className="note">
            A site is created with a name. Facility, address, coordinates,
            country and timezone are set by editing it afterwards.
          </p>

          <div className="toolbar">
            <Button
              variant="primary"
              busy={busy}
              disabled={name.trim() === ""}
              onClick={() => void onCreate()}
            >
              Create site
            </Button>
            <Button onClick={() => setCreating(false)}>Cancel</Button>
          </div>
        </Card>
      ) : null}

      {/* An empty list is the onboarding, and it reads as "this hub is new".
          A failed read must never stand in for it: the copy would invite a
          create that the hub then rejects as a duplicate of a site the page
          simply could not see. */}
      {sites.length === 0 && !creating && readError === null ? (
        <EmptyState
          icon={MapPin}
          title="No sites yet"
          body="A site is a location: a datacenter, a rack, a room. Hosts do not need one. It scopes hostnames, so two machines may share a name at different sites, and it carries the coordinates the map reads."
          action={
            <Button variant="primary" onClick={startCreate}>
              Add the first site
            </Button>
          }
        />
      ) : null}

      {!creating && (sites.length > 0 || readError !== null) ? (
        <div className="toolbar">
          <Button variant="primary" onClick={startCreate}>
            Add site
          </Button>
        </div>
      ) : null}

      {sites.length > 0 ? (
        // The shared Table, not a hand-rolled one. It was hand-rolled for the
        // markup, which is trivial -- but the markup was never the point: the
        // primitive is where click-to-sort lives, and a page that writes its
        // own <thead> is a page whose columns silently cannot sort. Built
        // inline rather than at module scope because the Actions cell closes
        // over `editing` and `startEdit`; the list is a handful of rows, so
        // re-deriving it per render costs nothing worth memoising.
        <Table
          columns={siteColumns(providerName, editing, startEdit)}
          rows={sites}
          rowKey={(site) => site.id}
        />
      ) : null}

      {edited !== undefined && draft !== null ? (
        <Card title={`Edit ${edited.name}`}>
          <div className="field">
            <label htmlFor="edit-site-name">Name</label>
            <Input
              id="edit-site-name"
              value={draft.name}
              onChange={(e) => setDraft({ ...draft, name: e.target.value })}
            />
          </div>

          {hasProviders ? (
            <>
              <div className="field">
                <label htmlFor="edit-site-provider">Provider</label>
                <Select
                  id="edit-site-provider"
                  value={draft.provider_id}
                  onChange={(e) =>
                    setDraft({ ...draft, provider_id: e.target.value })
                  }
                >
                  {/* Offered only while the site has no provider. The hub
                      cannot set provider_id back to NULL, so on a site that
                      has one this option is a state the control could not
                      reach: picking it would save silently and change
                      nothing. */}
                  {edited.provider_id === null ? (
                    <option value="">No provider</option>
                  ) : null}
                  {providers.map((p) => (
                    <option key={p.id} value={String(p.id)}>
                      {p.name}
                    </option>
                  ))}
                </Select>
              </div>
            </>
          ) : null}

          <div className="field">
            <label htmlFor="edit-site-facility">Facility</label>
            <Input
              id="edit-site-facility"
              value={draft.facility}
              onChange={(e) => setDraft({ ...draft, facility: e.target.value })}
            />
          </div>

          <div className="field">
            <label htmlFor="edit-site-address">Address</label>
            <Input
              id="edit-site-address"
              value={draft.address}
              onChange={(e) => setDraft({ ...draft, address: e.target.value })}
            />
          </div>

          <div className="field">
            <label htmlFor="edit-site-latitude">Latitude</label>
            <Input
              id="edit-site-latitude"
              value={draft.latitude}
              onChange={(e) => setDraft({ ...draft, latitude: e.target.value })}
            />
          </div>

          <div className="field">
            <label htmlFor="edit-site-longitude">Longitude</label>
            <Input
              id="edit-site-longitude"
              value={draft.longitude}
              onChange={(e) =>
                setDraft({ ...draft, longitude: e.target.value })
              }
            />
          </div>

          <div className="field">
            <label htmlFor="edit-site-country">Country code</label>
            <Input
              id="edit-site-country"
              value={draft.country_code}
              onChange={(e) =>
                setDraft({ ...draft, country_code: e.target.value })
              }
            />
          </div>

          <div className="field">
            <label htmlFor="edit-site-timezone">Timezone</label>
            <Input
              id="edit-site-timezone"
              value={draft.timezone}
              onChange={(e) => setDraft({ ...draft, timezone: e.target.value })}
            />
          </div>

          <p className="note">
            Only the fields you change are sent. Clearing one leaves it as it
            is, and a provider cannot be taken off a site once it has one: the
            hub can set a field but has no way to unset it.
          </p>

          <div className="toolbar">
            <Button
              variant="primary"
              busy={busy}
              disabled={draft.name.trim() === ""}
              onClick={() => void onSave(edited)}
            >
              Save site
            </Button>
            <Button onClick={stopEdit}>Cancel</Button>
          </div>
        </Card>
      ) : null}
    </>
  );
}

/**
 * Host administration: phase 1's create, rotate and delete, absorbed into the
 * SPA (spec §9). Every call rides the session cookie same-origin; no bearer
 * token is ever held by this page.
 */
/**
 * The host list's columns.
 *
 * Named `hostColumns` like the fleet's, and deliberately not shared with it:
 * that one is a monitoring row -- charts, meters, a status rail -- and this
 * one is a registry row with a token to rotate. They answer different
 * questions about the same machine.
 *
 * Bundled into one options object rather than six positional parameters,
 * which past two or three is a call nobody can read at the call site.
 */
function hostColumns({
  siteName,
  rotating,
  confirming,
  onRotate,
  onDelete,
  setConfirming,
}: {
  siteName: (id: number | null) => string | null;
  rotating: number | null;
  confirming: number | null;
  onRotate: (host: Host) => Promise<void>;
  onDelete: (host: Host) => Promise<void>;
  setConfirming: (id: number | null) => void;
}): Column<Host>[] {
  return [
    {
      key: "hostname",
      header: "Host",
      cell: (host) => host.hostname,
      sortValue: (host) => host.hostname,
    },
    {
      key: "site",
      header: "Site",
      cell: (host) => siteName(host.site_id) ?? "none",
      sortValue: (host) => siteName(host.site_id),
    },
    {
      key: "last_seen",
      header: "Last seen",
      /* A host with a token but no agent yet has never reported, which is a
         different fact from "the age is unknown" -- and on this page it is
         the expected state right after creation. */
      cell: (host) =>
        host.last_seen === null ? "never" : relative(host.last_seen),
      // The instant, not the "3 minutes ago" the cell prints. A host that has
      // never reported is the unknown Table sorts last, which is where a row
      // waiting for its agent belongs at either end of this column.
      sortValue: (host) =>
        host.last_seen === null ? null : Date.parse(host.last_seen),
    },
    {
      key: "actions",
      header: "Actions",
      cell: (host) => (
        <div className="toolbar">
          <Button
            small
            busy={rotating === host.id}
            disabled={rotating !== null}
            onClick={() => void onRotate(host)}
          >
            Rotate token
          </Button>
          {confirming === host.id ? (
            <Button small variant="danger" onClick={() => void onDelete(host)}>
              Confirm delete
            </Button>
          ) : (
            <Button small onClick={() => setConfirming(host.id)}>
              Delete
            </Button>
          )}
        </div>
      ),
    },
  ];
}

export function HostAdminPage() {
  const [hosts, setHosts] = useState<Host[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  // Why the last read of the site list failed, or null. Held apart from the
  // list itself, which is kept on a failed re-read.
  const [sitesError, setSitesError] = useState<string | null>(null);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);

  const [creating, setCreating] = useState(false);
  const [hostname, setHostname] = useState("");
  const [siteId, setSiteId] = useState("");
  const [busy, setBusy] = useState(false);
  // The host whose token is being rotated right now, or null. Per-host
  // rather than a flag, so the button that is working is the one that says
  // so.
  const [rotating, setRotating] = useState<number | null>(null);

  const [minted, setMinted] = useState<Minted | null>(null);
  // The hub's own BACKEND_HUB_URL, read once at mount and then left alone. Held
  // apart from the editable field below because dismissing a token panel has
  // to restore it: the effect that reads it runs once, so overwriting this
  // with a constant on dismiss threw the configured value away for the rest
  // of the session and made the operator retype it on every later mint.
  const [configuredHubURL, setConfiguredHubURL] = useState("");
  // What the field shows: the configured value when there is one, "" when
  // there is not. Empty renders no install command at all rather than a
  // runnable command with a stranger's hostname in it.
  const [hubURL, setHubURL] = useState("");
  // Delete is two-click rather than window.confirm: a native dialog is not
  // stylable, not testable, and easy to dismiss by reflex on the wrong row.
  const [confirming, setConfirming] = useState<number | null>(null);

  const refresh = useCallback(async () => {
    try {
      setHosts(await getHosts());
      setLoadError(null);
    } catch (err) {
      // hosts stays null so the empty state -- which is the onboarding, and
      // reads as "this hub is new" -- never stands in for a failed read.
      setLoadError(message(err));
    }
  }, []);

  // Held apart from the mount effect because the Sites section calls it after
  // every create and patch: the site list feeds BOTH that section's table and
  // the host form's dropdown, so refreshing it here is what makes a
  // just-created site selectable on a host without a reload.
  const refreshSites = useCallback(async () => {
    try {
      setSites(await getSites());
      setSitesError(null);
    } catch (err) {
      // The list already on screen is kept. A failed read costs the name
      // column, not the page -- and emptying it here would blank both the
      // sites table and the host form's dropdown the instant AFTER a write
      // succeeded, which reads as "everything I had is gone".
      //
      // Kept, but SAID: the table the operator is left looking at is the one
      // from before their write, and an empty one is not proof the hub has no
      // sites.
      setSitesError(message(err));
    }
  }, []);

  useEffect(() => {
    void refresh();
    void refreshSites();
    // Providers are read-only here -- nothing in the UI creates one -- so an
    // empty list is the ordinary case and simply hides the picker.
    getProviders()
      .then(setProviders)
      .catch(() => setProviders([]));
    // An unset BACKEND_HUB_URL comes back as "" and the field stays empty --
    // a guess would be worse than an obvious gap, because a wrong hostname
    // in that command sends an agent token to whoever owns the name.
    getConfig()
      .then(({ hub_url }) => {
        if (hub_url !== "") {
          setConfiguredHubURL(hub_url);
          setHubURL(hub_url);
        }
      })
      .catch(() => {
        // The field stays empty and says why, and it is editable -- a failed
        // config read costs the convenience, not the ability to install.
      });
  }, [refresh, refreshSites]);

  async function onCreate() {
    setBusy(true);
    setFormError(null);
    try {
      const created = await createHost(
        hostname.trim(),
        siteId === "" ? null : Number(siteId),
      );
      setMinted({
        hostname: created.hostname,
        token: created.token,
        rotated: false,
      });
      setCreating(false);
      setHostname("");
      setSiteId("");
      await refresh();
    } catch (err) {
      setFormError(message(err));
    } finally {
      setBusy(false);
    }
  }

  // Guarded against a second click while the first is in flight, and this is
  // not cosmetic: two rotations mint two tokens, the hub keeps only the
  // hash of the newer one, and the display shows whichever answers last. The
  // first token is then unusable and unrecoverable, and the agent holding
  // the pre-rotation token is locked out with nothing on screen saying why.
  async function onRotate(host: Host) {
    if (rotating !== null) return;
    setFormError(null);
    setRotating(host.id);
    try {
      const { token } = await rotateHostToken(host.id);
      setMinted({ hostname: host.hostname, token, rotated: true });
    } catch (err) {
      setFormError(message(err));
    } finally {
      setRotating(null);
    }
  }

  async function onDelete(host: Host) {
    setFormError(null);
    setConfirming(null);
    try {
      await deleteHost(host.id);
      await refresh();
    } catch (err) {
      setFormError(message(err));
    }
  }

  const siteName = (id: number | null) =>
    sites.find((s) => s.id === id)?.name ?? null;

  return (
    <>
      {/* `.section` is a heading ROW -- a baseline flex line holding the
          title and its hint (see EventsPage). Everything below is a sibling
          of it, not a child: as a child it was laid out as another column of
          that row, which put the whole page beside its own heading. */}
      <div className="section">
        <h2>Hosts</h2>
        <span className="hint">
          One agent token per host, minted when the host is created.
        </span>
      </div>

      {loadError ? (
        <p className="error" role="alert">
          {loadError}
        </p>
      ) : null}
      {formError ? (
        <p className="error" role="alert">
          {formError}
        </p>
      ) : null}

      {minted ? (
        <TokenPanel
          minted={minted}
          hubURL={hubURL}
          onHubURLChange={setHubURL}
          onDismiss={() => {
            // Dropping the state is the whole dismissal: there is no copy
            // anywhere else to clear.
            setMinted(null);
            // Back to what the hub is configured with, NOT to a constant. Any
            // edit the operator made applied to this one install command and
            // should not outlive it; the configured value should outlive
            // every one of them.
            setHubURL(configuredHubURL);
          }}
        />
      ) : null}

      {creating ? (
        <Card title="New host">
          <div className="field">
            <label htmlFor="new-hostname">Hostname</label>
            <Input
              id="new-hostname"
              value={hostname}
              onChange={(e) => setHostname(e.target.value)}
              autoFocus
            />
          </div>

          <div className="field">
            <label htmlFor="new-site">Site</label>
            <Select
              id="new-site"
              value={siteId}
              onChange={(e) => setSiteId(e.target.value)}
            >
              <option value="">No site</option>
              {sites.map((s) => (
                <option key={s.id} value={String(s.id)}>
                  {s.name}
                </option>
              ))}
            </Select>
          </div>

          <div className="toolbar">
            <Button
              variant="primary"
              busy={busy}
              disabled={hostname.trim() === ""}
              onClick={() => void onCreate()}
            >
              Create host
            </Button>
            <Button onClick={() => setCreating(false)}>Cancel</Button>
          </div>
        </Card>
      ) : null}

      {hosts !== null && hosts.length === 0 && !creating ? (
        <EmptyState
          icon={ServerCog}
          title="No hosts yet"
          body="Create a host to mint its agent token. The token is shown once, together with the command that installs the agent."
          action={
            <Button variant="primary" onClick={() => setCreating(true)}>
              Add the first host
            </Button>
          }
        />
      ) : null}

      {hosts !== null && hosts.length > 0 ? (
        <>
          {!creating ? (
            <div className="toolbar">
              <Button variant="primary" onClick={() => setCreating(true)}>
                Add host
              </Button>
            </div>
          ) : null}

          {/* The shared Table, for the reason the site list above gives:
              hand-rolling the markup also hand-rolls away the sorting. */}
          <Table
            columns={hostColumns({
              siteName,
              rotating,
              confirming,
              onRotate,
              onDelete,
              setConfirming,
            })}
            rows={hosts}
            rowKey={(host) => host.id}
          />
        </>
      ) : null}

      <SitesSection
        sites={sites}
        providers={providers}
        readError={sitesError}
        onChanged={refreshSites}
      />
    </>
  );
}
