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

new_workspace() {
    rm -rf "$TEST_ROOT/work"
    mkdir -p "$TEST_ROOT/work/packaging"
    cp "$REPO_ROOT/packaging/bootstrap.sh" "$TEST_ROOT/work/packaging/bootstrap.sh"
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

new_workspace
mkdir -p "$TEST_ROOT/work/.release"
printf 'stale\n' > "$TEST_ROOT/work/.release/install.sh"
TEST_CHANNEL=nightly prepare v1.2.3 false >/dev/null 2>&1 && fail "refused input was accepted over a stale installer"
[ ! -e "$TEST_ROOT/work/.release/install.sh" ] || fail "a stale installer survived a refused preparation"

printf 'prepare release tests passed\n'
