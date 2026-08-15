# netra

Fleet monitoring for self-hosted Linux boxes. Two pieces:

* **hub** — one central service. Stores samples in TimescaleDB, serves the
  management UI and the read/admin API, and mints one token per host.
* **agent** — one container per monitored host. Scrapes every collector this
  host can run, every 60s (`config.ScrapeInterval` — fixed, deliberately not
  configurable) and POSTs them to `<hub>/api/agent/v1/ingest`.

The agent is stateless: its backlog is an in-memory ring buffer, so there is no
data directory to back up on a monitored host.

Both images are published on every release:
`ghcr.io/trick77/netra` and `ghcr.io/trick77/netra-agent`.

---

## Deploy the hub

The hub sits behind Traefik. `compose.yaml` expects a Traefik instance already
on the box, with a certificate for your hostname pre-loaded in its static
config — there is no ACME resolver on netra's router.

```sh
docker network create traefik      # external network, shared with your other stacks
mkdir -p data/timescaledb          # bind mount; must exist first or initdb fails root-owned
cp .env.example .env
```

Fill in the three values in `.env`:

| Variable | How |
| --- | --- |
| `POSTGRES_PASSWORD` | `openssl rand -hex 32` |
| `NETRA_ADMIN_TOKEN` | `openssl rand -hex 32` |
| `NETRA_HOSTNAME` | the name Traefik routes to, e.g. `netra.example.com` |

**Hex, not base64.** The password is interpolated raw into `NETRA_DB_DSN`, and
base64's `/` terminates the URL authority — a base64 password makes the hub
crash-loop against a perfectly healthy database. `.env.example` explains the
rest of each variable.

```sh
docker compose up -d
```

The hub runs its migrations on startup and reports database reachability at
`/api/health`, so it goes unhealthy rather than accepting ingest it can only
503.

### What is exposed

Only `PathPrefix(/api/agent/)` is routed from the internet. Everything else —
the management UI at `/`, the read API and the admin API that mints tokens and
deletes hosts — is published on `127.0.0.1:8080` on the hub host and gated on
`NETRA_ADMIN_TOKEN`. If that port is already taken on your host, move netra
with `NETRA_BIND_ADDR` in `.env` (address and port together, e.g.
`127.0.0.1:18080`) and adjust the commands below to match; Traefik reaches the
container over the Docker network, so agent ingest is unaffected either way.
From a laptop, tunnel:

```sh
ssh -L 8080:127.0.0.1:8080 <hub-host>
```

then open <http://127.0.0.1:8080/> and log in with `NETRA_ADMIN_TOKEN`.
Changing that token logs every open session out — the session cookie is signed
with a key derived from it.

### Memory

Both containers are capped in `compose.yaml`: 512 MB for the hub, 2 GB for
timescaledb. The database is additionally told what it has — `TS_TUNE_MEMORY`
and `TS_TUNE_NUM_CPUS` — because `timescaledb-tune` reads the host's
`/proc/meminfo` rather than the container's cgroup limit, and would otherwise
size a 31 GB machine's buffer pool for a database serving a handful of agents.
Raise all four together if your fleet outgrows them.

**Upgrading an install created before those limits existed:** the tune step
runs on first init only, so an existing `data/timescaledb` still carries the
oversized `shared_buffers` and starting it under the new 2 GB cap fails or gets
OOM-killed. Re-tune before you restart:

```sh
docker compose exec timescaledb timescaledb-tune --memory=2GB --cpus=2 --yes
docker compose restart timescaledb
```

A fresh install needs none of this.

---

## Mint an agent token

Each host gets its own token, prefixed `nta_`. In the UI, add the host; or over
the admin API — on the hub host, at whatever `NETRA_BIND_ADDR` publishes:

```sh
curl -s -X POST http://127.0.0.1:8080/api/v1/hosts \
  -H "Authorization: Bearer $NETRA_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"hostname":"ark"}'
```

The response is the only place the plaintext token ever appears: the hub stores
a SHA-256 of it and nothing else. A lost token is replaced
(`POST /api/v1/hosts/{id}/token`), not recovered.

---

## Deploy an agent

### With the setup script (recommended)

```sh
curl -fsSL https://raw.githubusercontent.com/trick77/netra/master/setup-agent.sh | sh
```

It probes what the host actually has — cgroup v2, the Docker socket, the D-Bus
socket, the package database, utmp, SMART devices, `/etc/machine-id`, the mount
table — asks before granting anything, and writes a `compose.yaml` and `.env`
into `./netra-agent`. It installs no software; Docker pulls the agent image when
the stack starts.

Everything read-only is enabled automatically. `SYS_ADMIN` (NVMe SMART health
and wear) is the only privilege it prompts for, and `pid: host` is never
prompted for at all — pass `--pid-host` if you want per-process metrics.

The script is **interactive by design**: it reads `/dev/tty` and fails
immediately without one. There is no unattended mode; see *Fleets* below.

Values it would otherwise prompt for can be passed as flags:

```sh
curl -fsSL https://raw.githubusercontent.com/trick77/netra/master/setup-agent.sh | sh -s -- \
  --hub-url https://netra.example.com \
  --token-file /root/nta.token \
  --location "Zurich, CH" --provider Hetzner --host-type bare_metal \
  --start
```

Other flags worth knowing: `--sys-admin`, `--pid-host`, `--output-dir`,
`--force` (required to overwrite an existing `.env`), `--template-dir` (no
network at all), `--ref`. `sh setup-agent.sh --help` lists them all.

Even with every value passed as a flag the run still needs a terminal: the
grants are confirmed interactively unless `--sys-admin` / `--pid-host` /
`--unsupported-os` take them by name.

### By hand

Skip the script when you want to decide each mount yourself.
`deploy/agent/compose.yaml.example` is the hand-written reference and documents
every mount, capability and device inline:

```sh
cp deploy/agent/compose.yaml.example compose.yaml
cp deploy/agent/env.tmpl .env && $EDITOR .env   # replace every __PLACEHOLDER__
mkdir -p /.netra                                # filesystem marker directory
docker compose up -d
```

Only two variables are required: `NETRA_HUB_URL` and `NETRA_TOKEN`. The agent
refuses to start without either. `deploy/agent/.env.example` marks every other
variable as consumed today or specified-but-not-yet-consumed.

Two things that bite:

* Optional binds ship **commented out**. A long-form bind does not create a
  missing source, and a `devices:` entry for a node this host lacks stops the
  container from starting — uncomment only what this host actually has.
* Disk usage is measured through **empty marker directories** (`/.netra`,
  `/mnt/ark/.netra`, …) bind-mounted to `/netra/fs/<label>`, never by mounting
  the data. `statfs()` reports the filesystem containing the path, so this gives
  full disk and inode metrics with zero data exposure.

### Fleets

There is no unattended mode. Template `deploy/agent/compose.yaml.example` and
`deploy/agent/.env.example` from whatever provisioning system you already run.
(`NETRA_ANSWERS_FILE` exists, but it is the shell suite's test seam — positional
answers, no compatibility promise — and is not a provisioning interface.)

---

## What each collector needs

| Needs | Collectors |
| --- | --- |
| Nothing but `/proc` and `/sys` | CPU, per-core CPU, memory, load, kernelstat, vmstat, limits, netstat, procs, users, disk I/O, sensors, mdraid |
| `network_mode: host` | network, addresses |
| A mount | containers (cgroup v2 + Docker socket), filesystems (marker dirs), systemd (D-Bus socket), packages (dpkg or apk db) |
| An explicit privilege | SMART (`SYS_RAWIO`, plus `SYS_ADMIN` for NVMe, plus `devices:`), processes (`pid: host`) |

A collector that cannot run reports **why** as a capability and is skipped. It
never prevents the agent from starting, and an unavailable metric is left NULL
rather than reported as 0. So a host that grants none of the optional access
still delivers everything in the first row.

Two long-standing exceptions worth stating up front: the process count needs
`pid: host` (and `NETRA_PID_HOST=1` to match), and the logged-in session count
needs the `/var/run/utmp` bind — which yields nothing on Alpine and other
busybox systems that ship no utmp writer.

---

## Build from source

```sh
make build          # hub + agent into bin/
make build-hub      # runs `make ui` first
make build-agent
```

Build the UI with `make ui`, not `npm run build` directly: vite's
`emptyOutDir` wipes the tracked `internal/hub/web/dist/.gitkeep` that
`go:embed` needs, and the make rule restores it.

Container images: `Containerfile` (hub) and `Containerfile.agent`.

## Tests

```sh
make test                                     # unit
make test-integration                         # needs NETRA_TEST_DSN at a TimescaleDB
make test-shell                               # shellcheck -s sh + the suite under dash
```

`test-integration` runs with `-p 1` on purpose — the test store drops the shared
schema, so parallel package binaries race on one database.

## Docs

* Design of record: `docs/superpowers/specs/2026-08-07-netra-design.md`
* Configuration reference: the four annotated deploy files — `compose.yaml`,
  `.env.example`, `deploy/agent/compose.yaml.example`,
  `deploy/agent/.env.example`
