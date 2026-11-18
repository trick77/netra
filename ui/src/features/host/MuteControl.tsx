// Mutes for one host: what is muted now, each with a way to lift it, and a
// small form to add one. A mute silences notifications only; the page, the
// fleet and the events log keep showing everything.
import { useState } from "react";

import { createMute, type Mute } from "../../lib/api";
import { absolute } from "../../lib/format";
import { Badge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { Input, Select } from "../../ui/Control";
import { Segmented } from "../../ui/Segmented";
import {
  CONDITION_EVENT_TYPES,
  CONDITION_LABELS,
  KERNEL_EVENT_TYPES,
} from "../events/message";
import { useMutes } from "./useMutes";

type Span = "60" | "1440" | "0";

/** MaxMuteNoteBytes in internal/hub/admin/mutes.go. */
const MAX_NOTE_BYTES = 500;

const SPANS: { value: Span; label: string }[] = [
  { value: "60", label: "1 h" },
  { value: "1440", label: "24 h" },
  { value: "0", label: "Until lifted" },
];

/** The event types that can notify, so the ones a mute can name. The kernel
 * log's types plus mdraid and the agent's hub-delivery events; mirrors
 * notify.EventTypes in internal/hub/notify/kinds.go, which rejects any
 * other kind. */
export const MUTABLE_EVENT_TYPES = [
  ...KERNEL_EVENT_TYPES,
  "mdraid",
  "hub",
] as const;

/** What a mute covers, in words. */
export function muteScope(kind: string): string {
  if (kind === "") return "all notifications";
  return CONDITION_LABELS[kind] ?? kind;
}

/** "until 18 Nov 2026, 14:30:00" or "until lifted". */
export function muteUntil(m: Mute): string {
  return m.until === null ? "until lifted" : `until ${absolute(m.until)}`;
}

/** The whole sentence a mute reads as, its note included. */
export function muteText(m: Mute): string {
  const text = `${muteScope(m.kind)} ${muteUntil(m)}`;
  return m.note === "" ? text : `${text} (${m.note})`;
}

export function MuteControl({ hostId }: { hostId: number }) {
  const { mutes, loadError, liftErrors, lifting, lift, refresh } =
    useMutes(hostId);

  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState("");
  const [span, setSpan] = useState<Span>("60");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  function close() {
    setOpen(false);
    setKind("");
    setSpan("60");
    setNote("");
    setFormError(null);
  }

  async function onMute() {
    // The hub counts bytes, not characters: 300 characters of German or a
    // few emoji pass a maxLength and still come back 400.
    if (new TextEncoder().encode(note.trim()).length > MAX_NOTE_BYTES) {
      setFormError(`The note is longer than ${MAX_NOTE_BYTES} bytes.`);
      return;
    }
    setBusy(true);
    setFormError(null);
    try {
      await createMute(hostId, kind, Number(span), note.trim());
      close();
      refresh();
    } catch (e) {
      setFormError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  const errors = [
    loadError === null ? null : `Mutes could not be loaded: ${loadError}`,
    ...liftErrors,
    formError,
  ].filter((e): e is string => e !== null);

  return (
    <div className="mutes" role="group" aria-label="Notification mutes">
      {mutes.map((m) => (
        <span className="mute" key={m.id}>
          <Badge severity="neutral">Muted: {muteText(m)}</Badge>
          <Button
            variant="ghost"
            small
            busy={lifting.has(m.id)}
            onClick={() => void lift(m.id)}
            aria-label={`Unmute ${muteScope(m.kind)}`}
          >
            Unmute
          </Button>
        </span>
      ))}
      {open ? (
        <form
          className="muteform"
          onSubmit={(e) => {
            e.preventDefault();
            void onMute();
          }}
        >
          <Select
            aria-label="What to mute"
            value={kind}
            onChange={(e) => setKind(e.target.value)}
          >
            <option value="">All notifications</option>
            <optgroup label="Conditions">
              {CONDITION_EVENT_TYPES.map((k) => (
                <option key={k} value={k}>
                  {muteScope(k)}
                </option>
              ))}
            </optgroup>
            <optgroup label="Events">
              {MUTABLE_EVENT_TYPES.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </optgroup>
          </Select>
          <Segmented
            label="Mute for"
            options={SPANS}
            value={span}
            onChange={(value) => setSpan(value)}
          />
          <Input
            aria-label="Why (optional)"
            placeholder="Why (optional)"
            maxLength={MAX_NOTE_BYTES}
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
          <Button variant="primary" small type="submit" busy={busy}>
            Mute
          </Button>
          <Button variant="ghost" small onClick={close}>
            Cancel
          </Button>
        </form>
      ) : (
        <Button small onClick={() => setOpen(true)}>
          Mute notifications
        </Button>
      )}
      {errors.map((e, i) => (
        // By position: two errors can carry the same text.
        <p className="note" role="alert" key={i}>
          {e}
        </p>
      ))}
    </div>
  );
}
