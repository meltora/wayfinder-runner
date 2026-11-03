#!/bin/sh
set -eu

TAG=${1:-}
IMAGE_REPOSITORY=${2:-}
MANIFEST_TYPES='application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json'

fail() {
    printf 'published tag check failed: %s\n' "$*" >&2
    exit 1
}

[ -n "$TAG" ] && [ -n "$IMAGE_REPOSITORY" ] || fail "usage: refuse-published-tag.sh TAG ghcr.io/OWNER/NAME"
printf '%s\n' "$TAG" | grep -Eqx 'v[0-9]+\.[0-9]+\.[0-9]+(-(staging|develop)\.[0-9]+)?' || \
    fail "release tag $TAG is not vX.Y.Z, vX.Y.Z-staging.N or vX.Y.Z-develop.N"
printf '%s\n' "$IMAGE_REPOSITORY" | grep -Eqx 'ghcr\.io/[a-z0-9]([a-z0-9._-]*[a-z0-9])?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)+' || \
    fail "the job image repository must be a ghcr.io repository without a tag or digest"
REPOSITORY=${GITHUB_REPOSITORY:-}
printf '%s\n' "$REPOSITORY" | grep -Eqx '[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+' || \
    fail "GITHUB_REPOSITORY must be in owner/repository form"
[ -n "${REGISTRY_USER:-}" ] && [ -n "${REGISTRY_TOKEN:-}" ] || fail "REGISTRY_USER and REGISTRY_TOKEN are required"

releases=$(gh api --paginate "repos/$REPOSITORY/releases?per_page=100" --jq '.[].tag_name') || \
    fail "could not list the GitHub releases of $REPOSITORY; refusing to publish $TAG"
if printf '%s\n' "$releases" | grep -Fqx -- "$TAG"; then
    fail "$TAG already has a GitHub release (draft or published); cut a new tag instead of re-signing a new build under it"
fi

path=${IMAGE_REPOSITORY#ghcr.io/}
token_response=$(printf 'user = "%s:%s"\n' "$REGISTRY_USER" "$REGISTRY_TOKEN" | \
    curl --fail --silent --show-error --config - "https://ghcr.io/token?service=ghcr.io&scope=repository:$path:pull") || \
    fail "could not obtain a registry token for $IMAGE_REPOSITORY"
token=$(printf '%s' "$token_response" | sed -n 's/.*"token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
[ -n "$token" ] || fail "could not obtain a registry token for $IMAGE_REPOSITORY"

status=$(printf 'header = "Authorization: Bearer %s"\nheader = "Accept: %s"\n' "$token" "$MANIFEST_TYPES" | \
    curl --silent --show-error --head --output /dev/null --write-out '%{http_code}' --config - \
        "https://ghcr.io/v2/$path/manifests/$TAG") || \
    fail "could not query the registry for $IMAGE_REPOSITORY:$TAG"
case "$status" in
    404) ;;
    200) fail "$IMAGE_REPOSITORY:$TAG already exists; a rerun would push and sign a new digest under the same tag. Cut a new tag, or delete the image and its signature first" ;;
    *) fail "the registry answered $status for $IMAGE_REPOSITORY:$TAG; refusing to publish without knowing whether the tag exists" ;;
esac

printf 'no release or job image exists yet for %s\n' "$TAG"
