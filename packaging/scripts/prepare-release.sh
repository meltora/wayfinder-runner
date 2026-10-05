#!/bin/sh
set -eu

TAG=${1:-}
SNAPSHOT=${2:-false}
OUTPUT=.release/install.sh

fail() {
    printf 'release preparation failed: %s\n' "$*" >&2
    exit 1
}

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
