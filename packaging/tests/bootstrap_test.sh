#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
BOOTSTRAP="$REPO_ROOT/packaging/bootstrap.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM

FIXTURES="$TEST_ROOT/fixtures"
PAYLOAD="$TEST_ROOT/payload"
FAKE_BIN="$TEST_ROOT/bin"
MARKER="$TEST_ROOT/installed"
COSIGN_LOG="$TEST_ROOT/cosign.log"
mkdir -p "$FIXTURES" "$PAYLOAD/packaging" "$FAKE_BIN"

cat > "$PAYLOAD/our-runner" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$PAYLOAD/our-runner"

cat > "$PAYLOAD/our-runner-updater" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$PAYLOAD/our-runner-updater"

cat > "$PAYLOAD/packaging/install.sh" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" > "$BOOTSTRAP_TEST_MARKER"
EOF
chmod 0755 "$PAYLOAD/packaging/install.sh"

ARTIFACT=our-runner_1.2.3_linux_amd64.tar.gz
tar -C "$PAYLOAD" -czf "$FIXTURES/$ARTIFACT" our-runner our-runner-updater packaging/install.sh
DARWIN_ARTIFACT=our-runner_1.2.3_darwin_arm64.tar.gz
tar -C "$PAYLOAD" -czf "$FIXTURES/$DARWIN_ARTIFACT" our-runner
if command -v sha256sum >/dev/null 2>&1; then
    DIGEST=$(sha256sum "$FIXTURES/$ARTIFACT" | awk '{print $1}')
    DARWIN_DIGEST=$(sha256sum "$FIXTURES/$DARWIN_ARTIFACT" | awk '{print $1}')
else
    DIGEST=$(shasum -a 256 "$FIXTURES/$ARTIFACT" | awk '{print $1}')
    DARWIN_DIGEST=$(shasum -a 256 "$FIXTURES/$DARWIN_ARTIFACT" | awk '{print $1}')
fi
printf '%s  %s\n' "$DIGEST" "$ARTIFACT" > "$FIXTURES/checksums.txt"
printf '%s  %s\n' "$DARWIN_DIGEST" "$DARWIN_ARTIFACT" >> "$FIXTURES/checksums.txt"
printf '%s\n' '{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}' > "$FIXTURES/checksums.txt.sigstore.json"

cat > "$FAKE_BIN/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
    -s) printf '%s\n' "${BOOTSTRAP_TEST_OS:-Linux}" ;;
    -m) printf '%s\n' "${BOOTSTRAP_TEST_ARCH:-x86_64}" ;;
    *) exit 2 ;;
esac
EOF

cat > "$FAKE_BIN/id" <<'EOF'
#!/bin/sh
test "${1:-}" = -u
printf '%s\n' 1000
EOF

cat > "$FAKE_BIN/sudo" <<'EOF'
#!/bin/sh
case "${1:-}" in
    mkdir) exit 0 ;;
    install)
        printf '%s\n' "$*" > "$BOOTSTRAP_TEST_MARKER"
        exit 0
        ;;
esac
exec "$@"
EOF

cat > "$FAKE_BIN/cosign" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" > "$BOOTSTRAP_TEST_COSIGN_LOG"
if [ "${BOOTSTRAP_TEST_COSIGN_FAIL:-0}" = 1 ]; then
    exit 1
fi
EOF

cat > "$FAKE_BIN/curl" <<'EOF'
#!/bin/sh
set -eu
OUTPUT=
URL=
HEAD=0
while [ "$#" -gt 0 ]; do
    case "$1" in
        --output|-o)
            shift
            OUTPUT=${1:-}
            ;;
        --connect-timeout|--max-time|--max-filesize|--retry|--proto|--proto-redir|--write-out)
            shift
            ;;
        --head)
            HEAD=1
            ;;
        --fail|--silent|--show-error|--location)
            ;;
        *)
            URL=$1
            ;;
    esac
    shift
done
test -n "$OUTPUT"
test -n "$URL"
if [ "$HEAD" -eq 1 ]; then
    printf '%s' "${BOOTSTRAP_TEST_LATEST_URL:-https://github.com/acme/app-runner/releases/tag/v1.2.3}"
    exit 0
fi
cp "$BOOTSTRAP_TEST_FIXTURES/${URL##*/}" "$OUTPUT"
EOF
chmod 0755 "$FAKE_BIN"/*

run_installer() {
    PATH="$FAKE_BIN:/usr/bin:/bin:/usr/sbin:/sbin" \
    OUR_RUNNER_RELEASE_REPOSITORY=acme/app-runner \
    OUR_RUNNER_VERSION=v1.2.3 \
    BOOTSTRAP_TEST_FIXTURES="$FIXTURES" \
    BOOTSTRAP_TEST_MARKER="$MARKER" \
    BOOTSTRAP_TEST_COSIGN_LOG="$COSIGN_LOG" \
    sh "$BOOTSTRAP"
}

run_installer
test -s "$MARKER"
grep -q -- '--version 1.2.3' "$MARKER"
grep -q -- 'our-runner-updater' "$MARKER"
grep -q -- '--bundle' "$COSIGN_LOG"
grep -q -- 'https://github.com/acme/app-runner/.github/workflows/release.yml@refs/tags/v1.2.3' "$COSIGN_LOG"
grep -q -- 'https://token.actions.githubusercontent.com' "$COSIGN_LOG"

rm -f "$MARKER"
(
    unset OUR_RUNNER_VERSION
    PATH="$FAKE_BIN:/usr/bin:/bin:/usr/sbin:/sbin" \
    OUR_RUNNER_RELEASE_REPOSITORY=acme/app-runner \
    BOOTSTRAP_TEST_FIXTURES="$FIXTURES" \
    BOOTSTRAP_TEST_MARKER="$MARKER" \
    BOOTSTRAP_TEST_COSIGN_LOG="$COSIGN_LOG" \
    sh "$BOOTSTRAP"
)
test -s "$MARKER"

rm -f "$MARKER"
PATH="$FAKE_BIN:/usr/bin:/bin:/usr/sbin:/sbin" \
OUR_RUNNER_RELEASE_REPOSITORY=acme/app-runner \
OUR_RUNNER_VERSION=v1.2.3 \
BOOTSTRAP_TEST_OS=Darwin \
BOOTSTRAP_TEST_ARCH=arm64 \
BOOTSTRAP_TEST_FIXTURES="$FIXTURES" \
BOOTSTRAP_TEST_MARKER="$MARKER" \
BOOTSTRAP_TEST_COSIGN_LOG="$COSIGN_LOG" \
sh "$BOOTSTRAP"
test -s "$MARKER"
grep -q -- 'install -m 0755' "$MARKER"
grep -q -- '/usr/local/bin/our-runner' "$MARKER"

# A correctly checksummed Linux archive is still rejected when the privileged
# updater half of the atomic release is absent.
cp "$FIXTURES/$ARTIFACT" "$FIXTURES/$ARTIFACT.with-updater"
cp "$FIXTURES/checksums.txt" "$FIXTURES/checksums.txt.with-updater"
tar -C "$PAYLOAD" -czf "$FIXTURES/$ARTIFACT" our-runner packaging/install.sh
if command -v sha256sum >/dev/null 2>&1; then
    WITHOUT_UPDATER_DIGEST=$(sha256sum "$FIXTURES/$ARTIFACT" | awk '{print $1}')
else
    WITHOUT_UPDATER_DIGEST=$(shasum -a 256 "$FIXTURES/$ARTIFACT" | awk '{print $1}')
fi
printf '%s  %s\n' "$WITHOUT_UPDATER_DIGEST" "$ARTIFACT" > "$FIXTURES/checksums.txt"
printf '%s  %s\n' "$DARWIN_DIGEST" "$DARWIN_ARTIFACT" >> "$FIXTURES/checksums.txt"
rm -f "$MARKER"
if run_installer >/dev/null 2>&1; then
    echo "Linux archive without updater was accepted" >&2
    exit 1
fi
test ! -e "$MARKER"
mv "$FIXTURES/$ARTIFACT.with-updater" "$FIXTURES/$ARTIFACT"
mv "$FIXTURES/checksums.txt.with-updater" "$FIXTURES/checksums.txt"

cp "$FIXTURES/$ARTIFACT" "$FIXTURES/$ARTIFACT.valid"
printf '%s\n' tampered >> "$FIXTURES/$ARTIFACT"
rm -f "$MARKER"
if run_installer >/dev/null 2>&1; then
    echo "tampered archive was accepted" >&2
    exit 1
fi
test ! -e "$MARKER"
mv "$FIXTURES/$ARTIFACT.valid" "$FIXTURES/$ARTIFACT"

rm -f "$MARKER"
if BOOTSTRAP_TEST_COSIGN_FAIL=1 run_installer >/dev/null 2>&1; then
    echo "invalid signature was accepted" >&2
    exit 1
fi
test ! -e "$MARKER"

echo "bootstrap installer tests passed"
