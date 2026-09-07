#!/bin/sh
#
# The kernel log grant: one device node and one capability.
#
# What makes this worth its own case is the DEVICES key. /dev/kmsg is char
# device 1:11 and is not on Docker's default device cgroup allowlist, so a bind
# mount would create a node the container cannot open -- EPERM at runtime, with
# nothing at startup saying so. Only a `devices:` entry does both jobs, and the
# same block also carries SMART's cgroup rules, so the two halves have to be
# renderable independently.
# Many variables set here are read by the SOURCED setup script, not by this
# file, so shellcheck cannot see the use.
# shellcheck disable=SC2034
set -eu
# shellcheck source=/dev/null
. "$LIB"

SETUP="$REPO/setup-agent.sh"

AGENT_SOURCED=1
export AGENT_SOURCED
# shellcheck source=/dev/null
. "$SETUP"

# --- 1. granted: the device and the capability, both -------------------------
KMSG_ENABLED=1
CAP_SYSLOG=1
SMART_ENABLED=0
CAP_RAWIO=0
CAP_SYS_ADMIN=0

build_device_block
assert_contains "$AGENT_BLK_DEVICES" "devices:" "the kernel log renders a devices: key"
assert_contains "$AGENT_BLK_DEVICES" "/dev/kmsg:/dev/kmsg:r" \
    "the node is named, because a bind alone cannot be opened"
# Read-only, and meant literally: writing to /dev/kmsg injects messages into
# the host's kernel log, which the agent has no business doing.
assert_not_contains "$AGENT_BLK_DEVICES" "/dev/kmsg:/dev/kmsg:rw" \
    "the device is never granted write access"
# SMART declined, so its half must not appear.
assert_not_contains "$AGENT_BLK_DEVICES" "device_cgroup_rules" \
    "the kernel log does not drag SMART's wildcard rules in with it"

build_cap_block
assert_contains "$AGENT_BLK_CAP_ADD" "SYSLOG" "the cap block carries SYSLOG"
assert_not_contains "$AGENT_BLK_CAP_ADD" "SYS_RAWIO" \
    "and nothing SMART would have added"

# --- 2. both granted: one block, both halves ---------------------------------
#
# cap_add is ONE key. Two grants that each rendered their own would leave
# compose keeping only the last, and the first would vanish silently.
SMART_ENABLED=1
CAP_RAWIO=1
build_device_block
assert_contains "$AGENT_BLK_DEVICES" "/dev/kmsg" "the kernel log survives beside SMART"
assert_contains "$AGENT_BLK_DEVICES" "device_cgroup_rules" "and so do SMART's rules"

build_cap_block
assert_eq 1 "$(printf '%s' "$AGENT_BLK_CAP_ADD" | grep -c 'cap_add:')" \
    "exactly one cap_add key, however many grants feed it"
assert_contains "$AGENT_BLK_CAP_ADD" "SYS_RAWIO" "SMART's capability is there"
assert_contains "$AGENT_BLK_CAP_ADD" "SYSLOG" "and so is the kernel log's"

# --- 3. declined: the key vanishes rather than rendering empty ----------------
#
# An empty mapping key is a compose file that will not parse, which is why
# every conditional block in this script deletes its own marker line.
KMSG_ENABLED=0
CAP_SYSLOG=0
SMART_ENABLED=0
CAP_RAWIO=0
build_device_block
assert_eq "" "$AGENT_BLK_DEVICES" "with nothing granted the whole block is empty"
build_cap_block
assert_eq "" "$AGENT_BLK_CAP_ADD" "and so is the cap block"

# --- 4. detection follows the host ------------------------------------------
#
# plan_extras reads one path. A host without it is a supported deployment: the
# collector reports a capability and every other collector is unaffected.
ROOT="$TMP/kmsg-host"
mkdir -p "$ROOT/dev" "$ROOT/proc/sys/kernel"
printf '' >"$ROOT/dev/kmsg"
printf '1\n' >"$ROOT/proc/sys/kernel/dmesg_restrict"

P_KMSG="$ROOT/dev/kmsg"
P_DMESG_RESTRICT="$ROOT/proc/sys/kernel/dmesg_restrict"
KMSG_ENABLED=0
CAP_SYSLOG=0
if [ -e "$P_KMSG" ]; then
    KMSG_ENABLED=1
    CAP_SYSLOG=1
fi
assert_eq 1 "$KMSG_ENABLED" "a host with /dev/kmsg enables the collector"
assert_eq 1 "$CAP_SYSLOG" "and takes the capability that reads it"

rm -f "$ROOT/dev/kmsg"
KMSG_ENABLED=0
CAP_SYSLOG=0
if [ -e "$P_KMSG" ]; then
    KMSG_ENABLED=1
    CAP_SYSLOG=1
fi
assert_eq 0 "$KMSG_ENABLED" "a host without it is not enabled"

exit_case
