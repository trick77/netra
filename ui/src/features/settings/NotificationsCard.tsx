// The hub's notification channels, and a button that sends a test message
// through them.
//
// The channels are configured in the hub's environment (BACKEND_NOTIFY_URLS),
// not here: this card shows what the hub has, says plainly when it has none,
// and answers "does the mail actually arrive" without waiting for a disk to
// fill up.
import { useEffect, useState } from "react";
import { CheckCircle, WarningTriangle } from "iconoir-react";

import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";
import {
  errorText,
  getConfig,
  testNotify,
  type HubConfig,
  type NotifyDelivery,
} from "../../lib/api";

/**
 * onError receives every failed call, so the screen can send a 401 (an
 * expired session) to the login page the way every other screen does; the
 * card still says what failed.
 */
export function NotificationsCard({
  onError,
}: {
  onError?: (e: Error) => void;
}) {
  const [config, setConfig] = useState<HubConfig | null>(null);
  const [configError, setConfigError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [results, setResults] = useState<NotifyDelivery[] | null>(null);
  const [testError, setTestError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    getConfig().then(
      (c) => live && setConfig(c),
      (e: unknown) => {
        if (!live) return;
        setConfigError(errorText(e));
        if (e instanceof Error) onError?.(e);
      },
    );
    return () => {
      live = false;
    };
  }, [onError]);

  async function test() {
    setSending(true);
    setResults(null);
    setTestError(null);
    try {
      setResults(await testNotify());
    } catch (e) {
      setTestError(errorText(e));
      if (e instanceof Error) onError?.(e);
    } finally {
      setSending(false);
    }
  }

  const channels = config?.notify_channels ?? [];

  return (
    <Card title="Notifications">
      {configError !== null ? (
        <p className="note" role="alert">
          The hub's configuration could not be read: {configError}
        </p>
      ) : config === null ? (
        <p className="note">Reading the hub's configuration…</p>
      ) : channels.length === 0 ? (
        <p className="note">
          Off: no channel is configured. Set <code>BACKEND_NOTIFY_URLS</code> in
          the hub's <code>.env</code> and recreate the hub (
          <code>docker compose up -d netra</code>).
        </p>
      ) : (
        <>
          <p className="note">
            Sending to {channels.join(", ")}. A test message goes through every
            channel as it is configured; mutes do not apply to it.
          </p>
          <div className="notifytest">
            <Button busy={sending} onClick={() => void test()}>
              Send test notification
            </Button>
            {sending && (
              // The channels are sent to at once, under one limit.
              <span className="note">
                Waiting for the channels, up to {config.notify_timeout_s} s…
              </span>
            )}
          </div>
          {testError !== null && (
            <p className="note" role="alert">
              {testError}
            </p>
          )}
          {results !== null && (
            <ul className="notifyresults" aria-label="Test results">
              {results.map((r) => (
                <li key={r.channel} className={r.ok ? "ok" : "failed"}>
                  {r.ok ? (
                    <CheckCircle aria-hidden="true" />
                  ) : (
                    <WarningTriangle aria-hidden="true" />
                  )}
                  <span>
                    {r.ok
                      ? // Accepted is all the hub can know: whether it
                        // reached the inbox is the reader's to check.
                        `${r.channel}: accepted, check that it arrived`
                      : `${r.channel}: failed — ${r.error ?? "no reason given"}`}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </Card>
  );
}
