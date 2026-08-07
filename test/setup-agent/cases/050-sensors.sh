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

DRY_RUN=1
ASSUME_YES=1
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
# The agent auto-selects at runtime with this same preference order. Freezing an
# install-time guess into .env would outlive the CPU swap or kernel upgrade that
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
ASSUME_YES=1
detect_sensors >/dev/null 2>&1
assert_eq "coretemp" "$PRIMARY_SENSOR" \
    "two equally-ranked chips of the same name prompt, and the answer is written"

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

exit_case
