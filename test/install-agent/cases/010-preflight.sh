#!/bin/sh
#
# Preflight: cgroup v2, machine-id, tty handling, prompt errexit safety,
# argument parsing.
# Many variables set here are read by the SOURCED installer, not by this file,
# so shellcheck cannot see the use. The directive is file-wide rather than
# repeated at a dozen assignments.
# shellcheck disable=SC2034
set -eu
# shellcheck source=/dev/null
. "$LIB"

INSTALLER="$REPO/install-agent.sh"

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

# NETRA_TTY is the probe seam for /dev/tty. There is no portable way to detach a
# controlling terminal in a test (no setsid on macOS, and redirecting stdin does
# not touch /dev/tty), so the installer resolves the terminal through this
# variable and the tests point it at a path that cannot be opened.
NO_TTY=/nonexistent/netra-tty

# --- 1. cgroup v1 (no cgroup.controllers) => hard fail naming cgroup v2 -------
ROOT=$(mkroot cgroupv1)
rm -f "$ROOT/sys/fs/cgroup/cgroup.controllers"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token nta_test --hub-url https://hub.example \
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
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "hybrid cgroup host exits non-zero"
assert_contains "$RUN_OUT" "cgroup v2" "hybrid failure names cgroup v2"
assert_contains "$RUN_OUT" "hybrid" "hybrid failure says hybrid"

# --- 3. /etc/machine-id present but zero length => fail -----------------------
ROOT=$(mkroot emptymachineid)
: >"$ROOT/etc/machine-id"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "empty machine-id exits non-zero"
assert_contains "$RUN_OUT" "machine-id" "empty machine-id failure names machine-id"
assert_contains "$RUN_OUT" "empty" "empty machine-id failure says it is empty"

# --- 4a. --yes with no usable /dev/tty still completes ------------------------
#
# The grant flags are here to keep the LAST assertion meaningful. --yes takes
# each prompt's default, and SYS_ADMIN defaults no, so on this NVMe fixture a
# plain --yes run legitimately reports a skip. "Everything accepted" is now
# spelled --yes --sys-admin --pid-host; that a plain --yes does skip SYS_ADMIN
# is asserted in 070.
ROOT=$(mkroot healthy)
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --sys-admin --pid-host \
    --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 0 "$RUN_RC" "--yes completes with no terminal"
assert_contains "$RUN_OUT" "Summary" "--yes run reaches the summary"
assert_contains "$RUN_OUT" "compose_v2" "--yes run detects docker compose v2"
assert_contains "$RUN_OUT" "dpkg" "--yes run detects dpkg"
assert_not_contains "$RUN_OUT" "Skipped or degraded" \
    "a healthy host with everything accepted lists nothing as skipped"

# Compose v1 fallback and an unreachable daemon. The shims read these at run
# time, which is why check_docker's three outcomes are reachable without
# rebuilding them.
ROOT=$(mkroot composev1)
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_SHIM_COMPOSE_V2_RC=1 "$SH" "$INSTALLER" --yes --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 0 "$RUN_RC" "a host with only docker-compose v1 still installs"
assert_contains "$RUN_OUT" "compose_v1" "docker compose v1 is detected as the fallback"

ROOT=$(mkroot nodaemon)
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_SHIM_INFO_RC=1 "$SH" "$INSTALLER" --yes --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "an unreachable Docker daemon exits non-zero"
assert_contains "$RUN_OUT" "Docker daemon" "the daemon failure says so"

# --- 4b. no --yes and no usable /dev/tty => explicit failure, not a default ---
ROOT=$(mkroot notty)
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "no terminal and no --yes exits non-zero"
assert_contains "$RUN_OUT" "no terminal" "no-terminal failure is explicit"
assert_contains "$RUN_OUT" "--yes" "no-terminal failure suggests --yes"
assert_not_contains "$RUN_OUT" "Summary" \
    "no-terminal failure does not silently take defaults and finish"

# --- 4c. --yes on an unsupported OS aborts, because that prompt defaults n ----
#
# The reach of "--yes takes the default" is not limited to the two capability
# prompts: prompt 1 defaults n as well, so an unattended install on a distro
# netra does not know refuses rather than proceeding. Locked in here because
# nothing else in the suite runs --yes against an unsupported os-release, and
# the old "--yes accepts everything" quietly continued.
#
# The abort must also name its own remedy. The version floors are only the
# releases where cgroup v2 became the default (§12a) and the checks that matter
# are probed directly, so an unattended install on a cgroup-v2 host netra does
# not recognise BY NAME has to stay possible — which it only is if the abort
# tells the operator that --unsupported-os exists.
ROOT=$(mkroot unsupportedyes)
cp "$(fixture os-release)/void" "$ROOT/etc/os-release"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH" "$INSTALLER" --yes --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 1 "$RUN_RC" "--yes on an unsupported OS aborts rather than continuing"
assert_contains "$RUN_OUT" "unsupported operating system" "the abort says why"
# The needle SPANS two die arguments on purpose. netra_ask already prints
# "pass --unsupported-os to grant" as its --yes hint, so a bare "--unsupported-os"
# needle would stay green even if the die message lost the remedy entirely. This
# one can only match the die itself — and, since die renders "$*", it also pins
# that the IFS='|' read above left IFS restored for the join.
assert_contains "$RUN_OUT" "system. Re-run with --unsupported-os" \
    "the abort message itself names the flag that would allow it"
assert_file_absent "$ROOT/out/compose.yaml" "nothing is written after the abort"

# Answering y to the same prompt from an answers file still continues, so the
# assertion above is about the default taken, not about an unreachable prompt.
ROOT=$(mkroot unsupportedy)
cp "$(fixture os-release)/void" "$ROOT/etc/os-release"
# Every prompt accepted. Surplus lines are simply never read; an answers file
# that ran SHORT would die, so this is the safe direction to err in.
printf 'y\ny\ny\ny\ny\ny\ny\ny\ny\n' >"$TMP/answers-unsupported"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$TMP/answers-unsupported" "$SH" "$INSTALLER" \
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
ROOT=$(mkroot answern)
# Seven answers, one per prompt, in the order pinned by the PROMPT ORDER
# contract in the installer header: SYS_RAWIO, SYS_ADMIN, package DB, D-Bus,
# pid: host, write compose.yaml, write .env. An exhausted answers file is a hard
# error in netra_ask, so this count is checked by the test rather than assumed.
printf 'n\nn\nn\nn\nn\nn\nn\n' >"$TMP/answers-n"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$TMP/answers-n" "$SH" "$INSTALLER" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 0 "$RUN_RC" "answering n mid-sequence does not abort the script"
assert_contains "$RUN_OUT" "Summary" "the script continues past a declined prompt"
# These two, and not a bare "package" match: `package manager: dpkg` prints on
# every run including the fully-accepted one, so matching it would pass whether
# or not warn() accumulated into SKIPPED_NOTES and whether or not finish_report
# renders the block. Paired with the assert_not_contains above, this is the only
# assertion in the suite that exercises the warn -> SKIPPED_NOTES -> report path.
assert_contains "$RUN_OUT" "Skipped or degraded" \
    "the finish report renders the skipped-notes block"
assert_contains "$RUN_OUT" "will not run" \
    "the declined package collector is named in the skipped notes"
assert_contains "$RUN_OUT" "package mount:   none" \
    "the declined mount is reported as none"

# ...and answering y to the same prompt also completes, so the assertion above
# is about errexit and not about the prompt being unreachable.
ROOT=$(mkroot answery)
printf 'y\ny\ny\ny\ny\ny\ny\n' >"$TMP/answers-y"
run_capture env NETRA_INSTALL_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    NETRA_ANSWERS_FILE="$TMP/answers-y" "$SH" "$INSTALLER" --token nta_test --hub-url https://hub.example \
    --template-dir "$REPO/deploy/agent" --output-dir "$ROOT/out"
assert_eq 0 "$RUN_RC" "answering y completes"
assert_contains "$RUN_OUT" "Summary" "answering y reaches the summary"

# --- 6. argument parsing ------------------------------------------------------
run_capture env NETRA_TTY="$NO_TTY" "$SH" "$INSTALLER" --frobnicate
assert_eq 1 "$RUN_RC" "unknown flag exits non-zero"
assert_contains "$RUN_OUT" "--frobnicate" "unknown flag error names the flag"

run_capture env NETRA_TTY="$NO_TTY" "$SH" "$INSTALLER" --help
assert_eq 0 "$RUN_RC" "--help exits 0"
assert_contains "$RUN_OUT" "Usage" "--help prints usage"

run_capture env NETRA_TTY="$NO_TTY" "$SH" "$INSTALLER" --token
assert_eq 1 "$RUN_RC" "a value-taking flag with no value exits non-zero"
assert_contains "$RUN_OUT" "--token" "missing-value error names the flag"

run_capture env NETRA_TTY="$NO_TTY" "$SH" "$INSTALLER" --token t --token-file /f
assert_eq 1 "$RUN_RC" "--token and --token-file together exit non-zero"
assert_contains "$RUN_OUT" "mutually exclusive" "the conflict is explained"

run_capture env NETRA_TTY="$NO_TTY" "$SH" "$INSTALLER" -h
assert_eq 0 "$RUN_RC" "-h exits 0"
assert_contains "$RUN_OUT" "Usage" "-h prints usage"

# --- netra_exec is the single mutation choke point ----------------------------
NETRA_SOURCED=1
export NETRA_SOURCED
# shellcheck source=/dev/null
. "$INSTALLER"

DRY_RUN=1
run_capture netra_exec mkdir -p "$TMP/must-not-exist"
assert_eq 0 "$RUN_RC" "netra_exec returns 0 under --dry-run"
assert_contains "$RUN_OUT" "would run: mkdir -p" "netra_exec announces the command"
assert_file_absent "$TMP/must-not-exist" "netra_exec mutates nothing under --dry-run"

DRY_RUN=0
export DRY_RUN
netra_exec mkdir -p "$TMP/really-created"
assert_file_present "$TMP/really-created" \
    "netra_exec actually runs the command when not in dry-run"

# --- parse_args sets exactly the variables phase B consumes -------------------
# Every one of these names is a contract with the template renderer, so they are
# pinned here rather than left to be discovered by a typo in phase B.
parse_args --dry-run --force --start --token t1 --hub-url https://h \
    --ref v9.9.9 --template-dir /tpl --output-dir /out \
    --primary-sensor coretemp/Package_id_0 --include-network-fs
assert_eq 1 "$DRY_RUN" "--dry-run sets DRY_RUN"
assert_eq 1 "$FORCE" "--force sets FORCE"
assert_eq 1 "$START" "--start sets START"
assert_eq 0 "$ASSUME_YES" "ASSUME_YES defaults to 0"
assert_eq "t1" "$TOKEN" "--token sets TOKEN"
assert_eq "" "$TOKEN_FILE" "TOKEN_FILE defaults to empty"
assert_eq "https://h" "$HUB_URL" "--hub-url sets HUB_URL"
assert_eq "v9.9.9" "$REF" "--ref sets REF"
assert_eq "/tpl" "$TEMPLATE_DIR" "--template-dir sets TEMPLATE_DIR"
assert_eq "/out" "$OUTPUT_DIR" "--output-dir sets OUTPUT_DIR"
assert_eq "coretemp/Package_id_0" "$PRIMARY_SENSOR" "--primary-sensor sets PRIMARY_SENSOR"
assert_eq 1 "$INCLUDE_NETWORK_FS" "--include-network-fs sets INCLUDE_NETWORK_FS"

parse_args -y --token-file /tf
assert_eq 1 "$ASSUME_YES" "-y sets ASSUME_YES"
assert_eq "/tf" "$TOKEN_FILE" "--token-file sets TOKEN_FILE"
assert_eq 0 "$DRY_RUN" "DRY_RUN defaults to 0"
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

# --- probe-path prefixing -----------------------------------------------------
# Every probe path must pick up NETRA_INSTALL_ROOT; explicit overrides must not.
NETRA_INSTALL_ROOT="$TMP/probe-root"
NETRA_OSRELEASE_PATH="$TMP/explicit-os-release"
export NETRA_INSTALL_ROOT NETRA_OSRELEASE_PATH
init_paths
assert_eq "$TMP/probe-root/etc/machine-id" "$P_MACHINEID" \
    "probe paths are prefixed with NETRA_INSTALL_ROOT"
assert_eq "$TMP/probe-root/sys/fs/cgroup" "$P_CGROUP" \
    "cgroup probe root is prefixed"
assert_eq "$TMP/explicit-os-release" "$P_OSRELEASE" \
    "an explicit probe override wins over the prefix"

exit_case
