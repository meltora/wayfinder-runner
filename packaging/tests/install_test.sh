#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
INSTALLER="$REPO_ROOT/packaging/install.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM

FAKE_BIN="$TEST_ROOT/bin"
PAYLOAD="$TEST_ROOT/payload"
TARGET="$TEST_ROOT/target"
SYSTEMCTL_LOG="$TEST_ROOT/systemctl.log"
mkdir -p "$FAKE_BIN" "$PAYLOAD" "$TARGET/usr/bin"

fail() {
    printf 'install test failed: %s\n' "$*" >&2
    exit 1
}

file_mode() {
    if stat -f '%Lp' "$1" >/dev/null 2>&1; then
        stat -f '%Lp' "$1"
    else
        stat -c '%a' "$1"
    fi
}

cat > "$PAYLOAD/our-runner" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$PAYLOAD/our-runner-updater" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$PAYLOAD/our-runner" "$PAYLOAD/our-runner-updater"

cat > "$FAKE_BIN/id" <<'EOF'
#!/bin/sh
case "$*" in
    "-u") printf '%s\n' 0 ;;
    "-u our-runner") printf '%s\n' 991 ;;
    *) exec /usr/bin/id "$@" ;;
esac
EOF

cat > "$FAKE_BIN/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
    -s) printf '%s\n' Linux ;;
    *) exec /usr/bin/uname "$@" ;;
esac
EOF

cat > "$FAKE_BIN/chown" <<'EOF'
#!/bin/sh
exit 0
EOF

cat > "$FAKE_BIN/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$INSTALL_TEST_SYSTEMCTL_LOG"
case "$*" in
    "is-enabled "*|"is-active "*) exit 1 ;;
esac
if [ "${INSTALL_TEST_FAIL_SYSTEMCTL:-0}" = 1 ] && [ "${1:-}" = daemon-reload ]; then
    exit 1
fi
exit 0
EOF

cat > "$FAKE_BIN/systemd-tmpfiles" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$FAKE_BIN"/*

run_installer() {
    PATH="$FAKE_BIN:/usr/bin:/bin:/usr/sbin:/sbin" \
    DESTDIR="$TARGET" \
    INSTALL_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
    sh "$INSTALLER" --version "$1" "$PAYLOAD/our-runner" "$PAYLOAD/our-runner-updater"
}

# Migrate a legacy regular binary without changing configuration or runner state.
printf '%s\n' legacy-binary > "$TARGET/usr/bin/our-runner"
chmod 0755 "$TARGET/usr/bin/our-runner"
mkdir -p "$TARGET/etc/our-runner" "$TARGET/var/lib/our-runner"
printf '%s\n' existing-config > "$TARGET/etc/our-runner/config.yaml"
printf '%s\n' existing-state > "$TARGET/var/lib/our-runner/keep"

run_installer 1.2.3

SLOT="$TARGET/opt/our-runner/versions/1.2.3"
[ -f "$SLOT/our-runner" ] || fail 'runner binary was not installed in the version slot'
[ -f "$SLOT/our-runner-updater" ] || fail 'updater binary was not installed in the version slot'
[ "$(readlink "$TARGET/opt/our-runner/current")" = /opt/our-runner/versions/1.2.3 ] || fail 'current slot is not an atomic absolute link'
[ "$(readlink "$TARGET/opt/our-runner/updater-current")" = /opt/our-runner/versions/1.2.3/our-runner-updater ] || fail 'recovery updater is not pinned to the initial proven slot'
[ "$(readlink "$TARGET/usr/bin/our-runner")" = /opt/our-runner/current/our-runner ] || fail '/usr/bin compatibility link is incorrect'
[ "$(cat "$TARGET/opt/our-runner/migration/our-runner.pre-ab")" = legacy-binary ] || fail 'legacy binary backup was not preserved'
[ "$(cat "$TARGET/etc/our-runner/config.yaml")" = existing-config ] || fail 'runner configuration was overwritten'
[ "$(cat "$TARGET/var/lib/our-runner/keep")" = existing-state ] || fail 'runner state was overwritten'
[ -f "$TARGET/etc/our-runner/updater.yaml" ] || fail 'updater configuration was not created'
[ -f "$TARGET/etc/our-runner/update-trust.json" ] || fail 'update trust store was not created'
[ "$(file_mode "$TARGET/etc/our-runner/updater.yaml")" = 600 ] || fail 'updater configuration is not mode 0600'
[ "$(file_mode "$TARGET/etc/our-runner/update-trust.json")" = 600 ] || fail 'update trust store is not mode 0600'
grep -Fqx 'enabled: false' "$TARGET/etc/our-runner/updater.yaml" || fail 'automatic updates are not disabled by default'
grep -Fq '"keys": []' "$TARGET/etc/our-runner/update-trust.json" || fail 'installer fabricated a release trust key'
[ -d "$TARGET/var/lib/our-runner-updater" ] || fail 'updater state directory was not created'
grep -q 'our-runner-updater.socket' "$SYSTEMCTL_LOG" || fail 'updater socket was not enabled'
grep -q 'enable our-runner-updater.service' "$SYSTEMCTL_LOG" || fail 'updater recovery service was not enabled for boot'

# Reinstalling the same immutable version is idempotent and preserves operator files.
printf '%s\n' operator-updater-config > "$TARGET/etc/our-runner/updater.yaml"
run_installer 1.2.3
[ "$(cat "$TARGET/etc/our-runner/updater.yaml")" = operator-updater-config ] || fail 'updater configuration was overwritten'
[ "$(readlink "$TARGET/opt/our-runner/updater-current")" = /opt/our-runner/versions/1.2.3/our-runner-updater ] || fail 'idempotent install changed the recovery updater'

# Slots are immutable: a different binary cannot replace an existing version.
printf '%s\n' changed >> "$PAYLOAD/our-runner"
if run_installer 1.2.3 >/dev/null 2>&1; then
    fail 'installer replaced an immutable version slot'
fi
sed -i.bak '$d' "$PAYLOAD/our-runner"
rm -f "$PAYLOAD/our-runner.bak"

# A failure after switching slots restores the prior current link and removes the new slot.
if PATH="$FAKE_BIN:/usr/bin:/bin:/usr/sbin:/sbin" \
    DESTDIR="$TARGET" \
    INSTALL_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
    INSTALL_TEST_FAIL_SYSTEMCTL=1 \
    sh "$INSTALLER" --version 2.0.0 "$PAYLOAD/our-runner" "$PAYLOAD/our-runner-updater" >/dev/null 2>&1; then
    fail 'injected systemd failure was ignored'
fi
[ "$(readlink "$TARGET/opt/our-runner/current")" = /opt/our-runner/versions/1.2.3 ] || fail 'current link was not rolled back'
[ "$(readlink "$TARGET/opt/our-runner/updater-current")" = /opt/our-runner/versions/1.2.3/our-runner-updater ] || fail 'recovery updater link was not rolled back'
[ ! -e "$TARGET/opt/our-runner/versions/2.0.0" ] || fail 'failed version slot was not removed'
grep -q 'disable our-runner-updater.service' "$SYSTEMCTL_LOG" || fail 'failed install left recovery service enabled'
grep -q 'disable our-runner-updater.socket' "$SYSTEMCTL_LOG" || fail 'failed install left update socket enabled'

# A failed first migration restores the exact legacy regular binary rather than
# following the temporary compatibility symlink into the new slot.
PRIMARY_TARGET=$TARGET
TARGET="$TEST_ROOT/migration-failure"
mkdir -p "$TARGET/usr/bin"
printf '%s\n' second-legacy > "$TARGET/usr/bin/our-runner"
chmod 0755 "$TARGET/usr/bin/our-runner"
if PATH="$FAKE_BIN:/usr/bin:/bin:/usr/sbin:/sbin" \
    DESTDIR="$TARGET" \
    INSTALL_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
    INSTALL_TEST_FAIL_SYSTEMCTL=1 \
    sh "$INSTALLER" --version 1.2.3 "$PAYLOAD/our-runner" "$PAYLOAD/our-runner-updater" >/dev/null 2>&1; then
    fail 'injected migration failure was ignored'
fi
[ ! -L "$TARGET/usr/bin/our-runner" ] || fail 'legacy binary was restored as a symlink'
[ "$(cat "$TARGET/usr/bin/our-runner")" = second-legacy ] || fail 'legacy binary contents were not restored'
[ ! -L "$TARGET/opt/our-runner/updater-current" ] || fail 'failed migration left an updater-current link behind'
TARGET=$PRIMARY_TARGET

# Migrating an older A/B installation that has no updater-current link pins the
# recovery service to the already-active slot rather than the new candidate.
TARGET="$TEST_ROOT/ab-migration"
mkdir -p "$TARGET/opt/our-runner/versions/1.0.0" "$TARGET/usr/bin"
cp "$PAYLOAD/our-runner" "$TARGET/opt/our-runner/versions/1.0.0/our-runner"
cp "$PAYLOAD/our-runner-updater" "$TARGET/opt/our-runner/versions/1.0.0/our-runner-updater"
ln -s versions/1.0.0 "$TARGET/opt/our-runner/current"
ln -s /opt/our-runner/current/our-runner "$TARGET/usr/bin/our-runner"
run_installer 2.0.0
[ "$(readlink "$TARGET/opt/our-runner/current")" = /opt/our-runner/versions/2.0.0 ] || fail 'A/B migration did not install the candidate runner'
[ "$(readlink "$TARGET/opt/our-runner/updater-current")" = /opt/our-runner/versions/1.0.0/our-runner-updater ] || fail 'A/B migration did not preserve the proven updater'
TARGET=$PRIMARY_TARGET

# Legacy relative links remain accepted and are normalized without changing the
# proven updater slot selected by the prior installation.
TARGET="$TEST_ROOT/relative-link-migration"
mkdir -p "$TARGET/opt/our-runner/versions/1.0.0" "$TARGET/usr/bin"
cp "$PAYLOAD/our-runner" "$TARGET/opt/our-runner/versions/1.0.0/our-runner"
cp "$PAYLOAD/our-runner-updater" "$TARGET/opt/our-runner/versions/1.0.0/our-runner-updater"
ln -s versions/1.0.0 "$TARGET/opt/our-runner/current"
ln -s versions/1.0.0/our-runner-updater "$TARGET/opt/our-runner/updater-current"
ln -s /opt/our-runner/current/our-runner "$TARGET/usr/bin/our-runner"
run_installer 2.0.0
[ "$(readlink "$TARGET/opt/our-runner/updater-current")" = /opt/our-runner/versions/1.0.0/our-runner-updater ] || fail 'legacy relative updater link was not safely normalized'
TARGET=$PRIMARY_TARGET

# A later manual install may move the runner link, but must not run recovery from
# that unproven slot. Only the updater transaction promotes updater-current after
# the candidate runner reports healthy.
run_installer 2.0.0
[ "$(readlink "$TARGET/opt/our-runner/current")" = /opt/our-runner/versions/2.0.0 ] || fail 'later runner slot was not installed'
[ "$(readlink "$TARGET/opt/our-runner/updater-current")" = /opt/our-runner/versions/1.2.3/our-runner-updater ] || fail 'installer promoted an unproven updater binary'

# A hostile recovery symlink is rejected instead of being preserved or followed.
rm -f "$TARGET/opt/our-runner/updater-current"
ln -s '../../etc/passwd' "$TARGET/opt/our-runner/updater-current"
if run_installer 3.0.0 >/dev/null 2>&1; then
    fail 'unsafe updater-current target was accepted'
fi
rm -f "$TARGET/opt/our-runner/updater-current"
ln -s /opt/our-runner/versions/1.2.3/our-runner-updater "$TARGET/opt/our-runner/updater-current"

# Reject path traversal in the slot name and symlinked install boundaries.
if run_installer '../escape' >/dev/null 2>&1; then
    fail 'unsafe slot name was accepted'
fi

SYMLINK_TARGET="$TEST_ROOT/symlink-target"
mkdir -p "$SYMLINK_TARGET"
rm -rf "$TARGET/opt/our-runner"
ln -s "$SYMLINK_TARGET" "$TARGET/opt/our-runner"
if run_installer 3.0.0 >/dev/null 2>&1; then
    fail 'symlinked installation root was accepted'
fi

printf 'service installation tests passed\n'
