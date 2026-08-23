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

# There is no unattended mode, so every run below that is expected to reach the
# end drives its prompts through AGENT_ANSWERS_FILE. AGENT_UID is pinned on each
# of those runs too: plan_drivetemp asks nothing when the run is not root, so an
# unpinned uid would make the prompt COUNT depend on who runs the suite.
#
#   root-full, uid 0     -> SYS_ADMIN, drivetemp, write gate
#   root-full, uid 1000  -> SYS_ADMIN, write gate   (drivetemp returns early)
ANS_ROOT=$(answers plat-root n y y)
ANS_USER=$(answers plat-user n y)

# AGENT_SOURCED=0 on every subprocess run below. This case SOURCES the script to
# unit-test _wrap, detect_virt and friends, and AGENT_SOURCED has to be exported
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

AGENT_SOURCED=1
export AGENT_SOURCED
# shellcheck source=/dev/null
. "$SETUP"

# --- 1. _wrap folds without awk, and never mid-word ---------------------------
#
# Pure shell on purpose: wrapping is what every message passes through, so a
# host missing awk would fail while printing the message explaining what it was
# missing.
LONG="the quick brown fox jumps over the lazy dog and keeps going well past any reasonable terminal width to prove the fold happens"
WRAPPED=$(_wrap 40 '' '  ' "$LONG")
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
assert_eq "hello * world ?" "$(_wrap 200 '' '' 'hello * world ?')" \
    "a glob character in a message is not expanded against the working directory"

# The prefix belongs to the FIRST line and the indent to the rest. Passing the
# bullet inside the text destroyed it: the word split strips leading whitespace,
# so "    - note" came out at column 0 under continuation lines indented six.
BULLET=$(_wrap 30 '    - ' '      ' "a note long enough that it has to fold at least once here")
assert_eq "    - a note long enough that" "$(printf '%s\n' "$BULLET" | sed -n 1p)" \
    "the prefix survives on the first line"
assert_contains "$(printf '%s\n' "$BULLET" | sed -n 2p)" "      " \
    "and continuation lines carry the indent instead"

# The indent counts toward the width, or every continuation line overruns by
# exactly the indent.
WIDE=$(_wrap 20 '' '          ' "alpha bravo charlie delta echo foxtrot golf hotel")
TESTS_RUN=$((TESTS_RUN + 1))
if [ "$(printf '%s\n' "$WIDE" | awk '{ if (length($0) > 20) c++ } END { print c + 0 }')" = 0 ]; then
    ok "a wide indent is counted toward the width, not added on top of it"
else
    fail "a wide indent overran the requested width"
fi

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
AGENT_SETUP_ROOT="$R"
export AGENT_SETUP_ROOT
init_paths
assert_eq "QEMU" "$(detect_virt)" "the hypervisor CPU flag is detected, and DMI names it"

# Bare metal: the same file WITHOUT the flag, and a real vendor.
R=$(mkplatform phys)
printf 'flags\t\t: fpu vme de pse tsc msr lahf_lm\n' >"$R/proc/cpuinfo"
printf 'Supermicro\n' >"$R/sys/class/dmi/id/sys_vendor"
AGENT_SETUP_ROOT="$R"
export AGENT_SETUP_ROOT
init_paths
assert_eq "" "$(detect_virt)" "a physical host is not reported as virtual"

# No CPU flag: DMI carries it alone, and only for a hypervisor PRODUCT.
R=$(mkplatform virt-dmi)
printf 'processor\t: 0\n' >"$R/proc/cpuinfo"
printf 'QEMU\n' >"$R/sys/class/dmi/id/sys_vendor"
AGENT_SETUP_ROOT="$R"
export AGENT_SETUP_ROOT
init_paths
assert_eq "QEMU" "$(detect_virt)" "a hypervisor product in DMI is detected without the CPU flag"

# A cloud's BRAND NAME is not a hypervisor. This branch is reached exactly when
# the CPU flag is absent - on genuine bare metal - so a match here is always
# wrong, and every one of these companies also ships physical machines:
# sys_vendor on a Chromebox is "Google", on a Surface "Microsoft Corporation".
# The cost is not symmetric either: a missed guest is offered SMART that reads
# nothing, while a misjudged physical host loses SMART, drive temperatures and
# the sensor hint on hardware that has them.
for v in Google "Microsoft Corporation" "Amazon EC2" Hetzner Scaleway DigitalOcean; do
    R=$(mkplatform "virt-brand")
    printf 'processor\t: 0\n' >"$R/proc/cpuinfo"
    printf '%s\n' "$v" >"$R/sys/class/dmi/id/sys_vendor"
    AGENT_SETUP_ROOT="$R"
    export AGENT_SETUP_ROOT
    init_paths
    assert_eq "" "$(detect_virt)" "'$v' alone is a brand name, not a hypervisor"
done

# Xen PV sets neither.
R=$(mkplatform virt-xen)
printf 'processor\t: 0\n' >"$R/proc/cpuinfo"
printf 'Bare Metal Inc\n' >"$R/sys/class/dmi/id/sys_vendor"
mkdir -p "$R/sys/hypervisor"
printf 'xen\n' >"$R/sys/hypervisor/type"
AGENT_SETUP_ROOT="$R"
export AGENT_SETUP_ROOT
init_paths
assert_eq "xen" "$(detect_virt)" "Xen PV is detected through /sys/hypervisor/type"

# ...but a Xen dom0 is REAL HARDWARE reading `xen` from the same file. The CPU
# flag is absent on dom0 and sys_vendor is the real board vendor, so both
# earlier branches fall through and the type file alone would misjudge a machine
# with real disks — losing SMART, drive temperatures and the sensor hint on
# hardware that has all of them. `control_d` in the capabilities file is what
# systemd-detect-virt discriminates on.
R=$(mkplatform virt-dom0)
printf 'processor\t: 0\n' >"$R/proc/cpuinfo"
printf 'Supermicro\n' >"$R/sys/class/dmi/id/sys_vendor"
mkdir -p "$R/sys/hypervisor/properties"
printf 'xen\n' >"$R/sys/hypervisor/type"
printf 'xen-3.0-x86_64 xen-3.0-x86_32p hvm-3.0-x86_32 control_d\n' \
    >"$R/sys/hypervisor/properties/capabilities"
AGENT_SETUP_ROOT="$R"
export AGENT_SETUP_ROOT
init_paths
assert_eq "" "$(detect_virt)" "a Xen dom0 is physical, not a guest"

# The same tree WITHOUT control_d is a Xen HVM/PV guest and stays virtual, so
# the assertion above is about dom0 and not about the capabilities file merely
# existing.
printf 'xen-3.0-x86_64 xen-3.0-x86_32p hvm-3.0-x86_32\n' \
    >"$R/sys/hypervisor/properties/capabilities"
init_paths
assert_eq "xen" "$(detect_virt)" "a Xen guest with capabilities but no control_d is virtual"

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
AGENT_SETUP_ROOT="$R"
export AGENT_SETUP_ROOT
init_paths
VIRT="QEMU"
SKIPPED_NOTES=""
GRANT_SYS_ADMIN=0
run_capture plan_smart
assert_eq 0 "$RUN_RC" "a virtual host is not an error"
assert_contains "$RUN_OUT" "skipped on a virtual host" "the run says SMART was skipped and why"
SKIPPED_NOTES=""
plan_smart >/dev/null 2>&1
assert_eq 0 "$CAP_RAWIO" "no SYS_RAWIO is granted for a disk that has no SMART data"
# SKIPPED_NOTES is "everything the agent will NOT collect", and this withholds
# every SMART metric and every SATA drive temperature. An operator whose host
# was misjudged has to find that in the finish report without re-reading the
# scroll - which means warn, not info.
assert_contains "$(flatten "$SKIPPED_NOTES")" "SMART is skipped" \
    "the skip reaches the finish report"
assert_contains "$(flatten "$SKIPPED_NOTES")" "--assume-physical" \
    "and names the flag that overrides the detection"
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
assert_contains "$(flatten "$RUN_OUT")" "normal on a virtual host" \
    "a guest is told why there is no CPU sensor"
# It must not claim there is no thermal hardware at all: the third call site is
# reached with a populated chip list that simply holds nothing the agent
# recognises as a CPU, and telling that operator "no sensors" contradicts the
# list they are looking at.
assert_contains "$RUN_OUT" "no CPU temperature sensor" "and told precisely what is missing"
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
run_capture env PATH="$ONEBIN" AGENT_SOURCED=0 AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    "$SH_ABS" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tools"
assert_eq 1 "$RUN_RC" "a host missing a required command exits non-zero"
assert_contains "$(flatten "$RUN_OUT")" "missing commands the setup script needs: tr" \
    "the failure names the command that is missing"

# `id` is the one that proved the check was running too late: init_paths
# resolved $P_UID with `id -u` BEFORE check_tools, so a host without it died
# with "line 745: id: command not found" and never reached the check whose
# whole job is to name it.
NOID=$(linkbin noid awk sed grep tr head cat sort wc mktemp mkdir rm cp)
run_capture env PATH="$NOID" AGENT_SOURCED=0 AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    "$SH_ABS" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-noid"
assert_eq 1 "$RUN_RC" "a host without id exits non-zero"
assert_contains "$(flatten "$RUN_OUT")" "missing commands the setup script needs: id" \
    "and is told which command, rather than being shown a line number"
assert_not_contains "$RUN_OUT" "id: command not found" \
    "the check runs before anything reaches for id"

# `sed` is the one that proved die itself could be the casualty. die used to
# colour its label by piping _wrap through sed; under `set -e` a missing sed
# failed the pipeline and aborted die BEFORE `exit 1`, so the host got
# "sed: not found" and exit 127 instead of the message naming what to install.
# Every other command in the list is reported BY die, so die has to survive the
# absence of each of them.
NOSED=$(linkbin nosed awk grep tr head cat sort wc mktemp mkdir rm cp id)
run_capture env PATH="$NOSED" AGENT_SOURCED=0 AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    "$SH_ABS" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-nosed"
assert_eq 1 "$RUN_RC" "a host without sed exits 1, not 127 from a broken die"
assert_contains "$(flatten "$RUN_OUT")" "missing commands the setup script needs: sed" \
    "die still delivers its message with no sed on the host"
assert_not_contains "$RUN_OUT" "sed: command not found" \
    "die does not shell out to sed to print an error about sed"

# ...and with tr restored the same run gets past the check, so the assertion
# above is about tr and not about the restricted PATH in general.
FULLBIN=$(linkbin fullbin awk sed grep tr head cat sort wc mktemp mkdir rm cp id)
run_capture env PATH="$FULLBIN" AGENT_SOURCED=0 AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_UID=1000 AGENT_ANSWERS_FILE="$ANS_USER" \
    "$SH_ABS" "$SETUP" --token nta_x --hub-url https://h \
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
printf 'wget %s\n' "$*" >>"$AGENT_SHIM_LOG"
printf '%s' "${AGENT_SHIM_CURL_BODY:-}"
exit "${AGENT_SHIM_CURL_RC:-0}"
SHIM
chmod +x "$NOCURL/wget"

: >"$AGENT_SHIM_LOG"
run_capture env PATH="$NOCURL" AGENT_SOURCED=0 AGENT_SETUP_ROOT="$ROOT2" AGENT_TTY="$NO_TTY" \
    AGENT_SHIM_CURL_BODY='{"tag_name":"v9.9.9"}' \
    AGENT_UID=1000 AGENT_ANSWERS_FILE="$ANS_USER" \
    "$SH_ABS" "$SETUP" --token nta_x --hub-url https://h \
    --output-dir "$TMP/out-wget"
assert_contains "$(cat "$AGENT_SHIM_LOG")" "wget " "wget is used when curl is absent"
assert_not_contains "$(cat "$AGENT_SHIM_LOG")" "curl " "and curl is not reached for at all"

# Neither one, and no --template-dir, is a hard error naming both.
NONET=$(linkbin nonetbin awk sed grep tr head cat sort wc mktemp mkdir rm cp id)
run_capture env PATH="$NONET" AGENT_SOURCED=0 AGENT_SETUP_ROOT="$ROOT2" AGENT_TTY="$NO_TTY" \
    "$SH_ABS" "$SETUP" --token nta_x --hub-url https://h \
    --output-dir "$TMP/out-nonet"
assert_eq 1 "$RUN_RC" "no HTTP client and no --template-dir is a hard error"
assert_contains "$(flatten "$RUN_OUT")" "neither curl nor wget" "the failure names both"

# ...but --template-dir needs no HTTP client at all.
run_capture env PATH="$NONET" AGENT_SOURCED=0 AGENT_SETUP_ROOT="$ROOT2" AGENT_TTY="$NO_TTY" \
    AGENT_UID=1000 AGENT_ANSWERS_FILE="$ANS_USER" \
    "$SH_ABS" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-nonet2"
assert_eq 0 "$RUN_RC" "--template-dir works with no HTTP client on the host"

# --- 7b. the root check states the fact once, and refuses nothing --------------
#
# Root was noticed only inside plan_drivetemp before, halfway through a run that
# had already promised things it could not do. It is not a hard failure and not
# a list of consequences either: detect_filesystems already skips each mount
# point it cannot write and names it, plan_drivetemp already says the module
# cannot be loaded, and check_docker has already proved this user reaches the
# daemon. What was missing was the plain fact, stated before any question.
ROOT3="$TMP/rootcheck"
mkdir -p "$ROOT3"
cp -R "$(fixture root-full)/." "$ROOT3/"

run_capture env AGENT_SETUP_ROOT="$ROOT3" AGENT_UID=0 AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_ROOT" \
    AGENT_SOURCED=0 "$SH_ABS" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-root"
assert_eq 0 "$RUN_RC" "a root run succeeds"
assert_contains "$RUN_OUT" "user:            root" "a root run says so"
assert_not_contains "$RUN_OUT" "not running as root" "and warns about nothing"

run_capture env AGENT_SETUP_ROOT="$ROOT3" AGENT_UID=1000 AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_USER" \
    AGENT_SOURCED=0 "$SH_ABS" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-root2"
assert_eq 0 "$RUN_RC" "a non-root run is not refused"
assert_contains "$RUN_OUT" "uid 1000 (not root)" "the run says which uid it is"
FLAT=$(flatten "$RUN_OUT")
assert_contains "$FLAT" "not running as root" "and says so before any question is asked"
assert_contains "$FLAT" "Docker is fine" \
    "docker is excluded, because the daemon has already answered"
# Once in the scroll, and once more in the finish report - which is warn's whole
# contract, not a duplicate. Every consequence stays reported where it happens;
# repeating the list here would bury the individual notes rather than help.
SCROLL=$(printf '%s\n' "$RUN_OUT" | sed -n '1,/==> Summary/p')
assert_eq 1 "$(printf '%s\n' "$SCROLL" | grep -c 'not running as root' || true)" \
    "the root fact is stated once in the scroll"
REPORT=$(printf '%s\n' "$RUN_OUT" | sed -n '/Skipped or degraded/,$p')
assert_contains "$(flatten "$REPORT")" "not running as root" \
    "and repeated in the finish report, like every other note"
# Before the first question, so an operator can stop and re-run with sudo rather
# than find out after answering.
ROOT_LINE=$(printf '%s\n' "$RUN_OUT" | grep -n 'not running as root' | head -n 1 | cut -d: -f1)
FIRST_Q=$(printf '%s\n' "$RUN_OUT" | grep -n '\[Y/n\]\|\[y/N\]' | head -n 1 | cut -d: -f1)
TESTS_RUN=$((TESTS_RUN + 1))
if [ "$ROOT_LINE" -lt "$FIRST_Q" ]; then
    ok "the root note comes before the first prompt"
else
    fail "the root note came after a prompt had already been asked"
fi

# --- 8. colour is off unless both streams are terminals ------------------------
#
# run_capture reads through a command substitution, so this is the real check a
# piped run gets.
assert_not_contains "$RUN_OUT" "$(printf '\033')" \
    "no escape sequences reach a non-terminal"

exit_case
