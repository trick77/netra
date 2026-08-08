#!/bin/sh
#
# Virtualisation detection, the external tooling check, and message wrapping.
#
# All three exist because of the same field report: a Debian cloud VPS was
# offered SMART on a disk the hypervisor invented, offered a drivetemp module
# for drives that are not there, told to modprobe coretemp on a machine with no
# thermal hardware, and shown all of it as one unwrapped wall of text.
#
# Many variables set here are read by the SOURCED setup script, not by this file,
# so shellcheck cannot see the use. The directive is file-wide rather than
# repeated at a dozen assignments.
# shellcheck disable=SC2034
set -eu
# shellcheck source=/dev/null
. "$LIB"

SETUP="$REPO/setup-agent.sh"
TEMPLATES="$REPO/deploy/agent"
NO_TTY=/nonexistent/netra-tty

mkshims "$TMP/shims"

# $SH is a bare name ("sh"), so every restricted-PATH run below would fail to
# find the INTERPRETER rather than exercising the check under test. Resolve it
# once, and link it into each restricted bin alongside the commands being tested.
SH_ABS=$(command -v "$SH")

# NETRA_SOURCED=0 on every subprocess run below. This case SOURCES the script to
# unit-test _wrap, detect_virt and friends, and NETRA_SOURCED has to be exported
# for that — but a child process inheriting it reads "you are being sourced",
# defines its functions, and exits 0 having done nothing at all. Which is
# indistinguishable from a clean run if you only assert on the exit code.

# linkbin DIR CMD... — a PATH containing exactly these commands, plus the shell.
linkbin() {
    _lb_dir="$TMP/$1"
    shift
    mkdir -p "$_lb_dir"
    ln -sf "$SH_ABS" "$_lb_dir/sh"
    # The docker SHIM, not whatever docker this machine has - a developer laptop
    # without Docker would otherwise fail preflight and every assertion below it
    # would be about the wrong thing.
    ln -sf "$TMP/shims/bin/docker" "$_lb_dir/docker"
    for _lb_c in "$@"; do
        _lb_src=$(command -v "$_lb_c" 2>/dev/null || printf '')
        [ -z "$_lb_src" ] || ln -sf "$_lb_src" "$_lb_dir/$_lb_c"
    done
    printf '%s\n' "$_lb_dir"
}

NETRA_SOURCED=1
export NETRA_SOURCED
# shellcheck source=/dev/null
. "$SETUP"

# --- 1. _wrap folds without awk, and never mid-word ---------------------------
#
# Pure shell on purpose: wrapping is what every message passes through, so a
# host missing awk would fail while printing the message explaining what it was
# missing.
LONG="the quick brown fox jumps over the lazy dog and keeps going well past any reasonable terminal width to prove the fold happens"
WRAPPED=$(_wrap 40 '  ' "$LONG")
TESTS_RUN=$((TESTS_RUN + 1))
if [ "$(printf '%s\n' "$WRAPPED" | wc -l | tr -d ' ')" -gt 1 ]; then
    ok "_wrap folds a long line"
else
    fail "_wrap did not fold a long line"
fi
TESTS_RUN=$((TESTS_RUN + 1))
if [ "$(printf '%s\n' "$WRAPPED" | awk '{ if (length($0) > 40) c++ } END { print c + 0 }')" = 0 ]; then
    ok "no wrapped line exceeds the requested width"
else
    fail "a wrapped line exceeded the requested width"
fi
assert_eq "$LONG" "$(printf '%s' "$WRAPPED" | tr -s ' \n' ' ' | sed 's/ $//')" \
    "wrapping changes only whitespace, never a word"
assert_contains "$(printf '%s\n' "$WRAPPED" | sed -n 2p)" "  " \
    "continuation lines carry the indent"

# A message containing a glob must not be replaced by filenames. The unquoted
# expansion _wrap relies on would do exactly that without `set -f`.
assert_eq "hello * world ?" "$(_wrap 200 '' 'hello * world ?')" \
    "a glob character in a message is not expanded against the working directory"

# --- 2. detect_virt -----------------------------------------------------------
mkplatform() {
    _mp="$TMP/$1"
    mkdir -p "$_mp/proc" "$_mp/sys/class/dmi/id"
    printf '%s\n' "$_mp"
}

# The x86 signal: the hypervisor CPU flag.
R=$(mkplatform virt-flag)
printf 'flags\t\t: fpu vme de pse tsc msr hypervisor lahf_lm\n' >"$R/proc/cpuinfo"
printf 'QEMU\n' >"$R/sys/class/dmi/id/sys_vendor"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
assert_eq "QEMU" "$(detect_virt)" "the hypervisor CPU flag is detected, and DMI names it"

# Bare metal: the same file WITHOUT the flag, and a real vendor.
R=$(mkplatform phys)
printf 'flags\t\t: fpu vme de pse tsc msr lahf_lm\n' >"$R/proc/cpuinfo"
printf 'Supermicro\n' >"$R/sys/class/dmi/id/sys_vendor"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
assert_eq "" "$(detect_virt)" "a physical host is not reported as virtual"

# arm64 guests set no such flag, so DMI carries it alone.
R=$(mkplatform virt-dmi)
printf 'processor\t: 0\n' >"$R/proc/cpuinfo"
printf 'Amazon EC2\n' >"$R/sys/class/dmi/id/sys_vendor"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
assert_eq "Amazon EC2" "$(detect_virt)" "a known cloud vendor is detected without the CPU flag"

# Xen PV sets neither.
R=$(mkplatform virt-xen)
printf 'processor\t: 0\n' >"$R/proc/cpuinfo"
printf 'Bare Metal Inc\n' >"$R/sys/class/dmi/id/sys_vendor"
mkdir -p "$R/sys/hypervisor"
printf 'xen\n' >"$R/sys/hypervisor/type"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
assert_eq "xen" "$(detect_virt)" "Xen PV is detected through /sys/hypervisor/type"

# --- 3. SMART is skipped on a virtual host ------------------------------------
#
# The hypervisor presents an /dev/sda that looks exactly like a real one from
# sysfs, so the transport probe cannot tell the difference. Granting SYS_RAWIO
# and mapping a device for a metric that cannot exist is worse than collecting
# nothing.
R="$TMP/vm-smart"
mkdir -p "$R/sys/devices/pci0000:00/ata1/host0/block/sda/device" "$R/sys/block" "$R/dev"
printf '1000\n' >"$R/sys/devices/pci0000:00/ata1/host0/block/sda/size"
ln -sf "../devices/pci0000:00/ata1/host0/block/sda" "$R/sys/block/sda"
: >"$R/dev/sda"
NETRA_SETUP_ROOT="$R"
export NETRA_SETUP_ROOT
init_paths
VIRT="QEMU"
SKIPPED_NOTES=""
GRANT_SYS_ADMIN=0
run_capture plan_smart
assert_eq 0 "$RUN_RC" "a virtual host is not an error"
assert_contains "$RUN_OUT" "skipped on a virtual host" "the run says SMART was skipped and why"
plan_smart >/dev/null 2>&1
assert_eq 0 "$CAP_RAWIO" "no SYS_RAWIO is granted for a disk that has no SMART data"
assert_eq "" "$SMART_DEVICES" "no device is mapped either"
assert_eq "" "$SMART_ATA_DEVICES" "and drivetemp is therefore never offered"

# --sys-admin on a virtual host is an unhonoured request, which belongs in the
# report rather than being silently dropped.
SKIPPED_NOTES=""
GRANT_SYS_ADMIN=1
plan_smart >/dev/null 2>&1
assert_eq 0 "$CAP_SYS_ADMIN" "--sys-admin grants nothing on a virtual host"
assert_contains "$SKIPPED_NOTES" "--assume-physical" "the note names the override"
GRANT_SYS_ADMIN=0

# The same fixture WITHOUT the virtualisation verdict still finds the disk, so
# the assertions above are about VIRT and not about an empty fixture.
VIRT=""
SKIPPED_NOTES=""
plan_smart >/dev/null 2>&1
assert_eq 1 "$CAP_RAWIO" "the same host, detected as physical, does get SYS_RAWIO"
assert_eq "sda" "$SMART_ATA_DEVICES" "and does offer its SATA device"

# --- 4. the sensor hint does not tell a VM to modprobe coretemp ---------------
VIRT="QEMU"
SKIPPED_NOTES=""
run_capture _sensor_module_hint
assert_contains "$RUN_OUT" "normal on a virtual host" "a guest is told why there are no sensors"
assert_not_contains "$RUN_OUT" "modprobe" "and is not told to load a driver that cannot help"
assert_eq "" "$SKIPPED_NOTES" "missing sensors on a VM are not a degradation"

VIRT=""
SKIPPED_NOTES=""
run_capture _sensor_module_hint
assert_contains "$RUN_OUT" "coretemp" "physical hardware still gets the driver hint"
# Again in THIS shell: run_capture used a command substitution, so the
# SKIPPED_NOTES the subshell accumulated died with it.
SKIPPED_NOTES=""
_sensor_module_hint >/dev/null 2>&1
assert_contains "$(flatten "$SKIPPED_NOTES")" "no CPU temperature sensor" \
    "and it is recorded as a note"

# --- 5. warn_cmd keeps the command copy-pasteable -----------------------------
#
# A shell command folded into a paragraph cannot be copied, which is the only
# thing anyone wants to do with it.
SKIPPED_NOTES=""
run_capture warn_cmd "modprobe drivetemp && echo drivetemp > /etc/modules-load.d/drivetemp.conf" \
    "a long explanation that is comfortably wider than the wrap column so that it" \
    "definitely folds across more than one line of output"
CMDLINE=$(printf '%s\n' "$RUN_OUT" | grep 'modprobe drivetemp')
assert_eq "    modprobe drivetemp && echo drivetemp > /etc/modules-load.d/drivetemp.conf" \
    "$CMDLINE" "the command is emitted verbatim on its own line, unwrapped"
TESTS_RUN=$((TESTS_RUN + 1))
if [ "$(printf '%s\n' "$RUN_OUT" | wc -l | tr -d ' ')" -gt 2 ]; then
    ok "the explanation around it is wrapped"
else
    fail "the explanation was not wrapped"
fi

# --- 6. the external tooling check --------------------------------------------
#
# `curl … | sh` lands on hosts nobody has inspected. Discovering a missing `tr`
# halfway through detection produces "tr: not found" from inside a command
# substitution and a run that limps on with an empty variable.
ROOT="$TMP/toolroot"
mkdir -p "$ROOT"
cp -R "$(fixture root-full)/." "$ROOT/"

# Everything the check requires EXCEPT tr, so the failure has exactly one cause.
ONEBIN=$(linkbin onebin awk sed grep head cat sort wc mktemp mkdir rm cp id)
run_capture env PATH="$ONEBIN" NETRA_SOURCED=0 NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH_ABS" "$SETUP" --dry-run --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tools"
assert_eq 1 "$RUN_RC" "a host missing a required command exits non-zero"
assert_contains "$(flatten "$RUN_OUT")" "missing commands the setup script needs: tr" \
    "the failure names the command that is missing"

# ...and with tr restored the same run gets past the check, so the assertion
# above is about tr and not about the restricted PATH in general.
FULLBIN=$(linkbin fullbin awk sed grep tr head cat sort wc mktemp mkdir rm cp id)
run_capture env PATH="$FULLBIN" NETRA_SOURCED=0 NETRA_SETUP_ROOT="$ROOT" NETRA_TTY="$NO_TTY" \
    "$SH_ABS" "$SETUP" --dry-run --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tools2"
assert_eq 0 "$RUN_RC" "the same run succeeds once the missing command is there"

# --- 7. wget stands in for curl ------------------------------------------------
#
# Plenty of minimal images ship exactly one of the two.
assert_eq "curl" "$(_http_client)" "curl is preferred when both are present"

ROOT2="$TMP/wgetroot"
mkdir -p "$ROOT2"
cp -R "$(fixture root-full)/." "$ROOT2/"

NOCURL=$(linkbin nocurlbin awk sed grep tr head cat sort wc mktemp mkdir rm cp id)
cat >"$NOCURL/wget" <<'SHIM'
#!/bin/sh
printf 'wget %s\n' "$*" >>"$NETRA_SHIM_LOG"
printf '%s' "${NETRA_SHIM_CURL_BODY:-}"
exit "${NETRA_SHIM_CURL_RC:-0}"
SHIM
chmod +x "$NOCURL/wget"

: >"$NETRA_SHIM_LOG"
run_capture env PATH="$NOCURL" NETRA_SOURCED=0 NETRA_SETUP_ROOT="$ROOT2" NETRA_TTY="$NO_TTY" \
    NETRA_SHIM_CURL_BODY='{"tag_name":"v9.9.9"}' \
    "$SH_ABS" "$SETUP" --dry-run --token nta_x --hub-url https://h \
    --output-dir "$TMP/out-wget"
assert_contains "$(cat "$NETRA_SHIM_LOG")" "wget " "wget is used when curl is absent"
assert_not_contains "$(cat "$NETRA_SHIM_LOG")" "curl " "and curl is not reached for at all"

# Neither one, and no --template-dir, is a hard error naming both.
NONET=$(linkbin nonetbin awk sed grep tr head cat sort wc mktemp mkdir rm cp id)
run_capture env PATH="$NONET" NETRA_SOURCED=0 NETRA_SETUP_ROOT="$ROOT2" NETRA_TTY="$NO_TTY" \
    "$SH_ABS" "$SETUP" --dry-run --token nta_x --hub-url https://h \
    --output-dir "$TMP/out-nonet"
assert_eq 1 "$RUN_RC" "no HTTP client and no --template-dir is a hard error"
assert_contains "$(flatten "$RUN_OUT")" "neither curl nor wget" "the failure names both"

# ...but --template-dir needs no HTTP client at all.
run_capture env PATH="$NONET" NETRA_SOURCED=0 NETRA_SETUP_ROOT="$ROOT2" NETRA_TTY="$NO_TTY" \
    "$SH_ABS" "$SETUP" --dry-run --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-nonet2"
assert_eq 0 "$RUN_RC" "--template-dir works with no HTTP client on the host"

# --- 8. colour is off unless both streams are terminals ------------------------
#
# run_capture reads through a command substitution, so this is the real check a
# piped run gets.
assert_not_contains "$RUN_OUT" "$(printf '\033')" \
    "no escape sequences reach a non-terminal"

exit_case
