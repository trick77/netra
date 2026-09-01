// Typed client for the hub's read API (internal/hub/httpapi/read.go).
//
// The SPA is served same-origin by the hub, so `credentials: "same-origin"`
// on every request is sufficient for RequireAdmin: it accepts either an
// Authorization: Bearer header or the session cookie minted from the same
// admin token, and the cookie already rides same-origin requests. Nothing
// here ever stores or sends a bearer token from browser JavaScript.

/**
 * ApiError carries the HTTP status as a field so a 401 is distinguishable
 * from a 500 by type, not by parsing a message. A later task routes 401 to
 * the login page.
 */
export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

// --- Response types, transcribed from internal/hub/read/*.go JSON tags ---

// internal/hub/read/host.go: HostSummary
export type Host = {
  id: number;
  hostname: string;
  last_seen: string | null;
  cpu_total: number | null;
  mem_used: number | null;
  mem_total: number | null;
  uptime_s: number | null;
  // Traffic summed over the host's interfaces at its last scrape, in bytes
  // per second. A gauge, not the end of a series: read off a fetched series
  // this number changed with the RANGE the charts were drawn over, because
  // the range picks the step and the step picks the storage tier -- an
  // instantaneous rate at 1h, a five-minute average that ended a quarter of
  // an hour ago at 6h and 24h. What "now" means must not depend on how far
  // back somebody is looking.
  //
  // rx/tx here because that is what the column and /proc/net/dev call them.
  // Ingress and egress are what the labels say.
  net_rx_bytes: number | null;
  net_tx_bytes: number | null;
  // The host's systemd service counts as of its last scrape.
  //
  // Gauges rather than a count of the /units response, which is what the host
  // page used to do. That endpoint returns only the units that NEED ATTENTION
  // now, so counting it reports "0 units" for a healthy host running several
  // hundred of them. Null on a host with no systemd -- a different fact from
  // zero, which capabilities explains.
  //
  // Optional here, unlike the Go fields, for the reason `capabilities` below
  // is: every reader already handles the absent case, and the hand-built host
  // literals across the tests predate these fields.
  services_total?: number | null;
  services_failed?: number | null;
  // Names for up to three of the units behind services_failed, alphabetically
  // -- an annotation on that count, never a substitute for it. They come from
  // a different table than the count does (see read.HostSummary.FailedUnits),
  // so an empty list means the hub cannot name them, not that none failed.
  //
  // Optional for the same reason capabilities below is: the hand-built host
  // literals across the tests predate the field.
  failed_units?: string[];
  // When the OLDEST of those units entered its failed state -- systemd's own
  // timestamp, not when the hub heard about it. One timestamp for the whole
  // set: the fleet list states this as one condition per host, and a reader
  // asking how long it has been broken means the first of them.
  //
  // Null is real and common: state_ts is nullable, and a host with a count
  // and no unit rows yet has nothing to date. The list leaves its Since
  // column empty rather than substituting a plausible instant.
  failed_since?: string | null;
  // Inventory rather than a gauge, and on the list because the CPU sparkline
  // is a per-core stack: the page has to know how many logical CPUs a host
  // has before deciding to ask for one series per core.
  threads: number | null;
  // The distribution, for the mark and the words the fleet row labels a host
  // with. Optional here and NOT on the Go struct, the same convention
  // capabilities below follows: the hand-built host literals across the tests
  // predate the field, and a row that does not carry it simply draws no mark.
  os_name?: string | null;
  // What each collector reported about its own availability, verbatim. On the
  // LIST because the absences it explains are fleet-wide: a host reporting
  // `containers: no-cgroup-scopes` contributes no containers at all, so the
  // fleet's container list is short by a whole host and only this endpoint
  // can say why.
  //
  // Optional here, unlike the Go field, which is NOT NULL DEFAULT '{}'. Every
  // reader chains through it anyway, and the hand-built host literals in the
  // tests predate the field -- requiring it would mean touching each of them
  // to add {} without a single assertion changing.
  capabilities?: Record<string, string>;
  /**
   * Where the host is, verbatim as its own agent reports it -- the
   * AGENT_LOCATION, AGENT_PROVIDER and AGENT_FACILITY variables set wherever
   * the agent runs.
   *
   * On the LIST for the same reason capabilities is: it is what the fleet
   * draws under every hostname, and resolving it any other way meant fetching
   * the sites and providers tables whole and joining them by id on the client.
   *
   * Free text. `location` is "Roubaix, France" -- a place as a person writes
   * it, not a city and a country code to be recombined -- so nothing here is
   * parsed and nothing is looked up. Null is "the agent did not report one",
   * which is every host whose operator set none of the variables.
   *
   * Optional, like capabilities and for the same reason: the hand-built host
   * literals across the tests predate the fields.
   */
  location?: string | null;
  provider?: string | null;
  facility?: string | null;
};

// internal/hub/read/host.go: HostDetail (embeds HostSummary)
export type HostDetail = Host & {
  fingerprint: string | null;
  host_type: string | null;
  agent_version: string | null;
  go_version: string | null;
  build_commit: string | null;
  kernel: string | null;
  os_name: string | null;
  arch: string | null;
  cpu_model: string | null;
  cores: number | null;
  threads: number | null;
  memory_total: number | null;

  latitude: number | null;
  longitude: number | null;
  created_at: string;

  // Restated as required rather than inherited optional: the detail endpoint
  // always sends it, and the host page reads it without chaining. The list's
  // copy is the same field -- Go declares it once, on HostSummary.
  capabilities: Record<string, string>;
};

// internal/hub/read/inventory.go: Container
export type Container = {
  id: number;
  container_key: string;
  name: string | null;
  image: string | null;
  is_agent: boolean;
  /** When this container's newest sample was taken, from the agent's clock.
   * The agent reports what is RUNNING, so a container that was removed --
   * or stopped -- simply stops appearing, and this is the only thing that
   * says so. Never null: the hub stamps it on the first upsert. */
  last_seen: string;
};

// internal/hub/read/inventory.go: Filesystem
export type Filesystem = {
  id: number;
  label: string;
  mountpoint: string | null;
  device_id: number | null;
};

// internal/hub/read/inventory.go: Address
export type Address = {
  iface: string;
  if_index: number | null;
  address: string;
  family: number;
  scope: string | null;
  vrf: string | null;
  description: string | null;
  first_seen: string;
  last_seen: string;
};

// internal/hub/read/inventory.go: Interface
//
// Named Iface rather than Interface: `interface` is a TypeScript keyword and
// a type called Interface reads as one in every import that mentions it.
export type Iface = {
  iface: string;
  if_index: number | null;
  /** The kernel's operstate verbatim -- up, down, unknown, lowerlayerdown --
   * not a bool. See HostInterface in the proto for why the agent does not
   * classify it. */
  oper_state: string | null;
  /** null, not 0, wherever the kernel has no answer: a virtual device has no
   * link speed and a down one refuses to report its. */
  speed_mbps: number | null;
  duplex: string | null;
  mtu: number | null;
  mac: string | null;
  description: string | null;
  first_seen: string;
  last_seen: string;
};

// internal/hub/read/inventory.go: Drive and DriveAttribute
//
// The attribute set is untyped on purpose, as the schema stores it: SMART
// attributes vary per drive model, so a typed field per attribute would need a
// schema change for every new drive. What an id MEANS is named in
// features/host/smart.ts, on this side, for the same reason the fleet's
// severity rules live here rather than in the hub.
export type DriveAttribute = {
  id: number;
  raw: number | null;
  /** ATA's vendor-scaled 1-253 health figure, where higher is better. Always
   * null on an NVMe row: the health log has no such scale, and the collector
   * declines to invent one. */
  normalized: number | null;
};

export type Drive = {
  device: string;
  model: string | null;
  serial: string | null;
  attributes: DriveAttribute[];
  /** When this drive's newest reading was TAKEN -- devices.last_seen. The
   * same instant the newest attribute carries, but stored rather than
   * derived, so a drive whose readings have aged out under retention still
   * has a date on it.
   *
   * devices.first_seen is not sent: it is a hub timestamp the prune uses as a
   * floor, and the two clocks side by side could read first_seen > last_seen
   * after a replayed batch. */
  last_seen: string;
};

// internal/hub/read/inventory.go: Package
export type Pkg = {
  name: string;
  version: string;
  arch: string;
  format: string;
  size_bytes: number | null;
  first_seen: string;
  last_seen: string;
  /** When the version last moved, as opposed to when the row was first
   * written or last re-emitted. */
  version_changed_at: string;
};

// internal/hub/read/inventory.go: Unit
//
// NOT an inventory: /units returns only the units that NEED ATTENTION, so a
// unit missing from the list is a unit that is fine, never a unit the host
// does not have. For "how many services does this host run", read
// Host.services_total. See internal/hub/systemdstate for the rule.
export type Unit = {
  id: number;
  unit_name: string;
  state: string | null;
  substate: string | null;
  // When the unit ENTERED this state, not when the hub last heard about it:
  // both write paths advance it only on an actual change. That is what makes
  // "failed since 03:12" mean the failure started then, rather than meaning
  // the last snapshot happened to land then.
  since: string | null;
  // State changes recorded for this unit in the last hour.
  //
  // The only thing that reveals a unit which is broken without ever LOOKING
  // broken: a service that runs a few minutes, dies and comes back is healthy
  // at nearly every scrape, and systemd never escalates it to `failed` because
  // it does not trip the start limit. See flapping() in Overview.tsx.
  restarts_1h: number;
};

// internal/hub/read/events.go: Event
export type Event = {
  // A string, not a number: the log unions three tables with three different
  // keys, and the hub prefixes each ("e:", "p:", "u:") rather than inventing
  // integers that could collide across them.
  id: string;
  host_id: number;
  hostname: string;
  ts: string;
  type: string;
  subject: string | null;
  // detail is passed through as stored; its shape is the emitting
  // collector's, not this API's.
  detail: unknown;
};

// internal/hub/read/tier.go: Window
export type MetricsWindow = {
  from: string;
  to: string;
};

// internal/hub/read/metrics.go: Series
export type MetricsSeries = {
  key: Record<string, string>;
  points: unknown[][];
};

// internal/hub/read/metrics.go: Result
//
// Not parsed, reshaped or normalised here -- this type just describes what
// the hub sent. Interpreting tiers and columns is lib/metrics.ts's job.
export type MetricsResponse = {
  family: string;
  tier: string;
  step_s: number;
  window: MetricsWindow;
  requested_window: MetricsWindow;
  warnings: string[];
  key_columns: string[];
  columns: string[];
  series: MetricsSeries[];
  truncated: boolean;
};

// --- Request helper ---

async function request<T>(
  path: string,
  init?: { method: string; body?: unknown },
): Promise<T> {
  const res = await fetch(path, {
    credentials: "same-origin",
    method: init?.method,
    headers:
      init?.body === undefined
        ? { Accept: "application/json" }
        : { Accept: "application/json", "Content-Type": "application/json" },
    body: init?.body === undefined ? undefined : JSON.stringify(init.body),
  });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: string };
      if (body?.error) message = body.error;
    } catch {
      // Body wasn't JSON, or was empty. The status code still reaches the
      // caller either way.
    }
    throw new ApiError(res.status, message);
  }
  // A 204 carries no body at all, and res.json() on an empty body throws a
  // SyntaxError that would surface as a failed delete even though the delete
  // succeeded. DELETE /api/v1/hosts/{id} is the case.
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// Builds a query string from params whose values may be undefined (dropped),
// an array (repeated, matching splitColumns' ?columns=a&columns=b form), or
// a scalar (stringified). Nothing here validates or reformats a value --
// the query is passed through to the hub verbatim.
function toQueryString(params: Record<string, unknown>): string {
  const usp = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null) continue;
    if (Array.isArray(value)) {
      for (const v of value) usp.append(key, String(v));
    } else {
      usp.append(key, String(value));
    }
  }
  const qs = usp.toString();
  return qs ? `?${qs}` : "";
}

// --- Endpoints ---

export function getHosts(): Promise<Host[]> {
  return request<Host[]>("/api/v1/hosts");
}

export function getHost(id: number | string): Promise<HostDetail> {
  return request<HostDetail>(`/api/v1/hosts/${id}`);
}

export function getContainers(id: number | string): Promise<Container[]> {
  return request<Container[]>(`/api/v1/hosts/${id}/containers`);
}

export function getFilesystems(id: number | string): Promise<Filesystem[]> {
  return request<Filesystem[]>(`/api/v1/hosts/${id}/filesystems`);
}

export function getAddresses(id: number | string): Promise<Address[]> {
  return request<Address[]>(`/api/v1/hosts/${id}/addresses`);
}

export function getInterfaces(id: number | string): Promise<Iface[]> {
  return request<Iface[]>(`/api/v1/hosts/${id}/interfaces`);
}

export function getDrives(id: number | string): Promise<Drive[]> {
  return request<Drive[]>(`/api/v1/hosts/${id}/drives`);
}

export function getPackages(id: number | string): Promise<Pkg[]> {
  return request<Pkg[]>(`/api/v1/hosts/${id}/packages`);
}

export function getUnits(id: number | string): Promise<Unit[]> {
  return request<Unit[]>(`/api/v1/hosts/${id}/units`);
}

// BACKEND_HUB_URL as the hub itself has it, or "" when it is unset. The
// browser reaches the hub on loopback, so window.location says nothing about
// the name agents post to; only the hub knows it, and the setup command is
// wrong without it.
export function getConfig(): Promise<{ hub_url: string }> {
  return request<{ hub_url: string }>("/api/v1/config");
}

// The token is returned exactly once, at creation and at each rotation, and
// is never readable again: the hub stores only its hash
// (internal/hub/admin). A UI that loses it can only rotate, never recover.
export type CreatedHost = {
  id: number;
  hostname: string;
  last_seen: string | null;
  token: string;
};

export function createHost(hostname: string): Promise<CreatedHost> {
  return request<CreatedHost>("/api/v1/hosts", {
    method: "POST",
    body: { hostname },
  });
}

export function rotateHostToken(
  id: number | string,
): Promise<{ id: number; token: string }> {
  return request<{ id: number; token: string }>(`/api/v1/hosts/${id}/token`, {
    method: "POST",
  });
}

export function deleteHost(id: number | string): Promise<void> {
  return request<void>(`/api/v1/hosts/${id}`, { method: "DELETE" });
}

/**
 * Removes one container from a host's inventory, its stored samples with it.
 *
 * The operator's answer to a row for a container that was removed on the
 * host: nothing on the wire says a container is gone -- it just stops being
 * reported, exactly as a stopped one does -- so netra cannot delete the row
 * on its own without also deleting the history of everything that merely
 * paused.
 */
export function purgeContainer(
  hostId: number | string,
  containerId: number | string,
): Promise<void> {
  return request<void>(`/api/v1/hosts/${hostId}/containers/${containerId}`, {
    method: "DELETE",
  });
}

// EventsParams mirrors internal/hub/httpapi/read.go's events() query
// parsing: host, since, until, type, limit. since/until accept RFC 3339 or
// unix milliseconds, matching parseTime.
export type EventsParams = {
  host?: number | string;
  since?: string;
  until?: string;
  type?: string;
  limit?: number;
};

export function getEvents(params: EventsParams = {}): Promise<Event[]> {
  return request<Event[]>(`/api/v1/events${toQueryString(params)}`);
}

// MetricsParams mirrors internal/hub/httpapi/read.go's metrics() query
// parsing: family, from, to, step, columns. from/to accept RFC 3339 or unix
// milliseconds, matching parseTime; step is a Go duration string such as
// "60s", "5m" or "1h", matching parseStep.
export type MetricsParams = {
  family: string;
  from?: string;
  to?: string;
  step?: string;
  columns?: string[];
};

export function getMetrics(
  hostId: number | string,
  params: MetricsParams,
): Promise<MetricsResponse> {
  return request<MetricsResponse>(
    `/api/v1/hosts/${hostId}/metrics${toQueryString(params)}`,
  );
}

// internal/hub/read/metrics.go: FleetResult
//
// Everything above `hosts` is shared by every host in the answer, because the
// tier is chosen from the family and the step and never from the data. That is
// what makes the split below sound.
type FleetMetricsResponse = Omit<MetricsResponse, "series"> & {
  hosts: { host_id: number; series: MetricsSeries[] }[];
};

/**
 * One family, one window, every host named -- the fleet form of getMetrics.
 *
 * Returns a Map of the SAME MetricsResponse shape the per-host call returns,
 * one entry per host, by re-attaching the shared header to each host's series.
 * That is the point of the split: every reader downstream -- griddedValues,
 * seriesOnGrid, memoryBands, perCoreBands, containerTrends -- keeps taking a
 * MetricsResponse and does not learn that the fleet page now asks once instead
 * of N times.
 *
 * A host that reported nothing comes back with an empty `series`, never a
 * missing entry, so a caller can tell silence from an unanswered request.
 */
export async function getFleetMetrics(
  hostIds: readonly (number | string)[],
  params: MetricsParams,
): Promise<Map<number, MetricsResponse>> {
  const res = await request<FleetMetricsResponse>(
    `/api/v1/metrics${toQueryString({ ...params, hosts: hostIds.join(",") })}`,
  );
  const { hosts, ...header } = res;
  return new Map(
    hosts.map((host) => [host.host_id, { ...header, series: host.series }]),
  );
}
