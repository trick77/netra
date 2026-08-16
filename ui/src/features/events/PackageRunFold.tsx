// The rest of an apt run, when the hub kept only the first few.
//
// Its own file rather than an export from EventsPage, because both the fleet
// log and a host's Events tab draw it and neither owns it -- the same reason
// messageOf lives beside it in message.ts. A host tab reaching into a page
// component for a widget is the import that gets awkward later.
import type { Event } from "../../lib/api";
import { packageRunSize, packagesOmitted } from "./message";

/**
 * Renders nothing at all for an ordinary run, which is the common case: the
 * hub omits the counts entirely rather than sending zeroes, so a weekly
 * three-package upgrade is untouched.
 *
 * `.runfold` is modelled on the fleet band's `+N more` pill and deliberately
 * NOT the bare `.more` class it uses: that rule is scoped to `.attn`, and the
 * other one to `details.sysfold > summary`, so borrowing the name here would
 * have matched neither and rendered a plain link.
 *
 * The link goes to the host's package inventory rather than expanding in
 * place. A log row is a moment; "which 397?" is answered by a list of
 * packages, and that list already exists -- with a "changed recently" filter
 * on it, which is exactly the question someone clicking this is asking.
 */
export function PackageRunFold({ event }: { event: Event }) {
  const omitted = packagesOmitted(event);
  if (omitted === 0) return null;

  const total = packageRunSize(event);
  return (
    <>
      {" "}
      <a
        className="runfold"
        href={`/hosts/${event.host_id}/packages`}
        title={
          total > 0
            ? `${total} packages changed in this run`
            : "the rest of this run"
        }
      >
        +{omitted} more packages changed
      </a>
    </>
  );
}
