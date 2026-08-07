# netra

Self-hosted Docker host and container monitoring: a Go hub with TimescaleDB storage, and
Go agents that push metrics from Linux hosts. Both components are `CGO_ENABLED=0` static
binaries shipped as container images.

## Status

Phase 1 is in progress. What works today, precisely:

- **Three collectors** — CPU, memory, load. That is all of `internal/agent/collector/`.
  The remaining collectors in the design spec (containers, filesystems, network, SMART,
  sensors, systemd, packages, processes, mdraid, per-core CPU) are not implemented.
- **Agent → hub ingest** — the agent scrapes every 60s, buffers in memory when the hub is
  unreachable, and POSTs protobuf to `/api/agent/v1/ingest` with a bearer token.
- **Hub storage** — host-level schema only, applied by migration on startup: `providers`,
  `sites`, `hosts`, `tokens`, `host_current`, `host_samples`. One hypertable
  (`host_samples`) with 5m and 1h continuous aggregates and retention at 7/30/90 days.
  The per-collector tables in the spec (`net_samples`, `filesystem_samples`,
  `collector_samples`, `host_addresses`, `systemd_unit_events`, `package_events`) land
  with the collectors that fill them.

What does not exist yet:

- **No read API.** The hub serves exactly two routes: `GET /api/health` and
  `POST /api/agent/v1/ingest`. There is nothing to query the stored metrics with except
  `psql`.
- **No admin API**, so no endpoint and no CLI to mint an agent token — see
  [Creating an agent token](#creating-an-agent-token).
- **No UI.**
- **No `install-agent.sh`.**

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
docker network create traefik          # external, shared with other stacks
mkdir -p data/timescaledb              # must exist first, or initdb fails root-owned
cp .env.example .env && $EDITOR .env   # POSTGRES_PASSWORD, NETRA_ADMIN_TOKEN, hostname
docker compose up -d
curl -s http://127.0.0.1:8080/api/health
```

`/api/health` reports database reachability, not just process liveness, so a hub that
cannot reach Postgres reports unhealthy rather than accepting ingest it can only 503.

## Agent quickstart

The intended path is `install-agent.sh` — it detects the host's capabilities, asks before
changing anything, and renders the compose file and `.env` for you. It has not landed yet.

Until then, on each monitored host:

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
