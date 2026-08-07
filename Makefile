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
