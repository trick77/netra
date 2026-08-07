#!/bin/sh
#
# Mount table parsing, filtering, deduplication and labelling.
#
# These are unit tests: setup-agent.sh is sourced with NETRA_SOURCED=1 so the
# guarded entrypoint does not run.
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

# Defaults the sourced functions read. parse_args normally sets these; sourcing
# skips netra_main, so they are set explicitly rather than left unset under -u.
INCLUDE_NETWORK_FS=0
DRY_RUN=1

# mi NAME — write $TMP/mi.NAME from stdin and point P_MOUNTINFO at it.
mi() {
    cat >"$TMP/mi.$1"
    P_MOUNTINFO="$TMP/mi.$1"
}

# --- 1. the shared-subtree regression -----------------------------------------
#
# The optional-fields section between the mount options and the literal `-` is
# VARIABLE LENGTH. A laptop usually has none, so fixed field indexing appears to
# work; any host with shared subtrees has `shared:1 master:2` there and fixed
# indexing then reads the fstype out of the middle of the optional fields. The
# first row below has zero optional fields, the second one, the third two.
mi shared <<'EOF'
25 0 8:1 / / rw,relatime - ext4 /dev/sda1 rw
30 25 8:16 / /mnt/one rw,relatime shared:9 - xfs /dev/sdb rw
31 25 8:32 / /mnt/two rw,relatime shared:5 master:2 - btrfs /dev/sdc rw
EOF
ROWS=$(mountinfo_rows)
assert_contains "$ROWS" "8:1|/|/|ext4|/dev/sda1" "no optional fields: fstype parses"
assert_contains "$ROWS" "8:16|/|/mnt/one|xfs|/dev/sdb" "one optional field: fstype parses"
assert_contains "$ROWS" "8:32|/|/mnt/two|btrfs|/dev/sdc" \
    "two optional fields (shared+master): fstype parses"
assert_not_contains "$ROWS" "shared:" "optional fields never leak into a row"

# --- 2. octal unescaping ------------------------------------------------------
#
# printf '%b' is wrong for this: it understands \0ddd but not the bare \ddd form
# the kernel writes, so \134 would survive literally and a backslash-first pass
# would double-unescape. Unescaping happens in awk with explicit gsubs, and the
# backslash gsub runs LAST.
mi esc <<'EOF'
25 0 8:1 / / rw - ext4 /dev/sda1 rw
30 25 8:16 / /mnt/my\040data rw - ext4 /dev/sdb rw
31 25 8:32 / /mnt/tab\011here rw - ext4 /dev/sdc rw
32 25 8:48 / /mnt/back\134slash rw - ext4 /dev/sdd rw
33 25 8:64 / /mnt/dbl\040\134040 rw - ext4 /dev/sde rw
EOF
ROWS=$(mountinfo_rows)
assert_contains "$ROWS" "8:16|/|/mnt/my data|ext4" '\\040 decodes to a space'
assert_contains "$ROWS" "$(printf '8:32|/|/mnt/tab\there|ext4')" '\\011 decodes to a tab'
assert_contains "$ROWS" '8:48|/|/mnt/back\slash|ext4' '\\134 decodes to a backslash'
# The whole point of running the backslash gsub last: `\134040` must come out as
# the literal text `\040`, NOT as a second space.
assert_contains "$ROWS" '8:64|/|/mnt/dbl \040|ext4' \
    'the backslash pass runs last, so \\134040 does not become a space'

# --- 3. the accepted set is exactly {/, /mnt/ark} -----------------------------
mi mixed <<'EOF'
25 0 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
26 25 0:22 / /proc rw - proc proc rw
27 25 0:21 / /sys rw shared:7 - sysfs sysfs rw
28 25 0:5 / /dev rw - devtmpfs udev rw
29 25 0:24 / /run rw shared:5 - tmpfs tmpfs rw
30 25 0:25 / /run/user/1000 rw - tmpfs tmpfs rw
31 25 8:32 / /mnt/ark rw,relatime - ext4 /dev/sdc rw
32 25 8:1 /home /mnt/homebind rw - ext4 /dev/sda1 rw
33 25 0:44 / /var/lib/docker/overlay2/abc/merged rw - overlay overlay rw
34 25 0:45 / /snap/core/1234 rw - squashfs /dev/loop0 rw
EOF
OUT=$(mountinfo_rows | filter_mounts)
ACCEPTED=$(printf '%s\n' "$OUT" | sed -n 's/^ok|[^|]*|\([^|]*\)|.*/\1/p' | sort | tr '\n' ' ')
assert_eq "/ /mnt/ark " "$ACCEPTED" "the accepted set is exactly {/, /mnt/ark}"
assert_contains "$OUT" "skip|/proc|" "/proc is rejected"
assert_contains "$OUT" "skip|/run|" "tmpfs on /run is rejected"
assert_contains "$OUT" "skip|/var/lib/docker/overlay2/abc/merged|" "/var/lib/docker is rejected"
assert_contains "$OUT" "skip|/snap/core/1234|" "/snap is rejected"
assert_contains "$OUT" "skip|/mnt/homebind|bind|" "a root != / bind subtree is rejected"

# --- 4. the same maj:min twice => one volume, warning naming both -------------
#
# mountinfo field 3 IS major:minor, so st_dev deduplication needs no stat(1) and
# no GNU-vs-BSD format problem. Keep the FIRST.
mi dupdev <<'EOF'
25 0 8:1 / / rw - ext4 /dev/sda1 rw
31 25 8:32 / /mnt/ark rw - ext4 /dev/sdc rw
32 25 8:32 / /mnt/ark-again rw - ext4 /dev/sdc rw
EOF
OUT=$(mountinfo_rows | filter_mounts)
assert_eq 1 "$(printf '%s\n' "$OUT" | grep -c '^ok|8:32|')" \
    "one maj:min mounted twice yields exactly one accepted mount"
assert_contains "$OUT" "ok|8:32|/mnt/ark|" "the FIRST of a duplicate maj:min pair is kept"
DUPLINE=$(printf '%s\n' "$OUT" | grep '^skip|/mnt/ark-again|')
assert_contains "$DUPLINE" "duplicate" "the duplicate is recorded with a duplicate reason"
assert_contains "$DUPLINE" "/mnt/ark" "the duplicate warning names the mount that was kept"
assert_contains "$DUPLINE" "8:32" "the duplicate warning names the shared maj:min"

# --- 5. the same mountpoint twice => the LAST one wins ------------------------
# A later mount on the same mountpoint is the visible one; the earlier is hidden
# underneath it and measuring it would report a filesystem nobody can reach.
mi dupmp <<'EOF'
25 0 8:1 / / rw - ext4 /dev/sda1 rw
31 25 8:32 / /mnt/ark rw - ext4 /dev/sdc rw
32 25 8:48 / /mnt/ark rw - xfs /dev/sdd rw
EOF
OUT=$(mountinfo_rows | filter_mounts)
assert_contains "$OUT" "ok|8:48|/mnt/ark|xfs" "the LAST mount on a mountpoint is the visible one"
assert_not_contains "$OUT" "ok|8:32|" "the shadowed earlier mount is not accepted"

# --- 6. network filesystems: opt-in only --------------------------------------
mi netfs <<'EOF'
25 0 8:1 / / rw - ext4 /dev/sda1 rw
31 25 0:60 / /mnt/nas rw - nfs4 srv:/export rw
32 25 0:61 / /mnt/win rw - cifs //srv/share rw
EOF
INCLUDE_NETWORK_FS=0
OUT=$(mountinfo_rows | filter_mounts)
assert_not_contains "$OUT" "ok|0:60|" "NFS is skipped by default"
assert_contains "$OUT" "skip|/mnt/nas|network|" "the NFS skip is recorded as a network skip"
assert_contains "$OUT" "statfs" "the network skip explains the blocking-statfs reason"
assert_contains "$OUT" "--include-network-fs" "the network skip names the opt-in flag"
assert_contains "$OUT" "skip|/mnt/win|network|" "CIFS is skipped by default"

INCLUDE_NETWORK_FS=1
OUT=$(mountinfo_rows | filter_mounts)
assert_contains "$OUT" "ok|0:60|/mnt/nas|nfs4" "--include-network-fs opts NFS in"
assert_contains "$OUT" "ok|0:61|/mnt/win|cifs" "--include-network-fs opts CIFS in"
INCLUDE_NETWORK_FS=0

# --- 7. fs_label --------------------------------------------------------------
assert_eq "root" "$(fs_label / 8:1)" "/ is labelled root, not empty"
assert_eq "ark" "$(fs_label /mnt/ark 8:32)" "the trailing segment is the label"
assert_eq "my-data" "$(fs_label '/mnt/my data' 8:16)" "a space becomes a dash"
assert_eq "backup-2024" "$(fs_label /mnt/Backup.2024 8:16)" \
    "uppercase is lowered and a dot is replaced"
assert_eq "a-b" "$(fs_label '/mnt/a  ...  b' 8:16)" "runs of replaced characters collapse"
assert_eq "data" "$(fs_label '/mnt/--data--' 8:16)" "leading and trailing dashes are stripped"
assert_eq "fs8-16" "$(fs_label '/mnt/...' 8:16)" \
    "a segment with nothing whitelistable falls back to fs<maj>-<min>"

# --- 8. end to end: detect_filesystems, labels, collisions, writability -------
#
# Mount points are PROBE paths while they are being tested for writability and
# EMIT paths once they reach compose.yaml. The fixture tree below is what _p()
# resolves them against.
ROOT="$TMP/fsroot"
mkdir -p "$ROOT/mnt/a/data" "$ROOT/mnt/b/data" "$ROOT/mnt/ark" "$ROOT/mnt/ro"
NETRA_SETUP_ROOT="$ROOT"
export NETRA_SETUP_ROOT

cat >"$TMP/mi.e2e" <<'EOF'
25 0 8:1 / / rw shared:1 - ext4 /dev/sda1 rw
31 25 8:32 / /mnt/ark rw - ext4 /dev/sdc rw
32 25 8:48 / /mnt/a/data rw - ext4 /dev/sdd rw
33 25 8:64 / /mnt/b/data rw - xfs /dev/sde rw
EOF
NETRA_MOUNTINFO_PATH="$TMP/mi.e2e"
export NETRA_MOUNTINFO_PATH
init_paths

detect_filesystems >/dev/null 2>&1
assert_contains "$FS_MOUNTS" "8:1|/|root" "/ is accepted and labelled root"
assert_contains "$FS_MOUNTS" "8:32|/mnt/ark|ark" "/mnt/ark is accepted and labelled ark"
assert_contains "$FS_MOUNTS" "8:48|/mnt/a/data|data" "the first /data wins the label data"
assert_contains "$FS_MOUNTS" "8:64|/mnt/b/data|data-2" "the colliding label gets -2"

# The emitted volume block: real host paths, never the fixture prefix, and a
# quoted source.
build_volume_block
assert_contains "$NETRA_BLK_VOLUMES" 'source: "/.netra"' "the root marker source is quoted"
assert_contains "$NETRA_BLK_VOLUMES" "target: /netra/fs/root" "/ maps to target /netra/fs/root"
assert_contains "$NETRA_BLK_VOLUMES" 'source: "/mnt/ark/.netra"' "the ark marker source is quoted"
assert_not_contains "$NETRA_BLK_VOLUMES" "$ROOT" \
    "NETRA_SETUP_ROOT never leaks into an emitted source path"

# A space in the mount point survives into a quoted source rather than breaking
# the YAML.
mkdir -p "$ROOT/mnt/my data"
cat >"$TMP/mi.space" <<'EOF'
25 0 8:1 / / rw - ext4 /dev/sda1 rw
30 25 8:16 / /mnt/my\040data rw - ext4 /dev/sdb rw
EOF
NETRA_MOUNTINFO_PATH="$TMP/mi.space"
export NETRA_MOUNTINFO_PATH
init_paths
detect_filesystems >/dev/null 2>&1
assert_contains "$FS_MOUNTS" "8:16|/mnt/my data|my-data" "a space in the mount point is labelled my-data"
build_volume_block
assert_contains "$NETRA_BLK_VOLUMES" 'source: "/mnt/my data/.netra"' \
    "a mount point containing a space renders as a quoted source"

# --- 9. an unwritable mount point is demoted BEFORE any prompting -------------
#
# `mkdir /mnt/ro/.netra` would fail halfway through the mutating phase. It is
# caught during detection instead, with access(W_OK) rather than a mkdir probe:
# a mkdir here would be a mutation outside netra_exec, which --dry-run must not
# perform.
#
# Root bypasses DAC permission bits entirely, so chmod 500 is not unwritable for
# uid 0 and this assertion would be a false failure in a root CI container.
if [ "$(id -u)" = 0 ]; then
    printf 'skip (running as root: chmod 500 is not unwritable for uid 0)\n'
else
    chmod 500 "$ROOT/mnt/ro"
    cat >"$TMP/mi.ro" <<'EOF'
25 0 8:1 / / rw - ext4 /dev/sda1 rw
31 25 8:80 / /mnt/ro rw - ext4 /dev/sdf rw
EOF
    NETRA_MOUNTINFO_PATH="$TMP/mi.ro"
    export NETRA_MOUNTINFO_PATH
    init_paths
    detect_filesystems >/dev/null 2>&1
    assert_not_contains "$FS_MOUNTS" "/mnt/ro" "an unwritable mount point is not accepted"
    assert_contains "$FS_SKIPS" "unwritable|/mnt/ro|" "it is demoted to a recorded skip"
    assert_contains "$SKIPPED_NOTES" "/mnt/ro" "the operator is told about it in the notes"
    chmod 700 "$ROOT/mnt/ro"
fi

# --- 10. a mount point that cannot be represented is rejected, not sanitised --
mkdir -p "$ROOT/mnt/qu"
cat >"$TMP/mi.bad" <<'EOF'
25 0 8:1 / / rw - ext4 /dev/sda1 rw
31 25 8:96 / /mnt/qu\042ote rw - ext4 /dev/sdg rw
32 25 8:97 / /mnt/dol\044lar rw - ext4 /dev/sdh rw
EOF
NETRA_MOUNTINFO_PATH="$TMP/mi.bad"
export NETRA_MOUNTINFO_PATH
init_paths
detect_filesystems >/dev/null 2>&1
assert_not_contains "$FS_MOUNTS" "ote" "a mount point containing a double quote is rejected"
assert_not_contains "$FS_MOUNTS" "lar" "a mount point containing a dollar sign is rejected"
assert_contains "$FS_SKIPS" "unsupported|" "the rejection is recorded as unsupported"

# --- 10b. SELinux enforcing warns about the relabel suffix --------------------
#
# On an enforcing host a bind mount from a container is denied unless the source
# carries the right label, and the failure mode is an agent that starts and
# reports nothing. Permissive must stay silent: warning on every permissive host
# is noise, and noise is what makes operators stop reading the notes.
SELROOT="$TMP/selroot"
mkdir -p "$SELROOT/sys/fs/selinux"
NETRA_SETUP_ROOT="$SELROOT"
export NETRA_SETUP_ROOT
init_paths

printf '0\n' >"$SELROOT/sys/fs/selinux/enforce"
SKIPPED_NOTES=""
check_selinux
assert_eq "" "$SKIPPED_NOTES" "SELinux in permissive mode says nothing"

printf '1\n' >"$SELROOT/sys/fs/selinux/enforce"
SKIPPED_NOTES=""
check_selinux
assert_contains "$SKIPPED_NOTES" "SELinux" "SELinux enforcing produces a warning"
assert_contains "$SKIPPED_NOTES" ":z" "the warning names the relabel suffix to add"

rm -rf "$SELROOT/sys/fs/selinux"
SKIPPED_NOTES=""
check_selinux
assert_eq "" "$SKIPPED_NOTES" "a host without SELinux at all says nothing"

# --- 11. zero accepted filesystems is legitimate, not an error ----------------
# A container-only host (§6.4) must still produce a working compose.
cat >"$TMP/mi.none" <<'EOF'
26 25 0:22 / /proc rw - proc proc rw
29 25 0:24 / /run rw - tmpfs tmpfs rw
EOF
NETRA_MOUNTINFO_PATH="$TMP/mi.none"
export NETRA_MOUNTINFO_PATH
init_paths
run_capture detect_filesystems
assert_eq 0 "$RUN_RC" "a host with no monitorable filesystem is not an error"

exit_case
