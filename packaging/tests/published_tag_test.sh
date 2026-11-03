#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/../.." && pwd)
REFUSE="$REPO_ROOT/packaging/scripts/refuse-published-tag.sh"
WORKFLOW="$REPO_ROOT/.github/workflows/release.yml"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM
IMAGE_REPOSITORY=ghcr.io/acme/runner-job

fail() {
    printf 'published tag test failed: %s\n' "$*" >&2
    exit 1
}

mkdir -p "$TEST_ROOT/bin"
cat > "$TEST_ROOT/bin/gh" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "$TEST_ROOT/gh.calls"
[ "${STUB_GH_EXIT:-0}" -eq 0 ] || exit "$STUB_GH_EXIT"
printf '%b' "${STUB_RELEASE_TAGS:-}"
STUB
cat > "$TEST_ROOT/bin/curl" <<'STUB'
#!/bin/sh
config=$(cat)
printf '%s\n' "$*" >> "$TEST_ROOT/curl.calls"
case "$*" in
    *"https://ghcr.io/token?"*)
        printf '%s\n' "$config" | grep -Fqx 'user = "release-bot:test-token"' || exit 22
        [ "${STUB_TOKEN_EXIT:-0}" -eq 0 ] || exit "$STUB_TOKEN_EXIT"
        printf '{"token":"%s"}' "${STUB_REGISTRY_TOKEN-registry-token}"
        ;;
    *"https://ghcr.io/v2/"*)
        printf '%s\n' "$config" | grep -Fqx 'header = "Authorization: Bearer registry-token"' || { printf '401'; exit 0; }
        [ "${STUB_MANIFEST_EXIT:-0}" -eq 0 ] || exit "$STUB_MANIFEST_EXIT"
        printf '%s' "${STUB_MANIFEST_STATUS:-404}"
        ;;
    *) exit 3 ;;
esac
STUB
chmod 0755 "$TEST_ROOT/bin/gh" "$TEST_ROOT/bin/curl"

refuse() {
    rm -f "$TEST_ROOT/gh.calls" "$TEST_ROOT/curl.calls"
    export TEST_ROOT
    PATH="$TEST_ROOT/bin:$PATH" \
        GITHUB_REPOSITORY=${TEST_REPOSITORY-acme/runner} GH_TOKEN=test-token \
        REGISTRY_USER=release-bot REGISTRY_TOKEN=test-token \
        sh "$REFUSE" "$@"
}

expect_refused() {
    description=$1
    expected=$2
    shift 2
    if refuse "$@" >"$TEST_ROOT/out" 2>&1; then
        fail "$description was accepted"
    fi
    grep -Fq "$expected" "$TEST_ROOT/out" || fail "$description gave: $(cat "$TEST_ROOT/out")"
    if grep -Fq test-token "$TEST_ROOT/out"; then
        fail "$description printed the registry credential"
    fi
}

expect_accepted() {
    description=$1
    shift
    refuse "$@" >"$TEST_ROOT/out" 2>&1 || fail "$description was refused: $(cat "$TEST_ROOT/out")"
}

expect_accepted "a first release of a new tag" v1.2.3 "$IMAGE_REPOSITORY"
grep -Fq 'repos/acme/runner/releases' "$TEST_ROOT/gh.calls" || fail "the release listing did not query this repository"
grep -Fq -- '--paginate' "$TEST_ROOT/gh.calls" || fail "the release listing is not paginated"
grep -Fq 'scope=repository:acme/runner-job:pull' "$TEST_ROOT/curl.calls" || fail "the registry token was not scoped to the job image"
grep -Fq 'https://ghcr.io/v2/acme/runner-job/manifests/v1.2.3' "$TEST_ROOT/curl.calls" || fail "the registry probe did not ask for the tag manifest"
if grep -Fq test-token "$TEST_ROOT/curl.calls"; then
    fail "the registry credential was passed on the curl command line"
fi

STUB_RELEASE_TAGS='v1.2.2\nv1.2.3-staging.1\nv1.2.30\n' expect_accepted "a tag that only shares a prefix with existing releases" \
    v1.2.3 "$IMAGE_REPOSITORY"
STUB_RELEASE_TAGS='v1.2.2\nv1.2.3\n' expect_refused "a tag that already has a draft or published release" \
    "v1.2.3 already has a GitHub release" v1.2.3 "$IMAGE_REPOSITORY"
STUB_GH_EXIT=1 expect_refused "a release listing that failed" \
    "could not list the GitHub releases" v1.2.3 "$IMAGE_REPOSITORY"
STUB_MANIFEST_STATUS=200 expect_refused "a tag whose job image was already pushed" \
    "ghcr.io/acme/runner-job:v1.2.3 already exists" v1.2.3 "$IMAGE_REPOSITORY"
STUB_MANIFEST_STATUS=401 expect_refused "a registry probe that was not authorized" \
    "registry answered 401" v1.2.3 "$IMAGE_REPOSITORY"
STUB_MANIFEST_STATUS=500 expect_refused "a registry probe that failed on the server" \
    "registry answered 500" v1.2.3 "$IMAGE_REPOSITORY"
STUB_MANIFEST_EXIT=7 expect_refused "a registry probe that could not connect" \
    "could not query the registry" v1.2.3 "$IMAGE_REPOSITORY"
STUB_TOKEN_EXIT=22 expect_refused "a registry token request that failed" \
    "could not obtain a registry token" v1.2.3 "$IMAGE_REPOSITORY"
STUB_REGISTRY_TOKEN='' expect_refused "a registry token response without a token" \
    "could not obtain a registry token" v1.2.3 "$IMAGE_REPOSITORY"
expect_refused "a tag that is not a release tag" "is not vX.Y.Z" latest "$IMAGE_REPOSITORY"
expect_refused "a job image outside GHCR" "must be a ghcr.io repository" v1.2.3 docker.io/acme/runner-job
expect_refused "a job image reference that carries a tag" "must be a ghcr.io repository" v1.2.3 "$IMAGE_REPOSITORY:v1.2.3"
TEST_REPOSITORY='acme/runner extra' expect_refused "a malformed repository" "GITHUB_REPOSITORY must be" v1.2.3 "$IMAGE_REPOSITORY"
expect_refused "a missing job image repository" "usage:" v1.2.3

step_block() {
    awk -v pattern="$1" '
        /^      - name:/ { if (found) exit; block = "" }
        { block = block $0 "\n" }
        index($0, pattern) { found = 1 }
        END { if (found) printf "%s", block }
    ' "$WORKFLOW"
}

block=$(step_block 'refuse-published-tag.sh')
[ -n "$block" ] || fail "release.yml has no step refusing a published tag"
if printf '%s\n' "$block" | grep -Eq '^        (if|continue-on-error):'; then
    fail "the release.yml step refusing a published tag can be skipped or ignored"
fi

preflight_line=$(grep -n 'prepare-release.sh "$TAG" false preflight' "$WORKFLOW" | head -n 1 | cut -d: -f1)
refuse_line=$(grep -n 'refuse-published-tag.sh' "$WORKFLOW" | head -n 1 | cut -d: -f1)
push_line=$(grep -n 'packaging/job-image/build.sh .*--push' "$WORKFLOW" | head -n 1 | cut -d: -f1)
goreleaser_line=$(grep -n 'goreleaser/goreleaser-action' "$WORKFLOW" | head -n 1 | cut -d: -f1)
[ -n "$push_line" ] && [ -n "$goreleaser_line" ] || fail "release.yml does not push the job image or run GoReleaser"
[ "$preflight_line" -lt "$push_line" ] || fail "release.yml pushes the job image before the release preflight"
[ "$refuse_line" -lt "$push_line" ] || fail "release.yml pushes the job image before refusing a published tag"
[ "$refuse_line" -lt "$goreleaser_line" ] || fail "release.yml runs GoReleaser before refusing a published tag"
grep -q 'JOB_IMAGE_REPOSITORY:\$TAG' "$WORKFLOW" || fail "release.yml does not push the job image to the repository it checked"

printf 'published tag tests passed\n'
