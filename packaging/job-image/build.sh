#!/bin/sh
set -eu

usage() {
    printf 'usage: %s IMAGE_TAG [--push]\n' "$0" >&2
    exit 2
}

fail() {
    printf 'job image build failed: %s\n' "$*" >&2
    exit 1
}

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
IMAGE=$1
PUSH=0
if [ "$#" -eq 2 ]; then
    [ "$2" = --push ] || usage
    PUSH=1
fi
[ "$(printf '%s' "$IMAGE" | wc -l | tr -d '[:space:]')" -eq 0 ] || fail "IMAGE_TAG must be a single line"
printf '%s\n' "$IMAGE" | grep -Eqx '[a-z0-9]([a-z0-9._/-]*[a-z0-9])?(:[0-9]+)?(/[a-z0-9]([a-z0-9._/-]*[a-z0-9])?)*:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}' || \
    fail "IMAGE_TAG must be a lowercase repository with an explicit tag, for example ghcr.io/owner/runner-job:v1.2.3"

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

command -v docker >/dev/null 2>&1 || fail "docker is required"
command -v go >/dev/null 2>&1 || fail "go is required"
ARCH=$(docker version --format '{{.Server.Arch}}')
case "$ARCH" in
    amd64|arm64) ;;
    *) fail "unsupported Docker server architecture: $ARCH" ;;
esac

CONTEXT=$(mktemp -d "${TMPDIR:-/tmp}/our-runner-job-image.XXXXXX")
trap 'rm -rf "$CONTEXT"' EXIT HUP INT TERM
mkdir -p "$CONTEXT/bin" "$CONTEXT/packaging/job-image"
cp "$SCRIPT_DIR/package.json" "$SCRIPT_DIR/package-lock.json" "$CONTEXT/packaging/job-image/"
(
    cd "$REPO_ROOT"
    CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -o "$CONTEXT/bin/our-runner-linux" ./cmd/our-runner
)

docker build --file "$SCRIPT_DIR/Dockerfile" --iidfile "$CONTEXT/image-id" --tag "$IMAGE" "$CONTEXT"
IMAGE_ID=$(cat "$CONTEXT/image-id")
printf '%s\n' "$IMAGE_ID" | grep -Eqx 'sha256:[a-f0-9]{64}' || fail "docker did not report an image ID"
printf 'image: %s\n' "$IMAGE"
printf 'platform: linux/%s\n' "$ARCH"
printf 'local image ID: %s\n' "$IMAGE_ID"

if [ "$PUSH" -eq 0 ]; then
    printf 'not pushed; the registry digest for RUNNER_DEFAULT_JOB_IMAGE exists only after --push\n'
    exit 0
fi

docker push "$IMAGE"
REPOSITORY=${IMAGE%:*}
PINNED=$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$IMAGE" | grep -F "$REPOSITORY@sha256:" | head -n 1)
printf '%s\n' "$PINNED" | grep -Eqx '[^[:space:]@]+@sha256:[a-f0-9]{64}' || fail "the pushed image has no registry digest"
printf 'pinned: %s\n' "$PINNED"
