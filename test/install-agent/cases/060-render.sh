#!/bin/sh
#
# Template fetching and rendering: the golden compose, the empty-volumes case,
# env substitution, and the fetch failure message.
# Many variables set here are read by the SOURCED installer, not by this file,
# so shellcheck cannot see the use. The directive is file-wide rather than
# repeated at a dozen assignments.
# shellcheck disable=SC2034
set -eu
# shellcheck source=/dev/null
. "$LIB"

INSTALLER="$REPO/install-agent.sh"
TEMPLATES="$REPO/deploy/agent"
GOLDEN="$(dirname "$0")/../golden"
GOLDEN=$(cd "$GOLDEN" && pwd -P)

# The real PATH, captured BEFORE mkshims prepends a docker that exits 0 for
# everything. `docker compose config` must be answered by the actual binary or
# it proves nothing at all.
REAL_PATH="$PATH"

NO_TTY=/nonexistent/netra-tty
mkshims "$TMP/shims"

# --- 1. a full run renders compose.yaml byte for byte --------------------------
ROOT="$TMP/full"
mkdir -p "$ROOT"
cp -R "$(fixture root-full)/." "$ROOT/"

OUT="$TMP/out"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token nta_testtoken --hub-url https://netra.example.com \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a full run against the root-full fixture succeeds"
assert_file_present "$OUT/compose.yaml" "compose.yaml is written"
assert_file_present "$OUT/.env" ".env is written"

if diff -u "$GOLDEN/full.compose.yaml" "$OUT/compose.yaml" >"$TMP/diff.full" 2>&1; then
    ok "the rendered compose.yaml matches golden/full.compose.yaml byte for byte"
    TESTS_RUN=$((TESTS_RUN + 1))
else
    TESTS_RUN=$((TESTS_RUN + 1))
    fail "the rendered compose.yaml does not match golden/full.compose.yaml"
    sed 's/^/       /' "$TMP/diff.full" >&2
fi

# The marker directories are created for the accepted mounts, on the PROBE side.
assert_file_present "$ROOT/.netra" "the root marker directory is created under the fixture root"
assert_file_present "$ROOT/mnt/ark/.netra" "the ark marker directory is created"

# --- 2. .env ------------------------------------------------------------------
ENVOUT=$(cat "$OUT/.env")
assert_contains "$ENVOUT" "NETRA_HUB_URL=https://netra.example.com" \
    "the hub URL is substituted, slashes and all"
assert_contains "$ENVOUT" "NETRA_TOKEN=nta_testtoken" "the token is substituted"
assert_contains "$ENVOUT" "NETRA_PRIMARY_SENSOR=" \
    "the primary sensor is present but empty (the agent picks at runtime)"
assert_not_contains "$ENVOUT" "NETRA_PRIMARY_SENSOR=coretemp" \
    "an install-time auto-pick is NOT frozen into .env"
# The scrape interval is a fixed 60s constant, so there is no knob for it.
# Matched against the ASSIGNMENTS only: the template's own header comment names
# NETRA_INTERVAL in order to explain why it is absent.
ENVBODY=$(printf '%s\n' "$ENVOUT" | grep -v '^#' || true)
assert_not_contains "$ENVBODY" "NETRA_INTERVAL" "there is no NETRA_INTERVAL assignment in .env"
assert_not_contains "$ENVBODY" "__" "no substitution token is left unreplaced"

# The whole reason render_env is awk and not `sed s///`: a value containing `/`
# or `&` turns a sed expression into something else entirely.
NETRA_SOURCED=1
export NETRA_SOURCED
# shellcheck source=/dev/null
. "$INSTALLER"

NETRA_VAL_HUB_URL='https://h.example/a&b/c'
NETRA_VAL_TOKEN='nta_a&b/c\d'
NETRA_VAL_PRIMARY_SENSOR=''
export NETRA_VAL_HUB_URL NETRA_VAL_TOKEN NETRA_VAL_PRIMARY_SENSOR
ENVOUT=$(render_env "$TEMPLATES/env.tmpl")
assert_contains "$ENVOUT" 'NETRA_HUB_URL=https://h.example/a&b/c' \
    "a value containing & and / survives substitution intact"
assert_contains "$ENVOUT" 'NETRA_TOKEN=nta_a&b/c\d' \
    "a value containing a backslash is not escape-processed"

# --- 3. an empty volumes block still renders valid YAML -----------------------
#
# Zero accepted filesystems is legitimate (§6.4, a container-only host). The
# `volumes:` KEY lives inside the block, so an empty block removes the key
# entirely rather than leaving a dangling null mapping.
NETRA_BLK_VOLUMES=""
NETRA_BLK_DEVICES=""
NETRA_BLK_CAP_ADD=""
NETRA_BLK_PID=""
export NETRA_BLK_VOLUMES NETRA_BLK_DEVICES NETRA_BLK_CAP_ADD NETRA_BLK_PID
render_template "$TEMPLATES/compose.yaml.tmpl" >"$TMP/empty.yaml"
if diff -u "$GOLDEN/empty-volumes.compose.yaml" "$TMP/empty.yaml" >"$TMP/diff.empty" 2>&1; then
    ok "an empty block deletes its marker line entirely"
    TESTS_RUN=$((TESTS_RUN + 1))
else
    TESTS_RUN=$((TESTS_RUN + 1))
    fail "the empty render does not match golden/empty-volumes.compose.yaml"
    sed 's/^/       /' "$TMP/diff.empty" >&2
fi
# Comment lines are stripped first: the template's header explains both keys and
# quotes a marker name, so matching the whole file would test the documentation.
EMPTYBODY=$(grep -v '^[[:space:]]*#' "$TMP/empty.yaml" || true)
assert_not_contains "$EMPTYBODY" "volumes:" "no dangling volumes: key remains"
assert_not_contains "$EMPTYBODY" "cap_add:" "no dangling cap_add: key remains"
assert_not_contains "$EMPTYBODY" "devices:" "no dangling devices: key remains"
assert_not_contains "$EMPTYBODY" "pid:" "no dangling pid: key remains"
assert_not_contains "$EMPTYBODY" "#__NETRA_" "no marker line survives"

# --- 4. blocks travel through ENVIRON, never `awk -v` --------------------------
#
# `awk -v x="$block"` runs the value through awk's escape processing, so a mount
# point containing the literal text \040 would silently become a space on its
# way into compose.yaml — undoing the decoding the mountinfo parser took care to
# get right one step earlier.
# SC2089/SC2090: the quotes below are YAML data bound for compose.yaml, not
# shell quoting, and the variable is only exported for awk's ENVIRON.
# shellcheck disable=SC2089
NETRA_BLK_VOLUMES='    volumes:
      - type: bind
        source: "/mnt/lit\040eral/.netra"
        target: /netra/fs/lit-eral
        read_only: true
'
# shellcheck disable=SC2090
export NETRA_BLK_VOLUMES
RENDERED=$(render_template "$TEMPLATES/compose.yaml.tmpl")
assert_contains "$RENDERED" 'source: "/mnt/lit\040eral/.netra"' \
    'a literal \040 in a block reaches compose.yaml unchanged'

# --- 5. fetch_template failure names the URL and both escape hatches ----------
#
# This is the installer's most likely field failure — a proxy, an air-gapped
# host, a tag that does not exist. For the person reading it the message IS the
# remedy, so it is asserted rather than left to chance.
SCRATCH_DIR="$TMP/scratch"
mkdir -p "$SCRATCH_DIR"
TEMPLATE_DIR=""
REF="v9.9.9-nope"
NETRA_SHIM_CURL_RC=22
export NETRA_SHIM_CURL_RC
run_capture fetch_template compose.yaml.tmpl
assert_eq 1 "$RUN_RC" "a failed template download exits non-zero"
assert_contains "$RUN_OUT" \
    "https://raw.githubusercontent.com/trick77/netra/v9.9.9-nope/deploy/agent/compose.yaml.tmpl" \
    "the failure names the exact URL it tried"
assert_contains "$RUN_OUT" "--ref" "the failure names --ref"
assert_contains "$RUN_OUT" "--template-dir" "the failure names --template-dir"

# An empty 200 is a failure too: a proxy that returns a courtesy page with no
# body would otherwise render an empty compose.yaml.
NETRA_SHIM_CURL_RC=0
NETRA_SHIM_CURL_BODY=""
export NETRA_SHIM_CURL_RC NETRA_SHIM_CURL_BODY
run_capture fetch_template compose.yaml.tmpl
assert_eq 1 "$RUN_RC" "an empty download is a failure, not an empty compose.yaml"
assert_contains "$RUN_OUT" "empty" "the empty-download failure says so"

# --- 5b. resolve_ref pins a tag at run time, and NEVER master ------------------
#
# Without --ref the installer resolves the latest release tag from the GitHub
# API. This is the code path that enforces "never master" (§12a: a mid-refactor
# template must not be able to land on a production host), and every end-to-end
# case above passes --template-dir, which short-circuits it — so it is exercised
# directly here or not at all.
#
# The body is compact single-line JSON, which is what the API actually returns.
TEMPLATE_DIR=""
REF_EXPLICIT=0
REF="v$NETRA_INSTALLER_VERSION"
NETRA_SHIM_CURL_RC=0
# SC2089/SC2090: the quotes are JSON syntax in a data string handed to a shim,
# not shell quoting, and the variable is only exported into the environment.
# shellcheck disable=SC2089
NETRA_SHIM_CURL_BODY='{"url":"https://api.github.com/repos/trick77/netra/releases/1","id":1,"tag_name":"v1.2.3","name":"1.2.3","draft":false}'
# shellcheck disable=SC2090
export NETRA_SHIM_CURL_RC NETRA_SHIM_CURL_BODY
resolve_ref
assert_eq "v1.2.3" "$REF" "the latest release tag is resolved at run time"

# A failed lookup falls back to the installer's own version tag, which is still
# a tag. It must never fall back to a branch.
REF="v$NETRA_INSTALLER_VERSION"
SKIPPED_NOTES=""
NETRA_SHIM_CURL_RC=22
export NETRA_SHIM_CURL_RC
resolve_ref
assert_eq "v$NETRA_INSTALLER_VERSION" "$REF" "a failed lookup keeps the version tag"
assert_not_contains "$REF" "master" "a failed lookup never falls back to master"
assert_contains "$SKIPPED_NOTES" "--ref" "the failed lookup warns and names --ref"

# An explicit --ref wins even when the API would answer.
REF="v0.0.1-pinned"
REF_EXPLICIT=1
NETRA_SHIM_CURL_RC=0
export NETRA_SHIM_CURL_RC
resolve_ref
assert_eq "v0.0.1-pinned" "$REF" "an explicit --ref is never overridden by the API lookup"

# --template-dir short-circuits the lookup entirely: an air-gapped install must
# make no network call at all.
TEMPLATE_DIR="$TEMPLATES"
REF_EXPLICIT=0
REF="v$NETRA_INSTALLER_VERSION"
: >"$NETRA_SHIM_LOG"
resolve_ref
assert_eq "" "$(grep 'api.github.com' "$NETRA_SHIM_LOG" || true)" \
    "--template-dir makes no network call at all"
TEMPLATE_DIR=""

# --- 5c. an env value that cannot be represented is rejected ------------------
#
# Values are written UNQUOTED, because compose's env_file parser takes the rest
# of the line literally. A newline therefore has no representation at all. This
# check had a live defect — `case "$v" in *"$(printf '\n')"*` collapses to `**`
# because command substitution strips trailing newlines, so it matched every
# value — which is why the reject side is asserted rather than assumed.
run_capture _env_value HUB_URL "$(printf 'https://h\nEVIL=1')"
assert_eq 1 "$RUN_RC" "a value containing a newline is rejected"
assert_contains "$RUN_OUT" "newline" "the rejection says why"
run_capture _env_value HUB_URL "$(printf 'https://h\rx')"
assert_eq 1 "$RUN_RC" "a value containing a carriage return is rejected"
run_capture _env_value HUB_URL "https://ordinary.example/path?a=b&c=d"
assert_eq 0 "$RUN_RC" "an ordinary value is accepted"

# --- 6. the rendered compose is accepted by docker compose --------------------
#
# `docker compose config` validates SYNTAX and schema only. It never stats a
# bind source, so this proves the file parses and the keys are legal — NOT that
# /mnt/ark/.netra exists on the machine running the test. That is exactly the
# check wanted here.
#
# It runs against the REAL docker, outside the PATH shims (which exit 0 for
# everything and would make this assertion vacuous), and is skipped rather than
# failed where docker is not installed.
if PATH="$REAL_PATH" docker compose version >/dev/null 2>&1; then
    mkdir -p "$TMP/cfg"
    cp "$OUT/compose.yaml" "$TMP/cfg/compose.yaml"
    # config errors on a missing env_file, so both rendered files go in.
    cp "$OUT/.env" "$TMP/cfg/.env"
    if RUN_OUT=$(PATH="$REAL_PATH" docker compose -f "$TMP/cfg/compose.yaml" config 2>&1); then
        RUN_RC=0
    else
        RUN_RC=$?
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
    if [ "$RUN_RC" = 0 ]; then
        ok "the rendered compose.yaml passes docker compose config"
    else
        fail "docker compose config rejected the rendered compose.yaml"
        printf '%s\n' "$RUN_OUT" | sed 's/^/       /' >&2
    fi
else
    printf 'skip (docker compose is not available: config validation not run)\n'
fi

exit_case
