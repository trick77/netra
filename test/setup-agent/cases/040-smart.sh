#!/bin/sh
#
# Block device enumeration, transport classification, NVMe controller
# resolution, and the one SMART capability prompt that remains.
# Many variables set here are read by the SOURCED setup script, not by this file,
# so shellcheck cannot see the use. The directive is file-wide rather than
# repeated at a dozen assignments.
# shellcheck disable=SC2034
set -eu
# shellcheck source=/dev/null
. "$LIB"

SETUP="$REPO/setup-agent.sh"

NETRA_SOURCED=1
export NETRA_SOURCED
# shellcheck source=/dev/null
. "$SETUP"

DRY_RUN=1
ASSUME_YES=0
PRIMARY_SENSOR=""

# mkdisk ROOT NAME SIZE PATHPART — build a sysfs devices-tree entry for a whole
# device and link /sys/block/NAME at it, the way the kernel does.
#
# The link is what makes device_transport meaningful: the transport is named by
# the components of the RESOLVED path, not by anything in /sys/block itself.
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

# mkvirt ROOT NAME — a virtual device: present in /sys/block, no `device` link.
mkvirt() {
    mkdir -p "$1/sys/block/$2"
    printf '1024\n' >"$1/sys/block/$2/size"
}

mkdev() {
    mkdir -p "$1/dev"
    : >"$1/dev/$2"
}

# --- 1. block_devices lists whole physical devices only -----------------------
R="$TMP/r1"
mkdisk "$R" sda 1000 "pci0000:00/ata1/host0"
mkdisk "$R" sdb 2000 "pci0000:00/ata2/host1"
# Virtual devices. loop/dm/md/sr also have no `device` link in reality, but the
# name filter is what must reject them: a dm-0 with a device link (multipath)
# exists and is still not a drive smartctl should be pointed at.
mkvirt "$R" loop0
mkvirt "$R" dm-0
mkvirt "$R" md0
mkvirt "$R" sr0
mkvirt "$R" zram0
# An empty card reader slot: a real device link, size 0.
mkdisk "$R" mmcblk0 0 "pci0000:00/mmc_host/mmc0"
# A virtual device that passes the name filter but has no `device` link.
mkvirt "$R" vda

NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths

DEVS=$(block_devices | sort | tr '\n' ' ')
assert_eq "sda sdb " "$DEVS" "only real whole devices survive (loop/dm/md/sr/zram/empty reader are out)"

# The P_SYSBLOCK probe is /sys/block, where partitions are SUBDIRECTORIES of
# their parent and so are never top-level entries. Excluding them is not this
# function's job, and this asserts the tree it is actually reading.
assert_contains "$P_SYSBLOCK" "/sys/block" "block devices are probed from /sys/block"

# --- 2. device_transport ------------------------------------------------------
assert_eq "sata" "$(device_transport sda)" "an ata* path component means SATA"

R="$TMP/r2"
mkdisk "$R" sdc 3000 "pci0000:00/usb1/1-1/1-1:1.0/host4"
mkdisk "$R" nvme0n1 4000 "pci0000:00/nvme/nvme0"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
assert_eq "usb" "$(device_transport sdc)" "a usb1 path component means USB"
assert_eq "nvme" "$(device_transport nvme0n1)" "an nvme path component means NVMe"
assert_eq "unknown" "$(device_transport nosuchdisk)" "a device that is not there is unknown"

# --- 3. the NETRA_SETUP_ROOT strip, which is the whole ballgame -------------
#
# A fixture root under a directory called `usb1` matches the USB pattern if the
# prefix is not stripped before matching, and then EVERY device on the host is
# classified as USB — silently, and with the test suite agreeing. This fixture
# exists only to fail if the strip is ever removed.
R="$TMP/usb1/root"
mkdir -p "$R"
mkdisk "$R" sdd 5000 "pci0000:00/ata3/host2"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
assert_contains "$R" "/usb1/" "the guard fixture root really does contain a usb pattern"
assert_eq "sata" "$(device_transport sdd)" \
    "a fixture root path containing usb1 does not misclassify a SATA device"

# --- 4. NVMe controllers, not namespaces --------------------------------------
#
# Two namespaces on one controller must yield ONE devices: entry. /sys/class/nvme
# is the primary source.
R="$TMP/r4"
mkdisk "$R" nvme0n1 4000 "pci0000:00/nvme/nvme0"
mkdisk "$R" nvme0n2 4000 "pci0000:00/nvme/nvme0"
mkdir -p "$R/sys/class/nvme/nvme0"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
CTRLS=$(nvme_controllers | tr '\n' ' ')
assert_eq "nvme0 " "$CTRLS" "two namespaces on one controller yield one controller"

# The old-kernel fallback: no /sys/class/nvme, derive it from the namespace names.
rm -rf "$R/sys/class/nvme"
init_paths
CTRLS=$(nvme_controllers | tr '\n' ' ')
assert_eq "nvme0 " "$CTRLS" "the namespace-name fallback also dedups to one controller"

# --- 5. SATA only: SYS_RAWIO present, SYS_ADMIN absent ------------------------
R="$TMP/r5"
mkdisk "$R" sda 1000 "pci0000:00/ata1/host0"
mkdev "$R" sda
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths

ASSUME_YES=1
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_RAWIO" "a SATA-only host gets SYS_RAWIO"
# Read by plan_drivetemp in the sensor phase to decide whether the drivetemp
# module is worth offering, so it has to outlive plan_smart.
assert_eq "sda" "$SMART_ATA_DEVICES" "the SATA device list survives plan_smart"
assert_eq 0 "$CAP_SYS_ADMIN" "a SATA-only host is never even asked about SYS_ADMIN"
assert_eq "/dev/sda" "$SMART_DEVICES" "the SATA device is emitted unprefixed"
assert_not_contains "$SMART_DEVICES" "$R" "NETRA_SETUP_ROOT never leaks into devices:"

build_cap_block
assert_contains "$NETRA_BLK_CAP_ADD" "SYS_RAWIO" "the cap block carries SYS_RAWIO"
assert_not_contains "$NETRA_BLK_CAP_ADD" "SYS_ADMIN" "the cap block has no SYS_ADMIN"

# --- 6. NVMe present, SYS_ADMIN declined --------------------------------------
R="$TMP/r6"
mkdisk "$R" sda 1000 "pci0000:00/ata1/host0"
mkdisk "$R" nvme0n1 4000 "pci0000:00/nvme/nvme0"
mkdir -p "$R/sys/class/nvme/nvme0"
mkdev "$R" sda
mkdev "$R" nvme0
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths

SKIPPED_NOTES=""
NETRA_ANSWER_INDEX=0
# ONE prompt inside plan_smart: SYS_ADMIN. SYS_RAWIO is granted automatically
# whenever a SATA/SAS device exists - it lets smartctl issue ATA passthrough
# ioctls and nothing else, and without it every SATA drive reports SMART as
# unavailable, which is not an agent anyone asked for.
printf 'n\n' >"$TMP/ans-decline"
NETRA_ANSWERS_FILE="$TMP/ans-decline"
export NETRA_ANSWERS_FILE
run_capture plan_smart
assert_contains "$RUN_OUT" "temperature" \
    "the SYS_ADMIN prompt states that NVMe temperature works without it"
assert_contains "$RUN_OUT" "hwmon" "the SYS_ADMIN prompt names hwmon as the temperature source"

NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_RAWIO" "SYS_RAWIO is granted without being asked about"
assert_eq 1 "$NETRA_ANSWER_INDEX" \
    "exactly one answer is consumed: SYS_RAWIO is no longer a prompt"
assert_eq 0 "$CAP_SYS_ADMIN" "declining leaves SYS_ADMIN off"
assert_eq "/dev/sda" "$SMART_DEVICES" \
    "a declined SYS_ADMIN also drops the NVMe controller from devices:"
assert_contains "$SKIPPED_NOTES" "SYS_ADMIN declined" "declining SYS_ADMIN is recorded as a skip"
assert_contains "$SKIPPED_NOTES" "temperature still works" \
    "the skip note says what declining does NOT cost"

# --- 7. NVMe present, SYS_ADMIN granted ---------------------------------------
NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
printf 'y\n' >"$TMP/ans-accept"
NETRA_ANSWERS_FILE="$TMP/ans-accept"
export NETRA_ANSWERS_FILE
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_SYS_ADMIN" "granting SYS_ADMIN sets the capability"
assert_contains "$SMART_DEVICES" "/dev/nvme0" "the NVMe CONTROLLER is emitted, not the namespace"
assert_not_contains "$SMART_DEVICES" "nvme0n1" "the NVMe namespace is never emitted"
build_cap_block
assert_contains "$NETRA_BLK_CAP_ADD" "SYS_ADMIN" "the cap block carries SYS_ADMIN"

# --- 8. USB-attached drives are excluded with a note --------------------------
#
# `-d sat` through a USB bridge is unreliable and can hang the enclosure, which
# would stall the whole scrape.
R="$TMP/r8"
mkdisk "$R" sda 1000 "pci0000:00/ata1/host0"
mkdisk "$R" sdc 3000 "pci0000:00/usb1/1-1/1-1:1.0/host4"
mkdev "$R" sda
mkdev "$R" sdc
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
# No NVMe on this host, so plan_smart asks nothing at all: an EMPTY answers file
# is what proves it.
: >"$TMP/ans-usb"
NETRA_ANSWERS_FILE="$TMP/ans-usb"
export NETRA_ANSWERS_FILE
plan_smart >/dev/null 2>&1
assert_eq "/dev/sda" "$SMART_DEVICES" "a USB-attached drive is left out of devices:"
assert_contains "$SKIPPED_NOTES" "sdc" "the excluded USB drive is named in the notes"
assert_contains "$SKIPPED_NOTES" "USB" "the note says it was excluded for being USB-attached"

# --- 9. a device node that does not exist is not emitted ----------------------
# A devices: entry for a missing node prevents the container from starting, so a
# probe miss must drop the entry rather than render it hopefully.
R="$TMP/r9"
mkdisk "$R" sda 1000 "pci0000:00/ata1/host0"
mkdir -p "$R/dev"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
: >"$TMP/ans-nodev"
NETRA_ANSWERS_FILE="$TMP/ans-nodev"
export NETRA_ANSWERS_FILE
plan_smart >/dev/null 2>&1
assert_eq "" "$SMART_DEVICES" "a device with no node in /dev is not emitted"
build_device_block
assert_eq "" "$NETRA_BLK_DEVICES" "an empty device list deletes the devices: key entirely"

# --- 10. --sys-admin grants without consuming a prompt ------------------------
#
# The discriminating assertion is NETRA_ANSWER_INDEX. An EMPTY answers file
# means a --sys-admin that still prompted would exhaust the file and die, so
# "index is 0" is the proof that the prompt was skipped rather than answered.
R="$TMP/r10"
mkdisk "$R" sda 1000 "pci0000:00/ata1/host0"
mkdisk "$R" nvme0n1 4000 "pci0000:00/nvme/nvme0"
mkdir -p "$R/sys/class/nvme/nvme0"
mkdev "$R" sda
mkdev "$R" nvme0
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths

GRANT_SYS_ADMIN=1
NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
: >"$TMP/ans-grantflag"
NETRA_ANSWERS_FILE="$TMP/ans-grantflag"
export NETRA_ANSWERS_FILE
run_capture plan_smart
assert_eq 0 "$RUN_RC" "--sys-admin consumes nothing from an empty answers file"
assert_contains "$RUN_OUT" "--sys-admin" "the output says the capability came from the flag"

# run_capture ran plan_smart in a command-substitution subshell, so none of its
# assignments survived. Re-run it in this shell for the state assertions.
NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_SYS_ADMIN" "--sys-admin grants SYS_ADMIN"
assert_eq 0 "$NETRA_ANSWER_INDEX" "--sys-admin consumes no answer: the prompt is skipped entirely"
assert_contains "$SMART_DEVICES" "/dev/nvme0" "--sys-admin also emits the NVMe controller"

# --- 11. --sys-admin on a SATA-only host is a no-op with a note ----------------
#
# Nothing on such a host can use SYS_ADMIN, so honouring the flag would expand
# privilege for no collected metric. It has to be visible in the finish report,
# not silently dropped: the operator asked for something they did not get.
R="$TMP/r11"
mkdisk "$R" sda 1000 "pci0000:00/ata1/host0"
mkdev "$R" sda
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths

GRANT_SYS_ADMIN=1
NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
: >"$TMP/ans-satagrant"
NETRA_ANSWERS_FILE="$TMP/ans-satagrant"
export NETRA_ANSWERS_FILE
plan_smart >/dev/null 2>&1
assert_eq 0 "$CAP_SYS_ADMIN" "--sys-admin grants nothing when there is no NVMe device"
assert_contains "$SKIPPED_NOTES" "--sys-admin" "the unhonoured flag is recorded as a skip"
assert_contains "$SKIPPED_NOTES" "no NVMe" "the note says why the flag was not honoured"
build_cap_block
assert_not_contains "$NETRA_BLK_CAP_ADD" "SYS_ADMIN" "the cap block still has no SYS_ADMIN"
GRANT_SYS_ADMIN=0

exit_case
