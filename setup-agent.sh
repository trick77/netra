#!/bin/sh
#
# netra agent setup script.
#
#   curl -fsSL https://raw.githubusercontent.com/trick77/netra/master/setup-agent.sh | sh
#   sh setup-agent.sh --help
#
# Detects what this host actually has, asks before changing anything, and renders
# the agent's compose.yaml and .env from templates. It INSTALLS NOTHING: Docker
# pulls the agent image when the stack is started.
# See docs/superpowers/specs/2026-08-07-netra-design.md §12a.
#
# ============================================================================
# PROBE PATHS vs EMIT PATHS — read this before adding any path
# ============================================================================
# Every path in this script is exactly one of two kinds:
#
#   PROBE path — read during detection. It is resolved through _p(), so tests
#                can redirect it under AGENT_SETUP_ROOT at a fixture tree.
#
#   EMIT path  — written into the rendered compose.yaml/.env. It must stay the
#                REAL host path, always, with no prefix.
#
# AGENT_SETUP_ROOT applies ONLY when resolving a probe variable. It must never
# reach a string that goes into a template. Prefix a marker file on its way
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
#   prefix. Shared state is UPPERCASE (P_CGROUP, SKIPPED_NOTES).
# * NEVER `cmd | while read` — the loop runs in a subshell and the accumulator
#   is lost when it exits. Use a heredoc-fed loop instead:
#       while IFS= read -r l; do ...; done <<EOF
#   $(producer)
#   EOF
# * No arrays. Multi-valued state is a newline-delimited string, split with a
#   heredoc-fed loop and IFS='|' for fields.
# * printf, never echo. No `read -p`. No `readlink -f`. No `${var,,}` (use tr).
# * No `.`/`source` of anything from the host. /etc/os-release is untrusted
#   input this script is validating; it is parsed with awk.
#
# ============================================================================
# curl | sh CONSEQUENCES
# ============================================================================
# * stdin IS this script. `read` from stdin returns the script's own bytes, so
#   ALL prompting reads $P_TTY (/dev/tty), never stdin.
# * If $P_TTY is unreadable the setup script FAILS IMMEDIATELY, before any
#   detection runs, rather than assuming answers: a run that quietly said "no"
#   to the package mount would produce an agent that collects less than the
#   operator thinks it does. There is NO unattended mode — see below.
#
# ============================================================================
# THERE IS NO UNATTENDED MODE
# ============================================================================
# The script asks, the operator answers, and one of the answers loads a kernel
# module — none of that belongs in a provisioning run nobody is watching. A
# fleet is configured from deploy/agent/compose.yaml.example and .env.example,
# templated by whatever provisioning system already exists.
#
# require_tty() enforces this once, in netra_main, before any phase. It is
# exempted in exactly one case:
#
#   AGENT_ANSWERS_FILE    the test seam, checked before $P_TTY in netra_ask.
#
# That IS a non-interactive path, and the README says so rather than pretending
# otherwise. It is an env var, not a flag, and its answers are positional — fit
# for the suite, not a supported provisioning interface.
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
# The mirror-image trap, which every added detector meets: a
# capturing assignment `X=$(detect_something)` DOES propagate a die() correctly
# under a bare `set -e` (DOCKER_COMPOSE=$(check_docker) relies on this), but put
# that same assignment inside an if/&&/|| and errexit is suspended — the
# subshell's exit 1 is swallowed, the error message prints, and the script sails
# on with X empty. Capture at statement level; test the value afterwards.

# ============================================================================
# PROMPT ORDER — a contract, not an implementation detail
# ============================================================================
# Every prompt reads its answer from the same sequence, and AGENT_ANSWERS_FILE
# in the tests is that sequence written down. Reordering these silently rewrites
# what every answers file in test/setup-agent/cases/ means: line 3 stops being
# "no to SYS_ADMIN" and becomes "no to the package mount", and the tests still
# pass while asserting something else. Change the order only by changing the
# answers files with it.
#
#   1. continue on an unsupported OS   (only when the distro is unrecognised)
#   2. SYS_ADMIN                       (only when NVMe exists; default NO)
#   3. load the drivetemp module       (only with SATA drives and no such chip)
#   4. write everything                (the single gate; default YES)
#
# Only ONE of the four is asked on every host. Everything read-only — the
# package database, the D-Bus socket, SYS_RAWIO — is enabled automatically, on
# exactly the argument plan_extras already makes for the Docker socket: it is
# read-only, and an agent configured without it is not the thing the operator
# asked for. The primary-sensor tie is resolved by --primary-sensor, not by a
# prompt with a variable count. And nothing about host CPU, memory or load is
# ever asked: that is the product.
#
# Free-text values (hub URL, token, location, provider, host type) are read by
# netra_ask_value from AGENT_VALUES_FILE, a SEPARATE seam with its own index, so
# adding one never shifts a y/n answers file by a line.
#
# --unsupported-os and --sys-admin REMOVE prompts 1 and 2 from this sequence:
# the answer is taken without asking, so nothing is consumed for them. A case
# that passes either flag and an answers file must drop the matching line, or
# every answer after it means something else. Prompt 3 (drivetemp) has no flag;
# it disappears only when the host gives it no reason to be asked.
# Renumber this WITH the list above or the warning is worthless.
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

AGENT_SETUP_VERSION="0.1.0"

# ---------------------------------------------------------------------------
# Shared state
# ---------------------------------------------------------------------------

# "" in production. Tests point it at a fixture tree. PROBE paths only.
AGENT_SETUP_ROOT="${AGENT_SETUP_ROOT:-}"

# Newline-delimited notes for the finish report: everything that was skipped,
# declined or degraded, so the operator sees what the agent will NOT collect.
SKIPPED_NOTES=""

# Newline-delimited ledger of what this run actually CHANGED on the host, for
# the finish report. The counterpart to SKIPPED_NOTES: that one lists what the
# agent will not collect, this one lists what now exists on the box that did
# not before.
#
# THE RULE for anyone adding a mutation: record into it at the point the change
# SUCCEEDS — inside the branch that did the work, after the command returned 0 —
# never up front and never as a block assembled in print_finish. A summary
# assembled at the end can only describe what the script intended, and on a
# re-run that is a lie: compose.yaml is overwritten while .env is left alone, a
# marker file is created on the first run and already there on the second.
# The ledger has to be able to tell those apart, and it can only do that if the
# entry is written where the difference is known.
#
# Paths only. No value the operator gave us goes in here — the token least of
# all.
HOST_CHANGES=""

# Set by plan_drivetemp when it has actually changed the host — the one mutation
# that happens BEFORE the write gate, because the sensor scan has to see its
# result. Declining the gate must not then claim "nothing was changed", which
# would be a lie on exactly this path, and the operator would have no idea what
# to undo.
DRIVETEMP_CHANGED=""

# Set separately from DRIVETEMP_CHANGED, because the two changes are separate
# and either can happen without the other: the module can be loaded and then
# fail to unload, leaving no file behind, and it is never persisted without
# first being loaded. The undo instructions must name only what happened.
DRIVETEMP_PERSISTED=""

# Consumed answers when AGENT_ANSWERS_FILE is set (test seam only). The two
# indexes are deliberately independent: a y/n prompt and a free-text prompt read
# from different files, so neither can shift the other by a line.
AGENT_ANSWER_INDEX=0
AGENT_VALUE_INDEX=0

# Defined before any function can reference them: the tests source this file and
# call individual functions without going through netra_main, and `set -u` would
# abort on the first unset colour. init_colors fills them in for a real run.
C_RESET=""
C_BOLD=""
C_RED=""
C_YELLOW=""
C_CYAN=""

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

# init_colors — ANSI escapes, or empty strings when they would be noise.
#
# BOTH streams must be terminals. A run whose stdout is piped into a file and
# whose stderr is not would otherwise write escape codes into the file, and the
# suite captures with 2>&1 into a command substitution, which is neither — so
# the tests see plain text without asking for it.
#
# NO_COLOR is honoured (no-color.org), as is TERM=dumb. Colour is applied to the
# THREE things that mark a place in the scroll — phase headings, warnings and
# the prompt hint — and to nothing else: colouring every value is how output
# stops being scannable rather than starts.
init_colors() {
    C_RESET=""
    C_BOLD=""
    C_RED=""
    C_YELLOW=""
    C_CYAN=""

    [ -t 1 ] || return 0
    [ -t 2 ] || return 0
    [ -z "${NO_COLOR:-}" ] || return 0
    case "${TERM:-}" in
    "" | dumb) return 0 ;;
    esac

    C_RESET=$(printf '\033[0m')
    C_BOLD=$(printf '\033[1m')
    C_RED=$(printf '\033[31m')
    C_YELLOW=$(printf '\033[33m')
    C_CYAN=$(printf '\033[36m')
}

# die MSG... — one-line error on stderr, exit 1.
#
# NO PIPELINE, and no `sed`. This used to colour the label by piping _wrap
# through sed; under `set -e` a host with no `sed` failed the pipeline and
# aborted die BEFORE `exit 1` was reached — no message, exit 127. The victim was
# check_tools, whose entire job is to name the missing command. The label is a
# known literal, so stripping it back off the wrapped text and re-emitting it
# coloured needs nothing but builtins. Colour is applied AFTER wrapping because
# an escape sequence inside the text would be counted toward the fold width.
die() {
    _die_out=$(_wrap 76 'setup-agent: error: ' '  ' "$*")
    printf '%s%s\n' "${C_RED:-}${C_BOLD:-}setup-agent: error:${C_RESET:-}" \
        "${_die_out#setup-agent: error:}" >&2
    exit 1
}

# _wrap WIDTH PREFIX INDENT TEXT — fold PREFIX+TEXT at WIDTH columns, every line
# after the first starting with INDENT.
#
# PREFIX is separate from TEXT because the word split below destroys leading
# whitespace: passing "    - note" as the text produced a bullet at column 0
# under continuation lines indented six, which is worse than not wrapping at all.
# The indent counts toward WIDTH for the same reason a prefix does — a fold that
# ignores it overruns by exactly the indent on every continuation line.
#
# Pure shell: no awk, no `fold` (which breaks mid-word), no external process at
# all. Wrapping is the one thing every message in this script passes through, so
# it is also the one thing that must not add a dependency — a host missing awk
# would otherwise fail while printing the message explaining what it was missing.
#
# `set -f` around the word split is load-bearing: the split relies on unquoted
# expansion, and a message containing * or ? would otherwise be replaced by
# matching filenames from the current directory. Restored immediately, because
# the rest of this script relies on globbing (the hwmon and block-device scans).
_wrap() {
    _wr_w="$1"
    _wr_pfx="$2"
    _wr_ind="$3"
    _wr_line=""

    set -f
    # shellcheck disable=SC2086 # deliberate: split the text into words on IFS.
    # ${4:-} rather than $4: `set -u` makes a missing argument fatal, and a
    # caller wrapping an empty message is asking for the prefix alone.
    set -- ${4:-}
    set +f

    for _wr_word in "$@"; do
        if [ -z "$_wr_line" ]; then
            _wr_line="$_wr_pfx$_wr_word"
        elif [ $((${#_wr_line} + 1 + ${#_wr_word})) -gt "$_wr_w" ]; then
            printf '%s\n' "$_wr_line"
            _wr_line="$_wr_ind$_wr_word"
        else
            _wr_line="$_wr_line $_wr_word"
        fi
    done
    if [ -z "$_wr_line" ]; then
        # No words at all: the prefix alone is still the caller's line.
        [ -z "$_wr_pfx" ] || printf '%s\n' "$_wr_pfx"
    else
        printf '%s\n' "$_wr_line"
    fi
}

# warn MSG... — a degradation the operator must know about. Also accumulates
# into SKIPPED_NOTES so the finish report repeats it after the scroll.
#
# WRAPPED, because these messages are long by design — they name the missing
# metric, the cause and the remedy — and a paragraph printed as one 400-column
# line is a wall of text the operator's eye slides off. The note stored in
# SKIPPED_NOTES stays a SINGLE line: the finish report is a list of notes, and a
# wrapped note would turn one degradation into four bullets.
warn() {
    _warn_msg="$*"
    # Wrapped as PLAIN text and coloured afterwards: an escape sequence inside
    # the text would be counted as characters and throw the fold off. Builtins
    # only, for the reason spelled out on die.
    _warn_out=$(_wrap 76 'setup-agent: warning: ' '  ' "$_warn_msg")
    printf '%s%s\n' "${C_YELLOW:-}${C_BOLD:-}setup-agent: warning:${C_RESET:-}" \
        "${_warn_out#setup-agent: warning:}" >&2
    if [ -n "$SKIPPED_NOTES" ]; then
        SKIPPED_NOTES="$SKIPPED_NOTES
$_warn_msg"
    else
        SKIPPED_NOTES="$_warn_msg"
    fi
}

# warn_cmd CMD MSG... — a warning whose remedy is a command. The command is
# printed on its OWN line, indented and unwrapped, because a shell command
# folded into a paragraph cannot be copied and pasted, which is the only thing
# anyone wants to do with it.
#
# The command rides into SKIPPED_NOTES as a separate entry marked with a leading
# tab, which print_finish renders verbatim instead of as another bullet.
warn_cmd() {
    _wc_cmd="$1"
    shift
    warn "$@"
    printf '    %s\n' "$_wc_cmd" >&2
    SKIPPED_NOTES="$SKIPPED_NOTES
	$_wc_cmd"
}

info() {
    printf '%s\n' "$*"
}

# record_change MSG... — one line onto the HOST_CHANGES ledger. See the note on
# the variable for WHERE this may be called from; the short version is "after
# the mutation succeeded, and nowhere else".
#
# Prints NOTHING. Each phase already narrates itself as it goes, and a second
# copy of the same line at the moment it happens would add noise to the scroll
# while adding nothing to the report. This exists purely to survive the scroll.
#
# Kept to a single line per entry, like SKIPPED_NOTES and for the same reason:
# the report is a list, and a wrapped entry would read as several changes.
record_change() {
    if [ -n "$HOST_CHANGES" ]; then
        HOST_CHANGES="$HOST_CHANGES
$*"
    else
        HOST_CHANGES="$*"
    fi
}

step() {
    printf '\n%s==> %s%s\n' "${C_CYAN:-}${C_BOLD:-}" "$*" "${C_RESET:-}"
}

# ---------------------------------------------------------------------------
# The single mutation choke point
# ---------------------------------------------------------------------------

# netra_exec CMD... — runs CMD. EVERY mkdir, file write and
# `docker compose up -d` goes through this single choke point, so the set of
# things this script can change to the host is one grep away.
netra_exec() {
    "$@"
}

# ---------------------------------------------------------------------------
# Prompting
# ---------------------------------------------------------------------------

# netra_ask QUESTION DEFAULT - DEFAULT is `y` or `n`. Returns 0 for yes, 1 for
# no.
#
# CALL SITES MUST BE `if netra_ask ...; then` - see the errexit note in the
# header. A bare call kills the script the moment the operator says no.
#
# AGENT_ANSWERS_FILE is a test seam: each call consumes the next line of that
# file (an exhausted file is a hard error, not a silent default). Otherwise a
# readable $P_TTY is required, which require_tty has already checked.
netra_ask() {
    _ask_q="$1"
    _ask_def="$2"
    if [ "$_ask_def" = y ]; then
        _ask_hint="${C_BOLD:-}[Y/n]${C_RESET:-}"
    else
        _ask_hint="${C_BOLD:-}[y/N]${C_RESET:-}"
    fi

    while :; do
        if [ -n "${AGENT_ANSWERS_FILE:-}" ]; then
            # sed -n "${n}p", not head/tail rewriting: rewriting would destroy
            # the test's own input and break on a read-only file.
            AGENT_ANSWER_INDEX=$((AGENT_ANSWER_INDEX + 1))
            _ask_reply=$(sed -n "${AGENT_ANSWER_INDEX}p" "$AGENT_ANSWERS_FILE")
            if [ -z "$_ask_reply" ]; then
                die "AGENT_ANSWERS_FILE exhausted at answer $AGENT_ANSWER_INDEX (question: $_ask_q)"
            fi
            printf '%s %s %s\n' "$_ask_q" "$_ask_hint" "$_ask_reply"
        else
            printf '%s %s ' "$_ask_q" "$_ask_hint"
            if ! IFS= read -r _ask_reply <"$P_TTY"; then
                die "could not read an answer from $P_TTY"
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

        if [ -n "${AGENT_ANSWERS_FILE:-}" ]; then
            die "AGENT_ANSWERS_FILE line $AGENT_ANSWER_INDEX is '$_ask_reply', expected y or n"
        fi
        printf 'please answer y or n\n'
    done
}

# netra_ask_value VARNAME PROMPT [EXAMPLE] - a free-text answer, ASSIGNED to
# VARNAME. An empty answer leaves VARNAME as it was, which is not an error: an
# operator who has not minted a token yet must still be able to finish the run.
#
# It assigns rather than printing to stdout for a reason that cost an afternoon:
# `X=$(netra_ask_value ...)` runs the function in a SUBSHELL, so the
# AGENT_VALUE_INDEX increment below would be discarded on every call. The values
# file would hand out its first line forever, and the host-type validation loop
# below would spin on it without end. This is the same class of trap as the
# `cmd | while read` note in the header: state does not survive a subshell.
#
# AGENT_VALUES_FILE is the test seam, with its OWN index. Keeping it separate
# from AGENT_ANSWERS_FILE is the whole point: adding a value prompt must never
# shift a y/n answers file by a line. Unlike the y/n seam an exhausted file is
# NOT fatal - a case that cares about one value should not have to spell out
# every other one.
netra_ask_value() {
    _av_var="$1"
    _av_q="$2"
    _av_eg="${3:-}"
    # Assigned literally first so shellcheck can see it: the eval below is
    # invisible to static analysis, and SC2154 is right to say so.
    _av_def=""
    eval "_av_def=\$$_av_var"

    if [ -n "${AGENT_VALUES_FILE:-}" ]; then
        AGENT_VALUE_INDEX=$((AGENT_VALUE_INDEX + 1))
        _av_reply=$(sed -n "${AGENT_VALUE_INDEX}p" "$AGENT_VALUES_FILE")
    elif [ ! -r "$P_TTY" ]; then
        _av_reply=""
    else
        if [ -n "$_av_eg" ]; then
            printf '%s (e.g. %s): ' "$_av_q" "$_av_eg"
        else
            printf '%s: ' "$_av_q"
        fi
        IFS= read -r _av_reply <"$P_TTY" || _av_reply=""
    fi

    # A CR arrives from anything Windows-shaped and would become part of the
    # value in .env, where every request 401s with nothing in the logs to say why.
    _av_reply=$(printf '%s' "$_av_reply" | tr -d '\r')
    if [ -z "$_av_reply" ]; then
        _av_reply="$_av_def"
    fi
    eval "$_av_var=\$_av_reply"
}

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------

# describe_setup - the one description of what this script does, shared by
# --help and the opening banner so the two cannot drift. $OUTPUT_DIR must
# already be final when this runs (parse_args + the basename rule).
describe_setup() {
    cat <<EOF
Writes two files. Installs nothing — Docker pulls the agent image at \`up -d\`.

  $OUTPUT_DIR/compose.yaml   generated, overwritten every run
  $OUTPUT_DIR/.env           hub URL and token, never overwritten

Plus an empty .netra marker file on each measured filesystem. Nothing is written
until you approve the plan; nothing starts unless you pass --start.
EOF
}

usage() {
    cat <<'EOF'
Usage: setup-agent.sh [options]

Detects this host's capabilities and writes the netra agent's compose.yaml and
.env. It installs no software - Docker pulls the agent image when the stack is
started. Nothing is created, written or started until you agree.

This script is INTERACTIVE. There is no unattended mode: it needs a terminal and
fails immediately without one. To configure a fleet, template
deploy/agent/compose.yaml.example and .env.example from your provisioning system.

Everything read-only is enabled automatically - the Docker socket, the mount
table, the package database, the D-Bus socket and SYS_RAWIO for SATA SMART.
SYS_ADMIN is the only privilege this asks about. `pid: host` is always granted:
without it the agent reports no per-container network and no process count, and
a monitoring agent that cannot see the host's process namespace has a hole in
it. Host CPU, memory and load are always collected and are not optional.

Options:
      --sys-admin          Grant SYS_ADMIN without prompting (NVMe SMART health
                           and wear). A no-op with a note if there is no NVMe.
      --pid-host           Accepted and ignored. `pid: host` is now always
                           rendered; the flag stays parsed so existing
                           provisioning does not fail on an unknown option.
      --unsupported-os     Continue on a distro netra does not recognise,
                           without prompting. The version floors are only where
                           cgroup v2 became the default and the real checks are
                           probed directly, so this is how a run reaches a
                           cgroup v2 host netra has no name for. The warning is
                           still printed and still reported; only the prompt
                           goes away. A no-op on a known distro.
      --assume-physical    Treat this host as bare metal even if it looks
                           virtual. Virtual disks carry no SMART data, so SMART
                           is skipped on a detected hypervisor; this overrides
                           that.
      --force              Required to overwrite an existing .env.
      --start              Run `docker compose up -d` at the end.
      --token VALUE        Agent token minted by the hub (starts with nta_).
      --token-file PATH    Read the agent token from PATH instead.
      --hub-url VALUE      Hub base URL, e.g. https://netra.example.com
      --location VALUE     Where this host is, e.g. "Zurich, CH".
      --provider VALUE     Who hosts it, e.g. Hetzner, OVH, self-hosted.
      --host-type VALUE    One of bare_metal, vps, vm.
      --ref VALUE          Git ref to fetch templates from. Without it the
                           latest release tag is resolved at run time; never
                           master, in either case.
      --template-dir PATH  Use local template files instead of fetching them
                           (no network at all).
      --output-dir PATH    Where compose.yaml and .env are written (default:
                           ./netra-agent, or ./ when the current directory is
                           already named netra-agent).
      --primary-sensor VALUE
                           Override automatic primary-sensor selection.
      --include-network-fs Also offer NFS/CIFS/SMB filesystems.
  -h, --help               Print this and exit.

The values asked for interactively - hub URL, token, location, provider and
host type - can each be passed as a flag instead, in which case the prompt is
skipped.
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
    # Privilege is granted by name. Each of these removes a prompt from the
    # sequence in the header; see the note there about answers files.
    GRANT_SYS_ADMIN=0
    # Not privilege, but the same shape: a prompt that defaults n, which an
    # operator who means it takes by name.
    GRANT_UNSUPPORTED_OS=0
    # Not a grant: an override of a DETECTION. A virtual host has no SMART data
    # behind its disks, and this says "you detected wrong, this box is real".
    ASSUME_PHYSICAL=0
    FORCE=0
    START=0
    TOKEN=""
    TOKEN_FILE=""
    HUB_URL=""
    LOCATION=""
    PROVIDER=""
    HOST_TYPE=""
    # REF defaults to the setup script's own version tag, and REF_EXPLICIT records
    # whether the operator chose it. Without that flag, "not given" and "given
    # as exactly the default" are indistinguishable after parsing, and the
    # runtime latest-release lookup would either never run or override an
    # explicit --ref. Never master, in either branch.
    REF="v$AGENT_SETUP_VERSION"
    REF_EXPLICIT=0
    TEMPLATE_DIR=""
    # OUTPUT_DIR_EXPLICIT mirrors REF_EXPLICIT above, and for the same reason:
    # after the loop, "not given" and "given as exactly the default" would
    # otherwise be indistinguishable, and the basename rule below must not
    # override a directory the operator named on purpose.
    OUTPUT_DIR="./netra-agent"
    OUTPUT_DIR_EXPLICIT=0
    PRIMARY_SENSOR=""
    INCLUDE_NETWORK_FS=0

    while [ "$#" -gt 0 ]; do
        case "$1" in
        --sys-admin) GRANT_SYS_ADMIN=1 ;;
        # Accepted and ignored: `pid: host` is unconditional now. Kept so
        # existing provisioning does not fail on an unknown option.
        --pid-host) ;;
        --unsupported-os) GRANT_UNSUPPORTED_OS=1 ;;
        --assume-physical) ASSUME_PHYSICAL=1 ;;
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
        --location)
            _need_val "$1" "$#"
            LOCATION="$2"
            shift
            ;;
        --provider)
            _need_val "$1" "$#"
            PROVIDER="$2"
            shift
            ;;
        --host-type)
            _need_val "$1" "$#"
            HOST_TYPE="$2"
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
            OUTPUT_DIR_EXPLICIT=1
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

    # Read here rather than in configure(): an unreadable token file is a typo
    # in the command line, and finding it out after the operator has answered
    # four questions is a worse experience than finding it out immediately.
    if [ -n "$TOKEN_FILE" ]; then
        if [ ! -f "$TOKEN_FILE" ]; then
            die "--token-file $TOKEN_FILE does not exist"
        fi
        TOKEN=$(head -n 1 "$TOKEN_FILE" | tr -d '\r')
        [ -n "$TOKEN" ] || die "--token-file $TOKEN_FILE is empty"
    fi

    if [ -n "$HOST_TYPE" ]; then
        _valid_host_type "$HOST_TYPE" ||
            die "--host-type must be one of bare_metal, vps, vm (got '$HOST_TYPE')"
    fi

    # Running from inside a directory already named netra-agent means THIS is
    # the netra-agent directory; nesting another one inside it is nobody's
    # intent. Computed from $PWD directly and never through _p(): this is where
    # output goes, not a path being probed. An explicit --output-dir still means
    # exactly what it says, even when it names ./netra-agent from in here.
    if [ "$OUTPUT_DIR_EXPLICIT" = 0 ] && [ "${PWD##*/}" = netra-agent ]; then
        OUTPUT_DIR="."
    fi
}

# _valid_host_type VALUE - the three the hub understands (spec 12a env table).
# Returns 0/1 and is called from `if`/`||` only; see the errexit note.
_valid_host_type() {
    case "$1" in
    bare_metal | vps | vm) return 0 ;;
    esac
    return 1
}

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

# _p PATH — prefix a PROBE path with AGENT_SETUP_ROOT. Never call this on a
# path that ends up in a template; see the header.
_p() {
    printf '%s%s' "$AGENT_SETUP_ROOT" "$1"
}

init_paths() {
    # $P_TTY is a probe path but is deliberately NOT prefixed: it is a device,
    # not part of the fixture tree. Tests point AGENT_TTY at an unopenable path
    # because there is no portable way to drop a controlling terminal (no setsid
    # on macOS, and redirecting stdin does not touch /dev/tty).
    P_TTY="${AGENT_TTY:-/dev/tty}"

    P_OSRELEASE="${AGENT_OSRELEASE_PATH:-$(_p /etc/os-release)}"
    P_MACHINEID="${AGENT_MACHINEID_PATH:-$(_p /etc/machine-id)}"
    P_CGROUP="${AGENT_CGROUP_ROOT:-$(_p /sys/fs/cgroup)}"
    P_MOUNTINFO="${AGENT_MOUNTINFO_PATH:-$(_p /proc/1/mountinfo)}"

    P_SYSFS="${AGENT_SYSFS_ROOT:-$(_p /sys)}"
    # /sys/block, NOT /sys/class/block. The two trees hold different things:
    # /sys/class/block is flat and lists every partition alongside its parent,
    # while /sys/block lists ONLY whole devices and keeps partitions as
    # subdirectories of the disk they belong to. SMART is asked about whole
    # devices (/dev/sda, never /dev/sda1), so /sys/block is the tree that
    # answers the question being asked, and no partition filter is needed.
    P_SYSBLOCK="$P_SYSFS/block"
    P_SYSNVME="$P_SYSFS/class/nvme"
    P_HWMON="$P_SYSFS/class/hwmon"

    P_DEV="${AGENT_DEV_ROOT:-$(_p /dev)}"
    P_DPKG="${AGENT_DPKG_PATH:-$(_p /var/lib/dpkg/status)}"
    P_APK="${AGENT_APK_PATH:-$(_p /lib/apk/db/installed)}"
    P_DBUS="${AGENT_DBUS_PATH:-$(_p /run/dbus/system_bus_socket)}"
    P_DOCKERSOCK="${AGENT_DOCKERSOCK_PATH:-$(_p /var/run/docker.sock)}"
    # utmp holds the logged-in session list. Absent on Alpine and other
    # busybox systems, which ship no utmp writer at all, so its presence is
    # detected rather than assumed.
    P_UTMP="${AGENT_UTMP_PATH:-$(_p /var/run/utmp)}"
    P_CPUINFO="${AGENT_CPUINFO_PATH:-$(_p /proc/cpuinfo)}"
    P_DMIVENDOR="${AGENT_DMIVENDOR_PATH:-$(_p /sys/class/dmi/id/sys_vendor)}"
    P_MACHINEID="${AGENT_MACHINEID_PATH:-$(_p /etc/machine-id)}"
    P_MACHINEID_DBUS="${AGENT_MACHINEID_DBUS_PATH:-$(_p /var/lib/dbus/machine-id)}"
    P_HYPERVISOR="${AGENT_HYPERVISOR_PATH:-$(_p /sys/hypervisor/type)}"
    P_HYPERVISOR_CAPS="${AGENT_HYPERVISOR_CAPS_PATH:-$(_p /sys/hypervisor/properties/capabilities)}"
    # A host WRITE path, not an emit path, and therefore prefixed: it never
    # reaches a template, and a test that persisted a module to the real
    # /etc/modules-load.d would be changing the machine running the suite.
    P_MODULESLOAD="${AGENT_MODULESLOAD_DIR:-$(_p /etc/modules-load.d)}"

    # The effective user id is a probe like any other. Not prefixed (it is not a
    # path), but seamed for the same reason $P_TTY is: the suite has to reach
    # both the root and the non-root branch of plan_drivetemp, and it runs as
    # neither reliably — a developer's laptop is not root and a CI runner is not
    # root either, so without this the root path would never be exercised.
    P_UID="${AGENT_UID:-$(id -u)}"
}

# debug_paths — dump every resolved probe path. `AGENT_DEBUG_PATHS=1` makes the
# prefixing rule inspectable from the outside. Every probe path added to the
# script belongs in this dump too.
debug_paths() {
    printf 'install_root|%s\n' "$AGENT_SETUP_ROOT"
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
    printf 'utmp|%s\n' "$P_UTMP"
    printf 'modulesload|%s\n' "$P_MODULESLOAD"
    printf 'cpuinfo|%s\n' "$P_CPUINFO"
    printf 'dmivendor|%s\n' "$P_DMIVENDOR"
    printf 'hypervisor|%s\n' "$P_HYPERVISOR"
    printf 'hypervisor_caps|%s\n' "$P_HYPERVISOR_CAPS"
    printf 'machineid|%s\n' "$P_MACHINEID"
    printf 'machineid_dbus|%s\n' "$P_MACHINEID_DBUS"
    printf 'uid|%s\n' "$P_UID"
}

# ---------------------------------------------------------------------------
# OS identification
# ---------------------------------------------------------------------------

# read_os_release — prints `id|version_id|id_like|pretty_name`.
#
# Parsed with awk and NEVER sourced. /etc/os-release is shell-syntax by
# convention, but it is untrusted input that this script exists to validate;
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

    # The emitted bind hardcodes /sys/fs/cgroup as its source, because a bind
    # source is an EMIT path and $P_CGROUP carries $AGENT_SETUP_ROOT under test.
    # So an operator who exported AGENT_CGROUP_ROOT because their hierarchy
    # genuinely lives elsewhere would have this script validate one path and
    # mount another. Say so rather than let the mismatch pass unremarked; the
    # test fixture root is not the case being warned about.
    if [ -n "${AGENT_CGROUP_ROOT:-}" ] &&
        [ "$AGENT_CGROUP_ROOT" != "/sys/fs/cgroup" ] &&
        [ -z "${AGENT_SETUP_ROOT:-}" ]; then
        warn "AGENT_CGROUP_ROOT is set to $AGENT_CGROUP_ROOT, which is the path this script" \
            "PROBED. The compose.yaml it writes still mounts /sys/fs/cgroup, so edit the" \
            "bind source by hand if that is not where this host's hierarchy lives."
    fi
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
# check_root — who this run is, and what root would add.
#
# NOT a hard failure, and not a list of consequences either. Everything root
# actually affects is already handled where it happens: detect_filesystems
# probes each mount point for writability and skips the ones it cannot use, with
# a note naming each; plan_drivetemp says the module cannot be loaded; and
# check_docker has ALREADY proved this user can reach the daemon, so the part
# the operator will actually run needs no root at all.
#
# What was missing was the plain fact, stated once, before any question: you are
# not root, and here is what that costs. Refusing outright would turn a degraded
# run into no run at all, and repeating the individual notes here would bury
# them.
check_root() {
    if [ "$P_UID" = 0 ]; then
        info "  user:            root"
        return 0
    fi

    info "  user:            uid $P_UID (not root)"
    warn "not running as root. Filesystems this user cannot write are skipped (each one is" \
        "reported as it is found), and the drivetemp module cannot be loaded. Docker is" \
        "fine — the daemon answered above. Re-run with sudo to get the rest."
}

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

# detect_virt — prints the hypervisor's name, or nothing on bare metal.
#
# It exists because three separate pieces of this script were giving a VPS
# advice that could not possibly apply to it: offering SMART on a disk the
# hypervisor invented, offering a drivetemp module for drives that are not
# there, and telling the operator to modprobe coretemp on a machine with no
# thermal hardware at all. All three are the same question asked once.
#
# Three signals, cheapest first. The `hypervisor` CPU flag is the reliable one on
# x86 and is always readable; DMI names the hypervisor PRODUCT where the flag is
# absent (see the warning on that branch); /sys/hypervisor/type catches Xen PV,
# which sets neither.
#
# Deliberately conservative: a guest this misses merely gets offered SMART that
# reads nothing, while a physical host it misjudges loses SMART, drive
# temperatures and the sensor hint on hardware that has them.
detect_virt() {
    if [ -r "$P_CPUINFO" ] && grep -q '^flags.*[[:space:]]hypervisor' "$P_CPUINFO" 2>/dev/null; then
        _dv_vendor=$(cat "$P_DMIVENDOR" 2>/dev/null || printf '')
        printf '%s' "${_dv_vendor:-hypervisor}"
        return 0
    fi

    if [ -r "$P_DMIVENDOR" ]; then
        _dv_vendor=$(cat "$P_DMIVENDOR" 2>/dev/null || printf '')
        # HYPERVISOR PRODUCTS ONLY — never a cloud's brand name.
        #
        # This branch is reached exactly when the CPU flag is ABSENT, which is
        # to say on genuine bare metal, so a false match here is always wrong.
        # An earlier version listed "Google", "Microsoft Corporation",
        # "Hetzner" and friends; every one of those companies also ships
        # physical machines, and sys_vendor on a Chromebox is "Google". The
        # cost of that mistake is not symmetric: a missed guest merely offers
        # SMART that reads nothing, while a misjudged physical host loses SMART,
        # drive temperatures and the sensor hint on hardware that has them.
        case "$_dv_vendor" in
        QEMU* | *VMware* | Xen* | *VirtualBox* | innotek* | Bochs* | Parallels*)
            printf '%s' "$_dv_vendor"
            return 0
            ;;
        esac
    fi

    if [ -r "$P_HYPERVISOR" ]; then
        _dv_type=$(cat "$P_HYPERVISOR" 2>/dev/null || printf '')
        # A Xen dom0 is REAL HARDWARE with real disks, and it reads `xen` here
        # exactly like a guest does: the CPU flag is absent on dom0 and
        # sys_vendor is the real board vendor, so both branches above fall
        # through. Calling it virtual costs it SMART, drive temperatures and the
        # sensor hint on hardware that has all of them — the asymmetric cost the
        # DMI branch above already refuses to pay. systemd-detect-virt
        # discriminates on the `control_d` capability; so do we.
        if [ -n "$_dv_type" ] && [ -r "$P_HYPERVISOR_CAPS" ] &&
            grep -q 'control_d' "$P_HYPERVISOR_CAPS" 2>/dev/null; then
            return 0
        fi
        [ -z "$_dv_type" ] || {
            printf '%s' "$_dv_type"
            return 0
        }
    fi

    return 0
}

# check_tools — every external command this script runs, checked once, up front.
#
# The script is `curl … | sh` onto a host nobody has inspected: a minimal image,
# a distroless-ish base, a busybox userland with pieces removed. Discovering a
# missing `tr` halfway through detection produces "tr: not found" from inside a
# command substitution and a run that limps on with an empty variable, which is
# a far worse failure than saying so in the first second.
#
# REQUIRED is what the script cannot work without. An HTTP client is NOT in it:
# it is only needed when templates are fetched, and --template-dir skips that
# entirely, so it is checked separately — and curl or wget will do, since plenty
# of minimal images ship exactly one of the two. stty and modprobe are optional
# by design and each already has its own fallback path.
check_tools() {
    _ct_missing=""
    # Derived from the source, not from memory: `sort` was missing from a first
    # version of this list and version_ge reached for it three lines into
    # preflight, which is precisely the failure this check exists to prevent.
    # readlink is deliberately absent — the header rules it out in favour of
    # `cd … && pwd -P`, and listing it would require a command nothing runs.
    for _ct_cmd in awk sed grep tr head cat sort wc mktemp mkdir rm cp id; do
        command -v "$_ct_cmd" >/dev/null 2>&1 ||
            _ct_missing="${_ct_missing:+$_ct_missing }$_ct_cmd"
    done
    if [ -n "$_ct_missing" ]; then
        die "this host is missing commands the setup script needs: $_ct_missing." \
            "Install them (on a minimal image, coreutils and the awk/sed/grep trio)" \
            "and re-run."
    fi

    # Optional, and each one degrades rather than fails — said once, here, so
    # the reason a later phase is quieter is not a mystery.
    command -v stty >/dev/null 2>&1 ||
        info "  note:            no stty, so the token prompt cannot hide what you type"
}

# check_http_client — separate from check_tools because it depends on a FLAG,
# and check_tools runs before parse_args has been consulted for anything.
check_http_client() {
    if [ -z "$TEMPLATE_DIR" ] && [ -z "$(_http_client)" ]; then
        die "neither curl nor wget is available, and templates are fetched over HTTPS." \
            "Install either one, or pass --template-dir with a local copy of deploy/agent."
    fi
}

# _http_client — the name of whichever HTTP client this host has, or nothing.
#
# curl first, because the `curl … | sh` line in the header means a host that ran
# this script that way demonstrably has it. Busybox wget is the common second:
# no --https-only, no -S, so _http_get uses only flags both spellings share.
_http_client() {
    if command -v curl >/dev/null 2>&1; then
        printf 'curl'
    elif command -v wget >/dev/null 2>&1; then
        printf 'wget'
    fi
}

# _http_get URL — the body on stdout, non-zero on any failure.
#
# One place, so the curl/wget split is not repeated at each call site and cannot
# drift between them. Quiet on both: a progress bar interleaved with the plan is
# noise, and the callers turn a failure into their own message.
_http_get() {
    case "$(_http_client)" in
    curl) curl -fsSL "$1" 2>/dev/null ;;
    wget) wget -qO- "$1" 2>/dev/null ;;
    *) return 1 ;;
    esac
}

# detect_pkgmgr ID ID_LIKE — prints dpkg, apk, rpm or none.
#
# File existence FIRST: what is actually on disk beats what os-release claims,
# and a container-derived or heavily customised host lies about the latter
# routinely. ID/ID_LIKE are consulted only to recognise an rpm host, where there
# is no file to probe that the setup script can do anything with anyway — the rpm
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
# phase.
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
        # here would be a mutation in the middle of the detection phase. W_OK
        # also respects MNT_READONLY, so a read-only mount is caught as well as
        # a permissions problem.
        _df_probe=$(_p "$_df_mp")
        if [ ! -d "$_df_probe" ] || [ ! -w "$_df_probe" ]; then
            _fs_note unwritable "$_df_mp" \
                "not writable by this user, so the .netra marker file cannot be created. \
The setup script never invokes sudo; create it yourself and re-run."
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
            "rendered compose.yaml, or run 'chcon -t container_file_t' on the marker files."
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
# CRITICAL: strip AGENT_SETUP_ROOT from the resolved path BEFORE matching.
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
    if [ -n "$AGENT_SETUP_ROOT" ]; then
        _dt_real=${_dt_real#"$AGENT_SETUP_ROOT"}
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
    SMART_ATA_DEVICES=""
    CAP_RAWIO=0
    CAP_SYS_ADMIN=0
    _ps_ata=""
    _ps_nvme=""

    # An EMULATED SATA disk has no SMART data behind it. The hypervisor presents
    # an /dev/sda that looks exactly like a real one from sysfs, so the transport
    # probe below cannot tell the difference and would happily grant SYS_RAWIO,
    # map a device, and ship an agent whose SMART collector reads nothing on
    # every single scrape. Granting a capability for a metric that cannot exist
    # is worse than collecting nothing.
    #
    # NVMe is NOT in that boat and is deliberately still probed below. On
    # passthrough and virtio-NVMe setups — AWS Nitro instance store and EBS
    # among them — `nvme smart-log` returns real temperature, data units
    # read/written and percentage_used. Skipping the whole function on any guest
    # threw that away on hosts that had it, and SYS_ADMIN is behind its own
    # prompt regardless, so nothing is granted here that was not asked for.
    if [ -n "${VIRT:-}" ]; then
        info "  SATA/SAS:        skipped on a virtual host ($VIRT)"
        # warn, not info: SKIPPED_NOTES is "everything the agent will NOT
        # collect", and this withholds every ATA SMART metric and every SATA
        # drive temperature. An operator whose host was misjudged has to be able
        # to find out from the finish report, without re-reading the scroll.
        warn "ATA SMART is skipped on this host because it looks virtual ($VIRT), and a" \
            "hypervisor's emulated disks carry no SMART data: no health, no wear, no" \
            "drive temperatures. NVMe, if present, is still offered. Re-run with" \
            "--assume-physical if this machine really does have disks of its own."
    fi

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

    # On a guest the ATA candidates are dropped here rather than at collection
    # time, so SYS_RAWIO is never granted and drivetemp is never offered for
    # drives the hypervisor invented. Cleared AFTER the loop so the loop stays
    # one shape.
    if [ -n "${VIRT:-}" ]; then
        _ps_ata=""
    fi

    # SMART_ATA_DEVICES outlives this function: detect_sensors reads it to decide
    # whether the drivetemp module is worth offering. A probe result, never an
    # emit value.
    SMART_ATA_DEVICES="$_ps_ata"

    if [ -n "$_ps_ata" ]; then
        # Not prompted. SYS_RAWIO lets smartctl issue ATA passthrough ioctls and
        # nothing else; without it every SATA drive reports SMART as
        # unavailable, which is not an agent anyone asked for.
        CAP_RAWIO=1
        info "  SATA/SAS:        $_ps_ata (SYS_RAWIO)"
        for _ps_d in $_ps_ata; do _smart_dev "$_ps_d"; done
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
        elif netra_ask "Grant SYS_ADMIN for NVMe SMART health and wear? NVMe temperature
  works without it." n; then
            _ps_grant=1
        fi
        if [ "$_ps_grant" = 1 ]; then
            CAP_SYS_ADMIN=1
            for _ps_c in $_ps_nvme; do _smart_dev "$_ps_c"; done
        else
            warn "SYS_ADMIN declined: no NVMe SMART health status or wear indicators." \
                "NVMe temperature still works, from hwmon. --sys-admin grants it."
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
# preference order. Writing AGENT_PRIMARY_SENSOR here would freeze an
# install-time guess into .env, where it outlives the CPU swap or kernel upgrade
# that changed the chip — so it is written ONLY when --primary-sensor was passed
# explicitly, or when two equally-ranked known CPU chips exist and the operator
# resolves the tie.
# _sensor_module_hint — no CPU temperature chip is usually a missing driver
# rather than missing hardware, EXCEPT on a virtual host, where it is neither: a
# guest has no thermal hardware to expose and no driver will conjure any. Telling
# a VPS operator to modprobe coretemp is advice that can only waste their time,
# so the two cases say different things.
#
# A note either way, never a prompt: nothing else in the agent depends on it.
_sensor_module_hint() {
    if [ -n "${VIRT:-}" ]; then
        # A continuation line, not a second "sensors:" label: all three call
        # sites have already printed one. And it says NO CPU SENSOR rather than
        # "no sensors", because the third site is reached with a populated chip
        # list that simply holds nothing the agent recognises as a CPU — telling
        # that operator there is no thermal hardware contradicts the list they
        # are looking at.
        info "                   no CPU temperature sensor, which is normal on a virtual"
        info "                   host ($VIRT): a guest is not given the CPU's thermal data"
        return 0
    fi
    warn_cmd "modprobe coretemp && echo coretemp > /etc/modules-load.d/coretemp.conf" \
        "no CPU temperature sensor was found. On physical hardware that is usually a" \
        "driver that is not loaded rather than a chip that is not there — coretemp" \
        "(Intel), k10temp (AMD) or nct6775 (many motherboards). Optional; everything" \
        "else works without it. To load one and keep it across reboots:"
}

# plan_drivetemp — the one host mutation this script offers, and the only thing
# that changes the machine before the write gate.
#
# WHY IT IS WORTH ASKING. Per spec §6.2 only SATA drive temperature needs the
# SMART path, and SMART polls at 1h (§6.1). With drivetemp loaded the same
# temperatures arrive through hwmon on the 60s sensor scrape, with no SYS_RAWIO
# and no devices: mapping — the existing hwmon enumeration picks the chips up
# with no collector change at all.
#
# WHY IT IS VERIFIED RATHER THAN ASSUMED. `modprobe drivetemp` exits 0 on any
# kernel that ships the module, but produces no hwmon chip whatsoever when the
# controller or the drives do not report SCT temperature. Writing
# /etc/modules-load.d/drivetemp.conf on the strength of that exit status is
# cargo-culting: it persists a module that does nothing, forever. So it is
# loaded, the hwmon tree is re-read, and only a chip that actually appeared
# earns the persist. When nothing appears the module is unloaded again and the
# host is left as it was found.
plan_drivetemp() {
    [ -n "${SMART_ATA_DEVICES:-}" ] || return 0

    if hwmon_chips | grep -q '|drivetemp|'; then
        info "  drivetemp:       already loaded"
        return 0
    fi

    if ! command -v modprobe >/dev/null 2>&1; then
        warn_cmd "modprobe drivetemp && echo drivetemp > /etc/modules-load.d/drivetemp.conf" \
            "no modprobe on PATH, so the drivetemp module cannot be offered. With it, SATA" \
            "drive temperatures come from hwmon every 60s instead of hourly from smartctl:"
        return 0
    fi

    if [ "$P_UID" != 0 ]; then
        # check_root has already said this run is not root and what that costs;
        # this adds only the part specific to drivetemp.
        warn_cmd "modprobe drivetemp && echo drivetemp > /etc/modules-load.d/drivetemp.conf" \
            "the drivetemp module needs root to load, and this run is not root. With it," \
            "SATA drive temperatures come from hwmon every 60s instead of hourly from" \
            "smartctl. As root:"
        return 0
    fi

    info "  drivetemp:       not loaded; it gives SATA drive temperatures every 60s"
    info "                   instead of hourly, with no extra privileges"

    # `if netra_ask` — see the header.
    if ! netra_ask "Load the drivetemp kernel module and check whether it works?" y; then
        warn_cmd "modprobe drivetemp && echo drivetemp > /etc/modules-load.d/drivetemp.conf" \
            "drivetemp not loaded: SATA drive temperatures stay on the hourly SMART path." \
            "To do it later:"
        return 0
    fi

    if ! netra_exec modprobe drivetemp; then
        warn "modprobe drivetemp failed: this kernel has no drivetemp module. Nothing was" \
            "persisted, and SATA drive temperatures stay on the hourly SMART path."
        return 0
    fi
    info "  modprobe drivetemp        ok"
    # Set HERE, the moment the host actually changed, not after the persist
    # below. The kernel module is loaded from this line onward, and every exit
    # path between here and the write gate has to be able to say so.
    DRIVETEMP_CHANGED=1

    _pd_chips=$(hwmon_chips | grep -c '|drivetemp|' || true)
    if [ "$_pd_chips" = 0 ]; then
        # Loaded and useless. Unload rather than leave a module behind that this
        # script talked the operator into for nothing; best-effort, because a
        # module held by something else is not this script's problem to solve.
        #
        # And the flag is cleared ONLY when that unload actually succeeds. The
        # `|| true` exists precisely because it can fail, and on that path the
        # module is still loaded — reporting "nothing was changed" afterwards
        # would be the exact lie this flag was added to prevent.
        if netra_exec modprobe -r drivetemp; then
            DRIVETEMP_CHANGED=""
            warn "drivetemp loaded but produced no hwmon chip: this controller or these drives" \
                "do not report SCT temperature. The module was unloaded again and nothing was" \
                "persisted; SATA drive temperatures stay on the hourly SMART path."
        else
            warn "drivetemp loaded but produced no hwmon chip, and unloading it again failed" \
                "(something else is holding it). It is STILL LOADED. Nothing was persisted, so" \
                "it will not come back after a reboot, and SATA drive temperatures stay on the" \
                "hourly SMART path."
        fi
        return 0
    fi
    info "  hwmon rescan              drivetemp: $_pd_chips chip(s)"

    netra_exec mkdir -p "$P_MODULESLOAD"
    netra_exec netra_write_line drivetemp "$P_MODULESLOAD/drivetemp.conf"
    info "  persisted                 /etc/modules-load.d/drivetemp.conf"
    # DRIVETEMP_CHANGED is already set, from the successful modprobe above.
    # This one records the SECOND change, so the undo instructions can name the
    # file only when there is a file to remove.
    DRIVETEMP_PERSISTED=1
}

detect_sensors() {
    step "Sensors"
    SENSOR_ROWS=""
    plan_drivetemp
    if [ ! -d "$P_HWMON" ]; then
        # A note, not an error: hwmon is absent inside some VMs and the agent
        # simply reports no temperatures.
        info "  sensors:         $P_HWMON does not exist (no temperatures on this host)"
        _sensor_module_hint
        return 0
    fi

    SENSOR_ROWS=$(hwmon_chips)
    if [ -z "$SENSOR_ROWS" ]; then
        info "  sensors:         none detected"
        _sensor_module_hint
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
        _sensor_module_hint
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
    # Not prompted, and this is a change from the first version of this script.
    # A tie can be any number of chips, so asking about it made the prompt
    # sequence variable-length - and the answer is a guess frozen into .env that
    # outlives the hardware that justified it. The agent picks at runtime unless
    # --primary-sensor says otherwise.
    warn "primary sensor left unpinned with $_ds_count equally-ranked '$_ds_primary' chips;" \
        "the agent will pick one at runtime and it may change across reboots." \
        "Pass --primary-sensor to pin one."
}

# ---------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------

# resolve_ref — pin $REF to the latest release tag when --ref was not given.
#
# NEVER master (§12a): a mid-refactor template must not be able to land on a
# production host. A failed lookup falls back to the setup script's own version tag,
# which is still a tag.
resolve_ref() {
    [ "$REF_EXPLICIT" = 0 ] || return 0
    [ -z "$TEMPLATE_DIR" ] || return 0
    _rr_url="https://api.github.com/repos/trick77/netra/releases/latest"
    if ! _rr_body=$(_http_get "$_rr_url"); then
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
# This is the setup script's most likely field failure: a corporate proxy, an
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
    if ! _http_get "$_ft_url" >"$_ft_dest"; then
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
# the exact byte sequence this script takes care to decode correctly one step
# earlier.
#
# An empty block deletes its marker line entirely. That is why `cap_add:` and
# `volumes:` are inside their blocks rather than in the template: an empty
# mapping key cannot be left dangling if the key never existed.
render_template() {
    awk '
        /^[[:space:]]*#__AGENT_[A-Z_]+__[[:space:]]*$/ {
            key = $0
            sub(/^[[:space:]]*#__AGENT_/, "", key)
            sub(/__[[:space:]]*$/, "", key)
            blk = ENVIRON["AGENT_BLK_" key]
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
                out = out substr(line, 1, RSTART - 1) ENVIRON["AGENT_VAL_" key]
                line = substr(line, RSTART + RLENGTH)
            }
            print out line
        }
    ' "$1"
}

# _env_value NAME VALUE — validate and export AGENT_VAL_<NAME>.
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
    eval "AGENT_VAL_$1=\$2"
    eval "export AGENT_VAL_$1"
}

# _pct_encode STRING — percent-encode the three characters AGENT_FS_MOUNTS
# cannot carry literally.
#
# The value is a `,`-joined list of `label=mountpoint`, and a mount point may
# legitimately contain either separator (/mnt/a,b is a valid path). `%` is
# encoded first, or the escapes written for the other two would be
# indistinguishable from a literal `%2C` that was in the path all along.
# internal/agent/config decodes it.
_pct_encode() {
    printf '%s' "$1" | sed -e 's/%/%25/g' -e 's/,/%2C/g' -e 's/=/%3D/g'
}

# build_fs_mounts_value — the AGENT_FS_MOUNTS value: `label=mountpoint,...`.
#
# Walks the SAME FS_MOUNTS list that build_volume_block turns into bind mounts,
# so the mapping and the mounts are rendered together and cannot drift apart.
#
# Without it the agent knows only the target it stat'd — /netra/fs/ark — and
# that path names nothing on the host. Reporting it is what produced
# "/netra/fs/ark is 94 % full" on a host with no netra anywhere.
build_fs_mounts_value() {
    AGENT_BLK_FS_MOUNTS=""
    while IFS='|' read -r _bf_mm _bf_mp _bf_lab; do
        [ -n "$_bf_mm" ] || continue
        AGENT_BLK_FS_MOUNTS="${AGENT_BLK_FS_MOUNTS:+$AGENT_BLK_FS_MOUNTS,}$_bf_lab=$(_pct_encode "$_bf_mp")"
    done <<EOF
${FS_MOUNTS:-}
EOF
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
    AGENT_BLK_VOLUMES=""
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

    # Unconditional, unlike everything around it: check_cgroup_v2 has already
    # hard-failed the run on a host without unified cgroup v2, so reaching here
    # means this host has one. It is also the mount the container collectors
    # cannot work without -- the container LIST is the walk of this tree, not
    # the Docker socket below.
    #
    # The literal, NOT $P_CGROUP: that is a PROBE path and carries
    # $AGENT_SETUP_ROOT, and a source here is an emit path. Same rule every
    # other bind in this function follows.
    #
    # The TARGET is not /sys/fs/cgroup. Docker's default cgroup namespace is
    # private, so the agent's own /sys/fs/cgroup is rooted at its own cgroup and
    # holds no other container's scope; giving the host's tree its own path
    # also means a missing mount fails loudly instead of walking an empty one.
    _bv_add "/sys/fs/cgroup" "/host/sys/fs/cgroup"

    if [ "${DOCKERSOCK_ENABLED:-0}" = 1 ]; then
        _bv_add "/var/run/docker.sock" "/var/run/docker.sock"
    fi
    if [ "${MOUNTINFO_ENABLED:-0}" = 1 ]; then
        _bv_add "/proc/1/mountinfo" "/host/mountinfo"
    fi
    if [ "${MACHINEID_ENABLED:-0}" = 1 ] && [ -n "${MACHINEID_SOURCE:-}" ]; then
        _bv_add "$MACHINEID_SOURCE" "/etc/machine-id"
    fi
    if [ "${OSRELEASE_ENABLED:-0}" = 1 ]; then
        _bv_add "/etc/os-release" "/host/etc/os-release"
    fi
    if [ "${DBUS_ENABLED:-0}" = 1 ]; then
        _bv_add "/run/dbus/system_bus_socket" "/run/dbus/system_bus_socket"
    fi
    if [ "${PKG_ENABLED:-0}" = 1 ] && [ -n "${PKG_MOUNT:-}" ]; then
        _bv_add "$PKG_MOUNT" "$PKG_MOUNT"
    fi
    if [ "${UTMP_ENABLED:-0}" = 1 ]; then
        _bv_add "/var/run/utmp" "/var/run/utmp"
    fi

    if [ -n "$_bv_body" ]; then
        AGENT_BLK_VOLUMES="    volumes:
$_bv_body"
    fi
    # shellcheck disable=SC2090
    export AGENT_BLK_VOLUMES
}

build_device_block() {
    AGENT_BLK_DEVICES=""
    if [ -n "${SMART_DEVICES:-}" ]; then
        AGENT_BLK_DEVICES="    devices:
$(printf '%s\n' "$SMART_DEVICES" | sed 's/^/      - /')
"
    fi
    export AGENT_BLK_DEVICES
}

build_cap_block() {
    AGENT_BLK_CAP_ADD=""
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
        AGENT_BLK_CAP_ADD="    cap_add:
$_bc_body"
    fi
    export AGENT_BLK_CAP_ADD
}

# No build_pid_block: `pid: host` is a literal line in compose.yaml.tmpl rather
# than a rendered block, because it is unconditional.

build_blocks() {
    build_volume_block
    # Beside the volume block on purpose: both read FS_MOUNTS, and the bind
    # mounts are meaningless to the hub without the mapping that names them.
    build_fs_mounts_value
    build_device_block
    build_cap_block
}

# netra_write_compose / netra_write_env — the redirection lives in a function so
# the whole write can be routed through netra_exec, which cannot itself carry a
# `>`.
netra_write_compose() {
    render_template "$1" >"$2"
}

netra_write_env() {
    render_env "$1" >"$2"
}

# netra_write_line TEXT PATH — a redirection cannot be an argument to
# netra_exec, so every file this script creates outside render_* goes through a
# named function the way netra_write_compose does.
netra_write_line() {
    printf '%s\n' "$1" >"$2"
}

# netra_create_marker PATH — an empty marker file, created but never truncated.
#
# `>>` rather than `>`: the caller has already established that nothing is
# there, and a redirection that can only ever create is the one to reach for
# when the target sits in the root of somebody's data filesystem. A stray `>`
# on a path that turned out to be a real file would empty it.
#
# No `touch`: this is the only place the script would need it, and check_tools
# does not require it. A redirection is a shell builtin and cannot be missing.
#
# `true`, NOT `:`, and this is load-bearing rather than taste. `:` is a POSIX
# SPECIAL builtin, and a redirection error on a special builtin makes a
# non-interactive shell EXIT — dash does exactly that, bash does not. With `:`
# here, a marker that cannot be created (ENOSPC, inode exhaustion, a quota, an
# immutable or relabelled parent) kills the whole run mid-write under dash,
# after the output directory has been created and before the summary prints,
# instead of reaching the warn-and-continue the caller deliberately wrote.
# `true` is a regular builtin, so the failure comes back as a status.
netra_create_marker() {
    true >>"$1"
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
        elif netra_ask "Continue on an unsupported OS?" n; then
            info "  os:              ${OS_PRETTY:-$OS_ID $OS_VER} (unsupported, continuing at operator request)"
            _pf_os_ok=1
        fi
        if [ "$_pf_os_ok" != 1 ]; then
            # The floors are advisory (§12a), so the refusal must name its own
            # remedy, or a run on a cgroup-v2 host netra simply does not
            # recognise by name has no way back in.
            die "aborted: unsupported operating system." \
                "Re-run with --unsupported-os to proceed without prompting."
        fi
        ;;
    esac

    check_cgroup_v2
    check_machine_id

    VIRT=$(detect_virt)
    if [ -n "$VIRT" ] && [ "${ASSUME_PHYSICAL:-0}" = 1 ]; then
        info "  platform:        $VIRT, overridden by --assume-physical"
        VIRT=""
    elif [ "${ASSUME_PHYSICAL:-0}" = 1 ]; then
        # An unhonoured request is not a neutral state — the same rule
        # --sys-admin follows on a virtual host. Without this, an operator who
        # suspects a misdetection passes the flag, detection returns physical
        # for some unrelated reason, and they cannot tell whether the flag did
        # anything at all.
        info "  platform:        physical (--assume-physical had nothing to override)"
    elif [ -n "$VIRT" ]; then
        info "  platform:        virtual ($VIRT)"
    else
        info "  platform:        physical"
    fi

    DOCKER_COMPOSE=$(check_docker)
    info "  docker:          daemon reachable, $DOCKER_COMPOSE"

    # After check_docker on purpose: it has just proved this user can reach the
    # daemon, so the note does not have to speculate about that half.
    check_root
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

    # Not prompted. A read-only mount of the package database is what the
    # package collector IS, and the same argument plan_extras makes for the
    # Docker socket applies unchanged: an agent configured without it is not the
    # thing the operator ran this script for.
    PKG_ENABLED=1
    info "  package db:      $PKG_MOUNT (read-only, $PKGMGR)"
}

# plan_extras — the mounts and namespaces that are not implied by hardware.
#
# Asks nothing. Everything left here is read-only and enabled automatically; see
# the PROMPT ORDER block in the header for why, and for what an added prompt
# would cost every answers file in test/setup-agent/cases/.
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

    # The agent hashes /etc/machine-id into the metadata fingerprint, which is
    # how the hub detects a token copied to a second host. The agent image is
    # Alpine and has NO /etc/machine-id of its own, so without this mount
    # fingerprint() reads nothing, returns "", and every containerised agent
    # reports the same empty fingerprint — the anti-copy check can never fire.
    # Read-only, and not prompted, for the same reason as the Docker socket.
    #
    # EMIT path, so the host path is used verbatim. /var/lib/dbus/machine-id is
    # the fallback on hosts that predate systemd's location; both are mounted AT
    # /etc/machine-id, because that is where the agent looks.
    MACHINEID_ENABLED=0
    MACHINEID_SOURCE=""
    if [ -f "$P_MACHINEID" ]; then
        MACHINEID_ENABLED=1
        MACHINEID_SOURCE=/etc/machine-id
        info "  machine id:      /etc/machine-id (read-only, host fingerprint)"
    elif [ -f "$P_MACHINEID_DBUS" ]; then
        MACHINEID_ENABLED=1
        MACHINEID_SOURCE=/var/lib/dbus/machine-id
        info "  machine id:      /var/lib/dbus/machine-id (read-only, host fingerprint)"
    else
        warn "no /etc/machine-id or /var/lib/dbus/machine-id on this host, so the agent" \
            "cannot fingerprint it. The hub will not be able to tell if this agent's token" \
            "is later copied to a second machine."
    fi

    # The agent reads PRETTY_NAME from /etc/os-release for the OS the host page
    # shows, and the distro mark drawn beside it. Without the mount it falls
    # back to the Go runtime's "linux" and the page draws a generic penguin.
    #
    # EMIT path, so the host path is used verbatim. The TARGET is under /host:
    # the agent image is Alpine and has an /etc/os-release of its OWN, so
    # mounting over that would make a missing mount report "Alpine Linux" for
    # every host rather than fall back visibly.
    OSRELEASE_ENABLED=0
    if [ -f "$P_OSRELEASE" ]; then
        OSRELEASE_ENABLED=1
        info "  os release:      /etc/os-release (read-only, distro name)"
    else
        warn "no /etc/os-release on this host, so the agent cannot report which distro" \
            "it runs. The host page will show a generic Linux instead of this one."
    fi

    MOUNTINFO_ENABLED=0
    if [ -f "$P_MOUNTINFO" ]; then
        MOUNTINFO_ENABLED=1
        info "  mount table:     /proc/1/mountinfo (read-only, awareness only)"
    fi

    # Read-only, like the two above, and for the same reason not prompted.
    #
    # Two collectors ride this one bind now: systemd's unit list, and the
    # logged-in session count, which logind answers and utmp no longer can on
    # a current distribution.
    DBUS_ENABLED=0
    if [ -e "$P_DBUS" ]; then
        DBUS_ENABLED=1
        info "  d-bus:           /run/dbus/system_bus_socket (read-only)"
    else
        info "  d-bus:           $P_DBUS not present (no systemd collector, no session count)"
    fi

    # Detected, not prompted: the five-question design is deliberate, and a
    # read-only bind of a file that lists tty names is not worth a sixth. The
    # agent reads one 2-byte field per record and transmits only the count.
    #
    # THE FALLBACK, NOT THE SOURCE, since the agent learned to ask logind.
    # Absent on Alpine and other busybox systems, which have no utmp writer at
    # all, and absent again on systemd 257 and later -- Ubuntu 25.10 onwards --
    # which is built without utmp support because its record format overflows
    # in 2038. On those hosts the count comes from the d-bus bind above and
    # this file is never created, which is why its absence is reported as a
    # fallback that is missing rather than as no session count at all.
    UTMP_ENABLED=0
    if [ -f "$P_UTMP" ]; then
        UTMP_ENABLED=1
        info "  logged-in users: /var/run/utmp (read-only, session count only)"
    elif [ "$DBUS_ENABLED" = 1 ]; then
        info "  logged-in users: logind over d-bus (no utmp on this host)"
    else
        info "  logged-in users: neither logind nor $P_UTMP (no session count)"
    fi

    # NOT PROMPTED, AND NOT OPTIONAL. It used to be opt-in behind --pid-host, on
    # the reasoning that the only thing it bought was a per-process breakdown.
    # That premise expired twice over: the breakdown has since been removed, and
    # the namespace is also what
    # resolves each container's interfaces, so declining it silently zeroed
    # net_rx and net_tx for EVERY container on the box -- and nothing in the
    # script or the docs said so. An operator declining it for the stated privacy
    # reason had no way to learn what else they had just switched off.
    #
    # Stated rather than asked, because the exposure is real and the operator
    # should read it even though there is no question attached: sharing the host
    # PID namespace makes every process's /proc entry readable to this container,
    # cmdline and environ included. netra reads NEITHER. Since the per-process
    # collector was removed the only per-process file it opens is comm, for
    # /proc/1 and /proc/self, and only when AGENT_PID_HOST is unset; the count
    # itself comes from counting numeric directories in /proc.
    # internal/agent/collector/argv_guard_test.go fails the build if either name
    # appears in a Go string literal.
    info "  processes:       pid: host (always) -- per-container network and"
    info "                   the process count both need the host PID namespace"
    info "                   the namespace exposes every process's cmdline and"
    info "                   environ to the container; netra reads neither"
}

# resolve_token — the prompt, with echo off. --token and --token-file are
# resolved in parse_args, so reaching the body means neither was given.
#
# The setup script NEVER invents a token: the hub mints them and stores only a
# SHA-256, so a value made up here could never authenticate. An empty answer is
# ALLOWED and is not an error: AGENT_TOKEN is written empty with a loud note
# that the agent will refuse to start until it is filled in. An operator who has
# not minted a token yet must still be able to finish the run, and dying here
# would waste every answer already given.
resolve_token() {
    # --token and --token-file have both already been resolved by parse_args.
    [ -z "$TOKEN" ] || return 0

    # An existing .env this run may not overwrite means a token typed here has
    # nowhere to go. Prompting for a secret in order to discard it is the very
    # thing the reuse path exists to stop; configure has already said so.
    [ "${REUSE_ENV:-0}" != 1 ] || return 0

    if [ ! -r "$P_TTY" ]; then
        # A --force run over an .env that HAD a token keeps it rather than
        # blanking it -- same rule as the prompt below, and the same reason:
        # nothing here was told to replace it.
        if [ -n "${FORCE_PRIOR_TOKEN:-}" ]; then
            TOKEN="$FORCE_PRIOR_TOKEN"
            info "  token:           kept the one already in .env (no --token given)"
            return 0
        fi
        warn "no agent token was provided (--token / --token-file). AGENT_TOKEN will be" \
            "written empty and the agent will refuse to start until you fill it in." \
            "Tokens are minted by the hub; the setup script cannot invent one."
        return 0
    fi

    # Enter has to mean "keep" here too. Every other prompt under --force now
    # restores the value the .env already held, and a token prompt that alone
    # meant "erase" would be a trap built out of the other four: run --force to
    # correct a typo in the location, press Enter through the rest as they just
    # taught you, and the agent stops authenticating with the old token already
    # overwritten on disk. Rotating is still one step -- type the new token, or
    # pass --token.
    if [ -n "${FORCE_PRIOR_TOKEN:-}" ]; then
        printf 'Agent token from the hub (starts with nta_, input hidden; blank keeps the current one): '
    else
        printf 'Agent token from the hub (starts with nta_, input hidden): '
    fi
    # `command -v stty` guarded: a minimal container image may not ship it, and
    # a visible token is better than a failed run. The saved setting is restored
    # on every path out, including an interrupt — see the trap.
    if command -v stty >/dev/null 2>&1; then
        _rt_saved=$(stty -g <"$P_TTY" 2>/dev/null || printf '')
        # Ctrl-C at a hidden prompt would otherwise leave the operator staring
        # at a terminal that no longer echoes what they type. Cleared again
        # right after, so an interrupt later in the run is not caught by this
        # handler.
        trap '[ -z "$_rt_saved" ] || stty "$_rt_saved" <"$P_TTY" 2>/dev/null; printf "\n"; exit 130' INT
        [ -z "$_rt_saved" ] || stty -echo <"$P_TTY" 2>/dev/null || true
        IFS= read -r TOKEN <"$P_TTY" || TOKEN=""
        [ -z "$_rt_saved" ] || stty "$_rt_saved" <"$P_TTY" 2>/dev/null || true
        trap - INT
        printf '\n'
    else
        IFS= read -r TOKEN <"$P_TTY" || TOKEN=""
    fi

    if [ -z "$TOKEN" ] && [ -n "${FORCE_PRIOR_TOKEN:-}" ]; then
        TOKEN="$FORCE_PRIOR_TOKEN"
        info "  token:           kept the one already in .env (nothing entered)"
    elif [ -z "$TOKEN" ]; then
        warn "no token entered. AGENT_TOKEN will be written empty and the agent will refuse" \
            "to start until you fill it in."
    fi
}

# env_file_value FILE KEY — one key's value out of an .env, or the empty string.
#
# The FIRST '=' only: a hub URL may carry a query string and a token is
# arbitrary. Trailing whitespace is stripped, inner whitespace is not --
# "Zurich, CH" is a legitimate location. The LAST assignment wins, matching what
# a shell sourcing the file would see.
env_file_value() {
    [ -r "$1" ] || return 0
    _efv=$(sed -n "s/^[[:space:]]*${2}=//p" "$1" | sed -n '$p')
    printf '%s' "${_efv%"${_efv##*[![:space:]]}"}"
}

# force_seeded VAR — did --force take VAR's value from the existing .env?
#
# The prompts are gated on an EMPTY value, so a seeded variable would have
# nothing left to ask about -- which is right on the reuse path, where the
# answer cannot be written anyway, and wrong under --force, where the whole
# point is to be asked. This is what tells the two apart: a seeded value is a
# DEFAULT (netra_ask_value restores it when the answer is empty), not an
# answer.
#
# Written as a case rather than a glob test so a variable whose name is a
# prefix of another cannot match -- the spaces are the delimiters.
force_seeded() {
    case " ${FORCE_SEEDED:-} " in
        *" $1 "*) return 0 ;;
    esac
    return 1
}

# keeps_hint VAR — " (blank keeps X)" for a seeded prompt, otherwise nothing.
#
# The contract has to be ON the prompt. An operator cannot be expected to know
# that Enter means "keep" here when it meant "skip" the last time they ran this,
# and the value is printed because these are the non-secret dimensions -- the
# token's equivalent hint names no value, for the obvious reason.
keeps_hint() {
    force_seeded "$1" || return 0
    # Assigned literally first so shellcheck can see it, the same way
    # netra_ask_value declares _av_def: the eval below is invisible to static
    # analysis, and SC2154 is right to say so.
    _kh=""
    eval "_kh=\$$1"
    printf ' (blank keeps %s)' "$_kh"
}

# seed_from_env FILE [KEYS] — adopt the values an existing .env already holds.
#
# Only fills a variable that is still EMPTY, which is what keeps the precedence
# straight: parse_args has already applied every flag, so a flag wins, the file
# comes next, and a prompt is the last resort for a value nobody has supplied.
#
# KEYS narrows which ones are adopted, and defaults to all of them. It exists
# for --force, which seeds every key EXCEPT AGENT_TOKEN: there the values are
# defaults for prompts that still get asked, and seeding the token would skip
# its prompt entirely. See configure -- the token is held aside there instead,
# so that Enter means "keep" at every prompt including that one.
#
# The token is adopted like everything else on the reuse path and NEVER printed.
# That is the whole point: it is the one value an operator would otherwise
# retype from a password manager only to have it discarded.
seed_from_env() {
    _se_file="$1"
    # Unquoted on purpose below: this is a list to split on whitespace.
    _se_keys="${2:-AGENT_HUB_URL AGENT_TOKEN AGENT_LOCATION AGENT_PROVIDER AGENT_HOST_TYPE}"
    [ -r "$_se_file" ] || return 0
    _se_adopted=""

    # shellcheck disable=SC2086 # deliberate word splitting: $_se_keys is a list
    for _se_key in $_se_keys; do
        _se_val=$(env_file_value "$_se_file" "$_se_key")
        [ -n "$_se_val" ] || continue

        # Only what was actually TAKEN is reported below. A key the file holds
        # and a flag already answered is not "reused": on the reuse path that
        # was harmless, because the flag's value was discarded too, but under
        # --force the flag is what gets written -- so naming it as reused
        # would be a straight falsehood about the file being written.
        case "$_se_key" in
            AGENT_HUB_URL) [ -z "$HUB_URL" ] || continue; HUB_URL="$_se_val" ;;
            AGENT_TOKEN) [ -z "$TOKEN" ] || continue; TOKEN="$_se_val" ;;
            AGENT_LOCATION) [ -z "$LOCATION" ] || continue; LOCATION="$_se_val" ;;
            AGENT_PROVIDER) [ -z "$PROVIDER" ] || continue; PROVIDER="$_se_val" ;;
            AGENT_HOST_TYPE) [ -z "$HOST_TYPE" ] || continue; HOST_TYPE="$_se_val" ;;
        esac
        _se_adopted="$_se_adopted $_se_key"
    done

    # The KEYS, never the values: AGENT_TOKEN is in this list. Naming them is
    # what turns "nothing was asked" from something the operator has to take on
    # trust into something they can check.
    [ -z "$_se_adopted" ] ||
        info "  reused from .env:${_se_adopted}"
}

# configure — everything the operator states rather than the host reveals.
#
# Detection cannot guess any of these, and an .env without a hub URL or a token
# cannot start the agent, so they are asked rather than left blank with a
# comment telling the operator to come back later. Each prompt is skipped when
# its flag was given. AGENT_FACILITY is deliberately NOT asked: it is a
# datacenter code most self-hosters do not have, and a prompt nobody can answer
# is a prompt that teaches people to hit Enter through the rest of them.
configure() {
    step "Configuration"

    # BEFORE anything reads or writes that file, because every reader below
    # looks for AGENT_ names and a pre-rename .env holds NETRA_ ones. Left to
    # run, this script would report AGENT_HUB_URL and AGENT_TOKEN as EMPTY
    # while both sit in the file under their old names, append AGENT_PID_HOST
    # and AGENT_FS_MOUNTS to a file the agent now refuses to start with, and --
    # with --force, where the prior token is read from AGENT_TOKEN -- destroy a
    # token the hub only ever stored the SHA-256 of.
    #
    # Refuses rather than renaming the keys itself: the agent accepts exactly
    # one spelling, on purpose, and a file this script silently rewrote is not
    # the file the operator thinks is on the host.
    #
    # Anchored. The shipped .env.example carries the sed line below in a
    # comment, and an unanchored match would refuse the file that documents the
    # fix.
    if [ -f "$OUTPUT_DIR/.env" ] && grep -q '^NETRA_' "$OUTPUT_DIR/.env" 2>/dev/null; then
        die "$OUTPUT_DIR/.env still uses the old NETRA_ prefix. Every agent variable is" \
            "AGENT_-prefixed now, and the agent refuses to start while an old name is set." \
            "Rename them and re-run this script:" \
            "  sed -i.bak 's/^NETRA_/AGENT_/' $OUTPUT_DIR/.env"
    fi

    # BEFORE the questions, because the answer to most of them is already on
    # disk. write_outputs refuses to overwrite an existing .env without --force,
    # so a re-run used to ask for the hub URL, then the TOKEN at a hidden
    # prompt, then the location and the rest -- and drop every one of them. It
    # warned first rather than not asking, which is the same defect with better
    # manners: nobody types a secret at a prompt in order to be told it was
    # ignored. Re-running to pick up a new mount or a new template is the
    # ordinary case, and it should be one command and no questions.
    #
    # Seeding rather than skipping, because the prompts are already gated on an
    # empty value: a seeded variable simply has nothing to ask about. That also
    # gets the precedence right for free -- a flag beats the existing .env,
    # which beats a prompt.
    REUSE_ENV=0
    FORCE_SEEDED=""
    if [ -e "$OUTPUT_DIR/.env" ] && [ "$FORCE" != 1 ]; then
        REUSE_ENV=1
        seed_from_env "$OUTPUT_DIR/.env"
        warn "$OUTPUT_DIR/.env already exists and --force was not given, so its values are" \
            "REUSED and nothing below is asked. Re-run with --force to be asked again and" \
            "replace them."

        # A key the file leaves EMPTY cannot be filled on this run: the answer
        # has nowhere to be written. Asking anyway is the original defect in its
        # worst form -- a token typed at a hidden prompt and dropped -- so the
        # prompts are skipped and the gap is named instead. An .env written by a
        # run that had no token is exactly this case.
        _cfg_missing=""
        [ -n "$HUB_URL" ] || _cfg_missing="$_cfg_missing AGENT_HUB_URL"
        [ -n "$TOKEN" ] || _cfg_missing="$_cfg_missing AGENT_TOKEN"
        [ -z "$_cfg_missing" ] ||
            warn "and these are EMPTY in it:${_cfg_missing}. They are not asked for here," \
                "because this run cannot write them. Edit $OUTPUT_DIR/.env directly, or" \
                "re-run with --force to be asked."
    elif [ -e "$OUTPUT_DIR/.env" ]; then
        # --force, over an .env that already exists. Every prompt below is
        # still asked -- being asked is what --force is for -- but the answers
        # this file already holds become their DEFAULTS, because --force
        # rewrites the whole file and every key it cannot fill is written
        # EMPTY.
        #
        # Without this, the values the operator did not retype were silently
        # destroyed. Two ways, and the first is the ordinary one: pressing
        # Enter at "Where is this host" meant "erase it", when every prompt in
        # this script treats Enter as "leave it alone". The second is the
        # suite's shape and a values file that runs out -- an unreadable
        # $P_TTY makes netra_ask_value return empty without printing anything
        # at all, so `--force --token X --hub-url Y` rewrote AGENT_LOCATION,
        # AGENT_PROVIDER and AGENT_HOST_TYPE empty with nothing on screen to
        # say it had happened.
        #
        # AGENT_TOKEN is NOT seeded, because seeding would skip its prompt
        # entirely and a rotation is the main reason to pass --force. It is
        # held aside instead, so the PROMPT still happens and an empty answer
        # keeps the token rather than destroying it -- Enter has to mean the
        # same thing at all five prompts. Anything else trains an operator on
        # four prompts and then punishes them on the fifth: a --force run to
        # correct a typo in the location, Enter through the rest, and the agent
        # can no longer authenticate with the old token already gone from disk.
        #
        # Never printed, never info'd, and cleared nowhere else: resolve_token
        # is its only reader.
        FORCE_PRIOR_TOKEN=$(env_file_value "$OUTPUT_DIR/.env" AGENT_TOKEN)

        # AGENT_PRIMARY_SENSOR is deliberately absent. It is written only when
        # the operator states it, and detect_sensors has ALREADY run and
        # reported by the time this executes -- so seeding it re-pinned a chip
        # the same run had just described as auto-selected, and re-pinned it
        # even when that chip is no longer on the host. That is the
        # install-time guess outliving the hardware which detect_sensors' own
        # comment exists to prevent, and --force is the one run that should be
        # re-evaluating it. A --force run still clears the key; correcting that
        # needs the value validated against this run's sensors, which is its
        # own change.
        _cfg_empty=""
        for _cfg_var in HUB_URL LOCATION PROVIDER HOST_TYPE; do
            eval "_cfg_val=\$$_cfg_var"
            [ -n "$_cfg_val" ] || _cfg_empty="$_cfg_empty $_cfg_var"
        done

        seed_from_env "$OUTPUT_DIR/.env" \
            "AGENT_HUB_URL AGENT_LOCATION AGENT_PROVIDER AGENT_HOST_TYPE"

        # Seeded means "was empty before, is not now" -- which is exactly the
        # set seed_from_env filled, and excludes anything a flag supplied. A
        # flag is an answer, not a default, and must not be re-asked.
        # shellcheck disable=SC2086 # deliberate word splitting: a list
        for _cfg_var in $_cfg_empty; do
            eval "_cfg_val=\$$_cfg_var"
            [ -z "$_cfg_val" ] || FORCE_SEEDED="$FORCE_SEEDED $_cfg_var"
        done
    fi

    if { [ -z "$HUB_URL" ] || force_seeded HUB_URL; } && [ "$REUSE_ENV" != 1 ]; then
        netra_ask_value HUB_URL "Hub base URL$(keeps_hint HUB_URL)" "https://netra.example.com"
    fi
    if [ -z "$HUB_URL" ] && [ "$REUSE_ENV" != 1 ]; then
        # The same shape as the missing-token note below: equally fatal to the
        # agent, so it is equally loud and reaches the finish report.
        #
        # Not on the reuse path, where this run writes no .env at all: "will be
        # written empty" would be false, and the empty key has already been
        # named with the remedy that actually applies.
        warn "no hub URL was given. AGENT_HUB_URL will be written empty and the agent will" \
            "refuse to start until you fill it in."
    elif [ -n "$HUB_URL" ]; then
        info "  hub url:         $HUB_URL"
    fi

    resolve_token

    if { [ -z "$LOCATION" ] || force_seeded LOCATION; } && [ "$REUSE_ENV" != 1 ]; then
        netra_ask_value LOCATION "Where is this host (city, country)$(keeps_hint LOCATION)" "Zurich, CH"
    fi
    if { [ -z "$PROVIDER" ] || force_seeded PROVIDER; } && [ "$REUSE_ENV" != 1 ]; then
        netra_ask_value PROVIDER "Who hosts it$(keeps_hint PROVIDER)" "Hetzner, OVH, self-hosted"
    fi

    # The one validated value: the hub stores it as an enum, so a typo here is a
    # host that never groups with its siblings. Empty stays allowed - re-asking
    # forever would trap an operator who does not know what to call a container
    # host - but a non-empty answer must be one of the three.
    if { [ -z "$HOST_TYPE" ] || force_seeded HOST_TYPE; } && [ "$REUSE_ENV" != 1 ]; then
        # "blank to skip" is only true when there is nothing to keep. With a
        # seeded value blank RESTORES it, and a prompt that says otherwise is
        # the trap this whole change exists to remove.
        if force_seeded HOST_TYPE; then
            netra_ask_value HOST_TYPE "Host type (bare_metal, vps, vm; blank keeps $HOST_TYPE)"
        else
            netra_ask_value HOST_TYPE "Host type (bare_metal, vps, vm; blank to skip)" "vps"
        fi
    fi
    while [ -n "$HOST_TYPE" ] && ! _valid_host_type "$HOST_TYPE"; do
        warn "host type '$HOST_TYPE' is not one of bare_metal, vps, vm"
        # Cleared first, so an empty answer ENDS the loop instead of re-taking
        # the rejected value as its own default and spinning forever.
        HOST_TYPE=""
        netra_ask_value HOST_TYPE "Host type (bare_metal, vps, vm; blank to skip)" "vps"
    done

    info "  location:        ${LOCATION:-(not set)}"
    info "  provider:        ${PROVIDER:-(not set)}"
    info "  host type:       ${HOST_TYPE:-(not set)}"
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
        info "  filesystems to measure (empty .netra marker files, no data exposure):"
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
    info "  package mount:   ${PKG_MOUNT:-none}"
    info "  d-bus socket:    $(if [ "$DBUS_ENABLED" = 1 ]; then printf 'yes'; else printf 'no'; fi)"
    info "  hub url:         ${HUB_URL:-(not set)}"
    # The token is NEVER printed, here or anywhere else.
    info "  token:           $(if [ -n "$TOKEN" ]; then printf '****'; else printf '(not provided)'; fi)"
    info "  location:        ${LOCATION:-(not set)}"
    info "  provider:        ${PROVIDER:-(not set)}"
    info "  host type:       ${HOST_TYPE:-(not set)}"
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

# write_outputs — everything that touches the output directory and the marker
# files, behind ONE gate, and nothing happens before that gate.
#
# The ordering is the point. No mkdir of the output directory and no .netra
# marker may move ahead of the gate: declining would then leave a host littered
# with marker files, possibly a .env, and a finish report cheerfully printing
# `cd <dir> && docker compose up -d`. With --start it is worse — docker compose
# runs against whatever stale compose.yaml a previous run left, paired with this
# run's fresh .env.
#
# Template fetching and rendering stay ahead of the gate deliberately: mktemp is
# not a host mutation, and a proxy that blocks the template download is better
# discovered before the operator is asked to approve a plan that cannot be
# carried out.
#
# Sets WROTE_OUTPUTS rather than returning a status, and is called PLAINLY —
# never `if write_outputs; then`. That distinction is the whole point:
# `if write_outputs` suspends errexit for every command inside the function, so
# a failed `mkdir` and two failed redirections would each be shrugged off, the
# function would return 0, the summary would announce files that do not exist,
# and --start would run `docker compose -f <dir>/compose.yaml up -d` against a
# missing file — while the script exited 0 and a provisioning wrapper checking
# $? saw success. The asymmetry gave it away: the benign outcome (a declined
# gate) could be signalled, the dangerous one could not.
#
# So: errexit stays armed, the writes that matter carry `|| die`, and the
# declined-gate case is carried by a variable instead of an exit status.
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
    _env_value LOCATION "$LOCATION"
    _env_value PROVIDER "$PROVIDER"
    _env_value HOST_TYPE "$HOST_TYPE"
    _env_value FS_MOUNTS "${AGENT_BLK_FS_MOUNTS:-}"

    # The gate. One question covering everything below it, so "no" means the
    # host is exactly as it was.
    if ! netra_ask "Write compose.yaml and .env to $OUTPUT_DIR and create the marker files?" y; then
        rm -rf "$SCRATCH_DIR"
        WROTE_OUTPUTS=0
        info ""
        info "  Nothing was written."
        # The one exception to "nothing was changed", and it has to be named:
        # plan_drivetemp runs before this gate and may already have loaded the
        # module, persisted it, or both. Saying "nothing was changed" here
        # would be false, and would leave the operator with no idea what to
        # undo — so the remedy names exactly what happened, and nothing else.
        # `rm` on a file that was never written would fail and read as though
        # the undo itself were broken.
        if [ -n "$DRIVETEMP_PERSISTED" ]; then
            warn_cmd "modprobe -r drivetemp && rm /etc/modules-load.d/drivetemp.conf" \
                "the drivetemp module was loaded and persisted earlier in this run, before" \
                "this gate, and that change is still in place. To undo it:"
        elif [ -n "$DRIVETEMP_CHANGED" ]; then
            warn_cmd "modprobe -r drivetemp" \
                "the drivetemp module was loaded earlier in this run, before this gate, and" \
                "is still loaded. Nothing was persisted, so it will not survive a reboot" \
                "either way. To unload it now:"
        else
            info "  Nothing was changed."
        fi
        return 0
    fi
    WROTE_OUTPUTS=1

    # Asked BEFORE the mkdir, because afterwards the answer is always yes and
    # the ledger would report a directory this run created on every re-run.
    _wo_dir_existed=0
    [ ! -d "$OUTPUT_DIR" ] || _wo_dir_existed=1

    netra_exec mkdir -p "$OUTPUT_DIR" ||
        die "could not create $OUTPUT_DIR. Nothing was written."
    [ "$_wo_dir_existed" = 1 ] || record_change "created the directory $OUTPUT_DIR"

    # Marker directories. The mount point is a PROBE path here (so a test can
    # redirect it) and an EMIT path in compose.yaml; _p() on the way in, never on
    # the way out. The ledger entry names the EMIT path: a fixture prefix in the
    # report would tell the operator to look somewhere that does not exist.
    while IFS='|' read -r _wo_mm _wo_mp _wo_lab; do
        [ -n "$_wo_mm" ] || continue
        # `/` would otherwise give //.netra, which is a legal path and an ugly
        # line in the report.
        if [ "$_wo_mp" = "/" ]; then
            _wo_marker=/.netra
        else
            _wo_marker="$_wo_mp/.netra"
        fi
        # -e, not -f: hosts set up before the marker became a file have a
        # DIRECTORY at this path. It is an equally good bind source — statfs(2)
        # reports the filesystem a path lives on and does not care what kind of
        # object it is — so an existing one is left exactly as it is rather than
        # replaced. Nothing is gained by churning a working install, and a
        # rmdir/create here could only fail in interesting ways.
        _wo_marker_existed=0
        [ ! -e "$(_p "$_wo_marker")" ] || _wo_marker_existed=1
        # A marker that cannot be created is a DEGRADATION, not a failure: a
        # read-only filesystem loses its own measurement and nothing else.
        # Warned rather than fatal, unlike the two writes below.
        if [ "$_wo_marker_existed" = 1 ]; then
            :
        elif netra_exec netra_create_marker "$(_p "$_wo_marker")"; then
            record_change "created the marker file $_wo_marker"
        else
            warn "could not create the marker file at $_wo_marker, so $_wo_mp" \
                "will not be measured."
        fi
    done <<EOF
$FS_MOUNTS
EOF

    _wo_compose_existed=0
    [ ! -e "$OUTPUT_DIR/compose.yaml" ] || _wo_compose_existed=1

    netra_exec netra_write_compose "$_wo_compose_tmpl" "$OUTPUT_DIR/compose.yaml" ||
        die "could not write $OUTPUT_DIR/compose.yaml"

    # A marker this script does not know is a marker it did not substitute, and
    # an unsubstituted marker is a COMMENT LINE: no volumes, no devices, no
    # cap_add, and a compose that starts clean while the agent measures almost
    # nothing. The one way to reach it is version skew -- this script comes from
    # master, its templates from the latest release tag -- which is exactly what
    # happened when the markers were renamed from #__NETRA_*__ to #__AGENT_*__.
    # Silent is the whole problem, so it is fatal here.
    if grep -qE '^[[:space:]]*#__[A-Z_]+__[[:space:]]*$' "$OUTPUT_DIR/compose.yaml"; then
        die "$OUTPUT_DIR/compose.yaml still contains an unsubstituted template marker," \
            "so the template fetched from $REF is older than this script and the bind" \
            "mounts, devices and capabilities it describes were not rendered." \
            "Pass --ref <tag> to pin a newer release tag, or --template-dir <path> to use" \
            "local template files."
    fi

    if [ "$_wo_compose_existed" = 1 ]; then
        record_change "overwrote $OUTPUT_DIR/compose.yaml"
    else
        record_change "wrote $OUTPUT_DIR/compose.yaml"
    fi

    # compose.yaml is overwritten freely and .env is not (§12a). compose.yaml is
    # derived output — every byte of it comes from this run's detection — while
    # .env holds the token, and a re-run must not be able to silently replace a
    # working one.
    if [ -e "$OUTPUT_DIR/.env" ] && [ "$FORCE" != 1 ]; then
        warn "$OUTPUT_DIR/.env already exists and --force was not given, so it was left" \
            "untouched. Its existing token and settings still apply. Re-run with --force to" \
            "overwrite it."
        # One exception, and only one: AGENT_FS_MOUNTS. It is DERIVED from the
        # same detection that just rendered the bind mounts, exactly like
        # compose.yaml, and it is the line that stops the agent naming a
        # filesystem after the container path it is measured through. An .env
        # left alone here would keep the mounts and lose their names, which is
        # the bug this exists to fix — and demanding --force to fix it would
        # make the operator re-supply a token to correct a label.
        sync_fs_mounts "$OUTPUT_DIR/.env"

        # The second exception, on the same argument and a sharper edge: the
        # compose this run just wrote always carries `pid: host`, so an .env
        # still saying AGENT_PID_HOST=0 describes a container that no longer
        # exists -- and the agent believes it, refusing to read counters it
        # could read perfectly well.
        sync_pid_host "$OUTPUT_DIR/.env"

        # The ONE case where leaving .env alone leaves the agent broken, and
        # broken silently. AGENT_CGROUP_ROOT used to be documented as
        # /sys/fs/cgroup, so an operator who uncommented it then pins the agent
        # to its OWN cgroup hierarchy -- which under Docker's default private
        # cgroup namespace contains no other container's scope. The compose
        # this run just wrote mounts the host's tree at /host/sys/fs/cgroup and
        # the agent's default points there, but a pinned .env overrides both,
        # and the failure looks exactly like a host with no containers.
        #
        # grep on the value, not the key: an operator whose hierarchy genuinely
        # lives elsewhere is not making this mistake.
        if grep -qE '^[[:space:]]*AGENT_CGROUP_ROOT=/sys/fs/cgroup[[:space:]]*$' \
            "$OUTPUT_DIR/.env" 2>/dev/null; then
            warn "$OUTPUT_DIR/.env pins AGENT_CGROUP_ROOT=/sys/fs/cgroup, which is the" \
                "AGENT'S OWN cgroup hierarchy and holds no other container's scope. The" \
                "container collector will report nothing, without an error. Delete that" \
                "line (the default is now /host/sys/fs/cgroup, where this compose.yaml" \
                "mounts the host's tree) or set it to /host/sys/fs/cgroup."
        fi
    else
        _wo_env_existed=0
        [ ! -e "$OUTPUT_DIR/.env" ] || _wo_env_existed=1

        netra_exec netra_write_env "$_wo_env_tmpl" "$OUTPUT_DIR/.env" ||
            die "could not write $OUTPUT_DIR/.env"
        # The ledger names the file and never its contents: the token is in it.
        if [ "$_wo_env_existed" = 1 ]; then
            record_change "overwrote $OUTPUT_DIR/.env (--force)"
        else
            record_change "wrote $OUTPUT_DIR/.env"
        fi

        # INSIDE the branch that rendered the file, and only there. Run against
        # an .env this run deliberately left alone, these checks report every
        # value that differs from the existing file as a missing placeholder —
        # blaming the template for what the --force guard directly above just
        # did.
        _check_env_value AGENT_HUB_URL "$HUB_URL" "$OUTPUT_DIR/.env"
        _check_env_value AGENT_TOKEN "$TOKEN" "$OUTPUT_DIR/.env"
        _check_env_value AGENT_LOCATION "$LOCATION" "$OUTPUT_DIR/.env"
        _check_env_value AGENT_PROVIDER "$PROVIDER" "$OUTPUT_DIR/.env"
        _check_env_value AGENT_HOST_TYPE "$HOST_TYPE" "$OUTPUT_DIR/.env"
        _check_env_value AGENT_PRIMARY_SENSOR "$PRIMARY_SENSOR" "$OUTPUT_DIR/.env"
    fi

    rm -rf "$SCRATCH_DIR"
}

# netra_rewrite_fs_mounts FILE — replace (or append) the AGENT_FS_MOUNTS line.
#
# awk with ENVIRON, never `sed s///`: the value is built from mount points, and
# a `/` in the replacement would end a sed expression early.
#
# The result is copied back over the original with `cat` rather than moved into
# place. A `mv` would replace the inode, and this file holds the agent's token —
# whatever ownership and mode the operator gave it must survive a run that only
# came here to correct one derived line.
netra_rewrite_fs_mounts() {
    _rf_tmp="$SCRATCH_DIR/env.fs_mounts"
    awk '
        /^AGENT_FS_MOUNTS=/ {
            print "AGENT_FS_MOUNTS=" ENVIRON["AGENT_VAL_FS_MOUNTS"]
            seen = 1
            next
        }
        { print }
        END {
            if (!seen) {
                print ""
                print "# What each measured filesystem is called on this host, added by"
                print "# setup-agent.sh. Rendered from the same list as the bind mounts."
                print "AGENT_FS_MOUNTS=" ENVIRON["AGENT_VAL_FS_MOUNTS"]
            }
        }
    ' "$1" >"$_rf_tmp" || return 1
    # Checked before the truncating redirect below, never after. `cat >` empties
    # the target first, so an awk that produced nothing -- out of space, killed
    # mid-run -- would take the agent's token with it, and recovering that costs
    # the operator a new one.
    [ -s "$_rf_tmp" ] || return 1
    cat "$_rf_tmp" >"$1" || return 1
    rm -f "$_rf_tmp"
}

# netra_rewrite_pid_host FILE — replace (or append) the AGENT_PID_HOST line.
netra_rewrite_pid_host() {
    _rp_tmp="$SCRATCH_DIR/env.pid_host"
    awk '
        /^AGENT_PID_HOST=/ {
            print "AGENT_PID_HOST=1"
            seen = 1
            next
        }
        { print }
        END {
            if (!seen) {
                print ""
                print "# This container runs with `pid: host`, stated by setup-agent.sh so the"
                print "# collectors need not guess. Required for the process count and for"
                print "# per-container network traffic."
                print "AGENT_PID_HOST=1"
            }
        }
    ' "$1" >"$_rp_tmp" || return 1
    # Same order as netra_rewrite_fs_mounts, and for the same reason: `cat >`
    # truncates first, so an awk that produced nothing would take the token with
    # it.
    [ -s "$_rp_tmp" ] || return 1
    cat "$_rp_tmp" >"$1" || return 1
    rm -f "$_rp_tmp"
}

# sync_pid_host FILE — bring AGENT_PID_HOST in an existing .env up to date.
#
# The second derived line that must not be left behind, and the one an upgrade
# gets wrong most damagingly. compose.yaml is rewritten on every run and now
# always carries `pid: host`; .env is not rewritten without --force. So a host
# set up before this became unconditional keeps AGENT_PID_HOST=0 while actually
# HAVING the namespace -- and the agent believes the .env over the kernel.
#
# That combination is worse than the bug it replaced: containers.go refuses to
# read anything when it is told there is no host PID namespace, so those hosts
# would report zero per-container traffic FOREVER, and the message they get
# ("re-run setup-agent.sh") would be the very thing that just failed to fix it.
#
# Corrected without --force, like AGENT_FS_MOUNTS: the value is derived from
# what this run rendered, not supplied by the operator, and demanding a token
# be re-supplied to correct a line the script itself wrote would be absurd.
sync_pid_host() {
    [ -f "$1" ] || return 0
    if [ -n "$(sed -n '/^AGENT_PID_HOST=1$/p' "$1")" ]; then
        return 0
    fi
    if ! netra_exec netra_rewrite_pid_host "$1"; then
        warn "could not update AGENT_PID_HOST in $1. Until it says 1, the agent reports no" \
            "process count and no per-container network traffic, even though this run" \
            "granted the namespace. Set AGENT_PID_HOST=1 by hand."
        return 0
    fi
    record_change "updated AGENT_PID_HOST in $1"
    info "  processes:       AGENT_PID_HOST corrected to 1 in the existing .env"
}

# sync_fs_mounts FILE — bring one derived line of an existing .env up to date.
#
# Silent when the file already says the right thing, which is every re-run that
# changed nothing about the host's filesystems.
sync_fs_mounts() {
    [ -f "$1" ] || return 0
    _sf_want="${AGENT_BLK_FS_MOUNTS:-}"
    _sf_have=$(sed -n 's/^AGENT_FS_MOUNTS=//p' "$1" | head -1)
    if [ -n "$(sed -n '/^AGENT_FS_MOUNTS=/p' "$1")" ] && [ "$_sf_have" = "$_sf_want" ]; then
        return 0
    fi
    if ! netra_exec netra_rewrite_fs_mounts "$1"; then
        warn "could not update AGENT_FS_MOUNTS in $1. Until it is set, the agent reports" \
            "each filesystem under its short label instead of its mount point. Set it by" \
            "hand to: ${_sf_want:-(empty)}"
        return 0
    fi
    record_change "updated AGENT_FS_MOUNTS in $1"
    info "  filesystems:     AGENT_FS_MOUNTS updated in the existing .env"
}

# _check_env_value KEY VALUE FILE — did a value the operator gave us actually
# land in the rendered .env?
#
# The templates are fetched at a RELEASE TAG, not from this working tree, so a
# script that asks for a value the tagged env.tmpl has no `__TOKEN__` for will
# ask the question, echo the answer in the plan, and write nothing. Silent, and
# it recurs every time a value is added ahead of the release that carries the
# template. This turns it into a note in the finish report.
#
# grep -qF, not -q: a hub URL is full of regex metacharacters. The VALUE is
# never printed — this runs over the token too.
_check_env_value() {
    [ -n "$2" ] || return 0
    [ -f "$3" ] || return 0
    if ! grep -qF "$1=$2" "$3"; then
        warn "$1 is not in the rendered .env, although you gave a value for it. The env" \
            "template fetched at ${REF} has no placeholder for it — most likely this" \
            "script is newer than the release its templates come from. Set $1 in" \
            "$3 by hand and restart the agent."
    fi
}

# compose_cmd — whichever of the two spellings this host actually has.
compose_cmd() {
    if [ "$DOCKER_COMPOSE" = compose_v1 ]; then
        printf 'docker-compose'
    else
        printf 'docker compose'
    fi
}

# print_changes — the HOST_CHANGES ledger, rendered.
#
# The two drivetemp entries are DERIVED from DRIVETEMP_CHANGED and
# DRIVETEMP_PERSISTED rather than recorded by plan_drivetemp, because those two
# flags already are this truth and are maintained more carefully than a
# record_change call could be: the first is set at the successful modprobe and
# cleared again only when the unload actually succeeds. Recording separately
# would give the report a second source that has to be kept in step with them,
# and the report would be the copy that drifts.
#
# They come first because they happen first, and because they are the two the
# operator is least likely to have expected.
print_changes() {
    _pc_list="$HOST_CHANGES"
    if [ -n "$DRIVETEMP_PERSISTED" ]; then
        # The literal host path, not $P_MODULESLOAD: that one is a PROBE path
        # and carries the fixture prefix under test. plan_drivetemp prints the
        # literal for the same reason.
        _pc_list="wrote /etc/modules-load.d/drivetemp.conf${_pc_list:+
$_pc_list}"
    fi
    if [ -n "$DRIVETEMP_CHANGED" ]; then
        _pc_list="loaded the drivetemp kernel module${_pc_list:+
$_pc_list}"
    fi

    info ""
    if [ -z "$_pc_list" ]; then
        # NOT "Nothing was changed": that sentence belongs to the declined-gate
        # branch of write_outputs, which says it about the whole run at the
        # point the operator declined. This one is about the ledger.
        info "  Nothing on this host was changed."
        return 0
    fi

    info "  Changed on this host:"
    while IFS= read -r _pc_entry; do
        [ -n "$_pc_entry" ] || continue
        _wrap 76 '    - ' '      ' "$_pc_entry"
    done <<EOF
$_pc_list
EOF
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

    # BEFORE the early return on a declined gate, and before the skipped notes:
    # a declined run can still have loaded a kernel module, and what changed
    # outranks what was degraded.
    print_changes

    if [ -n "$SKIPPED_NOTES" ]; then
        info ""
        info "  Skipped or degraded:"
        while IFS= read -r _fr_note; do
            [ -n "$_fr_note" ] || continue
            # A tab-marked entry is a command from warn_cmd: verbatim, no
            # bullet, no wrapping — it exists to be copied.
            case "$_fr_note" in
            "$(printf '\t')"*)
                info "        $(printf '%s' "$_fr_note" | tr -d '\t')"
                ;;
            *)
                _wrap 76 '    - ' '      ' "$_fr_note"
                ;;
            esac
        done <<EOF
$SKIPPED_NOTES
EOF
    fi

    if [ "${WROTE_OUTPUTS:-1}" != 1 ]; then
        info ""
        info "  Nothing was written. Re-run when you want to apply the plan above."
        info ""
        return 0
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
    # `cd . && ...` reads as a bug, and OUTPUT_DIR is "." whenever this ran from
    # a directory already named netra-agent.
    # `pull` and not just `up -d`. The image tag is a moving `latest`, and
    # `up -d` reuses whatever copy of it the host already has — so on an
    # upgrade, the command that looks like it applied the change starts the old
    # binary again. An operator re-running this script to pick up an agent fix
    # would see nothing change and have no reason to suspect why.
    if [ "$OUTPUT_DIR" = "." ]; then
        info "    $(compose_cmd) pull && $(compose_cmd) up -d"
    else
        info "    cd $OUTPUT_DIR && $(compose_cmd) pull && $(compose_cmd) up -d"
    fi
    info ""
    info "  pull first: the image tag is a moving one, and up -d alone would restart the"
    info "  copy already on this host."
    info ""
}

start_stack() {
    [ "$START" = 1 ] || return 0
    # A declined write gate means the compose.yaml on disk, if any, is a
    # PREVIOUS run's — pairing it with this run's plan is exactly the bug the
    # single gate exists to prevent.
    [ "${WROTE_OUTPUTS:-1}" = 1 ] || return 0
    step "Start"
    # Pull before starting, for the same reason the finish report tells an
    # operator to: the image tag moves, and `up -d` alone restarts the copy
    # already on the host — so --start on a re-run to pick up an agent fix
    # would quietly start the unfixed binary again.
    #
    # Fatal, and said out loud. Starting anyway would run whatever copy of the
    # image this host already has, which on an upgrade is the binary the operator
    # came here to replace -- but a bare non-zero exit from errexit tells them
    # nothing about which of the two commands failed, or that everything above
    # this step was already written.
    _ss_pull_failed="could not pull the agent image, so nothing was started."
    _ss_pull_failed="$_ss_pull_failed compose.yaml and .env are written and the"
    _ss_pull_failed="$_ss_pull_failed markers are in place: fix the registry access"
    _ss_pull_failed="$_ss_pull_failed and run '$(compose_cmd) pull &&"
    _ss_pull_failed="$_ss_pull_failed $(compose_cmd) up -d' in $OUTPUT_DIR."
    if [ "$DOCKER_COMPOSE" = compose_v1 ]; then
        netra_exec docker-compose -f "$OUTPUT_DIR/compose.yaml" pull || die "$_ss_pull_failed"
        netra_exec docker-compose -f "$OUTPUT_DIR/compose.yaml" up -d
    else
        netra_exec docker compose -f "$OUTPUT_DIR/compose.yaml" pull || die "$_ss_pull_failed"
        netra_exec docker compose -f "$OUTPUT_DIR/compose.yaml" up -d
    fi

    # The one change that happens AFTER the summary has already printed its
    # ledger, because print_finish's "Starting the stack:" line only makes sense
    # ahead of the compose output. So this entry prints itself here, in the same
    # words the ledger would have used. Errexit is armed and the compose command
    # above is not guarded, so reaching this line means it succeeded.
    #
    # One variable for both, so the ledger entry and the printed line cannot
    # drift apart into two different sentences for one change.
    _ss_entry="started the agent from $OUTPUT_DIR/compose.yaml"
    record_change "$_ss_entry"
    info ""
    info "  Also changed on this host:"
    info "    - $_ss_entry"
}

# require_tty — the whole of "there is no unattended mode", in one place.
#
# Checked ONCE, before any phase, rather than discovered at the first prompt: a
# `curl ... | sh` on a host with no terminal should fail in the first second,
# not after three minutes of detection it is about to throw away.
require_tty() {
    # AGENT_ANSWERS_FILE is the test seam, checked ahead of $P_TTY inside
    # netra_ask.
    [ -z "${AGENT_ANSWERS_FILE:-}" ] || return 0
    [ -r "$P_TTY" ] && return 0

    die "no terminal available ($P_TTY is not readable), and this script is interactive." \
        "There is no unattended mode. Run it from a terminal. To configure a fleet," \
        "template deploy/agent/compose.yaml.example and .env.example from your" \
        "provisioning system instead."
}

netra_main() {
    # FIRST, before anything runs an external command. init_paths resolves
    # $P_UID with `id -u`, and on a host missing `id` that dies with the shell's
    # own "command not found" — never reaching the check whose entire job is to
    # name it. parse_args is after it for the same reason: --help needs `cat`.
    #
    # check_tools itself uses only `command -v`, a shell builtin, so it can run
    # this early. Its own message goes through _wrap and printf, both builtins.
    check_tools

    parse_args "$@"
    check_http_client
    init_paths
    init_colors

    if [ "${AGENT_DEBUG_PATHS:-0}" = 1 ]; then
        debug_paths
        return 0
    fi

    require_tty

    info "netra agent setup $AGENT_SETUP_VERSION"
    info ""
    describe_setup

    preflight
    detect_filesystems
    plan_smart
    detect_sensors
    detect_packages
    plan_extras
    configure
    print_plan

    # PLAINLY, never `if write_outputs` — see the note on the function. It sets
    # WROTE_OUTPUTS itself, which is what lets errexit stay armed for the writes.
    WROTE_OUTPUTS=0
    write_outputs

    print_finish
    start_stack
}

# Guarded entrypoint, and the last line of the file on purpose. Tests source
# this script with AGENT_SOURCED=1 to unit-test individual functions; a
# `curl ... | sh` pipeline runs it.
[ "${AGENT_SOURCED:-0}" = 1 ] || netra_main "$@"
