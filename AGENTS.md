# netra

## Gate and release
- Gate = `make check` (ship master, unpinned; `ship.yaml`; local ship: `SHIP_CMD=<binary>`, hook too). Hook: `make hooks` once per clone, again when ship says it is stale. GitHub never first to find a failure; CI runs on every PR (public repo: Dependabot and forks have no hook). Dependabot PR: `make check` before merge.
- Coverage 75% floor + patch, `[skip patch-coverage]` skips patch only. Not counted: `cmd/` (ship default, as core), and `internal/hub/store/` + `internal/hub/read/` (SQL; their tests need TimescaleDB). Never lower the floor.
- Commit subjects + PR titles: Go style `area: what` (`hub: …`, `agent: …`), gated by ship; release notes list them by area.
- Quick while iterating: `make lint`, `make test`, `make fe-test`. The gate still decides.
- Integration tests need a TimescaleDB and are not in the gate: `make test-integration` (`BACKEND_TEST_DSN`, `-p 1`).
- Merge ships nothing: `gh workflow run Release -f bump=patch` → tag `X.Y.Z` (plain), hub + agent images, GitHub release. setup-agent.sh fetches `deploy/agent/` at the newest tag: a deploy/ change reaches hosts only with a release. Version is stamped at build (`-X …/internal/version.Version`, as core), never committed.
