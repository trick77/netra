#!/bin/sh
#
# Test runner for install-agent.sh.
#
#   sh   test/install-agent/run.sh            # all cases
#   dash test/install-agent/run.sh            # same cases, under dash
#   sh   test/install-agent/run.sh preflight  # only cases matching a pattern
#
# Each case runs in its own process with a fresh $TMP and a cleanup trap.
set -eu

HERE=$(cd "$(dirname "$0")" && pwd -P)
REPO=$(cd "$HERE/../.." && pwd -P)
FIXTURES="$HERE/fixtures"
LIB="$HERE/lib.sh"
export REPO FIXTURES LIB

# Which shell to run the cases and install-agent.sh under. `dash run.sh` must
# actually exercise dash, so the runner asks the OS what interpreter it is
# executing under rather than hardcoding `sh`. NETRA_TEST_SH overrides.
SH="${NETRA_TEST_SH:-}"
if [ -z "$SH" ]; then
    SH=$(ps -o comm= -p $$ 2>/dev/null | sed 's|.*/||; s/^-//') || SH=""
fi
if [ -z "$SH" ]; then
    SH="sh"
fi
export SH

PATTERN="${1:-}"

# Guard: several fixture directories are meaningful precisely because they are
# EMPTY — the installer probes for their existence, not their contents
# (`/sys/block/sda/device`, a mount point to create a marker dir under, an NVMe
# controller node). Git does not track empty directories, so without a
# placeholder file they exist in a working tree and vanish in a fresh clone.
#
# That failure mode already happened once and was expensive to read: the suite
# was green locally and on a developer's machine, and failed only in CI with
# four assertions pointing at the detection logic — "the SATA device is still
# collected", "the ark marker directory is created" — none of which named the
# real cause. Check up front and say so plainly instead.
for _req in \
    root-full/mnt/ark \
    root-full/sys/class/nvme/nvme0 \
    "root-full/sys/devices/pci0000:00/ata1/host0/block/sda/device" \
    "root-full/sys/devices/pci0000:00/nvme/nvme0/block/nvme0n1/device"; do
    if [ ! -d "$FIXTURES/$_req" ]; then
        printf 'fixture directory missing: %s\n' "$FIXTURES/$_req" >&2
        printf 'It is empty by design and needs a .gitkeep to survive a clone.\n' >&2
        exit 2
    fi
done

# Inline trap bodies rather than a cleanup function: $TMP changes per case, so
# the trap has to read it at fire time, and an unset $TMP must be a no-op.
TMP=""
trap '[ -z "$TMP" ] || rm -rf "$TMP"' EXIT
trap '[ -z "$TMP" ] || rm -rf "$TMP"; exit 130' INT
trap '[ -z "$TMP" ] || rm -rf "$TMP"; exit 143' TERM

PASS=0
FAIL=0

for CASEFILE in "$HERE"/cases/*.sh; do
    CASE_NAME=$(basename "$CASEFILE")

    if [ -n "$PATTERN" ]; then
        case "$CASE_NAME" in
        *"$PATTERN"*) ;;
        *) continue ;;
        esac
    fi

    # Canonicalise TMP with `cd … && pwd -P`.
    #
    # WHY: on macOS `mktemp -d` returns a path under a SYMLINKED directory —
    # /var/folders/... where /var -> /private/var (and likewise /tmp ->
    # /private/tmp). Any code that later resolves a path with `pwd -P`,
    # `realpath` or a sysfs symlink read gets back the /private/... form, so a
    # naive `${path#$NETRA_INSTALL_ROOT}` strip against the un-canonicalised
    # /var/... root silently fails to strip and the prefix leaks into the
    # rendered compose. Canonicalising here means fixture roots are already
    # physical paths and the strip is a plain prefix match. This bites the SMART
    # transport detection (which resolves /sys/class/block/*/device symlinks),
    # so the harness gets it right before that code exists.
    TMP=$(cd "$(mktemp -d)" && pwd -P)
    export TMP CASE_NAME

    printf '\n=== %s\n' "$CASE_NAME"
    # `if cmd` — a bare invocation would abort the whole runner on the first
    # failing case under `set -e`.
    if "$SH" "$CASEFILE"; then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
        printf 'CASE FAILED: %s\n' "$CASE_NAME" >&2
    fi

    rm -rf "$TMP"
    TMP=""
done

printf '\n=== summary: %s passed, %s failed (shell: %s)\n' "$PASS" "$FAIL" "$SH"
if [ "$FAIL" -ne 0 ]; then
    exit 1
fi
exit 0
