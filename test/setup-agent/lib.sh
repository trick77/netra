# shellcheck shell=sh
#
# Assertion helpers for the setup-agent.sh test cases.
#
# Sourced by every file in cases/ as `. "$LIB"`. run.sh exports LIB, REPO,
# FIXTURES, TMP and SH.
#
# Assertions NEVER return non-zero. A case file runs under `set -eu`, and an
# assertion that returned 1 would abort the case at the first failure, hiding
# every later assertion. Failures are counted instead, and exit_case turns the
# count into the case's exit status.

TESTS_RUN=0
TESTS_FAILED=0

ok() {
    printf 'ok   %s\n' "$1"
}

fail() {
    TESTS_FAILED=$((TESTS_FAILED + 1))
    printf 'FAIL %s\n' "$1" >&2
}

assert_eq() {
    # assert_eq EXPECTED ACTUAL MSG
    TESTS_RUN=$((TESTS_RUN + 1))
    if [ "$1" = "$2" ]; then
        ok "$3"
    else
        fail "$3"
        printf '       expected: [%s]\n' "$1" >&2
        printf '         actual: [%s]\n' "$2" >&2
    fi
}

assert_contains() {
    # assert_contains HAYSTACK NEEDLE MSG
    TESTS_RUN=$((TESTS_RUN + 1))
    case "$1" in
    *"$2"*)
        ok "$3"
        ;;
    *)
        fail "$3"
        printf '       needle not found: [%s]\n' "$2" >&2
        printf '       haystack was:\n' >&2
        printf '%s\n' "$1" | sed 's/^/         | /' >&2
        ;;
    esac
}

assert_not_contains() {
    # assert_not_contains HAYSTACK NEEDLE MSG
    TESTS_RUN=$((TESTS_RUN + 1))
    case "$1" in
    *"$2"*)
        fail "$3"
        printf '       needle unexpectedly found: [%s]\n' "$2" >&2
        printf '%s\n' "$1" | sed 's/^/         | /' >&2
        ;;
    *)
        ok "$3"
        ;;
    esac
}

assert_exit_code() {
    # assert_exit_code EXPECTED CMD...
    _aec_want="$1"
    shift
    TESTS_RUN=$((TESTS_RUN + 1))
    # `if cmd` so a non-zero exit does not trip the case's `set -e`.
    if "$@" >/dev/null 2>&1; then
        _aec_rc=0
    else
        _aec_rc=$?
    fi
    if [ "$_aec_rc" = "$_aec_want" ]; then
        ok "exit $_aec_want from: $*"
    else
        fail "expected exit $_aec_want, got $_aec_rc from: $*"
    fi
}

assert_file_absent() {
    # assert_file_absent PATH MSG
    TESTS_RUN=$((TESTS_RUN + 1))
    if [ -e "$1" ]; then
        fail "$2 (path exists: $1)"
    else
        ok "$2"
    fi
}

assert_file_present() {
    # assert_file_present PATH MSG
    TESTS_RUN=$((TESTS_RUN + 1))
    if [ -e "$1" ]; then
        ok "$2"
    else
        fail "$2 (path missing: $1)"
    fi
}

# run_capture CMD... — runs CMD, leaving combined output in RUN_OUT and the
# exit status in RUN_RC. Never aborts the caller.
run_capture() {
    if RUN_OUT=$("$@" 2>&1); then
        RUN_RC=0
    else
        RUN_RC=$?
    fi
    # Exported because the consumers are the case files, not this file.
    export RUN_OUT RUN_RC
}

# answers NAME LINE... — write a y/n answers file and print its path.
#
# Spelling the answers as arguments rather than a printf of '\n'-joined letters
# is the whole point: `answers foo n y n` is checkable against the PROMPT ORDER
# contract at a glance, and a run that consumes one line too many dies naming
# the question it could not answer.
answers() {
    _ans_name="$1"
    shift
    : >"$TMP/ans-$_ans_name"
    for _ans_l in "$@"; do
        printf '%s\n' "$_ans_l" >>"$TMP/ans-$_ans_name"
    done
    printf '%s\n' "$TMP/ans-$_ans_name"
}

# values NAME LINE... — the same for NETRA_VALUES_FILE (free-text prompts).
# A separate file with its own index, so adding a value prompt never shifts a
# y/n answers file by a line.
values() {
    _val_name="$1"
    shift
    : >"$TMP/val-$_val_name"
    for _val_l in "$@"; do
        printf '%s\n' "$_val_l" >>"$TMP/val-$_val_name"
    done
    printf '%s\n' "$TMP/val-$_val_name"
}

# fixture NAME — absolute path of a fixture.
fixture() {
    printf '%s\n' "$FIXTURES/$1"
}

# mkshims DIR — build PATH shims for docker, docker-compose and curl.
#
# PATH shims, deliberately NOT shell function overrides: check_docker uses
# `command -v docker`, which a shell function would satisfy, so an override
# would hide a regression where the binary check was dropped. A real executable
# on PATH exercises exactly the code path a real host takes.
#
# The shims read their behaviour from the environment AT RUN TIME
# (NETRA_SHIM_INFO_RC, NETRA_SHIM_COMPOSE_V2_RC, NETRA_SHIM_HAVE_COMPOSE_V1,
# NETRA_SHIM_CURL_RC, NETRA_SHIM_CURL_BODY) so a case can vary them without
# regenerating the shims.
mkshims() {
    _mks_dir="$1"
    mkdir -p "$_mks_dir/bin"
    NETRA_SHIM_LOG="$_mks_dir/calls.log"
    : >"$NETRA_SHIM_LOG"
    export NETRA_SHIM_LOG

    cat >"$_mks_dir/bin/docker" <<'SHIM'
#!/bin/sh
printf 'docker %s\n' "$*" >>"$NETRA_SHIM_LOG"
case "${1:-}" in
info)
    if [ "${NETRA_SHIM_INFO_RC:-0}" != 0 ]; then
        printf 'Cannot connect to the Docker daemon\n' >&2
        exit "${NETRA_SHIM_INFO_RC}"
    fi
    printf 'Server Version: 27.0.0\n'
    exit 0
    ;;
compose)
    if [ "${NETRA_SHIM_COMPOSE_V2_RC:-0}" != 0 ]; then
        printf "docker: 'compose' is not a docker command.\n" >&2
        exit "${NETRA_SHIM_COMPOSE_V2_RC}"
    fi
    printf 'Docker Compose version v2.29.0\n'
    exit 0
    ;;
esac
exit 0
SHIM

    cat >"$_mks_dir/bin/docker-compose" <<'SHIM'
#!/bin/sh
printf 'docker-compose %s\n' "$*" >>"$NETRA_SHIM_LOG"
if [ "${NETRA_SHIM_HAVE_COMPOSE_V1:-1}" != 1 ]; then
    printf 'docker-compose: unavailable\n' >&2
    exit 127
fi
printf 'docker-compose version 1.29.2\n'
exit 0
SHIM

    # modprobe is shimmed for two reasons at once: macOS has no modprobe at all,
    # so plan_drivetemp would take its "no modprobe on PATH" branch on a laptop
    # and its real branch in CI — the prompt count would differ per platform and
    # every answers file with it. And a real modprobe in CI would load a kernel
    # module into the runner.
    #
    # NETRA_SHIM_MODPROBE_HWMON makes the shim HONEST rather than merely
    # successful: it creates the hwmon chip a working drivetemp would create, so
    # the verify-then-persist path is exercised end to end. Left unset, the shim
    # exits 0 and creates nothing, which is exactly the "loads but reports no SCT
    # temperature" hardware plan_drivetemp must detect and unload again.
    cat >"$_mks_dir/bin/modprobe" <<'SHIM'
#!/bin/sh
printf 'modprobe %s\n' "$*" >>"$NETRA_SHIM_LOG"
if [ "${NETRA_SHIM_MODPROBE_RC:-0}" != 0 ]; then
    printf 'modprobe: FATAL: Module drivetemp not found.\n' >&2
    exit "${NETRA_SHIM_MODPROBE_RC}"
fi
if [ "${1:-}" = "-r" ]; then
    [ -z "${NETRA_SHIM_MODPROBE_HWMON:-}" ] || rm -rf "$NETRA_SHIM_MODPROBE_HWMON/hwmon90"
    exit 0
fi
if [ -n "${NETRA_SHIM_MODPROBE_HWMON:-}" ]; then
    mkdir -p "$NETRA_SHIM_MODPROBE_HWMON/hwmon90"
    printf 'drivetemp\n' >"$NETRA_SHIM_MODPROBE_HWMON/hwmon90/name"
    printf '38000\n' >"$NETRA_SHIM_MODPROBE_HWMON/hwmon90/temp1_input"
fi
exit 0
SHIM

    cat >"$_mks_dir/bin/curl" <<'SHIM'
#!/bin/sh
printf 'curl %s\n' "$*" >>"$NETRA_SHIM_LOG"
printf '%s' "${NETRA_SHIM_CURL_BODY:-}"
exit "${NETRA_SHIM_CURL_RC:-0}"
SHIM

    chmod +x "$_mks_dir/bin/docker" "$_mks_dir/bin/docker-compose" "$_mks_dir/bin/curl" \
        "$_mks_dir/bin/modprobe"
    PATH="$_mks_dir/bin:$PATH"
    export PATH
}

exit_case() {
    printf '%s: %s assertions, %s failed\n' "${CASE_NAME:-case}" "$TESTS_RUN" "$TESTS_FAILED"
    if [ "$TESTS_FAILED" -ne 0 ]; then
        exit 1
    fi
    exit 0
}
