.PHONY: build build-hub build-agent build-sim test test-setup-agent tidy proto fe-build fe-test

# Stamped into the binary (kit/version): logged at start, sent by the agent.
VERSION ?= dev
LDFLAGS = -s -w -X github.com/trick77/ship/kit/version.Version=$(VERSION)

# The Go module lives in backend/: go -C runs there, outputs stay at the repo root (bin/).
GO := go -C backend

tidy:
	$(GO) mod tidy

# -race needs cgo; that is independent of the app's CGO_ENABLED=0 build. The store and read tests skip
# unless SHIP_TEST_DSN points at a TimescaleDB superuser (make check starts one).
test:
	CGO_ENABLED=1 $(GO) test -race ./...

# setup-agent.sh is POSIX sh, not bash, and is curl'd onto hosts whose /bin/sh
# is dash or busybox ash. ship's shellcheck lints it (the #!/bin/sh shebang
# selects sh); running the suite under a real dash is what catches the
# bashisms shellcheck misses and bash-as-sh accepts. A ship.yaml step.
test-setup-agent:
	sh test/setup-agent/run.sh
	@if command -v dash >/dev/null 2>&1; then \
		dash test/setup-agent/run.sh; \
	else \
		echo "dash not installed - skipping the dash pass (CI runs it)"; \
	fi

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

# ship:begin (owned by ship init)
# The gate: everything CI checks, configured in ship.yaml. Master's ship, as the pre-push hook and the @master
# workflows run it: its sha asked of GitHub (a proxy lags on @master). A local ship: SHIP_CMD=<go-installed binary>,
# one name for here and the hook, so make check and the push gate never run different ships.
# Exactly refs/heads/master (ls-remote matches by suffix), a 40-hex sha or nothing runs, as in the workflows.
SHIP_CMD ?= sha=$$(git ls-remote https://github.com/trick77/ship refs/heads/master | \
		awk '$$2 == "refs/heads/master" { print $$1 }'); \
	echo "$$sha" | grep -Eqx '[0-9a-f]{40}' || \
		{ echo "cannot resolve ship's master on GitHub: '$$sha' (git ls-remote, error above)"; exit 1; }; \
	go run github.com/trick77/ship@$$sha

.PHONY: check hooks lint

check:
	$(SHIP_CMD) check

hooks:
	$(SHIP_CMD) hooks

# Quick lint while editing, at the version check runs.
lint:
	$(SHIP_CMD) lint
# ship:end
