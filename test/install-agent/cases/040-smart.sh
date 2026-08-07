#!/bin/sh
#
# Block device enumeration, transport classification, NVMe controller
# resolution, and the two SMART capability prompts.
# Many variables set here are read by the SOURCED installer, not by this file,
# so shellcheck cannot see the use. The directive is file-wide rather than
# repeated at a dozen assignments.
# shellcheck disable=SC2034
set -eu
# shellcheck source=/dev/null
. "$LIB"

INSTALLER="$REPO/install-agent.sh"

NETRA_SOURCED=1
export NETRA_SOURCED
# shellcheck source=/dev/null
. "$INSTALLER"

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

NETRA_INSTALL_ROOT="$R"
export NETRA_INSTALL_ROOT
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
NETRA_INSTALL_ROOT="$R"
export NETRA_INSTALL_ROOT
init_paths
assert_eq "usb" "$(device_transport sdc)" "a usb1 path component means USB"
assert_eq "nvme" "$(device_transport nvme0n1)" "an nvme path component means NVMe"
assert_eq "unknown" "$(device_transport nosuchdisk)" "a device that is not there is unknown"

# --- 3. the NETRA_INSTALL_ROOT strip, which is the whole ballgame -------------
#
# A fixture root under a directory called `usb1` matches the USB pattern if the
# prefix is not stripped before matching, and then EVERY device on the host is
# classified as USB — silently, and with the test suite agreeing. This fixture
# exists only to fail if the strip is ever removed.
R="$TMP/usb1/root"
mkdir -p "$R"
mkdisk "$R" sdd 5000 "pci0000:00/ata3/host2"
NETRA_INSTALL_ROOT="$R"
export NETRA_INSTALL_ROOT
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
NETRA_INSTALL_ROOT="$R"
export NETRA_INSTALL_ROOT
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
NETRA_INSTALL_ROOT="$R"
export NETRA_INSTALL_ROOT
init_paths

ASSUME_YES=1
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_RAWIO" "a SATA-only host gets SYS_RAWIO"
assert_eq 0 "$CAP_SYS_ADMIN" "a SATA-only host is never even asked about SYS_ADMIN"
assert_eq "/dev/sda" "$SMART_DEVICES" "the SATA device is emitted unprefixed"
assert_not_contains "$SMART_DEVICES" "$R" "NETRA_INSTALL_ROOT never leaks into devices:"

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
NETRA_INSTALL_ROOT="$R"
export NETRA_INSTALL_ROOT
init_paths

ASSUME_YES=0
SKIPPED_NOTES=""
NETRA_ANSWER_INDEX=0
# Prompt order inside plan_smart: SYS_RAWIO, then SYS_ADMIN.
printf 'y\nn\n' >"$TMP/ans-decline"
NETRA_ANSWERS_FILE="$TMP/ans-decline"
export NETRA_ANSWERS_FILE
run_capture plan_smart
assert_contains "$RUN_OUT" "temperature" \
    "the SYS_ADMIN prompt states that NVMe temperature works without it"
assert_contains "$RUN_OUT" "hwmon" "the SYS_ADMIN prompt names hwmon as the temperature source"

NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_RAWIO" "SYS_RAWIO accepted"
assert_eq 0 "$CAP_SYS_ADMIN" "declining leaves SYS_ADMIN off"
assert_eq "/dev/sda" "$SMART_DEVICES" \
    "a declined SYS_ADMIN also drops the NVMe controller from devices:"
assert_contains "$SKIPPED_NOTES" "SYS_ADMIN declined" "declining SYS_ADMIN is recorded as a skip"
assert_contains "$SKIPPED_NOTES" "temperature still works" \
    "the skip note says what declining does NOT cost"

# --- 7. NVMe present, SYS_ADMIN granted ---------------------------------------
NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
printf 'y\ny\n' >"$TMP/ans-accept"
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
NETRA_INSTALL_ROOT="$R"
export NETRA_INSTALL_ROOT
init_paths
NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
printf 'y\n' >"$TMP/ans-usb"
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
NETRA_INSTALL_ROOT="$R"
export NETRA_INSTALL_ROOT
init_paths
NETRA_ANSWER_INDEX=0
SKIPPED_NOTES=""
printf 'y\n' >"$TMP/ans-nodev"
NETRA_ANSWERS_FILE="$TMP/ans-nodev"
export NETRA_ANSWERS_FILE
plan_smart >/dev/null 2>&1
assert_eq "" "$SMART_DEVICES" "a device with no node in /dev is not emitted"
build_device_block
assert_eq "" "$NETRA_BLK_DEVICES" "an empty device list deletes the devices: key entirely"

exit_case
