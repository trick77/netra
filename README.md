# netra

Self-hosted Docker host and container monitoring: a Go hub with TimescaleDB storage, and
Go agents that push metrics from Linux hosts. Both components are `CGO_ENABLED=0` static
binaries shipped as container images.

## Status

Phase 1 is in progress. What works today, precisely:

- **Seven collectors** — CPU, memory, load, kernelstat, netstat, procs, users. That is all
  of `internal/agent/collector/`. Between them they cover host CPU/memory/load and uptime,
  context switches, interrupts, fork rate, runnable and blocked task counts, boot time,
  TCP/UDP/IP counters and fragmentation for both IPv4 and IPv6, the total process count
  (needs `pid: host`) and the logged-in session count (needs `/var/run/utmp`).
  The remaining collectors in the design spec (containers, filesystems, per-interface
  network, disk I/O, SMART, sensors, systemd, packages, per-process, mdraid, per-core CPU)
  are not implemented.
- **Agent self-telemetry** — scrape duration, agent→hub post latency and buffer depth land
  in `agent_samples`. There is no hub→agent probe and there will not be one: the hub is
  stateless and never dials an agent (spec §7.1).
- **Agent → hub ingest** — the agent scrapes every 60s, buffers in memory when the hub is
  unreachable, and POSTs protobuf to `/api/agent/v1/ingest` with a bearer token.
- **Hub storage** — host-level schema only, applied by migration on startup: `providers`,
  `sites`, `hosts`, `tokens`, `host_current`, `host_samples`, `agent_samples`. Two
  hypertables (`host_samples`, `agent_samples`), each with 5m and 1h continuous aggregates
  and retention at 7/30/90 days.
  The per-collector tables in the spec (`net_samples`, `filesystem_samples`,
  `collector_samples`, `host_addresses`, `systemd_unit_events`, `package_events`) land
  with the collectors that fill them.

**The schema is not stable yet, and there is no upgrade path.** While netra is
pre-release the whole schema lives in `0001_init.sql` and is edited in place rather than
extended by numbered migrations. `Migrate` tracks migrations **by filename, with no
checksum** (`internal/hub/store/migrate.go`), so an edited `0001` is silently skipped on a
database that already applied it: the hub starts cleanly against a schema missing the new
columns, and the first insert fails on an unknown column. After pulling a change that
touches the schema, **drop and recreate the database** — do not try to migrate it. This
stops at the first release.

What does not exist yet:

- **No read API.** The hub serves exactly two routes: `GET /api/health` and
  `POST /api/agent/v1/ingest`. There is nothing to query the stored metrics with except
  `psql`.
- **No admin API**, so no endpoint and no CLI to mint an agent token — see
  [Creating an agent token](#creating-an-agent-token).
- **No UI.**

## Architecture

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

One datastore, not two: relational inventory and time-series metrics both live in
Postgres/TimescaleDB via `pgx`. SQLite is not used anywhere.

## Hub quickstart

The hub expects a Traefik-fronted host. `compose.yaml` publishes the API on `127.0.0.1`
only, because in phase 1 that loopback port is the entire interface.

```bash
git clone https://github.com/trick77/netra.git && cd netra   # compose.yaml and .env.example live here
docker network create traefik          # external, shared with other stacks
mkdir -p data/timescaledb              # must exist first, or initdb fails root-owned
cp .env.example .env && $EDITOR .env   # POSTGRES_PASSWORD, NETRA_ADMIN_TOKEN, hostname
docker compose up -d
curl -s http://127.0.0.1:8080/api/health
```

`/api/health` reports database reachability, not just process liveness, so a hub that
cannot reach Postgres reports unhealthy rather than accepting ingest it can only 503.

## Agent quickstart

On each monitored host:

```bash
curl -fsSL https://raw.githubusercontent.com/trick77/netra/master/setup-agent.sh -o setup-agent.sh
sh setup-agent.sh
```

It **installs nothing**. It detects what the host actually has, asks before changing
anything, and writes two files into `./netra-agent` (or into `./` when you are already in
a directory named `netra-agent`):

| | |
|---|---|
| `compose.yaml` | generated from this run's detection, overwritten on every run |
| `.env` | your hub URL and token — never overwritten without `--force` |

Plus empty `.netra` marker directories on each measured filesystem, which hold no data and
exist only so the agent can `statfs` the filesystem they sit on. Docker pulls the agent
image when you start the stack, which the script does not do unless you pass `--start`.

It asks four yes/no questions at most, and only the last applies to every host:

| Question | Default | Skip it with |
|---|---|---|
| Continue on a distro netra does not recognise? | no | `--unsupported-os` |
| Grant `SYS_ADMIN` for NVMe SMART health and wear? | no | `--sys-admin` |
| Load the `drivetemp` kernel module and check whether it works? | yes | — |
| Write the files and create the marker directories? | yes | — |

**Host CPU, memory and load are never asked about.** They come from `/proc`, they need no
privileges, and they are what netra is for. Only the per-*process* breakdown needs the host
PID namespace, and that is `--pid-host` or nothing — never a prompt, because a question
reading "enable CPU and memory metrics?" makes the core of the product look optional.

Everything read-only is enabled without asking, too — the Docker socket, the mount table,
the package database, the D-Bus socket, and `SYS_RAWIO` for SATA SMART. `SYS_ADMIN` is the
only privilege it asks about.

On a **virtual host** it asks less still. A hypervisor's disks carry no SMART data, so SMART
is skipped entirely rather than granting `SYS_RAWIO` for a metric that cannot exist, and the
missing temperature sensors are reported as normal rather than as a driver you should go and
load. `--assume-physical` overrides the detection.

It also asks for the hub URL, the agent token (input hidden), and where the host is —
location, provider and host type. Each can be given as a flag instead: `--hub-url`,
`--token` / `--token-file`, `--location`, `--provider`, `--host-type`. Nothing is
created, written or started until you agree at the single write gate.

It reports whether it is running as **root** before it asks anything. Root is not required
— detection reads world-readable files, the two files land wherever `--output-dir` points,
and Docker only needs a user in the `docker` group. What root adds is the `.netra` marker
directory on filesystems this user cannot write (each one is named as it is skipped) and
loading the `drivetemp` module.

It needs `awk`, `sed`, `grep`, `tr`, `head`, `cat`, `sort`, `wc`, `mktemp`, `mkdir`, `rm`,
`cp` and `id` — checked in the first second, so a minimal image is told what it is missing
instead of failing halfway through with `tr: not found`. Templates are fetched with `curl`
or, failing that, `wget`; `--template-dir` needs neither.

`drivetemp` is worth a word. Without it, SATA drive temperatures come from `smartctl` on
the hourly SMART poll; with it they arrive through hwmon on the 60-second sensor scrape,
with no extra privileges. The module loads on any kernel that ships it but produces
nothing when the controller or the drives do not report SCT temperature, so the script
loads it, re-reads hwmon, and persists it to `/etc/modules-load.d/drivetemp.conf` **only**
if a chip actually appeared — unloading it again if not. No host package is ever needed:
`smartmontools` ships inside the agent image.

### There is no unattended mode

The script is interactive and fails immediately without a terminal. To configure a fleet,
template [`deploy/agent/compose.yaml.example`](deploy/agent/compose.yaml.example) and
[`.env.example`](deploy/agent/.env.example) from whatever provisioning system you already
run:

```bash
mkdir -p /path/to/netra-agent && cd /path/to/netra-agent
curl -O https://raw.githubusercontent.com/trick77/netra/master/deploy/agent/compose.yaml.example
curl -O https://raw.githubusercontent.com/trick77/netra/master/deploy/agent/.env.example
mv compose.yaml.example compose.yaml
cp .env.example .env

mkdir -p /.netra                       # marker dir for the root filesystem
$EDITOR .env                           # NETRA_HUB_URL and NETRA_TOKEN
$EDITOR compose.yaml                   # prune mounts/devices this host lacks
docker compose up -d
```

Read [`deploy/agent/compose.yaml.example`](deploy/agent/compose.yaml.example) before
running it — it is a reference covering every optional mount, capability and device, and
several of them describe collectors that do not exist yet. In particular the `devices:`
entries (`/dev/sda`, `/dev/nvme0`) must match the host or the container will not start,
and `pid: host` and `SYS_ADMIN` are commented out on purpose.

The agent is stateless: no volume for state, no data directory, nothing to back up.

### Creating an agent token

The hub mints agent tokens, and the admin API that would expose that is phase 2. Today
`auth.Mint` has no non-test callers and there is no endpoint or CLI for it. **This is a
known gap** — in phase 1 you insert the host and the token hash directly.

Tokens are `nta_` + unpadded base64url of 32 random bytes. The hub stores only
`sha256(<the full token, prefix included>)` as `BYTEA`, so the plaintext exists exactly
once, at the moment you generate it.

```bash
TOKEN="nta_$(openssl rand 32 | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
HASH=$(printf %s "$TOKEN" | openssl dgst -sha256 -hex | awk '{print $NF}')
echo "token: $TOKEN"
echo "hash:  $HASH"
```

Then, against the hub's database:

```sql
INSERT INTO hosts (hostname) VALUES ('web01') RETURNING id;
-- use the returned id, and the $HASH from above:
INSERT INTO tokens (host_id, token_hash) VALUES (1, decode('<hash>', 'hex'));
```

Put `$TOKEN` in the agent's `NETRA_TOKEN`. Hosts are added manually by design; there is no
self-registration.

## Development

```bash
make check     # go vet, go test ./..., plus a gofmt gate that fails on unformatted files
make build     # bin/netra and bin/netra-agent, version-stamped via -ldflags
make proto     # regenerate internal/gen/ from proto/ with buf
```

Integration tests need a real TimescaleDB and are skipped unless `NETRA_TEST_DSN` is set:

```bash
docker run -d --name netra-test -p 5432:5432 \
  -e POSTGRES_USER=netra -e POSTGRES_PASSWORD=netra -e POSTGRES_DB=netra_test \
  timescale/timescaledb:2.29.1-pg17

make test-integration
```

`make test-integration` runs with `-p 1`, which is mandatory rather than cautious:
`store.OpenTest` drops and recreates the shared `public` schema, so two package test
binaries running in parallel against one database destroy each other's fixtures
mid-test.

## Configuration

Hub variables are documented in [`.env.example`](.env.example); agent variables in
[`deploy/agent/.env.example`](deploy/agent/.env.example). Both mark which variables are
read today and which are specified but not yet consumed.

The full reference — data model, wire protocol, collector requirements, marker
directories, phase boundaries — is
[`docs/superpowers/specs/2026-08-07-netra-design.md`](docs/superpowers/specs/2026-08-07-netra-design.md).
It is authoritative; the `.env.example` files deliberately do not duplicate its tables.

## Images

- `ghcr.io/trick77/netra` — hub
- `ghcr.io/trick77/netra-agent` — agent

Versioned in lockstep from a single release: the agent and hub of a given tag are built
from the same commit and are the combination that is tested together.

`linux/amd64` only today — `release.yaml` builds a single platform. arm64 needs the
`platforms:` list widened, nothing more; the binaries are pure Go.
