import { useCallback, useEffect, useState } from "react";
import { ServerCog } from "lucide-react";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import { Input, Select } from "../../ui/Control";
import { EmptyState } from "../../ui/EmptyState";
import { relative } from "../../lib/format";
import {
  createHost,
  deleteHost,
  getHosts,
  getSites,
  rotateHostToken,
  type Host,
  type Site,
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
 * The hub URL the command starts with. It is a placeholder, not a guess: the
 * browser reaches this hub on loopback, so nothing this page can observe
 * reveals the address AGENTS use, and a plausible-looking wrong value would
 * be worse than an obvious gap. NETRA_HUB_URL lives server-side and the read
 * API does not expose it.
 */
export const HUB_URL_PLACEHOLDER = "https://netra.example.com";

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
      <label htmlFor="hub-url">Hub URL</label>
      <Input
        id="hub-url"
        value={hubURL}
        onChange={(e) => onHubURLChange(e.target.value)}
      />
      <p className="note">
        <strong>
          <code>{HUB_URL_PLACEHOLDER}</code> is a placeholder.
        </strong>{" "}
        Replace it with the address agents reach this hub on. As written the
        command below sends this token to whoever owns that name.
      </p>

      <p>
        Run this on <code>{minted.hostname}</code>:
      </p>
      <pre className="cmd" data-testid="setup-command">
        {setupCommand(hubURL, minted.token)}
      </pre>

      <Button variant="primary" onClick={onDismiss}>
        Done
      </Button>
    </Card>
  );
}

/**
 * Host administration: phase 1's create, rotate and delete, absorbed into the
 * SPA (spec §9). Every call rides the session cookie same-origin; no bearer
 * token is ever held by this page.
 */
export function HostAdminPage() {
  const [hosts, setHosts] = useState<Host[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
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
  // Seeded from the hub's own NETRA_HUB_URL when it is set, and only from
  // the placeholder when it is not. It used to be the placeholder always, so
  // an operator who had configured their hub URL retyped it by hand on every
  // mint -- while the configured value, which the retired token.gohtml used
  // to render, was read by nothing at all.
  const [hubURL, setHubURL] = useState(HUB_URL_PLACEHOLDER);
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

  useEffect(() => {
    void refresh();
    // A missing site list costs the name column, not the page; the ids still
    // render and every action still works.
    getSites()
      .then(setSites)
      .catch(() => setSites([]));
    // An unset NETRA_HUB_URL comes back as "" and the placeholder stands --
    // a guess would be worse than an obvious gap, because a wrong hostname
    // in that command sends an agent token to whoever owns the name.
    getConfig()
      .then(({ hub_url }) => {
        if (hub_url !== "") setHubURL(hub_url);
      })
      .catch(() => {
        // The placeholder already says it is one, and the field is editable.
      });
  }, [refresh]);

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
    <div className="section">
      <h2>Hosts</h2>

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
            setHubURL(HUB_URL_PLACEHOLDER);
          }}
        />
      ) : null}

      {creating ? (
        <Card title="New host">
          <label htmlFor="new-hostname">Hostname</label>
          <Input
            id="new-hostname"
            value={hostname}
            onChange={(e) => setHostname(e.target.value)}
            autoFocus
          />

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

          <table>
            <thead>
              <tr>
                <th>Host</th>
                <th>Site</th>
                <th>Last seen</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {hosts.map((host) => (
                <tr key={host.id}>
                  <td>{host.hostname}</td>
                  <td>{siteName(host.site_id) ?? "none"}</td>
                  <td>
                    {/* A host with a token but no agent yet has never
                        reported, which is a different fact from "the age is
                        unknown" -- and on this page it is the expected state
                        right after creation. */}
                    {host.last_seen === null
                      ? "never"
                      : relative(host.last_seen)}
                  </td>
                  <td>
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
                        <Button
                          small
                          variant="danger"
                          onClick={() => void onDelete(host)}
                        >
                          Confirm delete
                        </Button>
                      ) : (
                        <Button small onClick={() => setConfirming(host.id)}>
                          Delete
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      ) : null}
    </div>
  );
}
