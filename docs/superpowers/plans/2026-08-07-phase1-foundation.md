# netra Phase 1 Foundation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A netra agent collects CPU, memory and load from a Linux host and pushes them over HTTPS to a Go hub, which stores them in TimescaleDB — verifiable end to end.

**Architecture:** One Go module with two binaries (`hub/`, `agent/`) and a shared protobuf wire package. The agent scrapes on a timer into an in-memory ring buffer and POSTs protobuf batches; the hub authenticates by bearer token, decodes, and `COPY`s into a hypertable with continuous aggregates for rollups.

**Tech Stack:** Go 1.26, `pgx/v5`, protobuf via `buf` (run through `go run`, no local protoc), TimescaleDB (Postgres 17), GitHub Actions.

## Global Constraints

Copied verbatim from `docs/superpowers/specs/2026-08-07-netra-design.md`:

- Module path `github.com/trick77/netra`. Go 1.26.
- `CGO_ENABLED=0` for all shipped binaries. `CGO_ENABLED=1` **only** for `go test -race` in CI.
- **No SQLite anywhere in netra.** Postgres + TimescaleDB via `pgx` is the only datastore.
- All agent env vars are `NETRA_`-prefixed. Two are required: `NETRA_HUB_URL`, `NETRA_TOKEN`.
- Default scrape interval `60s`, expressed as a Go duration string — never a millisecond integer type.
- **Absent subsystems are `NULL`, never `0`** (spec §5.1 rule 3). Protobuf `optional` scalars everywhere a value may be absent; Go `*T` on the struct side; SQL `NULL` in the column.
- Continuous aggregate `start_offset` must exceed the agent ring-buffer window. Buffer `1h` → `start_offset = 6h` (spec §5.4).
- Raw retention (7 days) must exceed the aggregate refresh lag. Do not lower it.
- Metric tables reference integer surrogate ids, never strings.
- English only in code, comments and docs. `.yaml`, never `.yml`.
- TDD: failing test first, then minimal implementation.
- Conventional commits. Never commit to `master`; this plan runs on `feat/phase1-foundation`.
- Commit as `trick77@users.noreply.github.com`.

## Scope

**In this plan:** module layout, proto wire format, migrations for the dimension tables and the `host_samples` hypertable with rollups, token auth, ingest endpoint, health endpoint, agent config, `Collector` interface plus CPU/memory/load collectors, ring buffer, post loop with the metadata-hash handshake, end-to-end test, CI with coverage gates.

**Deferred to later plans:** the other thirteen collectors, the read API beyond `/api/health`, the remaining dimension and hypertable schema, `release.yaml`, `cleanup-images.yaml`, and the shipped compose files.

## File Structure

```
go.mod                                  module github.com/trick77/netra
Makefile                                test / build / proto / lint targets
buf.yaml, buf.gen.yaml                  proto codegen config
proto/netra/v1/ingest.proto             wire schema (single source of truth)
internal/gen/netrav1/                    generated protobuf code (committed)
internal/buildinfo/buildinfo.go          version, commit, go version

hub/cmd/netra/main.go                    hub entrypoint, wiring only
hub/internal/config/config.go            NETRA_* env -> Config
hub/internal/store/store.go              pgx pool lifecycle
hub/internal/store/migrate.go            embedded migration runner
hub/internal/store/migrations/*.sql      numbered, never edited once applied
hub/internal/store/ingest.go             COPY into host_samples, host_current upsert
hub/internal/auth/token.go               mint / hash / verify bearer tokens
hub/internal/httpapi/router.go           route table
hub/internal/httpapi/health.go           GET /api/health
hub/internal/httpapi/ingest.go           POST /api/agent/v1/ingest

agent/cmd/netra-agent/main.go            agent entrypoint, wiring only
agent/internal/config/config.go          NETRA_* env -> Config
agent/internal/collector/collector.go    Collector interface + registry
agent/internal/collector/cpu.go          /proc/stat
agent/internal/collector/memory.go       /proc/meminfo
agent/internal/collector/load.go         /proc/loadavg
agent/internal/collector/testdata/       fixture proc trees
agent/internal/buffer/ring.go            bounded overwrite-oldest buffer
agent/internal/client/client.go          POST loop, ack handling, metadata hash

hack/coverage-floors, coverage-gate.sh, patch-coverage.sh
.github/workflows/ci.yaml
```

Each file has one responsibility. Collectors are one file per source so a fixture test reads next to the parser it exercises.

---

## Task 1: Module scaffold, Makefile, buildinfo

**Files:**
- Create: `go.mod`, `Makefile`, `internal/buildinfo/buildinfo.go`, `internal/buildinfo/buildinfo_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `buildinfo.Version() string`, `buildinfo.Commit() string`, `buildinfo.GoVersion() string`. Set at link time via `-X`; falls back to `"dev"` / `"unknown"`.

- [ ] **Step 1: Write the failing test**

`internal/buildinfo/buildinfo_test.go`:

```go
package buildinfo

import (
	"runtime"
	"testing"
)

func TestDefaultsWhenNotStamped(t *testing.T) {
	if got := Version(); got != "dev" {
		t.Fatalf("Version() = %q, want %q", got, "dev")
	}
	if got := Commit(); got != "unknown" {
		t.Fatalf("Commit() = %q, want %q", got, "unknown")
	}
}

func TestGoVersionMatchesRuntime(t *testing.T) {
	if got := GoVersion(); got != runtime.Version() {
		t.Fatalf("GoVersion() = %q, want %q", got, runtime.Version())
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/buildinfo/ -run TestDefaults -v`
Expected: FAIL — package does not compile, `undefined: Version`.

- [ ] **Step 3: Create go.mod**

```bash
go mod init github.com/trick77/netra
go mod edit -go=1.26
```

- [ ] **Step 4: Implement buildinfo**

`internal/buildinfo/buildinfo.go`:

```go
// Package buildinfo exposes build-time identity. Values are stamped with
// -ldflags -X at release; the defaults are what a local `go build` produces.
package buildinfo

import "runtime"

var (
	version = "dev"
	commit  = "unknown"
)

// Version returns the semantic version, or "dev" for an unstamped build.
func Version() string { return version }

// Commit returns the short git SHA, or "unknown" for an unstamped build.
func Commit() string { return commit }

// GoVersion returns the Go toolchain version the binary was built with.
func GoVersion() string { return runtime.Version() }
```

- [ ] **Step 5: Run the tests and make sure they pass**

Run: `go test ./internal/buildinfo/ -v`
Expected: PASS, both tests.

- [ ] **Step 6: Write the Makefile**

`Makefile`:

```make
GO      ?= go
LDFLAGS := -s -w -X github.com/trick77/netra/internal/buildinfo.version=$(VERSION) \
                 -X github.com/trick77/netra/internal/buildinfo.commit=$(COMMIT)
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

.PHONY: test test-integration build build-hub build-agent proto fmt vet check

test:
	$(GO) test ./...

# Integration tests are skipped unless NETRA_TEST_DSN points at a TimescaleDB.
test-integration:
	NETRA_TEST_DSN=$${NETRA_TEST_DSN:-postgres://netra:netra@127.0.0.1:5432/netra_test} \
		$(GO) test ./hub/... -run Integration -v

build: build-hub build-agent

build-hub:
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o bin/netra ./hub/cmd/netra

build-agent:
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o bin/netra-agent ./agent/cmd/netra-agent

proto:
	$(GO) run github.com/bufbuild/buf/cmd/buf@v1.47.2 generate

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

check: vet test
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi
```

- [ ] **Step 7: Verify the Makefile works**

Run: `make check`
Expected: PASS, no output from the gofmt check.

- [ ] **Step 8: Commit**

```bash
git add go.mod Makefile internal/buildinfo/
git commit -m "feat: add module scaffold, Makefile and buildinfo"
```

---

## Task 2: Protobuf wire schema

**Files:**
- Create: `buf.yaml`, `buf.gen.yaml`, `proto/netra/v1/ingest.proto`, `internal/gen/netrav1/` (generated, committed), `internal/gen/netrav1/roundtrip_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: Go types `netrav1.IngestRequest`, `netrav1.IngestResponse`, `netrav1.Metadata`, `netrav1.HostSample`. All optional scalars are `*float64` / `*uint64` on the Go side. Both hub and agent import `github.com/trick77/netra/internal/gen/netrav1`.

- [ ] **Step 1: Write the proto schema**

`proto/netra/v1/ingest.proto`:

```proto
syntax = "proto3";

package netra.v1;

option go_package = "github.com/trick77/netra/internal/gen/netrav1;netrav1";

// IngestRequest is one POST body from an agent to the hub.
message IngestRequest {
  // Monotonic per-agent batch sequence. The hub acks the highest contiguous
  // value it accepted; the agent drops acked batches from its ring buffer.
  uint64 seq = 1;

  // 8-byte hash of the static metadata block. The hub compares it against the
  // stored value and asks for a resend on mismatch.
  bytes metadata_hash = 2;

  // Populated only when the hub set request_metadata on a previous response.
  Metadata metadata = 3;

  repeated HostSample host_samples = 4;

  // True when these samples are replayed from the ring buffer after an outage.
  // Backfilled ranges need continuous-aggregate invalidation.
  bool backfill = 5;
}

// Metadata holds facts that change rarely. Sent once, then only on change.
message Metadata {
  string agent_version = 1;
  string go_version = 2;
  string build_commit = 3;
  string hostname = 4;
  string kernel = 5;
  string os_name = 6;
  string arch = 7;
  string cpu_model = 8;
  uint32 cores = 9;
  uint32 threads = 10;
  uint64 memory_total = 11;
  string location = 12;
  string provider = 13;
  string facility = 14;
  string host_type = 15;
  string fingerprint = 16;
}

// HostSample is one scrape of host-level vitals.
//
// Every metric is optional: a field left unset means the subsystem is absent
// (no swap, no ZFS) and must reach the database as NULL, not 0.
message HostSample {
  int64 ts_ms = 1;

  optional double cpu_total = 2;
  optional double cpu_user = 3;
  optional double cpu_system = 4;
  optional double cpu_iowait = 5;
  optional double cpu_steal = 6;
  optional double cpu_idle = 7;

  optional uint64 mem_total = 8;
  optional uint64 mem_used = 9;
  optional uint64 mem_available = 10;
  optional uint64 mem_buffcache = 11;
  optional uint64 mem_zfs_arc = 12;
  optional uint64 swap_total = 13;
  optional uint64 swap_used = 14;

  optional double load1 = 15;
  optional double load5 = 16;
  optional double load15 = 17;

  optional uint64 uptime_s = 18;
}

// IngestResponse carries everything the hub would push over a socket.
message IngestResponse {
  uint64 ack_seq = 1;
  bool request_metadata = 2;
  uint32 retry_after_s = 3;
  uint32 interval_s = 4;
}
```

- [ ] **Step 2: Write the buf configuration**

`buf.yaml`:

```yaml
version: v2
modules:
  - path: proto
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
```

`buf.gen.yaml`:

```yaml
version: v2
managed:
  enabled: false
plugins:
  - remote: buf.build/protocolbuffers/go:v1.36.4
    out: internal/gen
    opt: paths=source_relative
```

The `paths=source_relative` option plus the `go_package` option puts the generated file at `internal/gen/netra/v1/ingest.pb.go` with package name `netrav1`.

- [ ] **Step 3: Generate**

Run: `make proto`
Expected: `internal/gen/netra/v1/ingest.pb.go` created. `buf` compiles the schema itself — no local `protoc` is needed.

- [ ] **Step 4: Add the protobuf runtime dependency**

```bash
go get google.golang.org/protobuf@v1.36.4
go mod tidy
```

- [ ] **Step 5: Write the round-trip test**

`internal/gen/netra/v1/roundtrip_test.go`:

```go
package netrav1_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// The absent-vs-zero distinction is the whole reason these fields are
// optional. Losing it silently turns "this host has no swap" into
// "this host has 0 bytes of swap used".
func TestOptionalFieldsPreserveAbsentVersusZero(t *testing.T) {
	in := &netrav1.HostSample{
		TsMs:     1_700_000_000_000,
		SwapUsed: proto.Uint64(0), // present, and zero
		// MemZfsArc deliberately left unset: absent
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.HostSample
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out.SwapUsed == nil {
		t.Fatal("SwapUsed round-tripped as absent, want present with value 0")
	}
	if *out.SwapUsed != 0 {
		t.Fatalf("SwapUsed = %d, want 0", *out.SwapUsed)
	}
	if out.MemZfsArc != nil {
		t.Fatalf("MemZfsArc = %d, want absent", *out.MemZfsArc)
	}
}

func TestIngestRequestRoundTrip(t *testing.T) {
	in := &netrav1.IngestRequest{
		Seq:          42,
		MetadataHash: []byte{1, 2, 3, 4, 5, 6, 7, 8},
		Backfill:     true,
		HostSamples: []*netrav1.HostSample{
			{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(12.5)},
		},
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.IngestRequest
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out.Seq != 42 {
		t.Fatalf("Seq = %d, want 42", out.Seq)
	}
	if !out.Backfill {
		t.Fatal("Backfill = false, want true")
	}
	if len(out.HostSamples) != 1 {
		t.Fatalf("len(HostSamples) = %d, want 1", len(out.HostSamples))
	}
	if got := out.HostSamples[0].GetCpuTotal(); got != 12.5 {
		t.Fatalf("CpuTotal = %v, want 12.5", got)
	}
}
```

- [ ] **Step 6: Run the tests and make sure they pass**

Run: `go test ./internal/gen/... -v`
Expected: PASS, both tests.

- [ ] **Step 7: Commit**

```bash
git add buf.yaml buf.gen.yaml proto/ internal/gen/ go.mod go.sum
git commit -m "feat: add protobuf ingest wire schema"
```

---

## Task 3: Hub configuration

**Files:**
- Create: `hub/internal/config/config.go`, `hub/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config` with fields `ListenAddr string`, `DatabaseDSN string`, `AdminToken string`, `LogLevel string`. Constructor `config.Load() (Config, error)`.

- [ ] **Step 1: Write the failing test**

`hub/internal/config/config_test.go`:

```go
package config

import "testing"

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("NETRA_DB_DSN", "postgres://localhost/netra")
	t.Setenv("NETRA_ADMIN_TOKEN", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Fatalf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

func TestLoadRequiresDSN(t *testing.T) {
	t.Setenv("NETRA_DB_DSN", "")
	t.Setenv("NETRA_ADMIN_TOKEN", "secret")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no NETRA_DB_DSN, want error")
	}
}

func TestLoadRequiresAdminToken(t *testing.T) {
	t.Setenv("NETRA_DB_DSN", "postgres://localhost/netra")
	t.Setenv("NETRA_ADMIN_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no NETRA_ADMIN_TOKEN, want error")
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./hub/internal/config/ -v`
Expected: FAIL — `undefined: Load`.

- [ ] **Step 3: Implement the config loader**

`hub/internal/config/config.go`:

```go
// Package config turns NETRA_* environment variables into a hub Config.
package config

import (
	"fmt"
	"os"
)

// Config holds every hub setting. There is no config file: env only, so a
// container is configured entirely by its compose file.
type Config struct {
	ListenAddr  string
	DatabaseDSN string
	AdminToken  string
	LogLevel    string
}

// Load reads the environment and applies defaults. It fails rather than
// starting with no database or an unauthenticated admin API.
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:  envOr("NETRA_LISTEN_ADDR", ":8080"),
		DatabaseDSN: os.Getenv("NETRA_DB_DSN"),
		AdminToken:  os.Getenv("NETRA_ADMIN_TOKEN"),
		LogLevel:    envOr("NETRA_LOG_LEVEL", "info"),
	}

	if cfg.DatabaseDSN == "" {
		return Config{}, fmt.Errorf("NETRA_DB_DSN is required")
	}
	if cfg.AdminToken == "" {
		return Config{}, fmt.Errorf("NETRA_ADMIN_TOKEN is required")
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./hub/internal/config/ -v`
Expected: PASS, three tests.

- [ ] **Step 5: Commit**

```bash
git add hub/internal/config/
git commit -m "feat: add hub configuration loader"
```

---

## Task 4: Migration runner and dimension tables

**Files:**
- Create: `hub/internal/store/store.go`, `hub/internal/store/migrate.go`, `hub/internal/store/migrate_test.go`, `hub/internal/store/testing.go`, `hub/internal/store/migrations/0001_dimensions.sql`

**Interfaces:**
- Consumes: nothing.
- Produces: `store.Open(ctx, dsn) (*Store, error)`, `(*Store).Close()`, `(*Store).Pool() *pgxpool.Pool`, `(*Store).Migrate(ctx) error`. Test helper `store.OpenTest(t) *Store` which skips when `NETRA_TEST_DSN` is unset.

- [ ] **Step 1: Add the pgx dependency**

```bash
go get github.com/jackc/pgx/v5@v5.7.2
go mod tidy
```

- [ ] **Step 2: Write the failing integration test**

`hub/internal/store/migrate_test.go`:

```go
package store_test

import (
	"context"
	"testing"

	"github.com/trick77/netra/hub/internal/store"
)

func TestIntegrationMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	// Running again must be a no-op, not an error: the hub migrates on every
	// start, so a restart with no new migrations has to succeed.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var n int
	err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = 'hosts'`).Scan(&n)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("hosts table count = %d, want 1", n)
	}
}
```

- [ ] **Step 3: Run it to make sure it fails**

Run: `go test ./hub/internal/store/ -run Integration -v`
Expected: FAIL — `undefined: store.OpenTest`.

- [ ] **Step 4: Write the store and test helper**

`hub/internal/store/store.go`:

```go
// Package store owns the Postgres/TimescaleDB connection and schema.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps the connection pool. It is the only place that knows a DSN.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects and verifies the database is reachable.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Pool exposes the pool for queries in sibling packages.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases all connections.
func (s *Store) Close() { s.pool.Close() }
```

`hub/internal/store/testing.go`:

```go
package store

import (
	"context"
	"os"
	"testing"
)

// OpenTest connects to the database named by NETRA_TEST_DSN and drops the
// public schema so each test starts from nothing.
//
// Integration tests run against real TimescaleDB rather than a mock: the
// continuous aggregates and their start_offset behaviour are the risky part
// of this schema and a fake would verify nothing about them.
func OpenTest(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("NETRA_TEST_DSN")
	if dsn == "" {
		t.Skip("NETRA_TEST_DSN not set; skipping integration test")
	}

	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}

	if _, err := s.pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		s.Close()
		t.Fatalf("reset schema: %v", err)
	}

	t.Cleanup(s.Close)
	return s
}
```

- [ ] **Step 5: Write the migration runner**

`hub/internal/store/migrate.go`:

```go
package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// noTxMarker opts a migration out of the surrounding transaction.
//
// TimescaleDB refuses to create a continuous aggregate inside a transaction
// block, so those migrations carry this marker on their first line.
const noTxMarker = "-- netra:no-transaction"

// Migrate applies every pending migration in filename order, exactly once.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`,
			name).Scan(&applied); err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
		if applied {
			continue
		}

		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		if err := s.applyMigration(ctx, name, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}

	return nil
}

func (s *Store) applyMigration(ctx context.Context, name, body string) error {
	if strings.HasPrefix(strings.TrimSpace(body), noTxMarker) {
		// Outside a transaction: the statements are individually atomic and a
		// partial failure leaves the migration unrecorded, so it is retried.
		if _, err := s.pool.Exec(ctx, body); err != nil {
			return err
		}
		_, err := s.pool.Exec(ctx,
			`INSERT INTO schema_migrations (name) VALUES ($1)`, name)
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, body); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
```

- [ ] **Step 6: Write the first migration**

`hub/internal/store/migrations/0001_dimensions.sql`:

```sql
CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE providers (
    id   INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE sites (
    id           INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider_id  INTEGER REFERENCES providers (id),
    name         TEXT NOT NULL,
    facility     TEXT,
    address      TEXT,
    latitude     DOUBLE PRECISION,
    longitude    DOUBLE PRECISION,
    country_code TEXT,
    timezone     TEXT,
    UNIQUE (provider_id, name)
);

CREATE TABLE hosts (
    id            INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    site_id       INTEGER REFERENCES sites (id),
    hostname      TEXT,
    fingerprint   TEXT,
    host_type     TEXT,
    agent_version TEXT,
    go_version    TEXT,
    build_commit  TEXT,
    kernel        TEXT,
    os_name       TEXT,
    arch          TEXT,
    cpu_model     TEXT,
    cores         INTEGER,
    threads       INTEGER,
    memory_total  BIGINT,
    -- Stored as 8 raw bytes rather than an integer: the wire value is an
    -- unsigned 64-bit hash and Postgres has no unsigned integer type.
    metadata_hash BYTEA,
    capabilities  JSONB NOT NULL DEFAULT '{}'::jsonb,
    latitude      DOUBLE PRECISION,
    longitude     DOUBLE PRECISION,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tokens (
    id           INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    host_id      INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    token_hash   BYTEA NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);

CREATE TABLE host_current (
    host_id   INTEGER PRIMARY KEY REFERENCES hosts (id) ON DELETE CASCADE,
    last_seen TIMESTAMPTZ,
    cpu_total DOUBLE PRECISION,
    mem_used  BIGINT,
    mem_total BIGINT,
    uptime_s  BIGINT
);
```

- [ ] **Step 7: Start a local TimescaleDB and run the test**

```bash
docker run -d --name netra-test-db \
  -e POSTGRES_USER=netra -e POSTGRES_PASSWORD=netra -e POSTGRES_DB=netra_test \
  -p 5432:5432 timescale/timescaledb:latest-pg17

NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test \
  go test ./hub/internal/store/ -run Integration -v
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add hub/internal/store/ go.mod go.sum
git commit -m "feat: add migration runner and dimension tables"
```

---

## Task 5: host_samples hypertable, rollups and retention

**Files:**
- Create: `hub/internal/store/migrations/0002_host_samples.sql`, `hub/internal/store/rollup_test.go`

**Interfaces:**
- Consumes: `store.OpenTest`, `(*Store).Migrate` from Task 4.
- Produces: tables `host_samples`, continuous aggregates `host_samples_5m` and `host_samples_1h`.

- [ ] **Step 1: Write the failing test**

`hub/internal/store/rollup_test.go`:

```go
package store_test

import (
	"context"
	"testing"

	"github.com/trick77/netra/hub/internal/store"
)

func TestIntegrationHostSamplesIsHypertable(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.hypertables
		 WHERE hypertable_name = 'host_samples'`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("host_samples hypertable count = %d, want 1", n)
	}
}

// start_offset must exceed the agent ring-buffer window, or data replayed
// after an outage is recorded as invalid but never re-materialised, leaving
// the rollup permanently wrong. Buffer is 1h, so the floor is 1h.
func TestIntegrationRefreshPolicyStartOffsetExceedsBufferWindow(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rows, err := s.Pool().Query(ctx,
		`SELECT config ->> 'start_offset'
		   FROM timescaledb_information.jobs
		  WHERE proc_name = 'policy_refresh_continuous_aggregate'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var startOffset string
		if err := rows.Scan(&startOffset); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++

		var greater bool
		if err := s.Pool().QueryRow(ctx,
			`SELECT $1::interval > interval '1 hour'`, startOffset).Scan(&greater); err != nil {
			t.Fatalf("compare: %v", err)
		}
		if !greater {
			t.Fatalf("start_offset = %s, want greater than the 1h buffer window", startOffset)
		}
	}
	if seen != 2 {
		t.Fatalf("refresh policies found = %d, want 2 (5m and 1h aggregates)", seen)
	}
}

func TestIntegrationRawRetentionExceedsRefreshLag(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var dropAfter string
	if err := s.Pool().QueryRow(ctx,
		`SELECT config ->> 'drop_after'
		   FROM timescaledb_information.jobs
		  WHERE proc_name = 'policy_retention'
		    AND hypertable_name = 'host_samples'`).Scan(&dropAfter); err != nil {
		t.Fatalf("query: %v", err)
	}

	var ok bool
	if err := s.Pool().QueryRow(ctx,
		`SELECT $1::interval > interval '6 hours'`, dropAfter).Scan(&ok); err != nil {
		t.Fatalf("compare: %v", err)
	}
	if !ok {
		t.Fatalf("raw drop_after = %s, want greater than the 6h start_offset", dropAfter)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test go test ./hub/internal/store/ -run Integration -v`
Expected: FAIL — `host_samples hypertable count = 0, want 1`.

- [ ] **Step 3: Write the migration**

`hub/internal/store/migrations/0002_host_samples.sql`:

```sql
-- netra:no-transaction
-- TimescaleDB refuses to create a continuous aggregate inside a transaction
-- block, so this whole migration runs outside one.

CREATE TABLE host_samples (
    host_id       INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    ts            TIMESTAMPTZ NOT NULL,
    cpu_total     DOUBLE PRECISION,
    cpu_user      DOUBLE PRECISION,
    cpu_system    DOUBLE PRECISION,
    cpu_iowait    DOUBLE PRECISION,
    cpu_steal     DOUBLE PRECISION,
    cpu_idle      DOUBLE PRECISION,
    mem_total     BIGINT,
    mem_used      BIGINT,
    mem_available BIGINT,
    mem_buffcache BIGINT,
    mem_zfs_arc   BIGINT,
    swap_total    BIGINT,
    swap_used     BIGINT,
    load1         DOUBLE PRECISION,
    load5         DOUBLE PRECISION,
    load15        DOUBLE PRECISION,
    uptime_s      BIGINT,
    -- Natural key. Replayed batches collide here and are discarded by
    -- ON CONFLICT DO NOTHING, which is what makes at-least-once safe.
    PRIMARY KEY (host_id, ts)
);

SELECT create_hypertable('host_samples', by_range('ts'));

CREATE MATERIALIZED VIEW host_samples_5m
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '5 minutes', ts) AS bucket,
       avg(cpu_total)  AS cpu_total_avg,
       max(cpu_total)  AS cpu_total_max,
       avg(mem_used)   AS mem_used_avg,
       max(mem_used)   AS mem_used_max,
       avg(swap_used)  AS swap_used_avg,
       avg(load1)      AS load1_avg,
       max(load1)      AS load1_max,
       last(uptime_s, ts) AS uptime_s
  FROM host_samples
 GROUP BY host_id, bucket
WITH NO DATA;

CREATE MATERIALIZED VIEW host_samples_1h
    WITH (timescaledb.continuous) AS
SELECT host_id,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       avg(cpu_total_avg) AS cpu_total_avg,
       max(cpu_total_max) AS cpu_total_max,
       avg(mem_used_avg)  AS mem_used_avg,
       max(mem_used_max)  AS mem_used_max,
       avg(swap_used_avg) AS swap_used_avg,
       avg(load1_avg)     AS load1_avg,
       max(load1_max)     AS load1_max,
       last(uptime_s, bucket) AS uptime_s
  FROM host_samples_5m
 GROUP BY host_id, time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

-- start_offset (6h) must stay above the agent ring-buffer window (1h).
-- Timescale cuts invalidations against the refresh window, so anything
-- backfilled older than start_offset is never re-materialised.
SELECT add_continuous_aggregate_policy('host_samples_5m',
    start_offset      => INTERVAL '6 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes');

SELECT add_continuous_aggregate_policy('host_samples_1h',
    start_offset      => INTERVAL '12 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes');

-- Raw retention must exceed the refresh lag, or chunks are dropped before
-- being materialised into the 5m tier.
SELECT add_retention_policy('host_samples',    INTERVAL '7 days');
SELECT add_retention_policy('host_samples_5m', INTERVAL '30 days');
SELECT add_retention_policy('host_samples_1h', INTERVAL '90 days');
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test go test ./hub/internal/store/ -run Integration -v`
Expected: PASS, four tests.

- [ ] **Step 5: Commit**

```bash
git add hub/internal/store/
git commit -m "feat: add host_samples hypertable with rollups and retention"
```

---

## Task 6: Token authentication

**Files:**
- Create: `hub/internal/auth/token.go`, `hub/internal/auth/token_test.go`

**Interfaces:**
- Consumes: `*store.Store` from Task 4.
- Produces: `auth.Mint() (plain string, hash []byte, err error)`, `auth.Hash(plain string) []byte`, `auth.Authenticator` with `Authenticate(ctx, bearer string) (hostID int32, err error)` and sentinel `auth.ErrUnauthorized`.

- [ ] **Step 1: Write the failing unit test**

`hub/internal/auth/token_test.go`:

```go
package auth_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/trick77/netra/hub/internal/auth"
)

func TestMintProducesPrefixedTokenAndMatchingHash(t *testing.T) {
	plain, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !strings.HasPrefix(plain, "nta_") {
		t.Fatalf("token = %q, want the nta_ prefix", plain)
	}
	if len(hash) != 32 {
		t.Fatalf("len(hash) = %d, want 32", len(hash))
	}
	if !bytes.Equal(hash, auth.Hash(plain)) {
		t.Fatal("Hash(plain) does not match the hash returned by Mint")
	}
}

func TestMintIsUnique(t *testing.T) {
	a, _, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	b, _, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if a == b {
		t.Fatal("two Mint calls produced the same token")
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./hub/internal/auth/ -v`
Expected: FAIL — `undefined: auth.Mint`.

- [ ] **Step 3: Implement token minting and hashing**

`hub/internal/auth/token.go`:

```go
// Package auth mints and verifies the bearer tokens agents use.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenPrefix marks a netra agent token so a leaked one is identifiable.
const TokenPrefix = "nta_"

// ErrUnauthorized is returned for any authentication failure. It is
// deliberately opaque: the caller must not learn whether the host exists.
var ErrUnauthorized = errors.New("unauthorized")

// Mint generates a new agent token, returning the plaintext (shown to the
// operator exactly once) and the hash to store.
func Mint() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("read random: %w", err)
	}
	plain := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return plain, Hash(plain), nil
}

// Hash reduces a token to the value stored in the database. Tokens are high
// entropy random strings, so a plain SHA-256 is appropriate — a slow KDF
// would only add per-request cost against an unguessable secret.
func Hash(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

// Authenticator resolves bearer tokens to host ids.
type Authenticator struct {
	pool *pgxpool.Pool
}

// NewAuthenticator builds an Authenticator over the given pool.
func NewAuthenticator(pool *pgxpool.Pool) *Authenticator {
	return &Authenticator{pool: pool}
}

// Authenticate returns the host id owning the token, or ErrUnauthorized.
func (a *Authenticator) Authenticate(ctx context.Context, bearer string) (int32, error) {
	if bearer == "" {
		return 0, ErrUnauthorized
	}

	want := Hash(bearer)

	var (
		hostID int32
		stored []byte
	)
	err := a.pool.QueryRow(ctx,
		`SELECT host_id, token_hash FROM tokens WHERE token_hash = $1`,
		want).Scan(&hostID, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrUnauthorized
	}
	if err != nil {
		return 0, fmt.Errorf("lookup token: %w", err)
	}

	// The lookup already matched on equality; the constant-time compare guards
	// against a future change that widens the query.
	if subtle.ConstantTimeCompare(stored, want) != 1 {
		return 0, ErrUnauthorized
	}

	if _, err := a.pool.Exec(ctx,
		`UPDATE tokens SET last_used_at = now() WHERE token_hash = $1`, want); err != nil {
		return 0, fmt.Errorf("touch token: %w", err)
	}

	return hostID, nil
}
```

- [ ] **Step 4: Run the unit tests and make sure they pass**

Run: `go test ./hub/internal/auth/ -v`
Expected: PASS, two tests.

- [ ] **Step 5: Write the failing integration test**

Append to `hub/internal/auth/token_test.go`:

```go
func TestIntegrationAuthenticateResolvesHostID(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('h1') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	plain, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID, hash); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	a := auth.NewAuthenticator(s.Pool())

	got, err := a.Authenticate(ctx, plain)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != hostID {
		t.Fatalf("host id = %d, want %d", got, hostID)
	}

	if _, err := a.Authenticate(ctx, "nta_wrong"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authenticate(wrong) error = %v, want ErrUnauthorized", err)
	}
	if _, err := a.Authenticate(ctx, ""); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authenticate(empty) error = %v, want ErrUnauthorized", err)
	}
}
```

Add these imports to the test file: `"context"`, `"errors"`, `"github.com/trick77/netra/hub/internal/store"`.

- [ ] **Step 6: Run the integration test and make sure it passes**

Run: `NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test go test ./hub/internal/auth/ -v`
Expected: PASS, three tests.

- [ ] **Step 7: Commit**

```bash
git add hub/internal/auth/
git commit -m "feat: add agent bearer token minting and verification"
```

---

## Task 7: Sample insertion

**Files:**
- Create: `hub/internal/store/ingest.go`, `hub/internal/store/ingest_test.go`

**Interfaces:**
- Consumes: `*store.Store` (Task 4), `netrav1.HostSample` (Task 2).
- Produces: `(*Store).InsertHostSamples(ctx, hostID int32, samples []*netrav1.HostSample) (int64, error)` returning the number of rows inserted, and `(*Store).UpsertHostCurrent(ctx, hostID int32, s *netrav1.HostSample) error`.

- [ ] **Step 1: Write the failing test**

`hub/internal/store/ingest_test.go`:

```go
package store_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
	"github.com/trick77/netra/hub/internal/store"
)

func seedHost(t *testing.T, s *store.Store) int32 {
	t.Helper()
	var id int32
	if err := s.Pool().QueryRow(context.Background(),
		`INSERT INTO hosts (hostname) VALUES ('h1') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	return id
}

func TestIntegrationInsertHostSamplesPreservesNulls(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	samples := []*netrav1.HostSample{{
		TsMs:     1_700_000_000_000,
		CpuTotal: proto.Float64(12.5),
		SwapUsed: proto.Uint64(0), // present and zero
		// MemZfsArc unset: this host has no ZFS
	}}

	n, err := s.InsertHostSamples(ctx, hostID, samples)
	if err != nil {
		t.Fatalf("InsertHostSamples: %v", err)
	}
	if n != 1 {
		t.Fatalf("inserted = %d, want 1", n)
	}

	var (
		swapUsed  *int64
		memZfsArc *int64
	)
	if err := s.Pool().QueryRow(ctx,
		`SELECT swap_used, mem_zfs_arc FROM host_samples WHERE host_id = $1`,
		hostID).Scan(&swapUsed, &memZfsArc); err != nil {
		t.Fatalf("query: %v", err)
	}

	if swapUsed == nil {
		t.Fatal("swap_used is NULL, want 0 — a present zero must not become NULL")
	}
	if *swapUsed != 0 {
		t.Fatalf("swap_used = %d, want 0", *swapUsed)
	}
	if memZfsArc != nil {
		t.Fatalf("mem_zfs_arc = %d, want NULL — this host has no ZFS", *memZfsArc)
	}
}

// Replay after an outage re-sends batches the hub may already hold. The
// natural key plus ON CONFLICT DO NOTHING is what makes that harmless.
func TestIntegrationInsertHostSamplesIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	samples := []*netrav1.HostSample{
		{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(1)},
		{TsMs: 1_700_000_060_000, CpuTotal: proto.Float64(2)},
	}

	if _, err := s.InsertHostSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := s.InsertHostSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("replayed insert: %v", err)
	}

	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples WHERE host_id = $1`, hostID).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 2 {
		t.Fatalf("row count = %d, want 2 after a replay", count)
	}
}

func TestIntegrationUpsertHostCurrent(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	first := &netrav1.HostSample{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(10)}
	second := &netrav1.HostSample{TsMs: 1_700_000_060_000, CpuTotal: proto.Float64(20)}

	if err := s.UpsertHostCurrent(ctx, hostID, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := s.UpsertHostCurrent(ctx, hostID, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var cpu float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT cpu_total FROM host_current WHERE host_id = $1`, hostID).Scan(&cpu); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cpu != 20 {
		t.Fatalf("cpu_total = %v, want 20 — the later sample must win", cpu)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test go test ./hub/internal/store/ -run Integration -v`
Expected: FAIL — `undefined: InsertHostSamples`.

- [ ] **Step 3: Implement insertion**

`hub/internal/store/ingest.go`:

```go
package store

import (
	"context"
	"fmt"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// InsertHostSamples writes a batch and returns the number of rows stored.
//
// Rows already present are skipped rather than updated: an agent replaying its
// ring buffer after an outage re-sends batches the hub may already hold, and
// the first write is authoritative.
//
// This uses a multi-row INSERT rather than COPY because COPY cannot express
// ON CONFLICT. Batches are one scrape deep, so the row count per statement is
// small and the difference does not matter here; bulk backfill paths in later
// plans may revisit this.
func (s *Store) InsertHostSamples(ctx context.Context, hostID int32, samples []*netrav1.HostSample) (int64, error) {
	if len(samples) == 0 {
		return 0, nil
	}

	const stmt = `
		INSERT INTO host_samples (
			host_id, ts,
			cpu_total, cpu_user, cpu_system, cpu_iowait, cpu_steal, cpu_idle,
			mem_total, mem_used, mem_available, mem_buffcache, mem_zfs_arc,
			swap_total, swap_used,
			load1, load5, load15, uptime_s
		) VALUES (
			$1, $2,
			$3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13,
			$14, $15,
			$16, $17, $18, $19
		)
		ON CONFLICT (host_id, ts) DO NOTHING`

	batch := &pgxBatch{}
	for _, m := range samples {
		batch.Queue(stmt,
			hostID, time.UnixMilli(m.GetTsMs()).UTC(),
			f64(m.CpuTotal), f64(m.CpuUser), f64(m.CpuSystem),
			f64(m.CpuIowait), f64(m.CpuSteal), f64(m.CpuIdle),
			u64(m.MemTotal), u64(m.MemUsed), u64(m.MemAvailable),
			u64(m.MemBuffcache), u64(m.MemZfsArc),
			u64(m.SwapTotal), u64(m.SwapUsed),
			f64(m.Load1), f64(m.Load5), f64(m.Load15), u64(m.UptimeS),
		)
	}

	results := s.pool.SendBatch(ctx, batch.b)
	defer func() { _ = results.Close() }()

	var inserted int64
	for range samples {
		tag, err := results.Exec()
		if err != nil {
			return 0, fmt.Errorf("insert host sample: %w", err)
		}
		inserted += tag.RowsAffected()
	}

	return inserted, nil
}

// UpsertHostCurrent keeps the denormalised latest snapshot fresh so the host
// list never has to touch a hypertable.
func (s *Store) UpsertHostCurrent(ctx context.Context, hostID int32, m *netrav1.HostSample) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO host_current (host_id, last_seen, cpu_total, mem_used, mem_total, uptime_s)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (host_id) DO UPDATE SET
			last_seen = EXCLUDED.last_seen,
			cpu_total = EXCLUDED.cpu_total,
			mem_used  = EXCLUDED.mem_used,
			mem_total = EXCLUDED.mem_total,
			uptime_s  = EXCLUDED.uptime_s
		WHERE host_current.last_seen IS NULL
		   OR host_current.last_seen <= EXCLUDED.last_seen`,
		hostID, time.UnixMilli(m.GetTsMs()).UTC(),
		f64(m.CpuTotal), u64(m.MemUsed), u64(m.MemTotal), u64(m.UptimeS))
	if err != nil {
		return fmt.Errorf("upsert host_current: %w", err)
	}
	return nil
}

// f64 and u64 map an unset protobuf optional to a SQL NULL. Returning the
// pointer directly would work for float64 but not for the uint64 -> int64
// column mapping, so both are explicit.
func f64(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func u64(p *uint64) any {
	if p == nil {
		return nil
	}
	return int64(*p)
}
```

- [ ] **Step 4: Add the small batch wrapper**

Append to `hub/internal/store/ingest.go`:

```go
// pgxBatch is a thin wrapper so the batch type does not leak into the
// function signature above.
type pgxBatch struct{ b *pgxpoolBatch }

func (p *pgxBatch) Queue(sql string, args ...any) {
	if p.b == nil {
		p.b = &pgxpoolBatch{}
	}
	p.b.Queue(sql, args...)
}
```

Replace `pgxpoolBatch` with the real type by adding this import and alias at the top of the file:

```go
import (
	"github.com/jackc/pgx/v5"
)

type pgxpoolBatch = pgx.Batch
```

- [ ] **Step 5: Run the tests and make sure they pass**

Run: `NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test go test ./hub/internal/store/ -run Integration -v`
Expected: PASS, seven tests.

- [ ] **Step 6: Commit**

```bash
git add hub/internal/store/
git commit -m "feat: add host sample insertion with null preservation"
```

---

## Task 8: Ingest endpoint

**Files:**
- Create: `hub/internal/httpapi/ingest.go`, `hub/internal/httpapi/ingest_test.go`

**Interfaces:**
- Consumes: `auth.Authenticator` (Task 6), `*store.Store` (Tasks 4, 7), `netrav1` (Task 2).
- Produces: `httpapi.IngestHandler` with `ServeHTTP`, constructed by `httpapi.NewIngestHandler(a *auth.Authenticator, s *store.Store, interval time.Duration)`.

- [ ] **Step 1: Write the failing test**

`hub/internal/httpapi/ingest_test.go`:

```go
package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/hub/internal/auth"
	"github.com/trick77/netra/hub/internal/httpapi"
	"github.com/trick77/netra/hub/internal/store"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func newFixture(t *testing.T) (*httptest.Server, string, *store.Store) {
	t.Helper()
	ctx := context.Background()

	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('h1') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	plain, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID, hash); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	h := httpapi.NewIngestHandler(auth.NewAuthenticator(s.Pool()), s, time.Minute)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return srv, plain, s
}

func post(t *testing.T, srv *httptest.Server, token string, req *netrav1.IngestRequest) *http.Response {
	t.Helper()

	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := srv.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestIntegrationIngestRejectsMissingToken(t *testing.T) {
	srv, _, _ := newFixture(t)
	resp := post(t, srv, "", &netrav1.IngestRequest{Seq: 1})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestIntegrationIngestStoresSamplesAndAcks(t *testing.T) {
	srv, token, s := newFixture(t)

	req := &netrav1.IngestRequest{
		Seq:          7,
		MetadataHash: []byte{1, 2, 3, 4, 5, 6, 7, 8},
		HostSamples: []*netrav1.HostSample{
			{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(33)},
		},
	}

	resp := post(t, srv, token, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out netrav1.IngestResponse
	decodeBody(t, resp, &out)

	if out.AckSeq != 7 {
		t.Fatalf("AckSeq = %d, want 7", out.AckSeq)
	}
	// The hub has never seen this host's metadata, so it must ask for it.
	if !out.RequestMetadata {
		t.Fatal("RequestMetadata = false, want true on first contact")
	}
	if out.IntervalS != 60 {
		t.Fatalf("IntervalS = %d, want 60", out.IntervalS)
	}

	var count int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM host_samples`).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("stored rows = %d, want 1", count)
	}
}

func TestIntegrationIngestStopsAskingOnceMetadataMatches(t *testing.T) {
	srv, token, _ := newFixture(t)
	hash := []byte{9, 9, 9, 9, 9, 9, 9, 9}

	first := post(t, srv, token, &netrav1.IngestRequest{
		Seq:          1,
		MetadataHash: hash,
		Metadata:     &netrav1.Metadata{Hostname: "h1", AgentVersion: "0.1.0"},
	})
	var out netrav1.IngestResponse
	decodeBody(t, first, &out)
	if out.RequestMetadata {
		t.Fatal("RequestMetadata = true, want false when metadata was supplied")
	}

	second := post(t, srv, token, &netrav1.IngestRequest{Seq: 2, MetadataHash: hash})
	decodeBody(t, second, &out)
	if out.RequestMetadata {
		t.Fatal("RequestMetadata = true, want false when the hash still matches")
	}
}
```

- [ ] **Step 2: Add the response decode helper**

Append to `hub/internal/httpapi/ingest_test.go`:

```go
func decodeBody(t *testing.T, resp *http.Response, out proto.Message) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := proto.Unmarshal(raw, out); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
}
```

Add `"io"` to the test file imports.

- [ ] **Step 3: Run it to make sure it fails**

Run: `NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test go test ./hub/internal/httpapi/ -v`
Expected: FAIL — `undefined: httpapi.NewIngestHandler`.

- [ ] **Step 4: Implement the handler**

`hub/internal/httpapi/ingest.go`:

```go
// Package httpapi holds the hub's HTTP surface.
package httpapi

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/hub/internal/auth"
	"github.com/trick77/netra/hub/internal/store"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// maxBodyBytes caps a single ingest POST. A 60s batch of host samples is a
// few hundred bytes; this is generous headroom that still bounds memory.
const maxBodyBytes = 4 << 20

// IngestHandler accepts agent metric batches.
type IngestHandler struct {
	auth     *auth.Authenticator
	store    *store.Store
	interval time.Duration
}

// NewIngestHandler wires the handler. interval is the scrape interval the hub
// hands back to agents.
func NewIngestHandler(a *auth.Authenticator, s *store.Store, interval time.Duration) *IngestHandler {
	return &IngestHandler{auth: a, store: s, interval: interval}
}

func (h *IngestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostID, err := h.auth.Authenticate(ctx, bearer(r))
	if errors.Is(err, auth.ErrUnauthorized) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		slog.Error("authenticate", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var req netrav1.IngestRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		http.Error(w, "malformed body", http.StatusBadRequest)
		return
	}

	if _, err := h.store.InsertHostSamples(ctx, hostID, req.GetHostSamples()); err != nil {
		slog.Error("insert host samples", "host_id", hostID, "err", err)
		// 503, not 500: the agent should buffer and retry rather than discard.
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	if s := latest(req.GetHostSamples()); s != nil {
		if err := h.store.UpsertHostCurrent(ctx, hostID, s); err != nil {
			slog.Error("upsert host_current", "host_id", hostID, "err", err)
		}
	}

	requestMetadata, err := h.reconcileMetadata(ctx, hostID, &req)
	if err != nil {
		slog.Error("reconcile metadata", "host_id", hostID, "err", err)
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	writeProto(w, &netrav1.IngestResponse{
		AckSeq:          req.GetSeq(),
		RequestMetadata: requestMetadata,
		IntervalS:       uint32(h.interval.Seconds()),
	})
}

// reconcileMetadata stores a supplied metadata block and reports whether the
// hub still needs one. There is no connection to hang "on connect" off, so the
// hash comparison is what makes the handshake self-healing across hub
// restarts and agent upgrades alike.
func (h *IngestHandler) reconcileMetadata(ctx context.Context, hostID int32, req *netrav1.IngestRequest) (bool, error) {
	if md := req.GetMetadata(); md != nil {
		if err := h.store.SaveMetadata(ctx, hostID, req.GetMetadataHash(), md); err != nil {
			return false, err
		}
		return false, nil
	}

	stored, err := h.store.MetadataHash(ctx, hostID)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(stored, req.GetMetadataHash()) || len(stored) == 0, nil
}

func latest(samples []*netrav1.HostSample) *netrav1.HostSample {
	var out *netrav1.HostSample
	for _, s := range samples {
		if out == nil || s.GetTsMs() > out.GetTsMs() {
			out = s
		}
	}
	return out
}

func bearer(r *http.Request) string {
	const prefix = "Bearer "
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, prefix) {
		return ""
	}
	return strings.TrimPrefix(v, prefix)
}

func writeProto(w http.ResponseWriter, m proto.Message) {
	raw, err := proto.Marshal(m)
	if err != nil {
		slog.Error("marshal response", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(raw)
}
```

Add `"context"` to the imports.

- [ ] **Step 5: Add the metadata store methods**

Append to `hub/internal/store/ingest.go`:

```go
// MetadataHash returns the stored metadata hash for a host, or nil if the hub
// has never received one.
func (s *Store) MetadataHash(ctx context.Context, hostID int32) ([]byte, error) {
	var hash []byte
	err := s.pool.QueryRow(ctx,
		`SELECT metadata_hash FROM hosts WHERE id = $1`, hostID).Scan(&hash)
	if err != nil {
		return nil, fmt.Errorf("read metadata hash: %w", err)
	}
	return hash, nil
}

// SaveMetadata persists the static facts an agent reports, together with the
// hash that lets the hub detect the next change.
func (s *Store) SaveMetadata(ctx context.Context, hostID int32, hash []byte, md *netrav1.Metadata) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE hosts SET
			hostname      = $2,
			fingerprint   = $3,
			host_type     = NULLIF($4, ''),
			agent_version = $5,
			go_version    = $6,
			build_commit  = $7,
			kernel        = $8,
			os_name       = $9,
			arch          = $10,
			cpu_model     = $11,
			cores         = $12,
			threads       = $13,
			memory_total  = $14,
			metadata_hash = $15
		WHERE id = $1`,
		hostID,
		md.GetHostname(), md.GetFingerprint(), md.GetHostType(),
		md.GetAgentVersion(), md.GetGoVersion(), md.GetBuildCommit(),
		md.GetKernel(), md.GetOsName(), md.GetArch(), md.GetCpuModel(),
		int32(md.GetCores()), int32(md.GetThreads()), int64(md.GetMemoryTotal()),
		hash)
	if err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Run the tests and make sure they pass**

Run: `NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test go test ./hub/... -v`
Expected: PASS, all tests.

- [ ] **Step 7: Commit**

```bash
git add hub/internal/httpapi/ hub/internal/store/
git commit -m "feat: add agent ingest endpoint with metadata handshake"
```

---

## Task 9: Health endpoint, router and hub entrypoint

**Files:**
- Create: `hub/internal/httpapi/health.go`, `hub/internal/httpapi/health_test.go`, `hub/internal/httpapi/router.go`, `hub/cmd/netra/main.go`

**Interfaces:**
- Consumes: everything above.
- Produces: `httpapi.NewRouter(a *auth.Authenticator, s *store.Store, interval time.Duration) http.Handler`, `httpapi.NewHealthHandler(s *store.Store) http.Handler`.

- [ ] **Step 1: Write the failing test**

`hub/internal/httpapi/health_test.go`:

```go
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/netra/hub/internal/httpapi"
	"github.com/trick77/netra/hub/internal/store"
)

func TestIntegrationHealthReportsOK(t *testing.T) {
	s := store.OpenTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	srv := httptest.NewServer(httpapi.NewHealthHandler(s))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q, want %q", body.Status, "ok")
	}
	if body.Database != "ok" {
		t.Fatalf("database = %q, want %q", body.Database, "ok")
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `NETRA_TEST_DSN=... go test ./hub/internal/httpapi/ -run Health -v`
Expected: FAIL — `undefined: httpapi.NewHealthHandler`.

- [ ] **Step 3: Implement the health handler**

`hub/internal/httpapi/health.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/trick77/netra/hub/internal/buildinfoshim"
	"github.com/trick77/netra/hub/internal/store"
)

type healthHandler struct {
	store *store.Store
}

// NewHealthHandler reports liveness plus database reachability. The compose
// healthcheck hits this, so a hub that cannot reach Postgres must not look
// healthy.
func NewHealthHandler(s *store.Store) http.Handler {
	return &healthHandler{store: s}
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := map[string]string{
		"status":   "ok",
		"database": "ok",
		"version":  buildinfoshim.Version(),
	}
	status := http.StatusOK

	if err := h.store.Pool().Ping(r.Context()); err != nil {
		body["status"] = "degraded"
		body["database"] = "unreachable"
		status = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```

Replace the `buildinfoshim` import with the real one: `"github.com/trick77/netra/internal/buildinfo"` and call `buildinfo.Version()`. (The shim name above is a placeholder that must not survive — use the real package.)

- [ ] **Step 4: Implement the router**

`hub/internal/httpapi/router.go`:

```go
package httpapi

import (
	"net/http"
	"time"

	"github.com/trick77/netra/hub/internal/auth"
	"github.com/trick77/netra/hub/internal/store"
)

// NewRouter builds the hub's route table. Go 1.22 method routing is used
// directly; there is no framework.
func NewRouter(a *auth.Authenticator, s *store.Store, interval time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/health", NewHealthHandler(s))
	mux.Handle("POST /api/agent/v1/ingest", NewIngestHandler(a, s, interval))
	return mux
}
```

- [ ] **Step 5: Implement the entrypoint**

`hub/cmd/netra/main.go`:

```go
// Command netra is the monitoring hub: it accepts agent metric batches and
// stores them in TimescaleDB.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/trick77/netra/hub/internal/auth"
	"github.com/trick77/netra/hub/internal/config"
	"github.com/trick77/netra/hub/internal/httpapi"
	"github.com/trick77/netra/hub/internal/store"
	"github.com/trick77/netra/internal/buildinfo"
)

// defaultInterval is the scrape interval handed to agents. 60s matches the
// spec's default; per-host overrides arrive with the admin API.
const defaultInterval = 60 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	slog.Info("starting netra hub",
		"version", buildinfo.Version(),
		"commit", buildinfo.Commit(),
		"listen", cfg.ListenAddr)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s, err := store.Open(ctx, cfg.DatabaseDSN)
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.Migrate(ctx); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpapi.NewRouter(auth.NewAuthenticator(s.Pool()), s, defaultInterval),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
```

- [ ] **Step 6: Run the tests and build**

Run: `NETRA_TEST_DSN=... go test ./hub/... -v && make build-hub`
Expected: PASS, and `bin/netra` produced.

- [ ] **Step 7: Commit**

```bash
git add hub/
git commit -m "feat: add health endpoint, router and hub entrypoint"
```

---

## Task 10: Agent configuration

**Files:**
- Create: `agent/internal/config/config.go`, `agent/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config` with `HubURL`, `Token`, `Interval time.Duration`, `BufferWindow time.Duration`, `ProcRoot`, `SysRoot`, `Location`, `Provider`, `Facility`, `HostType`, `LogLevel`. Constructor `config.Load() (Config, error)`.

- [ ] **Step 1: Write the failing test**

`agent/internal/config/config_test.go`:

```go
package config

import (
	"testing"
	"time"
)

func TestLoadRequiresHubURLAndToken(t *testing.T) {
	t.Setenv("NETRA_HUB_URL", "")
	t.Setenv("NETRA_TOKEN", "nta_x")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no NETRA_HUB_URL, want error")
	}

	t.Setenv("NETRA_HUB_URL", "http://hub:8080")
	t.Setenv("NETRA_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no NETRA_TOKEN, want error")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("NETRA_HUB_URL", "http://hub:8080")
	t.Setenv("NETRA_TOKEN", "nta_x")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Interval != time.Minute {
		t.Fatalf("Interval = %v, want 1m", cfg.Interval)
	}
	if cfg.BufferWindow != time.Hour {
		t.Fatalf("BufferWindow = %v, want 1h", cfg.BufferWindow)
	}
	if cfg.ProcRoot != "/proc" {
		t.Fatalf("ProcRoot = %q, want %q", cfg.ProcRoot, "/proc")
	}
}

// The interval is a duration string, not a millisecond integer: beszel's
// uint16 field caps its interval at ~65s, and netra must not inherit that.
func TestLoadParsesDurationInterval(t *testing.T) {
	t.Setenv("NETRA_HUB_URL", "http://hub:8080")
	t.Setenv("NETRA_TOKEN", "nta_x")
	t.Setenv("NETRA_INTERVAL", "5m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Interval != 5*time.Minute {
		t.Fatalf("Interval = %v, want 5m", cfg.Interval)
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	t.Setenv("NETRA_HUB_URL", "http://hub:8080")
	t.Setenv("NETRA_TOKEN", "nta_x")
	t.Setenv("NETRA_INTERVAL", "sixty")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with an unparseable NETRA_INTERVAL, want error")
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./agent/internal/config/ -v`
Expected: FAIL — `undefined: Load`.

- [ ] **Step 3: Implement the config loader**

`agent/internal/config/config.go`:

```go
// Package config turns NETRA_* environment variables into an agent Config.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds every agent setting. Only HubURL and Token are required.
type Config struct {
	HubURL       string
	Token        string
	Interval     time.Duration
	BufferWindow time.Duration
	ProcRoot     string
	SysRoot      string
	Location     string
	Provider     string
	Facility     string
	HostType     string
	LogLevel     string
}

// Load reads the environment and applies defaults.
func Load() (Config, error) {
	cfg := Config{
		HubURL:   os.Getenv("NETRA_HUB_URL"),
		Token:    os.Getenv("NETRA_TOKEN"),
		ProcRoot: envOr("NETRA_PROC_ROOT", "/proc"),
		SysRoot:  envOr("NETRA_SYSFS_ROOT", "/sys"),
		Location: os.Getenv("NETRA_LOCATION"),
		Provider: os.Getenv("NETRA_PROVIDER"),
		Facility: os.Getenv("NETRA_FACILITY"),
		HostType: os.Getenv("NETRA_HOST_TYPE"),
		LogLevel: envOr("NETRA_LOG_LEVEL", "info"),
	}

	if cfg.HubURL == "" {
		return Config{}, fmt.Errorf("NETRA_HUB_URL is required")
	}
	if cfg.Token == "" {
		return Config{}, fmt.Errorf("NETRA_TOKEN is required")
	}

	var err error
	if cfg.Interval, err = durationOr("NETRA_INTERVAL", time.Minute); err != nil {
		return Config{}, err
	}
	// The buffer window is coupled to the hub's continuous-aggregate
	// start_offset (6h). Raising it past that silently corrupts rollups for
	// replayed data, so it is documented rather than validated here.
	if cfg.BufferWindow, err = durationOr("NETRA_BUFFER_WINDOW", time.Hour); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationOr(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %s", key, v)
	}
	return d, nil
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./agent/internal/config/ -v`
Expected: PASS, four tests.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/config/
git commit -m "feat: add agent configuration loader"
```

---

## Task 11: Collector interface and CPU collector

**Files:**
- Create: `agent/internal/collector/collector.go`, `agent/internal/collector/cpu.go`, `agent/internal/collector/cpu_test.go`, `agent/internal/collector/testdata/proc1/stat`, `agent/internal/collector/testdata/proc2/stat`

**Interfaces:**
- Consumes: `netrav1.HostSample` (Task 2).
- Produces: interface `collector.Collector { Name() string; Interval() time.Duration; Collect(ctx, *netrav1.HostSample) error }`; constructor `collector.NewCPU(procRoot string, interval time.Duration) *CPU`.

- [ ] **Step 1: Write the fixtures**

`agent/internal/collector/testdata/proc1/stat`:

```
cpu  1000 20 300 8000 100 0 50 10 0 0
cpu0 500 10 150 4000 50 0 25 5 0 0
cpu1 500 10 150 4000 50 0 25 5 0 0
intr 12345
ctxt 67890
```

`agent/internal/collector/testdata/proc2/stat`:

```
cpu  1100 20 350 8400 120 0 60 10 0 0
cpu0 550 10 175 4200 60 0 30 5 0 0
cpu1 550 10 175 4200 60 0 30 5 0 0
intr 22345
ctxt 77890
```

Between the two snapshots: user +100, nice +0, system +50, idle +400, iowait +20,
irq +0, softirq +10, steal +0. Total delta = 580 (guest and guest_nice are excluded
from the total, matching kernel accounting). Busy = total − idle − iowait = 160.
So `cpu_total` = 160/580 × 100 ≈ 27.586.

- [ ] **Step 2: Write the failing test**

`agent/internal/collector/cpu_test.go`:

```go
package collector_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/trick77/netra/agent/internal/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// The first scrape has no previous snapshot to diff against, so it must
// report nothing rather than a fabricated value.
func TestCPUFirstCollectYieldsNoValue(t *testing.T) {
	c := collector.NewCPU("testdata/proc1", time.Minute)

	var sample netrav1.HostSample
	if err := c.Collect(context.Background(), &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if sample.CpuTotal != nil {
		t.Fatalf("CpuTotal = %v, want absent on the first scrape", *sample.CpuTotal)
	}
}

func TestCPUSecondCollectComputesDelta(t *testing.T) {
	c := collector.NewCPU("testdata/proc1", time.Minute)
	ctx := context.Background()

	var first netrav1.HostSample
	if err := c.Collect(ctx, &first); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	c.SetProcRootForTest("testdata/proc2")

	var second netrav1.HostSample
	if err := c.Collect(ctx, &second); err != nil {
		t.Fatalf("second Collect: %v", err)
	}

	if second.CpuTotal == nil {
		t.Fatal("CpuTotal is absent, want a computed value")
	}
	const want = 160.0 / 580.0 * 100.0
	if math.Abs(*second.CpuTotal-want) > 0.001 {
		t.Fatalf("CpuTotal = %v, want %v", *second.CpuTotal, want)
	}

	if second.CpuIowait == nil {
		t.Fatal("CpuIowait is absent, want a computed value")
	}
	const wantIowait = 20.0 / 580.0 * 100.0
	if math.Abs(*second.CpuIowait-wantIowait) > 0.001 {
		t.Fatalf("CpuIowait = %v, want %v", *second.CpuIowait, wantIowait)
	}
}

// Counters reset to zero on reboot. A naive delta would produce a negative
// or an enormous spike; the collector must emit nothing instead.
func TestCPUCounterResetProducesNoValue(t *testing.T) {
	c := collector.NewCPU("testdata/proc2", time.Minute)
	ctx := context.Background()

	var first netrav1.HostSample
	if err := c.Collect(ctx, &first); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	c.SetProcRootForTest("testdata/proc1") // counters go backwards

	var second netrav1.HostSample
	if err := c.Collect(ctx, &second); err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if second.CpuTotal != nil {
		t.Fatalf("CpuTotal = %v, want absent after a counter reset", *second.CpuTotal)
	}
}

func TestCPUNameAndInterval(t *testing.T) {
	c := collector.NewCPU("testdata/proc1", 30*time.Second)
	if c.Name() != "cpu" {
		t.Fatalf("Name() = %q, want %q", c.Name(), "cpu")
	}
	if c.Interval() != 30*time.Second {
		t.Fatalf("Interval() = %v, want 30s", c.Interval())
	}
}
```

- [ ] **Step 3: Run it to make sure it fails**

Run: `go test ./agent/internal/collector/ -v`
Expected: FAIL — `undefined: collector.NewCPU`.

- [ ] **Step 4: Define the Collector interface**

`agent/internal/collector/collector.go`:

```go
// Package collector reads host metrics from kernel interfaces.
//
// Every collector is independent: one that cannot run reports why and is
// skipped, and the agent keeps posting everything else.
package collector

import (
	"context"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Collector fills in the fields of a HostSample it is responsible for.
//
// A Collector must leave a field unset when the underlying subsystem is
// absent or the value cannot be computed. An unset field becomes SQL NULL,
// which is a different fact from zero.
type Collector interface {
	// Name identifies the collector in logs and in collector_samples.
	Name() string

	// Interval is how often this collector should run. Slow collectors use a
	// longer interval than the scrape loop so they never stall it.
	Interval() time.Duration

	// Collect reads the current values and writes them into sample.
	Collect(ctx context.Context, sample *netrav1.HostSample) error
}
```

- [ ] **Step 5: Implement the CPU collector**

`agent/internal/collector/cpu.go`:

```go
package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// cpuTimes holds the fields of the aggregate "cpu" line in /proc/stat.
type cpuTimes struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
	guest, guestNice                                      uint64
}

// total excludes guest and guestNice: the kernel already counts guest time
// inside user, and nice guest inside nice, so adding them double-counts.
func (c cpuTimes) total() uint64 {
	return c.user + c.nice + c.system + c.idle +
		c.iowait + c.irq + c.softirq + c.steal
}

func (c cpuTimes) busy() uint64 {
	return c.total() - c.idle - c.iowait
}

// CPU reports aggregate CPU utilisation from /proc/stat.
//
// Values are percentages over the interval between two scrapes, so the first
// scrape after start produces nothing.
type CPU struct {
	procRoot string
	interval time.Duration
	prev     *cpuTimes
}

// NewCPU builds a CPU collector reading from procRoot (normally "/proc").
func NewCPU(procRoot string, interval time.Duration) *CPU {
	return &CPU{procRoot: procRoot, interval: interval}
}

// Name implements Collector.
func (c *CPU) Name() string { return "cpu" }

// Interval implements Collector.
func (c *CPU) Interval() time.Duration { return c.interval }

// SetProcRootForTest repoints the collector at a different fixture tree so a
// test can simulate the passage of time between two scrapes.
func (c *CPU) SetProcRootForTest(root string) { c.procRoot = root }

// Collect implements Collector.
func (c *CPU) Collect(_ context.Context, sample *netrav1.HostSample) error {
	cur, err := c.read()
	if err != nil {
		return err
	}

	prev := c.prev
	c.prev = &cur

	if prev == nil {
		// No baseline yet: report nothing rather than invent a value.
		return nil
	}

	totalDelta := cur.total() - prev.total()
	if cur.total() < prev.total() || totalDelta == 0 {
		// Counters went backwards (reboot) or did not move. Either way there
		// is no meaningful percentage to report.
		return nil
	}

	pct := func(a, b uint64) *float64 {
		if a < b {
			return nil
		}
		v := float64(a-b) / float64(totalDelta) * 100
		return &v
	}

	busyDelta := float64(cur.busy() - prev.busy())
	if cur.busy() < prev.busy() {
		return nil
	}
	totalPct := busyDelta / float64(totalDelta) * 100

	sample.CpuTotal = &totalPct
	sample.CpuUser = pct(cur.user, prev.user)
	sample.CpuSystem = pct(cur.system, prev.system)
	sample.CpuIowait = pct(cur.iowait, prev.iowait)
	sample.CpuSteal = pct(cur.steal, prev.steal)
	sample.CpuIdle = pct(cur.idle, prev.idle)

	return nil
}

func (c *CPU) read() (cpuTimes, error) {
	path := filepath.Join(c.procRoot, "stat")
	f, err := os.Open(path)
	if err != nil {
		return cpuTimes{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || fields[0] != "cpu" {
			continue
		}

		values := make([]uint64, 0, 10)
		for _, raw := range fields[1:] {
			v, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return cpuTimes{}, fmt.Errorf("parse %s: %w", path, err)
			}
			values = append(values, v)
		}
		for len(values) < 10 {
			values = append(values, 0)
		}

		return cpuTimes{
			user: values[0], nice: values[1], system: values[2], idle: values[3],
			iowait: values[4], irq: values[5], softirq: values[6], steal: values[7],
			guest: values[8], guestNice: values[9],
		}, nil
	}
	if err := scanner.Err(); err != nil {
		return cpuTimes{}, fmt.Errorf("read %s: %w", path, err)
	}

	return cpuTimes{}, fmt.Errorf("no aggregate cpu line in %s", path)
}
```

- [ ] **Step 6: Run the tests and make sure they pass**

Run: `go test ./agent/internal/collector/ -v`
Expected: PASS, four tests.

- [ ] **Step 7: Commit**

```bash
git add agent/internal/collector/
git commit -m "feat: add collector interface and CPU collector"
```

---

## Task 12: Memory and load collectors

**Files:**
- Create: `agent/internal/collector/memory.go`, `agent/internal/collector/memory_test.go`, `agent/internal/collector/load.go`, `agent/internal/collector/load_test.go`, `agent/internal/collector/testdata/proc1/meminfo`, `agent/internal/collector/testdata/proc1/loadavg`, `agent/internal/collector/testdata/proc1/uptime`, `agent/internal/collector/testdata/noswap/meminfo`

**Interfaces:**
- Consumes: `Collector` (Task 11).
- Produces: `collector.NewMemory(procRoot string, interval time.Duration) *Memory`, `collector.NewLoad(procRoot string, interval time.Duration) *Load`.

- [ ] **Step 1: Write the fixtures**

`agent/internal/collector/testdata/proc1/meminfo`:

```
MemTotal:       16384000 kB
MemFree:         2048000 kB
MemAvailable:    8192000 kB
Buffers:          512000 kB
Cached:          4096000 kB
SwapTotal:       4096000 kB
SwapFree:        3072000 kB
```

`agent/internal/collector/testdata/noswap/meminfo` — a host with swap disabled:

```
MemTotal:       16384000 kB
MemFree:         2048000 kB
MemAvailable:    8192000 kB
Buffers:          512000 kB
Cached:          4096000 kB
SwapTotal:             0 kB
SwapFree:              0 kB
```

`agent/internal/collector/testdata/proc1/loadavg`:

```
0.52 0.41 0.38 2/1234 56789
```

`agent/internal/collector/testdata/proc1/uptime`:

```
123456.78 987654.32
```

- [ ] **Step 2: Write the failing memory test**

`agent/internal/collector/memory_test.go`:

```go
package collector_test

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/netra/agent/internal/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func TestMemoryReadsMeminfo(t *testing.T) {
	c := collector.NewMemory("testdata/proc1", time.Minute)

	var s netrav1.HostSample
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	const kb = 1024
	if got, want := s.GetMemTotal(), uint64(16_384_000*kb); got != want {
		t.Fatalf("MemTotal = %d, want %d", got, want)
	}
	if got, want := s.GetMemAvailable(), uint64(8_192_000*kb); got != want {
		t.Fatalf("MemAvailable = %d, want %d", got, want)
	}
	// Used is total minus available, which is what a human means by "used".
	if got, want := s.GetMemUsed(), uint64((16_384_000-8_192_000)*kb); got != want {
		t.Fatalf("MemUsed = %d, want %d", got, want)
	}
	if got, want := s.GetMemBuffcache(), uint64((512_000+4_096_000)*kb); got != want {
		t.Fatalf("MemBuffcache = %d, want %d", got, want)
	}
	if got, want := s.GetSwapUsed(), uint64((4_096_000-3_072_000)*kb); got != want {
		t.Fatalf("SwapUsed = %d, want %d", got, want)
	}
}

// A host with no swap must report NULL, not 0: "swap is fine" and "there is
// no swap" are different facts and an alert rule has to tell them apart.
func TestMemoryAbsentSwapIsUnset(t *testing.T) {
	c := collector.NewMemory("testdata/noswap", time.Minute)

	var s netrav1.HostSample
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if s.SwapTotal != nil {
		t.Fatalf("SwapTotal = %d, want absent when the host has no swap", *s.SwapTotal)
	}
	if s.SwapUsed != nil {
		t.Fatalf("SwapUsed = %d, want absent when the host has no swap", *s.SwapUsed)
	}
	// Memory itself is still reported.
	if s.MemTotal == nil {
		t.Fatal("MemTotal is absent, want a value")
	}
}

// ZFS ARC is only present on hosts running ZFS.
func TestMemoryAbsentZfsArcIsUnset(t *testing.T) {
	c := collector.NewMemory("testdata/proc1", time.Minute)

	var s netrav1.HostSample
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if s.MemZfsArc != nil {
		t.Fatalf("MemZfsArc = %d, want absent with no ZFS kstat", *s.MemZfsArc)
	}
}
```

- [ ] **Step 3: Run it to make sure it fails**

Run: `go test ./agent/internal/collector/ -run Memory -v`
Expected: FAIL — `undefined: collector.NewMemory`.

- [ ] **Step 4: Implement the memory collector**

`agent/internal/collector/memory.go`:

```go
package collector

import (
	"bufio"
	"fmt"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Memory reports host memory and swap from /proc/meminfo, plus the ZFS ARC
// size from the SPL kstat when ZFS is loaded.
type Memory struct {
	procRoot string
	interval time.Duration
}

// NewMemory builds a Memory collector reading from procRoot.
func NewMemory(procRoot string, interval time.Duration) *Memory {
	return &Memory{procRoot: procRoot, interval: interval}
}

// Name implements Collector.
func (m *Memory) Name() string { return "memory" }

// Interval implements Collector.
func (m *Memory) Interval() time.Duration { return m.interval }

// Collect implements Collector.
func (m *Memory) Collect(_ context.Context, sample *netrav1.HostSample) error {
	values, err := m.readMeminfo()
	if err != nil {
		return err
	}

	total, hasTotal := values["MemTotal"]
	available, hasAvailable := values["MemAvailable"]
	buffers := values["Buffers"]
	cached := values["Cached"]

	if hasTotal {
		v := total
		sample.MemTotal = &v
	}
	if hasAvailable {
		v := available
		sample.MemAvailable = &v
	}
	if hasTotal && hasAvailable && total >= available {
		v := total - available
		sample.MemUsed = &v
	}
	if buffers+cached > 0 {
		v := buffers + cached
		sample.MemBuffcache = &v
	}

	// Swap absent is not swap empty. A SwapTotal of zero means the host has
	// no swap configured, so both fields stay unset and reach the hub as NULL.
	if swapTotal, ok := values["SwapTotal"]; ok && swapTotal > 0 {
		v := swapTotal
		sample.SwapTotal = &v
		if swapFree, ok := values["SwapFree"]; ok && swapTotal >= swapFree {
			used := swapTotal - swapFree
			sample.SwapUsed = &used
		}
	}

	if arc, ok := m.readZfsArc(); ok {
		sample.MemZfsArc = &arc
	}

	return nil
}

// readMeminfo returns every "Key: value kB" line converted to bytes.
func (m *Memory) readMeminfo() (map[string]uint64, error) {
	path := filepath.Join(m.procRoot, "meminfo")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out := make(map[string]uint64, 16)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// meminfo reports kB for everything except a few counters; the keys
		// netra reads are all kB.
		out[key] = v * 1024
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return out, nil
}

// readZfsArc returns the ARC size in bytes, and false when ZFS is not loaded.
func (m *Memory) readZfsArc() (uint64, bool) {
	path := filepath.Join(m.procRoot, "spl", "kstat", "zfs", "arcstats")
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || fields[0] != "size" {
			continue
		}
		v, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}
```

- [ ] **Step 5: Write the failing load test**

`agent/internal/collector/load_test.go`:

```go
package collector_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/trick77/netra/agent/internal/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func TestLoadReadsLoadavgAndUptime(t *testing.T) {
	c := collector.NewLoad("testdata/proc1", time.Minute)

	var s netrav1.HostSample
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if math.Abs(s.GetLoad1()-0.52) > 0.0001 {
		t.Fatalf("Load1 = %v, want 0.52", s.GetLoad1())
	}
	if math.Abs(s.GetLoad5()-0.41) > 0.0001 {
		t.Fatalf("Load5 = %v, want 0.41", s.GetLoad5())
	}
	if math.Abs(s.GetLoad15()-0.38) > 0.0001 {
		t.Fatalf("Load15 = %v, want 0.38", s.GetLoad15())
	}
	// Host uptime, truncated to whole seconds.
	if got, want := s.GetUptimeS(), uint64(123456); got != want {
		t.Fatalf("UptimeS = %d, want %d", got, want)
	}
}

func TestLoadMissingFileIsAnError() {}
```

Replace that last stub with the real test:

```go
func TestLoadMissingFileIsAnError(t *testing.T) {
	c := collector.NewLoad("testdata/does-not-exist", time.Minute)

	var s netrav1.HostSample
	if err := c.Collect(context.Background(), &s); err == nil {
		t.Fatal("Collect() succeeded with no /proc tree, want an error")
	}
}
```

- [ ] **Step 6: Implement the load collector**

`agent/internal/collector/load.go`:

```go
package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Load reports the 1/5/15 minute load averages and host uptime.
//
// Host uptime is deliberately here rather than in the agent's self-telemetry:
// host uptime and agent uptime are different facts, and conflating them hides
// an agent that is crash-looping on a machine that never rebooted.
type Load struct {
	procRoot string
	interval time.Duration
}

// NewLoad builds a Load collector reading from procRoot.
func NewLoad(procRoot string, interval time.Duration) *Load {
	return &Load{procRoot: procRoot, interval: interval}
}

// Name implements Collector.
func (l *Load) Name() string { return "load" }

// Interval implements Collector.
func (l *Load) Interval() time.Duration { return l.interval }

// Collect implements Collector.
func (l *Load) Collect(_ context.Context, sample *netrav1.HostSample) error {
	path := filepath.Join(l.procRoot, "loadavg")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return fmt.Errorf("malformed %s: %q", path, string(raw))
	}

	targets := []**float64{&sample.Load1, &sample.Load5, &sample.Load15}
	for i, target := range targets {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return fmt.Errorf("parse %s field %d: %w", path, i, err)
		}
		value := v
		*target = &value
	}

	if up, ok := l.readUptime(); ok {
		sample.UptimeS = &up
	}

	return nil
}

// readUptime returns whole seconds of host uptime, and false if unreadable.
func (l *Load) readUptime() (uint64, bool) {
	raw, err := os.ReadFile(filepath.Join(l.procRoot, "uptime"))
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return uint64(v), true
}
```

- [ ] **Step 7: Run the tests and make sure they pass**

Run: `go test ./agent/internal/collector/ -v`
Expected: PASS, nine tests.

- [ ] **Step 8: Commit**

```bash
git add agent/internal/collector/
git commit -m "feat: add memory and load collectors"
```

---

## Task 13: Ring buffer

**Files:**
- Create: `agent/internal/buffer/ring.go`, `agent/internal/buffer/ring_test.go`

**Interfaces:**
- Consumes: `netrav1.HostSample` (Task 2).
- Produces: `buffer.New(capacity int) *Ring`, `(*Ring).Add(seq uint64, s *netrav1.HostSample)`, `(*Ring).Pending() []buffer.Entry`, `(*Ring).AckThrough(seq uint64)`, `(*Ring).Depth() int`, `(*Ring).Dropped() uint64`. `buffer.Entry{Seq uint64; Sample *netrav1.HostSample}`.

- [ ] **Step 1: Write the failing test**

`agent/internal/buffer/ring_test.go`:

```go
package buffer_test

import (
	"testing"

	"github.com/trick77/netra/agent/internal/buffer"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func sample(ts int64) *netrav1.HostSample {
	return &netrav1.HostSample{TsMs: ts}
}

func TestAddAndPendingPreserveOrder(t *testing.T) {
	r := buffer.New(4)
	r.Add(1, sample(100))
	r.Add(2, sample(200))

	pending := r.Pending()
	if len(pending) != 2 {
		t.Fatalf("len(Pending()) = %d, want 2", len(pending))
	}
	if pending[0].Seq != 1 || pending[1].Seq != 2 {
		t.Fatalf("seqs = %d,%d, want 1,2", pending[0].Seq, pending[1].Seq)
	}
	if r.Depth() != 2 {
		t.Fatalf("Depth() = %d, want 2", r.Depth())
	}
}

// Overflow overwrites the oldest entry and counts the loss. Without the
// counter, a long hub outage silently eats data.
func TestOverflowDropsOldestAndCounts(t *testing.T) {
	r := buffer.New(2)
	r.Add(1, sample(100))
	r.Add(2, sample(200))
	r.Add(3, sample(300))

	if r.Depth() != 2 {
		t.Fatalf("Depth() = %d, want 2", r.Depth())
	}
	if r.Dropped() != 1 {
		t.Fatalf("Dropped() = %d, want 1", r.Dropped())
	}

	pending := r.Pending()
	if pending[0].Seq != 2 || pending[1].Seq != 3 {
		t.Fatalf("seqs = %d,%d, want 2,3 — the oldest must be dropped",
			pending[0].Seq, pending[1].Seq)
	}
}

func TestAckThroughRemovesAckedEntries(t *testing.T) {
	r := buffer.New(4)
	r.Add(1, sample(100))
	r.Add(2, sample(200))
	r.Add(3, sample(300))

	r.AckThrough(2)

	pending := r.Pending()
	if len(pending) != 1 {
		t.Fatalf("len(Pending()) = %d, want 1", len(pending))
	}
	if pending[0].Seq != 3 {
		t.Fatalf("remaining seq = %d, want 3", pending[0].Seq)
	}
}

func TestAckThroughIgnoresStaleAck(t *testing.T) {
	r := buffer.New(4)
	r.Add(5, sample(500))

	r.AckThrough(2) // older than anything held

	if r.Depth() != 1 {
		t.Fatalf("Depth() = %d, want 1 — a stale ack must not drop entries", r.Depth())
	}
}

func TestPendingReturnsACopy(t *testing.T) {
	r := buffer.New(4)
	r.Add(1, sample(100))

	pending := r.Pending()
	pending[0].Seq = 99

	if r.Pending()[0].Seq != 1 {
		t.Fatal("mutating the slice returned by Pending() changed the buffer")
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./agent/internal/buffer/ -v`
Expected: FAIL — `undefined: buffer.New`.

- [ ] **Step 3: Implement the ring buffer**

`agent/internal/buffer/ring.go`:

```go
// Package buffer holds unacknowledged samples while the hub is unreachable.
package buffer

import (
	"sync"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Entry is one buffered sample and its batch sequence number.
type Entry struct {
	Seq    uint64
	Sample *netrav1.HostSample
}

// Ring is a bounded, overwrite-oldest buffer of unacknowledged samples.
//
// It is deliberately in memory only. Persisting it would need a state volume
// and corruption handling to cover a case that barely exists: an agent
// restart usually means a host reboot or an image update, and during an image
// update the hub is up, so nothing would be buffered.
type Ring struct {
	mu       sync.Mutex
	capacity int
	entries  []Entry
	dropped  uint64
}

// New builds a Ring holding at most capacity entries.
func New(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{
		capacity: capacity,
		entries:  make([]Entry, 0, capacity),
	}
}

// Add appends a sample, discarding the oldest entry when full.
func (r *Ring) Add(seq uint64, s *netrav1.HostSample) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.entries) == r.capacity {
		r.entries = r.entries[1:]
		r.dropped++
	}
	r.entries = append(r.entries, Entry{Seq: seq, Sample: s})
}

// Pending returns a copy of the buffered entries, oldest first. Replay sends
// them in this order so history fills in forwards.
func (r *Ring) Pending() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

// AckThrough drops every entry with a sequence number at or below seq.
func (r *Ring) AckThrough(seq uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	keep := r.entries[:0]
	for _, e := range r.entries {
		if e.Seq > seq {
			keep = append(keep, e)
		}
	}
	r.entries = keep
}

// Depth reports how many entries are waiting to be acknowledged.
func (r *Ring) Depth() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Dropped reports how many entries have been discarded through overflow.
// This is cumulative and resets when the agent restarts, so the hub must
// treat a decrease as a reset rather than a negative delta.
func (r *Ring) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./agent/internal/buffer/ -v`
Expected: PASS, five tests.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/buffer/
git commit -m "feat: add bounded overwrite-oldest sample buffer"
```

---

## Task 14: Agent client, scrape loop and entrypoint

**Files:**
- Create: `agent/internal/client/client.go`, `agent/internal/client/client_test.go`, `agent/internal/client/metadata.go`, `agent/internal/client/metadata_test.go`, `agent/cmd/netra-agent/main.go`

**Interfaces:**
- Consumes: `config.Config` (Task 10), `Collector` (Tasks 11–12), `*buffer.Ring` (Task 13), `netrav1` (Task 2).
- Produces: `client.New(cfg config.Config, collectors []collector.Collector) *Client`, `(*Client).ScrapeOnce(ctx) *netrav1.HostSample`, `(*Client).Flush(ctx) error`, `(*Client).Run(ctx) error`, `client.BuildMetadata(cfg) *netrav1.Metadata`, `client.HashMetadata(*netrav1.Metadata) []byte`.

- [ ] **Step 1: Write the failing metadata test**

`agent/internal/client/metadata_test.go`:

```go
package client_test

import (
	"bytes"
	"testing"

	"github.com/trick77/netra/agent/internal/client"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func TestHashMetadataIsStable(t *testing.T) {
	md := &netrav1.Metadata{Hostname: "h1", AgentVersion: "0.1.0", Location: "Gravelines, FR"}

	first := client.HashMetadata(md)
	second := client.HashMetadata(md)

	if !bytes.Equal(first, second) {
		t.Fatal("HashMetadata is not deterministic for identical input")
	}
	if len(first) != 8 {
		t.Fatalf("len(hash) = %d, want 8", len(first))
	}
}

// The hash is the entire change-detection mechanism: if an edited location
// does not move it, the hub never learns about the change.
func TestHashMetadataChangesWithContent(t *testing.T) {
	a := client.HashMetadata(&netrav1.Metadata{Hostname: "h1", Location: "Gravelines, FR"})
	b := client.HashMetadata(&netrav1.Metadata{Hostname: "h1", Location: "Falkenstein, DE"})

	if bytes.Equal(a, b) {
		t.Fatal("HashMetadata returned the same value for different metadata")
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./agent/internal/client/ -v`
Expected: FAIL — `undefined: client.HashMetadata`.

- [ ] **Step 3: Implement metadata construction and hashing**

`agent/internal/client/metadata.go`:

```go
package client

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"runtime"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/agent/internal/config"
	"github.com/trick77/netra/internal/buildinfo"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// BuildMetadata gathers the static facts about this host and agent.
func BuildMetadata(cfg config.Config) *netrav1.Metadata {
	hostname, _ := os.Hostname()

	return &netrav1.Metadata{
		AgentVersion: buildinfo.Version(),
		GoVersion:    buildinfo.GoVersion(),
		BuildCommit:  buildinfo.Commit(),
		Hostname:     hostname,
		Arch:         runtime.GOARCH,
		OsName:       runtime.GOOS,
		Threads:      uint32(runtime.NumCPU()),
		Location:     cfg.Location,
		Provider:     cfg.Provider,
		Facility:     cfg.Facility,
		HostType:     cfg.HostType,
		Fingerprint:  fingerprint(),
	}
}

// HashMetadata reduces a metadata block to the 8 bytes sent on every POST.
//
// Deterministic marshalling matters: protobuf map and field ordering is not
// guaranteed stable by default, and an unstable hash would make the agent
// resend its metadata on every scrape.
func HashMetadata(md *netrav1.Metadata) []byte {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(md)
	if err != nil {
		// Marshalling a struct we built ourselves cannot fail in practice;
		// a zero hash still behaves correctly, it just forces a resend.
		return make([]byte, 8)
	}

	sum := sha256.Sum256(raw)
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, binary.BigEndian.Uint64(sum[:8]))
	return out
}

// fingerprint identifies the physical machine so a token copied to a second
// host is detectable. /etc/machine-id is stable across reboots and container
// recreation.
func fingerprint() string {
	raw, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(raw))))
	return string(sum[:])
}
```

- [ ] **Step 4: Run the metadata tests and make sure they pass**

Run: `go test ./agent/internal/client/ -v`
Expected: PASS, two tests.

- [ ] **Step 5: Write the failing client test**

`agent/internal/client/client_test.go`:

```go
package client_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/agent/internal/client"
	"github.com/trick77/netra/agent/internal/collector"
	"github.com/trick77/netra/agent/internal/config"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

type recorder struct {
	mu       sync.Mutex
	requests []*netrav1.IngestRequest
	respond  func(*netrav1.IngestRequest) *netrav1.IngestResponse
}

func (rec *recorder) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var req netrav1.IngestRequest
		if err := proto.Unmarshal(raw, &req); err != nil {
			t.Errorf("unmarshal: %v", err)
			return
		}

		rec.mu.Lock()
		rec.requests = append(rec.requests, &req)
		rec.mu.Unlock()

		resp := &netrav1.IngestResponse{AckSeq: req.GetSeq()}
		if rec.respond != nil {
			resp = rec.respond(&req)
		}
		out, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(out)
	})
}

func (rec *recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

func (rec *recorder) last() *netrav1.IngestRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		return nil
	}
	return rec.requests[len(rec.requests)-1]
}

func newClient(t *testing.T, url string) *client.Client {
	t.Helper()
	cfg := config.Config{
		HubURL:       url,
		Token:        "nta_test",
		Interval:     time.Minute,
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}
	collectors := []collector.Collector{
		collector.NewMemory(cfg.ProcRoot, cfg.Interval),
		collector.NewLoad(cfg.ProcRoot, cfg.Interval),
	}
	return client.New(cfg, collectors)
}

func TestFlushSendsBufferedSamplesAndClearsOnAck(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if rec.count() != 1 {
		t.Fatalf("requests = %d, want 1", rec.count())
	}
	if got := len(rec.last().GetHostSamples()); got != 1 {
		t.Fatalf("host samples in request = %d, want 1", got)
	}
	if c.BufferDepth() != 0 {
		t.Fatalf("BufferDepth() = %d, want 0 after an ack", c.BufferDepth())
	}
}

// An unreachable hub must not lose samples: they stay buffered and go out on
// the next successful flush, flagged as backfill.
func TestFlushBuffersWhileHubIsDown(t *testing.T) {
	var down bool
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rec.handler(t).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	down = true
	c.ScrapeOnce(ctx)
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush() succeeded against a 503, want an error")
	}
	if c.BufferDepth() != 2 {
		t.Fatalf("BufferDepth() = %d, want 2 while the hub is down", c.BufferDepth())
	}

	down = false
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush after recovery: %v", err)
	}
	if c.BufferDepth() != 0 {
		t.Fatalf("BufferDepth() = %d, want 0 after recovery", c.BufferDepth())
	}
	if !rec.last().GetBackfill() {
		t.Fatal("Backfill = false, want true for replayed samples")
	}
}

func TestFlushSendsMetadataWhenRequested(t *testing.T) {
	rec := &recorder{
		respond: func(req *netrav1.IngestRequest) *netrav1.IngestResponse {
			// Ask for metadata on the first request only.
			return &netrav1.IngestResponse{
				AckSeq:          req.GetSeq(),
				RequestMetadata: req.GetMetadata() == nil,
			}
		},
	}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("first Flush: %v", err)
	}
	if rec.last().GetMetadata() != nil {
		t.Fatal("first request carried metadata, want none until asked")
	}

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if rec.last().GetMetadata() == nil {
		t.Fatal("second request carried no metadata, want it after RequestMetadata")
	}
	if rec.last().GetMetadata().GetHostname() == "" {
		t.Fatal("metadata hostname is empty")
	}
}

func TestFlushSendsMetadataHashOnEveryRequest(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(rec.last().GetMetadataHash()) != 8 {
		t.Fatalf("len(MetadataHash) = %d, want 8", len(rec.last().GetMetadataHash()))
	}
}

func TestFlushRejectsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := newClient(t, srv.URL)
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	err := c.Flush(ctx)
	if err == nil {
		t.Fatal("Flush() succeeded against a 401, want an error")
	}
	// A revoked host must stop hammering the hub, so the buffer is cleared
	// rather than replayed forever.
	if c.BufferDepth() != 0 {
		t.Fatalf("BufferDepth() = %d, want 0 after a 401", c.BufferDepth())
	}
}
```

- [ ] **Step 6: Implement the client**

`agent/internal/client/client.go`:

```go
// Package client scrapes collectors and posts batches to the hub.
package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/agent/internal/buffer"
	"github.com/trick77/netra/agent/internal/collector"
	"github.com/trick77/netra/agent/internal/config"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// ErrUnauthorized means the hub rejected this agent's token.
var ErrUnauthorized = errors.New("hub rejected the agent token")

// ingestPath is the hub endpoint agents post to.
const ingestPath = "/api/agent/v1/ingest"

// Client owns the scrape loop, the buffer and the HTTP conversation.
type Client struct {
	cfg        config.Config
	collectors []collector.Collector
	http       *http.Client
	ring       *buffer.Ring

	seq             uint64
	metadata        *netrav1.Metadata
	metadataHash    []byte
	sendMetadata    bool
	lastFlushFailed bool
}

// New builds a Client. Buffer capacity is derived from the configured window
// and interval, so NETRA_BUFFER_WINDOW is expressed in time rather than in a
// sample count nobody can reason about.
func New(cfg config.Config, collectors []collector.Collector) *Client {
	capacity := int(cfg.BufferWindow / cfg.Interval)
	if capacity < 1 {
		capacity = 1
	}

	md := BuildMetadata(cfg)

	return &Client{
		cfg:          cfg,
		collectors:   collectors,
		http:         &http.Client{Timeout: 30 * time.Second},
		ring:         buffer.New(capacity),
		metadata:     md,
		metadataHash: HashMetadata(md),
		// The hub asks for metadata when it needs it; nothing is assumed.
		sendMetadata: false,
	}
}

// BufferDepth reports how many samples are waiting to be acknowledged.
func (c *Client) BufferDepth() int { return c.ring.Depth() }

// ScrapeOnce runs every collector and buffers the resulting sample.
//
// A collector that fails is logged and skipped: its fields stay unset, and
// the rest of the sample is still worth sending.
func (c *Client) ScrapeOnce(ctx context.Context) *netrav1.HostSample {
	sample := &netrav1.HostSample{TsMs: time.Now().UnixMilli()}

	for _, col := range c.collectors {
		if err := col.Collect(ctx, sample); err != nil {
			slog.Warn("collector failed", "collector", col.Name(), "err", err)
		}
	}

	c.seq++
	c.ring.Add(c.seq, sample)
	return sample
}

// Flush posts every buffered sample, oldest first, and drops the ones the hub
// acknowledges.
func (c *Client) Flush(ctx context.Context) error {
	pending := c.ring.Pending()
	if len(pending) == 0 {
		return nil
	}

	samples := make([]*netrav1.HostSample, 0, len(pending))
	for _, e := range pending {
		samples = append(samples, e.Sample)
	}
	highest := pending[len(pending)-1].Seq

	req := &netrav1.IngestRequest{
		Seq:          highest,
		MetadataHash: c.metadataHash,
		HostSamples:  samples,
		// Anything sent after a failed flush is replayed history, and the hub
		// needs to know so it can invalidate the affected aggregate ranges.
		Backfill: c.lastFlushFailed,
	}
	if c.sendMetadata {
		req.Metadata = c.metadata
	}

	resp, err := c.post(ctx, req)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			// The token is gone. Replaying forever would hammer the hub for
			// nothing, so the buffer is dropped and the operator has to act.
			c.ring.AckThrough(highest)
			c.lastFlushFailed = false
			return err
		}
		c.lastFlushFailed = true
		return err
	}

	c.ring.AckThrough(resp.GetAckSeq())
	c.sendMetadata = resp.GetRequestMetadata()
	c.lastFlushFailed = false

	return nil
}

func (c *Client) post(ctx context.Context, req *netrav1.IngestRequest) (*netrav1.IngestResponse, error) {
	body, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.cfg.HubURL, "/") + ingestPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.Token)

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post to hub: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub returned %s", httpResp.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp netrav1.IngestResponse
	if err := proto.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &resp, nil
}

// Run scrapes and flushes on the configured interval until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.ScrapeOnce(ctx)

			if err := c.Flush(ctx); err != nil {
				if errors.Is(err, ErrUnauthorized) {
					// Retry slowly: a revoked agent must not hammer the hub.
					slog.Error("hub rejected the agent token; retrying slowly", "err", err)
					sleep(ctx, 5*time.Minute)
					continue
				}

				slog.Warn("flush failed; samples are buffered",
					"err", err, "buffer_depth", c.ring.Depth(),
					"dropped_total", c.ring.Dropped())

				// Jitter keeps a fleet from reconnecting in lockstep after a
				// hub restart.
				jittered := backoff + time.Duration(rand.Int64N(int64(backoff)))
				sleep(ctx, jittered)
				if backoff < time.Minute {
					backoff *= 2
				}
				continue
			}

			backoff = time.Second
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
```

- [ ] **Step 7: Run the client tests and make sure they pass**

Run: `go test ./agent/internal/client/ -v`
Expected: PASS, seven tests.

- [ ] **Step 8: Implement the agent entrypoint**

`agent/cmd/netra-agent/main.go`:

```go
// Command netra-agent collects host metrics and pushes them to a netra hub.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/trick77/netra/agent/internal/client"
	"github.com/trick77/netra/agent/internal/collector"
	"github.com/trick77/netra/agent/internal/config"
	"github.com/trick77/netra/internal/buildinfo"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	slog.Info("starting netra agent",
		"version", buildinfo.Version(),
		"commit", buildinfo.Commit(),
		"hub", cfg.HubURL,
		"interval", cfg.Interval)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	collectors := []collector.Collector{
		collector.NewCPU(cfg.ProcRoot, cfg.Interval),
		collector.NewMemory(cfg.ProcRoot, cfg.Interval),
		collector.NewLoad(cfg.ProcRoot, cfg.Interval),
	}

	c := client.New(cfg, collectors)

	// Prime the CPU collector: it needs a baseline before it can report a
	// delta, and doing it here means the first scheduled scrape has one.
	c.ScrapeOnce(ctx)

	return c.Run(ctx)
}
```

- [ ] **Step 9: Build and commit**

Run: `make build && go test ./agent/... -v`
Expected: both binaries produced, all agent tests PASS.

```bash
git add agent/
git commit -m "feat: add agent client, scrape loop and entrypoint"
```

---

## Task 15: End-to-end test

**Files:**
- Create: `hub/internal/httpapi/e2e_test.go`

**Interfaces:**
- Consumes: everything.
- Produces: nothing new — this task proves the pieces fit.

- [ ] **Step 1: Write the failing test**

`hub/internal/httpapi/e2e_test.go`:

```go
package httpapi_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trick77/netra/agent/internal/client"
	"github.com/trick77/netra/agent/internal/collector"
	agentconfig "github.com/trick77/netra/agent/internal/config"
	"github.com/trick77/netra/hub/internal/auth"
	"github.com/trick77/netra/hub/internal/httpapi"
	"github.com/trick77/netra/hub/internal/store"
)

// A real agent posting to a real hub backed by a real TimescaleDB. Everything
// below the HTTP boundary is the production code path.
func TestIntegrationAgentToHubRoundTrip(t *testing.T) {
	ctx := context.Background()

	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('e2e') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	token, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID, hash); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	srv := httptest.NewServer(
		httpapi.NewRouter(auth.NewAuthenticator(s.Pool()), s, time.Minute))
	t.Cleanup(srv.Close)

	cfg := agentconfig.Config{
		HubURL:       srv.URL,
		Token:        token,
		Interval:     time.Minute,
		BufferWindow: time.Hour,
		ProcRoot:     "../../../agent/internal/collector/testdata/proc1",
	}
	c := client.New(cfg, []collector.Collector{
		collector.NewMemory(cfg.ProcRoot, cfg.Interval),
		collector.NewLoad(cfg.ProcRoot, cfg.Interval),
	})

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var (
		count     int
		memTotal  *int64
		load1     *float64
		swapTotal *int64
	)
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples WHERE host_id = $1`, hostID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("stored rows = %d, want 1", count)
	}

	if err := s.Pool().QueryRow(ctx,
		`SELECT mem_total, load1, swap_total FROM host_samples WHERE host_id = $1`,
		hostID).Scan(&memTotal, &load1, &swapTotal); err != nil {
		t.Fatalf("select: %v", err)
	}

	if memTotal == nil || *memTotal != 16_384_000*1024 {
		t.Fatalf("mem_total = %v, want %d", memTotal, int64(16_384_000*1024))
	}
	if load1 == nil || *load1 < 0.51 || *load1 > 0.53 {
		t.Fatalf("load1 = %v, want ~0.52", load1)
	}
	if swapTotal == nil {
		t.Fatal("swap_total is NULL, want a value — the fixture host has swap")
	}

	// The hub had no metadata, so it must have asked; the next flush supplies
	// it and the hostname lands on the host row.
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}

	var agentVersion *string
	if err := s.Pool().QueryRow(ctx,
		`SELECT agent_version FROM hosts WHERE id = $1`, hostID).Scan(&agentVersion); err != nil {
		t.Fatalf("select agent_version: %v", err)
	}
	if agentVersion == nil || *agentVersion == "" {
		t.Fatal("agent_version is empty, want it populated by the metadata handshake")
	}

	var lastSeen *time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM host_current WHERE host_id = $1`, hostID).Scan(&lastSeen); err != nil {
		t.Fatalf("select host_current: %v", err)
	}
	if lastSeen == nil {
		t.Fatal("host_current.last_seen is NULL, want it updated on ingest")
	}
}
```

- [ ] **Step 2: Run it**

Run: `NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test go test ./hub/internal/httpapi/ -run E2E -v`

If it fails, the failure is a real integration defect — fix the production code, not the test.

Run again: same command.
Expected: PASS.

- [ ] **Step 3: Run everything**

Run: `NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test go test ./... -v`
Expected: PASS, all packages.

- [ ] **Step 4: Commit**

```bash
git add hub/internal/httpapi/e2e_test.go
git commit -m "test: add agent to hub end-to-end integration test"
```

---

## Task 16: CI with coverage gates

**Files:**
- Create: `.github/workflows/ci.yaml`, `hack/coverage-floors`, `hack/coverage-gate.sh`, `hack/patch-coverage.sh`

**Interfaces:**
- Consumes: the whole module.
- Produces: a green CI run on pull requests.

- [ ] **Step 1: Copy the coverage gate scripts**

The two gate scripts already exist and are proven in the sibling `music` repo. Copy them verbatim, then adapt only the component names:

```bash
mkdir -p hack
cp ../../../music/hack/coverage-gate.sh hack/
cp ../../../music/hack/patch-coverage.sh hack/
chmod +x hack/coverage-gate.sh hack/patch-coverage.sh
```

Adjust paths inside both scripts so they look for `coverage/<component>.xml` where component is `hub` or `agent` rather than `backend` or `ui`. Read each script and change only the component name list and the report paths; leave the diff-cover invocation and the escape-hatch logic alone.

- [ ] **Step 2: Write the coverage floors**

`hack/coverage-floors`:

```
# Line-coverage floors. Hard floor, not a ratchet — see hack/coverage-gate.sh.
hub=75.0
agent=75.0
```

- [ ] **Step 3: Write the CI workflow**

`.github/workflows/ci.yaml`:

```yaml
name: CI

# Tests run on pull requests. Master builds and pushes images in a separate
# release workflow; no image build happens here.
on:
  pull_request:
    branches: [master]
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

jobs:
  build:
    name: Build and vet
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - run: go build ./...
      - run: go vet ./...
      # gofmt is not covered by `go vet`, so it is enforced explicitly rather
      # than trusted to editors.
      - name: gofmt
        run: |
          unformatted="$(gofmt -l .)"
          if [ -n "$unformatted" ]; then
            echo "::error::These files are not gofmt'd. Run: gofmt -w ."
            echo "$unformatted"
            exit 1
          fi

  test:
    name: Test (${{ matrix.component }})
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        include:
          - component: hub
            packages: ./hub/... ./internal/...
          - component: agent
            packages: ./agent/...
    services:
      timescaledb:
        image: timescale/timescaledb:latest-pg17
        env:
          POSTGRES_USER: netra
          POSTGRES_PASSWORD: netra
          POSTGRES_DB: netra_test
        ports:
          - 5432:5432
        options: >-
          --health-cmd "pg_isready -U netra"
          --health-interval 5s
          --health-timeout 5s
          --health-retries 10
    steps:
      # fetch-depth: 0 — hack/patch-coverage.sh diffs the branch against the
      # PR base, which needs real history. A shallow checkout leaves the base
      # ref unresolvable and the gate exits 2.
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - uses: actions/setup-python@v7
        with:
          python-version: "3.12"
      - run: pip install diff-cover==10.3.0
      - run: mkdir -p coverage
      # The race detector needs cgo. This is independent of the
      # CGO_ENABLED=0 invariant used for the statically linked shipped
      # binaries.
      #
      # -coverpkg=./... attributes coverage across package boundaries: code
      # exercised only by another package's tests would otherwise be reported
      # as uncovered, understating the real number badly.
      - name: Test
        run: |
          go test -race -covermode=atomic -coverpkg=./... \
            -coverprofile=coverage/${{ matrix.component }}.out ${{ matrix.packages }}
        env:
          CGO_ENABLED: "1"
          NETRA_TEST_DSN: postgres://netra:netra@127.0.0.1:5432/netra_test
      # Go exposes no line metric — `go tool cover` reports statements only —
      # so the line percentage comes from a Cobertura conversion. That also
      # merges the duplicate blocks -coverpkg emits, one set per test binary,
      # which a naive sum over the raw profile gets badly wrong.
      - run: go run github.com/boumenot/gocover-cobertura@v1.5.0 < coverage/${{ matrix.component }}.out > coverage/${{ matrix.component }}.xml
      # The floor gate runs first: when a PR drops the project below the floor
      # the absolute number is the more actionable failure, and reporting it
      # before patch coverage keeps the log readable.
      - run: ./hack/coverage-gate.sh ${{ matrix.component }}
      - name: Patch coverage
        run: ./hack/patch-coverage.sh "origin/${{ github.base_ref || 'master' }}"
```

- [ ] **Step 4: Verify the gates locally**

```bash
mkdir -p coverage
NETRA_TEST_DSN=postgres://netra:netra@127.0.0.1:5432/netra_test \
  go test -covermode=atomic -coverpkg=./... -coverprofile=coverage/hub.out ./hub/... ./internal/...
go run github.com/boumenot/gocover-cobertura@v1.5.0 < coverage/hub.out > coverage/hub.xml
./hack/coverage-gate.sh hub
```

Expected: the gate reports a percentage at or above 75.0. If it is below, add tests for the uncovered paths before proceeding — do not lower the floor.

- [ ] **Step 5: Add coverage output to .gitignore**

```bash
printf '%s\n' "coverage/" >> .gitignore
```

- [ ] **Step 6: Commit and open the pull request**

```bash
git add .github/ hack/ .gitignore
git commit -m "ci: add build, test and coverage gate workflow"
git push -u origin feat/phase1-foundation
gh pr create --base master --head feat/phase1-foundation \
  --title "feat: phase 1 foundation — agent to hub ingest pipeline" \
  --body "Implements the foundation slice of the phase 1 spec: protobuf wire format, TimescaleDB schema with rollups, token auth, ingest endpoint, agent with CPU/memory/load collectors, ring buffer and metadata handshake. CI with coverage gates included."
```

- [ ] **Step 7: Confirm CI is green**

Run: `gh pr checks --watch`
Expected: all checks pass. If the coverage gate fails, add tests rather than lowering the floor.

---

## Self-Review

**Spec coverage.** Every foundation-slice requirement maps to a task: module layout (1), wire format with optional-scalar NULL preservation (2, 7), config (3, 10), migrations and the `start_offset`/retention coupling constraints (4, 5), token auth with no universal token (6), ingest with `ON CONFLICT DO NOTHING` and the metadata-hash handshake (7, 8), health endpoint (9), collector interface with per-collector intervals (11), CPU counter-reset handling (11), absent-swap-is-NULL (12), separate host and agent uptime (12), bounded overwrite-oldest buffer with a drop counter (13), backfill flag, jittered backoff and slow retry on 401 (14), end-to-end proof (15), CI with both coverage gates (16).

**Deliberately out of scope**, deferred to later plans and listed in §Scope: the other thirteen collectors, remaining schema, read API breadth, release and cleanup workflows, compose files.

**Known rough edges to fix during implementation.** Task 7 Step 4 introduces a `pgxBatch` wrapper that adds nothing over using `pgx.Batch` directly — collapse it into `InsertHostSamples` rather than keeping the indirection. Task 9 Step 3 shows a `buildinfoshim` import that must be replaced with the real `internal/buildinfo` package; the step says so explicitly. Task 12 Step 5 contains a stub `TestLoadMissingFileIsAnError() {}` immediately followed by its real form — write only the real one.

**Type consistency.** `netrav1` is the import alias everywhere. `*float64` / `*uint64` on every optional field, mapped to SQL `NULL` by `f64` / `u64`. `Collector` is `Name() / Interval() / Collect(ctx, *netrav1.HostSample) error` in all four implementations. `auth.Authenticate` returns `int32`, matching the `hosts.id` column type used throughout.

---

Plan complete and saved to `docs/superpowers/plans/2026-08-07-phase1-foundation.md`.
