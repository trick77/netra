# Two images from one file. The hub is the LAST stage, so a build without
# --target produces it. The agent is built with --target netra-agent.

# --- Hub: build the SPA ---------------------------------------------------------
# Compiled here, not on the developer's machine: dist/ is gitignored down to an
# empty .gitkeep, so a Go build that skips this stage embeds one empty file. That
# image starts, answers /healthz, passes the compose healthcheck, and serves a
# "UI not built" page where the UI should be.
FROM node:26-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
# vite writes into ../backend/internal/hub/web/dist (the Go embed directory).
RUN npm run build

# --- Go sources: the module and the code, shared by both binaries ---------------
FROM golang:1.27-alpine AS gosrc
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/cmd/ ./cmd/
COPY backend/internal/ ./internal/

# --- Agent: build the binary ----------------------------------------------------
FROM gosrc AS agent-build
# Declared after the module download: it changes on every release.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/trick77/ship/kit/version.Version=${VERSION}" \
      -o /out/netra-agent ./cmd/netra-agent

# --- Agent: runtime -------------------------------------------------------------
# Digest-pinned so the runtime contents are reproducible across rebuilds; the tag
# alone moves.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS netra-agent
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/trick77/netra" \
      org.opencontainers.image.version="$VERSION"
# smartmontools is deliberately unversioned: the base digest pins what it
# resolves to (smartmontools-7.5-r0, smartctl 7.5 2025-04-30 r5714), and Alpine
# keeps only the current revision, so an `=7.5-r0` pin would break the day it
# rolls. ca-certificates: the agent POSTs to the hub over HTTPS.
#
# Nothing for the systemd collector, and nothing would work: systemd is not
# packaged for Alpine, and a host's glibc systemctl will not run against musl.
# The collector speaks D-Bus directly; it needs only the system bus socket.
RUN apk add --no-cache ca-certificates smartmontools
COPY --from=agent-build /out/netra-agent /usr/local/bin/netra-agent
# Root by design: smartctl's ATA/NVMe passthrough ioctls need SYS_RAWIO, and a
# dropped-privilege user gets EACCES on every SMART reading. No EXPOSE: the agent
# runs with network_mode: host and listens on nothing.
ENTRYPOINT ["/usr/local/bin/netra-agent"]

# --- Hub: build the binary ------------------------------------------------------
FROM gosrc AS build
# .dockerignore keeps the checkout's dist/ out of the context, so the ui stage's
# output is the only thing go:embed sees.
COPY --from=ui /src/backend/internal/hub/web/dist ./internal/hub/web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/trick77/ship/kit/version.Version=${VERSION}" \
      -o /out/netra ./cmd/netra

# --- Hub: runtime ---------------------------------------------------------------
# Distroless, non-root: no shell, no package manager. CA certificates are in
# the base (OIDC discovery is HTTPS). The compose healthcheck runs the binary's
# own -healthcheck, there is no wget to call.
FROM gcr.io/distroless/static-debian13:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/trick77/netra" \
      org.opencontainers.image.version="$VERSION"
COPY --from=build /out/netra /usr/local/bin/netra
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/netra"]
