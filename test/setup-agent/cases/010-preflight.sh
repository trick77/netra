#!/bin/sh
#
# Preflight: cgroup v2, machine-id, tty handling, prompt errexit safety,
# argument parsing.
# Many variables set here are read by the SOURCED setup script, not by this file,
# so shellcheck cannot see the use. The directive is file-wide rather than
# repeated at a dozen assignments.
# shellcheck disable=SC2034
set -eu
# shellcheck source=/dev/null
. "$LIB"

SETUP="$REPO/setup-agent.sh"

# A writable copy of a healthy Debian 12 host root, which cases then break in
# one specific way each.
#
# root-full, not root-debian12: from phase B onwards a run reaches filesystem,
# SMART, sensor and extras detection, and a fixture missing any of those makes
# every one of them emit a skip note — which would make the "a healthy host
# lists nothing as skipped" assertion below vacuously false rather than
# meaningful. The one thing removed is the NFS mount, which root-full carries so
# that 030 and 060 exercise the network-filesystem skip; here it would be the
# single note on an otherwise clean host.
mkroot() {
    _mkroot_dst="$TMP/$1"
    mkdir -p "$_mkroot_dst"
    cp -R "$(fixture root-full)/." "$_mkroot_dst/"
    grep -v ' nfs4 ' "$_mkroot_dst/proc/1/mountinfo" >"$_mkroot_dst/proc/1/mountinfo.tmp"
    mv "$_mkroot_dst/proc/1/mountinfo.tmp" "$_mkroot_dst/proc/1/mountinfo"
    printf '%s\n' "$_mkroot_dst"
}

# Arguments every end-to-end run needs from phase B onwards.
#
# --template-dir keeps the run off the network; --output-dir keeps it out of the
# repository, since the runner's working directory is the repository root and
# OUTPUT_DIR defaults to ./netra-agent; --token avoids the (legitimate) warning
# an absent token produces, which would otherwise be the one thing every
# "nothing was skipped" assertion trips over.
mkshims "$TMP/shims"

# The harness must hand out a PHYSICAL $TMP. On macOS `mktemp -d` returns a path
# under /var/folders/... where /var is a symlink to /private/var, so an
# un-canonicalised fixture root does not prefix-match anything a later `pwd -P`
# or symlink resolution produces, and a `${path#$root}` strip silently leaves
# the root in place. run.sh canonicalises; this asserts it stayed that way.
assert_eq "$TMP" "$(cd "$TMP" && pwd -P)" "\$TMP handed to a case is already a physical path"

# AGENT_TTY is the probe seam for /dev/tty. There is no portable way to detach a
# controlling terminal in a test (no setsid on macOS, and redirecting stdin does
# not touch /dev/tty), so the setup script resolves the terminal through this
# variable and the tests point it at a path that cannot be opened.
NO_TTY=/nonexistent/netra-tty

# There is no --yes any more: the script is interactive, and require_tty fails a
# run with no terminal before any phase. AGENT_ANSWERS_FILE is the seam that
# lets the suite drive the prompts anyway, and it exempts require_tty.
#
# AGENT_UID=0 makes plan_drivetemp take its root branch. Without it the suite
# would exercise whichever branch the machine happened to be on — never root on
# a laptop, never root on a CI runner.
AGENT_UID=0
export AGENT_UID

# An EMPTY answers file for the runs that die in preflight: it satisfies
# require_tty and, because an exhausted file is a hard error, it also proves no
# prompt was reached before the failure.
ANS_EMPTY=$(answers empty)

# --- 1. cgroup v1 (no cgroup.controllers) => hard fail naming cgroup v2 -------
ROOT=$(mkroot cgroupv1)
rm -f "$ROOT/sys/fs/cgroup/cgroup.controllers"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_EMPTY" \
    "$SH" "$SETUP" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "cgroup v1 host exits non-zero"
assert_contains "$RUN_OUT" "cgroup v2" "cgroup v1 failure names cgroup v2"
assert_contains "$RUN_OUT" "systemd.unified_cgroup_hierarchy=1" \
    "cgroup v1 failure gives the kernel command line remedy"

# --- 2. hybrid cgroup (controllers file AND a unified/ directory) => fail -----
# Hybrid mounts cgroup2 at /sys/fs/cgroup/unified while the controllers stay on
# v1, so the container collector would read nothing. It must fail, not pass.
ROOT=$(mkroot cgrouphybrid)
mkdir -p "$ROOT/sys/fs/cgroup/unified"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_EMPTY" \
    "$SH" "$SETUP" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "hybrid cgroup host exits non-zero"
assert_contains "$RUN_OUT" "cgroup v2" "hybrid failure names cgroup v2"
assert_contains "$RUN_OUT" "hybrid" "hybrid failure says hybrid"

# --- 3. /etc/machine-id present but zero length => fail -----------------------
ROOT=$(mkroot emptymachineid)
: >"$ROOT/etc/machine-id"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_EMPTY" \
    "$SH" "$SETUP" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "empty machine-id exits non-zero"
assert_contains "$RUN_OUT" "machine-id" "empty machine-id failure names machine-id"
assert_contains "$RUN_OUT" "empty" "empty machine-id failure says it is empty"

# --- 4a. a fully-accepted run on a healthy host reports nothing skipped -------
#
# The grant flags keep the LAST assertion meaningful: SYS_ADMIN defaults no and
# pid: host is off unless asked for, so a plain run on this NVMe fixture would
# legitimately report skips. "Everything accepted" is --sys-admin --pid-host
# plus a y to the two prompts that remain (drivetemp, the write gate).
ROOT=$(mkroot healthy)
ANS=$(answers healthy y y y)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" AGENT_SHIM_MODPROBE_HWMON="$ROOT/sys/class/hwmon" \
    "$SH" "$SETUP" --sys-admin --pid-host \
    --token nta_test --hub-url https://hub.example \
    --location "Zurich, CH" --provider Hetzner --host-type bare_metal \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 0 "$RUN_RC" "a fully-accepted run completes"
assert_contains "$RUN_OUT" "Summary" "the run reaches the summary"
assert_contains "$RUN_OUT" "compose_v2" "the run detects docker compose v2"
assert_contains "$RUN_OUT" "dpkg" "the run detects dpkg"
assert_not_contains "$RUN_OUT" "Skipped or degraded" \
    "a healthy host with everything accepted lists nothing as skipped"
assert_file_present "$ROOT/etc/modules-load.d/drivetemp.conf" \
    "a drivetemp load that produced a chip is persisted"

# Compose v1 fallback and an unreachable daemon. The shims read these at run
# time, which is why check_docker's three outcomes are reachable without
# rebuilding them.
ROOT=$(mkroot composev1)
ANS=$(answers composev1 y n n n n)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" \
    AGENT_SHIM_COMPOSE_V2_RC=1 "$SH" "$SETUP" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 0 "$RUN_RC" "a host with only docker-compose v1 completes"
assert_contains "$RUN_OUT" "compose_v1" "docker compose v1 is detected as the fallback"

ROOT=$(mkroot nodaemon)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_EMPTY" \
    AGENT_SHIM_INFO_RC=1 "$SH" "$SETUP" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "an unreachable Docker daemon exits non-zero"
assert_contains "$RUN_OUT" "Docker daemon" "the daemon failure says so"

# --- 4b. no terminal is an explicit failure, before any phase runs ------------
#
# There is no unattended mode, and the refusal must not arrive three minutes
# into detection it is about to throw away. AGENT_ANSWERS_FILE is not set here,
# so require_tty is the first thing that speaks.
ROOT=$(mkroot notty)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    "$SH" "$SETUP" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "no terminal exits non-zero"
assert_contains "$RUN_OUT" "no terminal" "no-terminal failure is explicit"
assert_contains "$RUN_OUT" "no unattended mode" \
    "the refusal says plainly that there is no unattended mode"
assert_contains "$RUN_OUT" "compose.yaml.example" \
    "the refusal names what to use for a fleet instead"
assert_not_contains "$RUN_OUT" "Preflight" \
    "the refusal comes before any detection, not after it"
assert_not_contains "$RUN_OUT" "Summary" \
    "no-terminal failure does not silently take defaults and finish"

# AGENT_ANSWERS_FILE is the one exemption, and it is the test seam rather than a
# supported provisioning interface: an env var, not a flag, with positional
# answers that the PROMPT ORDER contract in the script header defines.
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers notty y n n y)" \
    "$SH" "$SETUP" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 0 "$RUN_RC" "an answers file needs no terminal"
assert_contains "$RUN_OUT" "Summary" "the seeded run reaches the end"

# --- 4c. an unsupported OS aborts, because that prompt defaults n -------------
#
# The abort must name its own remedy. The version floors are only the releases
# where cgroup v2 became the default (§12a) and the checks that matter are
# probed directly, so a run on a cgroup-v2 host netra does not recognise BY NAME
# has to stay possible — which it only is if the abort says --unsupported-os
# exists. The answers file says `n`, which is also this prompt's default.
ROOT=$(mkroot unsupporteddefault)
cp "$(fixture os-release)/void" "$ROOT/etc/os-release"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers unsupported n)" \
    "$SH" "$SETUP" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "answering no on an unsupported OS aborts"
assert_contains "$RUN_OUT" "unsupported operating system" "the abort says why"
# The needle SPANS two die arguments on purpose, so it can only match the die
# itself rather than any other mention of the flag — and, since die renders
# "$*", it also pins that the IFS='|' read above left IFS restored.
assert_contains "$(flatten "$RUN_OUT")" "system. Re-run with --unsupported-os" \
    "the abort message itself names the flag that would allow it"
assert_file_absent "$ROOT/out/compose.yaml" "nothing is written after the abort"

# Answering y to the same prompt continues, so the assertion above is about the
# default taken, not about an unreachable prompt.
ROOT=$(mkroot unsupportedy)
cp "$(fixture os-release)/void" "$ROOT/etc/os-release"
ANS=$(answers unsupported y y y y y y)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" "$SH" "$SETUP" \
    --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 0 "$RUN_RC" "answering y to the unsupported-OS prompt continues"
assert_contains "$RUN_OUT" "continuing at operator request" "the run says it continued by consent"

# --- 5. answering n to a mid-sequence prompt does NOT abort under set -eu -----
# netra_ask returns 1 for "no". Under `set -e` a function returning 1 outside an
# if/&&/|| context kills the script, so every call site must be
# `if netra_ask ...; then`. This has to be a full end-to-end run: sourcing the
# script and calling netra_ask inside an `if` disables errexit for the whole
# dynamic extent of the call and would pass regardless.
#
# Four answers, one per prompt, in the order pinned by the PROMPT ORDER contract
# in the setup script header: SYS_ADMIN, drivetemp, pid: host, write gate. An
# exhausted answers file is a hard error in netra_ask, so this count is checked
# by the test rather than assumed.
ROOT=$(mkroot answern)
ANS=$(answers n4 y n n n n)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 0 "$RUN_RC" "answering n mid-sequence does not abort the script"
assert_contains "$RUN_OUT" "Summary" "the script continues past a declined prompt"
assert_contains "$RUN_OUT" "Skipped or degraded" \
    "the finish report renders the skipped-notes block"
assert_contains "$RUN_OUT" "SYS_ADMIN declined" \
    "the declined capability is named in the skipped notes"
# The read-only mounts are NOT prompts any more, so a run that declined
# everything it was asked still has its package database mounted. Asserting the
# positive here is what would catch a resurrected prompt: a re-added package
# question would consume the drivetemp answer and this line would report none.
assert_contains "$RUN_OUT" "package mount:   /var/lib/dpkg" \
    "the package database is mounted without being asked about"

# ...and answering y to the same prompts also completes, so the assertion above
# is about errexit and not about the prompts being unreachable.
ROOT=$(mkroot answery)
ANS=$(answers y4 y y y y y)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 0 "$RUN_RC" "answering y completes"
assert_contains "$RUN_OUT" "Summary" "answering y reaches the summary"

# --- 6. argument parsing ------------------------------------------------------
run_capture env AGENT_TTY="$NO_TTY" "$SH" "$SETUP" --frobnicate
assert_eq 1 "$RUN_RC" "unknown flag exits non-zero"
assert_contains "$RUN_OUT" "--frobnicate" "unknown flag error names the flag"

run_capture env AGENT_TTY="$NO_TTY" "$SH" "$SETUP" --help
assert_eq 0 "$RUN_RC" "--help exits 0"
assert_contains "$RUN_OUT" "Usage" "--help prints usage"

run_capture env AGENT_TTY="$NO_TTY" "$SH" "$SETUP" --token
assert_eq 1 "$RUN_RC" "a value-taking flag with no value exits non-zero"
assert_contains "$RUN_OUT" "--token" "missing-value error names the flag"

run_capture env AGENT_TTY="$NO_TTY" "$SH" "$SETUP" --token t --token-file /f
assert_eq 1 "$RUN_RC" "--token and --token-file together exit non-zero"
assert_contains "$RUN_OUT" "mutually exclusive" "the conflict is explained"

run_capture env AGENT_TTY="$NO_TTY" "$SH" "$SETUP" -h
assert_eq 0 "$RUN_RC" "-h exits 0"
assert_contains "$RUN_OUT" "Usage" "-h prints usage"

# --- netra_exec is the single mutation choke point ----------------------------
AGENT_SOURCED=1
export AGENT_SOURCED
# shellcheck source=/dev/null
. "$SETUP"

netra_exec mkdir -p "$TMP/really-created"
assert_file_present "$TMP/really-created" "netra_exec runs the command it is given"
assert_exit_code 1 netra_exec false \
    'netra_exec propagates the exit status, so the || die at each call site works'

# --- parse_args sets exactly the variables phase B consumes -------------------
# Every one of these names is a contract with the template renderer, so they are
# pinned here rather than left to be discovered by a typo in phase B.
parse_args --force --start --token t1 --hub-url https://h \
    --ref v9.9.9 --template-dir /tpl --output-dir /out \
    --primary-sensor coretemp/Package_id_0 --include-network-fs
assert_eq 1 "$FORCE" "--force sets FORCE"
assert_eq 1 "$START" "--start sets START"
assert_eq "t1" "$TOKEN" "--token sets TOKEN"
assert_eq "" "$TOKEN_FILE" "TOKEN_FILE defaults to empty"
assert_eq "https://h" "$HUB_URL" "--hub-url sets HUB_URL"
assert_eq "v9.9.9" "$REF" "--ref sets REF"
assert_eq "/tpl" "$TEMPLATE_DIR" "--template-dir sets TEMPLATE_DIR"
assert_eq "/out" "$OUTPUT_DIR" "--output-dir sets OUTPUT_DIR"
assert_eq "coretemp/Package_id_0" "$PRIMARY_SENSOR" "--primary-sensor sets PRIMARY_SENSOR"
assert_eq 1 "$INCLUDE_NETWORK_FS" "--include-network-fs sets INCLUDE_NETWORK_FS"

# A REAL file: parse_args now reads the token file rather than deferring it to
# configure(), so a typo in the command line is caught before the operator has
# answered four questions about a run that cannot finish.
printf 'nta_fromfile\n' >"$TMP/tokenfile"
parse_args --token-file "$TMP/tokenfile"
assert_eq "$TMP/tokenfile" "$TOKEN_FILE" "--token-file sets TOKEN_FILE"
assert_eq "nta_fromfile" "$TOKEN" "--token-file is read at parse time"
assert_eq 0 "$FORCE" "FORCE defaults to 0"
assert_eq 0 "$START" "START defaults to 0"
assert_eq 0 "$INCLUDE_NETWORK_FS" "INCLUDE_NETWORK_FS defaults to 0"
assert_eq "./netra-agent" "$OUTPUT_DIR" \
    "OUTPUT_DIR defaults to ./netra-agent, not the working directory itself"
assert_eq "" "$PRIMARY_SENSOR" "PRIMARY_SENSOR defaults to empty (auto)"
assert_contains "$REF" "v" "REF defaults to a version tag, never master"
assert_not_contains "$REF" "master" "REF never defaults to master"
# REF_EXPLICIT is what lets the renderer tell "not given" apart from "given as
# exactly the default": without it the runtime latest-release lookup would
# either never run or silently override an explicit --ref.
assert_eq 0 "$REF_EXPLICIT" "REF_EXPLICIT is 0 when --ref was not passed"
parse_args --ref v1.2.3
assert_eq 1 "$REF_EXPLICIT" "--ref sets REF_EXPLICIT"

# --- the identity values, and the one that is validated -----------------------
parse_args --location "Zurich, CH" --provider Hetzner --host-type vps
assert_eq "Zurich, CH" "$LOCATION" "--location sets LOCATION"
assert_eq "Hetzner" "$PROVIDER" "--provider sets PROVIDER"
assert_eq "vps" "$HOST_TYPE" "--host-type sets HOST_TYPE"
# The hub stores host type as an enum, so a typo is a host that never groups
# with its siblings. Rejected at parse time rather than written to .env.
#
# A SUBSHELL, not assert_exit_code: die() calls `exit 1`, and this case has
# sourced the script, so a bare call would take the case down with it.
if (parse_args --host-type nas) >/dev/null 2>&1; then HT_RC=0; else HT_RC=$?; fi
assert_eq 1 "$HT_RC" "an invalid --host-type is rejected at parse time"

# --- the output directory is not nested inside itself -------------------------
# Running from a directory already named netra-agent means THIS is the
# netra-agent directory; ./netra-agent/netra-agent is nobody's intent.
mkdir -p "$TMP/somewhere/netra-agent"
(
    cd "$TMP/somewhere/netra-agent" || exit 1
    parse_args
    [ "$OUTPUT_DIR" = "." ]
) && OD_RC=0 || OD_RC=1
assert_eq 0 "$OD_RC" "the default output dir collapses to . inside a netra-agent directory"
(
    cd "$TMP/somewhere/netra-agent" || exit 1
    parse_args --output-dir ./netra-agent
    [ "$OUTPUT_DIR" = "./netra-agent" ]
) && OD_RC=0 || OD_RC=1
assert_eq 0 "$OD_RC" "an explicit --output-dir still means exactly what it says"
(
    cd "$TMP/somewhere" || exit 1
    parse_args
    [ "$OUTPUT_DIR" = "./netra-agent" ]
) && OD_RC=0 || OD_RC=1
assert_eq 0 "$OD_RC" "anywhere else the default is still ./netra-agent"

# --- probe-path prefixing -----------------------------------------------------
# Every probe path must pick up AGENT_SETUP_ROOT; explicit overrides must not.
AGENT_SETUP_ROOT="$TMP/probe-root"
AGENT_OSRELEASE_PATH="$TMP/explicit-os-release"
export AGENT_SETUP_ROOT AGENT_OSRELEASE_PATH
init_paths
assert_eq "$TMP/probe-root/etc/machine-id" "$P_MACHINEID" \
    "probe paths are prefixed with AGENT_SETUP_ROOT"
assert_eq "$TMP/probe-root/sys/fs/cgroup" "$P_CGROUP" \
    "cgroup probe root is prefixed"
assert_eq "$TMP/explicit-os-release" "$P_OSRELEASE" \
    "an explicit probe override wins over the prefix"

exit_case
