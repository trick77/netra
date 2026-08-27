#!/bin/sh
#
# The consent model: declining the write gate mutates nothing, .env is protected
# by --force, compose.yaml is not, a run with no terminal fails loudly, and a
# repeated run is byte-identical.
#
# There is no unattended mode. Every run here drives its prompts through
# AGENT_ANSWERS_FILE, whose contents are the PROMPT ORDER contract from the
# setup script header written down:
#
#   unsupported OS (unknown distro only) -> SYS_ADMIN (NVMe only)
#     -> drivetemp (SATA and no such chip) -> the write gate
#
# On the root-full fixture that is three: SYS_ADMIN, drivetemp, gate. pid: host
# is NOT among them - it is --pid-host or nothing, because the question read as
# though host CPU and memory were optional, and they are the product.
set -eu
# shellcheck source=/dev/null
. "$LIB"

SETUP="$REPO/setup-agent.sh"
TEMPLATES="$REPO/deploy/agent"
NO_TTY=/nonexistent/netra-tty

mkshims "$TMP/shims"

# plan_drivetemp's root branch, deterministically, on a laptop and a CI runner
# alike. Without it the prompt count would depend on who is running the suite.
AGENT_UID=0
export AGENT_UID

# The three defaults on this fixture: SYS_ADMIN no, drivetemp yes, write gate
# yes. Named rather than repeated so a change to the prompt sequence is one
# edit, not fourteen.
ANS_DEFAULT=$(answers default y n y y)

mkroot() {
    _mkroot_dst="$TMP/$1"
    mkdir -p "$_mkroot_dst"
    cp -R "$(fixture root-full)/." "$_mkroot_dst/"
    printf '%s\n' "$_mkroot_dst"
}

# --- 1. declining the write gate creates nothing and starts nothing ------------
#
# The gate is the whole consent model: "no" must leave the host exactly as it
# was, even with --start on the command line. Every prompt before the gate is
# answered NO here as well, so this asserts the one path on which the script
# is guaranteed to have touched nothing at all.
ROOT=$(mkroot declined)
CWD="$TMP/emptycwd"
mkdir -p "$CWD"
: >"$AGENT_SHIM_LOG"
ANS_DECLINE=$(answers decline y n n n)

# A subshell so the case's own working directory is not disturbed. OUTPUT_DIR
# defaults to ./netra-agent, so a declined run that leaked would leave it here.
if RUN_OUT=$(cd "$CWD" && env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DECLINE" \
    "$SH" "$SETUP" --start --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" 2>&1); then
    RUN_RC=0
else
    RUN_RC=$?
fi
assert_eq 0 "$RUN_RC" "declining the gate exits 0 — it is an answer, not an error"
assert_eq "" "$(ls -A "$CWD")" "declining creates nothing in the working directory"
assert_file_absent "$CWD/netra-agent" "the default output directory is not created"
assert_file_absent "$ROOT/.netra" "no marker file is created when the gate is declined"
assert_file_absent "$ROOT/mnt/ark/.netra" "and none on the other filesystem either"
assert_contains "$RUN_OUT" "Nothing was written" "the run says plainly that it wrote nothing"
assert_contains "$RUN_OUT" "Installs nothing" \
    "the banner says up front that this installs nothing"
assert_contains "$RUN_OUT" "compose.yaml   generated" \
    "the banner names the files it would write"

# Every mutation goes through netra_exec, so the docker shim must have been
# called for preflight probes and NOTHING else. Asserting "the log is empty"
# would be wrong: preflight legitimately runs `docker info` and
# `docker compose version` before any consent is needed, since a host with no
# reachable daemon has nothing to consent to.
SHIMLOG=$(cat "$AGENT_SHIM_LOG")
assert_not_contains "$SHIMLOG" "up -d" \
    "--start never reaches the stack when the gate was declined"
assert_not_contains "$SHIMLOG" "modprobe" "a declined run loads no kernel module either"
assert_eq "" "$(printf '%s\n' "$SHIMLOG" | grep -v '^docker info$' |
    grep -v '^docker compose version$' | grep -v '^$' || true)" \
    "the only docker calls made are the preflight probes"

# --- 2. a real run writes both files ------------------------------------------
ROOT=$(mkroot real)
OUT="$TMP/out"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_first --hub-url https://first.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "the first real run succeeds"
assert_file_present "$OUT/compose.yaml" "compose.yaml is written"
assert_file_present "$OUT/.env" ".env is written"
# The token reaches .env and nothing else. The plan shows **** and no phase
# echoes the value, so a terminal scrollback or a CI log never carries it.
assert_not_contains "$RUN_OUT" "nta_first" "the token value is never printed, only ****"
assert_contains "$RUN_OUT" "****" "the plan shows the token as ****"
cp "$OUT/.env" "$TMP/env.first"
cp "$OUT/compose.yaml" "$TMP/compose.first"

# --- 3. re-running without --force leaves .env byte-identical ------------------
#
# §12a: a re-run inside a provisioning script must not be able to silently
# replace a working token. compose.yaml has no such protection because it is
# derived — every byte of it comes from this run's detection.
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a re-run without --force still succeeds"
assert_contains "$RUN_OUT" "--force" "the refusal names the flag that would allow it"
TESTS_RUN=$((TESTS_RUN + 1))
if cmp -s "$TMP/env.first" "$OUT/.env"; then
    ok ".env is byte-identical after a re-run without --force"
else
    fail ".env was overwritten without --force"
fi
assert_not_contains "$(cat "$OUT/.env")" "nta_second" "the second token never reached .env"
assert_contains "$(cat "$OUT/.env")" "nta_first" "the original token is still there"

# compose.yaml IS overwritten, and the finish output has to say so or an
# operator with hand-edits is surprised by their loss.
assert_contains "$RUN_OUT" "overwritten" "the finish report states the compose/.env asymmetry"
assert_contains "$RUN_OUT" "up -d" "the finish report gives the command that applies the change"

# --- 3b. a re-run repairs AGENT_FS_MOUNTS without --force ----------------------
#
# The one derived line in .env. An agent installed before it existed measures
# its filesystems through /netra/fs/<label> bind mounts and, with nothing to map
# those labels back, reports the container's own paths as the filesystem names —
# "/netra/fs/ark is 94 % full" for a host with no netra anywhere on it.
#
# Fixing that must not cost the operator their token. --force replaces the whole
# file, so requiring it here would mean re-supplying a token to correct a label.
grep -v '^AGENT_FS_MOUNTS=' "$OUT/.env" >"$TMP/env.nofs"
cp "$TMP/env.nofs" "$OUT/.env"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a re-run against an .env with no AGENT_FS_MOUNTS succeeds"
REPAIRED=$(cat "$OUT/.env")
assert_contains "$REPAIRED" "AGENT_FS_MOUNTS=root=/" \
    "the re-run adds the label-to-mountpoint mapping without --force"
assert_contains "$REPAIRED" "nta_first" "and the existing token survives the repair"
assert_not_contains "$REPAIRED" "nta_second" "the re-run still cannot replace the token"
assert_contains "$RUN_OUT" "AGENT_FS_MOUNTS updated" \
    "the change is stated rather than made silently"

# A stale value is corrected, not appended to.
sed 's|^AGENT_FS_MOUNTS=.*|AGENT_FS_MOUNTS=root=/wrong|' "$OUT/.env" >"$TMP/env.stale"
cp "$TMP/env.stale" "$OUT/.env"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a re-run against a stale AGENT_FS_MOUNTS succeeds"
assert_not_contains "$(cat "$OUT/.env")" "root=/wrong" "the stale mapping is replaced"
assert_eq 1 "$(grep -c '^AGENT_FS_MOUNTS=' "$OUT/.env")" \
    "there is exactly one AGENT_FS_MOUNTS line, not one per run"

# --- 3c. a re-run repairs AGENT_PID_HOST without --force ----------------------
#
# THE upgrade path, and the one that fails worst if left alone. A host set up
# before `pid: host` became unconditional has AGENT_PID_HOST=0 in .env. This run
# rewrites compose.yaml -- which now always grants the namespace -- but would
# leave .env saying it does not have it.
#
# The agent believes .env over the kernel: told there is no host PID namespace,
# containers.go refuses to resolve the host pids in cgroup.procs at all, because
# any that DO resolve resolve to the wrong process. So the host would report zero
# per-container traffic forever, and the message it shows says to re-run this
# script -- the thing that just failed to fix it.
sed 's|^AGENT_PID_HOST=.*|AGENT_PID_HOST=0|' "$OUT/.env" >"$TMP/env.oldpid"
cp "$TMP/env.oldpid" "$OUT/.env"
assert_contains "$(cat "$OUT/.env")" "AGENT_PID_HOST=0" "the fixture is an .env from before the change"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a re-run against an .env from before pid: host succeeds"
PIDREPAIRED=$(cat "$OUT/.env")
assert_contains "$PIDREPAIRED" "AGENT_PID_HOST=1" \
    "the re-run corrects AGENT_PID_HOST to match the compose it just wrote"
assert_not_contains "$PIDREPAIRED" "AGENT_PID_HOST=0" "the stale value is replaced, not appended to"
assert_eq 1 "$(grep -c '^AGENT_PID_HOST=' "$OUT/.env")" \
    "there is exactly one AGENT_PID_HOST line, not one per run"
assert_contains "$PIDREPAIRED" "nta_first" "and the existing token survives this repair too"
assert_contains "$RUN_OUT" "AGENT_PID_HOST corrected" \
    "the change is stated rather than made silently"

# An .env that has no AGENT_PID_HOST line at all -- older still -- gains one.
grep -v '^AGENT_PID_HOST=' "$OUT/.env" >"$TMP/env.nopid"
cp "$TMP/env.nopid" "$OUT/.env"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a re-run against an .env with no AGENT_PID_HOST succeeds"
assert_contains "$(cat "$OUT/.env")" "AGENT_PID_HOST=1" "the missing line is added"

cp "$OUT/.env" "$TMP/env.first"

# --- 4. --force overwrites .env ------------------------------------------------
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a --force run succeeds"
assert_contains "$(cat "$OUT/.env")" "nta_second" "--force replaces the token"
assert_contains "$(cat "$OUT/.env")" "https://second.example" "--force replaces the hub URL"

# --- 5. idempotency: a second identical --force run changes nothing ------------
cp "$OUT/.env" "$TMP/env.force1"
cp "$OUT/compose.yaml" "$TMP/compose.force1"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "the repeated --force run succeeds"
TESTS_RUN=$((TESTS_RUN + 1))
if cmp -s "$TMP/env.force1" "$OUT/.env"; then
    ok "a repeated --force run produces a byte-identical .env"
else
    fail "a repeated --force run produced a different .env"
fi
TESTS_RUN=$((TESTS_RUN + 1))
if cmp -s "$TMP/compose.force1" "$OUT/compose.yaml"; then
    ok "a repeated run produces a byte-identical compose.yaml"
else
    fail "a repeated run produced a different compose.yaml"
fi

# --- 6. no terminal is an explicit failure ------------------------------------
#
# Never a silent default: a run that quietly declined pid: host would produce an
# agent collecting less than the operator believes. There is no unattended mode
# to fall back to, so the refusal has to name what to use instead.
ROOT=$(mkroot notty)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-notty"
assert_eq 1 "$RUN_RC" "no terminal exits non-zero"
assert_contains "$RUN_OUT" "no terminal" "the failure is explicit about the terminal"
assert_contains "$RUN_OUT" "no unattended mode" "the failure says there is no unattended mode"
assert_contains "$RUN_OUT" ".env.example" "the failure names the fleet alternative"
assert_file_absent "$TMP/out-notty/compose.yaml" "nothing is written when consent cannot be obtained"

# --- 7. declining the write gate leaves the host exactly as it was ------------
#
# This is the ordering bug the single gate exists to prevent. The output
# directory and every .netra marker used to be created BEFORE the
# write prompts, so declining left a littered host, possibly a .env, and a
# finish report cheerfully printing `cd <dir> && docker compose up -d`.
ROOT=$(mkroot decline)
ANS=$(answers decline y n n n n)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" "$SH" "$SETUP" \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-decline"
assert_eq 0 "$RUN_RC" "declining every prompt is not an error"
assert_file_absent "$TMP/out-decline/compose.yaml" "a declined compose.yaml is not written"
assert_file_absent "$TMP/out-decline/.env" "a declined .env is not written"
assert_file_absent "$TMP/out-decline" "the output directory itself is not created either"
assert_file_absent "$ROOT/.netra" "no marker file is created at the filesystem root"
assert_file_absent "$ROOT/mnt/ark/.netra" "and none on the other measured filesystem"
assert_contains "$RUN_OUT" "Nothing was written" "the run says plainly that it wrote nothing"
# Only sayable because drivetemp was declined too (answer 2 above). The claim is
# conditional on purpose — see the drivetemp case below.
assert_contains "$RUN_OUT" "Nothing was changed" "the run says plainly that it changed nothing"
assert_contains "$RUN_OUT" "Skipped or degraded" "everything declined is reported"

# --- 7b. "nothing was changed" is CONDITIONAL, and drivetemp is the condition --
#
# plan_drivetemp is the one mutation that happens before the gate, because the
# sensor scan has to see its result. Declining the gate afterwards must not
# claim the host is untouched — the operator would have no idea what to undo.
#
# Three shapes, and the remedy has to match the one that actually happened:
#   a) loaded and persisted        -> unload AND remove the conf file
#   b) loaded, unload failed       -> unload only; no file was ever written
#   c) loaded, unloaded cleanly    -> genuinely nothing changed

# (a) drivetemp accepted, works, persisted, THEN the gate is declined.
ROOT=$(mkroot declinedt)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers declinedt y n y n)" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    AGENT_SHIM_MODPROBE_HWMON="$ROOT/sys/class/hwmon" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-declinedt"
assert_eq 0 "$RUN_RC" "declining the gate after drivetemp was loaded is not an error"
assert_file_absent "$TMP/out-declinedt/compose.yaml" "still nothing written"
assert_not_contains "$RUN_OUT" "Nothing was changed" \
    "the run must NOT claim it changed nothing after loading a kernel module"
assert_contains "$(flatten "$RUN_OUT")" "modprobe -r drivetemp && rm /etc/modules-load.d/drivetemp.conf" \
    "the undo names both the module and the file that was persisted"

# (b) drivetemp loads but reports no chip, and the unload FAILS — so the module
# is still loaded and no file was ever written. The `|| true` on that unload
# exists precisely because this can happen.
ROOT=$(mkroot declinedtstuck)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers declinedtstuck n y n)" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    AGENT_SHIM_MODPROBE_R_RC=1 \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-declinedtstuck"
assert_eq 0 "$RUN_RC" "a drivetemp module that will not unload is not a fatal error"
assert_contains "$RUN_OUT" "STILL LOADED" "the run says the module is still loaded"
assert_not_contains "$RUN_OUT" "Nothing was changed" \
    "a module left loaded is a change, and must not be reported as none"
# No conf file was written on this path, so the remedy must not tell the
# operator to rm one — a failing rm reads as though the undo itself is broken.
assert_not_contains "$RUN_OUT" "rm /etc/modules-load.d/drivetemp.conf" \
    "the undo does not name a file that was never written"
assert_file_absent "$ROOT/etc/modules-load.d/drivetemp.conf" "and indeed none was"

# (c) drivetemp declined outright: the host really is untouched, and the run is
# allowed to say so. This is the control for both cases above.
ROOT=$(mkroot declinedtno)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers declinedtno n n n)" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-declinedtno"
assert_contains "$RUN_OUT" "Nothing was changed" \
    "a run that loaded nothing is still allowed to say it changed nothing"

# --- 7b. a declined gate must not let --start run a PREVIOUS run's compose ----
#
# The worst shape of the old bug: --start ran `docker compose -f <dir>/compose.yaml
# up -d` regardless, so a declined write started whatever stale file was already
# there, paired with this run's fresh .env.
ROOT=$(mkroot declinestart)
mkdir -p "$TMP/out-stale"
printf 'services: {}\n' >"$TMP/out-stale/compose.yaml"
ANS=$(answers declinestart n n n n)
: >"$AGENT_SHIM_LOG"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --start \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-stale"
assert_eq 0 "$RUN_RC" "declining the gate with --start is not an error"
assert_not_contains "$(cat "$AGENT_SHIM_LOG")" "up -d" \
    "a declined write gate never starts a stale compose.yaml"
assert_eq "services: {}" "$(cat "$TMP/out-stale/compose.yaml")" \
    "and the stale file is left exactly as it was"

# --- 8. a run with no token warns rather than dying ---------------------------
#
# The hub mints tokens; the setup script never invents one. Dying here would
# waste every answer the operator already gave, so AGENT_TOKEN is written empty
# and the report says the agent will refuse to start until it is filled in. The
# same treatment applies to an empty hub URL, which is equally fatal.
ROOT=$(mkroot notoken)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-notoken"
assert_eq 0 "$RUN_RC" "a run with no token still completes"
assert_contains "$RUN_OUT" "refuse to start" "the missing token is called out loudly"
assert_contains "$(cat "$TMP/out-notoken/.env")" "AGENT_TOKEN=" "AGENT_TOKEN is written empty"
assert_not_contains "$RUN_OUT" "nta_" "no token value is ever printed"

ROOT=$(mkroot nohuburl)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --token nta_x \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-nohub"
assert_eq 0 "$RUN_RC" "a run with no hub URL still completes"
assert_contains "$RUN_OUT" "no hub URL" "the missing hub URL is called out"
assert_contains "$(cat "$TMP/out-nohub/.env")" "AGENT_HUB_URL=" "AGENT_HUB_URL is written empty"

# --- 8b. the free-text values are asked for, and each prompt advances ---------
#
# The bug this pins: netra_ask_value used to print its answer for the caller to
# capture with `VAR=$(netra_ask_value ...)`, which runs it in a SUBSHELL - so
# AGENT_VALUE_INDEX never advanced in the parent, every prompt read line 1
# forever, and the host-type validation loop spun on it without end. A single
# values file with four DIFFERENT lines is what catches that: with the bug, all
# four values are the hub URL and the run never terminates.
ROOT=$(mkroot values)
VALS=$(values four https://vals.example "Gravelines, FR" OVH vps)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" AGENT_VALUES_FILE="$VALS" \
    "$SH" "$SETUP" --token nta_x \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-values"
assert_eq 0 "$RUN_RC" "a run driven by a values file completes"
VALENV=$(cat "$TMP/out-values/.env")
assert_contains "$VALENV" "AGENT_HUB_URL=https://vals.example" "the hub URL is the first value"
assert_contains "$VALENV" "AGENT_LOCATION=Gravelines, FR" "the location is the second"
assert_contains "$VALENV" "AGENT_PROVIDER=OVH" "the provider is the third"
assert_contains "$VALENV" "AGENT_HOST_TYPE=vps" "the host type is the fourth"
# Never asked for, so it stays blank however many values are supplied.
assert_contains "$VALENV" "AGENT_FACILITY=" "the facility is not asked for"

# An invalid host type is re-asked rather than written to .env, and an empty
# answer ends the loop rather than re-taking the rejected value as its default.
ROOT=$(mkroot badhosttype)
VALS=$(values bad https://vals.example "Gravelines, FR" OVH nas "")
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" AGENT_VALUES_FILE="$VALS" \
    "$SH" "$SETUP" --token nta_x \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-badht"
assert_eq 0 "$RUN_RC" "an invalid host type does not abort the run"
assert_contains "$RUN_OUT" "is not one of bare_metal" "the invalid host type is rejected out loud"
assert_eq "AGENT_HOST_TYPE=" "$(grep '^AGENT_HOST_TYPE=' "$TMP/out-badht/.env")" \
    "a rejected host type is left blank rather than written to .env"

# --- 8c. a value with no placeholder to land in is REPORTED, not swallowed ----
#
# Templates are fetched at a RELEASE TAG, not from this working tree, so a
# script newer than the release its templates come from will ask for a value the
# tagged env.tmpl has no `__TOKEN__` for, echo the answer in the plan, and write
# nothing. Simulated here with a template directory whose env.tmpl predates the
# identity values.
ROOT=$(mkroot oldtmpl)
OLDTMPL="$TMP/oldtmpl"
mkdir -p "$OLDTMPL"
cp "$TEMPLATES/compose.yaml.tmpl" "$OLDTMPL/"
grep -v '^AGENT_LOCATION=' "$TEMPLATES/env.tmpl" >"$OLDTMPL/env.tmpl"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --token nta_x \
    --hub-url https://h --location "Gravelines, FR" \
    --template-dir "$OLDTMPL" --output-dir "$TMP/out-oldtmpl"
assert_eq 0 "$RUN_RC" "an out-of-date template is not a hard failure"
assert_contains "$RUN_OUT" "AGENT_LOCATION is not in the rendered .env" \
    "a value that found no placeholder is named"
assert_contains "$RUN_OUT" "newer than the release" "the note explains why it happened"
# ...and the values that DID land say nothing, or the note is noise.
assert_not_contains "$RUN_OUT" "AGENT_PROVIDER is not in the rendered .env" \
    "a value that was never given is not reported as lost"
assert_not_contains "$RUN_OUT" "AGENT_HUB_URL is not in the rendered .env" \
    "a value that landed correctly is not reported as lost"

# --- 8d. a write that FAILS is a failure, not a summary claiming success ------
#
# write_outputs used to be called as `if write_outputs; then`, which suspends
# errexit for every command inside it. With an unwritable output directory the
# mkdir failed, both redirections failed, the function still returned 0, the
# summary announced files that do not exist, --start ran docker compose against
# a missing compose.yaml, and the script exited 0 — so a provisioning wrapper
# checking $? saw success.
ROOT=$(mkroot writefail)
BLOCKED="$TMP/blocked"
mkdir -p "$BLOCKED"
chmod 500 "$BLOCKED"
: >"$AGENT_SHIM_LOG"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --start \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$BLOCKED/out"
chmod 700 "$BLOCKED"
assert_eq 1 "$RUN_RC" "a write that cannot be performed exits non-zero"
assert_contains "$RUN_OUT" "could not create" "the failure names what it could not do"
assert_not_contains "$RUN_OUT" "Summary" "a failed write does not reach the summary"
assert_not_contains "$(cat "$AGENT_SHIM_LOG")" "up -d" \
    "and --start never runs against files that were not written"

# --- 8e. no token is not a hard error, and the run still finishes -------------
#
# The setup script never invents a token: the hub mints them and stores only a
# SHA-256. An operator who has not minted one yet must still be able to finish
# the run rather than have every answer already given thrown away — AGENT_TOKEN
# is written empty with a loud note instead. Every other test passes --token,
# which is why this needs its own.
#
# AGENT_TTY points at /dev/null: READABLE (so the no-terminal branch is not the
# one under test) but instantly EOF, so the hidden read returns an empty token
# rather than hanging.
ROOT=$(mkroot notoken)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY=/dev/null \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-notoken"
assert_eq 0 "$RUN_RC" "a run with no token completes rather than dying"
assert_contains "$RUN_OUT" "Summary" "the run reaches the end"
assert_file_present "$TMP/out-notoken/.env" ".env is still written"
assert_contains "$(cat "$TMP/out-notoken/.env")" "AGENT_TOKEN=" \
    "AGENT_TOKEN is present and empty, not invented"

# --- 8f. an .env left alone is not diagnosed as a broken template -------------
#
# _check_env_value ran against whatever .env was on disk, so a re-run without
# --force warned (correctly) that the file was left untouched and then blamed
# the template for every value that differed from it.
ROOT=$(mkroot recheck)
OUT2="$TMP/out-recheck"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" \
    --token nta_first --hub-url https://first.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT2"
assert_eq 0 "$RUN_RC" "the first run succeeds"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" \
    --token nta_second --hub-url https://second.example --location "Zurich, CH" \
    --template-dir "$TEMPLATES" --output-dir "$OUT2"
assert_eq 0 "$RUN_RC" "the re-run succeeds"
assert_contains "$RUN_OUT" "already exists and --force" "the re-run says why .env was kept"
assert_not_contains "$RUN_OUT" "is not in the rendered .env" \
    "an .env deliberately left alone is not blamed on the template"

# --- 8g. a re-run reuses the existing .env instead of asking for it ------------
#
# The defect: write_outputs refuses to overwrite an existing .env without
# --force, and configure asked for every value anyway -- the hub URL, then the
# TOKEN at a hidden prompt -- and dropped all of them. Re-running to pick up a
# new mount is the ordinary case, so it must be one command and no questions.
#
# Driven with an EMPTY values file and no --token: with the bug, the run asks
# and writes blanks; with the fix, nothing is asked because nothing is empty.
ROOT=$(mkroot reuse)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_first --hub-url https://first.example \
    --location "Zurich, CH" --provider Hetzner --host-type bare_metal \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-reuse"
assert_eq 0 "$RUN_RC" "the first run completes"

# The re-run: no flags carrying any value, and no tty to be asked at.
ROOT=$(mkroot reuse2)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-reuse"
assert_eq 0 "$RUN_RC" "the re-run completes with no values supplied"

REUSEENV=$(cat "$TMP/out-reuse/.env")
assert_contains "$REUSEENV" "AGENT_TOKEN=nta_first" "the existing token survives the re-run"
assert_contains "$REUSEENV" "AGENT_HUB_URL=https://first.example" "the existing hub URL survives"
assert_contains "$REUSEENV" "AGENT_LOCATION=Zurich, CH" "the existing location survives"

# What the operator sees: the keys reused, and never the token itself.
assert_contains "$RUN_OUT" "reused from .env" "the re-run says what it reused"
assert_contains "$RUN_OUT" "AGENT_TOKEN" "the reused token is named"
assert_not_contains "$RUN_OUT" "nta_first" "the token VALUE is never printed"

# The point of the whole change: the re-run did not warn that it was about to
# discard anything, because it asked for nothing to discard.
assert_not_contains "$RUN_OUT" "will NOT be written" \
    "the re-run does not announce that it is dropping the answers it collected"

# --force is the opposite case: the operator is REPLACING those values, so
# pre-filling them would silently answer the question they asked to be asked.
# A rotated token must land.
ROOT=$(mkroot reuse3)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --token nta_rotated --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-reuse"
assert_eq 0 "$RUN_RC" "a --force re-run completes"
ROTENV=$(cat "$TMP/out-reuse/.env")
assert_contains "$ROTENV" "AGENT_TOKEN=nta_rotated" "--force replaces the token"
assert_not_contains "$ROTENV" "nta_first" "the old token is gone"

# --- 8h. a key the .env leaves EMPTY is not asked for either -------------------
#
# The worst shape of the original defect, and the one 8g does not reach because
# every value there arrives by flag. A first run with NO token writes
# AGENT_TOKEN= empty, which is an allowed path. The operator then mints a token
# and re-runs without --force: the value is empty, so seeding cannot fill it,
# and the old prompt would take the secret at a hidden prompt and drop it in
# write_outputs. It must be named, not asked for.
ROOT=$(mkroot emptyseed)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --hub-url https://first.example \
    --location "Zurich, CH" --provider Hetzner --host-type bare_metal \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-emptyseed"
assert_eq 0 "$RUN_RC" "a first run with no token completes"
assert_eq "AGENT_TOKEN=" "$(grep '^AGENT_TOKEN=' "$TMP/out-emptyseed/.env")" \
    "the first run leaves the token empty"

ROOT=$(mkroot emptyseed2)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-emptyseed"
assert_eq 0 "$RUN_RC" "the re-run completes"
assert_contains "$RUN_OUT" "are EMPTY in it" "the empty key is named rather than asked for"
assert_contains "$RUN_OUT" "AGENT_TOKEN" "and the token is the key named"
# resolve_token's own "no token was provided" complaint is the observable that
# separates skipped from attempted: reached, it fires; skipped, it cannot. (The
# hidden prompt itself is not observable here -- the harness has no tty, so
# resolve_token would take its no-tty branch either way, and asserting on the
# prompt string would pass whether or not the skip works. Verified by disabling
# the skip and watching this assertion, and only this one, go red.)
assert_not_contains "$RUN_OUT" "no agent token was provided" \
    "the token is not collected on a run that cannot write it"
assert_contains "$RUN_OUT" "AGENT_LOCATION" "the values it CAN reuse are still reused"

# --- 8i. --force keeps the dimensions the .env already had --------------------
#
# --force rewrites the WHOLE .env, and every value it cannot fill is written
# empty. seed_from_env was deliberately not called on this path, so the values
# the operator did not retype were destroyed:
#
#   setup-agent.sh --force --token X --hub-url Y
#
# rewrote AGENT_LOCATION=, AGENT_PROVIDER= and AGENT_HOST_TYPE= empty. Two ways
# to reach it, and the ordinary one is interactive: netra_ask_value falls back
# to the variable's current value, which was empty, so pressing Enter at "Where
# is this host" ERASED it -- while every other prompt in this script treats
# Enter as "leave it alone". The shape below is the other way: an unreadable
# $P_TTY makes netra_ask_value return empty without printing anything at all.
#
# Asserted on the RESULTING .env, never on prompt strings. With AGENT_TTY
# pointing at an unopenable path, netra_ask_value prints nothing whatever it
# does, so a prompt-text assertion here would pass with the fix removed.
ROOT=$(mkroot forceseed)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_first --hub-url https://first.example \
    --location "Zurich, CH" --provider Hetzner --host-type bare_metal \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-forceseed"
assert_eq 0 "$RUN_RC" "the first run completes"

# The rotation: a new token and nothing else. Every dimension is left to the
# prompts, which cannot be answered here.
ROOT=$(mkroot forceseed2)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --token nta_second --hub-url https://second.example \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-forceseed"
assert_eq 0 "$RUN_RC" "the --force rotation completes"

FORCEENV=$(cat "$TMP/out-forceseed/.env")
assert_contains "$FORCEENV" "AGENT_LOCATION=Zurich, CH" "--force keeps the location it was not given"
assert_contains "$FORCEENV" "AGENT_PROVIDER=Hetzner" "--force keeps the provider"
assert_contains "$FORCEENV" "AGENT_HOST_TYPE=bare_metal" "--force keeps the host type"

# What --force IS for still works: the flags win over the seeded values.
assert_contains "$FORCEENV" "AGENT_TOKEN=nta_second" "--force still replaces the token"
assert_contains "$FORCEENV" "AGENT_HUB_URL=https://second.example" "--force still replaces the hub URL"
assert_not_contains "$FORCEENV" "nta_first" "the old token is gone"

# ...and the message about what was reused names only what was actually taken.
# A key the file holds and a FLAG answered is not reused: on the reuse path
# that distinction was invisible (the flag's value was dropped too), but here
# the flag is what gets written, so naming it would be false.
assert_not_contains "$RUN_OUT" "reused from .env: AGENT_HUB_URL" \
    "a key the flag supplied is not reported as reused"

# The hub URL survives a --force run that did not supply one. Every other case
# here passes --hub-url, so without this the seeded-HUB_URL branch is never
# exercised and the fix could be half-removed with the suite still green.
ROOT=$(mkroot forceseed3)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --token nta_third \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-forceseed"
assert_eq 0 "$RUN_RC" "a --force run with no hub URL completes"
assert_contains "$(cat "$TMP/out-forceseed/.env")" "AGENT_HUB_URL=https://second.example" \
    "--force keeps the hub URL it was not given"
assert_not_contains "$RUN_OUT" "no hub URL was given" \
    "and does not warn about a hub URL it still has"

# The token follows the SAME rule as the other four, and this is the assertion
# that pins it: an operator who runs --force to fix a typo and presses Enter
# through the prompts must not end up with an agent that cannot authenticate
# and an old token already overwritten on disk. Rotating stays explicit --
# --token, or typing one at the prompt, both covered above.
ROOT=$(mkroot forceseed4)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --hub-url https://fourth.example \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-forceseed"
assert_eq 0 "$RUN_RC" "a --force run with no token completes"
assert_contains "$(cat "$TMP/out-forceseed/.env")" "AGENT_TOKEN=nta_third" \
    "--force keeps the existing token when it is not asked to replace one"
assert_not_contains "$RUN_OUT" "written empty" \
    "and does not threaten to blank a token it is keeping"
# Named, never printed: the whole file is checked, because the token's VALUE
# reaching the scroll is the failure this rule exists to prevent.
assert_not_contains "$RUN_OUT" "nta_third" "the kept token's value is never printed"

# A flag still beats the file, and is not re-asked: --location here must land
# even though the .env holds a different one.
ROOT=$(mkroot forceseed5)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --token nta_fourth --hub-url https://fifth.example \
    --location "Bern, CH" \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-forceseed"
assert_eq 0 "$RUN_RC" "a --force run with a location flag completes"
assert_contains "$(cat "$TMP/out-forceseed/.env")" "AGENT_LOCATION=Bern, CH" \
    "a flag beats the value seeded from the file"

# The sensor is NOT kept, and that is deliberate: detect_sensors has already
# run and reported by the time configure executes, so restoring the old value
# here would re-pin a chip this run may have just described as auto-selected --
# or one no longer on the host at all.
assert_eq "AGENT_PRIMARY_SENSOR=" "$(grep '^AGENT_PRIMARY_SENSOR=' "$TMP/out-forceseed/.env")" \
    "--force leaves the primary sensor to be re-detected rather than re-pinning it"

# --- 9. --token-file ----------------------------------------------------------
#
# A token pasted into a file by a provisioning system routinely arrives with a
# trailing newline and, from anything Windows-shaped, a CR. A CR that reached
# .env would be part of the value and every request would 401 with nothing in
# the logs to explain it.
ROOT=$(mkroot tokenfile)
printf 'nta_fromfile\r\n' >"$TMP/token.txt"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token-file "$TMP/token.txt" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tokenfile"
assert_eq 0 "$RUN_RC" "--token-file completes"
assert_eq "AGENT_TOKEN=nta_fromfile" \
    "$(grep '^AGENT_TOKEN=' "$TMP/out-tokenfile/.env")" \
    "the CR and the trailing newline are stripped from a token file"

run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    "$SH" "$SETUP" --token-file "$TMP/nosuch" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tf2"
assert_eq 1 "$RUN_RC" "a missing --token-file is a hard error"
assert_contains "$RUN_OUT" "does not exist" "the missing token file failure says so"
# Resolved in parse_args, so a typo in the command line is caught before the
# operator has answered four questions about a run that cannot finish.
assert_not_contains "$RUN_OUT" "Preflight" "an unreadable token file fails before detection"

: >"$TMP/token-empty.txt"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    "$SH" "$SETUP" --token-file "$TMP/token-empty.txt" --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-tf3"
assert_eq 1 "$RUN_RC" "an empty --token-file is a hard error, not an empty token"

# --- 10. --start actually starts the stack ------------------------------------
#
# Section 1 only proves --start is suppressed when the gate is declined. This
# proves the other half: that a real --start reaches docker at all, and does so through
# netra_exec with a -f pointing at the file just rendered (the setup script cannot
# cd, so an implicit ./compose.yaml would start the wrong thing or nothing).
ROOT=$(mkroot started)
: >"$AGENT_SHIM_LOG"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --start --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-start"
assert_eq 0 "$RUN_RC" "a --start run succeeds"
assert_contains "$(cat "$AGENT_SHIM_LOG")" "compose -f $TMP/out-start/compose.yaml up -d" \
    "--start runs docker compose against the rendered file"

# The v1 spelling is a different binary, not a different subcommand.
ROOT=$(mkroot startedv1)
: >"$AGENT_SHIM_LOG"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    AGENT_SHIM_COMPOSE_V2_RC=1 "$SH" "$SETUP" --start --token nta_x \
    --hub-url https://h --template-dir "$TEMPLATES" --output-dir "$TMP/out-start1"
assert_eq 0 "$RUN_RC" "a --start run on a compose v1 host succeeds"
assert_contains "$(cat "$AGENT_SHIM_LOG")" \
    "docker-compose -f $TMP/out-start1/compose.yaml up -d" \
    "--start uses docker-compose where that is the only spelling available"
assert_contains "$RUN_OUT" "docker-compose up -d" \
    "the finish report gives the v1 command, not the v2 one"

# --- 11. a container-only host still gets a working compose --------------------
#
# Zero accepted filesystems is legitimate (§6.4). The run must succeed, say so,
# and produce a compose.yaml with no volumes: key rather than an error.
ROOT=$(mkroot containeronly)
cat >"$ROOT/proc/1/mountinfo" <<'EOF'
26 25 0:22 / /proc rw,nosuid - proc proc rw
29 25 0:24 / /run rw,nosuid shared:5 - tmpfs tmpfs rw
33 25 0:44 / /var/lib/docker/overlay2/8a1/merged rw - overlay overlay rw
EOF
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-conly"
assert_eq 0 "$RUN_RC" "a container-only host is not an error"
assert_contains "$RUN_OUT" "none accepted" "the run says no filesystem was accepted"
assert_file_present "$TMP/out-conly/compose.yaml" "a working compose.yaml is still produced"
COMPOSE_BODY=$(grep -v '^[[:space:]]*#' "$TMP/out-conly/compose.yaml")
assert_not_contains "$COMPOSE_BODY" "/netra/fs/" "no marker mount is rendered"
# The Docker socket and the mount table are still mounted, so volumes: stays.
assert_contains "$COMPOSE_BODY" "docker.sock" "the Docker socket is still mounted"

# --- 12. the defaults produce a complete agent, and grant no privilege --------
#
# Taking every prompt's default must never expand privilege — SYS_ADMIN defaults
# n and stays off — while still producing an agent that
# collects everything it can without them. That second half is now carried by
# the read-only mounts, which are NOT prompts at all: the package database, the
# D-Bus socket and SYS_RAWIO are enabled automatically, on the same argument
# that was always made for the Docker socket.
ROOT=$(mkroot yesdefaults)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    AGENT_SHIM_MODPROBE_HWMON="$ROOT/sys/class/hwmon" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-yesdef"
assert_eq 0 "$RUN_RC" "a defaults run on an NVMe host succeeds"
assert_file_present "$TMP/out-yesdef/compose.yaml" "compose.yaml is written"
assert_file_present "$TMP/out-yesdef/.env" ".env is written"
YESBODY=$(grep -v '^[[:space:]]*#' "$TMP/out-yesdef/compose.yaml")
assert_not_contains "$YESBODY" "SYS_ADMIN" "the default never grants SYS_ADMIN"
assert_not_contains "$YESBODY" "/dev/nvme0" \
    "a declined SYS_ADMIN also drops the NVMe controller from devices:"
assert_contains "$RUN_OUT" "--sys-admin" "the run says which flag would grant SYS_ADMIN"
# pid: host is the counter-example: not a prompt, not a flag, and not withheld
# by taking the defaults. A run that granted no privilege at all still gets the
# namespace, because without it the agent reports no per-container network and
# no process count.
assert_contains "$YESBODY" "pid: host" \
    "the defaults still render pid: host -- it is unconditional"
assert_not_contains "$RUN_OUT" "Enable per-process" \
    "pid: host is not prompted for at all"
assert_contains "$RUN_OUT" "pid: host (always)" \
    "and the run states it rather than offering it"
assert_contains "$RUN_OUT" "cmdline" \
    "the run states the exposure even though there is no question attached"
assert_contains "$RUN_OUT" "Skipped or degraded" "the declines reach the finish report"
# The benign half, and the proof that it is no longer asked about: ANS_DEFAULT
# has exactly three lines (SYS_ADMIN, drivetemp, write gate), so a resurrected
# package or D-Bus prompt would consume a fourth and the run would die on an
# exhausted answers file rather than quietly asserting the wrong thing.
assert_contains "$YESBODY" "SYS_RAWIO" "SYS_RAWIO is granted automatically"
assert_contains "$YESBODY" "device_cgroup_rules" \
    "the agent is given the device tree rather than a computed device list"
assert_contains "$YESBODY" "/var/lib/dpkg" "the package database is mounted automatically"
assert_contains "$YESBODY" "system_bus_socket" "the D-Bus socket is mounted automatically"
assert_contains "$YESBODY" "docker.sock" "the Docker socket is mounted"
assert_contains "$YESBODY" "/netra/fs/root" "the marker mount is rendered"
assert_is_file "$ROOT/.netra" "the marker files are created"

# --- 13. --sys-admin grants explicitly, without prompting ---------------------
#
# The answers files hold exactly the prompts the flag does NOT remove, so a
# --sys-admin that still asked would run one line short and die with "answers
# file exhausted" rather than passing quietly.
ROOT=$(mkroot grantsysadmin)
ANS=$(answers sysadmin y y y)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --sys-admin \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-sysadmin"
assert_eq 0 "$RUN_RC" "--sys-admin succeeds while consuming no answer of its own"
SABODY=$(grep -v '^[[:space:]]*#' "$TMP/out-sysadmin/compose.yaml")
assert_contains "$SABODY" "SYS_ADMIN" "--sys-admin grants SYS_ADMIN"
# No device list to check any more: the rules name nothing, so what --sys-admin
# changes is the capability and only that.
assert_contains "$SABODY" "device_cgroup_rules" "--sys-admin leaves the device rules alone"

# --- 13a. --no-smart declines the device tree without being asked -------------
#
# The widest grant the rendered compose can carry, and the only one that used to
# be taken silently. An answers file one line SHORTER than case 13's is the
# proof it was not prompted: SYS_ADMIN is not asked either once SMART is gone.
ROOT=$(mkroot nosmart)
ANS=$(answers nosmart y y)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --no-smart \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-nosmart"
assert_eq 0 "$RUN_RC" "--no-smart succeeds while consuming no answer of its own"
NSBODY=$(grep -v '^[[:space:]]*#' "$TMP/out-nosmart/compose.yaml")
assert_not_contains "$NSBODY" "device_cgroup_rules" "no device rules are rendered"
assert_not_contains "$NSBODY" "target: /dev" "the host device tree is not bound"
assert_not_contains "$NSBODY" "SYS_RAWIO" "and no SYS_RAWIO either"
assert_contains "$RUN_OUT" "SMART declined" "the decline reaches the finish report"

# --- 14. --pid-host is accepted and changes nothing ---------------------------
#
# The flag is retained purely so provisioning that already passes it does not
# start dying on an unknown option. It must parse, consume no answer, and
# produce a render identical to the one without it -- in particular it must not
# drag SYS_ADMIN along, which is the failure this pairing has always guarded.
ROOT=$(mkroot grantpidhost)
ANS=$(answers pidhost n y y)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --pid-host \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-pidhost"
assert_eq 0 "$RUN_RC" "--pid-host is still accepted, and consumes no answer of its own"
PHBODY=$(grep -v '^[[:space:]]*#' "$TMP/out-pidhost/compose.yaml")
assert_contains "$PHBODY" "pid: host" "the namespace is rendered, as it would be without the flag"
assert_not_contains "$PHBODY" "SYS_ADMIN" "--pid-host does not also grant SYS_ADMIN"

# --- 15. --unsupported-os suppresses the prompt without silencing the warning --
#
# §12a: the version floors are only the releases where cgroup v2 became the
# default, and the checks that matter are probed directly rather than inferred
# from the distro name. So an install on a cgroup-v2 host netra does not
# recognise BY NAME must remain possible — otherwise a prompt that defaults to
# "no" quietly turns an advisory floor into a hard refusal.
ROOT=$(mkroot unsupportedgrant)
cp "$(fixture os-release)/void" "$ROOT/etc/os-release"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" "$SH" "$SETUP" --unsupported-os \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-unsupported"
assert_eq 0 "$RUN_RC" "--unsupported-os completes on a distro netra does not know"
assert_file_present "$TMP/out-unsupported/compose.yaml" "a working compose.yaml is still produced"
UOBODY=$(grep -v '^[[:space:]]*#' "$TMP/out-unsupported/compose.yaml")
assert_contains "$UOBODY" "docker.sock" "the rendered compose is a real one, not a stub"
assert_contains "$UOBODY" "/netra/fs/root" "the marker mount is rendered on the unknown distro too"

# The flag suppresses the PROMPT, not the DIAGNOSIS. An operator who forced an
# install onto an unrecognised distro must still be told which distro it was and
# what netra knows, both in the scroll and in the finish report they read after.
assert_contains "$RUN_OUT" "not a distribution/version" \
    "--unsupported-os still prints the unsupported-OS warning"
assert_contains "$RUN_OUT" "void Linux" "the warning names the actual distro"
assert_contains "$RUN_OUT" "Debian 11+" "the warning still states the floors netra knows"
assert_contains "$RUN_OUT" "--unsupported-os" "the run says the grant came from the flag"
# The note has to survive to the finish report, not just scroll past. Everything
# after the "Skipped or degraded:" header is that report.
UONOTES=$(printf '%s\n' "$RUN_OUT" | sed -n '/Skipped or degraded:/,$p')
assert_contains "$UONOTES" "not a distribution/version" \
    "the unsupported OS is recorded as a note in the finish report"

# The teeth: this file holds EXACTLY the three prompts that remain once prompt 1
# is gone (SMART, SYS_ADMIN, drivetemp, the write gate). An --unsupported-os that still
# asked prompt 1 would run the file one line short and die with "answers file
# exhausted".
ROOT=$(mkroot unsupportedcount)
cp "$(fixture os-release)/void" "$ROOT/etc/os-release"
ANS=$(answers unsupported3 y y y y)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" "$SH" "$SETUP" --unsupported-os \
    --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-unscount"
assert_eq 0 "$RUN_RC" "--unsupported-os consumes no answer: prompt 1 is skipped entirely"
assert_not_contains "$RUN_OUT" "exhausted" "the answers file is not run short"
assert_not_contains "$RUN_OUT" "Continue on an unsupported OS?" \
    "the unsupported-OS question is never asked under the flag"

# --- 16. --unsupported-os on a distro netra DOES know is a silent no-op --------
#
# Mirrors --sys-admin on a host with no NVMe, except that this one earns no note
# either: nothing was withheld, so there is nothing to report. A spurious
# warning here would make every provisioning script that passes the flag
# defensively look degraded on a perfectly supported host.
ROOT=$(mkroot unsupportednoop)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --unsupported-os --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-unsnoop"
assert_eq 0 "$RUN_RC" "--unsupported-os on a supported distro succeeds"
assert_contains "$RUN_OUT" "(supported)" "the OS is still reported as supported"
assert_not_contains "$RUN_OUT" "not a distribution/version" \
    "--unsupported-os invents no warning on a distro netra knows"
assert_not_contains "$RUN_OUT" "--unsupported-os" \
    "an unused --unsupported-os is not mentioned anywhere in the run"

# --- 17. every grant flag is documented ---------------------------------------
run_capture "$SH" "$SETUP" --help
assert_contains "$RUN_OUT" "--sys-admin" "--help documents --sys-admin"
assert_contains "$RUN_OUT" "--location" "--help documents --location"
assert_contains "$RUN_OUT" "--provider" "--help documents --provider"
assert_contains "$RUN_OUT" "--host-type" "--help documents --host-type"
assert_not_contains "$RUN_OUT" "--yes" "--help does not offer an unattended mode"
assert_contains "$RUN_OUT" "no unattended mode" "--help says so out loud"
assert_contains "$RUN_OUT" "--pid-host" "--help documents --pid-host"
assert_contains "$RUN_OUT" "--unsupported-os" "--help documents --unsupported-os"

# --- 18. the summary lists what THIS run changed on the host -------------------
#
# The finish report used to say only what was DETECTED. An operator who answered
# yes to the write gate got no list of what now exists on the box: two files, a
# handful of marker files, possibly a kernel module and a
# /etc/modules-load.d entry, all of it only findable by scrolling back.
#
# The ledger is recorded at each mutation rather than assembled at the end, and
# these cases are the difference: a summary written as a fixed block would pass
# the first run below and lie on the second.
ROOT=$(mkroot ledger)
LOUT="$TMP/out-ledger"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    AGENT_SHIM_MODPROBE_HWMON="$ROOT/sys/class/hwmon" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$LOUT"
assert_eq 0 "$RUN_RC" "the first run succeeds"
LEDGER=$(flatten "$RUN_OUT")
assert_contains "$LEDGER" "Changed on this host:" "the summary has a ledger of changes"
assert_contains "$LEDGER" "wrote $LOUT/compose.yaml" "the compose file is listed"
assert_contains "$LEDGER" "wrote $LOUT/.env" "the .env is listed"
assert_contains "$LEDGER" "created the directory $LOUT" "the output directory is listed"
assert_contains "$LEDGER" "created the marker file /.netra" "the root marker is listed"
assert_contains "$LEDGER" "created the marker file /mnt/ark/.netra" \
    "and so is the one on the second filesystem"
assert_is_file "$ROOT/.netra" "the marker really is a regular file, not a directory"
# The EMIT path, not the probe path: a report naming the fixture prefix would
# send the operator looking somewhere that does not exist on their host.
assert_not_contains "$LEDGER" "created the marker file $ROOT" \
    "the marker entry names the host path, not the probed one"
assert_contains "$LEDGER" "loaded the drivetemp kernel module" "the kernel module is listed"
assert_contains "$LEDGER" "wrote /etc/modules-load.d/drivetemp.conf" \
    "and so is the file that persists it"
assert_not_contains "$RUN_OUT" "nta_x" "the ledger never prints the token"

# The teeth. A ledger assembled in print_finish would repeat the first run's
# list; this one has to report exactly what the second run did, which is
# overwrite one derived file and nothing else.
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers ledger2 n y)" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$LOUT"
assert_eq 0 "$RUN_RC" "the re-run succeeds"
LEDGER2=$(flatten "$RUN_OUT")
assert_contains "$LEDGER2" "overwrote $LOUT/compose.yaml" \
    "a compose.yaml that was already there is reported as overwritten"
assert_not_contains "$LEDGER2" "wrote $LOUT/.env" \
    "an .env the --force guard left alone is not reported as written"
assert_not_contains "$LEDGER2" "created the directory" \
    "an output directory that already existed is not reported as created"
assert_not_contains "$LEDGER2" "created the marker file" \
    "and neither are markers that were already there"
# drivetemp was already loaded by the first run, so this one changed nothing
# about it. The answers file is two lines rather than three for exactly that
# reason: no prompt is asked.
assert_not_contains "$LEDGER2" "loaded the drivetemp kernel module" \
    "a module that was already loaded is not this run's change"

# --- 18b. a run that changed nothing says so, in its own words -----------------
#
# "Nothing on this host was changed" and the declined-gate "Nothing was changed"
# are deliberately different sentences: the second is about the whole run at the
# point the operator declined, the first is about the ledger. Case 7b (c) above
# pins the other one on the same path.
ROOT=$(mkroot ledgernone)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers ledgernone n n n)" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-ledgernone"
assert_eq 0 "$RUN_RC" "declining everything is not an error"
assert_contains "$RUN_OUT" "Nothing on this host was changed." \
    "an untouched host is reported as untouched"
assert_not_contains "$RUN_OUT" "Changed on this host:" \
    "and no empty ledger header is printed"

# A declined gate AFTER drivetemp was loaded still owes the operator the two
# entries: the ledger is printed before the early return for a declined write.
ROOT=$(mkroot ledgerdt)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers ledgerdt n y n)" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    AGENT_SHIM_MODPROBE_HWMON="$ROOT/sys/class/hwmon" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-ledgerdt"
assert_eq 0 "$RUN_RC" "declining the gate after loading the module is not an error"
LEDGER3=$(flatten "$RUN_OUT")
assert_contains "$LEDGER3" "loaded the drivetemp kernel module" \
    "a declined run still lists the module it loaded"
assert_contains "$LEDGER3" "wrote /etc/modules-load.d/drivetemp.conf" \
    "and the file it persisted"
assert_not_contains "$LEDGER3" "Nothing on this host was changed" \
    "a run that loaded a kernel module never claims it changed nothing"
# The banner names compose.yaml on every run, so this has to look for the
# ledger's own wording rather than the filename.
assert_not_contains "$LEDGER3" "wrote $TMP/out-ledgerdt/compose.yaml" \
    "and lists nothing it did not write"

# --- 18b2. an existing marker DIRECTORY is left exactly as it is ---------------
#
# The marker became a file, and every host set up before that has a directory at
# the same path. It is an equally good bind source — statfs(2) reports the
# filesystem a path lives on and does not care what kind of object it is — so a
# re-run must leave it alone rather than churn a working install, and must not
# claim it created anything.
ROOT=$(mkroot ledgerolddir)
mkdir -p "$ROOT/.netra"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers ledgerolddir n n y)" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-olddir"
assert_eq 0 "$RUN_RC" "a host with the old marker directory still installs"
assert_contains "$(flatten "$RUN_OUT")" "created the marker file /mnt/ark/.netra" \
    "the filesystem that had no marker gets a file"
assert_not_contains "$(flatten "$RUN_OUT")" "created the marker file /.netra " \
    "and the one that already had a directory is not reported as created"
if [ -d "$ROOT/.netra" ]; then
    ok "the existing marker directory is left as a directory"
else
    fail "the existing marker directory was replaced"
fi
assert_contains "$(cat "$TMP/out-olddir/compose.yaml")" "/.netra" \
    "and it is still rendered as a bind source"

# --- 18b3. a marker that cannot be created warns; it does not kill the run ----
#
# The code says out loud that this is a DEGRADATION, not a failure. It only is
# one if the redirection reports its failure as a STATUS: `:` is a POSIX special
# builtin, and a redirection error on a special builtin exits a non-interactive
# shell outright — dash does exactly that, bash does not — so `: >>` here would
# abort the run mid-write, after the output directory exists and before the
# summary prints, and the warn below would be unreachable under one of the two
# shells this suite runs.
#
# A dangling symlink is the portable way to make the create fail while the mount
# point itself stays writable; on a real host it is a full filesystem, an
# exhausted inode table, a quota or a relabelled parent.
ROOT=$(mkroot ledgernomarker)
ln -s /nonexistent-dir/marker "$ROOT/.netra"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers ledgernomarker n n y)" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-nomarker"
assert_eq 0 "$RUN_RC" "a marker that cannot be created is not a failed run"
assert_contains "$(flatten "$RUN_OUT")" "could not create the marker file at /.netra" \
    "it warns, naming the marker it could not create"
assert_file_present "$TMP/out-nomarker/compose.yaml" \
    "and the run carries on to write everything else"
assert_contains "$(flatten "$RUN_OUT")" "created the marker file /mnt/ark/.netra" \
    "the other filesystem still gets its marker"
assert_not_contains "$(flatten "$RUN_OUT")" "created the marker file /.netra" \
    "and the one that failed is not claimed as a change"

# --- 18c. --start is a change too, and lands after the summary -----------------
#
# It runs after print_finish, because the report's "Starting the stack:" line
# only makes sense ahead of the compose output. So it prints its own entry
# rather than being left out of the ledger entirely.
ROOT=$(mkroot ledgerstart)
: >"$AGENT_SHIM_LOG"
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    AGENT_SHIM_MODPROBE_HWMON="$ROOT/sys/class/hwmon" \
    "$SH" "$SETUP" --start --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-ledgerstart"
assert_eq 0 "$RUN_RC" "a --start run succeeds"
assert_contains "$(flatten "$RUN_OUT")" \
    "started the agent from $TMP/out-ledgerstart/compose.yaml" \
    "starting the stack is reported as a change"
# And a declined gate never gets that line, because it never started anything.
ROOT=$(mkroot ledgernostart)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$(answers ledgernostart n n n)" \
    AGENT_MODULESLOAD_DIR="$ROOT/etc/modules-load.d" \
    "$SH" "$SETUP" --start --token nta_x --hub-url https://h \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-ledgernostart"
assert_not_contains "$RUN_OUT" "started the agent from" \
    "a declined gate reports no start, because none happened"

exit_case
