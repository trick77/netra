/**
 * What the agent's `containers` capability means, in the reader's terms.
 *
 * The agent reports this key only when something went wrong, and the two
 * values it can carry mean opposite things:
 *
 *   no-cgroup-scopes  the Docker socket named containers that the cgroup walk
 *                     could not find. NOTHING is collected for that host, so
 *                     the containers simply are not there to list.
 *   no-docker-socket  the reverse, and much milder. cgroup v2 still yields
 *                     CPU, memory and I/O; only the names and compose labels
 *                     are missing, so whatever IS collected is keyed by its
 *                     raw 64-hex id. It says nothing about how many
 *                     containers there are: a host with no Docker installed
 *                     has no socket either, and reports this with an empty
 *                     list. The sentence below is worded not to claim
 *                     otherwise.
 *
 * Both mirror internal/agent/collector/containers.go. The values are free
 * text on the wire -- capabilities is JSONB with no enum and no CHECK -- so an
 * unrecognised one must still render as something the operator can act on,
 * the same rule ContainerPage's NETWORK_UNAVAILABLE follows.
 *
 * The sentences live here, in one module, because the fleet's container view
 * and the host's Containers tab both say them. Written twice they would drift,
 * and the half that drifts is the remedy.
 */

/** A host as this module needs it: a name, and whatever the agent reported. */
export type CapableHost = {
  hostname: string;
  capabilities?: Record<string, string>;
};

/**
 * The fix, and the only actionable half of the whole message. `no-cgroup-scopes`
 * is a mount that setup-agent.sh knows how to add, not a bug in the agent -- so
 * the sentence names the path and the script rather than describing the
 * symptom twice.
 */
const CGROUP_REMEDY =
  "The host's cgroup v2 hierarchy is not mounted at /host/sys/fs/cgroup — re-run setup-agent.sh on it.";

/**
 * One capability value as a sentence, with `subject` supplying the grammar.
 *
 * The subject is passed in rather than derived because the fleet names hosts
 * ("The agent on web-01 and web-02") and a host page does not ("This host's
 * agent") -- the only difference between the two callers, which is why it is
 * the only parameter.
 */
function sentence(value: string, subject: string): string {
  switch (value) {
    case "no-cgroup-scopes":
      return `${subject} can see containers but no cgroup scopes for them, so no container metrics reached the hub. ${CGROUP_REMEDY}`;
    case "no-docker-socket":
      // Careful not to assert that containers exist: no Docker installed
      // means no socket, and that host reports this alongside an empty list.
      return `${subject} cannot read the Docker socket, so any containers it collects are named by their raw id rather than by compose project and service. Their CPU, memory and I/O are still measured.`;
    default:
      // A capability netra does not know the wording of is still the agent
      // saying something went wrong, and quoting it verbatim is more use than
      // silence: it is greppable in the agent's source.
      return `${subject} reported containers as “${value}”.`;
  }
}

/** "a", "a and b", "a, b and c" -- sorted, so the sentence is stable. */
function names(hostnames: readonly string[]): string {
  const sorted = [...hostnames].sort();
  if (sorted.length <= 1) return sorted[0] ?? "";
  return `${sorted.slice(0, -1).join(", ")} and ${sorted[sorted.length - 1]}`;
}

/**
 * The explanation for ONE host, for the Containers tab of its own page.
 * Null when the agent reported nothing, which is the healthy case.
 */
export function hostContainerNote(
  capabilities: Record<string, string> | undefined,
): string | null {
  const value = capabilities?.containers;
  if (value === undefined || value === "" || value === "ok") return null;
  return sentence(value, "This host's agent");
}

/**
 * The explanations for a FLEET, one sentence per distinct capability value,
 * each naming the hosts that reported it.
 *
 * `partial` is whether the list being explained has rows in it. It changes
 * only the `no-cgroup-scopes` sentence, and it has to: a fleet where every
 * host is misconfigured has an empty list, which the empty state already
 * frames as "nothing here". A fleet where one host of twelve is
 * misconfigured has a list that looks complete and is not -- and saying so is
 * the whole point, because nothing else on the page can.
 */
export function fleetContainerNotes(
  hosts: readonly CapableHost[],
  { partial }: { partial: boolean },
): string[] {
  const byValue = new Map<string, string[]>();
  for (const host of hosts) {
    const value = host.capabilities?.containers;
    if (value === undefined || value === "" || value === "ok") continue;
    const existing = byValue.get(value);
    if (existing) existing.push(host.hostname);
    else byValue.set(value, [host.hostname]);
  }

  // Sorted by value so two renders of the same fleet read the same way; the
  // hosts within each sentence are sorted by names().
  return [...byValue.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([value, hostnames]) => {
      const note = sentence(value, `The agent on ${names(hostnames)}`);
      return value === "no-cgroup-scopes" && partial
        ? `This list is incomplete. ${note}`
        : note;
    });
}
