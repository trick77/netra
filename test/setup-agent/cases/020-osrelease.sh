#!/bin/sh
#
# /etc/os-release parsing, the supported-OS floors, and package-manager
# detection. These are unit tests: setup-agent.sh is sourced with
# AGENT_SOURCED=1 so the guarded entrypoint does not run.
set -eu
# shellcheck source=/dev/null
. "$LIB"

SETUP="$REPO/setup-agent.sh"

AGENT_SOURCED=1
export AGENT_SOURCED
# shellcheck source=/dev/null
. "$SETUP"

# --- 7. check_os_supported, table driven -------------------------------------
#
# ID | VERSION_ID | ID_LIKE | expected class
# Below-floor and unrecognised distros both land on `unknown`: the setup script has
# three outcomes, and "old Debian" is treated exactly like "never heard of it" —
# warn and ask, rather than refuse outright (spec §12a).
while IFS='|' read -r os_id os_ver os_like want; do
    [ -n "$os_id" ] || continue
    got=$(check_os_supported "$os_id" "$os_ver" "$os_like")
    assert_eq "$want" "$got" "check_os_supported $os_id $os_ver -> $want"
done <<'TABLE'
debian|10||unknown
debian|11||supported
debian|12||supported
ubuntu|20.04|debian|unknown
ubuntu|22.04|debian|supported
ubuntu|24.04|debian|supported
alpine|3.17.7||unknown
alpine|3.18.4||supported
alpine|3.18||supported
rocky|9.3|rhel centos fedora|nopkg
fedora|40||nopkg
rhel|9.4||nopkg
almalinux|8.9|rhel|unknown
voidlinux|||unknown
debian|||unknown
TABLE

# --- 8. read_os_release: strips quotes, executes nothing ----------------------
#
# The fixture contains PRETTY_NAME="Debian $(touch /tmp/netra-pwned)" and a
# backticked NAME. os-release is untrusted input the setup script is validating, so
# it is parsed with awk and never sourced.
PWNED=/tmp/netra-pwned
PWNED_BACKTICK=/tmp/netra-pwned-backtick
# Remove first: one genuinely broken run would otherwise leave the marker behind
# and make this case fail forever after.
rm -f "$PWNED" "$PWNED_BACKTICK"

AGENT_OSRELEASE_PATH=$(fixture os-release/injection)
export AGENT_OSRELEASE_PATH
init_paths
OS_LINE=$(read_os_release)

assert_file_absent "$PWNED" "read_os_release does not execute \$(...) in os-release"
assert_file_absent "$PWNED_BACKTICK" "read_os_release does not execute backticks in os-release"
# Single quotes are the whole point here: the needle must stay literal.
# shellcheck disable=SC2016
assert_contains "$OS_LINE" 'Debian $(touch /tmp/netra-pwned)' \
    "the command substitution survives as literal text"
assert_not_contains "$OS_LINE" '"Debian' "surrounding double quotes are stripped"

IFS='|' read -r ID_ VER_ LIKE_ PRETTY_ <<EOF
$OS_LINE
EOF
assert_eq "debian" "$ID_" "ID is parsed"
assert_eq "12" "$VER_" "VERSION_ID has its double quotes stripped"
assert_eq "" "$LIKE_" "a missing ID_LIKE yields an empty field"
# shellcheck disable=SC2016
assert_eq 'Debian $(touch /tmp/netra-pwned)' "$PRETTY_" "PRETTY_NAME is unquoted verbatim"

# Quoted and unquoted values from a real os-release.
AGENT_OSRELEASE_PATH=$(fixture os-release/rocky9)
export AGENT_OSRELEASE_PATH
init_paths
IFS='|' read -r ID_ VER_ LIKE_ PRETTY_ <<EOF
$(read_os_release)
EOF
assert_eq "rocky" "$ID_" "quoted ID is unquoted"
assert_eq "9.3" "$VER_" "quoted VERSION_ID is unquoted"
assert_eq "rhel centos fedora" "$LIKE_" "quoted multi-word ID_LIKE is unquoted"
assert_eq "Rocky Linux 9.3 (Blue Onyx)" "$PRETTY_" "quoted PRETTY_NAME is unquoted"

# --- 9. detect_pkgmgr ---------------------------------------------------------
#
# File existence first; ID/ID_LIKE only as the rpm fallback.
AGENT_SETUP_ROOT=$(fixture root-debian12)
export AGENT_SETUP_ROOT
unset AGENT_OSRELEASE_PATH
init_paths
assert_eq "dpkg" "$(detect_pkgmgr debian '')" "a dpkg status file wins"

# rpm host: neither database exists and ID=rocky.
AGENT_SETUP_ROOT=$(fixture root-rocky9)
export AGENT_SETUP_ROOT
init_paths
assert_eq "rpm" "$(detect_pkgmgr rocky 'rhel centos fedora')" \
    "ID=rocky with no dpkg or apk database detects rpm"
assert_eq "rpm" "$(detect_pkgmgr sles 'fedora')" "ID_LIKE alone can select rpm"
assert_eq "none" "$(detect_pkgmgr voidlinux '')" \
    "an unknown distro with no package database detects none"

# apk host.
APKROOT="$TMP/apkroot"
mkdir -p "$APKROOT/lib/apk/db"
printf 'P:busybox\n' >"$APKROOT/lib/apk/db/installed"
AGENT_SETUP_ROOT="$APKROOT"
export AGENT_SETUP_ROOT
init_paths
assert_eq "apk" "$(detect_pkgmgr alpine '')" "an apk installed database detects apk"

exit_case
