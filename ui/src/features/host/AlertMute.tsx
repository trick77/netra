// Mute on the alert itself: the line that says what is wrong carries the way
// to stop being told about it, and says so once it is muted. A mute silences
// notifications only; the line, the fleet and the events log stay.
import { useState } from "react";

import { createMute, errorText } from "../../lib/api";
import { Button } from "../../ui/Button";
import { Segmented } from "../../ui/Segmented";
import { coveringMute, muteScope, muteUntil, type MuteTarget } from "./mutes";
import type { Mutes } from "./useMutes";

type Span = "resolved" | "60" | "1440" | "0";

const TIMED: { value: Span; label: string }[] = [
  { value: "60", label: "1 h" },
  { value: "1440", label: "24 h" },
  { value: "0", label: "Permanently" },
];

export function AlertMute({
  hostId,
  target,
  mutes,
}: {
  hostId: number;
  target: MuteTarget;
  mutes: Mutes;
}) {
  const spans: { value: Span; label: string }[] = target.open
    ? [{ value: "resolved", label: "Until resolved" }, ...TIMED]
    : TIMED;
  const first: Span = target.open ? "resolved" : "60";

  const [open, setOpen] = useState(false);
  const [span, setSpan] = useState<Span>(first);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const scope = muteScope(target.kind, target.subject);
  const muted = coveringMute(mutes.mutes, hostId, target);

  if (muted !== undefined) {
    // A mute set on this very alert says only until when; a wider one names
    // what it covers, so a host-wide mute is not mistaken for this line's.
    const wide = muted.kind === "" || muted.subject === null;
    return (
      <span className="alertmute">
        <span className="state">
          muted{" "}
          {wide
            ? `with ${muteScope(muted.kind, muted.subject)} ${muteUntil(muted)}`
            : muteUntil(muted)}
        </span>
        <Button
          variant="ghost"
          small
          busy={mutes.lifting.has(muted.id)}
          onClick={() => void mutes.lift(muted.id)}
          aria-label={`Unmute ${muteScope(muted.kind, muted.subject)}`}
        >
          Unmute
        </Button>
      </span>
    );
  }

  function close() {
    setOpen(false);
    setSpan(first);
    setError(null);
  }

  async function onMute() {
    setBusy(true);
    setError(null);
    try {
      const created = await createMute({
        host_id: hostId,
        kind: target.kind,
        subject: target.subject,
        until_resolved: span === "resolved",
        minutes: span === "resolved" ? 0 : Number(span),
      });
      mutes.add(created);
      close();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  if (!open) {
    return (
      <span className="alertmute">
        <Button
          variant="ghost"
          small
          onClick={() => setOpen(true)}
          aria-label={`Mute ${scope}`}
        >
          Mute
        </Button>
      </span>
    );
  }

  return (
    <form
      className="alertmute open"
      aria-label={`Mute ${scope}`}
      onSubmit={(e) => {
        e.preventDefault();
        void onMute();
      }}
    >
      <Segmented
        label="Mute for"
        options={spans}
        value={span}
        onChange={(value) => setSpan(value)}
      />
      <Button variant="primary" small type="submit" busy={busy}>
        Mute
      </Button>
      <Button variant="ghost" small onClick={close}>
        Cancel
      </Button>
      {target.reach !== undefined && (
        <span className="reach">{target.reach}</span>
      )}
      {error !== null && (
        <p className="note" role="alert">
          {error}
        </p>
      )}
    </form>
  );
}
