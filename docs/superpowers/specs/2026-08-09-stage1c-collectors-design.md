# Stage 1C — the thirteen remaining collectors

**Status:** design, approved 2026-08-09. Supersedes nothing; extends the phase 1
design spec (`2026-08-07-netra-design.md`) and the 1C section of
`docs/superpowers/plans/2026-08-07-next-phases.md`.

## Context

Stage 1B landed the Group 1 schema: `cpu_core_samples`, `disk_io_samples`,
`sensor_samples`, `net_samples`, `events` and `collector_samples`, each with its
5m and 1h aggregates. Nothing writes to any of them. Seven collectors ship today
(CPU, memory, load, kernelstat, procs, netstat, users), all of them filling
scalar fields on the single wide `host_samples` row.

The thirteen collectors that remain cannot be built on that foundation, for a
reason that is structural rather than incidental: **they produce lists, and the
current collector contract has nowhere to put one.** Per-core CPU on a 16-core
host is sixteen rows. Disk I/O is one row per device, network one per interface.
mdraid and systemd produce events — statements that something changed — rather
than measurements at all.

So 1C begins with a change to how a collector returns data, and everything else
follows from it.

## 1. The collector contract

### The problem being fixed

Today `Collect` takes the sample and writes into it:

```go
Collect(ctx context.Context, sample *netrav1.HostSample) error
```

and the agent logs a failure and moves on:

```go
if err := col.Collect(ctx, sample); err != nil {
    slog.Warn("collector failed", "collector", col.Name(), "err", err)
}
```

A collector that sets three fields and then fails leaves those three fields in
the sample. Nothing unsets them, and they are stored as though they were
measured. Spec §5.1 rule 3 says an unset field means the subsystem is absent —
a partially-written field is indistinguishable from a real reading, so the rule
quietly stops holding. `Prime`'s doc comment already asserts the behaviour the
code does not provide ("its fields stay unset"). This is latent with seven
collectors and sharper with twenty.

### The change

Collectors return their contribution instead of mutating a shared one:

```go
// Result is one collector's contribution to a scrape. A collector fills only
// what it owns; everything else stays nil.
type Result struct {
    Host    *netrav1.HostSample // this collector's share of the host row
    Cores   []*netrav1.CpuCoreSample
    Disks   []*netrav1.DiskIoSample
    Sensors []*netrav1.SensorSample
    Nets    []*netrav1.NetSample
    Events  []*netrav1.Event
    // further families added by the PR that introduces them
}

type Collector interface {
    Name() string
    Interval() time.Duration
    Collect(ctx context.Context) (*Result, error)
}
```

The agent merges results from collectors that succeeded and discards the rest
whole:

```go
for _, col := range c.collectors {
    res, err := col.Collect(ctx)
    if err != nil {
        slog.Warn("collector failed", "collector", col.Name(), "err", err)
        continue // nothing from a failed collector reaches the sample
    }
    if res.Host != nil {
        proto.Merge(sample, res.Host)
    }
    cores = append(cores, res.Cores...)
    // ... and so on per family
}
```

`proto.Merge` is correct here because every `HostSample` field is declared
`optional`, so presence is explicit and only set fields are copied. Two
collectors writing the same field would be a bug; merge order must not be load
bearing, and no collector pair shares a field today.

### Why this shape

- **Failure is all-or-nothing.** A collector contributes everything or nothing,
  which is what §5.1 rule 3 requires. The guarantee is structural rather than a
  discipline each of twenty collectors has to observe.
- **Collectors become pure readers.** Input is a filesystem tree, output is a
  value. Tests construct no shared fixture and cannot interfere with each other.
- **Merge policy lives in one place** — the agent — instead of being decided by
  whichever collector ran last.

Rejected: nesting the repeated messages inside `HostSample`. It is the smallest
diff and it costs the meaning of the row. `HostSample` would become "everything
from this scrape" rather than "one measurement of this host", and an unset field
would mean two different things. Also rejected: a second interface alongside the
existing one, which leaves the agent with two dispatch paths permanently and
makes "which kind is this collector" a per-collector fact to remember.

### Migration

All seven existing collectors are reworked, not merely re-signatured. The
compiler finds every call site; there is no silent partial migration. Their
tests change shape too — they assert on a returned `Result` rather than on a
fixture sample passed in.

## 2. Wire protocol

Each family gets its own typed message (§7.3), carried as repeated fields on
`IngestRequest` — **not** on `HostSample`, so the host row keeps its meaning:

```proto
message IngestRequest {
  // ... fields 1-5 unchanged ...
  repeated CpuCoreSample   cpu_cores  = 6;
  repeated DiskIoSample    disk_io    = 7;
  repeated SensorSample    sensors    = 8;
  repeated NetSample       net        = 9;
  repeated CollectorSample collectors = 10;
  repeated Event           events     = 11;
}
```

Later families are appended by the PR that introduces them. `IngestResponse`
field 4 remains `reserved` permanently — it carried the retired `interval_s`,
the scrape interval is a fixed 60s, and no cadence override is planned.

Each message carries its entity key as the schema defines it: `cpu` for a core,
`device` for a disk, `chip_name + label` for a sensor, `iface` for an interface.

### Every family message carries its own `ts_ms`

`IngestRequest.host_samples` is **repeated**: one request carries a batch, and
after an outage it carries the whole replayed ring. The per-family lists are
therefore flat lists spanning several scrapes, not the rows belonging to "the"
sample in the request. Each family message carries its own `ts_ms`, exactly as
`HostSample` does, and the hub stores on that — there is no positional
association with a host row and none should be inferred.

### The ring buffer holds a scrape, not a host sample

This is the consequence that reaches furthest. Today:

```go
type Entry struct {
    Seq    uint64
    Sample *netrav1.HostSample
}
```

A buffered entry must become the whole scrape — the host row plus every family
list produced at that timestamp — or an outage would replay host rows and
silently drop every per-core, per-disk and per-interface row measured during it.

Two knock-on effects, both in PR 1:

- **`capacityFor` changes meaning.** It sizes the ring in slots so that
  `capacity × interval` stays inside `NETRA_BUFFER_WINDOW`, and `maxBufferSlots`
  bounds memory. A slot is now much larger and, worse, *variable*: a 64-core
  host with twelve disks buffers far more per slot than a 1-core VPS. The slot
  count still bounds the window correctly, but no longer bounds memory the way
  it did. Either `maxBufferSlots` is re-derived from a realistic worst-case slot
  size, or the bound moves from slot count to approximate bytes. **Decide this
  in PR 1 rather than discovering it during an outage on the largest host.**
- **`Flush` builds the request from the entries**, appending each entry's family
  lists into the request's repeated fields, rather than only its host samples.

## 3. Schema

Seven families have no tables yet. Per project rule, **every schema change is
edited into `internal/hub/store/migrations/0001_init.sql` in place** — there is
no `0002`. Each family lands in the PR of the collector that needs it, and
`0001` stays re-runnable (`IF NOT EXISTS`, guarded `SELECT`s) so
`TestIntegrationMigrationIsRerunnableAgainstItsOwnSchema` stays green.

| Family | Tables | PR |
|---|---|---|
| Containers | container dimension + `container_samples` | 4 |
| Filesystems | `filesystems` + `filesystem_samples` | 4 |
| SMART | `devices` + `smart_attributes` | 5 |
| Addresses | `host_addresses` | 3 |
| systemd | `systemd_units` + `systemd_unit_events` | 4 |
| Packages | `host_packages` + `package_events` | 4 |
| Processes | `process_samples` (raw only, 48h, no aggregates) | 5 |

Each new hypertable moves the hard-coded policy counts in `rollup_test.go`,
which is the point of their being literals: a hypertable whose refresh or
retention policy is forgotten is a permanently silent failure, so adding one
must break the test until it is counted.

## 4. The thirteen collectors

Ordered by what they need from the host, because that is what the compose file
and the setup script have to grant.

### PR 1 — plumbing, proven with per-core CPU

The contract change, the typed wire messages, the ring-buffer entry change and
the `capacityFor` decision, plus one collector to prove it end to end.

Per-core CPU (`/proc/stat`), all cores always. Writes `cpu_core_samples`, which
already exists. Chosen as the proof because it is the simplest collector that
produces a list, so it exercises the new contract end to end — agent read, ring
buffer, wire message, hub store — without any new schema.

**Cardinality note:** ~800 series at target scale, the largest jump in 1C. Ingest
body size and hub write throughput are worth measuring here, before PR 2 adds
more families on top.

### PR 2 — no privileges, no dependencies

- **Disk I/O** (`/proc/diskstats`) — counter-reset handling; a reset detected
  agent-side, since only the agent holds the previous value.
- **Sensors** (`/sys/class/hwmon`) — identity is `chip_name + label`, **never
  `hwmonN`**, which is allocation-order dependent and unstable across reboots.
  Per-read deadline (`NETRA_SENSORS_TIMEOUT`); a preference list rather than
  hottest-wins.
- **mdraid** (sysfs) — writes `events`, not a hypertable. Array state is
  constant for weeks; only the transition is worth storing. `events` stays a
  plain Postgres table: no `create_hypertable`, no aggregates, no retention, and
  it must not move the counts in `rollup_test.go`.
- **Agent self-telemetry** — fill the remaining `agent_samples` columns:
  `uptime_s`, `rss_bytes`, `goroutines`, `post_failures_total`.
  `agent_samples.uptime_s` is the agent process; `host_samples.uptime_s` is the
  host. **They are different facts** and both get written in this PR, which is
  where conflating them is most likely.

### PR 3 — needs `network_mode: host`

- **Network** (`/proc/net/dev`) — filter `lo`, `veth*`, `docker0`, `br-*` and
  tunnels.
- **Addresses** (netlink) — delivers no samples; flips `metadata_hash`. The hub
  derives `scope` from the address, so classification is one implementation that
  can be corrected without redeploying agents. **Interface names are the join
  key** with `net_samples.iface`.

### PR 4 — needs a mount

- **Containers** (cgroup v2, Docker socket for metadata only) — **subtract
  `cache` and `inactive_file`** from raw memory usage, or every container
  reports its page cache as consumption. Identity is compose project + service,
  falling back to container name.
- **Filesystems** (`statfs` on marker dirs) — dedup by `st_dev`. Per-filesystem
  I/O needs the `st_dev` → block device mapping; where that mapping fails the
  value is **NULL, not zero**.
- **systemd** (D-Bus) — events, not samples; a numeric summary rides on
  `host_samples`. A unit's state is constant for days, so a 60s series of it is
  the same near-constant waste that keeps it out of §5.3.
- **Packages** (`dpkg`/`apk`) — parse on mtime change with a daily floor; rpm
  reports an unsupported-format capability rather than failing.

### PR 5 — privileged, opt-in

- **SMART** (`smartctl`, already in the image at a pinned version) — 1h
  interval. Reports a capability when device access is missing, rather than
  failing.
- **Processes** (`pid: host`) — `utime+stime` deltas keyed on
  `(pid, starttime)`, because a recycled PID otherwise produces a garbage spike.
  Aggregate by name; top-10-by-CPU ∪ top-10-by-memory.

## 5. Rules every collector follows

- **Fixture-based tests** with checked-in `/proc`, `/sys` and cgroup trees. No
  test may depend on the machine it runs on.
- **Reports into `collector_samples`**: duration, success, and an error code on
  failure. `error_code` is NULL when the collector last succeeded — a collector
  that failed once must not read as broken forever.
- **Availability via `CapabilityReporter`**, the existing optional interface.
  Capabilities ride the metadata hash, not the sample, because they change on
  the order of deployments rather than of scrapes.
- **Never prevents the agent from starting.** A collector that cannot run
  reports why and is skipped.
- **Interval:** every collector contributing to `host_samples` runs at the fixed
  scrape interval. Only collectors writing their own tables (SMART, packages)
  take a longer one — otherwise a skipped tick would leave `host_samples`
  columns NULL, which reads as "subsystem absent" rather than "not due yet".
- **Deploy artifacts change in the same PR.** Every collector that lands makes
  an existing `setup-agent.sh` mount meaningful.
  `deploy/agent/compose.yaml.example` carries a note listing which collectors
  are implemented; updating it later means shipping a lie about what works.

## 6. Risks

- **Cardinality.** Per-core CPU alone is ~800 series at target scale; containers
  and processes add more. PR 1 is the place to measure ingest size and hub write
  throughput, while one family is easy to isolate.
- **`0001` grows with every PR.** Seven families are added to a file that is
  already large. It stays re-runnable, but review burden rises and the
  already-migrated-hub gap widens. Accepted: the project is pre-release and the
  database is recreated rather than upgraded.
- **Merge collisions.** `proto.Merge` assumes no two collectors set the same
  `host_samples` field. True today. A test asserting it stays true is cheap
  insurance as the count grows.
- **Buffer memory is no longer bounded the way it was.** A ring slot holding a
  whole scrape is variable in size and much larger on a big host. `capacityFor`
  and `maxBufferSlots` were written when a slot was one narrow row. Resolved in
  PR 1 — see §2 — but it is the item most likely to be missed, because nothing
  fails until a long outage on the largest host in the fleet.
