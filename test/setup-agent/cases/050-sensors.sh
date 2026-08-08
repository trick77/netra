#!/bin/sh
#
# hwmon enumeration and primary-sensor selection.
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

# No SATA devices in these fixtures unless a case sets it, so plan_drivetemp
# returns before it can prompt or shell out.
SMART_ATA_DEVICES=""
PRIMARY_SENSOR=""

# mkchip ROOT N NAME — a hwmon chip with a name file.
mkchip() {
    mkdir -p "$1/sys/class/hwmon/hwmon$2"
    printf '%s\n' "$3" >"$1/sys/class/hwmon/hwmon$2/name"
}

mktemp_sensor() {
    # mktemp_sensor ROOT N IDX VALUE [LABEL]
    _d="$1/sys/class/hwmon/hwmon$2"
    printf '%s\n' "$4" >"$_d/temp$3_input"
    if [ "$#" -ge 5 ]; then
        printf '%s\n' "$5" >"$_d/temp$3_label"
    fi
}

# --- 1. coretemp wins over a hotter drivetemp at a lower hwmon index ----------
#
# NEVER hottest-wins. drivetemp at 52C outranks coretemp at 41C on temperature
# and sits at hwmon0 so it also wins on index, yet the host's headline
# temperature must be the CPU: a NAS whose primary sensor is a spinning disk is
# a NAS whose CPU thermal problem is invisible.
R="$TMP/r1"
mkchip "$R" 0 drivetemp
mktemp_sensor "$R" 0 1 52000
mkchip "$R" 1 acpitz
mktemp_sensor "$R" 1 1 45000
mkchip "$R" 2 coretemp
mktemp_sensor "$R" 2 1 41000 "Package id 0"
mktemp_sensor "$R" 2 2 39000 "Core 0"

NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths

ROWS=$(hwmon_chips)
assert_contains "$ROWS" "hwmon0|drivetemp|temp1_input" \
    "a chip with no temp*_label falls back to the temp*_input basename"
assert_contains "$ROWS" "hwmon2|coretemp|Package id 0,Core 0" \
    "temp*_label values are collected in order"

assert_eq "coretemp" "$(pick_primary_sensor "$ROWS")" \
    "coretemp is preferred over a hotter drivetemp at a lower hwmon index"

# The preference list is walked IN ORDER, so acpitz only wins when nothing above
# it is present.
ROWS2="hwmon0|drivetemp|temp1_input
hwmon1|acpitz|temp1_input"
assert_eq "acpitz" "$(pick_primary_sensor "$ROWS2")" "acpitz wins when no CPU chip is present"

ROWS3="hwmon0|drivetemp|temp1_input
hwmon1|nct6797|temp1_input"
assert_eq "" "$(pick_primary_sensor "$ROWS3")" \
    "no known CPU chip yields no pick, rather than the hottest thing lying around"

ROWS4="hwmon0|k10temp|temp1_input
hwmon1|coretemp|temp1_input"
assert_eq "coretemp" "$(pick_primary_sensor "$ROWS4")" \
    "coretemp outranks k10temp regardless of hwmon order"

# --- 2. the name falls back to device/name ------------------------------------
R="$TMP/r2"
mkdir -p "$R/sys/class/hwmon/hwmon0/device"
printf 'nvme\n' >"$R/sys/class/hwmon/hwmon0/device/name"
printf '38000\n' >"$R/sys/class/hwmon/hwmon0/temp1_input"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
assert_contains "$(hwmon_chips)" "hwmon0|nvme|temp1_input" \
    "a chip with no name file falls back to device/name"

# --- 3. the sensor phase is INFORMATIONAL ------------------------------------
#
# The agent auto-selects at runtime with this same preference order. Freezing a
# setup-time guess into .env would outlive the CPU swap or kernel upgrade that
# invalidated it, so NETRA_PRIMARY_SENSOR stays unset unless the operator asked
# for it explicitly.
NETRA_SETUP_ROOT="$TMP/r1"
export NETRA_SETUP_ROOT
init_paths
PRIMARY_SENSOR=""
SKIPPED_NOTES=""
detect_sensors >/dev/null 2>&1
assert_eq "" "$PRIMARY_SENSOR" \
    "an unambiguous auto-pick is NOT written to .env"

PRIMARY_SENSOR="coretemp/Package id 0"
detect_sensors >/dev/null 2>&1
assert_eq "coretemp/Package id 0" "$PRIMARY_SENSOR" "--primary-sensor is preserved verbatim"

# Two equally-ranked chips of the same name: the only case where the operator
# has something to decide.
R="$TMP/r3"
mkchip "$R" 0 coretemp
mktemp_sensor "$R" 0 1 41000 "Package id 0"
mkchip "$R" 1 coretemp
mktemp_sensor "$R" 1 1 43000 "Package id 1"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
PRIMARY_SENSOR=""
SKIPPED_NOTES=""
NETRA_ANSWER_INDEX=0
: >"$TMP/ans-sensors"
NETRA_ANSWERS_FILE="$TMP/ans-sensors"
export NETRA_ANSWERS_FILE
detect_sensors >/dev/null 2>&1
# A tie is REPORTED, never prompted. Asking about it made the prompt sequence
# variable-length - a dual-socket box has two, a four-socket box four - and the
# answer was a guess frozen into .env that outlives the hardware justifying it.
# The empty answers file is the proof: a surviving prompt would die exhausted.
assert_eq "" "$PRIMARY_SENSOR" "a tie is left unpinned rather than guessed at"
assert_eq 0 "$NETRA_ANSWER_INDEX" "a tie consumes no answer, because it asks nothing"
assert_contains "$SKIPPED_NOTES" "equally-ranked" "the tie is reported as a note"
assert_contains "$SKIPPED_NOTES" "--primary-sensor" "the note names the flag that pins one"
unset NETRA_ANSWERS_FILE

# --- 4. a missing hwmon directory is a note, not an error ---------------------
R="$TMP/r4"
mkdir -p "$R/sys/class"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
SKIPPED_NOTES=""
PRIMARY_SENSOR=""
run_capture detect_sensors
assert_eq 0 "$RUN_RC" "a host with no /sys/class/hwmon is not an error"
assert_contains "$RUN_OUT" "does not exist" "the missing hwmon directory is reported"
assert_eq "" "$(hwmon_chips)" "hwmon_chips prints nothing when the directory is absent"

# --- 6. drivetemp: ask, load, VERIFY, and only then persist -------------------
#
# Per spec 6.2 only SATA drive temperature needs the SMART path, and SMART polls
# at 1h. With drivetemp loaded the same temperatures arrive through hwmon on the
# 60s sensor scrape, with no SYS_RAWIO and no devices: mapping.
#
# The verification is the point. `modprobe drivetemp` exits 0 on any kernel that
# ships the module but produces no hwmon chip at all when the controller or the
# drives do not report SCT temperature, so persisting on the strength of that
# exit status would enshrine a module that does nothing, forever.
mkshims "$TMP/shims"
NETRA_UID=0
export NETRA_UID

dt_root() {
    _dt_r="$TMP/$1"
    mkdir -p "$_dt_r/sys/class/hwmon" "$_dt_r/etc"
    printf '%s\n' "$_dt_r"
}

# 6a. loads and produces a chip -> persisted, and the sensor list sees it.
R=$(dt_root dt_ok)
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
SMART_ATA_DEVICES="sda"
SKIPPED_NOTES=""
NETRA_ANSWER_INDEX=0
NETRA_ANSWERS_FILE=$(answers dt-ok y)
export NETRA_ANSWERS_FILE
NETRA_SHIM_MODPROBE_HWMON="$R/sys/class/hwmon"
export NETRA_SHIM_MODPROBE_HWMON
: >"$NETRA_SHIM_LOG"
run_capture plan_drivetemp
assert_eq 0 "$RUN_RC" "a working drivetemp load succeeds"
plan_drivetemp >/dev/null 2>&1
assert_file_present "$R/etc/modules-load.d/drivetemp.conf" \
    "a load that produced a chip is persisted across reboots"
assert_eq "drivetemp" "$(cat "$R/etc/modules-load.d/drivetemp.conf")" \
    "the persisted file names the module and nothing else"
assert_contains "$(hwmon_chips)" "drivetemp" "the new chip is visible to the sensor scan"
assert_eq "" "$SKIPPED_NOTES" "a drivetemp that works is not a degradation"

# 6b. loads but produces nothing -> unloaded again, nothing persisted.
R=$(dt_root dt_nochip)
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
SMART_ATA_DEVICES="sda"
SKIPPED_NOTES=""
NETRA_ANSWER_INDEX=0
NETRA_ANSWERS_FILE=$(answers dt-nochip y)
export NETRA_ANSWERS_FILE
unset NETRA_SHIM_MODPROBE_HWMON
: >"$NETRA_SHIM_LOG"
plan_drivetemp >/dev/null 2>&1
assert_file_absent "$R/etc/modules-load.d/drivetemp.conf" \
    "a load that produced no chip persists nothing"
assert_contains "$(cat "$NETRA_SHIM_LOG")" "modprobe -r drivetemp" \
    "a useless module is unloaded again, leaving the host as it was found"
assert_contains "$SKIPPED_NOTES" "no hwmon chip" "the operator is told it did not work"

# 6c. modprobe fails outright -> warned, nothing persisted, no unload attempted.
R=$(dt_root dt_fail)
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
SMART_ATA_DEVICES="sda"
SKIPPED_NOTES=""
NETRA_ANSWER_INDEX=0
NETRA_ANSWERS_FILE=$(answers dt-fail y)
export NETRA_ANSWERS_FILE
NETRA_SHIM_MODPROBE_RC=1
export NETRA_SHIM_MODPROBE_RC
: >"$NETRA_SHIM_LOG"
plan_drivetemp >/dev/null 2>&1
assert_file_absent "$R/etc/modules-load.d/drivetemp.conf" \
    "a failed modprobe persists nothing"
assert_contains "$SKIPPED_NOTES" "no drivetemp module" "the failure names the cause"
assert_not_contains "$(cat "$NETRA_SHIM_LOG")" "modprobe -r" \
    "nothing is unloaded when nothing loaded"
unset NETRA_SHIM_MODPROBE_RC

# 6d. declining is not an error, and says how to do it later.
R=$(dt_root dt_no)
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
SMART_ATA_DEVICES="sda"
SKIPPED_NOTES=""
NETRA_ANSWER_INDEX=0
NETRA_ANSWERS_FILE=$(answers dt-no n)
export NETRA_ANSWERS_FILE
: >"$NETRA_SHIM_LOG"
plan_drivetemp >/dev/null 2>&1
assert_eq "" "$(cat "$NETRA_SHIM_LOG")" "declining calls modprobe not at all"
assert_contains "$SKIPPED_NOTES" "modules-load.d" "the note gives the command to do it later"

# 6e. not root -> the command is printed and modprobe is never called. The note
# covers only drivetemp: check_root says once, up front, what a non-root run
# costs generally, so repeating it here would be noise at the point of use.
R=$(dt_root dt_nonroot)
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
# Before init_paths, which is where P_UID is resolved.
NETRA_UID=1000
export NETRA_UID
init_paths
SMART_ATA_DEVICES="sda"
SKIPPED_NOTES=""
NETRA_ANSWER_INDEX=0
NETRA_ANSWERS_FILE=$(answers dt-nonroot)
export NETRA_ANSWERS_FILE
: >"$NETRA_SHIM_LOG"
plan_drivetemp >/dev/null 2>&1
assert_eq "" "$(cat "$NETRA_SHIM_LOG")" "a non-root run never calls modprobe"
assert_eq 0 "$NETRA_ANSWER_INDEX" "a non-root run does not ask a question it cannot act on"
assert_contains "$SKIPPED_NOTES" "modprobe drivetemp" "the note gives the command to run as root"
assert_contains "$(flatten "$SKIPPED_NOTES")" "needs root to load" \
    "and says why it could not"
NETRA_UID=0
export NETRA_UID

# 6f. no SATA devices at all -> nothing is offered, because nothing would use it.
R=$(dt_root dt_nosata)
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
SMART_ATA_DEVICES=""
SKIPPED_NOTES=""
NETRA_ANSWER_INDEX=0
: >"$NETRA_SHIM_LOG"
run_capture plan_drivetemp
assert_eq "" "$RUN_OUT" "an NVMe-only host is not offered drivetemp at all"
assert_eq "" "$SKIPPED_NOTES" "and it is not reported as a degradation either"

# 6g. already loaded -> nothing to do, and no second modprobe.
R=$(dt_root dt_already)
mkdir -p "$R/sys/class/hwmon/hwmon0"
printf 'drivetemp\n' >"$R/sys/class/hwmon/hwmon0/name"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
SMART_ATA_DEVICES="sda"
SKIPPED_NOTES=""
NETRA_ANSWER_INDEX=0
: >"$NETRA_SHIM_LOG"
run_capture plan_drivetemp
assert_contains "$RUN_OUT" "already loaded" "an already-loaded module says so"
assert_eq "" "$(cat "$NETRA_SHIM_LOG")" "and calls modprobe not at all"
unset NETRA_ANSWERS_FILE

exit_case
