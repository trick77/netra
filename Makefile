GO      ?= go
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

# Deferred expansion (=, not :=) so VERSION and COMMIT are read when a build
# rule runs rather than when this line is parsed. With := they expanded before
# the assignments below existed, and -X stamped both symbols to the empty
# string — which overrides the "dev"/"unknown" defaults in internal/buildinfo,
# so every shipped binary reported an empty version.
LDFLAGS = -s -w -X github.com/trick77/netra/internal/buildinfo.version=$(VERSION) \
                -X github.com/trick77/netra/internal/buildinfo.commit=$(COMMIT)

.PHONY: test test-integration test-shell build build-hub build-agent build-sim proto fmt vet check ui ui-test

test:
	$(GO) test ./...

# Integration tests are skipped unless NETRA_TEST_DSN points at a TimescaleDB.
# -p 1 is mandatory: store.OpenTest drops the shared public schema, so two
# package binaries running in parallel race on the same database.
test-integration:
	NETRA_TEST_DSN=$${NETRA_TEST_DSN:-postgres://netra:netra@127.0.0.1:5432/netra_test} \
		$(GO) test -p 1 ./internal/hub/... -run Integration -v

# setup-agent.sh is POSIX sh, not bash, and is curl'd onto hosts whose /bin/sh
# is dash or busybox ash. Linting with -s sh and then running the suite under a
# real dash is the pair that catches bashisms: shellcheck alone misses some, and
# bash-as-sh accepts nearly all of them.
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

build: build-hub build-agent

ui:
	cd ui && npm ci && npm run build

ui-test:
	cd ui && npm ci && npm run test

build-hub: ui
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o bin/netra ./cmd/netra

build-agent:
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o bin/netra-agent ./cmd/netra-agent

# netra-sim fills a hub with a fake fleet for development. Deliberately NOT
# part of `build`, of either Containerfile, or of the release workflow: it
# registers hosts, mints tokens and writes three months of invented history,
# none of which belongs anywhere near a production hub.
build-sim:
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o bin/netra-sim ./cmd/netra-sim

proto:
	$(GO) run github.com/bufbuild/buf/cmd/buf@v1.47.2 generate

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

check: vet test test-shell
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi
