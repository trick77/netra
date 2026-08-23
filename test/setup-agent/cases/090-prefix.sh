#!/bin/sh
#
# The two version-skew guards around the NETRA_ -> AGENT_ rename: an existing
# .env still written in the old prefix, and a template older than this script.
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

ROOT="$TMP/full"
mkdir -p "$ROOT"
cp -R "$(fixture root-full)/." "$ROOT/"

# --- 1. an .env still using the old prefix is refused, not read past ----------
#
# Every reader in the script looks for AGENT_ names. Left to run, it would
# report AGENT_HUB_URL and AGENT_TOKEN as EMPTY while both sit in the file under
# their old names, append AGENT_PID_HOST and AGENT_FS_MOUNTS to a file the agent
# now refuses to start with, and -- with --force -- overwrite a token the hub
# only ever stored the SHA-256 of.
OUT="$TMP/out"
mkdir -p "$OUT"
printf 'NETRA_HUB_URL=https://old.example.com\nNETRA_TOKEN=nta_old\n' >"$OUT/.env"
ANS=$(answers full y y)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS" AGENT_UID=0 \
    "$SH" "$SETUP" --sys-admin --pid-host \
    --token nta_testtoken --hub-url https://netra.example.com \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 1 "$RUN_RC" "a pre-rename .env is refused"
assert_contains "$RUN_OUT" "old NETRA_ prefix" "the refusal names the old prefix"
assert_contains "$RUN_OUT" "sed -i.bak" "and hands over the rename that fixes it"

# The refusal is worth nothing if the file it refuses is already damaged: the
# token in it is unrecoverable.
if grep -q '^NETRA_TOKEN=nta_old$' "$OUT/.env"; then
    ok "and the old .env is left byte for byte alone"
else
    fail "the refused .env was modified"
fi
TESTS_RUN=$((TESTS_RUN + 1))

# --- 2. a template older than this script is refused --------------------------
#
# This script comes from master and its templates from the latest release tag,
# so the two can disagree. An unsubstituted marker is a COMMENT LINE: no
# volumes, no devices, no cap_add, and a compose that starts clean while the
# agent measures almost nothing.
OLD="$TMP/oldtemplates"
mkdir -p "$OLD"
sed 's/#__AGENT_/#__NETRA_/' "$TEMPLATES/compose.yaml.tmpl" >"$OLD/compose.yaml.tmpl"
cp "$TEMPLATES/env.tmpl" "$OLD/env.tmpl"

OUT2="$TMP/out2"
ANS2=$(answers full y y)
run_capture env AGENT_SETUP_ROOT="$ROOT" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS2" AGENT_UID=0 \
    "$SH" "$SETUP" --sys-admin --pid-host \
    --token nta_testtoken --hub-url https://netra.example.com \
    --template-dir "$OLD" --output-dir "$OUT2"
assert_eq 1 "$RUN_RC" "a template with unrenamed markers is refused"
assert_contains "$RUN_OUT" "unsubstituted template marker" \
    "the refusal names the marker rather than the symptom"
assert_contains "$RUN_OUT" "--template-dir" "and names both escape hatches"

exit_case
