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

async function request<T>(path: string): Promise<T> {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: { Accept: "application/json" },
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
