# netra — Next Phases

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement each stage task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/superpowers/specs/2026-08-07-netra-design.md` — authoritative for everything below.
**Preceding plans:** `2026-08-07-phase1-foundation.md` (merged, PRs #1–#3), deployment and setup script (merged, PRs #4–#9).

---

## Where netra actually is

Two plans are complete. What that bought:

- **Wire and storage:** protobuf `IngestRequest`/`HostSample`, HTTPS POST ingest, bearer auth, `COPY` into one hypertable with 5m/1h continuous aggregates and 7/30/90-day retention.
- **Agent:** `Collector` interface, seven collectors (CPU, memory, load, kernelstat, netstat, procs, users), bounded ring buffer, metadata-hash handshake carrying per-collector capabilities.
- **Deployment:** two digest-pinned images published to GHCR on merge, lockstep-versioned; weekly GHCR retention; hub `compose.yaml`; agent reference compose; `setup-agent.sh` with a 353-assertion suite.
- **CI:** build/vet/gofmt, race tests against a real TimescaleDB, coverage floor + patch gates, a shell job under `sh` and `dash`, version-stamping guard.

**What that does not buy: netra is not yet usable.** Seven host-level collectors is still not monitoring — nothing per-device, per-interface, per-filesystem or per-container is collected — and there is no way to read the data back out except `psql`. The gap to close is stated bluntly in Stage 1.

### Group 0 — landed (host-level scalars)

Delivered on the existing flat `Collector` interface, without the dimensional
plumbing Group 1 needs:

- [x] kernelstat (`/proc/stat`) — `ctxt`, `intr`, `processes` as rates with counter-reset handling; `procs_running`/`procs_blocked` as gauges; `btime` as an absolute `boot_time_s`, so "last reboot" does not drift between rows of one boot
- [x] netstat (`/proc/net/snmp`, `/proc/net/netstat`, `/proc/net/snmp6`) — TCP retransmits/resets/opens/errors, `CurrEstab` as a gauge, listen overflows and drops, UDP errors, IP fragmentation and reassembly; UDP and IP mirrored for IPv6. **No `tcp6_*` mirror on purpose:** the kernel's TCP MIB is family-agnostic and already counts IPv6, and `snmp6` has no `Tcp6` block
- [x] procs (`/proc` dirents) — total process count; needs `pid: host`, reports `processes=namespaced` and stays NULL otherwise
- [x] users (`/var/run/utmp`) — session count only. Record size is **detected per file** (384 or 400 bytes): it depends on the host's libc and word size, which a static binary reading a bind-mounted file cannot know at build time
- [x] Agent self-telemetry — `agent_samples` with `scrape_duration_ms`, `post_latency_ms`, `buffer_depth`, `buffer_dropped_total`. Post latency lags one scrape by construction and is NULL after a failed flush
- [x] Collector capabilities — optional `CapabilityReporter`, merged into metadata, flips the hash, stored in `hosts.capabilities`
- [x] `services_total`/`services_failed` reconciled with spec §5.3 (NULL until the systemd collector lands)

### Six tables, one hypertable

`0001_init.sql` ships `providers`, `sites`, `hosts`, `tokens`, `host_current`, `host_samples`. The spec's §5.2 lists sixteen dimension tables and §5.3 lists twelve hypertables. The rest land with the collectors that fill them — see Stage 1A.

---

## Global constraints

Unchanged from the phase 1 plan and **not restated in full here** — read `2026-08-07-phase1-foundation.md` § "Global Constraints". The ones most likely to be violated by the work below:

- **Absent subsystems are `NULL`, never `0`** (§5.1 rule 3). Protobuf `optional` scalars, Go `*T`, SQL `NULL`. This is the single most repeated correctness rule in the spec and every new collector has to get it right.
- **Continuous aggregate `start_offset` (6h) must exceed the ring-buffer window (1h).** Any new aggregate inherits this. Any change to `NETRA_BUFFER_WINDOW`'s 6h ceiling must move `start_offset` with it.
- **Metric tables reference integer surrogate ids, never strings.** A container rename or a host re-registration must not fork history.
- **Counter resets emit no sample**, rather than a negative or a spike.
- TDD, conventional commits, `.yaml` never `.yml`, English only, `CGO_ENABLED=0` for shipped binaries.

Two constraints added by the deployment work:

- **`deploy/**` and `setup-agent.sh` are outside `release.yaml`'s `paths-ignore` on purpose.** The setup script fetches templates at a release tag; a change that cuts no release is contained in no tag and never ships. Do not "tidy" them into the ignore list.
- **Empty directories need a `.gitkeep`.** The setup script fixtures probe for directory *existence*; git does not track empty directories, so the suite was green in every working tree and red only in CI. `test/setup-agent/run.sh` now guards this.

---

## Stage 1 — Finish phase 1 (make netra usable)

The spec calls all of this phase 1. It was deferred, not descoped.

### 1A. Admin API and token minting — **landed**

`auth.Mint` had no non-test callers and `NETRA_ADMIN_TOKEN` was consumed by nothing. Adding a host meant hand-inserting a SHA-256 hash with `psql` — it worked, and it was not a product.

Plan: `2026-08-08-stage1a-admin-api.html`.

- [x] `POST /api/v1/hosts` — create; returns the minted token **once**, never again
- [x] `POST /api/v1/hosts/{id}/token` — rotate
- [x] `DELETE /api/v1/hosts/{id}`
- [x] `GET|PATCH /api/v1/sites`, `/providers` — including manual lat/lon override the hub never overwrites with a geocode result
- [x] Admin-token middleware, constant-time compare, bound to the loopback listener
- [x] A minimal server-rendered UI at `/` — host list, create, rotate, delete, and the token shown once beside a ready-to-paste `setup-agent.sh` line. `html/template` + `embed`, no build step and no design system; the phase-2 UI owns that decision. **This replaced the "update `README.md`" item**, which died with the README in #16

**Two things worth carrying forward.** `tokens` has no `UNIQUE (host_id)`, so rotation deletes and inserts in one transaction — an insert-only rotation leaves the old token live and nothing looks wrong. And the browser cannot send a bearer header on a form post, so the UI exchanges the admin token for a session cookie signed with a key **derived from that token**: changing `NETRA_ADMIN_TOKEN` logs every session out, with no session table to clear.

An unauthenticated `dev` mode was considered for local use and rejected — the loopback binding is a second line of defence, not a reason to drop the first. No `AuthMode` switch exists yet; phase-2 OIDC adds one when there is a second mode to select.

### 1B. Schema — the remaining tables

**While netra is pre-release the schema is squashed into `0001_init.sql` and edited in place.** There are no numbered follow-on migrations and no upgrade path; a schema change means dropping and recreating the database. This reverses the earlier "never edit `0001`" rule for as long as there is nothing deployed to migrate, and it buys something concrete: TimescaleDB cannot `ALTER` a continuous aggregate to add a column, so with numbered migrations every new host-level metric would have been raw-only (7 days) until a later migration recreated the rollups. Editing `0001` means the aggregate is simply *defined* with the new columns.

The properties `0001_init.sql:1-12` documents still bind: `-- netra:no-transaction` on line 1, and every statement individually re-runnable. No `DROP … CREATE` pair may appear in the file.

**Two consequences to carry until the first release:**

- [ ] `Migrate` matches `schema_migrations` **by filename with no checksum** (`migrate.go:41-50`), so an edited `0001` is *silently skipped* on an existing database — the hub starts against a schema missing the new columns and fails on the first insert. The fix is to drop and recreate the database. (The README that used to say so is gone; this line is now the only record.)
- [ ] Add checksum detection to the migration runner before the first release, so an edited applied migration is a startup error rather than silence. **This is what unfreezes the numbered-migration plan below.**

Still to add to `0001` (each hypertable with its continuous aggregates and retention policy, so no tier is ever half-configured):

- [ ] container dimensions and `container_samples`
- [ ] `filesystems`, `filesystem_samples`, `devices`, `disk_io_samples`, `smart_attributes`
- [x] `sensors`, `sensor_samples`, `cpu_core_samples`, `net_samples` — landed with the Group 1 schema slice. `host_addresses` still to come, with the Group 2 addresses collector
- [x] `events` — landed early, out of bullet order, because the **mdraid collector has no hypertable of its own and writes here**. See below
- [ ] `systemd_units`, `systemd_unit_events`, `host_packages`, `package_events`
- [x] `collector_samples` — landed with the Group 1 schema slice
- [ ] `process_samples` (**raw only, 48h, no continuous aggregates** — a 1-hour average of a top-N list whose membership changes between buckets is close to meaningless), `custom_samples`
- [ ] `geocode_cache`

**`mdraid` has no hypertable, and that is not an omission.** 1B lists no mdraid table while 1C Group 1 expects an mdraid collector, which reads like a gap. It is not: spec §5.2 names *"mdadm degradation"* explicitly as one of the things `events` carries, and §5.1 rule 4 sends anything constant for hours to `events` rather than to a sample table — an array is `clean` for weeks, so a 60s series saying so is the same near-constant-series waste that keeps systemd out of §5.3. The mdraid collector writes to **`events`** plus `collector_samples`, which is why `events` was pulled forward out of bullet order. `events` stays a **plain Postgres table**: no `create_hypertable`, no aggregates, no retention policy, and it must not move the counts in `rollup_test.go`.

**The `start_offset` regression test already generalises.** `TestIntegrationRefreshPolicyStartOffsetExceedsBufferWindow` sweeps *every* `policy_refresh_continuous_aggregate` row rather than naming tables, so a new aggregate is checked the moment it exists — no per-aggregate extension needed. The hard-coded policy count is the part that must move, and it is a literal on purpose: adding a hypertable must break the test until the new policy is counted. It reads **14** now (was 4). `TestIntegrationRawRetentionExceedsRefreshLag` was the narrow one — it filtered on `host_samples` alone — and now sweeps every raw hypertable, with `TestIntegrationEveryContinuousAggregateHasRetention` alongside it so an aggregate shipped without retention fails rather than growing unbounded.

**Timescale reports aggregate policies under the aggregate's own view name**, not the internal `_materialized_hypertable_N`. Separating raw from aggregate policies therefore needs an anti-join against `timescaledb_information.continuous_aggregates`, not a name pattern.

### 1C. The thirteen remaining collectors

Ordered by risk and by what unblocks what, not by the spec's table order. Each collector: fixture-based tests with checked-in `/proc`, `/sys` and cgroup trees; reports duration, success and error code into `collector_samples`; availability into host capabilities; **never prevents the agent from starting**.

**Group 1 — no privileges, no dependencies.** Cheap, high value, validates the multi-collector loop.
- [ ] Per-core CPU (`/proc/stat`) — all cores, always; ~800 series at target scale
- [ ] Disk I/O (`/proc/diskstats`) — counter-reset handling
- [ ] Sensors (`/sys/class/hwmon`) — identity is `chip_name + label`, **never `hwmonN`**; per-read deadline (`NETRA_SENSORS_TIMEOUT`); preference list, not hottest-wins
- [ ] mdraid (sysfs) — writes **`events`**, not a hypertable (see 1B). Array state is constant for weeks; only the transition is worth storing
- [~] Self (`agent_samples`) — **partly landed** (Group 0): the table, `scrape_duration_ms`, `post_latency_ms`, `buffer_depth`, `buffer_dropped_total`. Still to fill: `uptime_s`, `rss_bytes`, `goroutines`, `post_failures_total`. Note `agent_samples.uptime_s` and `host_samples.uptime_s` are *different facts*

**Group 2 — needs `network_mode: host`.**
- [ ] Network (`/proc/net/dev`) — filter `lo`, `veth*`, `docker0`, `br-*`, tunnels
- [ ] Addresses (netlink) — delivers nothing itself; flips `metadata_hash`. Hub derives `scope` from the address, so classification is one implementation that can be fixed without redeploying agents. **Interface names are the join key** with `net_samples.iface`.

**Group 3 — needs a mount.**
- [ ] Containers (cgroup v2 + Docker socket for metadata only) — **subtract `cache` and `inactive_file`** from raw memory usage; identity is compose project + service, falling back to container name
- [ ] Filesystems (`statfs` on marker dirs) — dedup by `st_dev`; per-filesystem I/O needs the `st_dev` → block device mapping, and `NULL` (not zero) where that mapping fails
- [ ] systemd (D-Bus) — **events, not samples**; numeric summary rides on `host_samples`
- [ ] Packages (`dpkg`/`apk`) — parse on mtime change with a daily floor; rpm reports unsupported-format

**Group 4 — privileged, opt-in.**
- [ ] SMART (`smartctl`, already in the image at a pinned version) — 1h interval; capability reported when device access is missing
- [ ] Processes (`pid: host`) — `utime+stime` deltas keyed on `(pid, starttime)`, because a recycled PID otherwise produces a garbage spike; aggregate by name, top-10-by-CPU ∪ top-10-by-memory

**Wire protocol:** each family needs its own typed protobuf message (§7.3). `IngestResponse` field 4 is **reserved** — a per-host cadence override in phase 2 must use a new number.

**Setup script coupling:** every collector that lands makes an existing `setup-agent.sh` mount meaningful. `deploy/agent/compose.yaml.example` carries a note listing which collectors are implemented; **update it in the same PR**, or it becomes a lie about what works.

### 1D. Read API

- [ ] `GET /api/v1/hosts` — from `host_current`, never touches a hypertable
- [ ] `GET /api/v1/hosts/{id}` — full metadata including collector capabilities
- [ ] `GET /api/v1/hosts/{id}/containers` · `/filesystems` · `/addresses` · `/packages` · `/units`
- [ ] `GET /api/v1/hosts/{id}/metrics?family=…&from=…&to=…&step=…`
- [ ] `GET /api/v1/events?host=…&since=…&type=…`

**Tier selection is the load-bearing part.** With `step` omitted the hub picks the tier from the range (<7d raw, longer → 5m or 1h). The UI will depend on that logic, so phase 1 is where it gets built and tested — including the boundary cases where a range straddles two tiers.

---

## Stage 2 — Phase 2

Ordered by dependency: alerting needs history, the UI needs the read API, OIDC needs a UI to log into.

- [ ] **Alerting engine** — thresholds, host-down (no POST within 3× interval), event-driven rules, notification delivery
- [ ] **Web UI** — its own design system. music's tokens are built for a media player and are *not* assumed to fit
- [ ] **OIDC (Authentik)** — `oidc-fix-report.md` in the music repo is the input. Replaces the single shared admin token with per-user roles
- [ ] **Geocoding + map** — schema exists already; a manual lat/lon override must never be overwritten by a geocode result
- [ ] **Fleet/aggregate endpoints** — deliberately unspecced until the UI exists, because speccing them now is guessing
- [ ] **Per-host scrape cadence** — `hosts.interval_s` column, admin API to set it, a **new** protobuf field number. `buffer.Ring.Resize` was kept for exactly this and currently has no production caller

---

## Stage 3 — Phase 3

- [ ] **LLM diagnostics** — reuses music's `internal/llm` and the `BACKEND_CHAT_*` pattern. Needs history and alerts to reason over, hence last

---

## Known gaps and debts

Carried forward. None block Stage 1, all are cheap to fix in the right PR.

| Gap | Where | Note |
|---|---|---|
| `NETRA_RETENTION_RAW/_5M/_1H` spec'd, unimplemented | `0001_init.sql:152-154` | Tiers hardcoded. `.env.example` deliberately omits them rather than shipping ignored variables. Fix with Stage 1B or delete from §12 |
| Images are `linux/amd64` only | `release.yaml` | Fine for x86 servers; arm64 (Ampere VPS, Pi) needs `platforms:` widened and a QEMU step |
| `/proc/1/mountinfo` bind-mount liveness unverified | Spec §14 item 1 | Whether it yields live reads or a snapshot. If a snapshot, drop discovery and keep pure marker-dir behaviour. Test when the filesystem collector lands |
| `smartctl` `drivedb.h` update cadence undecided | Spec §14 item 3 | Affects vendor attribute *naming* only; health, temperature, reallocated sectors and power-on hours are standardised |
| Setup script `--ref` resolves the latest release at runtime | `setup-agent.sh` | Deviates from §12a's "the setup script's own version". A literal constant cannot self-update without the release workflow pushing to master. Revisit only if the runtime lookup proves flaky |
| `--force` guards `.env` but not `compose.yaml` | §12a specifies this asymmetry | Defensible — compose is derived, `.env` holds the token — and stated in the finish output |
| Coverage floors still 75% | `hack/coverage-floors` | Raise deliberately once the collector packages land, not incidentally |

---

## Verification

Per stage, in addition to `make check` (which now includes `test-shell`):

**1A** — create a host over HTTP, use the returned token from a real agent, rotate it, assert the old token 401s and the new one 200s. Assert the plaintext appears exactly once in the response and nowhere in the logs.

**1B** — migrations apply to an empty database *and* on top of `0001` (both paths, both tested). Extend the `start_offset` backfill regression test to each new aggregate: write data older than the window and assert the rollup is correct.

**1C** — fixture-based unit tests per collector, plus one end-to-end run per group against a real host: agent posts → rows land → read API returns them. Counter-reset tests for diskstats, network and container CPU. Recorded `smartctl -j` output rather than privileged hardware.

**1D** — tier selection tested at the boundaries, not just the middle. A range that straddles 7 days must pick deterministically and say which tier it used.

**Integration tests need the container and `-p 1`** — `store.OpenTest` drops the shared public schema, so parallel package binaries race:

```bash
docker run -d --name netra-test-db -e POSTGRES_USER=netra -e POSTGRES_PASSWORD=netra \
  -e POSTGRES_DB=netra_test -p 5432:5432 timescale/timescaledb:latest-pg17
NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test make test-integration
```

---

## Process notes worth not relearning

- **Retargeting a PR does not trigger CI.** A base change emits `pull_request: edited`, which is not in the default trigger set. Close and reopen. This is how a fixture bug survived four stacked PRs.
- **`gh pr merge` fails on any PR touching `.github/workflows/`** unless the token carries `workflow` scope. A `git push` over SSH is not an OAuth app and is exempt.
- **`hack/patch-coverage.sh` excludes `cmd/`**, matching `coverage-gate.sh`. `main()` wiring is reachable only by running the binary; without the exclusion, changing one constructor argument is an uncoverable red gate.
- **Verify against a fresh clone**, not the working tree, whenever fixtures are involved.
