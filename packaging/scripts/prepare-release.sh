#!/bin/sh
set -eu

TAG=${1:-}
SNAPSHOT=${2:-false}
PHASE=${3:-release}
OUTPUT=.release/install.sh

fail() {
    printf 'release preparation failed: %s\n' "$*" >&2
    exit 1
}

case "$PHASE" in
    release) ;;
    preflight) [ "$SNAPSHOT" != true ] || fail "preflight applies only to a release tag, not a snapshot" ;;
    *) fail "unknown preparation phase $PHASE; use release or preflight" ;;
esac

channel_for_tag() {
    if printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
        printf '%s\n' production
    elif printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+-staging\.[0-9]+$'; then
        printf '%s\n' staging
    elif printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+-develop\.[0-9]+$'; then
        printf '%s\n' develop
    else
        printf '%s\n' unknown
    fi
}

single_line() {
    [ "$(printf '%s' "$1" | wc -l | tr -d '[:space:]')" -eq 0 ] || fail "$2 must be a single line"
}

rm -f "$OUTPUT" "$OUTPUT.tmp"

REPOSITORY=${OUR_RUNNER_RELEASE_REPOSITORY:-}
CHANNEL=${OUR_RUNNER_CHANNEL:-}
CONTROL_PLANE_URL=${OUR_RUNNER_CONTROL_PLANE_URL:-}
single_line "$REPOSITORY" OUR_RUNNER_RELEASE_REPOSITORY
single_line "$CHANNEL" OUR_RUNNER_CHANNEL
single_line "$CONTROL_PLANE_URL" OUR_RUNNER_CONTROL_PLANE_URL

printf '%s\n' "$REPOSITORY" | grep -Eqx '[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+' || \
    fail "OUR_RUNNER_RELEASE_REPOSITORY must be in owner/repository form"
case "$CHANNEL" in
    develop|staging|production) ;;
    *) fail "OUR_RUNNER_CHANNEL must be develop, staging or production" ;;
esac
if [ -n "$CONTROL_PLANE_URL" ]; then
    printf '%s\n' "$CONTROL_PLANE_URL" | grep -Eqx 'https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?/?' || \
        fail "OUR_RUNNER_CONTROL_PLANE_URL must be empty or an HTTPS origin"
fi
if [ "$SNAPSHOT" != true ]; then
    expected=$(channel_for_tag "$TAG")
    [ "$expected" != unknown ] || \
        fail "release tag $TAG must be vX.Y.Z, vX.Y.Z-staging.N or vX.Y.Z-develop.N"
    [ "$expected" = "$CHANNEL" ] || \
        fail "release tag $TAG belongs to the $expected channel, not $CHANNEL"
    [ -f packaging/release-version ] && [ ! -L packaging/release-version ] || \
        fail "packaging/release-version is missing"
    declared=$(cat packaging/release-version)
    printf '%s\n' "$declared" | grep -Eqx '(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)' && \
        [ "$(wc -l < packaging/release-version | tr -d '[:space:]')" -eq 1 ] || \
        fail "packaging/release-version must hold one X.Y.Z line"
    core=${TAG#v}
    core=${core%%-*}
    [ "$core" = "$declared" ] || \
        fail "release tag $TAG does not match the declared release version $declared"
    SCRIPT_ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
    command -v go >/dev/null 2>&1 || fail "go is required to check the update trust"
    if [ "$CHANNEL" = production ]; then
        go -C "$SCRIPT_ROOT" run ./cmd/update-manifest check-trust "$PWD/packaging/update-trust.json" || \
            fail "a production release requires a provisioned packaging/update-trust.json"
    else
        go -C "$SCRIPT_ROOT" run ./cmd/update-manifest check-trust --allow-empty "$PWD/packaging/update-trust.json" || \
            fail "packaging/update-trust.json is invalid"
    fi
    if [ "$PHASE" = preflight ]; then
        printf 'release preflight passed for %s on the %s channel\n' "$TAG" "$CHANNEL"
        exit 0
    fi
    [ -f .release/job-image.txt ] && [ ! -L .release/job-image.txt ] || \
        fail "the job image must be published before the release; .release/job-image.txt is missing"
    if [ "$(wc -l < .release/job-image.txt | tr -d '[:space:]')" -ne 1 ] || \
        ! grep -Eqx 'ghcr\.io/[a-z0-9]([a-z0-9._-]*[a-z0-9])?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)+@sha256:[a-f0-9]{64}' .release/job-image.txt; then
        fail ".release/job-image.txt must hold one ghcr.io reference pinned by sha256 digest"
    fi
elif [ ! -e .release/job-image.txt ]; then
    mkdir -p .release
    printf '%s\n' unpublished-snapshot > .release/job-image.txt
fi

mkdir -p .release
sed "s|__OUR_RUNNER_RELEASE_REPOSITORY__|$REPOSITORY|g" packaging/bootstrap.sh > "$OUTPUT.tmp"
if grep -q '__OUR_RUNNER_RELEASE_REPOSITORY__' "$OUTPUT.tmp"; then
    rm -f "$OUTPUT.tmp"
    fail "installer repository placeholder was not replaced"
fi
sh -n "$OUTPUT.tmp"
chmod 0755 "$OUTPUT.tmp"
mv "$OUTPUT.tmp" "$OUTPUT"
