#!/bin/sh
#
# The consent model: --dry-run mutates nothing, .env is protected by --force,
# compose.yaml is not, an unattended run with no terminal fails loudly, and a
# repeated run is byte-identical.
set -eu
# shellcheck source=/dev/null
. "$LIB"

INSTALLER="$REPO/install-agent.sh"
TEMPLATES="$REPO/deploy/agent"
NO_TTY=/nonexistent/netra-tty

mkshims "$TMP/shims"

mkroot() {
    _mkroot_dst="$TMP/$1"
    mkdir -p "$_mkroot_dst"
    cp -R "$(fixture root-full)/." "$_mkroot_dst/"
    printf '%s\n' "$_mkroot_dst"
}

# --- 1. --dry-run creates nothing and starts nothing ---------------------------
ROOT=$(mkroot dryrun)
CWD="$TMP/emptycwd"
mkdir -p "$CWD"
: >"$NETRA_SHIM_LOG"

# A subshell so the case's own working directory is not disturbed. OUTPUT_DIR
# defaults to ./netra-agent, so a --dry-run that leaked would leave it here.
if RUN_OUT=$(cd "$CWD" && env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --dry-run --start --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" 2>&1); then
    RUN_RC=0
else
    RUN_RC=$?
fi
assert_eq 0 "$RUN_RC" "--dry-run exits 0"
assert_eq "" "$(ls -A "$CWD")" "--dry-run creates nothing in the working directory"
assert_file_absent "$CWD/netra-agent" "the default output directory is not created"
assert_file_absent "$ROOT/.netra" "no marker directory is created under --dry-run"
assert_file_absent "$ROOT/mnt/ark/.netra" "no marker directory is created under --dry-run"
assert_contains "$RUN_OUT" "would run:" "--dry-run announces the mutations it did not perform"
assert_contains "$RUN_OUT" "would run: netra_write_compose" "the compose write is announced"
assert_contains "$RUN_OUT" "up -d" "--start under --dry-run announces the start it did not run"

# Every mutation goes through netra_exec, so the docker shim must have been
# called for preflight probes and NOTHING else. Asserting "the log is empty"
# would be wrong: preflight legitimately runs `docker info` and
# `docker compose version` before any consent is needed, since a host with no
# reachable daemon has nothing to consent to.
SHIMLOG=$(cat "$NETRA_SHIM_LOG")
assert_not_contains "$SHIMLOG" "up -d" "--dry-run never actually starts the stack"
assert_eq "" "$(printf '%s\n' "$SHIMLOG" | grep -v '^docker info$' |
    grep -v '^docker compose version$' | grep -v '^$' || true)" \
    "the only docker calls made are the preflight probes"

# --- 2. a real run writes both files ------------------------------------------
ROOT=$(mkroot real)
OUT="$TMP/out"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token nta_first --hub-url https://first.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "the first real run succeeds"
assert_file_present "$OUT/compose.yaml" "compose.yaml is written"
assert_file_present "$OUT/.env" ".env is written"
# The token reaches .env and nothing else. The plan shows **** and no phase
# echoes the value, so a terminal scrollback or a CI log never carries it.
assert_not_contains "$RUN_OUT" "nta_first" "the token value is never printed, only ****"
assert_contains "$RUN_OUT" "****" "the plan shows the token as ****"
cp "$OUT/.env" "$TMP/env.first"
cp "$OUT/compose.yaml" "$TMP/compose.first"

# --- 3. re-running without --force leaves .env byte-identical ------------------
#
# §12a: a re-run inside a provisioning script must not be able to silently
# replace a working token. compose.yaml has no such protection because it is
# derived — every byte of it comes from this run's detection.
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a re-run without --force still succeeds"
assert_contains "$RUN_OUT" "--force" "the refusal names the flag that would allow it"
TESTS_RUN=$((TESTS_RUN + 1))
if cmp -s "$TMP/env.first" "$OUT/.env"; then
    ok ".env is byte-identical after a re-run without --force"
else
    fail ".env was overwritten without --force"
fi
assert_not_contains "$(cat "$OUT/.env")" "nta_second" "the second token never reached .env"
assert_contains "$(cat "$OUT/.env")" "nta_first" "the original token is still there"

# compose.yaml IS overwritten, and the finish output has to say so or an
# operator with hand-edits is surprised by their loss.
assert_contains "$RUN_OUT" "overwritten" "the finish report states the compose/.env asymmetry"
assert_contains "$RUN_OUT" "up -d" "the finish report gives the command that applies the change"

# --- 4. --force overwrites .env ------------------------------------------------
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --force --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a --force run succeeds"
assert_contains "$(cat "$OUT/.env")" "nta_second" "--force replaces the token"
assert_contains "$(cat "$OUT/.env")" "https://second.example" "--force replaces the hub URL"

# --- 5. idempotency: a second identical --force run changes nothing ------------
cp "$OUT/.env" "$TMP/env.force1"
cp "$OUT/compose.yaml" "$TMP/compose.force1"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --force --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "the repeated --force run succeeds"
TESTS_RUN=$((TESTS_RUN + 1))
if cmp -s "$TMP/env.force1" "$OUT/.env"; then
    ok "a repeated --force run produces a byte-identical .env"
else
    fail "a repeated --force run produced a different .env"
fi
TESTS_RUN=$((TESTS_RUN + 1))
if cmp -s "$TMP/compose.force1" "$OUT/compose.yaml"; then
    ok "a repeated run produces a byte-identical compose.yaml"
else
    fail "a repeated run produced a different compose.yaml"
fi

# --- 6. no --yes and no terminal is an explicit failure ------------------------
#
# Never a silent default: an unattended run that quietly declined the package
# mount would produce an agent collecting less than the operator believes.
ROOT=$(mkroot notty)
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-notty"
assert_eq 1 "$RUN_RC" "no --yes and no terminal exits non-zero"
assert_contains "$RUN_OUT" "no terminal" "the failure is explicit about the terminal"
assert_contains "$RUN_OUT" "--yes" "the failure suggests --yes"
assert_file_absent "$TMP/out-notty/compose.yaml" "nothing is written when consent cannot be obtained"

# --- 7. declining the writes leaves the output directory empty -----------------
#
# Prompt order (see the contract in the installer header): SYS_RAWIO, SYS_ADMIN,
# package DB, D-Bus, pid: host, write compose.yaml, write .env.
ROOT=$(mkroot decline)
printf 'n\nn\nn\nn\nn\nn\nn\n' >"$TMP/ans-decline"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$TMP/ans-decline" "$SH" "$INSTALLER" \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-decline"
assert_eq 0 "$RUN_RC" "declining every prompt is not an error"
assert_file_absent "$TMP/out-decline/compose.yaml" "a declined compose.yaml is not written"
assert_file_absent "$TMP/out-decline/.env" "a declined .env is not written"
assert_contains "$RUN_OUT" "Skipped or degraded" "everything declined is reported"

# --- 8. an unattended run with no token warns rather than dying ----------------
#
# The hub mints tokens; the installer never invents one. Dying here would waste
# every answer the operator already gave, so NETRA_TOKEN is written empty and
# the report says the agent will refuse to start until it is filled in.
ROOT=$(mkroot notoken)
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-notoken"
assert_eq 0 "$RUN_RC" "a run with no token still completes"
assert_contains "$RUN_OUT" "refuse to start" "the missing token is called out loudly"
assert_contains "$(cat "$TMP/out-notoken/.env")" "NETRA_TOKEN=" "NETRA_TOKEN is written empty"
assert_not_contains "$RUN_OUT" "nta_" "no token value is ever printed"

# --- 9. --token-file ----------------------------------------------------------
#
# A token pasted into a file by a provisioning system routinely arrives with a
# trailing newline and, from anything Windows-shaped, a CR. A CR that reached
# .env would be part of the value and every request would 401 with nothing in
# the logs to explain it.
ROOT=$(mkroot tokenfile)
printf 'nta_fromfile\r\n' >"$TMP/token.txt"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token-file "$TMP/token.txt" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tokenfile"
assert_eq 0 "$RUN_RC" "--token-file completes"
assert_eq "NETRA_TOKEN=nta_fromfile" \
    "$(grep '^NETRA_TOKEN=' "$TMP/out-tokenfile/.env")" \
    "the CR and the trailing newline are stripped from a token file"

run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token-file "$TMP/nosuch" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tf2"
assert_eq 1 "$RUN_RC" "a missing --token-file is a hard error"
assert_contains "$RUN_OUT" "does not exist" "the missing token file failure says so"

: >"$TMP/token-empty.txt"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token-file "$TMP/token-empty.txt" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tf3"
assert_eq 1 "$RUN_RC" "an empty --token-file is a hard error, not an empty token"

# --- 10. --start actually starts the stack ------------------------------------
#
# Section 1 only proves --start is suppressed under --dry-run. This proves the
# other half: that a real --start reaches docker at all, and does so through
# netra_exec with a -f pointing at the file just rendered (the installer cannot
# cd, so an implicit ./compose.yaml would start the wrong thing or nothing).
ROOT=$(mkroot started)
: >"$NETRA_SHIM_LOG"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --start --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-start"
assert_eq 0 "$RUN_RC" "a --start run succeeds"
assert_contains "$(cat "$NETRA_SHIM_LOG")" "compose -f $TMP/out-start/compose.yaml up -d" \
    "--start runs docker compose against the rendered file"

# The v1 spelling is a different binary, not a different subcommand.
ROOT=$(mkroot startedv1)
: >"$NETRA_SHIM_LOG"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_SHIM_COMPOSE_V2_RC=1 "$SH" "$INSTALLER" --yes --start --token nta_x \
    --hub-url https://h --template-dir "$TEMPLATES" --output-dir "$TMP/out-start1"
assert_eq 0 "$RUN_RC" "a --start run on a compose v1 host succeeds"
assert_contains "$(cat "$NETRA_SHIM_LOG")" \
    "docker-compose -f $TMP/out-start1/compose.yaml up -d" \
    "--start uses docker-compose where that is the only spelling available"
assert_contains "$RUN_OUT" "docker-compose up -d" \
    "the finish report gives the v1 command, not the v2 one"

# --- 11. a container-only host still gets a working compose --------------------
#
# Zero accepted filesystems is legitimate (§6.4). The run must succeed, say so,
# and produce a compose.yaml with no volumes: key rather than an error.
ROOT=$(mkroot containeronly)
cat >"$ROOT/proc/1/mountinfo" <<'EOF'
26 25 0:22 / /proc rw,nosuid - proc proc rw
29 25 0:24 / /run rw,nosuid shared:5 - tmpfs tmpfs rw
33 25 0:44 / /var/lib/docker/overlay2/8a1/merged rw - overlay overlay rw
EOF
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-conly"
assert_eq 0 "$RUN_RC" "a container-only host is not an error"
assert_contains "$RUN_OUT" "none accepted" "the run says no filesystem was accepted"
assert_file_present "$TMP/out-conly/compose.yaml" "a working compose.yaml is still produced"
COMPOSE_BODY=$(grep -v '^[[:space:]]*#' "$TMP/out-conly/compose.yaml")
assert_not_contains "$COMPOSE_BODY" "/netra/fs/" "no marker mount is rendered"
# The Docker socket and the mount table are still mounted, so volumes: stays.
assert_contains "$COMPOSE_BODY" "docker.sock" "the Docker socket is still mounted"

exit_case
