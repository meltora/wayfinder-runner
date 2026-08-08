#!/bin/sh
set -eu
umask 077

DESTDIR=${DESTDIR:-}
if [ -z "$DESTDIR" ]; then
    PATH=/usr/sbin:/usr/bin:/sbin:/bin
    export PATH
fi

SETUP_ROOTLESS=0
SLOT=initial
RUNNER_SRC=
UPDATER_SRC=

usage() {
    echo "usage: $0 [--setup-rootless-docker] [--version VERSION] <our-runner> <our-runner-updater>" >&2
}

fail() {
    printf 'error: %s\n' "$*" >&2
    exit 1
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --setup-rootless-docker)
            SETUP_ROOTLESS=1
            ;;
        --version)
            shift
            [ "$#" -gt 0 ] || { usage; exit 2; }
            SLOT=$1
            ;;
        --*)
            fail "unknown option: $1"
            ;;
        *)
            if [ -z "$RUNNER_SRC" ]; then
                RUNNER_SRC=$1
            elif [ -z "$UPDATER_SRC" ]; then
                UPDATER_SRC=$1
            else
                usage
                exit 2
            fi
            ;;
    esac
    shift
done

case "$SLOT" in
    initial|[0-9]*.[0-9]*.[0-9]*) ;;
    *) fail "version must be 'initial' or a numeric semantic version" ;;
esac
printf '%s\n' "$SLOT" | grep -Eq '^(initial|[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?)$' || \
    fail "version contains unsafe characters"

if [ "$(id -u)" != 0 ]; then
    fail "run as root (sudo $0 [--version VERSION] <our-runner> <our-runner-updater>)"
fi

case "$(uname -s)" in
    Linux) ;;
    Darwin) fail "production service installation currently supports Linux only; use the macOS binary for development" ;;
    *) fail "unsupported operating system" ;;
esac

for source_file in "$RUNNER_SRC" "$UPDATER_SRC"; do
    [ -n "$source_file" ] || { usage; exit 2; }
    [ -f "$source_file" ] && [ ! -L "$source_file" ] || fail "binary source must be a regular, non-symlink file: $source_file"
done

if [ -n "$DESTDIR" ]; then
    case "$DESTDIR" in
        /*) ;;
        *) fail "DESTDIR must be absolute" ;;
    esac
    [ "$DESTDIR" != / ] || fail "DESTDIR=/ is not supported; leave it empty for a production installation"
    [ -d "$DESTDIR" ] && [ ! -L "$DESTDIR" ] || fail "DESTDIR must be a real directory"
    DESTDIR=${DESTDIR%/}
fi

root_path() {
    printf '%s%s\n' "$DESTDIR" "$1"
}

INSTALL_ROOT=$(root_path /opt/our-runner)
VERSIONS_DIR="$INSTALL_ROOT/versions"
CURRENT_LINK="$INSTALL_ROOT/current"
UPDATER_LINK="$INSTALL_ROOT/updater-current"
MIGRATION_DIR="$INSTALL_ROOT/migration"
BIN_LINK=$(root_path /usr/bin/our-runner)
CONFIG_DIR=$(root_path /etc/our-runner)
RUNNER_STATE=$(root_path /var/lib/our-runner)
UPDATER_STATE=$(root_path /var/lib/our-runner-updater)
LOG_DIR=$(root_path /var/log/our-runner)
SYSTEMD_DIR=$(root_path /etc/systemd/system)
TMPFILES_DIR=$(root_path /etc/tmpfiles.d)
TARGET_SLOT="$VERSIONS_DIR/$SLOT"
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

ensure_real_directory() {
    directory=$1
    mode=$2
    if [ -L "$directory" ]; then
        fail "refusing symlinked directory: $directory"
    fi
    install -d -m "$mode" "$directory"
    [ -d "$directory" ] && [ ! -L "$directory" ] || fail "unsafe directory boundary: $directory"
}

atomic_replace() {
    source_path=$1
    destination_path=$2
    # GNU mv uses -T to avoid following a destination symlink to a directory;
    # BSD mv provides the equivalent behavior with -h.
    if mv -Tf "$source_path" "$destination_path" 2>/dev/null; then
        return
    fi
    if mv -hf "$source_path" "$destination_path" 2>/dev/null; then
        return
    fi
    return 1
}

ensure_real_directory "$INSTALL_ROOT" 0755
ensure_real_directory "$VERSIONS_DIR" 0755
ensure_real_directory "$MIGRATION_DIR" 0700
ensure_real_directory "$(dirname "$BIN_LINK")" 0755
ensure_real_directory "$CONFIG_DIR" 0750
ensure_real_directory "$RUNNER_STATE" 0700
ensure_real_directory "$UPDATER_STATE" 0700
ensure_real_directory "$LOG_DIR" 0700
ensure_real_directory "$SYSTEMD_DIR" 0755
ensure_real_directory "$TMPFILES_DIR" 0755

if ! id -u our-runner >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/our-runner --shell /usr/sbin/nologin our-runner
fi
chown our-runner:our-runner "$RUNNER_STATE" "$LOG_DIR"
chown root:our-runner "$CONFIG_DIR"
chown root:root "$INSTALL_ROOT" "$VERSIONS_DIR" "$MIGRATION_DIR" "$UPDATER_STATE"
chmod 0755 "$INSTALL_ROOT" "$VERSIONS_DIR"
chmod 0700 "$MIGRATION_DIR" "$UPDATER_STATE" "$RUNNER_STATE" "$LOG_DIR"
chmod 0750 "$CONFIG_DIR"

CREATED_CONFIG=0
CREATED_UPDATER_CONFIG=0
CREATED_TRUST=0
ROLLBACK_DIR="$INSTALL_ROOT/.install-rollback.$$"
ensure_real_directory "$ROLLBACK_DIR" 0700
NEW_SLOT=0
PREVIOUS_CURRENT_STATE=absent
PREVIOUS_CURRENT_TARGET=
PREVIOUS_CURRENT_SLOT=
PREVIOUS_UPDATER_STATE=absent
PREVIOUS_UPDATER_TARGET=
PREVIOUS_UPDATER_SLOT=
NORMALIZE_UPDATER_LINK=0
PREVIOUS_BIN_STATE=absent
PREVIOUS_BIN_TARGET=
LEGACY_BACKUP="$MIGRATION_DIR/our-runner.pre-ab"
UNITS_CHANGED=0
TMPFILES_CHANGED=0
COMMITTED=0
RUNNER_WAS_ENABLED=0
UPDATER_WAS_ENABLED=0
SOCKET_WAS_ENABLED=0
SOCKET_WAS_ACTIVE=0
if systemctl is-enabled our-runner.service >/dev/null 2>&1; then RUNNER_WAS_ENABLED=1; fi
if systemctl is-enabled our-runner-updater.service >/dev/null 2>&1; then UPDATER_WAS_ENABLED=1; fi
if systemctl is-enabled our-runner-updater.socket >/dev/null 2>&1; then SOCKET_WAS_ENABLED=1; fi
if systemctl is-active our-runner-updater.socket >/dev/null 2>&1; then SOCKET_WAS_ACTIVE=1; fi

if [ -L "$CURRENT_LINK" ]; then
    PREVIOUS_CURRENT_STATE=symlink
    PREVIOUS_CURRENT_TARGET=$(readlink "$CURRENT_LINK")
    case "$PREVIOUS_CURRENT_TARGET" in
        versions/*)
            previous_slot=${PREVIOUS_CURRENT_TARGET#versions/}
            ;;
        /opt/our-runner/versions/*)
            previous_slot=${PREVIOUS_CURRENT_TARGET#/opt/our-runner/versions/}
            ;;
        *) fail "existing current link must target a version slot" ;;
    esac
    printf '%s\n' "$previous_slot" | grep -Eq '^(initial|[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?)$' || \
        fail "existing current link has an unsafe version target"
    PREVIOUS_CURRENT_SLOT=$previous_slot
elif [ -e "$CURRENT_LINK" ]; then
    fail "current must be a symlink"
fi

if [ -L "$UPDATER_LINK" ]; then
    PREVIOUS_UPDATER_STATE=symlink
    PREVIOUS_UPDATER_TARGET=$(readlink "$UPDATER_LINK")
    case "$PREVIOUS_UPDATER_TARGET" in
        versions/*/our-runner-updater)
            previous_updater_slot=${PREVIOUS_UPDATER_TARGET#versions/}
            previous_updater_slot=${previous_updater_slot%/our-runner-updater}
            NORMALIZE_UPDATER_LINK=1
            ;;
        /opt/our-runner/versions/*/our-runner-updater)
            previous_updater_slot=${PREVIOUS_UPDATER_TARGET#/opt/our-runner/versions/}
            previous_updater_slot=${previous_updater_slot%/our-runner-updater}
            ;;
        *) fail "existing updater-current link must target a versioned updater binary" ;;
    esac
    printf '%s\n' "$previous_updater_slot" | grep -Eq '^(initial|[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?)$' || \
        fail "existing updater-current link has an unsafe version target"
    [ -f "$VERSIONS_DIR/$previous_updater_slot/our-runner-updater" ] && \
        [ ! -L "$VERSIONS_DIR/$previous_updater_slot/our-runner-updater" ] || \
        fail "existing updater-current link does not resolve to a regular updater binary"
    PREVIOUS_UPDATER_SLOT=$previous_updater_slot
elif [ -e "$UPDATER_LINK" ]; then
    fail "updater-current must be a symlink"
fi

if [ -L "$BIN_LINK" ]; then
    PREVIOUS_BIN_STATE=symlink
    PREVIOUS_BIN_TARGET=$(readlink "$BIN_LINK")
    [ "$PREVIOUS_BIN_TARGET" = /opt/our-runner/current/our-runner ] || \
        fail "existing /usr/bin/our-runner symlink has an unexpected target"
elif [ -e "$BIN_LINK" ]; then
    [ -f "$BIN_LINK" ] || fail "legacy /usr/bin/our-runner is not a regular file"
    PREVIOUS_BIN_STATE=regular
    install -m 0755 "$BIN_LINK" "$ROLLBACK_DIR/legacy-current"
    if [ ! -e "$LEGACY_BACKUP" ]; then
        install -m 0755 "$BIN_LINK" "$LEGACY_BACKUP"
    elif [ -L "$LEGACY_BACKUP" ] || [ ! -f "$LEGACY_BACKUP" ]; then
        fail "legacy backup path is unsafe"
    fi
fi

restore_link() {
    path=$1
    state=$2
    target=$3
    temporary="$path.rollback.$$"
    rm -f "$temporary"
    case "$state" in
        symlink)
            ln -s "$target" "$temporary"
            atomic_replace "$temporary" "$path" || return 1
            ;;
        absent)
            rm -f "$path"
            ;;
    esac
}

rollback() {
    status=$?
    trap - EXIT HUP INT TERM
    if [ "$COMMITTED" -ne 1 ]; then
        restore_link "$CURRENT_LINK" "$PREVIOUS_CURRENT_STATE" "$PREVIOUS_CURRENT_TARGET" || true
        restore_link "$UPDATER_LINK" "$PREVIOUS_UPDATER_STATE" "$PREVIOUS_UPDATER_TARGET" || true
        case "$PREVIOUS_BIN_STATE" in
            regular)
                legacy_tmp="$BIN_LINK.rollback.$$"
                rm -f "$legacy_tmp"
                install -m 0755 "$ROLLBACK_DIR/legacy-current" "$legacy_tmp" || true
                atomic_replace "$legacy_tmp" "$BIN_LINK" || true
                ;;
            *)
                restore_link "$BIN_LINK" "$PREVIOUS_BIN_STATE" "$PREVIOUS_BIN_TARGET" || true
                ;;
        esac
        if [ "$UNITS_CHANGED" -eq 1 ]; then
            for unit in our-runner.service our-runner-updater.service our-runner-updater.socket; do
                if [ -f "$ROLLBACK_DIR/$unit" ]; then
                    install -m 0644 "$ROLLBACK_DIR/$unit" "$SYSTEMD_DIR/$unit" || true
                else
                    rm -f "$SYSTEMD_DIR/$unit" || true
                fi
            done
            systemctl daemon-reload >/dev/null 2>&1 || true
            if [ "$SOCKET_WAS_ACTIVE" -eq 0 ]; then systemctl stop our-runner-updater.socket >/dev/null 2>&1 || true; fi
            if [ "$SOCKET_WAS_ENABLED" -eq 0 ]; then systemctl disable our-runner-updater.socket >/dev/null 2>&1 || true; fi
            if [ "$UPDATER_WAS_ENABLED" -eq 0 ]; then systemctl disable our-runner-updater.service >/dev/null 2>&1 || true; fi
            if [ "$RUNNER_WAS_ENABLED" -eq 0 ]; then systemctl disable our-runner.service >/dev/null 2>&1 || true; fi
        fi
        if [ "$TMPFILES_CHANGED" -eq 1 ]; then
            if [ -f "$ROLLBACK_DIR/our-runner-updater.conf" ]; then
                install -m 0644 "$ROLLBACK_DIR/our-runner-updater.conf" "$TMPFILES_DIR/our-runner-updater.conf" || true
            else
                rm -f "$TMPFILES_DIR/our-runner-updater.conf" || true
            fi
        fi
        [ "$NEW_SLOT" -eq 0 ] || rm -rf "$TARGET_SLOT"
        [ "$CREATED_CONFIG" -eq 0 ] || rm -f "$CONFIG_DIR/config.yaml"
        [ "$CREATED_UPDATER_CONFIG" -eq 0 ] || rm -f "$CONFIG_DIR/updater.yaml"
        [ "$CREATED_TRUST" -eq 0 ] || rm -f "$CONFIG_DIR/update-trust.json"
        sync -f "$INSTALL_ROOT" >/dev/null 2>&1 || true
        sync -f "$(dirname "$BIN_LINK")" >/dev/null 2>&1 || true
    fi
    rm -rf "$ROLLBACK_DIR"
    exit "$status"
}
trap rollback EXIT HUP INT TERM

if [ ! -e "$CONFIG_DIR/config.yaml" ]; then
    install -m 0640 "$SCRIPT_DIR/../config.example.yaml" "$CONFIG_DIR/config.yaml"
    CREATED_CONFIG=1
    echo "created /etc/our-runner/config.yaml — edit control_plane_url before enrolling"
elif [ -L "$CONFIG_DIR/config.yaml" ] || [ ! -f "$CONFIG_DIR/config.yaml" ]; then
    fail "runner configuration must be a regular file"
fi
chown root:our-runner "$CONFIG_DIR/config.yaml"
chmod 0640 "$CONFIG_DIR/config.yaml"

if [ ! -e "$CONFIG_DIR/updater.yaml" ]; then
    install -m 0600 "$SCRIPT_DIR/updater.yaml" "$CONFIG_DIR/updater.yaml"
    CREATED_UPDATER_CONFIG=1
elif [ -L "$CONFIG_DIR/updater.yaml" ] || [ ! -f "$CONFIG_DIR/updater.yaml" ]; then
    fail "updater configuration must be a regular file"
fi
chown root:root "$CONFIG_DIR/updater.yaml"
chmod 0600 "$CONFIG_DIR/updater.yaml"

if [ ! -e "$CONFIG_DIR/update-trust.json" ]; then
    install -m 0600 "$SCRIPT_DIR/update-trust.json" "$CONFIG_DIR/update-trust.json"
    CREATED_TRUST=1
elif [ -L "$CONFIG_DIR/update-trust.json" ] || [ ! -f "$CONFIG_DIR/update-trust.json" ]; then
    fail "update trust store must be a regular file"
fi
chown root:root "$CONFIG_DIR/update-trust.json"
chmod 0600 "$CONFIG_DIR/update-trust.json"

if [ -e "$TARGET_SLOT" ]; then
    [ -d "$TARGET_SLOT" ] && [ ! -L "$TARGET_SLOT" ] || fail "version slot is not a real directory"
    for binary in our-runner our-runner-updater; do
        [ -f "$TARGET_SLOT/$binary" ] && [ ! -L "$TARGET_SLOT/$binary" ] || fail "version slot is incomplete"
    done
    cmp -s "$RUNNER_SRC" "$TARGET_SLOT/our-runner" || fail "version slot already exists with a different runner binary"
    cmp -s "$UPDATER_SRC" "$TARGET_SLOT/our-runner-updater" || fail "version slot already exists with a different updater binary"
else
    STAGED_SLOT="$VERSIONS_DIR/.install-$SLOT.$$"
    ensure_real_directory "$STAGED_SLOT" 0755
    install -m 0755 "$RUNNER_SRC" "$STAGED_SLOT/our-runner"
    install -m 0755 "$UPDATER_SRC" "$STAGED_SLOT/our-runner-updater"
    chown root:root "$STAGED_SLOT/our-runner" "$STAGED_SLOT/our-runner-updater"
    mv "$STAGED_SLOT" "$TARGET_SLOT"
    NEW_SLOT=1
fi
sync -f "$VERSIONS_DIR"

CURRENT_TMP="$INSTALL_ROOT/.current.$$"
rm -f "$CURRENT_TMP"
ln -s "/opt/our-runner/versions/$SLOT" "$CURRENT_TMP"
atomic_replace "$CURRENT_TMP" "$CURRENT_LINK" || fail "platform mv cannot atomically replace current"
sync -f "$INSTALL_ROOT"

if [ "$PREVIOUS_UPDATER_STATE" = absent ] || [ "$NORMALIZE_UPDATER_LINK" -eq 1 ]; then
    INITIAL_UPDATER_SLOT=$SLOT
    if [ "$PREVIOUS_CURRENT_STATE" = symlink ] && \
        [ -f "$VERSIONS_DIR/$PREVIOUS_CURRENT_SLOT/our-runner-updater" ] && \
        [ ! -L "$VERSIONS_DIR/$PREVIOUS_CURRENT_SLOT/our-runner-updater" ]; then
        INITIAL_UPDATER_SLOT=$PREVIOUS_CURRENT_SLOT
    fi
    if [ "$PREVIOUS_UPDATER_STATE" = symlink ]; then
        INITIAL_UPDATER_SLOT=$PREVIOUS_UPDATER_SLOT
    fi
    INITIAL_UPDATER_TARGET="/opt/our-runner/versions/$INITIAL_UPDATER_SLOT/our-runner-updater"
    UPDATER_TMP="$INSTALL_ROOT/.updater-current.$$"
    rm -f "$UPDATER_TMP"
    ln -s "$INITIAL_UPDATER_TARGET" "$UPDATER_TMP"
    atomic_replace "$UPDATER_TMP" "$UPDATER_LINK" || fail "platform mv cannot atomically replace updater-current"
    sync -f "$INSTALL_ROOT"
fi

BIN_TMP="$BIN_LINK.install.$$"
rm -f "$BIN_TMP"
ln -s /opt/our-runner/current/our-runner "$BIN_TMP"
atomic_replace "$BIN_TMP" "$BIN_LINK" || fail "platform mv cannot atomically replace /usr/bin/our-runner"
sync -f "$(dirname "$BIN_LINK")"

for unit in our-runner.service our-runner-updater.service our-runner-updater.socket; do
    if [ -f "$SYSTEMD_DIR/$unit" ] && [ ! -L "$SYSTEMD_DIR/$unit" ]; then
        install -m 0600 "$SYSTEMD_DIR/$unit" "$ROLLBACK_DIR/$unit"
    elif [ -e "$SYSTEMD_DIR/$unit" ] || [ -L "$SYSTEMD_DIR/$unit" ]; then
        fail "systemd unit path is not a regular file: $unit"
    fi
    install -m 0644 "$SCRIPT_DIR/systemd/$unit" "$SYSTEMD_DIR/$unit"
    chown root:root "$SYSTEMD_DIR/$unit"
done
UNITS_CHANGED=1

if [ -f "$TMPFILES_DIR/our-runner-updater.conf" ] && [ ! -L "$TMPFILES_DIR/our-runner-updater.conf" ]; then
    install -m 0600 "$TMPFILES_DIR/our-runner-updater.conf" "$ROLLBACK_DIR/our-runner-updater.conf"
elif [ -e "$TMPFILES_DIR/our-runner-updater.conf" ] || [ -L "$TMPFILES_DIR/our-runner-updater.conf" ]; then
    fail "tmpfiles configuration path is not a regular file"
fi
install -m 0644 "$SCRIPT_DIR/tmpfiles/our-runner-updater.conf" "$TMPFILES_DIR/our-runner-updater.conf"
chown root:root "$TMPFILES_DIR/our-runner-updater.conf"
TMPFILES_CHANGED=1
sync -f "$CONFIG_DIR"
sync -f "$SYSTEMD_DIR"
sync -f "$TMPFILES_DIR"

if [ "$SETUP_ROOTLESS" -eq 1 ]; then
    command -v dockerd-rootless-setuptool.sh >/dev/null 2>&1 || fail "dockerd-rootless-setuptool.sh not found; install Docker rootless mode first"
    command -v machinectl >/dev/null 2>&1 || fail "machinectl is required for unattended rootless Docker setup"
    loginctl enable-linger our-runner || true
    machinectl shell our-runner@ /usr/bin/dockerd-rootless-setuptool.sh install
fi

systemctl daemon-reload
if [ -z "$DESTDIR" ]; then
    systemd-tmpfiles --create our-runner-updater.conf
fi
systemctl enable our-runner.service
systemctl enable our-runner-updater.service
systemctl enable --now our-runner-updater.socket

COMMITTED=1
rm -rf "$ROLLBACK_DIR"
trap - EXIT HUP INT TERM

echo "installed our-runner in /opt/our-runner/versions/$SLOT"
echo "automatic updates are fail-closed until /etc/our-runner/update-trust.json is provisioned and enabled is set in updater.yaml"
echo "next steps:"
echo "  1. edit /etc/our-runner/config.yaml and enroll the runner"
echo "  2. provision the update trust policy, then enable updates"
echo "  3. restart our-runner-updater.service after enabling updates"
echo "  4. run doctor and start our-runner.service"
