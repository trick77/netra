# The SPA is compiled here, not on the developer's machine. internal/hub/web
# embeds dist/ with go:embed, and dist/ is gitignored down to an empty
# .gitkeep -- so a build stage that goes straight to `go build` produces a
# binary whose embedded FS holds one empty file. That image starts, answers
# /api/health, passes the compose healthcheck, and serves an http.FileServer
# directory listing where the UI should be. Nothing anywhere reports an error.
# This stage is what makes the released image and `make build-hub` agree.
FROM node:22-alpine AS ui

WORKDIR /src/ui

# Manifests first so the npm layer survives any source edit, the same shape as
# the go mod download layer below.
COPY ui/package.json ui/package-lock.json ./
RUN npm ci

COPY ui/ ./

# vite.config.ts writes to ../internal/hub/web/dist, i.e. /src/internal/... —
# outside this WORKDIR on purpose, so the path matches the checkout exactly and
# the config needs no container-specific branch.
RUN npm run build

FROM golang:1.26-alpine AS build

# Both are mandatory for a release image. The Makefile derives COMMIT from
# `git rev-parse`, but the build context carries no .git (see .dockerignore)
# and the builder has no git binary, so an unpassed COMMIT would stamp
# "unknown" into a shipped binary. The defaults keep a bare `docker build`
# working and match internal/buildinfo's own fallbacks.
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src

# Manifests first so the module download layer survives any source edit.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# After COPY . ., because that copies the gitignored-but-tracked empty dist/
# and would otherwise bury the built SPA underneath it.
COPY --from=ui /src/internal/hub/web/dist ./internal/hub/web/dist

# The cache mounts are repeated here on purpose: mount contents never land in
# the image layer, so without /go/pkg/mod the modules fetched above are gone.
# -ldflags mirrors the Makefile exactly; drift here means the CI version-
# stamping guard passes while the image reports something else.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w -X github.com/trick77/netra/internal/buildinfo.version=$VERSION -X github.com/trick77/netra/internal/buildinfo.commit=$COMMIT" \
      -o /out/netra ./cmd/netra

# Digest-pinned so the runtime contents are reproducible across rebuilds; the
# tag alone moves. This is a multi-arch index, so amd64 and arm64 both resolve.
FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

# ARG does not cross a FROM boundary, so these are re-declared purely to feed
# the labels below.
ARG VERSION=dev
ARG COMMIT=unknown

LABEL org.opencontainers.image.source="https://github.com/trick77/netra" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$COMMIT"

# Do NOT switch this to FROM scratch or strip busybox: the shipped compose
# healthcheck is `wget -qO- http://127.0.0.1:8080/api/health`, which relies on
# Alpine's built-in busybox wget applet. Removing it makes the container report
# unhealthy forever with no other symptom.
RUN apk add --no-cache ca-certificates \
 && adduser -D -H -u 10001 netra

COPY --from=build /out/netra /usr/local/bin/netra

USER netra
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/netra"]
