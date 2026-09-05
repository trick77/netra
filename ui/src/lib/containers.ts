import type { MetricsResponse } from "./api";
import { griddedValues } from "./metrics";

/**
 * What the agent's `containers` capability means, in the reader's terms.
 *
 * The agent reports this key only when something went wrong, and the three
 * values it can carry mean different things -- two of them opposite:
 *
 *   no-cgroup-scopes  the Docker socket named containers that the cgroup walk
 *                     could not find. NOTHING is collected for that host, so
 *                     the containers simply are not there to list.
 *   no-docker-socket  the reverse, and much milder. The socket was never
 *                     MOUNTED. cgroup v2 still yields CPU, memory and I/O;
 *                     only the names and compose labels are missing, so
 *                     whatever IS collected is keyed by its raw 64-hex id. It
 *                     says nothing about how many containers there are: a host
 *                     with no Docker installed has no socket either, and
 *                     reports this with an empty list. The sentence below is
 *                     worded not to claim otherwise.
 *   docker-socket-silent
 *                     the socket IS mounted and named no containers while the
 *                     agent's cgroup walk found scopes -- dockerd down,
 *                     restarting, or refusing the agent. Blocking, like
 *                     no-cgroup-scopes: those scopes are deliberately not
 *                     reported, because keying them by raw id mints a new
 *                     container on the hub per outage that then never updates
 *                     again and sits in this list as "gone" forever.
 *
 * All three mirror internal/agent/collector/containers.go. The values are free
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
    case "docker-socket-silent":
      // Deliberately says what was NOT reported and why, then the remedy.
      // Naming these containers by their raw id is what the agent used to do
      // here, and it is what filled this list with unnamed "gone" rows.
      return `${subject} can reach the Docker socket but it named no containers, so the container cgroups it measured are not reported. Check that Docker is running on the host and that its socket is readable by the agent.`;
    default:
      // A capability netra does not know the wording of is still the agent
      // saying something went wrong, and quoting it verbatim is more use than
      // silence: it is greppable in the agent's source.
      return `${subject} reported containers as “${value}”.`;
  }
}

/**
 * Does this value mean NOTHING was collected, as opposed to collected badly?
 *
 * `no-cgroup-scopes` and `docker-socket-silent` do. The distinction decides
 * whether an empty list is a fault or a fact: a fleet of hosts with no Docker
 * installed reports `no-docker-socket` and an empty list, and it is perfectly
 * healthy -- calling that "no containers collected" turns a fleet that simply
 * runs none into a problem, and drops the one true sentence available about it.
 *
 * `docker-socket-silent` is the opposite: the agent MEASURED container cgroups
 * and reported none of them, so the empty list is a fault and every panel over
 * it has to take its not-collected state rather than draw a host at rest.
 */
function blocking(value: string): boolean {
  return value === "no-cgroup-scopes" || value === "docker-socket-silent";
}

/**
 * Whether THIS host's containers are missing outright, not merely unnamed.
 *
 * The single-host form of fleetContainersBlocked, for the Docker panels above
 * the Containers list: they take the not-collected state only here. A NAS with
 * no Docker installed reports `no-docker-socket` and an empty list and is
 * perfectly well -- two panels over it saying nothing reached the hub would be
 * an alarm about a machine doing exactly what it is meant to.
 */
export function hostContainersBlocked(
  capabilities: Record<string, string> | undefined,
): boolean {
  const value = capabilities?.containers;
  return value !== undefined && blocking(value);
}

/** Whether any host's containers are missing outright, not merely unnamed. */
export function fleetContainersBlocked(hosts: readonly CapableHost[]): boolean {
  return hosts.some((host) => {
    const value = host.capabilities?.containers;
    return value !== undefined && blocking(value);
  });
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
 * only the BLOCKING sentences, and it has to: a fleet where every host is
 * misconfigured has an empty list, which the empty state already frames as
 * "nothing here". A fleet where one host of twelve is misconfigured has a list
 * that looks complete and is not -- and saying so is the whole point, because
 * nothing else on the page can.
 *
 * Keyed off blocking() rather than off `no-cgroup-scopes` by name, so a value
 * added to that set inherits the prefix instead of quietly going without it.
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
      return blocking(value) && partial
        ? `This list is incomplete. ${note}`
        : note;
    });
}

// --- Series -----------------------------------------------------------------

/**
 * Per-container CPU and memory over the same window, keyed by container_key.
 *
 * The container lists were the one place in the app showing a fleet of
 * things over time with no time in them: names, images and a host, and
 * nothing about what any of them was doing. family=container carries
 * cpu_pct, mem_used and mem_limit per container, so the rows can say it.
 */
export interface ContainerTrend {
  cpu: (number | null)[];
  mem: (number | null)[];
  /** The container's own ceiling, or null when it runs unlimited. */
  memLimit: number | null;
}

/**
 * Every container's series in a family=container response, keyed by
 * container_key.
 *
 * Shared with the host page's inventory list, with the enlarged view a reader
 * opens off either list's CPU or Memory cell, and with the two stacked Docker
 * panels above the host's Containers list -- so all of them read the same
 * columns out of the same response shape.
 *
 * It lived in features/fleet/hostTrends.ts, which is where the fleet list
 * first needed it. lib/bands.ts builds the stacked Docker panels from it now,
 * and lib importing from features is the wrong direction: this module already
 * exists for what more than one container view has to agree on.
 */
export function containerTrends(
  res: MetricsResponse | null,
): Map<string, ContainerTrend> {
  const trends = new Map<string, ContainerTrend>();
  if (res === null) return trends;

  res.series.forEach((series, index) => {
    // The keySpec's NAME is "container" (internal/hub/read/family.go), not
    // the SQL expression behind it. Reading series[0] instead would chart a
    // neighbouring container under this one's name.
    const key = series.key.container;
    if (key === undefined) return;
    trends.set(key, {
      cpu: griddedValues(res, index, "cpu_pct"),
      mem: griddedValues(res, index, "mem_used"),
      memLimit: lastNumber(griddedValues(res, index, "mem_limit")),
    });
  });
  return trends;
}

/** The latest non-null value, or null when the series never reported.
 *
 * NOT lib/metrics.ts's latestValue(), which is the LATEST BUCKET including a
 * trailing null. The two answer different questions and only this one is
 * right for mem_limit: a configured ceiling does not stop being the ceiling
 * because the newest bucket has not materialised yet. Kept private here, as
 * latestValue's own note asks of every caller needing the other rule.
 */
function lastNumber(values: readonly (number | null)[]): number | null {
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i];
    if (v !== null && v !== undefined) return v;
  }
  return null;
}
