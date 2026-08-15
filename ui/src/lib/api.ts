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
  site_id: number | null;
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
  // Inventory rather than a gauge, and on the list because the CPU sparkline
  // is a per-core stack: the page has to know how many logical CPUs a host
  // has before deciding to ask for one series per core.
  threads: number | null;
};

// internal/hub/read/host.go: HostDetail (embeds HostSummary)
export type HostDetail = Host & {
  site_name: string | null;
  provider_name: string | null;

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

  capabilities: Record<string, string>;
};

// internal/hub/read/inventory.go: Container
export type Container = {
  id: number;
  container_key: string;
  name: string | null;
  image: string | null;
  is_agent: boolean;
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

// internal/hub/read/inventory.go: Package
export type Pkg = {
  name: string;
  version: string;
  arch: string;
  format: string;
  size_bytes: number | null;
  first_seen: string;
  last_seen: string;
};

// internal/hub/read/inventory.go: Unit
export type Unit = {
  id: number;
  unit_name: string;
  state: string | null;
  substate: string | null;
  since: string | null;
};

// internal/hub/read/events.go: Event
export type Event = {
  id: number;
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

export function getPackages(id: number | string): Promise<Pkg[]> {
  return request<Pkg[]>(`/api/v1/hosts/${id}/packages`);
}

export function getUnits(id: number | string): Promise<Unit[]> {
  return request<Unit[]>(`/api/v1/hosts/${id}/units`);
}

// internal/hub/httpapi/dimensions.go: siteJSON. Every optional column is a
// pointer server-side so an unset one arrives as null rather than as a zero
// reading as a fact -- 0,0 in particular is a real place, not "no
// coordinates".
export type Site = {
  id: number;
  provider_id: number | null;
  name: string;
  facility: string | null;
  address: string | null;
  latitude: number | null;
  longitude: number | null;
  country_code: string | null;
  timezone: string | null;
};

// internal/hub/httpapi/dimensions.go: the providerJSON declared inside
// listProviders.
export type Provider = {
  id: number;
  name: string;
};

// The fleet list (GET /api/v1/hosts) carries site_id but no site name, and
// the per-host detail call that does carry one would be an N+1 across the
// fleet. Fetch this once and join client-side by site_id.
export function getSites(): Promise<Site[]> {
  return request<Site[]>("/api/v1/sites");
}

// POST /api/v1/sites takes name and provider_id and NOTHING else
// (internal/hub/admin/dimensions.go: CreateSite). Facility, address,
// coordinates, country and timezone are reachable only through patchSite, so
// a form offering all eight at creation would be a create-then-patch pair
// with a half-created site as its failure mode.
export function createSite(
  name: string,
  providerId?: number | null,
): Promise<Site> {
  return request<Site>("/api/v1/sites", {
    method: "POST",
    body: { name, provider_id: providerId ?? null },
  });
}

// The fields of a site to change. An omitted key is left alone -- that is
// what keeps a manually set latitude and longitude safe from a caller that
// only meant to set an address (internal/hub/admin/dimensions.go: PatchSite).
//
// The converse does NOT hold: PatchSite writes every field it is given
// verbatim, so sending "" for a column does not clear it back to null, it
// stores an empty string. No caller should ever send one; a field the
// operator left blank must be omitted from the patch entirely.
export type SitePatch = Partial<Omit<Site, "id">>;

// Returns 204 with no body, which request() already handles.
export function patchSite(id: number, patch: SitePatch): Promise<void> {
  return request<void>(`/api/v1/sites/${id}`, { method: "PATCH", body: patch });
}

// NETRA_HUB_URL as the hub itself has it, or "" when it is unset. The
// browser reaches the hub on loopback, so window.location says nothing about
// the name agents post to; only the hub knows it, and the setup command is
// wrong without it.
export function getConfig(): Promise<{ hub_url: string }> {
  return request<{ hub_url: string }>("/api/v1/config");
}

export function getProviders(): Promise<Provider[]> {
  return request<Provider[]>("/api/v1/providers");
}

// The token is returned exactly once, at creation and at each rotation, and
// is never readable again: the hub stores only its hash
// (internal/hub/admin). A UI that loses it can only rotate, never recover.
export type CreatedHost = {
  id: number;
  hostname: string;
  site_id: number | null;
  last_seen: string | null;
  token: string;
};

export function createHost(
  hostname: string,
  siteId?: number | null,
): Promise<CreatedHost> {
  return request<CreatedHost>("/api/v1/hosts", {
    method: "POST",
    body: { hostname, site_id: siteId ?? null },
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
