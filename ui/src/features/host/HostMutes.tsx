// The host's mutes in force, each with a way to lift it. Set on the alert
// lines below, listed here as well: a permanent mute on an alert that is not
// open right now (a NAS switched off most days) has no line to show it on,
// and a mute nobody can see is how one stays forgotten.
import { Badge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { muteScope, muteText } from "./mutes";
import type { Mutes } from "./useMutes";

export function HostMutes({ mutes }: { mutes: Mutes }) {
  const { loadError, liftErrors, lifting, lift } = mutes;
  const errors = [
    loadError === null ? null : `Mutes could not be loaded: ${loadError}`,
    ...liftErrors,
  ].filter((e): e is string => e !== null);
  if (mutes.mutes.length === 0 && errors.length === 0) return null;

  return (
    <div className="mutes" role="group" aria-label="Notification mutes">
      {mutes.mutes.map((m) => (
        <span className="mute" key={m.id}>
          <Badge severity="neutral">Muted: {muteText(m)}</Badge>
          <Button
            variant="ghost"
            small
            busy={lifting.has(m.id)}
            onClick={() => void lift(m.id)}
            aria-label={`Unmute ${muteScope(m.kind, m.subject)}`}
          >
            Unmute
          </Button>
        </span>
      ))}
      {errors.map((e, i) => (
        // By position: two errors can carry the same text.
        <p className="note" role="alert" key={i}>
          {e}
        </p>
      ))}
    </div>
  );
}
