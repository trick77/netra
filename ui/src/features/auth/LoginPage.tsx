import { useState, type FormEvent } from "react";
import { Button } from "../../ui/Button";
import { Input } from "../../ui/Control";

export interface LoginPageProps {
  /** Called once the hub has minted a session cookie. Navigation is the
   * router's job (task 20), not this page's. */
  onSuccess?: () => void;
}

/**
 * The SPA's peer of the server-rendered login page, posting to the SAME
 * POST /login (internal/hub/httpapi/ui.go): the hub reads the token with
 * r.PostFormValue("token"), so the body is form-encoded, never JSON. Nothing
 * about session.go changes for this page to exist, and templates/login.gohtml
 * stays as the no-JS fallback.
 *
 * The token is submitted and forgotten. It is never stored: what authenticates
 * later requests is the HttpOnly session cookie the hub sets on the response,
 * which script cannot read and does not need to.
 */
export function LoginPage({ onSuccess }: LoginPageProps) {
  const [token, setToken] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function login(event: FormEvent<HTMLFormElement>) {
    // The <form> keeps its real method and action, so a JS failure that stops
    // this handler from running degrades to the plain browser post rather than
    // to a dead button.
    event.preventDefault();
    setBusy(true);
    setError(null);

    try {
      const res = await fetch("/login", {
        method: "POST",
        credentials: "same-origin",
        // URLSearchParams makes fetch set
        // Content-Type: application/x-www-form-urlencoded itself. Setting it
        // by hand would risk disagreeing with the encoding actually used.
        body: new URLSearchParams({ token }),
      });

      if (res.ok) {
        // Success is a 303 to "/" that fetch has already followed; the cookie
        // came with it. Drop the token from state either way.
        setToken("");
        onSuccess?.();
        return;
      }

      // The 401 body is the server-rendered login page. Rendering hub HTML
      // inside the SPA would be both wrong and a needless injection surface,
      // so the status alone decides the message -- and the submitted value is
      // never echoed back, matching loginSubmit's reasoning.
      setToken("");
      setError(
        res.status === 401
          ? "That is not the admin token."
          : "The hub could not process the login. Try again.",
      );
    } catch {
      // fetch only rejects on transport failure, which is a different problem
      // from a rejected token and must not read as one.
      setError("Could not reach the hub. Check that it is still running.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login">
      <h1>netra</h1>
      <p className="sub">Enter the admin token to continue.</p>

      <form
        method="post"
        action="/login"
        aria-label="Admin login"
        onSubmit={(e) => void login(e)}
      >
        {error ? (
          <p className="error" role="alert">
            {error}
          </p>
        ) : null}

        <div className="field">
          <label htmlFor="admin-token">Admin token</label>
          <Input
            id="admin-token"
            name="token"
            type="password"
            autoComplete="current-password"
            autoFocus
            required
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
        </div>

        <Button type="submit" variant="primary" busy={busy}>
          Log in
        </Button>
      </form>

      <p className="note">
        This is <code>NETRA_ADMIN_TOKEN</code> from the hub's environment, not
        an agent token. Rotating it ends every session immediately.
      </p>
    </div>
  );
}
