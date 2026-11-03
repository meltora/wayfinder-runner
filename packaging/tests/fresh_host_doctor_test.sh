#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/../.." && pwd)
CHECK="$REPO_ROOT/packaging/scripts/fresh-host-doctor.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM

FAKE_BIN="$TEST_ROOT/bin"
SYS_BIN="$TEST_ROOT/sysbin"
LOG="$TEST_ROOT/calls.log"
mkdir -p "$FAKE_BIN" "$SYS_BIN"

fail() {
    printf 'fresh host doctor test failed: %s\n' "$*" >&2
    exit 1
}

for tool in sh cat grep sed awk mktemp rm tr wc head chmod mkdir env dirname cut sha256sum basename; do
    real=$(command -v "$tool") || fail "test host lacks $tool"
    ln -s "$real" "$SYS_BIN/$tool"
done

stub() {
    printf '#!/bin/sh\n%s\n' "$2" > "$FAKE_BIN/$1"
    chmod 0755 "$FAKE_BIN/$1"
}

stub id 'case "$*" in "-u") printf "%s\n" "${FRESH_TEST_EUID:-0}" ;; "-u our-runner") printf "%s\n" 991 ;; *) exit 1 ;; esac'
stub install 'printf "install %s\n" "$*" >> "$FRESH_TEST_LOG"'
stub systemctl 'case "$*" in "is-enabled our-runner.service") printf "enabled\n" ;; "is-active our-runner-updater.socket") printf "active\n" ;; *) exit 1 ;; esac'
for tool in curl tar machinectl loginctl dockerd-rootless-setuptool.sh newuidmap newgidmap docker; do
    stub "$tool" 'exit 0'
done
cat > "$FAKE_BIN/runuser" <<'EOF'
#!/bin/sh
printf 'runuser %s\n' "$*" >> "$FRESH_TEST_LOG"
[ "$1" = -u ] && [ "$2" = our-runner ] && [ "$3" = -- ] || exit 97
case "$*" in
    *" version") printf 'our-runner %s\nchannel: %s\n' "${FRESH_TEST_INSTALLED_VERSION:-1.2.3}" "${FRESH_TEST_CHANNEL:-production}" ;;
    *" doctor "*) cat "$FRESH_TEST_DOCTOR_OUTPUT"; exit "${FRESH_TEST_DOCTOR_STATUS:-0}" ;;
    *) exit 0 ;;
esac
EOF
chmod 0755 "$FAKE_BIN/runuser"

INSTALLER="$TEST_ROOT/install.sh"
cat > "$INSTALLER" <<'EOF'
#!/bin/sh
printf 'installer version=%s rootless=%s\n' "$OUR_RUNNER_VERSION" "$OUR_RUNNER_SETUP_ROOTLESS_DOCKER" >> "$FRESH_TEST_LOG"
EOF
INSTALLER_SHA=$(sha256sum "$INSTALLER" | awk '{print $1}')
CODE="$TEST_ROOT/enrollment-code"
printf '%s\n' 'one-time-code' > "$CODE"
OS_RELEASE="$TEST_ROOT/os-release"
DOCTOR_OUTPUT="$TEST_ROOT/doctor.out"

green_doctor() {
    cat > "$DOCTOR_OUTPUT" <<'EOF'
[OK  ] control-plane-dns: control.example.test resolves
[OK  ] control-plane-tcp: reachable
[OK  ] certificate: runner certificate valid
[OK  ] git: /usr/bin/git
[OK  ] docker: /usr/bin/docker
[OK  ] docker-mode: rootless docker 28.0.1
[WARN] restricted-egress: labels are not proof that egress is enforced on this host
[OK  ] disk: /var/lib/our-runner writable
EOF
}

run_check() {
    : > "$LOG"
    env -i PATH="$FAKE_BIN:$SYS_BIN" FRESH_TEST_LOG="$LOG" FRESH_TEST_DOCTOR_OUTPUT="$DOCTOR_OUTPUT" \
        FRESH_TEST_DOCTOR_STATUS="${DOCTOR_STATUS:-0}" FRESH_TEST_EUID="${EUID_OVERRIDE:-0}" \
        FRESH_TEST_INSTALLED_VERSION="${INSTALLED_VERSION:-1.2.3}" FRESH_TEST_CHANNEL="${INSTALLED_CHANNEL:-production}" \
        FRESH_HOST_OS_RELEASE="$OS_RELEASE" \
        sh "$CHECK" --version "${VERSION_ARG:-v1.2.3}" --installer "$INSTALLER" \
        --installer-sha256 "${SHA_ARG:-$INSTALLER_SHA}" --url "${URL_ARG:-https://control.example.test}" \
        --enrollment-code-file "$CODE" "$@" > "$TEST_ROOT/out" 2>&1
}

expect_refused() {
    description=$1
    message=$2
    shift 2
    if run_check "$@"; then
        fail "$description was accepted: $(cat "$TEST_ROOT/out")"
    fi
    grep -Fq -- "$message" "$TEST_ROOT/out" || fail "$description: expected '$message', got: $(cat "$TEST_ROOT/out")"
}

reset() {
    printf 'ID=ubuntu\nVERSION_ID="24.04"\n' > "$OS_RELEASE"
    green_doctor
    for tool in machinectl newuidmap; do
        stub "$tool" 'exit 0'
    done
}

reset
run_check || fail "green host refused: $(cat "$TEST_ROOT/out")"
grep -Fq 'fresh-host doctor: green for our-runner 1.2.3' "$TEST_ROOT/out" || fail "green verdict missing: $(cat "$TEST_ROOT/out")"
grep -Fq 'restricted-egress' "$TEST_ROOT/out" || fail "warnings are not reported"
grep -Fqx 'installer version=v1.2.3 rootless=1' "$LOG" || fail "installer did not run pinned with rootless Docker setup: $(cat "$LOG")"
grep -Fq 'enroll --state-dir /var/lib/our-runner --code-file /var/lib/our-runner/enrollment-code --url https://control.example.test' "$LOG" || \
    fail "enrollment did not run as our-runner with the code file: $(cat "$LOG")"
grep -Fq 'XDG_RUNTIME_DIR=/run/user/991 DOCKER_HOST=unix:///run/user/991/docker.sock /opt/our-runner/current/our-runner doctor --state-dir /var/lib/our-runner' "$LOG" || \
    fail "doctor did not use the runner user's rootless Docker socket: $(cat "$LOG")"
grep -Fq 'rm -f /var/lib/our-runner/enrollment-code' "$LOG" || fail "the enrollment code was not removed"
installer_line=$(grep -n '^installer ' "$LOG" | cut -d: -f1)
doctor_line=$(grep -n ' doctor ' "$LOG" | cut -d: -f1)
[ "$installer_line" -lt "$doctor_line" ] || fail "doctor ran before the installer"

reset
run_check --config /etc/our-runner/config.yaml || fail "--config run refused: $(cat "$TEST_ROOT/out")"
grep -Fq 'our-runner doctor --config /etc/our-runner/config.yaml' "$LOG" || fail "--config was not passed to doctor"

reset
cat >> "$DOCTOR_OUTPUT" <<'EOF'
[FAIL] provider-cli: claude is not authenticated
EOF
DOCTOR_STATUS=1 expect_refused "a doctor failure" "doctor reported a failed check"

reset
cat >> "$DOCTOR_OUTPUT" <<'EOF'
[FAIL] provider-cli: claude is not authenticated
EOF
expect_refused "a FAIL line with a zero doctor exit" "doctor reported a failed check"

reset
sed 's/^\[OK  \] docker-mode: rootless docker 28.0.1$/[WARN] docker-mode: Docker Desktop VM 28.0.1 is supported for development, not the Linux production gate/' "$DOCTOR_OUTPUT" > "$TEST_ROOT/doctor.tmp"
cat "$TEST_ROOT/doctor.tmp" > "$DOCTOR_OUTPUT"
expect_refused "a host without rootless Docker" "doctor did not confirm rootless Docker"

reset
SHA_ARG=0000000000000000000000000000000000000000000000000000000000000000 expect_refused "a modified installer" "installer SHA-256 does not match"
grep -q '^installer ' "$LOG" && fail "the installer ran despite a checksum mismatch"

reset
printf 'ID=debian\n' > "$OS_RELEASE"
expect_refused "a non-Ubuntu host" "Ubuntu"

reset
rm "$FAKE_BIN/machinectl" "$FAKE_BIN/newuidmap"
expect_refused "a host without rootless prerequisites" "missing commands: machinectl newuidmap"
grep -q '^installer ' "$LOG" && fail "the installer ran without rootless prerequisites"

reset
INSTALLED_VERSION=1.2.2 expect_refused "an installed version that differs" "installed our-runner reports 1.2.2, expected 1.2.3"

reset
INSTALLED_CHANNEL=source expect_refused "a source build" "installed our-runner is not a release build"

reset
EUID_OVERRIDE=1000 expect_refused "a non-root run" "run as root"

reset
URL_ARG=http://control.example.test expect_refused "an http control plane" "--url must be an HTTPS origin"

reset
VERSION_ARG=latest expect_refused "an unpinned version" "--version must be vX.Y.Z"

printf 'fresh host doctor tests passed\n'
