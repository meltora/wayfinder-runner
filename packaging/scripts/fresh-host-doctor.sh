#!/bin/sh
set -eu
umask 077

STATE_DIR=/var/lib/our-runner
RUNNER=/opt/our-runner/current/our-runner
CODE_TARGET="$STATE_DIR/enrollment-code"
OS_RELEASE=${FRESH_HOST_OS_RELEASE:-/etc/os-release}

VERSION=
INSTALLER=
INSTALLER_SHA256=
CONTROL_PLANE_URL=
CODE_FILE=
CONFIG=

usage() {
    echo "usage: $0 --version vX.Y.Z --installer FILE --installer-sha256 HEX --url HTTPS_ORIGIN --enrollment-code-file FILE [--config FILE]" >&2
    exit 2
}

fail() {
    printf 'fresh-host doctor failed: %s\n' "$*" >&2
    exit 1
}

while [ "$#" -gt 0 ]; do
    [ "$#" -ge 2 ] || usage
    case "$1" in
        --version) VERSION=$2 ;;
        --installer) INSTALLER=$2 ;;
        --installer-sha256) INSTALLER_SHA256=$2 ;;
        --url) CONTROL_PLANE_URL=$2 ;;
        --enrollment-code-file) CODE_FILE=$2 ;;
        --config) CONFIG=$2 ;;
        *) usage ;;
    esac
    shift 2
done

[ "$(id -u)" = 0 ] || fail "run as root on the fresh host"
printf '%s\n' "$VERSION" | grep -Eqx 'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(staging|develop)\.[1-9][0-9]*)?' || \
    fail "--version must be vX.Y.Z, vX.Y.Z-staging.N or vX.Y.Z-develop.N"
printf '%s\n' "$CONTROL_PLANE_URL" | grep -Eqx 'https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?/?' || \
    fail "--url must be an HTTPS origin"
printf '%s\n' "$INSTALLER_SHA256" | grep -Eqx '[a-f0-9]{64}' || fail "--installer-sha256 must be a lowercase SHA-256"
[ -f "$INSTALLER" ] && [ ! -L "$INSTALLER" ] || fail "--installer must name the downloaded release install.sh"
[ -f "$CODE_FILE" ] && [ ! -L "$CODE_FILE" ] || fail "--enrollment-code-file must name a regular file"
if [ -n "$CONFIG" ]; then
    case "$CONFIG" in
        /*) ;;
        *) fail "--config must be an absolute path" ;;
    esac
fi

[ -f "$OS_RELEASE" ] || fail "cannot read $OS_RELEASE"
os_id=$(sed -n 's/^ID=//p' "$OS_RELEASE" | tr -d '"' | head -n 1)
[ "$os_id" = ubuntu ] || fail "the first supported host is Ubuntu; this host reports ID=${os_id:-unknown}"

missing=
for tool in curl tar systemctl loginctl machinectl dockerd-rootless-setuptool.sh newuidmap newgidmap docker runuser sha256sum install; do
    command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
[ -z "$missing" ] || fail "missing commands:$missing (install docker-ce docker-ce-rootless-extras uidmap systemd-container first)"

WORK=$(mktemp -d)
code_installed=0
cleanup() {
    if [ "$code_installed" -eq 1 ]; then
        runuser -u our-runner -- rm -f "$CODE_TARGET" || rm -f "$CODE_TARGET"
        code_installed=0
    fi
    rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

PRIVATE_INSTALLER="$WORK/install.sh"
cat -- "$INSTALLER" > "$PRIVATE_INSTALLER"
actual_sha=$(sha256sum "$PRIVATE_INSTALLER" | awk '{print $1}')
[ "$actual_sha" = "$INSTALLER_SHA256" ] || fail "installer SHA-256 does not match the published release value"

OUR_RUNNER_VERSION=$VERSION OUR_RUNNER_SETUP_ROOTLESS_DOCKER=1 sh "$PRIVATE_INSTALLER" || fail "the release installer failed"

[ "$(systemctl is-enabled our-runner.service)" = enabled ] || fail "our-runner.service is not enabled"
[ "$(systemctl is-active our-runner-updater.socket)" = active ] || fail "our-runner-updater.socket is not active"

version_output=$(runuser -u our-runner -- "$RUNNER" version) || fail "the installed runner did not report its version"
installed=$(printf '%s\n' "$version_output" | sed -n 's/^our-runner //p' | head -n 1)
[ "$installed" = "${VERSION#v}" ] || fail "installed our-runner reports ${installed:-nothing}, expected ${VERSION#v}"
channel=$(printf '%s\n' "$version_output" | sed -n 's/^channel: //p' | head -n 1)
case "$channel" in
    production|staging|develop) ;;
    *) fail "installed our-runner is not a release build (channel ${channel:-unknown})" ;;
esac

code_installed=1
install -o our-runner -g our-runner -m 0600 "$CODE_FILE" "$CODE_TARGET"
enrolled=0
runuser -u our-runner -- "$RUNNER" enroll --state-dir "$STATE_DIR" --code-file "$CODE_TARGET" --url "$CONTROL_PLANE_URL" && enrolled=1
runuser -u our-runner -- rm -f "$CODE_TARGET" && code_installed=0
[ "$enrolled" -eq 1 ] || fail "enrollment failed; create a new one-time code in the dashboard and retry"

runner_uid=$(id -u our-runner)
if [ -n "$CONFIG" ]; then
    set -- --config "$CONFIG"
else
    set -- --state-dir "$STATE_DIR"
fi
REPORT="$WORK/doctor.report"
status=0
runuser -u our-runner -- env XDG_RUNTIME_DIR="/run/user/$runner_uid" DOCKER_HOST="unix:///run/user/$runner_uid/docker.sock" \
    "$RUNNER" doctor "$@" > "$REPORT" 2>&1 || status=$?
cat "$REPORT"

if [ "$status" -ne 0 ] || grep -q '^\[FAIL\]' "$REPORT"; then
    fail "doctor reported a failed check (exit $status)"
fi
grep -Eq '^\[OK  \] docker-mode: rootless docker ' "$REPORT" || fail "doctor did not confirm rootless Docker for the our-runner user"
warnings=$(grep -c '^\[WARN\]' "$REPORT" || true)
printf 'fresh-host doctor: green for our-runner %s (%s warnings listed above)\n' "$installed" "$warnings"
