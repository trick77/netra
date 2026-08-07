# netra — Design Specification

**Date:** 2026-08-07
**Status:** Approved for phase 1 planning
**Phase:** 1 of N

---

## 1. What netra is

A self-hosted monitoring system for Linux servers, bare-metal hosts and VPSes that run
Docker. A central **hub** (container) stores metrics and inventory; **agents**
(containers) run on each monitored host and push data to the hub.

Positioned against [beszel](https://github.com/henrygd/beszel), which solves the same
problem. netra targets the two axes where beszel is weakest: **hub throughput at scale**
and **collector correctness/observability**. netra is not a beszel fork and carries no
compatibility obligation — no shared wire format, no shared database, no migration path.

### Goals

- Handle ~100 hosts × 20–40 containers without the hub becoming the bottleneck.
- Never silently degrade: a collector that cannot run reports *why*, to the hub.
- No data loss across short hub outages.
- Docker-first deployment where every documented capability actually works in a container.

### Non-goals (permanent)

- Realtime / sub-minute live views.
- GPU metrics.
- Battery and eMMC metrics.
- Bare-binary agent distribution (Docker only).
- cgroup v1 support.
- Windows, macOS or FreeBSD agents.
- Replacing Prometheus/Grafana for arbitrary querying and dashboarding.

---

## 2. Scale targets

Sizing basis for every storage decision below.

| Parameter | Value |
|---|---|
| Hosts | ~100 |
| Containers per host | 20–40 |
| Scrape interval | **60s** (default; configurable per host) |
| Active series | ~22,000 |
| Sustained ingest | ~370 points/sec |
| Retention | 90 days (tiered) |

---

## 3. Phase boundaries

### Phase 1 (this spec)

Agents collect → HTTPS POST → hub → TimescaleDB, plus a token-gated read API so
ingestion can be verified from the command line.

- All collectors listed in §6, including package inventory.
- Full schema (§5), including tables whose data is only *displayed* in later phases.
- Read + admin JSON API (§8).
- CI/CD, image publishing, coverage gating (§11).

### Deferred

| Item | Phase | Note |
|---|---|---|
| Web UI | 2 | Own design system — music's tokens are built for a media player and are not assumed to fit |
| OIDC (Authentik) | 2 | Pointless without a UI to log into. `oidc-fix-report.md` in the music repo is the input |
| Alerting engine | 2 | Thresholds, host-down, event-driven rules, notification delivery |
| LLM diagnostics | 3 | Reuses music's `internal/llm` + `BACKEND_CHAT_*` pattern. Needs history and alerts to reason over |
| Geocoding + map | 2 | Schema exists in phase 1; resolution and display come with the UI |
| Fleet/aggregate API endpoints | 2 | Speccing them now would be guessing at the UI |
| Latency probe (ioping-style) | — | Considered and dropped |

---

## 4. Architecture

```
┌─────────────────────────┐         ┌──────────────────────────────┐
│ Monitored host          │         │ Hub host                     │
│                         │         │                              │
│  ┌───────────────────┐  │  HTTPS  │  ┌────────┐   ┌────────────┐ │
│  │ netra-agent       │──┼────────▶│  │ netra  │──▶│ TimescaleDB│ │
│  │ (container)       │  │  POST   │  │ (Go)   │   │ (Postgres) │ │
│  └───────────────────┘  │ protobuf│  └────────┘   └────────────┘ │
└─────────────────────────┘         └──────────────────────────────┘
```

Both components are Go, `CGO_ENABLED=0`, single static binary, distributed as Docker
images. The hub follows `../music`'s architecture: `backend/cmd/` + `backend/internal/<domain>/`,
stdlib `net/http` with Go 1.22 method routing (no framework), numbered SQL migrations,
Traefik labels on an external network.

**The one deviation from music's locked choices** is the database: netra uses
Postgres + TimescaleDB via `pgx` (pure Go, so `CGO_ENABLED=0` survives) instead of
`ncruces/go-sqlite3`. SQLite is **not** used anywhere in netra.

### Why one datastore, not two

A split — SQLite for relational, Timescale for metrics — was explicitly rejected. It
forfeits every advantage of Timescale (one backup, one migration runner, SQL joins from a
container to its host to its alert rules) while still adding the Postgres dependency.

---

## 5. Data model

### 5.1 Design rules

1. **Typed tables per metric family**, not a generic `(series_id, ts, value)` table.
   At target scale this is ~3,300 rows/min instead of ~22,000, and compresses far better
   because each column holds one metric with a consistent magnitude.
2. **Metric tables reference small integer ids**, never strings. A container rename or a
   host re-registration must not fork history.
3. **Absent subsystems are `NULL`, never `0`.** `swap_used = 0` means swap exists and is
   unused; `NULL` means there is no swap. Collapsing them produces misleading graphs and
   un-writable alert rules. Applies to swap, ZFS ARC, and any sensor a board lacks.
   SQL aggregates ignore `NULL` by default, so continuous aggregates do the right thing
   for free. The wire format must preserve the distinction (§7.3).
4. **Discrete state changes are events, not samples.** Anything that is constant for hours
   and matters at the moment it changes goes to `events`, not a hypertable.

### 5.2 Dimension tables (plain Postgres)

| Table | Key columns |
|---|---|
| `providers` | `id`, `name` |
| `sites` | `id`, `provider_id`, `name`, `facility`, `address`, `latitude`, `longitude`, `country_code`, `timezone` |
| `hosts` | `id`, `site_id`, `hostname`, `fingerprint`, `host_type`, `agent_version`, `go_version`, `build_commit`, `kernel`, `os_name`, `arch`, `cpu_model`, `cores`, `threads`, `memory_total`, `metadata_hash`, `capabilities` (jsonb), `latitude`, `longitude` |
| `host_current` | Denormalised latest snapshot per host, updated on ingest |
| `tokens` | `id`, `host_id`, `token_hash`, `created_at`, `last_used_at` |
| `containers` | `id`, `host_id`, `container_key`, `name`, `image`, `is_agent` |
| `filesystems` | `id`, `host_id`, `label`, `mountpoint`, `device_id` |
| `sensors` | `id`, `host_id`, `chip`, `label` |
| `devices` | `id`, `host_id`, `device`, `model`, `serial` |
| `systemd_units` | `id`, `host_id`, `unit_name` |
| `host_addresses` | `host_id`, `iface`, `if_index`, `address` (`inet`), `family`, `scope`, `vrf`, `description`, `first_seen`, `last_seen` |
| `host_packages` | `host_id`, `name`, `version`, `arch`, `format`, `size_bytes`, `first_seen`, `last_seen` |
| `events` | `id`, `host_id`, `ts`, `type`, `subject`, `detail` (jsonb) |
| `systemd_unit_events` | `host_id`, `unit_id`, `ts`, `state`, `substate` |
| `package_events` | `host_id`, `ts`, `name`, `action`, `from_version`, `to_version` |
| `geocode_cache` | `query`, `latitude`, `longitude`, `source`, `resolved_at` |

`events` is the general discrete-state table (SMART thresholds, mdadm degradation, public
IP changes, agent version changes). `systemd_unit_events` and `package_events` are separate
because both carry structured, queryable columns that would otherwise be buried in jsonb,
and both are written in bursts large enough to warrant their own indexes.

`host_current` exists so the host-list endpoint never touches a hypertable.

**Surrogate vs natural keys.** Every dimension has an integer `id` (the surrogate PK that
hypertables reference) *and*, where one exists, a natural key that identifies the thing on
the host. For containers: `containers.id` is the PK referenced by
`container_samples.container_id`, while `containers.container_key` holds the natural key —
compose project + service name, falling back to container name (§6.2). The same split
applies to `filesystems` (`id` / `label`), `sensors` (`id` / `chip`+`label`) and
`systemd_units` (`id` / `unit_name`). Hypertables reference `id` only, so a rename changes
one dimension row and no history.

`host_addresses.address` uses the native `inet` type, enabling subnet queries
(*"every host with an address in 172.19.0.0/16"*, *"every host with a public IPv4"*).
The **hub** derives `scope` (`loopback` / `private` / `public`) from the address — the
agent reports raw facts only, so classification is one implementation that can be fixed
without redeploying agents. IPv4 and IPv6 are treated identically throughout.

### 5.3 Hypertables

| Table | Columns beyond `(host_id, ts)` |
|---|---|
| `host_samples` | `cpu_total`, `cpu_user`, `cpu_system`, `cpu_iowait`, `cpu_steal`, `cpu_idle`, `mem_total`, `mem_used`, `mem_available`, `mem_buffcache`, `mem_zfs_arc`, `swap_total`, `swap_used`, `load1`, `load5`, `load15`, `uptime_s`, `services_total`, `services_failed` |
| `cpu_core_samples` | `core`, `busy` |
| `container_samples` | `container_id`, `cpu_pct`, `mem_used`, `mem_limit`, `net_rx`, `net_tx`, `io_read`, `io_write` |
| `filesystem_samples` | `fs_id`, `total`, `used`, `free`, `inodes_total`, `inodes_used`, `read_bytes`, `write_bytes` |
| `disk_io_samples` | `device`, `read_bytes`, `write_bytes`, `read_ops`, `write_ops`, `io_util_pct`, `r_await_ms`, `w_await_ms`, `weighted_io_pct` |
| `net_samples` | `iface`, `rx_bytes`, `tx_bytes`, `rx_errs`, `tx_errs` |
| `sensor_samples` | `sensor_id`, `temp` |
| `smart_attributes` | `device_id`, `attr_id`, `raw`, `normalized` |
| `process_samples` | `name`, `cpu_pct`, `mem_bytes`, `count` |
| `agent_samples` | `uptime_s`, `rss_bytes`, `goroutines`, `scrape_duration_ms`, `buffer_depth`, `buffer_dropped_total`, `post_failures_total`, `post_latency_ms` |
| `collector_samples` | `collector`, `duration_ms`, `ok`, `error_code` |
| `custom_samples` | `metric`, `labels` (jsonb), `value` |

`custom_samples` is the escape hatch so a future pluggable collector does not require a
migration. Everything with a known shape gets a typed table.

`smart_attributes` is deliberately generic — SMART attribute sets vary per drive model.

`agent_samples.uptime_s` and `host_samples.uptime_s` are **different facts**: an agent
uptime reset with host uptime unchanged means the agent restarted on its own, which also
means its ring buffer was lost. Conflating them hides agent crash-looping behind a
healthy-looking host.

### 5.4 Retention and rollups

| Tier | Resolution | Retained | Approx. points |
|---|---|---|---|
| Raw | 60s | 7 days | ~220M |
| Rollup 1 | 5 min | 30 days | ~190M |
| Rollup 2 | 1 hour | 90 days | ~48M |

Rollups are Timescale continuous aggregates with refresh policies; expired chunks are
dropped by retention policies. Estimated 1–2 GB compressed.

**Two coupling constraints that are silent-failure modes if violated:**

1. **`start_offset` must exceed the maximum backfill age.** Timescale cuts invalidations
   against the refresh window — *"invalidations are cut against the given refresh window,
   leaving only invalidation entries that are outside the refresh window"* (invalidation.c).
   Data backfilled older than `start_offset` is recorded as invalid but the scheduled
   refresh never reaches it, and the rollup stays wrong permanently. The agent ring buffer
   is ~1 hour, so **`start_offset = 6h`**. If the buffer window is ever raised,
   `start_offset` must be raised with it.
2. **Raw retention must exceed the aggregate refresh lag**, or chunks are dropped before
   being materialised into the 5-minute tier. Raw at 7 days against a 6h `start_offset` is
   comfortable. Do not reduce raw retention without re-checking this.

`start_offset => NULL` is not a safe shortcut — Timescale validates it against raw
retention.

**`process_samples` is the deliberate exception:** raw only, **48 hours**, no continuous
aggregates. A 1-hour average of a top-N list whose membership changes between buckets is
close to meaningless; process data answers recent-past questions.

### 5.5 Ingest

`COPY` into typed tables, not row-by-row `INSERT` — the single largest hub-side throughput
factor. Deduplication on the natural key via `ON CONFLICT DO NOTHING`, so replayed batches
are harmless.

---

## 6. Agent

Go, `CGO_ENABLED=0`, one image (`ghcr.io/trick77/netra-agent`). **Stateless — no volumes.**

### 6.1 Collector model

A `Collector` interface with **per-collector intervals**, so slow collectors do not
constrain the scrape loop. Every collector reports duration, success and an error code
into `collector_samples`, and its availability into host capabilities.

**A collector that cannot run never prevents the agent from starting.** It reports
unavailability with a reason; everything else keeps working. beszel's silent Debug-level
degradation is the specific behaviour being fixed.

### 6.2 Collectors

| Collector | Source | Interval | Requires |
|---|---|---|---|
| CPU | `/proc/stat` | 60s | — |
| Per-core CPU | `/proc/stat` | 60s | — |
| Memory | `/proc/meminfo`, ZFS ARC from `/proc/spl/kstat` | 60s | — |
| Load | `/proc/loadavg` | 60s | — |
| Disk I/O | `/proc/diskstats` | 60s | — |
| Network | `/proc/net/dev` | 60s | `network_mode: host` |
| Addresses | netlink | detects changes (delivered via metadata hash) | `network_mode: host` |
| Containers | cgroup v2 files + Docker socket for metadata | 60s | `/var/run/docker.sock:ro` |
| Filesystems | `statfs` on marker dirs | 60s | marker mounts (§6.4) |
| Sensors | `/sys/class/hwmon` | 60s | — (`/sys` is mounted ro by Docker automatically) |
| mdraid | sysfs | 60s | — |
| SMART | `smartctl --scan -j` + per-device collect | 1h | `SYS_RAWIO` (SATA), `SYS_ADMIN` (NVMe), `devices:` |
| systemd | D-Bus `org.freedesktop.systemd1` | 60s | `/run/dbus/system_bus_socket:ro` |
| Processes | `/proc/*/stat` | 60s | `pid: host` — **opt-in** |
| Packages | `/var/lib/dpkg/status`, `/lib/apk/db/installed` | on mtime change, daily floor | package DB mounts |
| Self | runtime | 60s | — |

**Per-core CPU is collected for all cores, always.** ~800 series at target scale; the
earlier cardinality concern was unfounded.

**Containers read cgroup v2 directly**; the Docker socket is used only for the container
list, names, labels, health and image, and only when the list changes. beszel calls the
Docker stats API per container per scrape, which is the most expensive thing its agent
does. **Container memory subtracts `cache` and `inactive_file`** from raw usage — omitting
this is the classic Docker-stats overreporting mistake.

**Container identity is `compose project + service name`** where those labels exist,
falling back to container name. Container IDs change on every recreate; keying on them
means one `docker compose up -d` orphans all history. The ID is stored as an attribute.

**The addresses collector does not deliver anything itself.** It detects that the address
set changed, which flips `metadata_hash`; the hub then requests the metadata block on the
next POST (§7.4). There is no push path.

**Interface names are the join key** between `net_samples.iface` and
`host_addresses.iface`, so both collectors must use the kernel interface name — netlink
for addresses, `/proc/net/dev` for counters, same string. An interface with counters but
no address appears only in `net_samples`; a bridge with an address but filtered from
metrics appears only in `host_addresses`. That asymmetry is intended.

**Per-filesystem I/O requires a device mapping.** `statfs` yields space and inodes only —
never I/O counters. To populate `filesystem_samples.read_bytes` / `write_bytes`, the
collector maps the marker directory's `st_dev` to a block device (`/sys/dev/block/<major>:<minor>`,
resolving partitions to their parent device) and reads the counters from `/proc/diskstats`.
Where that mapping fails — network filesystems, overlay, LVM stacks it cannot resolve —
both columns are `NULL` rather than zero, per §5.1 rule 3.

**Network metric filtering** excludes `lo`, `veth*`, `docker0`, `br-*` and tunnels by
default — a Docker host has one veth per container, and unfiltered they churn constantly.
Note this differs from the *address inventory*, where bridge addresses are wanted; there,
only interfaces that actually carry an address produce rows, which excludes veths naturally.

**Counter resets** — diskstats, network bytes, container CPU and `buffer_dropped_total`
are monotonic and reset on reboot or agent restart. If the current value is below the
previous, emit no sample rather than a negative or a spike.

**Process CPU** requires `utime+stime` deltas keyed on `(pid, starttime)` — PIDs are
reused, and without the start-time check a recycled PID produces a garbage spike.
Processes are **aggregated by name**, summing across PIDs, then top-10-by-CPU ∪
top-10-by-memory. `nginx` with 16 workers is one row with `count = 16`.

**Sensor identity is `chip_name + label`, never `hwmonN`** — hwmon numbering is not stable
across reboots, and keying on the index silently forks temperature history on every
restart. Primary sensor selection prefers known CPU chips (`coretemp`, `k10temp`,
`zenpower`, then thermal zones) rather than "highest temperature wins", which surfaces a
spinning disk. `NETRA_PRIMARY_SENSOR` overrides.

Sensor reads are **individually deadlined** (`NETRA_SENSORS_TIMEOUT`, default 2s) — a hung
`temp1_input` on a flaky driver must not stall the scrape. Sensors are read from sysfs
directly; gopsutil is not used, which also removes the panic-recovery workaround beszel
needs.

**NVMe temperatures come from hwmon**, so they need no `smartctl`, no `SYS_ADMIN` and no
device mounts. Only SATA drive temperatures require the SMART path.

**mdraid health is read from sysfs** — no smartctl, no privileges, works everywhere.

**systemd is stored as events, not samples.** A host has 50–200 units and almost none
change state between scrapes; writing "still active" rows would add ~10,000 near-constant
series. Only state changes are written, to `systemd_unit_events`; the numeric summary
(`services_total`, `services_failed`) rides on `host_samples`. Default filter is `.service`
units, excluding ephemeral ones (`user@*`, `systemd-udevd` workers, `session-*.scope`).

**Packages** are parsed only when the package DB mtime changes, with a daily floor.
`dpkg` and `apk` are parsed natively; **rpm is unsupported** (Berkeley DB/SQLite needs
librpm or the `rpm` binary) and reported as an unsupported-format capability rather than
silently producing nothing. `package_events` records install/upgrade/remove — the
"what changed before this host started misbehaving" timeline.

### 6.3 SMART

**One image, `smartctl` included at a pinned version.** No `:alpine` tag split. Shipping
it is what removes version variance: `smartctl` drives disks through kernel ioctls and has
no dependency on host userspace, so one pinned version behaves identically everywhere,
whereas using each host's binary means parsing output from whatever that distro shipped.

`drivedb.h` staleness affects only vendor *attribute naming*; health status, temperature,
reallocated sectors and power-on hours are standardised. Mitigated by regular image
rebuilds, with an optional mount for an updated `drivedb.h`.

**Capabilities and devices are documented in the shipped compose**, not left for the user
to discover:

- `SYS_RAWIO` — SATA
- `SYS_ADMIN` — NVMe (documented as required *only* with NVMe drives; it is
  root-adjacent and should not be pasted in unconditionally)
- explicit `devices:` entries for the physical controllers

**SMART availability is reported to the hub as a capability.** A host missing device
access says so, rather than showing an empty panel.

### 6.4 Filesystems — marker directories

`statfs()` reports the filesystem containing the path given, so the measured path must
physically live on that filesystem. An empty **marker directory** is bind-mounted, not the
data:

```bash
mkdir -p /mnt/ark/.netra
```
```yaml
- /mnt/ark/.netra:/netra/fs/ark:ro
```

This yields complete disk-usage metrics with **zero data exposure** — a deliberate
least-privilege property, and the reason a `/host` root-filesystem mount was rejected
despite being more convenient.

- Label is the trailing path segment (`ark`).
- The root filesystem needs its own marker (`/.netra` → `/netra/fs/root`); there is exactly
  one mechanism, no special case in code.
- **Deduplicate by `st_dev`** — two markers landing on one filesystem would double-report;
  keep the first, warn naming both.
- `:ro` is documentation of intent; an empty directory has nothing to protect.

**Discovery for awareness only.** Optionally bind-mount `/proc/1/mountinfo:/host/mountinfo:ro`
(exposing the mount table and nothing else). The agent then logs one line per unmonitored
filesystem at startup, and again only when the set changes:

```
filesystem /mnt/backup (8.0T, ext4) is not monitored — bind-mount a marker dir
to /netra/fs/<name> to enable
```

Absent, the check is skipped silently. **Discovery never feeds metrics**; measurement is
exclusively marker-dir `statfs`. If `/netra/fs/` is empty the agent logs a startup
**warning**, not a failure — a container-only host is legitimate.

> **To verify during implementation:** that bind-mounting a procfs file yields live reads
> rather than a snapshot. If it does not, drop discovery and keep pure marker-dir behaviour.

### 6.5 Buffering

**In-memory bounded ring buffer, overwrite-oldest.** No disk, no state directory, no
corruption handling.

- Default window ~1 hour (~60 snapshots at 60s), well under 1 MB.
- Replayed oldest-first on recovery, rate-limited so 100 agents reconnecting after a hub
  restart do not stampede it.
- **Deliberately not durable across agent restart.** That case means a host reboot or an
  image update; during an image update the hub is up, so nothing would be buffered.
- `buffer_depth` and `buffer_dropped_total` are reported, so overflow — i.e. real data
  loss — is visible rather than silent.

beszel has no buffering at all: its `agent_cache.go` holds one snapshot per interval for
request deduplication, discarded at half the interval, and `connection_manager.go` merely
retries every 10s. A hub restart is a permanent hole in its graphs.

### 6.6 Configuration

All variables are `NETRA_`-prefixed. beszel's bare `TOKEN`, `KEY`, `LISTEN` are
collision-prone in a `network_mode: host` container. This deviates from music's `BACKEND_`
convention because `BACKEND_` is meaningless on an agent and netra ships two components.

| Variable | Default | Purpose |
|---|---|---|
| `NETRA_HUB_URL` | — | **Required** |
| `NETRA_TOKEN` | — | **Required** |
| `NETRA_INTERVAL` | `60s` | Duration string — no `uint16` millisecond ceiling |
| `NETRA_COLLECTORS_DISABLED` | — | e.g. `smart,processes` — one uniform mechanism |
| `NETRA_SMART_INTERVAL` | `1h` | |
| `NETRA_SMART_DEVICES` | auto | `/dev/sda:sat,/dev/nvme0:nvme` — no separator variable needed |
| `NETRA_SENSORS` / `NETRA_SENSORS_EXCLUDED` | — | Two explicit lists instead of a magic `-` prefix |
| `NETRA_SENSORS_TIMEOUT` | `2s` | |
| `NETRA_PRIMARY_SENSOR` | auto | |
| `NETRA_SYSFS_ROOT` | `/sys` | |
| `NETRA_BUFFER_WINDOW` | `1h` | Coupled to hub `start_offset` (§5.4) |
| `NETRA_LOCATION` | — | e.g. `Gravelines, FR` |
| `NETRA_PROVIDER` | — | e.g. `OVH` |
| `NETRA_FACILITY` | — | e.g. `GRA11` |
| `NETRA_HOST_TYPE` | — | `bare_metal` \| `vps` \| `vm` |
| `NETRA_LOG_LEVEL` | `info` | |

Two required variables against beszel's four. Location and provider live in the agent's
compose because the person deploying the host is the one who knows where it physically is.
The agent performs **no geocoding and no outbound calls** — it states a fact; the hub
interprets it.

---

## 7. Wire protocol

### 7.1 Transport

**HTTPS POST**, not WebSocket. `POST /api/agent/v1/ingest`, protobuf body,
`Authorization: Bearer`.

WebSocket buys server→agent push, an always-open connection, and no per-message TLS
handshake. With a 60s cadence and no realtime view, none of those pay for the connection
state, reconnect logic and ping/pong they cost. Every hub→agent message becomes a field in
the response to a POST the agent was making anyway:

| Response field | Replaces |
|---|---|
| `ack_seq` | `Ack` |
| `config`, `config_version` | `ConfigUpdate` |
| `request_metadata` | metadata resync |
| `retry_after` | `Throttle` |

Consequences: the hub is stateless (no connection table, no per-connection goroutines);
keep-alive reuses one TCP+TLS connection anyway; the endpoint is `curl`-able, which matters
because phase 1 has no UI; buffer replay is just POSTs, needing no special reconnect path.

**Self-monitoring uses the same path.** The agent on the hub's own machine POSTs to
`http://127.0.0.1:8080`. No unix socket, no special case. beszel needs one only because its
hub dials the agent.

### 7.2 Authentication

**One long-lived token per host, from env. No enrollment handshake, no credential on disk.**

1. Operator creates a host in the hub; hub mints a token (`nta_` + 256 bits), stored hashed.
2. Token goes into that host's compose as `NETRA_TOKEN`.
3. Agent presents it as a bearer token; the hub resolves the host before accepting the body.

Revocation is deleting or rotating the token. There is **no universal/self-registration
token** — no fleet-wide secret whose leak enrolls arbitrary machines. Enrollment stays a
deliberate act.

The agent reports a **fingerprint** (`/etc/machine-id` + primary MAC, hashed). A token
arriving from a different fingerprint is flagged rather than silently accepted, catching a
compose file copied to a second host.

### 7.3 Encoding

Protobuf, versioned under `/v1/`. **Typed messages per family** (`HostSample`,
`ContainerSample`, `SensorSample`, …) — protobuf field numbers already serve as the
dictionary, so there is no series-dictionary mechanism and no resync path.

**Optional scalar fields** are required wherever a value may be absent, so "no swap here"
survives as `NULL` rather than being flattened to `0` by proto3 default-value semantics
(§5.1 rule 3).

### 7.4 Static metadata

Static facts are sent once, not per scrape. The agent includes an 8-byte
**`metadata_hash`** in every POST; on mismatch or unknown host the hub sets
`request_metadata: true` and the agent sends the full block next tick.

This self-heals across: the first POST, an agent upgrade, a hub restored from backup, and
an edited `NETRA_LOCATION`.

Metadata contents: `agent_version`, `go_version`, `build_commit`, `hostname`, `kernel`,
`os_name`, `arch`, `cpu_model`, `cores`, `threads`, `memory_total`, `location`, `provider`,
`facility`, `host_type`, `collector_capabilities`, `fingerprint`, `addresses`.

Addresses ride here, so a new Docker bridge or changed public IP flips the hash and is
picked up on the next POST — no separate mechanism.

`agent_version` changes are written to `events`, giving an upgrade timeline without storing
the same string 1,440 times a day.

### 7.5 Delivery semantics

At-least-once. Unacked batches stay in the ring buffer and are dropped on `ack_seq`; replay
is oldest-first with a `backfill` flag. The hub dedupes via `ON CONFLICT DO NOTHING`.

Backfilled batches trigger continuous-aggregate invalidation for the affected range,
subject to the `start_offset` constraint in §5.4.

Timestamps are assigned **agent-side** in epoch milliseconds. The hub records observed
clock skew per host and rejects implausibly-future samples rather than corrupting a chunk.

Liveness: no POST within 3× the host's interval marks it down.

---

## 8. Read API (phase 1)

JSON — this exists to be `curl`ed. Gated by `Authorization: Bearer $NETRA_ADMIN_TOKEN`,
bound to `127.0.0.1:8080`.

**Unauthenticated**

- `GET /api/health` — liveness for the compose healthcheck; reports DB reachability

**Inventory / state**

- `GET /api/v1/hosts` — served from `host_current`, never touches a hypertable
- `GET /api/v1/hosts/{id}` — full metadata including **collector capabilities**
- `GET /api/v1/hosts/{id}/containers` · `/filesystems` · `/addresses` · `/packages` · `/units`

**Time series**

- `GET /api/v1/hosts/{id}/metrics?family=…&from=…&to=…&step=…`

`family` ∈ `host|cpu_core|container|disk_io|net|sensor|process|agent`. `step` is optional;
omitted, the hub selects the tier from the range (<7d → raw, longer → 5-minute or 1-hour).
**That tier-selection logic is what the UI will depend on**, so phase 1 is where it is
built and tested.

**Events**

- `GET /api/v1/events?host=…&since=…&type=…` — SMART thresholds, mdadm degradation, systemd
  state changes, package changes, public IP changes, agent version changes

**Admin** (same token)

- `POST /api/v1/hosts` — create; returns the minted token **once**
- `POST /api/v1/hosts/{id}/token` — rotate
- `DELETE /api/v1/hosts/{id}`
- `GET|PATCH /api/v1/sites`, `/providers` — including manual lat/lon override, which the hub
  never overwrites with a geocode result

A **single shared admin token** in phase 1; per-user roles arrive with OIDC in phase 2.
No fleet-wide aggregate endpoints yet.

---

## 9. Failure modes

| Failure | Behaviour |
|---|---|
| Hub unreachable | Buffer in memory, jittered backoff, replay oldest-first with `backfill` |
| Hub 5xx / `retry_after` | Same path; honour `retry_after` |
| Token invalid (401) | Stop posting, log loudly, retry slowly — a revoked host must not hammer the hub |
| Collector unavailable | Reports reason as a capability; **agent keeps running**, everything else posts |
| Docker socket absent | Container collectors disabled; host vitals continue |
| Duplicate/replayed batch | `ON CONFLICT DO NOTHING` |
| Clock skew | Per-host skew recorded; implausibly-future samples rejected |
| Agent restart | Buffer lost by design; gap in graphs; host down after 3× interval |
| Postgres down | Hub returns 503; agents buffer — no loss for outages under the buffer window |
| Buffer overflow | Oldest dropped; `buffer_dropped_total` increments so loss is visible |
| cgroup v1 host | Startup error naming cgroup v1 explicitly |
| rpm-based host | Package collector reports unsupported format |

---

## 10. Testing

TDD — failing test first, per music's conventions. `make test`.

- **Fixture-based collector tests**: checked-in `/proc`, `/sys` and cgroup trees plus
  recorded `smartctl -j` output, so parsing is tested without privileged hardware.
- **Protobuf round-trip tests**, including optional-field/`NULL` preservation.
- **Ingest tested against real TimescaleDB** in a throwaway container, never a mock —
  continuous aggregates, backfill invalidation and the `start_offset` constraint are the
  risky parts and cannot be verified against a fake.
- **A `start_offset` regression test** that backfills data older than the window and asserts
  the rollup is correct. This is the failure mode that is otherwise silent.
- **Counter-reset tests** for diskstats, network and container CPU.
- **End-to-end**: agent posts → rows land → read API returns them.
- No Playwright this phase — there is no UI.

---

## 11. CI/CD

Modelled on `../music`'s workflows.

### `ci.yaml` — pull requests only, no image build

- `actions/checkout` with **`fetch-depth: 0`** (patch coverage diffs against the PR base;
  a shallow checkout leaves the base ref unresolvable and the gate exits 2)
- `go build ./...`, `go vet ./...`, and an explicit **`gofmt -l`** check (`go vet` does not
  cover formatting)
- `go test -race -covermode=atomic -coverpkg=./...` with `CGO_ENABLED=1` — the race detector
  needs cgo, which is independent of the `CGO_ENABLED=0` invariant for the shipped binary.
  `-coverpkg=./...` attributes coverage across package boundaries.
- Cobertura conversion via `gocover-cobertura` — Go reports statements, not lines, and the
  conversion also merges the duplicate blocks `-coverpkg` emits
- `hack/coverage-gate.sh` — absolute project floor
- `hack/patch-coverage.sh` — changed lines must be ≥ `PATCH_MIN`% covered
- `concurrency` with `cancel-in-progress: true`

Jobs: **`hub`**, **`agent`**, and an **`integration`** job running TimescaleDB as a service
container (netra-specific; music has no equivalent). No UI job in phase 1.

`hack/coverage-floors` carries one entry per component:

```
hub=75.0
agent=75.0
```

Both gates are kept because they ask different questions: the floor asks "is the codebase
tested enough?", the patch gate asks "is the code I just wrote tested?". The floor alone
lets a large well-tested codebase absorb untested new code; the patch gate alone lets debt
sit forever.

### `release.yaml` — push to master

- **`paths-ignore`** so a docs or comment change does not cut a version, build two images
  and push a tag. An ignore-list, not an allow-list: a new top-level directory starts out
  releasing, which is the safe default.
- Semver computed from the latest git tag; `workflow_dispatch` allows major/minor/patch
- Buildx + GHCR, tags `X.Y.Z`, `X.Y`, `X`, short SHA, `latest`; `cache-from/to: type=gha`
- **Two images built per release, sharing one version**: `ghcr.io/trick77/netra` and
  `ghcr.io/trick77/netra-agent`. Lockstep versioning makes protocol compatibility legible.
- **Git tag created only after both image pushes succeed**, via the REST API — a
  git-transport tag push with `GITHUB_TOKEN` is refused once the repo contains workflow
  files. This prevents an orphan tag from a failed release.
- GitHub release created after the tag, so it attaches rather than creating a second pointer

### `cleanup-images.yaml` — weekly, Sundays 04:00 UTC

- `snok/container-retention-policy@v3.1.0`, `keep-n-most-recent: 10`, `tag-selection: both`
- **`cut-off: 1h`** so a fresh release is never caught by a concurrently scheduled cleanup
- **Package-existence check first** — the action panics rather than no-oping when the
  package does not exist
- Covers **both** `netra` and `netra-agent`
- `workflow_dispatch` with a `dry-run` input

---

## 12. Deployment

### Hub — `compose.yaml`

Two services on the external `traefik` network: `netra` and `timescaledb`. Traefik labels
with `tls: "true"` and no certresolver (pre-loaded certificate, as in music). Postgres data
under `./data`. Healthcheck on `/api/health`. Port published on `127.0.0.1` so the read API
can be curled on the box — in phase 1 that loopback port *is* the interface.

Hub variables: `NETRA_LISTEN_ADDR`, `NETRA_DB_DSN`, `NETRA_ADMIN_TOKEN`, `NETRA_LOG_LEVEL`,
`NETRA_RETENTION_RAW` / `_5M` / `_1H`.

### Agent — documented compose

`ghcr.io/trick77/netra-agent:latest`, `network_mode: host`, **no volumes for state**.

Mounts: `/var/run/docker.sock:ro`; marker dirs under `/netra/fs/`; optionally
`/proc/1/mountinfo:ro`, `/run/dbus/system_bus_socket:ro`, `/var/lib/dpkg:ro`.
`cap_add: [SYS_RAWIO]` (+ `SYS_ADMIN` with NVMe) and explicit `devices:` for SMART.
`pid: host` only if the process collector is enabled.

---

## 12a. Agent installer

`install-agent.sh` — POSIX shell, `curl`-able, idempotent, re-runnable. It detects the
host's capabilities, asks before changing anything, and renders the agent's `compose.yaml`
and `.env` from templates fetched from this repository.

### Consent model

**Detect first, then ask.** Nothing is created, written or started until the operator has
seen what was found and agreed. Each mutating step prompts separately, so marker
directories can be accepted while `pid: host` is declined:

- creating each `.netra` marker directory
- writing `compose.yaml`
- writing `.env`
- granting each capability or optional mount
- starting the stack

Defaults are **no** for anything privilege-expanding (`SYS_ADMIN`, `pid: host`), yes for
benign steps. Flags: `--yes` accepts every prompt for unattended installs; `--dry-run`
prints the full plan and touches nothing; `--force` is additionally required to overwrite
an existing `.env`, so a re-run in a provisioning script cannot silently replace a working
token.

### Phases

| Phase | Behaviour |
|---|---|
| Preflight | Docker present and daemon reachable; **cgroup v2** (hard fail with an explicit message on v1); `/etc/machine-id` present |
| Filesystems | Reads the mount table, filters pseudo/bind/overlay mounts and everything under `/var/lib/docker`, and for each accepted filesystem creates the `.netra` marker directory and emits the matching `/netra/fs/<label>` bind mount |
| Sensors | Enumerates `/sys/class/hwmon/*/name` and `temp*_label`; picks the primary sensor by known CPU chip (`coretemp`, `k10temp`, `zenpower`), not hottest-wins |
| SMART | Lists physical controllers, not partitions; distinguishes SATA from NVMe and emits the matching `devices:` entries plus `SYS_RAWIO`, adding `SYS_ADMIN` **only** when NVMe is present |
| Optional extras | Offers the D-Bus socket (systemd units), `/var/lib/dpkg` (packages), and `pid: host` (processes) — the last with an explicit warning that it exposes every process's cmdline and environ |
| Render | Downloads `compose.yaml.tmpl` and `env.tmpl` from the repository and substitutes detected values |
| Finish | Prints what was detected **and what was skipped**, then the `docker compose up -d` command. `--start` runs it |

### Template sourcing

Templates live in the repository, not inline in the script. They are fetched at a **pinned
tag** (`--ref`, defaulting to the installer's own version), never from `master`, so a
mid-refactor template cannot land on a production host. `--template-dir` uses local files
for development and air-gapped installs.

### Token

The hub mints agent tokens; the installer never invents one. It accepts `--token` or
prompts for it.

## 13. Assumptions

1. **Agent is Docker-only.** No bare binary, no Homebrew, no systemd unit — matching the
   "docker based agent to docker based hub" framing.
2. **Linux only.**
3. **cgroup v2 only.** Debian 11+, Ubuntu 22.04+, RHEL 9+ default to it.
4. **Hosts are added manually.** No auto-discovery, no self-registration.
5. **TimescaleDB's TSL license is acceptable** — verified: it restricts offering
   TimescaleDB as a managed database service; self-hosting is unrestricted.
6. **Location and provider are operator-entered facts**, never inferred from IP.

## 14. Items to verify during implementation

1. Bind-mounting `/proc/1/mountinfo` yields live reads, not a snapshot (§6.4).
2. Timescale continuous-aggregate `start_offset` behaviour under backfill, asserted by the
   regression test in §10 rather than trusted from documentation.
3. `smartctl` version pinning strategy and `drivedb.h` update cadence.
