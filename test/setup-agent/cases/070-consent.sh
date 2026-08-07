#!/bin/sh
#
# The consent model: --dry-run mutates nothing, .env is protected by --force,
# compose.yaml is not, a run with no terminal fails loudly, and a repeated run
# is byte-identical.
#
# There is no --yes. Every run here drives its prompts through
# NETRA_ANSWERS_FILE, whose contents are the PROMPT ORDER contract from the
# setup script header written down:
#
#   unsupported OS (unknown distro only) -> SYS_ADMIN (NVMe only)
#     -> drivetemp (SATA and no such chip) -> pid: host -> the write gate
#
# On the root-full fixture that is four: SYS_ADMIN, drivetemp, pid: host, gate.
set -eu
# shellcheck source=/dev/null
. "$LIB"

SETUP="$REPO/setup-agent.sh"
TEMPLATES="$REPO/deploy/agent"
NO_TTY=/nonexistent/netra-tty

mkshims "$TMP/shims"

# plan_drivetemp's root branch, deterministically, on a laptop and a CI runner
# alike. Without it the prompt count would depend on who is running the suite.
NETRA_UID=0
export NETRA_UID

# The four defaults on this fixture: SYS_ADMIN no, drivetemp yes, pid: host no,
# write gate yes. Named rather than repeated so a change to the prompt sequence
# is one edit, not fourteen.
ANS_DEFAULT=$(answers default n y n y)

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
if RUN_OUT=$(cd "$CWD" && env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$SETUP" --dry-run --start --token nta_x --hub-url https://h \
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
assert_contains "$RUN_OUT" "does not install any software" \
    "the banner says up front that this installs nothing"
assert_contains "$RUN_OUT" "compose.yaml   generated" \
    "the banner names the files it is going to write"
assert_contains "$RUN_OUT" "up -d" "--start under --dry-run announces the start it did not run"

# Every mutation goes through netra_exec, so the docker shim must have been
# called for preflight probes and NOTHING else. Asserting "the log is empty"
# would be wrong: preflight legitimately runs `docker info` and
# `docker compose version` before any consent is needed, since a host with no
# reachable daemon has nothing to consent to.
SHIMLOG=$(cat "$NETRA_SHIM_LOG")
assert_not_contains "$SHIMLOG" "up -d" "--dry-run never actually starts the stack"
assert_not_contains "$SHIMLOG" "modprobe" "--dry-run loads no kernel module either"
assert_eq "" "$(printf '%s\n' "$SHIMLOG" | grep -v '^docker info$' |
    grep -v '^docker compose version$' | grep -v '^$' || true)" \
    "the only docker calls made are the preflight probes"

# --- 2. a real run writes both files ------------------------------------------
ROOT=$(mkroot real)
OUT="$TMP/out"
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_first --hub-url https://first.example \
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
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_second --hub-url https://second.example \
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
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a --force run succeeds"
assert_contains "$(cat "$OUT/.env")" "nta_second" "--force replaces the token"
assert_contains "$(cat "$OUT/.env")" "https://second.example" "--force replaces the hub URL"

# --- 5. idempotency: a second identical --force run changes nothing ------------
cp "$OUT/.env" "$TMP/env.force1"
cp "$OUT/compose.yaml" "$TMP/compose.force1"
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --token nta_second --hub-url https://second.example \
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

# --- 6. no terminal is an explicit failure ------------------------------------
#
# Never a silent default: a run that quietly declined pid: host would produce an
# agent collecting less than the operator believes. There is no unattended mode
# to fall back to, so the refusal has to name what to use instead.
ROOT=$(mkroot notty)
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-notty"
assert_eq 1 "$RUN_RC" "no terminal exits non-zero"
assert_contains "$RUN_OUT" "no terminal" "the failure is explicit about the terminal"
assert_contains "$RUN_OUT" "no unattended mode" "the failure says there is no unattended mode"
assert_contains "$RUN_OUT" ".env.example" "the failure names the fleet alternative"
assert_file_absent "$TMP/out-notty/compose.yaml" "nothing is written when consent cannot be obtained"

# --- 7. declining the write gate leaves the host exactly as it was ------------
#
# This is the ordering bug the single gate exists to prevent. The output
# directory and every .netra marker directory used to be created BEFORE the
# write prompts, so declining left a littered host, possibly a .env, and a
# finish report cheerfully printing `cd <dir> && docker compose up -d`.
ROOT=$(mkroot decline)
ANS=$(answers decline n n n n)
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS" "$SH" "$SETUP" \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-decline"
assert_eq 0 "$RUN_RC" "declining every prompt is not an error"
assert_file_absent "$TMP/out-decline/compose.yaml" "a declined compose.yaml is not written"
assert_file_absent "$TMP/out-decline/.env" "a declined .env is not written"
assert_file_absent "$TMP/out-decline" "the output directory itself is not created either"
assert_file_absent "$ROOT/.netra" "no marker directory is created at the filesystem root"
assert_file_absent "$ROOT/mnt/ark/.netra" "and none on the other measured filesystem"
assert_contains "$RUN_OUT" "nothing was changed" "the run says plainly that it changed nothing"
assert_contains "$RUN_OUT" "Skipped or degraded" "everything declined is reported"

# --- 7b. a declined gate must not let --start run a PREVIOUS run's compose ----
#
# The worst shape of the old bug: --start ran `docker compose -f <dir>/compose.yaml
# up -d` regardless, so a declined write started whatever stale file was already
# there, paired with this run's fresh .env.
ROOT=$(mkroot declinestart)
mkdir -p "$TMP/out-stale"
printf 'services: {}\n' >"$TMP/out-stale/compose.yaml"
ANS=$(answers declinestart n n n n)
: >"$NETRA_SHIM_LOG"
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --start \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-stale"
assert_eq 0 "$RUN_RC" "declining the gate with --start is not an error"
assert_not_contains "$(cat "$NETRA_SHIM_LOG")" "up -d" \
    "a declined write gate never starts a stale compose.yaml"
assert_eq "services: {}" "$(cat "$TMP/out-stale/compose.yaml")" \
    "and the stale file is left exactly as it was"

# --- 8. a run with no token warns rather than dying ---------------------------
#
# The hub mints tokens; the setup script never invents one. Dying here would
# waste every answer the operator already gave, so NETRA_TOKEN is written empty
# and the report says the agent will refuse to start until it is filled in. The
# same treatment applies to an empty hub URL, which is equally fatal.
ROOT=$(mkroot notoken)
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-notoken"
assert_eq 0 "$RUN_RC" "a run with no token still completes"
assert_contains "$RUN_OUT" "refuse to start" "the missing token is called out loudly"
assert_contains "$(cat "$TMP/out-notoken/.env")" "NETRA_TOKEN=" "NETRA_TOKEN is written empty"
assert_not_contains "$RUN_OUT" "nta_" "no token value is ever printed"

ROOT=$(mkroot nohuburl)
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --token nta_x \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-nohub"
assert_eq 0 "$RUN_RC" "a run with no hub URL still completes"
assert_contains "$RUN_OUT" "no hub URL" "the missing hub URL is called out"
assert_contains "$(cat "$TMP/out-nohub/.env")" "NETRA_HUB_URL=" "NETRA_HUB_URL is written empty"

# --- 8b. the free-text values are asked for, and each prompt advances ---------
#
# The bug this pins: netra_ask_value used to print its answer for the caller to
# capture with `VAR=$(netra_ask_value ...)`, which runs it in a SUBSHELL - so
# NETRA_VALUE_INDEX never advanced in the parent, every prompt read line 1
# forever, and the host-type validation loop spun on it without end. A single
# values file with four DIFFERENT lines is what catches that: with the bug, all
# four values are the hub URL and the run never terminates.
ROOT=$(mkroot values)
VALS=$(values four https://vals.example "Gravelines, FR" OVH vps)
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" NETRA_VALUES_FILE="$VALS" \
    "$SH" "$SETUP" --token nta_x \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-values"
assert_eq 0 "$RUN_RC" "a run driven by a values file completes"
VALENV=$(cat "$TMP/out-values/.env")
assert_contains "$VALENV" "NETRA_HUB_URL=https://vals.example" "the hub URL is the first value"
assert_contains "$VALENV" "NETRA_LOCATION=Gravelines, FR" "the location is the second"
assert_contains "$VALENV" "NETRA_PROVIDER=OVH" "the provider is the third"
assert_contains "$VALENV" "NETRA_HOST_TYPE=vps" "the host type is the fourth"
# Never asked for, so it stays blank however many values are supplied.
assert_contains "$VALENV" "NETRA_FACILITY=" "the facility is not asked for"

# An invalid host type is re-asked rather than written to .env, and an empty
# answer ends the loop rather than re-taking the rejected value as its default.
ROOT=$(mkroot badhosttype)
VALS=$(values bad https://vals.example "Gravelines, FR" OVH nas "")
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" NETRA_VALUES_FILE="$VALS" \
    "$SH" "$SETUP" --token nta_x \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-badht"
assert_eq 0 "$RUN_RC" "an invalid host type does not abort the run"
assert_contains "$RUN_OUT" "is not one of bare_metal" "the invalid host type is rejected out loud"
assert_eq "NETRA_HOST_TYPE=" "$(grep '^NETRA_HOST_TYPE=' "$TMP/out-badht/.env")" \
    "a rejected host type is left blank rather than written to .env"

# --- 9. --token-file ----------------------------------------------------------
#
# A token pasted into a file by a provisioning system routinely arrives with a
# trailing newline and, from anything Windows-shaped, a CR. A CR that reached
# .env would be part of the value and every request would 401 with nothing in
# the logs to explain it.
ROOT=$(mkroot tokenfile)
printf 'nta_fromfile\r\n' >"$TMP/token.txt"
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token-file "$TMP/token.txt" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tokenfile"
assert_eq 0 "$RUN_RC" "--token-file completes"
assert_eq "NETRA_TOKEN=nta_fromfile" \
    "$(grep '^NETRA_TOKEN=' "$TMP/out-tokenfile/.env")" \
    "the CR and the trailing newline are stripped from a token file"

run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$SETUP" --token-file "$TMP/nosuch" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tf2"
assert_eq 1 "$RUN_RC" "a missing --token-file is a hard error"
assert_contains "$RUN_OUT" "does not exist" "the missing token file failure says so"
# Resolved in parse_args, so a typo in the command line is caught before the
# operator has answered four questions about a run that cannot finish.
assert_not_contains "$RUN_OUT" "Preflight" "an unreadable token file fails before detection"

: >"$TMP/token-empty.txt"
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$SETUP" --token-file "$TMP/token-empty.txt" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tf3"
assert_eq 1 "$RUN_RC" "an empty --token-file is a hard error, not an empty token"

# --- 10. --start actually starts the stack ------------------------------------
#
# Section 1 only proves --start is suppressed under --dry-run. This proves the
# other half: that a real --start reaches docker at all, and does so through
# netra_exec with a -f pointing at the file just rendered (the setup script cannot
# cd, so an implicit ./compose.yaml would start the wrong thing or nothing).
ROOT=$(mkroot started)
: >"$NETRA_SHIM_LOG"
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --start --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-start"
assert_eq 0 "$RUN_RC" "a --start run succeeds"
assert_contains "$(cat "$NETRA_SHIM_LOG")" "compose -f $TMP/out-start/compose.yaml up -d" \
    "--start runs docker compose against the rendered file"

# The v1 spelling is a different binary, not a different subcommand.
ROOT=$(mkroot startedv1)
: >"$NETRA_SHIM_LOG"
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" \
    NETRA_SHIM_COMPOSE_V2_RC=1 "$SH" "$SETUP" --start --token nta_x \
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
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-conly"
assert_eq 0 "$RUN_RC" "a container-only host is not an error"
assert_contains "$RUN_OUT" "none accepted" "the run says no filesystem was accepted"
assert_file_present "$TMP/out-conly/compose.yaml" "a working compose.yaml is still produced"
COMPOSE_BODY=$(grep -v '^[[:space:]]*#' "$TMP/out-conly/compose.yaml")
assert_not_contains "$COMPOSE_BODY" "/netra/fs/" "no marker mount is rendered"
# The Docker socket and the mount table are still mounted, so volumes: stays.
assert_contains "$COMPOSE_BODY" "docker.sock" "the Docker socket is still mounted"

# --- 12. the defaults produce a complete agent, and grant no privilege --------
#
# Taking every prompt's default must never expand privilege — SYS_ADMIN and
# pid: host default n and stay off — while still producing an agent that
# collects everything it can without them. That second half is now carried by
# the read-only mounts, which are NOT prompts at all: the package database, the
# D-Bus socket and SYS_RAWIO are enabled automatically, on the same argument
# that was always made for the Docker socket.
ROOT=$(mkroot yesdefaults)
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" \
    NETRA_SHIM_MODPROBE_HWMON="$ROOT/sys/class/hwmon" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-yesdef"
assert_eq 0 "$RUN_RC" "a defaults run on an NVMe host succeeds"
assert_file_present "$TMP/out-yesdef/compose.yaml" "compose.yaml is written"
assert_file_present "$TMP/out-yesdef/.env" ".env is written"
YESBODY=$(grep -v '^[[:space:]]*#' "$TMP/out-yesdef/compose.yaml")
assert_not_contains "$YESBODY" "SYS_ADMIN" "the default never grants SYS_ADMIN"
assert_not_contains "$YESBODY" "pid: host" "the default never enables pid: host"
assert_not_contains "$YESBODY" "/dev/nvme0" \
    "a declined SYS_ADMIN also drops the NVMe controller from devices:"
assert_contains "$RUN_OUT" "--sys-admin" "the run says which flag would grant SYS_ADMIN"
assert_contains "$RUN_OUT" "--pid-host" "the run says which flag would enable pid: host"
assert_contains "$RUN_OUT" "Skipped or degraded" "the declines reach the finish report"
# The benign half, and the proof that it is no longer asked about: this run's
# answers file has exactly four lines, so a resurrected package or D-Bus prompt
# would consume one and the run would die on an exhausted file.
assert_contains "$YESBODY" "SYS_RAWIO" "SYS_RAWIO is granted automatically"
assert_contains "$YESBODY" "/dev/sda" "the SATA device is still collected"
assert_contains "$YESBODY" "/var/lib/dpkg" "the package database is mounted automatically"
assert_contains "$YESBODY" "system_bus_socket" "the D-Bus socket is mounted automatically"
assert_contains "$YESBODY" "docker.sock" "the Docker socket is mounted"
assert_contains "$YESBODY" "/netra/fs/root" "the marker mount is rendered"
assert_file_present "$ROOT/.netra" "the marker directories are created"

# --- 13. --sys-admin and --pid-host grant explicitly, without prompting --------
#
# The answers files hold exactly the prompts the flag does NOT remove, so a
# --sys-admin that still asked would run one line short and die with "answers
# file exhausted" rather than passing quietly.
ROOT=$(mkroot grantsysadmin)
ANS=$(answers sysadmin y n y)
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --sys-admin \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-sysadmin"
assert_eq 0 "$RUN_RC" "--sys-admin succeeds while consuming no answer of its own"
SABODY=$(grep -v '^[[:space:]]*#' "$TMP/out-sysadmin/compose.yaml")
assert_contains "$SABODY" "SYS_ADMIN" "--sys-admin grants SYS_ADMIN"
assert_contains "$SABODY" "/dev/nvme0" "--sys-admin also brings the NVMe controller back"
assert_not_contains "$SABODY" "pid: host" "--sys-admin does not also enable pid: host"

ROOT=$(mkroot grantpidhost)
ANS=$(answers pidhost n y y)
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --pid-host \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-pidhost"
assert_eq 0 "$RUN_RC" "--pid-host succeeds while consuming no answer of its own"
PHBODY=$(grep -v '^[[:space:]]*#' "$TMP/out-pidhost/compose.yaml")
assert_contains "$PHBODY" "pid: host" "--pid-host enables the host PID namespace"
assert_not_contains "$PHBODY" "SYS_ADMIN" "--pid-host does not also grant SYS_ADMIN"

# --- 15. --unsupported-os suppresses the prompt without silencing the warning --
#
# §12a: the version floors are only the releases where cgroup v2 became the
# default, and the checks that matter are probed directly rather than inferred
# from the distro name. So an unattended install on a cgroup-v2 host netra does
# not recognise BY NAME must remain possible — otherwise "--yes takes the
# default" quietly turns an advisory floor into a hard refusal.
ROOT=$(mkroot unsupportedgrant)
cp "$(fixture os-release)/void" "$ROOT/etc/os-release"
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --unsupported-os \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-unsupported"
assert_eq 0 "$RUN_RC" "--unsupported-os completes on a distro netra does not know"
assert_file_present "$TMP/out-unsupported/compose.yaml" "a working compose.yaml is still produced"
UOBODY=$(grep -v '^[[:space:]]*#' "$TMP/out-unsupported/compose.yaml")
assert_contains "$UOBODY" "docker.sock" "the rendered compose is a real one, not a stub"
assert_contains "$UOBODY" "/netra/fs/root" "the marker mount is rendered on the unknown distro too"

# The flag suppresses the PROMPT, not the DIAGNOSIS. An operator who forced an
# install onto an unrecognised distro must still be told which distro it was and
# what netra knows, both in the scroll and in the finish report they read after.
assert_contains "$RUN_OUT" "not a distribution/version" \
    "--unsupported-os still prints the unsupported-OS warning"
assert_contains "$RUN_OUT" "void Linux" "the warning names the actual distro"
assert_contains "$RUN_OUT" "Debian 11+" "the warning still states the floors netra knows"
assert_contains "$RUN_OUT" "--unsupported-os" "the run says the grant came from the flag"
# The note has to survive to the finish report, not just scroll past. Everything
# after the "Skipped or degraded:" header is that report.
UONOTES=$(printf '%s\n' "$RUN_OUT" | sed -n '/Skipped or degraded:/,$p')
assert_contains "$UONOTES" "not a distribution/version" \
    "the unsupported OS is recorded as a note in the finish report"

# The teeth: this file holds EXACTLY the four prompts that remain once prompt 1
# is gone (SYS_ADMIN, drivetemp, pid: host, the write gate). An
# --unsupported-os that still asked prompt 1 would run the file one line short
# and die with "answers file exhausted".
ROOT=$(mkroot unsupportedcount)
cp "$(fixture os-release)/void" "$ROOT/etc/os-release"
ANS=$(answers unsupported4 y y y y)
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --unsupported-os \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-unscount"
assert_eq 0 "$RUN_RC" "--unsupported-os consumes no answer: prompt 1 is skipped entirely"
assert_not_contains "$RUN_OUT" "exhausted" "the answers file is not run short"
assert_not_contains "$RUN_OUT" "Continue on an unsupported OS?" \
    "the unsupported-OS question is never asked under the flag"

# --- 16. --unsupported-os on a distro netra DOES know is a silent no-op --------
#
# Mirrors --sys-admin on a host with no NVMe, except that this one earns no note
# either: nothing was withheld, so there is nothing to report. A spurious
# warning here would make every provisioning script that passes the flag
# defensively look degraded on a perfectly supported host.
ROOT=$(mkroot unsupportednoop)
run_capture env NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --unsupported-os --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-unsnoop"
assert_eq 0 "$RUN_RC" "--unsupported-os on a supported distro succeeds"
assert_contains "$RUN_OUT" "(supported)" "the OS is still reported as supported"
assert_not_contains "$RUN_OUT" "not a distribution/version" \
    "--unsupported-os invents no warning on a distro netra knows"
assert_not_contains "$RUN_OUT" "--unsupported-os" \
    "an unused --unsupported-os is not mentioned anywhere in the run"

# --- 17. every grant flag is documented ---------------------------------------
run_capture "$SH" "$SETUP" --help
assert_contains "$RUN_OUT" "--sys-admin" "--help documents --sys-admin"
assert_contains "$RUN_OUT" "--location" "--help documents --location"
assert_contains "$RUN_OUT" "--provider" "--help documents --provider"
assert_contains "$RUN_OUT" "--host-type" "--help documents --host-type"
assert_not_contains "$RUN_OUT" "--yes" "--help does not offer an unattended mode"
assert_contains "$RUN_OUT" "no unattended mode" "--help says so out loud"
assert_contains "$RUN_OUT" "--pid-host" "--help documents --pid-host"
assert_contains "$RUN_OUT" "--unsupported-os" "--help documents --unsupported-os"

exit_case
