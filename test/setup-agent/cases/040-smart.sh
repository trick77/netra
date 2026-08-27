#!/bin/sh
#
# The SMART privileges and the compose blocks that carry them.
#
# This case used to test a hardware probe: block device enumeration, transport
# classification, NVMe controller resolution. All of it is gone. The script no
# longer decides which drives are real -- it grants access to the device tree
# and the agent scans it on every collection -- so what is left to test is that
# plan_smart probes NOTHING and that the two compose blocks which replace the
# devices: list are always rendered.
# Many variables set here are read by the SOURCED setup script, not by this file,
# so shellcheck cannot see the use. The directive is file-wide rather than
# repeated at a dozen assignments.
# shellcheck disable=SC2034
set -eu
# shellcheck source=/dev/null
. "$LIB"

SETUP="$REPO/setup-agent.sh"

AGENT_SOURCED=1
export AGENT_SOURCED
# shellcheck source=/dev/null
. "$SETUP"

PRIMARY_SENSOR=""

# --- 1. plan_smart on a root with no block devices at all ---------------------
#
# The discriminating fixture. An empty root once meant "no drives, nothing to
# collect" and produced no capability; now it must produce exactly the same
# grants as a root full of disks, because the answer no longer comes from here.
R="$TMP/empty"
mkdir -p "$R/sys" "$R/dev"
AGENT_SETUP_ROOT="$R"
export AGENT_SETUP_ROOT
init_paths

AGENT_ANSWER_INDEX=0
SKIPPED_NOTES=""
printf 'n\n' >"$TMP/ans-decline"
AGENT_ANSWERS_FILE="$TMP/ans-decline"
export AGENT_ANSWERS_FILE
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_RAWIO" "SYS_RAWIO is granted on a host with no visible block devices"
assert_eq 0 "$CAP_SYS_ADMIN" "declining leaves SYS_ADMIN off"
assert_eq 1 "$AGENT_ANSWER_INDEX" "exactly one prompt: SYS_ADMIN, and nothing else"
assert_contains "$SKIPPED_NOTES" "SYS_ADMIN declined" "declining SYS_ADMIN is recorded as a skip"
assert_contains "$SKIPPED_NOTES" "temperature still works" \
    "the skip note says what declining does NOT cost"

# --- 2. the prompt still explains what it does and does not cost --------------
AGENT_ANSWER_INDEX=0
SKIPPED_NOTES=""
run_capture plan_smart
assert_contains "$RUN_OUT" "temperature" \
    "the SYS_ADMIN prompt states that NVMe temperature works without it"
assert_contains "$RUN_OUT" "scans for drives itself" "the run says the agent finds the drives itself"

# --- 3. SYS_ADMIN granted -----------------------------------------------------
AGENT_ANSWER_INDEX=0
SKIPPED_NOTES=""
printf 'y\n' >"$TMP/ans-accept"
AGENT_ANSWERS_FILE="$TMP/ans-accept"
export AGENT_ANSWERS_FILE
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_SYS_ADMIN" "granting SYS_ADMIN sets the capability"
build_cap_block
assert_contains "$AGENT_BLK_CAP_ADD" "SYS_RAWIO" "the cap block carries SYS_RAWIO"
assert_contains "$AGENT_BLK_CAP_ADD" "SYS_ADMIN" "the cap block carries SYS_ADMIN"

# --- 4. --sys-admin grants without consuming a prompt -------------------------
#
# The discriminating assertion is AGENT_ANSWER_INDEX. An EMPTY answers file
# means a --sys-admin that still prompted would exhaust the file and die, so
# "index is 0" is the proof that the prompt was skipped rather than answered.
#
# Note what is NOT asserted any more: that the flag goes unhonoured on a host
# with no NVMe. The script cannot know that, and pretending to would be the
# probe coming back through a side door.
GRANT_SYS_ADMIN=1
AGENT_ANSWER_INDEX=0
SKIPPED_NOTES=""
: >"$TMP/ans-grantflag"
AGENT_ANSWERS_FILE="$TMP/ans-grantflag"
export AGENT_ANSWERS_FILE
run_capture plan_smart
assert_eq 0 "$RUN_RC" "--sys-admin consumes nothing from an empty answers file"
assert_contains "$RUN_OUT" "--sys-admin" "the output says the capability came from the flag"

# run_capture ran plan_smart in a command-substitution subshell, so none of its
# assignments survived. Re-run it in this shell for the state assertions.
AGENT_ANSWER_INDEX=0
SKIPPED_NOTES=""
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_SYS_ADMIN" "--sys-admin grants SYS_ADMIN"
assert_eq 0 "$AGENT_ANSWER_INDEX" "--sys-admin consumes no answer: the prompt is skipped entirely"
GRANT_SYS_ADMIN=0

# --- 5. the device block is rules, not a device list --------------------------
#
# Unconditional, and that is the point: there is no host state that can empty
# it, because there is no host state it reads.
build_device_block
assert_contains "$AGENT_BLK_DEVICES" "device_cgroup_rules" "the device block is cgroup rules"
assert_contains "$AGENT_BLK_DEVICES" 'b *:* rmw' "block devices are permitted"
assert_contains "$AGENT_BLK_DEVICES" 'c *:* rmw' \
    "character devices are permitted, which is what /dev/nvme0 is"
assert_not_contains "$AGENT_BLK_DEVICES" "devices:" "no devices: key is rendered any more"

# --- 6. /dev is bound, at /dev ------------------------------------------------
#
# The target is the assertion that matters. `smartctl --scan` looks at /dev and
# nowhere else, so a bind landing on /host/dev like the other host paths would
# render a compose file in which the scan finds nothing -- silently, and only
# on real hosts.
FS_MOUNTS=""
build_volume_block
assert_contains "$AGENT_BLK_VOLUMES" "source: \"/dev\"" "the host's device tree is bound"
assert_contains "$AGENT_BLK_VOLUMES" "target: /dev" "it is bound AT /dev, where smartctl looks"

exit_case
