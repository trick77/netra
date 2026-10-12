#!/bin/sh
# The agent and the hub share one go.mod, so nothing in the compiler stops a collector from importing
# internal/hub/store: it would link pgx into the agent image and every build would stay green.
#
# One direction only: internal/hub/httpapi/e2e_test.go drives a real agent client against the hub router, and
# `go list -deps` reports the binary's imports, not test-only ones.
#
# Fails closed: the list is captured before it is searched, so a `go list` that fails (a renamed cmd/ path)
# aborts under set -e instead of grep finding nothing and passing.
set -eu
deps="$(go -C backend list -deps ./cmd/netra-agent)"
if printf '%s\n' "$deps" | grep -E 'netra/internal/(hub|devtools)|jackc/pgx'; then
	echo "agent imports: the agent binary must not depend on hub packages or pgx" >&2
	exit 1
fi
