// Every mute in force across the fleet, so a "mute until lifted" set on one
// host page months ago is not forgotten. Rendered only when there is one, or
// when the list could not be read -- an empty card would read as "nothing
// muted", which is exactly the wrong thing to say about a failed read.
import { routePath } from "../../lib/router";
import { muteScope, muteText } from "../host/MuteControl";
import { useMutes } from "../host/useMutes";
import { Button } from "../../ui/Button";
import { Card } from "../../ui/Card";

/** `version` changes when the page changed the mutes behind this list's
 * back: deleting a host deletes its mutes with it. */
export function MuteList({ version }: { version?: number }) {
  const { mutes, loadError, liftErrors, lifting, lift } = useMutes(undefined, [
    version,
  ]);

  // A lift error belongs to a listed row, so an empty list has none.
  if (mutes.length === 0 && loadError === null) {
    return null;
  }

  return (
    <Card title="Muted notifications">
      <ul className="mutelist">
        {mutes.map((m) => (
          <li key={m.id}>
            <a
              href={routePath({
                name: "host",
                hostId: String(m.host_id),
                tab: "overview",
              })}
            >
              {m.hostname}
            </a>
            <span>{muteText(m)}</span>
            <Button
              variant="ghost"
              small
              busy={lifting.has(m.id)}
              onClick={() => void lift(m.id)}
              aria-label={`Unmute ${muteScope(m.kind)} on ${m.hostname}`}
            >
              Unmute
            </Button>
          </li>
        ))}
      </ul>
      {loadError !== null && (
        <p className="note" role="alert">
          Mutes could not be loaded: {loadError}
        </p>
      )}
      {liftErrors.map((e, i) => (
        <p className="note" role="alert" key={i}>
          {e}
        </p>
      ))}
    </Card>
  );
}
