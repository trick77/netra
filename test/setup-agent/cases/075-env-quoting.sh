#!/bin/sh
#
# Free-text values compose's env_file parser would rewrite: unquoted, it strips
# an inline ` #...` comment and expands `$VAR`. Such values are written
# single-quoted and read back verbatim by --force re-runs.
set -eu
# shellcheck source=/dev/null
. "$LIB"

SETUP="$REPO/setup-agent.sh"
TEMPLATES="$REPO/deploy/agent"
NO_TTY=/nonexistent/netra-tty
REAL_PATH="$PATH"

mkshims "$TMP/shims"
AGENT_UID=0
export AGENT_UID
ANS_DEFAULT=$(answers default y n y y)

mkroot() {
    _mkroot_dst="$TMP/$1"
    mkdir -p "$_mkroot_dst"
    cp -R "$(fixture root-full)/." "$_mkroot_dst/"
    printf '%s\n' "$_mkroot_dst"
}

OUT="$TMP/out"
# shellcheck disable=SC2016
run_capture env AGENT_SETUP_ROOT="$(mkroot first)" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_first --hub-url https://first.example \
    --location "Rack 3 #2" --provider 'a$HOME' \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "a run with # and \$ in free-text values succeeds"
assert_eq "AGENT_LOCATION='Rack 3 #2'" "$(grep '^AGENT_LOCATION=' "$OUT/.env")" \
    "a value with an inline # is written single-quoted"
assert_eq "AGENT_PROVIDER='a\$HOME'" "$(grep '^AGENT_PROVIDER=' "$OUT/.env")" \
    "a value with a \$ is written single-quoted"
assert_eq "AGENT_TOKEN=nta_first" "$(grep '^AGENT_TOKEN=' "$OUT/.env")" \
    "a value needing no quotes is written as before"
assert_not_contains "$RUN_OUT" "is not in the rendered .env" \
    "a quoted value is recognised as landed"

# --force with only a new token: the quoted values are seeded from .env and
# must come back out exactly as they went in.
run_capture env AGENT_SETUP_ROOT="$(mkroot force)" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --token nta_second --hub-url https://first.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "the --force re-run succeeds"
assert_eq "AGENT_LOCATION='Rack 3 #2'" "$(grep '^AGENT_LOCATION=' "$OUT/.env")" \
    "--force round-trips the quoted location"
assert_eq "AGENT_PROVIDER='a\$HOME'" "$(grep '^AGENT_PROVIDER=' "$OUT/.env")" \
    "--force round-trips the quoted provider"

# A single quote cannot be represented inside single quotes.
run_capture env AGENT_SETUP_ROOT="$(mkroot squote)" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --token nta_x --hub-url https://h --location "O'Brien #2" \
    --template-dir "$TEMPLATES" --output-dir "$TMP/out-squote"
assert_eq 1 "$RUN_RC" "a value needing quotes that contains a single quote is refused"
assert_contains "$(flatten "$RUN_OUT")" "single quote" "the refusal names the single quote"

# What compose itself makes of the file, where docker is installed.
if PATH="$REAL_PATH" docker compose version >/dev/null 2>&1; then
    mkdir -p "$TMP/cfg"
    cp "$OUT/compose.yaml" "$OUT/.env" "$TMP/cfg/"
    CFG=$(PATH="$REAL_PATH" docker compose -f "$TMP/cfg/compose.yaml" config 2>&1 || true)
    assert_contains "$CFG" "AGENT_LOCATION: 'Rack 3 #2'" "compose reads the location verbatim"
    # shellcheck disable=SC2016
    assert_contains "$CFG" 'AGENT_PROVIDER: a$$HOME' "compose does not expand the \$"
else
    printf 'skip (docker compose is not available: compose parse not checked)\n'
fi

# A hand-written double-quoted value, which compose reads without the quotes:
# --force seeds it without them too, rather than re-quoting the quotes in.
grep -v '^AGENT_LOCATION=' "$OUT/.env" >"$TMP/env.dquote"
printf 'AGENT_LOCATION="Rack 3"\n' >>"$TMP/env.dquote"
cp "$TMP/env.dquote" "$OUT/.env"
run_capture env AGENT_SETUP_ROOT="$(mkroot dquote)" AGENT_TTY="$NO_TTY" \
    AGENT_ANSWERS_FILE="$ANS_DEFAULT" \
    "$SH" "$SETUP" --force --token nta_third --hub-url https://first.example \
    --template-dir "$TEMPLATES" --output-dir "$OUT"
assert_eq 0 "$RUN_RC" "the --force re-run over a double-quoted value succeeds"
assert_eq "AGENT_LOCATION=Rack 3" "$(grep '^AGENT_LOCATION=' "$OUT/.env")" \
    "--force drops a hand-written pair of double quotes"

exit_case
