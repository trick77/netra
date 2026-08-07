#!/bin/sh
#
# netra agent installer.
#
#   curl -fsSL https://raw.githubusercontent.com/trick77/netra/master/install-agent.sh | sh
#   sh install-agent.sh --help
#
# Detects what this host actually has, asks before changing anything, and (from
# phase B onwards) renders the agent's compose.yaml and .env from templates.
# See docs/superpowers/specs/2026-08-07-netra-design.md §12a.
#
# ============================================================================
# PROBE PATHS vs EMIT PATHS — read this before adding any path
# ============================================================================
# Every path in this script is exactly one of two kinds:
#
#   PROBE path — read during detection. It is resolved through _p(), so tests
#                can redirect it under NETRA_INSTALL_ROOT at a fixture tree.
#
#   EMIT path  — written into the rendered compose.yaml/.env. It must stay the
#                REAL host path, always, with no prefix.
#
# NETRA_INSTALL_ROOT applies ONLY when resolving a probe variable. It must never
# reach a string that goes into a template. Prefix a marker directory on its way
# into compose.yaml and the installed agent measures /tmp/fixture/mnt/ark/.netra
# — a path that does not exist on the host — and silently reports nothing.
#
# The rule in one line: _p() on the way IN, never on the way OUT.
#
# ============================================================================
# POSIX MECHANICS — decided once, do not relitigate per function
# ============================================================================
# This file targets `#!/bin/sh` and must pass `shellcheck -s sh` with zero
# findings and run under dash. It is ONE file on purpose: §12a requires
# `curl ... | sh`, and that process has no sibling file to source.
#
# * set -eu, never -o pipefail (not POSIX).
# * `local` is not POSIX (SC3043). Function-scoped variables are globals named
#   `_<abbreviated fnname>_<var>`, e.g. `_ask_reply` in netra_ask. The prefix is
#   the ownership claim: a function only ever assigns names carrying its own
#   prefix. Shared state is UPPERCASE (DRY_RUN, P_CGROUP, SKIPPED_NOTES).
# * NEVER `cmd | while read` — the loop runs in a subshell and the accumulator
#   is lost when it exits. Use a heredoc-fed loop instead:
#       while IFS= read -r l; do ...; done <<EOF
#   $(producer)
#   EOF
# * No arrays. Multi-valued state is a newline-delimited string, split with a
#   heredoc-fed loop and IFS='|' for fields.
# * printf, never echo. No `read -p`. No `readlink -f`. No `${var,,}` (use tr).
# * No `.`/`source` of anything from the host. /etc/os-release is untrusted
#   input this installer is validating; it is parsed with awk.
#
# ============================================================================
# curl | sh CONSEQUENCES
# ============================================================================
# * stdin IS this script. `read` from stdin returns the script's own bytes, so
#   ALL prompting reads $P_TTY (/dev/tty), never stdin.
# * If $P_TTY is unreadable and --yes was not given, the installer FAILS with an
#   explicit message rather than assuming an answer: a run that quietly said
#   "no" to the package mount would produce an agent that collects less than the
#   operator thinks it does. --yes IS that consent, and it takes each prompt's
#   documented default — announced on stdout, never silently.
#
# ============================================================================
# netra_ask AND errexit — the sharp edge
# ============================================================================
# netra_ask returns 1 to mean "the operator said no", which is a normal outcome,
# not an error. Under `set -e` a function returning non-zero outside an
# if/while/&&/|| context terminates the script. EVERY call site must therefore
# be written `if netra_ask "..." y; then ... else ... fi`. Never a bare call,
# never `netra_ask ... || handler` with a handler that can itself fail.
#
# The rule binds in UNATTENDED runs too, which it did not always: --yes takes
# each prompt's default, so every `n`-default prompt now returns 1 under --yes.
# A bare call at one of those sites would kill a --yes run, and no interactive
# test would ever see it.
#
# The mirror-image trap, which phase B will meet as it adds detectors: a
# capturing assignment `X=$(detect_something)` DOES propagate a die() correctly
# under a bare `set -e` (DOCKER_COMPOSE=$(check_docker) relies on this), but put
# that same assignment inside an if/&&/|| and errexit is suspended — the
# subshell's exit 1 is swallowed, the error message prints, and the script sails
# on with X empty. Capture at statement level; test the value afterwards.

# ============================================================================
# PROMPT ORDER — a contract, not an implementation detail
# ============================================================================
# Every prompt reads its answer from the same sequence, and NETRA_ANSWERS_FILE
# in the tests is that sequence written down. Reordering these silently rewrites
# what every answers file in test/install-agent/cases/ means: line 3 stops being
# "no to SYS_ADMIN" and becomes "no to the package mount", and the tests still
# pass while asserting something else. Change the order only by changing the
# answers files with it.
#
#   1. continue on an unsupported OS   (only when the distro is unrecognised)
#   2. SYS_RAWIO                       (only when a SATA/unknown drive exists)
#   3. SYS_ADMIN                       (only when NVMe exists; default NO)
#   4. pin the primary sensor          (only on a tie between equal-ranked chips)
#   5. mount the package database      (only when dpkg/apk was found)
#   6. mount the D-Bus socket          (only when the socket exists)
#   7. pid: host                       (always; default NO)
#   8. write compose.yaml
#   9. write .env                      (skipped entirely when it exists without --force)
#
# --unsupported-os, --sys-admin and --pid-host REMOVE prompts 1, 3 and 7 from
# this sequence: the answer is taken without asking, so nothing is consumed for
# them. A case that passes any of these flags and an answers file must drop the
# matching line, or every answer after it means something else.
#
# Detection runs to completion BEFORE any of these, which is what makes "detect
# first, then ask" (§12a) true rather than aspirational: an unwritable mount
# point is already demoted to a note by the time prompt 2 is asked, instead of
# failing halfway through the mutating phase.
#
# Starting the stack is a FLAG (--start), not a prompt. An operator who passed
# it has already consented.
#

set -eu

NETRA_INSTALLER_VERSION="0.1.0"

# ---------------------------------------------------------------------------
# Shared state
# ---------------------------------------------------------------------------

# "" in production. Tests point it at a fixture tree. PROBE paths only.
NETRA_INSTALL_ROOT="${NETRA_INSTALL_ROOT:-}"

# Newline-delimited notes for the finish report: everything that was skipped,
# declined or degraded, so the operator sees what the agent will NOT collect.
SKIPPED_NOTES=""

# Consumed answers when NETRA_ANSWERS_FILE is set (test seam only).
NETRA_ANSWER_INDEX=0

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

# die MSG... — one-line error on stderr, exit 1.
die() {
    printf 'install-agent: %s\n' "$*" >&2
    exit 1
}

# warn MSG... — a degradation the operator must know about. Also accumulates
# into SKIPPED_NOTES so the finish report repeats it after the scroll.
warn() {
    printf 'install-agent: warning: %s\n' "$*" >&2
    if [ -n "$SKIPPED_NOTES" ]; then
        SKIPPED_NOTES="$SKIPPED_NOTES
$*"
    else
        SKIPPED_NOTES="$*"
    fi
}

info() {
    printf '%s\n' "$*"
}

step() {
    printf '\n==> %s\n' "$*"
}

# ---------------------------------------------------------------------------
# The single mutation choke point
# ---------------------------------------------------------------------------

# netra_exec CMD... — runs CMD, or announces it and does nothing under
# --dry-run. EVERY mkdir, file write and `docker compose up -d` goes through
# this. If a mutation is not routed through netra_exec then --dry-run is a lie.
netra_exec() {
    if [ "${DRY_RUN:-0}" = 1 ]; then
        printf '  would run: %s\n' "$*"
        return 0
    fi
    "$@"
}

# ---------------------------------------------------------------------------
# Prompting
# ---------------------------------------------------------------------------

# netra_ask QUESTION DEFAULT [GRANT_FLAG] — DEFAULT is `y` or `n`. Returns 0
# for yes, 1 for no. GRANT_FLAG is optional and names the flag that grants this
# prompt outright; it only shapes the message printed under --yes.
#
# CALL SITES MUST BE `if netra_ask ...; then` — see the errexit note in the
# header. A bare call kills the script the moment the operator says no.
#
# --yes takes each prompt's DEFAULT — it does NOT accept everything. A
# provisioning script must never silently expand privilege, so the prompts that
# default `n` (SYS_ADMIN, pid: host) are DECLINED under --yes; an operator who
# wants them says so by name with --sys-admin / --pid-host. Everything benign
# defaults `y` and is accepted, so an unattended run still produces a complete
# agent.
#
# NETRA_ANSWERS_FILE is a test seam: each call consumes the next line of that
# file (an exhausted file is a hard error, not a silent default). Otherwise a
# readable $P_TTY is required.
netra_ask() {
    _ask_q="$1"
    _ask_def="$2"
    _ask_flag="${3:-}"
    if [ "$_ask_def" = y ]; then
        _ask_hint='[Y/n]'
    else
        _ask_hint='[y/N]'
    fi

    if [ "${ASSUME_YES:-0}" = 1 ]; then
        if [ -n "$_ask_flag" ]; then
            printf '%s %s %s (--yes takes the default; pass %s to grant)\n' \
                "$_ask_q" "$_ask_hint" "$_ask_def" "$_ask_flag"
        else
            printf '%s %s %s (--yes takes the default)\n' \
                "$_ask_q" "$_ask_hint" "$_ask_def"
        fi
        # Explicit branches, not `[ "$_ask_def" = y ]` as the last command: a
        # bare test that fails is a command that failed, and errexit is only
        # suspended here because every call site is an `if`. Spelling out the
        # returns keeps that independent of the caller.
        if [ "$_ask_def" = y ]; then
            return 0
        fi
        return 1
    fi

    while :; do
        if [ -n "${NETRA_ANSWERS_FILE:-}" ]; then
            # sed -n "${n}p", not head/tail rewriting: rewriting would destroy
            # the test's own input and break on a read-only file.
            NETRA_ANSWER_INDEX=$((NETRA_ANSWER_INDEX + 1))
            _ask_reply=$(sed -n "${NETRA_ANSWER_INDEX}p" "$NETRA_ANSWERS_FILE")
            if [ -z "$_ask_reply" ]; then
                die "NETRA_ANSWERS_FILE exhausted at answer $NETRA_ANSWER_INDEX (question: $_ask_q)"
            fi
            printf '%s %s %s\n' "$_ask_q" "$_ask_hint" "$_ask_reply"
        else
            if [ ! -r "$P_TTY" ]; then
                die "no terminal available to ask '$_ask_q' ($P_TTY is not readable)." \
                    "Re-run with --yes to take every prompt's default, or run the installer" \
                    "from a terminal." \
                    "Refusing to silently take defaults."
            fi
            printf '%s %s ' "$_ask_q" "$_ask_hint"
            if ! IFS= read -r _ask_reply <"$P_TTY"; then
                die "could not read an answer from $P_TTY; re-run with --yes"
            fi
        fi

        _ask_norm=$(printf '%s' "$_ask_reply" | tr '[:upper:]' '[:lower:]')
        if [ -z "$_ask_norm" ]; then
            _ask_norm="$_ask_def"
        fi
        case "$_ask_norm" in
        y | yes) return 0 ;;
        n | no) return 1 ;;
        esac

        if [ -n "${NETRA_ANSWERS_FILE:-}" ]; then
            die "NETRA_ANSWERS_FILE line $NETRA_ANSWER_INDEX is '$_ask_reply', expected y or n"
        fi
        printf 'please answer y or n\n'
    done
}

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------

usage() {
    cat <<'EOF'
Usage: install-agent.sh [options]

Detects this host's capabilities and installs the netra agent as a Docker
Compose stack. Nothing is created, written or started until you agree.

Options:
  -y, --yes                Take every prompt's DEFAULT (unattended install).
                           Benign prompts default yes; SYS_ADMIN, pid: host and
                           "Continue on an unsupported OS?" default no and are
                           DECLINED. Say yes to those by name with the three
                           flags below.
      --sys-admin          Grant SYS_ADMIN without prompting (NVMe SMART health
                           and wear). A no-op with a note if there is no NVMe.
      --pid-host           Enable `pid: host` without prompting. This exposes
                           every process's cmdline and environ to the agent.
      --unsupported-os     Continue on a distro netra does not recognise,
                           without prompting. The version floors are only where
                           cgroup v2 became the default and the real checks are
                           probed directly, so this is how an unattended install
                           reaches a cgroup v2 host netra has no name for. The
                           warning is still printed and still reported; only the
                           prompt goes away. A no-op on a known distro.
      --dry-run            Print the full plan and touch nothing.
      --force              Required to overwrite an existing .env.
      --start              Run `docker compose up -d` at the end.
      --token VALUE        Agent token minted by the hub (starts with nta_).
      --token-file PATH    Read the agent token from PATH instead.
      --hub-url VALUE      Hub base URL, e.g. https://netra.example.com
      --ref VALUE          Git ref to fetch templates from. Without it the
                           latest release tag is resolved at run time; never
                           master, in either case.
      --template-dir PATH  Use local template files instead of fetching them
                           (no network at all).
      --output-dir PATH    Where compose.yaml and .env are written
                           (default: ./netra-agent).
      --primary-sensor VALUE
                           Override automatic primary-sensor selection.
      --include-network-fs Also offer NFS/CIFS/SMB filesystems.
  -h, --help               Print this and exit.
EOF
}

# _need_val FLAG ARGC — guards `$2` before it is read. Under `set -u` a
# value-taking flag in final position would otherwise abort with the shell's own
# "parameter not set" instead of a message naming the flag.
_need_val() {
    if [ "$2" -lt 2 ]; then
        die "$1 requires a value"
    fi
}

parse_args() {
    DRY_RUN=0
    ASSUME_YES=0
    # Privilege is granted by name, never by --yes. See netra_ask.
    GRANT_SYS_ADMIN=0
    GRANT_PID_HOST=0
    # Not privilege, but the same shape: a prompt that defaults n, so --yes
    # alone declines it and an operator who means it says so by name.
    GRANT_UNSUPPORTED_OS=0
    FORCE=0
    START=0
    TOKEN=""
    TOKEN_FILE=""
    HUB_URL=""
    # REF defaults to the installer's own version tag, and REF_EXPLICIT records
    # whether the operator chose it. Without that flag, "not given" and "given
    # as exactly the default" are indistinguishable after parsing, and the
    # runtime latest-release lookup would either never run or override an
    # explicit --ref. Never master, in either branch.
    REF="v$NETRA_INSTALLER_VERSION"
    REF_EXPLICIT=0
    TEMPLATE_DIR=""
    OUTPUT_DIR="./netra-agent"
    PRIMARY_SENSOR=""
    INCLUDE_NETWORK_FS=0

    while [ "$#" -gt 0 ]; do
        case "$1" in
        -y | --yes) ASSUME_YES=1 ;;
        --sys-admin) GRANT_SYS_ADMIN=1 ;;
        --pid-host) GRANT_PID_HOST=1 ;;
        --unsupported-os) GRANT_UNSUPPORTED_OS=1 ;;
        --dry-run) DRY_RUN=1 ;;
        --force) FORCE=1 ;;
        --start) START=1 ;;
        --include-network-fs) INCLUDE_NETWORK_FS=1 ;;
        --token)
            _need_val "$1" "$#"
            TOKEN="$2"
            shift
            ;;
        --token-file)
            _need_val "$1" "$#"
            TOKEN_FILE="$2"
            shift
            ;;
        --hub-url)
            _need_val "$1" "$#"
            HUB_URL="$2"
            shift
            ;;
        --ref)
            _need_val "$1" "$#"
            REF="$2"
            REF_EXPLICIT=1
            shift
            ;;
        --template-dir)
            _need_val "$1" "$#"
            TEMPLATE_DIR="$2"
            shift
            ;;
        --output-dir)
            _need_val "$1" "$#"
            OUTPUT_DIR="$2"
            shift
            ;;
        --primary-sensor)
            _need_val "$1" "$#"
            PRIMARY_SENSOR="$2"
            shift
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            die "unknown flag: $1 (try --help)"
            ;;
        esac
        shift
    done

    if [ -n "$TOKEN" ] && [ -n "$TOKEN_FILE" ]; then
        die "--token and --token-file are mutually exclusive"
    fi
}

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

# _p PATH — prefix a PROBE path with NETRA_INSTALL_ROOT. Never call this on a
# path that ends up in a template; see the header.
_p() {
    printf '%s%s' "$NETRA_INSTALL_ROOT" "$1"
}

init_paths() {
    # $P_TTY is a probe path but is deliberately NOT prefixed: it is a device,
    # not part of the fixture tree. Tests point NETRA_TTY at an unopenable path
    # because there is no portable way to drop a controlling terminal (no setsid
    # on macOS, and redirecting stdin does not touch /dev/tty).
    P_TTY="${NETRA_TTY:-/dev/tty}"

    P_OSRELEASE="${NETRA_OSRELEASE_PATH:-$(_p /etc/os-release)}"
    P_MACHINEID="${NETRA_MACHINEID_PATH:-$(_p /etc/machine-id)}"
    P_CGROUP="${NETRA_CGROUP_ROOT:-$(_p /sys/fs/cgroup)}"
    P_MOUNTINFO="${NETRA_MOUNTINFO_PATH:-$(_p /proc/1/mountinfo)}"

    P_SYSFS="${NETRA_SYSFS_ROOT:-$(_p /sys)}"
    # /sys/block, NOT /sys/class/block. The two trees hold different things:
    # /sys/class/block is flat and lists every partition alongside its parent,
    # while /sys/block lists ONLY whole devices and keeps partitions as
    # subdirectories of the disk they belong to. SMART is asked about whole
    # devices (/dev/sda, never /dev/sda1), so /sys/block is the tree that
    # answers the question being asked, and no partition filter is needed.
    P_SYSBLOCK="$P_SYSFS/block"
    P_SYSNVME="$P_SYSFS/class/nvme"
    P_HWMON="$P_SYSFS/class/hwmon"

    P_DEV="${NETRA_DEV_ROOT:-$(_p /dev)}"
    P_DPKG="${NETRA_DPKG_PATH:-$(_p /var/lib/dpkg/status)}"
    P_APK="${NETRA_APK_PATH:-$(_p /lib/apk/db/installed)}"
    P_DBUS="${NETRA_DBUS_PATH:-$(_p /run/dbus/system_bus_socket)}"
    P_DOCKERSOCK="${NETRA_DOCKERSOCK_PATH:-$(_p /var/run/docker.sock)}"
}

# debug_paths — dump every resolved probe path. `NETRA_DEBUG_PATHS=1` makes the
# prefixing rule inspectable from the outside; phase B's filesystem, SMART and
# sensor detection add their probes here too.
debug_paths() {
    printf 'install_root|%s\n' "$NETRA_INSTALL_ROOT"
    printf 'tty|%s\n' "$P_TTY"
    printf 'osrelease|%s\n' "$P_OSRELEASE"
    printf 'machineid|%s\n' "$P_MACHINEID"
    printf 'cgroup|%s\n' "$P_CGROUP"
    printf 'mountinfo|%s\n' "$P_MOUNTINFO"
    printf 'sysfs|%s\n' "$P_SYSFS"
    printf 'sysblock|%s\n' "$P_SYSBLOCK"
    printf 'sysnvme|%s\n' "$P_SYSNVME"
    printf 'hwmon|%s\n' "$P_HWMON"
    printf 'dev|%s\n' "$P_DEV"
    printf 'dpkg|%s\n' "$P_DPKG"
    printf 'apk|%s\n' "$P_APK"
    printf 'dbus|%s\n' "$P_DBUS"
    printf 'dockersock|%s\n' "$P_DOCKERSOCK"
}

# ---------------------------------------------------------------------------
# OS identification
# ---------------------------------------------------------------------------

# read_os_release — prints `id|version_id|id_like|pretty_name`.
#
# Parsed with awk and NEVER sourced. /etc/os-release is shell-syntax by
# convention, but it is untrusted input that this installer exists to validate;
# `.` on it would execute whatever a compromised or merely creative file
# contains, as root, before any check has run.
read_os_release() {
    if [ ! -f "$P_OSRELEASE" ]; then
        die "$P_OSRELEASE not found: this does not look like a Linux host netra supports"
    fi
    awk '
        function unquote(s) {
            sub(/\r$/, "", s)
            sub(/^["'\'']/, "", s)
            sub(/["'\'']$/, "", s)
            # The caller splits this record on "|", so a literal pipe in a value
            # would shift every following field.
            gsub(/\|/, " ", s)
            return s
        }
        /^[A-Z_]+=/ {
            eq = index($0, "=")
            key = substr($0, 1, eq - 1)
            val = unquote(substr($0, eq + 1))
            if (key == "ID") id = val
            else if (key == "VERSION_ID") ver = val
            else if (key == "ID_LIKE") like = val
            else if (key == "PRETTY_NAME") pretty = val
        }
        END { printf "%s|%s|%s|%s\n", id, ver, like, pretty }
    ' "$P_OSRELEASE"
}

# version_ge VERSION MIN — true when VERSION is at or above MIN.
#
# No float math: "3.18" is not a number and `[ 3.10 -ge 3.9 ]` is not a thing in
# POSIX sh. Sorting both values numerically field by field and checking which
# one comes first is exact for dotted versions. LC_ALL=C is set per-command so
# the decimal separator cannot vary, without perturbing anything else.
version_ge() {
    _vge_ver="$1"
    _vge_min="$2"
    if [ -z "$_vge_ver" ]; then
        # A missing VERSION_ID fails closed: unknown version, not "new enough".
        return 1
    fi
    _vge_first=$(printf '%s\n%s\n' "$_vge_min" "$_vge_ver" |
        LC_ALL=C sort -t. -k1,1n -k2,2n | head -n 1)
    [ "$_vge_first" = "$_vge_min" ]
}

# check_os_supported ID VERSION_ID ID_LIKE — prints one of:
#
#   supported  netra runs fully here
#   nopkg      the agent runs, but the package collector is unsupported (rpm)
#   unknown    unrecognised distro, or below the version floor
#
# The floors are the releases where cgroup v2 became the default; the real gate
# is cgroup v2 itself, probed directly by check_cgroup_v2. `unknown` is a warn
# plus a confirmation prompt, not an automatic refusal (spec §12a) — which is
# also why a below-floor Debian 10 lands here rather than in a fourth outcome.
check_os_supported() {
    _cos_id=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
    _cos_ver="$2"
    _cos_like=$(printf '%s' "$3" | tr '[:upper:]' '[:lower:]')

    case "$_cos_id" in
    debian)
        _cos_min=11
        _cos_class=supported
        ;;
    ubuntu)
        _cos_min=22.04
        _cos_class=supported
        ;;
    alpine)
        _cos_min=3.18
        _cos_class=supported
        ;;
    rhel | centos | fedora | rocky | almalinux)
        _cos_min=9
        _cos_class=nopkg
        ;;
    *)
        # Derivatives (Mint, Raspbian, AlmaLinux rebuilds) declare their base in
        # ID_LIKE. Fall back to it rather than refusing a Debian in a hat.
        case "$_cos_like" in
        *debian* | *ubuntu*)
            _cos_min=11
            _cos_class=supported
            ;;
        *rhel* | *fedora* | *centos*)
            _cos_min=9
            _cos_class=nopkg
            ;;
        *)
            printf 'unknown\n'
            return 0
            ;;
        esac
        ;;
    esac

    if version_ge "$_cos_ver" "$_cos_min"; then
        printf '%s\n' "$_cos_class"
    else
        printf 'unknown\n'
    fi
}

# ---------------------------------------------------------------------------
# Preflight checks
# ---------------------------------------------------------------------------

# check_cgroup_v2 — hard fail on anything that is not unified cgroup v2.
#
# TWO SEPARATE CHECKS, deliberately not one condition:
#
#   1. no cgroup.controllers            => pure v1
#   2. cgroup.controllers AND unified/  => HYBRID
#
# Hybrid is the subtle one. systemd's hybrid mode mounts cgroup2 at
# /sys/fs/cgroup/unified for its own bookkeeping while the actual controllers
# (cpu, memory, io) stay on v1 — so cgroup.controllers exists, the naive check
# passes, and the container collector then reads nothing at all. Collapsing
# these into a single `||` chain makes the hybrid case indistinguishable from a
# healthy one.
check_cgroup_v2() {
    if [ ! -f "$P_CGROUP/cgroup.controllers" ]; then
        die "cgroup v2 is required and this host is on cgroup v1" \
            "($P_CGROUP/cgroup.controllers does not exist)." \
            "Boot with systemd.unified_cgroup_hierarchy=1 on the kernel command line and try again."
    fi
    if [ -d "$P_CGROUP/unified" ]; then
        die "cgroup v2 is required and this host is in hybrid mode" \
            "($P_CGROUP/unified exists, so cgroup2 is mounted for systemd while the" \
            "controllers stay on v1 and the container collector would read nothing)." \
            "Boot with systemd.unified_cgroup_hierarchy=1 on the kernel command line and try again."
    fi
    info "  cgroup:          v2 (unified) at $P_CGROUP"
}

# check_machine_id — present AND non-empty.
#
# Empty is the uninitialised-golden-image case: the file exists because the
# image was built with it truncated, and every clone would otherwise report the
# same (empty) identity. The agent's host fingerprint depends on it.
check_machine_id() {
    if [ ! -f "$P_MACHINEID" ]; then
        die "$P_MACHINEID does not exist; the agent's host identity depends on it." \
            "Run systemd-machine-id-setup (or dbus-uuidgen > /etc/machine-id) and try again."
    fi
    if [ ! -s "$P_MACHINEID" ]; then
        die "$P_MACHINEID is empty — this host was cloned from a golden image whose" \
            "machine-id was truncated and never regenerated." \
            "Run systemd-machine-id-setup (or dbus-uuidgen > /etc/machine-id) and try again."
    fi
    info "  machine-id:      present ($P_MACHINEID)"
}

# check_docker — prints `compose_v2` or `compose_v1`, or dies.
#
# `command -v docker` rather than a path probe: which docker is on PATH is the
# thing that matters, and it is also what the tests shim.
check_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        die "docker is not on PATH. The netra agent is Docker-only (spec §13);" \
            "install Docker Engine and try again."
    fi
    if ! docker info >/dev/null 2>&1; then
        die "the Docker daemon is not reachable (docker info failed)." \
            "Start it, or re-run as a user in the docker group."
    fi
    if docker compose version >/dev/null 2>&1; then
        printf 'compose_v2\n'
        return 0
    fi
    if command -v docker-compose >/dev/null 2>&1 && docker-compose version >/dev/null 2>&1; then
        printf 'compose_v1\n'
        return 0
    fi
    die "neither 'docker compose' nor 'docker-compose' works." \
        "Install the Compose plugin (docker-compose-plugin) and try again."
}

# detect_pkgmgr ID ID_LIKE — prints dpkg, apk, rpm or none.
#
# File existence FIRST: what is actually on disk beats what os-release claims,
# and a container-derived or heavily customised host lies about the latter
# routinely. ID/ID_LIKE are consulted only to recognise an rpm host, where there
# is no file to probe that the installer can do anything with anyway — the rpm
# store needs librpm and the collector is unsupported there regardless.
detect_pkgmgr() {
    if [ -f "$P_DPKG" ]; then
        printf 'dpkg\n'
        return 0
    fi
    if [ -f "$P_APK" ]; then
        printf 'apk\n'
        return 0
    fi
    _dpm_id=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
    _dpm_like=$(printf '%s' "$2" | tr '[:upper:]' '[:lower:]')
    case "$_dpm_id $_dpm_like" in
    *rhel* | *fedora* | *centos* | *rocky* | *almalinux* | *suse*)
        printf 'rpm\n'
        ;;
    *)
        printf 'none\n'
        ;;
    esac
}

# ---------------------------------------------------------------------------
# Filesystems (§6.4)
# ---------------------------------------------------------------------------

# mountinfo_rows — one `maj:min|root|mountpoint|fstype|source` row per mount.
#
# THE OPTIONAL-FIELDS SECTION IS VARIABLE LENGTH. /proc/*/mountinfo is
#
#   36 35 98:0 /mnt1 /mnt2 rw,noatime [optional fields...] - fstype source opts
#
# and the optional fields (`shared:1`, `master:2`, `propagate_from:`,
# `unbindable`) are terminated by a literal `-`. There can be zero of them, which
# is why fixed field indexing looks correct on a laptop and reads the fstype out
# of the middle of the optional fields on any host with shared subtrees — i.e.
# on every systemd host with a propagated mount. Scan for the `-`.
#
# The octal escapes the kernel writes are \040 \011 \012 \134. `printf '%b'` is
# the wrong tool: it understands the \0ddd form, not the bare \ddd form used
# here, so \134 would survive literally. They are unescaped in awk with explicit
# gsubs, and BACKSLASH RUNS LAST — unescaping it first would turn the text
# `\134040` into `\040` and then into a space, inventing a character the kernel
# never wrote.
#
# A mount point containing a decoded newline is dropped here rather than
# emitted: this format is line-oriented and such a path cannot be represented in
# it. It would be rejected downstream anyway, along with `"` and `$`, by the
# rule that a real path can only be rejected, never sanitised.
mountinfo_rows() {
    [ -f "$P_MOUNTINFO" ] || return 0
    awk '
        BEGIN { BS = sprintf("%c", 92) }
        # \134 is NOT done with gsub. A gsub replacement string is itself
        # escape-processed, and awk implementations disagree about what a
        # backslash in it means: the same "\\\\" yields one backslash under gawk
        # and two under the one-true-awk that macOS ships. index()/substr() do
        # no escape processing at all, so this is the only portable spelling.
        function unbs(s,   out, i) {
            out = ""
            while ((i = index(s, "\\134")) > 0) {
                out = out substr(s, 1, i - 1) BS
                s = substr(s, i + 4)
            }
            return out s
        }
        function unesc(s) {
            gsub(/\\040/, " ", s)
            gsub(/\\011/, "\t", s)
            gsub(/\\012/, "\n", s)
            return unbs(s)
        }
        {
            sep = 0
            for (i = 7; i <= NF; i++) {
                if ($i == "-") { sep = i; break }
            }
            if (sep == 0 || sep + 2 > NF) next
            root = unesc($4)
            mp   = unesc($5)
            src  = unesc($(sep + 2))
            if (root ~ /\n/ || mp ~ /\n/ || src ~ /\n/) next
            printf "%s|%s|%s|%s|%s\n", $3, root, mp, $(sep + 1), src
        }
    ' "$P_MOUNTINFO"
}

# filter_mounts — reads mountinfo_rows on stdin and emits, in mount-table order:
#
#   ok|maj:min|mountpoint|fstype
#   skip|mountpoint|kind|reason
#
# Every rejection is recorded with a reason so the plan can say WHY something is
# not being measured. Silence there is how an operator ends up believing a
# filesystem is monitored when it is not.
#
# ST_DEV DEDUPLICATION WITHOUT stat(1): mountinfo field 3 IS `major:minor`, so
# the dedup key is a string already in the table — no stat(1), and therefore no
# GNU-vs-BSD `--format` problem. Note that btrfs subvolumes and ZFS datasets
# each get their own ANONYMOUS major:minor, so they legitimately count as
# separate filesystems here even though they share a pool; the plan says so.
filter_mounts() {
    awk -v include_net="${INCLUDE_NETWORK_FS:-0}" '
        BEGIN { FS = "|" }
        # index() on a padded list, never a dynamic regex: an fstype is
        # attacker-adjacent text and `f` in a regex would let a `.` match.
        function ispseudo(f) {
            if (f ~ /^fuse\./) return 1
            return index(" proc sysfs devtmpfs devpts tmpfs ramfs cgroup cgroup2 \
securityfs pstore bpf debugfs tracefs configfs fusectl mqueue hugetlbfs autofs \
binfmt_misc efivarfs nsfs selinuxfs rpc_pipefs squashfs overlay ", " " f " ") > 0
        }
        function isnet(f) {
            return index(" nfs nfs4 cifs smb3 fuse.sshfs ", " " f " ") > 0
        }
        function isexcluded(p) {
            if (p == "/") return 0
            if (p ~ /^\/var\/lib\/docker(\/|$)/) return 1
            if (p ~ /^\/var\/lib\/containers(\/|$)/) return 1
            if (p ~ /^\/snap(\/|$)/) return 1
            if (p ~ /^\/proc(\/|$)/) return 1
            if (p ~ /^\/sys(\/|$)/) return 1
            if (p ~ /^\/dev(\/|$)/) return 1
            if (p ~ /^\/run(\/|$)/) return 1
            return 0
        }
        function skip(p, kind, why) { printf "skip|%s|%s|%s\n", p, kind, why }
        { n++; mm[n] = $1; rt[n] = $2; mp[n] = $3; ft[n] = $4; last[$3] = n }
        END {
            for (i = 1; i <= n; i++) {
                f = ft[i]
                p = mp[i]
                # Network first, then pseudo: fuse.sshfs is both, and it is the
                # network rule that should describe it (and that
                # --include-network-fs should be able to override).
                if (isnet(f)) {
                    if (!include_net) {
                        skip(p, "network", "network filesystem (" f "): skipped by default because \
a dead server makes statfs block, which the agent'"'"'s scrape budget cannot absorb. \
--include-network-fs opts in.")
                        continue
                    }
                } else if (ispseudo(f)) {
                    skip(p, "pseudo", "pseudo filesystem (" f "): nothing to measure")
                    continue
                }
                if (rt[i] != "/") {
                    skip(p, "bind", "bind mount of the subtree " rt[i] ", not a whole filesystem")
                    continue
                }
                if (isexcluded(p)) {
                    skip(p, "path", "container or runtime path, not host storage")
                    continue
                }
                # Keep the LAST mount on a mountpoint: it is the visible one,
                # and the earlier mount is hidden underneath it.
                if (last[p] != i) {
                    skip(p, "shadowed", "shadowed by a later mount on the same mountpoint")
                    continue
                }
                # Keep the FIRST of a maj:min, and name both.
                if (mm[i] in seen) {
                    skip(p, "duplicate", "same filesystem (" mm[i] ") as " seen[mm[i]] \
", which is already monitored; measuring both would double-report one filesystem")
                    continue
                }
                seen[mm[i]] = p
                printf "ok|%s|%s|%s\n", mm[i], p, f
            }
        }
    '
}

# fs_label MOUNTPOINT [MAJ:MIN] — the /netra/fs/<label> segment for a mount.
#
# A WHITELIST, not an escaping scheme. The label ends up in a compose target
# path and in the hub's dimension tables; anything outside [a-z0-9_-] is
# replaced rather than quoted, because there is no context here where a quoted
# oddity is better than a plain name.
fs_label() {
    _fsl_mp="$1"
    _fsl_majmin="${2:-}"
    if [ "$_fsl_mp" = "/" ]; then
        printf 'root\n'
        return 0
    fi
    _fsl_out=$(printf '%s' "${_fsl_mp##*/}" |
        tr '[:upper:]' '[:lower:]' |
        tr -c 'a-z0-9_-' '-' |
        tr -s '-' |
        sed 's/^-*//; s/-*$//')
    if [ -z "$_fsl_out" ]; then
        # Nothing whitelistable survived (a mount point named "..." or in a
        # non-Latin script). maj:min is always present and always unique.
        _fsl_out="fs$(printf '%s' "$_fsl_majmin" | tr ':' '-')"
    fi
    printf '%s\n' "$_fsl_out"
}

# _fs_note KIND MOUNTPOINT REASON — record a rejection.
#
# Only the interesting kinds reach warn(). A busy Docker host has hundreds of
# overlay and tmpfs mounts, and repeating every one of them in the finish report
# would bury the two lines the operator actually needs to read.
_fs_note() {
    FS_SKIPS="${FS_SKIPS:+$FS_SKIPS
}$1|$2|$3"
    case "$1" in
    network | duplicate | unwritable | unsupported)
        warn "filesystem $2 skipped: $3"
        ;;
    esac
}

# detect_filesystems — fills FS_MOUNTS (`maj:min|mountpoint|label`) and FS_SKIPS.
#
# Creates nothing. Marker directories are a mutation and belong to the consent
# phase; this runs under --dry-run too.
detect_filesystems() {
    step "Filesystems"
    FS_MOUNTS=""
    FS_SKIPS=""
    _df_labels=""

    if [ ! -f "$P_MOUNTINFO" ]; then
        warn "$P_MOUNTINFO is not readable, so no filesystem can be measured." \
            "Disk usage and inode metrics will be missing."
        info "  filesystems:     none (no mount table)"
        return 0
    fi

    _df_rows=$(mountinfo_rows | filter_mounts)
    while IFS='|' read -r _df_kind _df_f2 _df_f3 _df_f4; do
        [ -n "$_df_kind" ] || continue
        if [ "$_df_kind" = skip ]; then
            _fs_note "$_df_f3" "$_df_f2" "$_df_f4"
            continue
        fi
        _df_mm="$_df_f2"
        _df_mp="$_df_f3"

        # A real path can only be rejected, never sanitised: these characters
        # have no representation that survives both YAML quoting and the shell.
        #
        # The backslash pattern is built from its character code rather than
        # written as a literal `*'\'*`. Spelled literally it is ambiguous enough
        # that a linter flags it and a reader has to stop and work out whether it
        # means one backslash or an escaped quote; `%c` of 92 cannot mean
        # anything else. Same trick as the mountinfo parser's BS.
        _df_bs=$(awk 'BEGIN { printf "%c", 92 }')
        case "$_df_mp" in
        *'"'* | *'$'* | *"$_df_bs"*)
            _fs_note unsupported "$_df_mp" \
                "unsupported mount point (contains a quote, dollar sign or backslash), skipping"
            continue
            ;;
        esac

        # PROBE side: writability is tested against the (optionally prefixed)
        # path. access(W_OK) rather than a mkdir/rmdir probe, because a mkdir
        # here would be a mutation outside netra_exec and --dry-run must not
        # perform it. W_OK also respects MNT_READONLY, so a read-only mount is
        # caught as well as a permissions problem.
        _df_probe=$(_p "$_df_mp")
        if [ ! -d "$_df_probe" ] || [ ! -w "$_df_probe" ]; then
            _fs_note unwritable "$_df_mp" \
                "not writable by this user, so the .netra marker directory cannot be created. \
The installer never invokes sudo; create it yourself and re-run."
            continue
        fi

        _df_base=$(fs_label "$_df_mp" "$_df_mm")
        _df_lab="$_df_base"
        _df_n=1
        while :; do
            case " $_df_labels " in
            *" $_df_lab "*) ;;
            *) break ;;
            esac
            _df_n=$((_df_n + 1))
            _df_lab="$_df_base-$_df_n"
        done
        _df_labels="$_df_labels $_df_lab"

        FS_MOUNTS="${FS_MOUNTS:+$FS_MOUNTS
}$_df_mm|$_df_mp|$_df_lab"
    done <<EOF
$_df_rows
EOF

    if [ -z "$FS_MOUNTS" ]; then
        # Legitimate, not an error (§6.4): a container-only host has nothing to
        # measure and still gets a working agent.
        info "  filesystems:     none accepted (the agent will log a startup warning; this is fine)"
    else
        _df_count=$(printf '%s\n' "$FS_MOUNTS" | grep -c .)
        info "  filesystems:     $_df_count accepted"
        while IFS='|' read -r _df_mm _df_mp _df_lab; do
            [ -n "$_df_mm" ] || continue
            info "                   $_df_mp -> /netra/fs/$_df_lab ($_df_mm)"
        done <<EOF
$FS_MOUNTS
EOF
        # btrfs subvolumes and ZFS datasets each carry a distinct ANONYMOUS
        # major:minor, so they are NOT deduplicated against each other and each
        # one is measured separately. That is correct — they report different
        # usage against a shared pool — but it surprises people, so it is said
        # out loud here rather than discovered from a graph.
        info "                   (btrfs subvolumes and ZFS datasets each carry their own"
        info "                    anonymous maj:min, so they are counted separately)"
    fi

    check_selinux
}

# check_selinux — bind mounts on an enforcing host need a relabel suffix.
check_selinux() {
    _sel="$P_SYSFS/fs/selinux/enforce"
    [ -f "$_sel" ] || return 0
    if [ "$(cat "$_sel" 2>/dev/null || printf '0')" = 1 ]; then
        warn "SELinux is enforcing. Bind mounts from a container may be denied unless the" \
            "source is relabelled; add ':z' (shared) or ':Z' (private) to the mounts in the" \
            "rendered compose.yaml, or run 'chcon -Rt container_file_t' on the marker dirs."
    fi
}

# ---------------------------------------------------------------------------
# SMART (§6.3)
# ---------------------------------------------------------------------------

# block_devices — whole physical devices.
#
# $P_SYSBLOCK is /sys/block, where the entries are ALREADY only whole devices:
# a partition is a subdirectory of its parent (/sys/block/sda/sda1), not a
# top-level entry. Filtering partitions out is therefore not this function's
# job. What it does filter is the virtual and removable-empty cases:
#
#   loop* ram* zram* dm-* md* sr* fd* nbd*  — not physical drives
#   no `device` link                        — virtual, nothing to ask SMART
#   size == 0                               — an empty card reader slot
block_devices() {
    [ -d "$P_SYSBLOCK" ] || return 0
    for _bd_p in "$P_SYSBLOCK"/*; do
        [ -e "$_bd_p" ] || continue
        _bd_n=${_bd_p##*/}
        case "$_bd_n" in
        loop* | ram* | zram* | dm-* | md* | sr* | fd* | nbd*) continue ;;
        esac
        [ -e "$_bd_p/device" ] || continue
        _bd_size=$(cat "$_bd_p/size" 2>/dev/null || printf '0')
        [ "$_bd_size" != 0 ] || continue
        printf '%s\n' "$_bd_n"
    done
}

# device_transport NAME — sata, nvme, usb or unknown.
#
# `cd … && pwd -P`, never `readlink -f` (not POSIX, and absent on some minimal
# userlands). The resolved path is the sysfs devices-tree path, whose components
# name the transport: .../ata1/host0/... or .../usb1/1-1/... or .../nvme/nvme0/.
#
# CRITICAL: strip NETRA_INSTALL_ROOT from the resolved path BEFORE matching.
# Without the strip, a fixture root that happens to live under a directory
# containing `usb` or `ata` — and mktemp paths do contain surprising things —
# classifies every device on the host by the name of a temporary directory, and
# a test suite built on it encodes the bug as expected behaviour. run.sh hands
# out a `pwd -P` canonicalised $TMP precisely so this strip is a plain prefix
# match; 010 asserts that it stayed that way.
device_transport() {
    _dt_dir="$P_SYSBLOCK/$1"
    if [ ! -e "$_dt_dir" ]; then
        printf 'unknown\n'
        return 0
    fi
    if ! _dt_real=$(cd "$_dt_dir" && pwd -P); then
        printf 'unknown\n'
        return 0
    fi
    if [ -n "$NETRA_INSTALL_ROOT" ]; then
        _dt_real=${_dt_real#"$NETRA_INSTALL_ROOT"}
    fi
    case "$_dt_real" in
    */nvme*) printf 'nvme\n' ;;
    */usb[0-9]* | */usb/*) printf 'usb\n' ;;
    */ata[0-9]*) printf 'sata\n' ;;
    *) printf 'unknown\n' ;;
    esac
}

# nvme_controllers — /dev/nvme0-style CONTROLLER names, never namespaces.
#
# /sys/block/nvme0n1 is a NAMESPACE. smartctl is given the controller, and a
# drive with two namespaces must still produce ONE devices: entry — listing
# nvme0n1 and nvme0n2 would ask the same controller twice and, worse, name
# device nodes that smartctl cannot drive.
#
# /sys/class/nvme is the direct answer where it exists; the namespace-name
# fallback is for kernels old enough to predate it.
nvme_controllers() {
    _nc_found=0
    if [ -d "$P_SYSNVME" ]; then
        for _nc_p in "$P_SYSNVME"/nvme[0-9]*; do
            [ -e "$_nc_p" ] || continue
            _nc_found=1
            printf '%s\n' "${_nc_p##*/}"
        done
    fi
    [ "$_nc_found" = 0 ] || return 0

    _nc_seen=""
    for _nc_p in "$P_SYSBLOCK"/nvme*n*; do
        [ -e "$_nc_p" ] || continue
        _nc_ctrl=$(printf '%s' "${_nc_p##*/}" | sed 's/n[0-9]*$//')
        [ -n "$_nc_ctrl" ] || continue
        case " $_nc_seen " in
        *" $_nc_ctrl "*) continue ;;
        esac
        _nc_seen="$_nc_seen $_nc_ctrl"
        printf '%s\n' "$_nc_ctrl"
    done
}

# _smart_dev NAME — append /dev/NAME to SMART_DEVICES if the node exists.
#
# PROBE vs EMIT in two adjacent lines: existence is checked against $P_DEV/NAME
# (prefixed under test), and the string that reaches compose.yaml is the
# UNPREFIXED /dev/NAME. A prefixed device path in a devices: list names a node
# that does not exist on the host, and Docker refuses to start the container.
_smart_dev() {
    if [ ! -e "$P_DEV/$1" ]; then
        warn "SMART: $P_DEV/$1 does not exist, so /dev/$1 is left out of devices:" \
            "(a devices: entry for a missing node prevents the container from starting)."
        return 0
    fi
    SMART_DEVICES="${SMART_DEVICES:+$SMART_DEVICES
}/dev/$1"
}

# plan_smart — fills SMART_DEVICES, CAP_RAWIO and CAP_SYS_ADMIN.
#
# Candidates are collected BEFORE any prompt so that a declined capability also
# removes the devices: entries it would have driven. A devices: list that the
# container has no capability to use is noise at best.
plan_smart() {
    step "SMART"
    SMART_DEVICES=""
    CAP_RAWIO=0
    CAP_SYS_ADMIN=0
    _ps_ata=""
    _ps_nvme=""

    for _ps_d in $(block_devices); do
        _ps_t=$(device_transport "$_ps_d")
        case "$_ps_t" in
        usb)
            warn "SMART: $_ps_d is USB-attached and is left out. Driving a USB bridge with" \
                "'-d sat' is unreliable and can hang the enclosure, which would stall the" \
                "whole scrape."
            ;;
        nvme) ;;
        *)
            _ps_ata="${_ps_ata:+$_ps_ata }$_ps_d"
            ;;
        esac
    done

    for _ps_c in $(nvme_controllers); do
        _ps_nvme="${_ps_nvme:+$_ps_nvme }$_ps_c"
    done

    if [ -n "$_ps_ata" ]; then
        info "  SATA/SAS:        $_ps_ata"
        # `if netra_ask` — see the header.
        if netra_ask "Grant SYS_RAWIO so smartctl can read SATA/SAS drives (benign)?" y; then
            CAP_RAWIO=1
            for _ps_d in $_ps_ata; do _smart_dev "$_ps_d"; done
        else
            warn "SYS_RAWIO declined: no SMART data for SATA/SAS drives, and no drive" \
                "temperatures for them either. The agent reports SMART as unavailable."
        fi
    fi

    if [ -n "$_ps_nvme" ]; then
        info "  NVMe:            $_ps_nvme (controllers, not namespaces)"
        # --sys-admin takes the grant WITHOUT asking, and runs exactly the body
        # the yes-branch runs — the flag is a pure grant, not a second code
        # path that could drift from the prompted one.
        _ps_grant=0
        if [ "${GRANT_SYS_ADMIN:-0}" = 1 ]; then
            info "  SYS_ADMIN:       granted by --sys-admin (not prompted)"
            _ps_grant=1
        elif netra_ask "Grant SYS_ADMIN for NVMe SMART health and wear? It is root-adjacent, and
  declining costs ONLY health/wear — NVMe temperature comes from hwmon and keeps
  working without it." n --sys-admin; then
            _ps_grant=1
        fi
        if [ "$_ps_grant" = 1 ]; then
            CAP_SYS_ADMIN=1
            for _ps_c in $_ps_nvme; do _smart_dev "$_ps_c"; done
        else
            warn "SYS_ADMIN declined: no NVMe SMART health status or wear indicators." \
                "NVMe temperature still works — it comes from hwmon, not smartctl." \
                "Re-run with --sys-admin to grant it without prompting."
        fi
    elif [ "${GRANT_SYS_ADMIN:-0}" = 1 ]; then
        # An unhonoured request, not a neutral state: the operator asked for a
        # capability they did not get, so it belongs in the finish report.
        # Granting it anyway would expand privilege for no collected metric.
        warn "--sys-admin was given but no NVMe device was found: SYS_ADMIN is NOT granted." \
            "Nothing on this host would use it."
    fi

    if [ -z "$SMART_DEVICES" ]; then
        info "  smart:           no devices (nothing to collect)"
    fi
}

# ---------------------------------------------------------------------------
# Sensors
# ---------------------------------------------------------------------------

# hwmon_chips — one `hwmonN|chipname|label1,label2,…` row per chip.
hwmon_chips() {
    [ -d "$P_HWMON" ] || return 0
    for _hc_d in "$P_HWMON"/hwmon*; do
        [ -d "$_hc_d" ] || continue
        _hc_name=$(cat "$_hc_d/name" 2>/dev/null || printf '')
        if [ -z "$_hc_name" ]; then
            # Older drivers expose the name one level down, on the device.
            _hc_name=$(cat "$_hc_d/device/name" 2>/dev/null || printf '')
        fi
        [ -n "$_hc_name" ] || _hc_name=unknown

        _hc_labels=""
        for _hc_f in "$_hc_d"/temp*_label; do
            [ -f "$_hc_f" ] || continue
            _hc_v=$(cat "$_hc_f" 2>/dev/null || printf '')
            [ -n "$_hc_v" ] || continue
            _hc_labels="${_hc_labels:+$_hc_labels,}$_hc_v"
        done
        if [ -z "$_hc_labels" ]; then
            # No _label files: the input basename is the only identity there is.
            for _hc_f in "$_hc_d"/temp*_input; do
                [ -e "$_hc_f" ] || continue
                _hc_labels="${_hc_labels:+$_hc_labels,}${_hc_f##*/}"
            done
        fi
        printf '%s|%s|%s\n' "${_hc_d##*/}" "$_hc_name" "$_hc_labels"
    done
}

# pick_primary_sensor ROWS — the chip name to treat as the host's headline
# temperature, or nothing.
#
# The preference list is walked IN ORDER and the first match wins. NEVER
# hottest-wins: the hottest chip on a NAS is a spinning disk, and a host whose
# headline temperature is a disk is a host whose CPU thermal problem is
# invisible. Matches §6.2.
pick_primary_sensor() {
    for _pp_pref in coretemp k10temp zenpower thermal_zone acpitz; do
        while IFS='|' read -r _pp_h _pp_n _pp_l; do
            [ -n "$_pp_h" ] || continue
            if [ "$_pp_n" = "$_pp_pref" ]; then
                printf '%s\n' "$_pp_n"
                return 0
            fi
        done <<EOF
$1
EOF
    done
    return 0
}

# detect_sensors — INFORMATIONAL, and deliberately so.
#
# The agent already auto-selects the primary sensor at runtime with the same
# preference order. Writing NETRA_PRIMARY_SENSOR here would freeze an
# install-time guess into .env, where it outlives the CPU swap or kernel upgrade
# that changed the chip — so it is written ONLY when --primary-sensor was passed
# explicitly, or when two equally-ranked known CPU chips exist and the operator
# resolves the tie.
detect_sensors() {
    step "Sensors"
    SENSOR_ROWS=""
    if [ ! -d "$P_HWMON" ]; then
        # A note, not an error: hwmon is absent inside some VMs and the agent
        # simply reports no temperatures.
        info "  sensors:         $P_HWMON does not exist (no temperatures on this host)"
        return 0
    fi

    SENSOR_ROWS=$(hwmon_chips)
    if [ -z "$SENSOR_ROWS" ]; then
        info "  sensors:         none detected"
        return 0
    fi

    while IFS='|' read -r _ds_h _ds_n _ds_l; do
        [ -n "$_ds_h" ] || continue
        info "                   $_ds_h $_ds_n [${_ds_l:-no labels}]"
    done <<EOF
$SENSOR_ROWS
EOF

    _ds_primary=$(pick_primary_sensor "$SENSOR_ROWS")
    if [ -n "$PRIMARY_SENSOR" ]; then
        info "  primary sensor:  $PRIMARY_SENSOR (--primary-sensor, written to .env)"
        return 0
    fi
    if [ -z "$_ds_primary" ]; then
        info "  primary sensor:  none of the known CPU chips; the agent will choose at runtime"
        return 0
    fi

    # Ambiguity is a tie among chips of the SAME top-ranked name — two coretemp
    # chips on a dual-socket box, say. Only then is there something an operator
    # can usefully decide.
    _ds_count=0
    while IFS='|' read -r _ds_h _ds_n _ds_l; do
        [ "$_ds_n" = "$_ds_primary" ] || continue
        _ds_count=$((_ds_count + 1))
    done <<EOF
$SENSOR_ROWS
EOF

    if [ "$_ds_count" -le 1 ]; then
        info "  primary sensor:  $_ds_primary (auto; left unset in .env so it can follow the hardware)"
        return 0
    fi

    info "  primary sensor:  $_ds_count '$_ds_primary' chips are equally ranked"
    while IFS='|' read -r _ds_h _ds_n _ds_l; do
        [ "$_ds_n" = "$_ds_primary" ] || continue
        # `if netra_ask` — see the header.
        if netra_ask "Pin $_ds_h ($_ds_n) as the primary sensor?" y; then
            PRIMARY_SENSOR="$_ds_n"
            break
        fi
    done <<EOF
$SENSOR_ROWS
EOF
    if [ -z "$PRIMARY_SENSOR" ]; then
        warn "primary sensor left unpinned with $_ds_count equally-ranked '$_ds_primary' chips;" \
            "the agent will pick one at runtime and it may change across reboots."
    fi
}

# ---------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------

# resolve_ref — pin $REF to the latest release tag when --ref was not given.
#
# NEVER master (§12a): a mid-refactor template must not be able to land on a
# production host. A failed lookup falls back to the installer's own version tag,
# which is still a tag.
resolve_ref() {
    [ "$REF_EXPLICIT" = 0 ] || return 0
    [ -z "$TEMPLATE_DIR" ] || return 0
    _rr_url="https://api.github.com/repos/trick77/netra/releases/latest"
    if ! _rr_body=$(curl -fsSL "$_rr_url" 2>/dev/null); then
        _rr_body=""
    fi
    _rr_tag=$(printf '%s' "$_rr_body" |
        sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
    if [ -n "$_rr_tag" ]; then
        REF="$_rr_tag"
    else
        # A warning, not a note: the templates are about to come from a
        # different tag than the operator would have got, which is exactly the
        # kind of silent version skew --ref exists to prevent.
        warn "could not resolve the latest release tag from $_rr_url, so templates will be" \
            "fetched from $REF instead. Pass --ref <tag> to choose explicitly."
    fi
}

# fetch_template NAME — copies or downloads a template into $SCRATCH_DIR and
# prints its path.
#
# This is the installer's most likely field failure: a corporate proxy, an
# air-gapped host, a tag that does not exist yet. The message therefore names
# the exact URL and both escape hatches, because for the person reading it the
# message IS the remedy.
fetch_template() {
    _ft_name="$1"
    _ft_dest="$SCRATCH_DIR/$_ft_name"

    if [ -n "$TEMPLATE_DIR" ]; then
        if [ ! -f "$TEMPLATE_DIR/$_ft_name" ]; then
            die "template '$_ft_name' not found in --template-dir $TEMPLATE_DIR"
        fi
        cp "$TEMPLATE_DIR/$_ft_name" "$_ft_dest" ||
            die "could not copy $TEMPLATE_DIR/$_ft_name to $_ft_dest"
        printf '%s\n' "$_ft_dest"
        return 0
    fi

    _ft_url="https://raw.githubusercontent.com/trick77/netra/$REF/deploy/agent/$_ft_name"
    if ! curl -fsSL "$_ft_url" >"$_ft_dest" 2>/dev/null; then
        die "could not download the template from $_ft_url." \
            "Pass --ref <tag> to pin a different release tag, or --template-dir <path> to use" \
            "local template files (no network at all). Neither option needs the hub."
    fi
    if [ ! -s "$_ft_dest" ]; then
        die "the template downloaded from $_ft_url was empty." \
            "Pass --ref <tag> to pin a different release tag, or --template-dir <path> to use" \
            "local template files (no network at all)."
    fi
    printf '%s\n' "$_ft_dest"
}

# render_template FILE — whole-marker-line substitution.
#
# Blocks are passed through ENVIRON, NEVER `awk -v`. `-v x="$block"` runs the
# value through awk's escape processing, so a mount point containing the literal
# text `\040` would silently turn into a space on its way into compose.yaml —
# the exact byte sequence this installer takes care to decode correctly one step
# earlier.
#
# An empty block deletes its marker line entirely. That is why `cap_add:` and
# `volumes:` are inside their blocks rather than in the template: an empty
# mapping key cannot be left dangling if the key never existed.
render_template() {
    awk '
        /^[[:space:]]*#__NETRA_[A-Z_]+__[[:space:]]*$/ {
            key = $0
            sub(/^[[:space:]]*#__NETRA_/, "", key)
            sub(/__[[:space:]]*$/, "", key)
            blk = ENVIRON["NETRA_BLK_" key]
            if (blk != "") printf "%s", blk
            next
        }
        { print }
    ' "$1"
}

# render_env FILE — inline `__NAME__` substitution.
#
# awk with an accumulator, NOT `sed s///`: a hub URL contains `/` and a token may
# contain `&`, either of which turns a sed expression into something else. The
# accumulator (rather than a gsub loop over the same line) also means a value
# that itself looks like a token cannot cause an infinite rescan.
render_env() {
    awk '
        {
            line = $0
            out = ""
            while (match(line, /__[A-Z][A-Z0-9_]*__/)) {
                key = substr(line, RSTART + 2, RLENGTH - 4)
                out = out substr(line, 1, RSTART - 1) ENVIRON["NETRA_VAL_" key]
                line = substr(line, RSTART + RLENGTH)
            }
            print out line
        }
    ' "$1"
}

# _env_value NAME VALUE — validate and export NETRA_VAL_<NAME>.
#
# Values are written UNQUOTED, because compose's env_file parser takes the rest
# of the line literally and quotes would become part of the value. A newline or
# carriage return therefore cannot be represented at all and is rejected rather
# than mangled.
_env_value() {
    # A newline cannot be tested with `case "$2" in *"$(printf '\n')"*`: command
    # substitution strips trailing newlines, so the pattern would collapse to
    # `**` and match every value. Count lines instead. A carriage return is not
    # stripped and can be matched directly.
    if [ "$(printf '%s' "$2" | wc -l | tr -d ' ')" != 0 ]; then
        die "the value for $1 contains a newline, which cannot be represented in an" \
            "env_file (the parser takes the rest of the line literally). Fix it and re-run."
    fi
    case "$2" in
    *"$(printf '\r')"*)
        die "the value for $1 contains a carriage return, which cannot be represented in" \
            "an env_file. Fix it and re-run."
        ;;
    esac
    eval "NETRA_VAL_$1=\$2"
    eval "export NETRA_VAL_$1"
}

# build_volume_block — the `volumes:` key AND its entries, or nothing at all.
#
# Long form (`type: bind` / `source:` / `target:` / `read_only: true`) rather
# than `src:dst:ro`: the short form is parsed by splitting on ':', so a mount
# point containing a colon breaks it outright. `source:` is quoted so a space
# survives.
#
# Every source here is an EMIT path — the real host path, never prefixed.
build_volume_block() {
    NETRA_BLK_VOLUMES=""
    _bv_body=""

    # SC2089/SC2090: the quotes in these blocks are DATA — they are YAML syntax
    # on their way into compose.yaml, not shell quoting — and the variable is
    # only ever exported for awk's ENVIRON, never expanded as a command.
    # shellcheck disable=SC2089
    _bv_add() {
        _bv_body="$_bv_body      - type: bind
        source: \"$1\"
        target: $2
        read_only: true
"
    }

    while IFS='|' read -r _bv_mm _bv_mp _bv_lab; do
        [ -n "$_bv_mm" ] || continue
        if [ "$_bv_mp" = "/" ]; then
            _bv_add "/.netra" "/netra/fs/$_bv_lab"
        else
            _bv_add "$_bv_mp/.netra" "/netra/fs/$_bv_lab"
        fi
    done <<EOF
${FS_MOUNTS:-}
EOF

    if [ "${DOCKERSOCK_ENABLED:-0}" = 1 ]; then
        _bv_add "/var/run/docker.sock" "/var/run/docker.sock"
    fi
    if [ "${MOUNTINFO_ENABLED:-0}" = 1 ]; then
        _bv_add "/proc/1/mountinfo" "/host/mountinfo"
    fi
    if [ "${DBUS_ENABLED:-0}" = 1 ]; then
        _bv_add "/run/dbus/system_bus_socket" "/run/dbus/system_bus_socket"
    fi
    if [ "${PKG_ENABLED:-0}" = 1 ] && [ -n "${PKG_MOUNT:-}" ]; then
        _bv_add "$PKG_MOUNT" "$PKG_MOUNT"
    fi

    if [ -n "$_bv_body" ]; then
        NETRA_BLK_VOLUMES="    volumes:
$_bv_body"
    fi
    # shellcheck disable=SC2090
    export NETRA_BLK_VOLUMES
}

build_device_block() {
    NETRA_BLK_DEVICES=""
    if [ -n "${SMART_DEVICES:-}" ]; then
        NETRA_BLK_DEVICES="    devices:
$(printf '%s\n' "$SMART_DEVICES" | sed 's/^/      - /')
"
    fi
    export NETRA_BLK_DEVICES
}

build_cap_block() {
    NETRA_BLK_CAP_ADD=""
    _bc_body=""
    # if/fi, not `[ … ] && …`: a false test as the last command of a function
    # makes the function return 1, which under `set -e` ends the script.
    if [ "${CAP_RAWIO:-0}" = 1 ]; then
        _bc_body="$_bc_body      - SYS_RAWIO
"
    fi
    if [ "${CAP_SYS_ADMIN:-0}" = 1 ]; then
        _bc_body="$_bc_body      - SYS_ADMIN
"
    fi
    if [ -n "$_bc_body" ]; then
        NETRA_BLK_CAP_ADD="    cap_add:
$_bc_body"
    fi
    export NETRA_BLK_CAP_ADD
}

build_pid_block() {
    NETRA_BLK_PID=""
    if [ "${PID_HOST:-0}" = 1 ]; then
        NETRA_BLK_PID="    pid: host
"
    fi
    export NETRA_BLK_PID
}

build_blocks() {
    build_volume_block
    build_device_block
    build_cap_block
    build_pid_block
}

# netra_write_compose / netra_write_env — the redirection lives in a function so
# the whole write can be routed through netra_exec, which cannot itself carry a
# `>`. Under --dry-run netra_exec prints the call and no file is touched.
netra_write_compose() {
    render_template "$1" >"$2"
}

netra_write_env() {
    render_env "$1" >"$2"
}

# ---------------------------------------------------------------------------
# Phases
# ---------------------------------------------------------------------------

preflight() {
    step "Preflight"

    _pf_line=$(read_os_release)
    IFS='|' read -r OS_ID OS_VER OS_LIKE OS_PRETTY <<EOF
$_pf_line
EOF
    OS_CLASS=$(check_os_supported "$OS_ID" "$OS_VER" "$OS_LIKE")

    case "$OS_CLASS" in
    supported)
        info "  os:              ${OS_PRETTY:-$OS_ID $OS_VER} (supported)"
        ;;
    nopkg)
        info "  os:              ${OS_PRETTY:-$OS_ID $OS_VER}"
        warn "rpm hosts cannot be inventoried: the rpm store needs librpm, so the package" \
            "collector will be disabled. Everything else works."
        ;;
    *)
        # The warn is OUTSIDE the branch below on purpose: --unsupported-os
        # suppresses the PROMPT, not the DIAGNOSIS. An operator who forced the
        # install still gets told which distro this is and what netra knows,
        # and warn() puts it in the finish report as well as the scroll.
        warn "${OS_PRETTY:-$OS_ID ${OS_VER:-unknown}} is not a distribution/version netra is" \
            "known to support (Debian 11+, Ubuntu 22.04+, Alpine 3.18+, RHEL family 9+)." \
            "The checks that actually matter are probed directly, so this may still work."
        # Two branches into one outcome, as in plan_smart: the flag is a pure
        # grant, never a second code path that could drift from the prompted one.
        # `if netra_ask` — see the header. A bare call would end the script here.
        _pf_os_ok=0
        if [ "${GRANT_UNSUPPORTED_OS:-0}" = 1 ]; then
            info "  os:              ${OS_PRETTY:-$OS_ID $OS_VER} (unsupported, continuing by --unsupported-os)"
            _pf_os_ok=1
        elif netra_ask "Continue on an unsupported OS?" n --unsupported-os; then
            info "  os:              ${OS_PRETTY:-$OS_ID $OS_VER} (unsupported, continuing at operator request)"
            _pf_os_ok=1
        fi
        if [ "$_pf_os_ok" != 1 ]; then
            # The floors are advisory (§12a), so the refusal must name its own
            # remedy or an unattended install on a cgroup-v2 host netra simply
            # does not recognise by name has no way back in.
            die "aborted: unsupported operating system." \
                "Re-run with --unsupported-os to proceed without prompting."
        fi
        ;;
    esac

    check_cgroup_v2
    check_machine_id

    DOCKER_COMPOSE=$(check_docker)
    info "  docker:          daemon reachable, $DOCKER_COMPOSE"
}

detect_packages() {
    step "Package inventory"

    PKGMGR=$(detect_pkgmgr "$OS_ID" "$OS_LIKE")
    PKG_MOUNT=""
    PKG_ENABLED=0

    case "$PKGMGR" in
    dpkg)
        # EMIT path: the real host path, never prefixed. See the header.
        PKG_MOUNT=/var/lib/dpkg
        ;;
    apk)
        PKG_MOUNT=/lib/apk/db
        ;;
    rpm)
        warn "package manager is rpm: unsupported (its Berkeley DB/SQLite store needs" \
            "librpm), so the package collector stays disabled and reports an" \
            "unsupported-format capability rather than silently collecting nothing."
        info "  package manager: rpm (collector disabled)"
        return 0
        ;;
    *)
        warn "no dpkg or apk database found, so the package collector will be disabled."
        info "  package manager: none detected"
        return 0
        ;;
    esac

    info "  package manager: $PKGMGR ($PKG_MOUNT)"
    # `if netra_ask` — see the header.
    if netra_ask "Mount $PKG_MOUNT read-only so the package collector can run?" y; then
        PKG_ENABLED=1
    else
        PKG_MOUNT=""
        warn "package inventory declined: the '$PKGMGR' collector will not run, so there" \
            "will be no install/upgrade/remove timeline for this host."
    fi
}

# plan_extras — the mounts and namespaces that are not implied by hardware.
#
# THREE SEPARATE PROMPTS, never one "enable optional features?" question. They
# have wildly different costs: a read-only D-Bus socket is unremarkable, and
# `pid: host` hands this container every process's cmdline and environ.
plan_extras() {
    step "Optional extras"

    # Not prompted, and deliberately so: the Docker socket and the mount table
    # are what the container and filesystem-discovery collectors are FOR, and an
    # agent installed without them is not the thing the operator asked for. Both
    # are read-only. Their absence is a note.
    DOCKERSOCK_ENABLED=0
    if [ -e "$P_DOCKERSOCK" ]; then
        DOCKERSOCK_ENABLED=1
        info "  docker socket:   /var/run/docker.sock (read-only)"
    else
        warn "no Docker socket at $P_DOCKERSOCK: no container inventory and no container" \
            "metrics."
    fi

    MOUNTINFO_ENABLED=0
    if [ -f "$P_MOUNTINFO" ]; then
        MOUNTINFO_ENABLED=1
        info "  mount table:     /proc/1/mountinfo (read-only, awareness only)"
    fi

    DBUS_ENABLED=0
    if [ -e "$P_DBUS" ]; then
        # `if netra_ask` — see the header.
        if netra_ask "Mount the D-Bus system socket read-only so the systemd collector can run?" y; then
            DBUS_ENABLED=1
        else
            warn "D-Bus declined: no systemd unit inventory and no failed-service alerting."
        fi
    else
        info "  d-bus:           $P_DBUS not present (no systemd collector)"
    fi

    PID_HOST=0
    if [ "${GRANT_PID_HOST:-0}" = 1 ]; then
        PID_HOST=1
        info "  processes:       pid: host enabled by --pid-host (not prompted)"
    elif netra_ask "Share the host PID namespace (pid: host) for per-process metrics?
  WARNING: this makes EVERY process's /proc entry readable to the agent container,
  including cmdline and environ — the command-line arguments and environment
  variables of every service on this box, which routinely carry passwords, API
  keys and DSNs. The collector only aggregates CPU and memory by process name,
  but the namespace grants far more than it uses." n --pid-host; then
        PID_HOST=1
    else
        info "  processes:       declined (no per-process metrics; --pid-host enables it)"
    fi
}

# resolve_token — --token, --token-file, or a prompt with echo off.
#
# The installer NEVER invents a token: the hub mints them and stores only a
# SHA-256, so a value made up here could never authenticate. An unattended run
# with no token renders an empty NETRA_TOKEN and says loudly that the agent will
# refuse to start until it is filled in — which is a better outcome than dying
# after the operator has already answered every prompt.
resolve_token() {
    if [ -n "$TOKEN_FILE" ]; then
        if [ ! -f "$TOKEN_FILE" ]; then
            die "--token-file $TOKEN_FILE does not exist"
        fi
        TOKEN=$(head -n 1 "$TOKEN_FILE" | tr -d '\r')
        [ -n "$TOKEN" ] || die "--token-file $TOKEN_FILE is empty"
    fi
    [ -z "$TOKEN" ] || return 0

    if [ "$ASSUME_YES" = 1 ] || [ ! -r "$P_TTY" ]; then
        warn "no agent token was provided (--token / --token-file). NETRA_TOKEN will be" \
            "written empty and the agent will refuse to start until you fill it in." \
            "Tokens are minted by the hub; the installer cannot invent one."
        return 0
    fi

    printf 'Agent token from the hub (starts with nta_, input hidden): '
    # `command -v stty` guarded: a minimal container image may not ship it, and
    # a visible token is better than a failed install. The trap restores echo if
    # the read is interrupted.
    if command -v stty >/dev/null 2>&1; then
        _rt_saved=$(stty -g <"$P_TTY" 2>/dev/null || printf '')
        [ -z "$_rt_saved" ] || stty -echo <"$P_TTY" 2>/dev/null || true
        IFS= read -r TOKEN <"$P_TTY" || TOKEN=""
        [ -z "$_rt_saved" ] || stty "$_rt_saved" <"$P_TTY" 2>/dev/null || true
        printf '\n'
    else
        IFS= read -r TOKEN <"$P_TTY" || TOKEN=""
    fi

    if [ -z "$TOKEN" ]; then
        warn "no token entered. NETRA_TOKEN will be written empty and the agent will refuse" \
            "to start until you fill it in."
    fi
}

# print_plan — SUMMARISED, never dumped.
#
# A host with hundreds of Docker overlay mounts would otherwise produce a
# consent prompt nobody reads, and a consent prompt nobody reads is not consent.
# Accepted items are listed in full (there are never many after filtering);
# rejections are counted by kind, with the interesting ones already in the notes.
print_plan() {
    step "Plan"

    if [ -n "$FS_MOUNTS" ]; then
        info "  filesystems to measure (empty .netra marker dirs, no data exposure):"
        while IFS='|' read -r _pp_mm _pp_mp _pp_lab; do
            [ -n "$_pp_mm" ] || continue
            info "    $_pp_mp -> /netra/fs/$_pp_lab"
        done <<EOF
$FS_MOUNTS
EOF
    else
        info "  filesystems to measure: none (legitimate on a container-only host)"
    fi

    if [ -n "$FS_SKIPS" ]; then
        _pp_total=$(printf '%s\n' "$FS_SKIPS" | grep -c .)
        info "  mounts skipped:  $_pp_total ($(printf '%s\n' "$FS_SKIPS" |
            awk -F'|' '{c[$1]++} END {s=""; for (k in c) s = s (s ? ", " : "") k ": " c[k]; print s}'))"
    fi

    if [ -n "$SMART_DEVICES" ]; then
        info "  smart devices:   $(printf '%s' "$SMART_DEVICES" | tr '\n' ' ')"
    else
        info "  smart devices:   none"
    fi
    info "  capabilities:    $(_plan_caps)"
    info "  pid namespace:   $(if [ "$PID_HOST" = 1 ]; then printf 'host (per-process metrics)'; else printf 'container (default)'; fi)"
    info "  package mount:   ${PKG_MOUNT:-none}"
    info "  d-bus socket:    $(if [ "$DBUS_ENABLED" = 1 ]; then printf 'yes'; else printf 'no'; fi)"
    info "  hub url:         ${HUB_URL:-(not set)}"
    # The token is NEVER printed, here or anywhere else.
    info "  token:           $(if [ -n "$TOKEN" ]; then printf '****'; else printf '(not provided)'; fi)"
    info "  output dir:      $OUTPUT_DIR"
}

_plan_caps() {
    _pc_out=""
    [ "$CAP_RAWIO" = 0 ] || _pc_out="SYS_RAWIO"
    if [ "$CAP_SYS_ADMIN" = 1 ]; then
        _pc_out="${_pc_out:+$_pc_out }SYS_ADMIN"
    fi
    printf '%s' "${_pc_out:-none}"
}

# write_outputs — everything that touches the host, and nothing else does.
write_outputs() {
    step "Render"

    SCRATCH_DIR=$(mktemp -d) || die "could not create a temporary directory"
    resolve_ref

    _wo_compose_tmpl=$(fetch_template compose.yaml.tmpl)
    _wo_env_tmpl=$(fetch_template env.tmpl)

    build_blocks
    _env_value HUB_URL "$HUB_URL"
    _env_value TOKEN "$TOKEN"
    _env_value PRIMARY_SENSOR "$PRIMARY_SENSOR"

    netra_exec mkdir -p "$OUTPUT_DIR"

    # Marker directories. The mount point is a PROBE path here (so a test can
    # redirect it) and an EMIT path in compose.yaml; _p() on the way in, never on
    # the way out.
    while IFS='|' read -r _wo_mm _wo_mp _wo_lab; do
        [ -n "$_wo_mm" ] || continue
        if [ "$_wo_mp" = "/" ]; then
            netra_exec mkdir -p "$(_p /.netra)"
        else
            netra_exec mkdir -p "$(_p "$_wo_mp/.netra")"
        fi
    done <<EOF
$FS_MOUNTS
EOF

    # compose.yaml is overwritten freely and .env is not (§12a). compose.yaml is
    # derived output — every byte of it comes from this run's detection — while
    # .env holds the token, and a provisioning script that re-ran the installer
    # must not be able to silently replace a working one.
    if netra_ask "Write $OUTPUT_DIR/compose.yaml?" y; then
        netra_exec netra_write_compose "$_wo_compose_tmpl" "$OUTPUT_DIR/compose.yaml"
    else
        warn "compose.yaml not written, so nothing detected above has been applied."
    fi

    if [ -e "$OUTPUT_DIR/.env" ] && [ "$FORCE" != 1 ]; then
        warn "$OUTPUT_DIR/.env already exists and --force was not given, so it was left" \
            "untouched. Its existing token and settings still apply. Re-run with --force to" \
            "overwrite it."
    elif netra_ask "Write $OUTPUT_DIR/.env?" y; then
        netra_exec netra_write_env "$_wo_env_tmpl" "$OUTPUT_DIR/.env"
    else
        warn ".env not written; the agent has no hub URL or token and will refuse to start."
    fi

    rm -rf "$SCRATCH_DIR"
}

# compose_cmd — whichever of the two spellings this host actually has.
compose_cmd() {
    if [ "$DOCKER_COMPOSE" = compose_v1 ]; then
        printf 'docker-compose'
    else
        printf 'docker compose'
    fi
}

print_finish() {
    step "Summary"
    info "  os:              ${OS_PRETTY:-$OS_ID $OS_VER} [$OS_CLASS]"
    info "  filesystems:     $(printf '%s' "${FS_MOUNTS:-}" | grep -c . || true) measured"
    info "  smart devices:   $(printf '%s' "$SMART_DEVICES" | tr '\n' ' ')"
    info "  capabilities:    $(_plan_caps)"
    info "  package manager: $PKGMGR${PKG_MOUNT:+ ($PKG_MOUNT)}"
    info "  primary sensor:  ${PRIMARY_SENSOR:-auto (chosen by the agent at runtime)}"
    info "  output dir:      $OUTPUT_DIR"

    if [ -n "$SKIPPED_NOTES" ]; then
        info ""
        info "  Skipped or degraded:"
        while IFS= read -r _fr_note; do
            [ -n "$_fr_note" ] || continue
            info "    - $_fr_note"
        done <<EOF
$SKIPPED_NOTES
EOF
    fi

    info ""
    info "  compose.yaml is DERIVED and is overwritten on every run; .env is NOT, because"
    info "  it holds the token. If you hand-edited either one, re-check it now: your"
    info "  compose.yaml changes are gone and your .env changes were kept."
    info ""
    if [ "$START" = 1 ]; then
        info "  Starting the stack:"
    else
        info "  A changed compose.yaml does nothing until the stack is recreated. Run:"
    fi
    info ""
    info "    cd $OUTPUT_DIR && $(compose_cmd) up -d"
    info ""
}

start_stack() {
    [ "$START" = 1 ] || return 0
    step "Start"
    if [ "$DOCKER_COMPOSE" = compose_v1 ]; then
        netra_exec docker-compose -f "$OUTPUT_DIR/compose.yaml" up -d
    else
        netra_exec docker compose -f "$OUTPUT_DIR/compose.yaml" up -d
    fi
}

netra_main() {
    parse_args "$@"
    init_paths

    if [ "${NETRA_DEBUG_PATHS:-0}" = 1 ]; then
        debug_paths
        return 0
    fi

    info "netra agent installer $NETRA_INSTALLER_VERSION"
    if [ "$DRY_RUN" = 1 ]; then
        info "(dry run: nothing will be created, written or started)"
    fi

    preflight
    detect_filesystems
    plan_smart
    detect_sensors
    detect_packages
    plan_extras
    resolve_token
    print_plan
    write_outputs
    print_finish
    start_stack
}

# Guarded entrypoint, and the last line of the file on purpose. Tests source
# this script with NETRA_SOURCED=1 to unit-test individual functions; a
# `curl ... | sh` pipeline runs it.
[ "${NETRA_SOURCED:-0}" = 1 ] || netra_main "$@"
