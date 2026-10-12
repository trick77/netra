.PHONY: check hooks build build-hub build-agent build-sim test test-integration test-shell lint tidy proto fe-build fe-test

# Stamped into the binary (internal/version): logged at start, served by /api/health, sent by the agent.
VERSION ?= dev
LDFLAGS = -s -w -X github.com/trick77/netra/internal/version.Version=$(VERSION)

# The Go module lives in backend/: go -C runs there, outputs stay at the repo root (bin/).
GO := go -C backend

tidy:
	$(GO) mod tidy

# -race needs cgo; that is independent of the app's CGO_ENABLED=0 build.
test:
	CGO_ENABLED=1 $(GO) test -race ./...

# Integration tests are skipped unless BACKEND_TEST_DSN points at a TimescaleDB.
# -p 1 is mandatory: store.OpenTest drops the shared public schema, so two
# package binaries running in parallel race on the same database.
test-integration:
	BACKEND_TEST_DSN=$${BACKEND_TEST_DSN:-postgres://netra:netra@127.0.0.1:5432/netra_test} \
		$(GO) test -p 1 ./internal/hub/... -run Integration -v

# setup-agent.sh is POSIX sh, not bash, and is curl'd onto hosts whose /bin/sh
# is dash or busybox ash. Linting with -s sh and then running the suite under a
# real dash is the pair that catches bashisms: shellcheck alone misses some, and
# bash-as-sh accepts nearly all of them. A ship.yaml step.
test-shell:
	shellcheck -s sh setup-agent.sh
	shellcheck -s sh test/setup-agent/run.sh test/setup-agent/lib.sh \
		test/setup-agent/cases/*.sh
	sh test/setup-agent/run.sh
	@if command -v dash >/dev/null 2>&1; then \
		dash test/setup-agent/run.sh; \
	else \
		echo "dash not installed - skipping the dash pass (CI runs it)"; \
	fi

# The gate: everything CI checks, configured in ship.yaml. Master's ship, as the pre-push hook and the @master
# workflows run it: its sha asked of GitHub (a proxy lags on @master). A local ship: SHIP_CMD=<go-installed binary>,
# one name for here and the hook, so make check and the push gate never run different ships.
SHIP_CMD ?= sha=$$(git ls-remote https://github.com/trick77/ship refs/heads/master | cut -f1); \
	[ -n "$$sha" ] || { echo "cannot resolve ship's master on GitHub (git ls-remote, error above)"; exit 1; }; \
	go run github.com/trick77/ship@$$sha

check:
	$(SHIP_CMD) check

hooks:
	$(SHIP_CMD) hooks

# Quick lint while editing, at the version check runs.
lint:
	$(SHIP_CMD) lint

build: build-hub build-agent

build-hub: fe-build
	CGO_ENABLED=0 $(GO) build -ldflags="$(LDFLAGS)" -o $(CURDIR)/bin/netra ./cmd/netra

build-agent:
	CGO_ENABLED=0 $(GO) build -ldflags="$(LDFLAGS)" -o $(CURDIR)/bin/netra-agent ./cmd/netra-agent

# netra-sim fills a hub with a fake fleet for development. Deliberately NOT
# part of `build`, of either container image, or of the release: it registers
# hosts, mints tokens and writes three months of invented history, none of
# which belongs anywhere near a production hub.
build-sim:
	CGO_ENABLED=0 $(GO) build -ldflags="$(LDFLAGS)" -o $(CURDIR)/bin/netra-sim ./cmd/netra-sim

proto:
	$(GO) run github.com/bufbuild/buf/cmd/buf@v1.47.2 generate

# --- UI (ui/) ---

# vite builds with emptyOutDir:false so the tracked dist/.gitkeep survives (go:embed needs something there); the stale
# build is cleared here instead, everything but .gitkeep, so a file removed from ui/ never stays embedded.
fe-build:
	find backend/internal/hub/web/dist -mindepth 1 ! -name '.gitkeep' -delete
	cd ui && npm ci && npm run build

fe-test:
	cd ui && npm ci && npm run test
