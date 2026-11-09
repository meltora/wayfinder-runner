#!/bin/sh
set -eu

usage() {
    printf 'usage: %s IMAGE_TAG [--push]\n' "$0" >&2
    exit 2
}

fail() {
    printf 'Go check image build failed: %s\n' "$*" >&2
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
    fail "IMAGE_TAG must be a lowercase repository with an explicit tag, for example our-runner-check-go:v1"

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

command -v docker >/dev/null 2>&1 || fail "docker is required"
ARCH=$(docker version --format '{{.Server.Arch}}')
case "$ARCH" in
    amd64|arm64) ;;
    *) fail "unsupported Docker server architecture: $ARCH" ;;
esac

CONTEXT=$(mktemp -d "${TMPDIR:-/tmp}/our-runner-check-image-go.XXXXXX")
trap 'rm -rf "$CONTEXT"' EXIT HUP INT TERM

docker build --file "$SCRIPT_DIR/Dockerfile" --iidfile "$CONTEXT/image-id" --tag "$IMAGE" "$CONTEXT"
IMAGE_ID=$(cat "$CONTEXT/image-id")
printf '%s\n' "$IMAGE_ID" | grep -Eqx 'sha256:[a-f0-9]{64}' || fail "docker did not report an image ID"
VERSION=$(docker run --rm --network none --read-only --user 10001:10001 --entrypoint go "$IMAGE_ID" version)
printf '%s\n' "$VERSION" | grep -Eqx "go version go1\.27\.3 linux/$ARCH" || fail "unexpected Go toolchain: $VERSION"
SETID=$(docker run --rm --network none --read-only --user 0:0 --entrypoint find "$IMAGE_ID" / -xdev -type f -perm /6000)
[ -z "$SETID" ] || fail "setuid or setgid files remain: $SETID"
printf 'image: %s\n' "$IMAGE"
printf 'platform: linux/%s\n' "$ARCH"
printf 'toolchain: %s\n' "$VERSION"
printf 'local image ID: %s\n' "$IMAGE_ID"

if [ "$PUSH" -eq 0 ]; then
    if RESOLVED=$(docker image inspect --format '{{.Id}}' -- "$IMAGE@$IMAGE_ID" 2>"$CONTEXT/inspect.err"); then
        [ "$RESOLVED" = "$IMAGE_ID" ] || fail "$IMAGE@$IMAGE_ID resolves to $RESOLVED, not the built image"
        printf 'pinned reference: %s@%s\n' "$IMAGE" "$IMAGE_ID"
        exit 0
    fi
    grep -Fq 'No such image' "$CONTEXT/inspect.err" || fail "could not inspect $IMAGE@$IMAGE_ID: $(cat "$CONTEXT/inspect.err")"
    fail "this Docker host's image store does not resolve a locally built image by digest (classic image store); rerun with --push to push $IMAGE to its registry and print a repository digest, or use a Docker host with the containerd image store"
fi

docker push "$IMAGE" || fail "docker push $IMAGE failed"
REPOSITORY=${IMAGE%:*}
DIGESTS=$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' -- "$IMAGE") || fail "could not read the repository digests of $IMAGE"
PINNED=
while IFS= read -r ENTRY; do
    case "$ENTRY" in
        "$REPOSITORY@sha256:"*) PINNED=$ENTRY; break ;;
    esac
done <<EOF
$DIGESTS
EOF
[ -n "$PINNED" ] || fail "the pushed image has no repository digest for $REPOSITORY"
printf '%s\n' "$PINNED" | grep -Eqx '[^[:space:]@]+@sha256:[a-f0-9]{64}' || fail "the repository digest $PINNED is not name@sha256:<64 lowercase hex>"
PINNED_ID=$(docker image inspect --format '{{.Id}}' -- "$PINNED") || fail "the repository digest $PINNED does not resolve on this Docker host"
[ "$PINNED_ID" = "$IMAGE_ID" ] || fail "the repository digest $PINNED resolves to $PINNED_ID, not the built image $IMAGE_ID"
printf 'pinned reference: %s\n' "$PINNED"
