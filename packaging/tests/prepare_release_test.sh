#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
PREPARE="$REPO_ROOT/packaging/scripts/prepare-release.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM

fail() {
    printf 'prepare release test failed: %s\n' "$*" >&2
    exit 1
}

command -v openssl >/dev/null 2>&1 || fail "openssl is required to generate the throwaway trust key"
openssl ecparam -name prime256v1 -genkey -noout -out "$TEST_ROOT/throwaway-key.pem" 2>/dev/null
THROWAWAY_PUBLIC_KEY=$(openssl ec -in "$TEST_ROOT/throwaway-key.pem" -pubout -outform DER 2>/dev/null | base64 | tr -d '\n')
[ -n "$THROWAWAY_PUBLIC_KEY" ] || fail "could not derive the throwaway public key"
PROVISIONED_TRUST="{\"schema_version\":1,\"signature_threshold\":1,\"keys\":[{\"key_id\":\"release-test\",\"public_key_b64\":\"$THROWAWAY_PUBLIC_KEY\",\"not_before\":\"2020-01-01T00:00:00Z\",\"not_after\":\"2099-01-01T00:00:00Z\"}]}"
EMPTY_TRUST='{"schema_version":1,"signature_threshold":1,"keys":[]}'

new_workspace() {
    rm -rf "$TEST_ROOT/work"
    mkdir -p "$TEST_ROOT/work/packaging"
    cp "$REPO_ROOT/packaging/bootstrap.sh" "$TEST_ROOT/work/packaging/bootstrap.sh"
    if [ "${TEST_RELEASE_VERSION-1.2.3}" != absent ]; then
        printf '%s\n' "${TEST_RELEASE_VERSION-1.2.3}" > "$TEST_ROOT/work/packaging/release-version"
    fi
    printf '%s\n' "${TEST_TRUST-$PROVISIONED_TRUST}" > "$TEST_ROOT/work/packaging/update-trust.json"
    chmod 0644 "$TEST_ROOT/work/packaging/update-trust.json"
    if [ "${TEST_JOB_IMAGE-ghcr.io/acme/runner-job@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}" != absent ]; then
        mkdir -p "$TEST_ROOT/work/.release"
        printf '%s\n' "${TEST_JOB_IMAGE-ghcr.io/acme/runner-job@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}" > "$TEST_ROOT/work/.release/job-image.txt"
    fi
}

prepare() {
    (
        cd "$TEST_ROOT/work"
        OUR_RUNNER_RELEASE_REPOSITORY=${TEST_REPOSITORY-acme/runner} \
        OUR_RUNNER_CHANNEL=${TEST_CHANNEL-production} \
        OUR_RUNNER_CONTROL_PLANE_URL=${TEST_URL-} \
        sh "$PREPARE" "$@"
    )
}

expect_refused() {
    description=$1
    shift
    new_workspace
    if prepare "$@" >"$TEST_ROOT/out" 2>&1; then
        fail "$description was accepted"
    fi
    [ ! -e "$TEST_ROOT/work/.release/install.sh" ] || fail "$description still wrote .release/install.sh"
}

new_workspace
prepare v1.2.3 false >/dev/null
installer="$TEST_ROOT/work/.release/install.sh"
[ -f "$installer" ] || fail "installer was not written"
grep -q '__OUR_RUNNER_RELEASE_REPOSITORY__' "$installer" && fail "placeholder was not replaced"
grep -Fqx 'DEFAULT_RELEASE_REPOSITORY=acme/runner' "$installer" || fail "repository was not stamped"
sh -n "$installer" || fail "installer has a syntax error"
[ -x "$installer" ] || fail "installer is not executable"

new_workspace
TEST_CHANNEL=staging TEST_URL=https://staging.example.test prepare v1.2.3-staging.4 false >/dev/null || fail "staging tag refused"
new_workspace
TEST_CHANNEL=develop TEST_URL=https://develop.example.test:8443/ prepare v1.2.3-develop.1 false >/dev/null || fail "develop tag refused"
new_workspace
TEST_CHANNEL=develop prepare v0.0.0 true >/dev/null || fail "snapshot with any tag refused"

TEST_REPOSITORY= expect_refused "a missing repository" v1.2.3 false
TEST_REPOSITORY='acme/runner|x' expect_refused "a repository with a separator" v1.2.3 false
TEST_REPOSITORY='acme' expect_refused "a repository without an owner" v1.2.3 false
TEST_CHANNEL= expect_refused "a missing channel" v1.2.3 false
TEST_CHANNEL=nightly expect_refused "an unknown channel" v1.2.3 false
TEST_CHANNEL=develop expect_refused "a production tag on the develop channel" v1.2.3 false
TEST_CHANNEL=production expect_refused "a staging tag on the production channel" v1.2.3-staging.1 false
TEST_CHANNEL=staging expect_refused "a develop tag on the staging channel" v1.2.3-develop.1 false
TEST_CHANNEL=production expect_refused "an unrecognized prerelease tag" v1.2.3-rc.1 false
TEST_URL=http://control.example.test expect_refused "an http control plane" v1.2.3 false
TEST_URL=https://control.example.test/app expect_refused "a control plane with a path" v1.2.3 false
TEST_URL='https://control.example.test/?x=1' expect_refused "a control plane with a query" v1.2.3 false
TEST_URL='https://user@control.example.test' expect_refused "a control plane with credentials" v1.2.3 false
TEST_URL='https://control.example.test
https://other.example.test' expect_refused "a control plane with two lines" v1.2.3 false
TEST_REPOSITORY='acme/runner
evil/runner' expect_refused "a repository with two lines" v1.2.3 false
TEST_URL=' https://control.example.test' expect_refused "a control plane with whitespace" v1.2.3 false

TEST_RELEASE_VERSION=1.2.4 expect_refused "a production tag that differs from the declared release version" v1.2.3 false
TEST_CHANNEL=staging TEST_RELEASE_VERSION=1.2.4 expect_refused "a staging tag whose core differs from the declared release version" v1.2.3-staging.1 false
TEST_RELEASE_VERSION=absent expect_refused "a missing release-version file" v1.2.3 false
TEST_RELEASE_VERSION=v1.2.3 expect_refused "a v-prefixed release-version file" v1.2.3 false
TEST_RELEASE_VERSION='1.2' expect_refused "a malformed release-version file" v1.2.3 false
TEST_TRUST=$EMPTY_TRUST expect_refused "a production release with an empty update trust" v1.2.3 false
TEST_TRUST='{"schema_version":1,"signature_threshold":1,"keys":[],"extra":1}' TEST_CHANNEL=staging expect_refused "a staging release with a malformed update trust" v1.2.3-staging.1 false
TEST_TRUST='{"schema_version":1,"signature_threshold":1,"keys":[{"key_id":"broken","public_key_b64":"bm90LWRlcg==","not_before":"2020-01-01T00:00:00Z","not_after":"2099-01-01T00:00:00Z"}]}' expect_refused "a production release with an invalid trust key" v1.2.3 false
TEST_TRUST="{\"schema_version\":1,\"signature_threshold\":1,\"keys\":[{\"key_id\":\"expired\",\"public_key_b64\":\"$THROWAWAY_PUBLIC_KEY\",\"not_before\":\"2020-01-01T00:00:00Z\",\"not_after\":\"2021-01-01T00:00:00Z\"}]}" expect_refused "a production release whose only trust key expired" v1.2.3 false

TEST_TRUST=$EMPTY_TRUST new_workspace
TEST_CHANNEL=staging prepare v1.2.3-staging.2 false >/dev/null || fail "a staging release with an empty update trust was refused"
TEST_TRUST=$EMPTY_TRUST new_workspace
TEST_CHANNEL=develop prepare v1.2.3-develop.3 false >/dev/null || fail "a develop release with an empty update trust was refused"
TEST_TRUST=$EMPTY_TRUST TEST_RELEASE_VERSION=9.9.9 new_workspace
TEST_CHANNEL=production prepare v0.0.0 true >/dev/null || fail "a snapshot was held to the release gates"
new_workspace
prepare v1.2.3 false >/dev/null || fail "a production release with a provisioned trust was refused"

TEST_JOB_IMAGE=absent expect_refused "a release without a published job image" v1.2.3 false
TEST_JOB_IMAGE=ghcr.io/acme/runner-job:v1.2.3 expect_refused "a release whose job image is pinned only by tag" v1.2.3 false
TEST_JOB_IMAGE=unpublished-snapshot expect_refused "a release carrying the snapshot placeholder" v1.2.3 false
TEST_JOB_IMAGE=ghcr.io/acme/runner-job@sha256:0123456789abcdef expect_refused "a release whose job image digest is short" v1.2.3 false
TEST_JOB_IMAGE=docker.io/acme/runner-job@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef expect_refused "a release whose job image is outside GHCR" v1.2.3 false
TEST_JOB_IMAGE=absent new_workspace
TEST_CHANNEL=develop prepare v0.0.0 true >/dev/null || fail "a snapshot without a published job image was refused"
[ "$(cat "$TEST_ROOT/work/.release/job-image.txt")" = unpublished-snapshot ] || fail "a snapshot did not write the job image placeholder"

expect_preflight_refused() {
    description=$1
    expected=$2
    shift 2
    new_workspace
    if prepare "$@" preflight >"$TEST_ROOT/out" 2>&1; then
        fail "preflight: $description was accepted"
    fi
    grep -Fq "$expected" "$TEST_ROOT/out" || fail "preflight: $description gave: $(cat "$TEST_ROOT/out")"
    [ ! -e "$TEST_ROOT/work/.release/install.sh" ] || fail "preflight: $description wrote .release/install.sh"
}

expect_preflight_passes() {
    description=$1
    shift
    new_workspace
    prepare "$@" preflight >"$TEST_ROOT/out" 2>&1 || fail "preflight: $description was refused: $(cat "$TEST_ROOT/out")"
    [ ! -e "$TEST_ROOT/work/.release/install.sh" ] || fail "preflight: $description wrote .release/install.sh"
    [ ! -e "$TEST_ROOT/work/.release/job-image.txt" ] || fail "preflight: $description wrote .release/job-image.txt"
}

TEST_JOB_IMAGE=absent TEST_TRUST=$EMPTY_TRUST expect_preflight_refused "a production tag with an empty update trust" \
    "a production release requires a provisioned packaging/update-trust.json" v1.2.3 false
TEST_JOB_IMAGE=absent TEST_RELEASE_VERSION=1.2.4 expect_preflight_refused "a tag that differs from the declared release version" \
    "does not match the declared release version 1.2.4" v1.2.3 false
TEST_JOB_IMAGE=absent TEST_CHANNEL=develop expect_preflight_refused "a production tag on the develop channel" \
    "belongs to the production channel, not develop" v1.2.3 false
TEST_JOB_IMAGE=absent TEST_CHANNEL=production expect_preflight_refused "a snapshot" \
    "preflight applies only to a release tag" v1.2.3 true
TEST_JOB_IMAGE=absent expect_preflight_refused "an unknown phase" "unknown preparation phase" v1.2.3 false publish
TEST_JOB_IMAGE=absent TEST_TRUST=$EMPTY_TRUST TEST_CHANNEL=staging expect_preflight_passes \
    "a staging tag with an empty update trust and no job image yet" v1.2.3-staging.1 false
TEST_JOB_IMAGE=absent expect_preflight_passes "a production tag with a provisioned trust and no job image yet" v1.2.3 false

new_workspace
mkdir -p "$TEST_ROOT/work/.release"
printf 'stale\n' > "$TEST_ROOT/work/.release/install.sh"
TEST_CHANNEL=nightly prepare v1.2.3 false >/dev/null 2>&1 && fail "refused input was accepted over a stale installer"
[ ! -e "$TEST_ROOT/work/.release/install.sh" ] || fail "a stale installer survived a refused preparation"

WORKFLOW="$REPO_ROOT/.github/workflows/release.yml"
preflight_line=$(grep -n 'prepare-release.sh "$TAG" false preflight' "$WORKFLOW" | head -n 1 | cut -d: -f1)
push_line=$(grep -n 'packaging/job-image/build.sh .*--push' "$WORKFLOW" | head -n 1 | cut -d: -f1)
[ -n "$preflight_line" ] || fail "release.yml does not run the release preflight"
[ -n "$push_line" ] || fail "release.yml does not push the job image"
[ "$preflight_line" -lt "$push_line" ] || fail "release.yml pushes the job image before the release preflight"

printf 'prepare release tests passed\n'
