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

# --- 0. the block-device probes that survived, and their root strip -----------
#
# block_devices and device_transport no longer feed a devices: list, but they
# are still live code: detect_ata_devices calls them, and plan_drivetemp
# modprobes a kernel module and writes /etc/modules-load.d on the strength of
# the answer. Deleting their coverage along with the SMART probe left them with
# none, and the strip below is the part that must never go untested.

# mkdisk ROOT NAME SIZE PATHPART — a sysfs devices-tree entry for a whole
# device, with /sys/block/NAME linked at it the way the kernel does. The link
# is what makes device_transport meaningful: the transport is named by the
# components of the RESOLVED path, not by anything in /sys/block itself.
mkdisk() {
    _md_root="$1"
    _md_name="$2"
    _md_size="$3"
    _md_part="$4"
    _md_dev="$_md_root/sys/devices/$_md_part/block/$_md_name"
    mkdir -p "$_md_dev" "$_md_root/sys/block"
    printf '%s\n' "$_md_size" >"$_md_dev/size"
    mkdir -p "$_md_dev/device"
    ln -s "../devices/$_md_part/block/$_md_name" "$_md_root/sys/block/$_md_name"
}

# mkvirt ROOT NAME — a virtual device: in /sys/block, with no `device` link.
mkvirt() {
    mkdir -p "$1/sys/block/$2"
    printf '1024\n' >"$1/sys/block/$2/size"
}

R="$TMP/probe"
mkdisk "$R" sda 1000 "pci0000:00/ata1/host0"
mkdisk "$R" sdc 3000 "pci0000:00/usb1/1-1/1-1:1.0/host4"
mkdisk "$R" nvme0n1 4000 "pci0000:00/nvme/nvme0"
# An empty card reader slot: a real device link, size 0.
mkdisk "$R" mmcblk0 0 "pci0000:00/mmc_host/mmc0"
mkvirt "$R" loop0
mkvirt "$R" dm-0
AGENT_SETUP_ROOT="$R"
export AGENT_SETUP_ROOT
init_paths

DEVS=$(block_devices | sort | tr '\n' ' ')
assert_eq "nvme0n1 sda sdc " "$DEVS" \
    "only real whole devices survive (loop/dm and the empty card slot are out)"
assert_eq "sata" "$(device_transport sda)" "an ata* path component means SATA"
assert_eq "usb" "$(device_transport sdc)" "a usb1 path component means USB"
assert_eq "nvme" "$(device_transport nvme0n1)" "an nvme path component means NVMe"
assert_eq "unknown" "$(device_transport nosuchdisk)" "a device that is not there is unknown"

# THE STRIP, and the fixture that exists only to fail without it. A root under a
# directory called usb1 matches the USB pattern if AGENT_SETUP_ROOT is not
# stripped before matching -- and then every device on the host classifies as
# USB, SMART_ATA_DEVICES comes back empty, drivetemp is silently never offered,
# and the suite goes on passing. The Go port of this check carries the same
# guard for the same reason (TestSmartDoesNotReadUsbOutOfTheSysfsRootItself).
R="$TMP/usb1/root"
mkdir -p "$R"
mkdisk "$R" sdd 5000 "pci0000:00/ata3/host2"
AGENT_SETUP_ROOT="$R"
export AGENT_SETUP_ROOT
init_paths
assert_contains "$R" "/usb1/" "the guard fixture root really does contain a usb pattern"
assert_eq "sata" "$(device_transport sdd)" \
    "a fixture root path containing usb1 does not misclassify a SATA device"

# And the consumer that depends on it: detect_ata_devices must still find the
# drive plan_drivetemp would be offered for.
detect_ata_devices
assert_eq "sdd" "$SMART_ATA_DEVICES" "the ATA candidate survives the strip"

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
printf 'y\nn\n' >"$TMP/ans-decline"
AGENT_ANSWERS_FILE="$TMP/ans-decline"
export AGENT_ANSWERS_FILE
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_RAWIO" "SYS_RAWIO is granted on a host with no visible block devices"
assert_eq 0 "$CAP_SYS_ADMIN" "declining leaves SYS_ADMIN off"
assert_eq 2 "$AGENT_ANSWER_INDEX" \
    "exactly two prompts: the device-tree grant and SYS_ADMIN, and nothing else"
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
printf 'y\ny\n' >"$TMP/ans-accept"
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
printf 'y\n' >"$TMP/ans-grantflag"
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
assert_eq 1 "$AGENT_ANSWER_INDEX" \
    "--sys-admin consumes no answer of its own: the one line read is the SMART grant"
GRANT_SYS_ADMIN=0

# --- 5. the device block is rules, not a device list --------------------------
#
# Unconditional, and that is the point: there is no host state that can empty
# it, because there is no host state it reads.
build_device_block
assert_contains "$AGENT_BLK_DEVICES" "device_cgroup_rules" "the device block is cgroup rules"
assert_contains "$AGENT_BLK_DEVICES" 'b *:* rw' "block devices are permitted"
assert_contains "$AGENT_BLK_DEVICES" 'c *:* rw' \
    "character devices are permitted, which is what /dev/nvme0 is"
# rw, never rmw. The m is mknod, and Docker's default caps include CAP_MKNOD,
# so with it the container could make a block node on its own writable layer -
# where the read-only /dev bind does not reach - and write the raw disk.
assert_not_contains "$AGENT_BLK_DEVICES" "rmw" "mknod is never granted"
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
