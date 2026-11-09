#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/../.." && pwd)
BUILD="$REPO_ROOT/packaging/check-image-go/build.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM

FAKE_BIN="$TEST_ROOT/bin"
SYS_BIN="$TEST_ROOT/sysbin"
TMP_DIR="$TEST_ROOT/tmp"
LOG="$TEST_ROOT/calls.log"
OUT="$TEST_ROOT/out.log"
mkdir -p "$FAKE_BIN" "$SYS_BIN" "$TMP_DIR"

fail() {
    printf 'check image go build test failed: %s\n' "$*" >&2
    if [ -f "$OUT" ]; then
        printf -- '--- output\n' >&2
        cat "$OUT" >&2
    fi
    if [ -f "$LOG" ]; then
        printf -- '--- docker calls\n' >&2
        cat "$LOG" >&2
    fi
    exit 1
}

for tool in sh cat grep wc tr mktemp rm dirname env; do
    real=$(command -v "$tool") || fail "test host lacks $tool"
    ln -s "$real" "$SYS_BIN/$tool"
done

cat > "$FAKE_BIN/docker" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_LOG"
no_such_image() {
    printf 'Error response from daemon: No such image: %s\n' "$1" >&2
    exit 1
}
case "$1" in
    version) printf 'amd64\n' ;;
    build)
        while [ "$#" -gt 0 ]; do
            if [ "$1" = --iidfile ]; then
                printf '%s\n' "$FAKE_IMAGE_ID" > "$2"
            fi
            shift
        done
        ;;
    run)
        case "$*" in
            *"--entrypoint go "*) printf 'go version go1.27.3 linux/amd64\n' ;;
            *"--entrypoint find "*) ;;
            *) exit 97 ;;
        esac
        ;;
    push) exit "${FAKE_PUSH_STATUS:-0}" ;;
    image)
        [ "$2" = inspect ] && [ "$3" = --format ] && [ "$5" = -- ] && [ "$#" -eq 6 ] || exit 97
        ref=$6
        case "$4" in
            '{{.Id}}')
                if [ "$ref" = "$FAKE_IMAGE@$FAKE_IMAGE_ID" ]; then
                    [ "$FAKE_STORE" = containerd ] || no_such_image "$ref"
                    printf '%s\n' "$FAKE_IMAGE_ID"
                    exit 0
                fi
                if [ -n "$FAKE_REPO_DIGESTS" ] && printf '%s\n' "$FAKE_REPO_DIGESTS" | grep -Fqx -- "$ref"; then
                    printf '%s\n' "${FAKE_DIGEST_ID:-$FAKE_IMAGE_ID}"
                    exit 0
                fi
                no_such_image "$ref"
                ;;
            '{{range .RepoDigests}}{{println .}}{{end}}')
                [ "$ref" = "$FAKE_IMAGE" ] || no_such_image "$ref"
                if [ -n "$FAKE_REPO_DIGESTS" ]; then
                    printf '%s\n' "$FAKE_REPO_DIGESTS"
                fi
                printf '\n'
                ;;
            *) exit 97 ;;
        esac
        ;;
    *) exit 97 ;;
esac
EOF
chmod 0755 "$FAKE_BIN/docker"

ID=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
REPO_DIGEST=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
OTHER_DIGEST=sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
NL='
'
CLASSIC_MESSAGE="Go check image build failed: this Docker host's image store does not resolve a locally built image by digest (classic image store); rerun with --push to push our-runner-check-go:v1 to its registry and print a repository digest, or use a Docker host with the containerd image store"

run_build() {
    want=$1
    shift
    : > "$LOG"
    set +e
    env -i PATH="$FAKE_BIN:$SYS_BIN" TMPDIR="$TMP_DIR" FAKE_LOG="$LOG" FAKE_STORE="$STORE" \
        FAKE_IMAGE="${IMAGE:-our-runner-check-go:v1}" FAKE_IMAGE_ID="$ID" FAKE_REPO_DIGESTS="${DIGESTS:-}" \
        FAKE_DIGEST_ID="${DIGEST_ID:-}" FAKE_PUSH_STATUS="${PUSH_STATUS:-0}" \
        sh "$BUILD" "$@" > "$OUT" 2>&1
    status=$?
    set -e
    [ "$status" -eq "$want" ] || fail "build.sh $* exited $status, want $want"
    if grep -Eq '(^|[[:space:]])-*pull' "$LOG"; then
        fail "build.sh $* pulled an image"
    fi
    if grep -Eq '^(rmi|image rm|tag) ' "$LOG"; then
        fail "build.sh $* removed or retagged an image"
    fi
}

expect_output() {
    grep -Fqx -- "$1" "$OUT" || fail "output is missing: $1"
}

expect_no_pinned_reference() {
    if grep -Fq 'pinned reference' "$OUT"; then
        fail 'a pinned reference was printed for a failed build'
    fi
}

expect_no_push() {
    if grep -Eq '^push' "$LOG"; then
        fail 'build.sh pushed without --push'
    fi
}

expect_last_line() {
    last=
    while IFS= read -r line; do
        [ -z "$line" ] || last=$line
    done < "$OUT"
    [ "$last" = "$1" ] || fail "last line is $last, want $1"
}

expect_usage() {
    expect_output "usage: $BUILD IMAGE_TAG [--push]"
    [ ! -s "$LOG" ] || fail 'docker ran for a usage error'
}

STORE=containerd
run_build 0 our-runner-check-go:v1
expect_output "local image ID: $ID"
expect_last_line "pinned reference: our-runner-check-go:v1@$ID"
expect_no_push
grep -Fqx -- "image inspect --format {{.Id}} -- our-runner-check-go:v1@$ID" "$LOG" || fail 'the containerd reference was not proven'

STORE=classic
run_build 1 our-runner-check-go:v1
expect_output "local image ID: $ID"
expect_output "$CLASSIC_MESSAGE"
expect_no_pinned_reference
expect_no_push

STORE=classic DIGESTS="our-runner-check-go@$REPO_DIGEST"
run_build 0 our-runner-check-go:v1 --push
grep -Fqx 'push our-runner-check-go:v1' "$LOG" || fail '--push did not push the tag'
expect_last_line "pinned reference: our-runner-check-go@$REPO_DIGEST"
grep -Fqx -- "image inspect --format {{.Id}} -- our-runner-check-go@$REPO_DIGEST" "$LOG" || fail 'the repository digest was not proven'

STORE=containerd DIGESTS="our-runner-check-go@$REPO_DIGEST"
run_build 0 our-runner-check-go:v1 --push
grep -Fqx 'push our-runner-check-go:v1' "$LOG" || fail '--push did not push on the containerd store'
expect_last_line "pinned reference: our-runner-check-go@$REPO_DIGEST"

STORE=classic DIGESTS="mirror.example/our-runner-check-go@$OTHER_DIGEST${NL}other/repo@$OTHER_DIGEST${NL}our-runner-check-go@$REPO_DIGEST${NL}our-runner-check-go@$OTHER_DIGEST"
run_build 0 our-runner-check-go:v1 --push
expect_last_line "pinned reference: our-runner-check-go@$REPO_DIGEST"

STORE=classic IMAGE=localhost:5000/tools/check-go:v1
DIGESTS="localhost:5000/other@$OTHER_DIGEST${NL}localhost:5000/tools/check-go@$REPO_DIGEST"
run_build 0 localhost:5000/tools/check-go:v1 --push
grep -Fqx 'push localhost:5000/tools/check-go:v1' "$LOG" || fail '--push did not push the registry tag'
expect_last_line "pinned reference: localhost:5000/tools/check-go@$REPO_DIGEST"
IMAGE=

STORE=classic DIGESTS="our-runner-check-go@$REPO_DIGEST" PUSH_STATUS=1
run_build 1 our-runner-check-go:v1 --push
expect_no_pinned_reference
PUSH_STATUS=0

STORE=classic DIGESTS="other/repo@$REPO_DIGEST${NL}mirror.example/our-runner-check-go@$REPO_DIGEST"
run_build 1 our-runner-check-go:v1 --push
expect_output 'Go check image build failed: the pushed image has no repository digest for our-runner-check-go'
expect_no_pinned_reference

STORE=classic DIGESTS=
run_build 1 our-runner-check-go:v1 --push
expect_output 'Go check image build failed: the pushed image has no repository digest for our-runner-check-go'
expect_no_pinned_reference

STORE=classic DIGESTS="our-runner-check-go@$REPO_DIGEST" DIGEST_ID="$OTHER_DIGEST"
run_build 1 our-runner-check-go:v1 --push
expect_output "Go check image build failed: the repository digest our-runner-check-go@$REPO_DIGEST resolves to $OTHER_DIGEST, not the built image $ID"
expect_no_pinned_reference
DIGEST_ID=

UPPER=sha256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB
STORE=classic DIGESTS="our-runner-check-go@$UPPER"
run_build 1 our-runner-check-go:v1 --push
expect_output "Go check image build failed: the repository digest our-runner-check-go@$UPPER is not name@sha256:<64 lowercase hex>"
expect_no_pinned_reference

STORE=containerd DIGESTS=
run_build 2 our-runner-check-go:v1 --pull
expect_usage
run_build 2 our-runner-check-go:v1 --push extra
expect_usage
run_build 2 --push our-runner-check-go:v1
expect_usage
run_build 2
expect_usage

printf 'check image go build tests passed\n'
