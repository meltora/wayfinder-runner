#!/bin/sh
set -eu

# This token is replaced by packaging/scripts/prepare-release.sh during the release build. Source
# checkouts can set OUR_RUNNER_RELEASE_REPOSITORY explicitly for local testing.
DEFAULT_RELEASE_REPOSITORY=__OUR_RUNNER_RELEASE_REPOSITORY__
COSIGN_VERSION=v3.0.6
COSIGN_OIDC_ISSUER=https://token.actions.githubusercontent.com

say() {
    printf '%s\n' "$*"
}

fail() {
    printf 'error: %s\n' "$*" >&2
    exit 1
}

need_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

file_size() {
    wc -c < "$1" | tr -d '[:space:]'
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        fail "sha256sum or shasum is required"
    fi
}

download() {
    url=$1
    destination=$2
    max_bytes=$3
    curl --fail --silent --show-error --location \
        --proto '=https' --proto-redir '=https' --tlsv1.2 \
        --retry 3 --connect-timeout 15 --max-time 300 \
        --max-filesize "$max_bytes" --output "$destination" "$url"
    size=$(file_size "$destination")
    [ "$size" -gt 0 ] || fail "downloaded file is empty: $url"
    [ "$size" -le "$max_bytes" ] || fail "download exceeds size limit: $url"
}

validate_repository() {
    printf '%s\n' "$1" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || \
        fail "OUR_RUNNER_RELEASE_REPOSITORY must be in owner/repository form"
}

validate_tag() {
    printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$' || \
        fail "release version must be a v-prefixed semantic version"
}

resolve_latest_tag() {
    latest_url="https://github.com/$1/releases/latest"
    effective_url=$(curl --fail --silent --show-error --location --head \
        --proto '=https' --proto-redir '=https' --tlsv1.2 \
        --retry 3 --connect-timeout 15 --max-time 60 \
        --output /dev/null --write-out '%{url_effective}' "$latest_url")
    prefix="https://github.com/$1/releases/tag/"
    case "$effective_url" in
        "$prefix"*) printf '%s\n' "${effective_url#"$prefix"}" ;;
        *) fail "latest release redirect left the expected GitHub repository" ;;
    esac
}

platform() {
    case "$(uname -s)" in
        Linux) OS=linux ;;
        Darwin) OS=darwin ;;
        *) fail "unsupported operating system: $(uname -s)" ;;
    esac
    case "$(uname -m)" in
        x86_64|amd64) ARCH=amd64 ;;
        arm64|aarch64) ARCH=arm64 ;;
        *) fail "unsupported architecture: $(uname -m)" ;;
    esac
}

cosign_digest() {
    case "$OS/$ARCH" in
        linux/amd64) printf '%s\n' c956e5dfcac53d52bcf058360d579472f0c1d2d9b69f55209e256fe7783f4c74 ;;
        linux/arm64) printf '%s\n' bedac92e8c3729864e13d4a17048007cfafa79d5deca993a43a90ffe018ef2b8 ;;
        darwin/amd64) printf '%s\n' 4c3e7af8372d3ca3296e62fa56f23fcbb5721cc6ac1827900d398f110d7cd280 ;;
        darwin/arm64) printf '%s\n' 5fadd012ae6381a6a29ff86a7d39aa873878852f1073fc90b15995961ecfb084 ;;
        *) fail "no pinned cosign binary for $OS/$ARCH" ;;
    esac
}

prepare_cosign() {
    if command -v cosign >/dev/null 2>&1; then
        COSIGN=$(command -v cosign)
        return
    fi
    COSIGN="$TEMP_ROOT/cosign"
    cosign_asset="cosign-$OS-$ARCH"
    cosign_url="https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION/$cosign_asset"
    say "cosign not found; downloading pinned $COSIGN_VERSION verifier"
    download "$cosign_url" "$COSIGN" 104857600
    expected=$(cosign_digest)
    actual=$(sha256_file "$COSIGN")
    [ "$actual" = "$expected" ] || fail "downloaded cosign binary failed its pinned SHA-256 check"
    chmod 0700 "$COSIGN"
}

verify_release() {
    identity="https://github.com/$RELEASE_REPOSITORY/.github/workflows/release.yml@refs/tags/$TAG"
    "$COSIGN" verify-blob "$CHECKSUMS" \
        --bundle "$CHECKSUM_BUNDLE" \
        --certificate-identity "$identity" \
        --certificate-oidc-issuer "$COSIGN_OIDC_ISSUER" >/dev/null

    expected=$(awk -v artifact="$ARTIFACT" '$2 == artifact { print $1 }' "$CHECKSUMS")
    case "$expected" in
        *[!0-9a-f]*|'') fail "signed checksum file does not contain a valid entry for $ARTIFACT" ;;
    esac
    [ "${#expected}" -eq 64 ] || fail "signed checksum entry has an invalid length"
    [ "$(awk -v artifact="$ARTIFACT" '$2 == artifact { count++ } END { print count + 0 }' "$CHECKSUMS")" -eq 1 ] || \
        fail "signed checksum file contains duplicate artifact entries"
    actual=$(sha256_file "$ARCHIVE")
    [ "$actual" = "$expected" ] || fail "release archive failed SHA-256 verification"
}

extract_release() {
    entries="$TEMP_ROOT/archive-entries"
    tar -tzf "$ARCHIVE" > "$entries"
    if grep -Eq '(^/|(^|/)\.\.(/|$))' "$entries"; then
        fail "release archive contains an unsafe path"
    fi
    mkdir -p "$EXTRACT_ROOT"
    tar -xzf "$ARCHIVE" -C "$EXTRACT_ROOT"
    [ -f "$EXTRACT_ROOT/our-runner" ] && [ ! -L "$EXTRACT_ROOT/our-runner" ] || \
        fail "release archive does not contain a regular our-runner binary"
    chmod 0755 "$EXTRACT_ROOT/our-runner"
    if [ "$OS" = linux ]; then
        [ -f "$EXTRACT_ROOT/our-runner-updater" ] && [ ! -L "$EXTRACT_ROOT/our-runner-updater" ] || \
            fail "Linux release does not contain a regular our-runner-updater binary"
        chmod 0755 "$EXTRACT_ROOT/our-runner-updater"
    fi
}

install_linux() {
    installer="$EXTRACT_ROOT/packaging/install.sh"
    [ -f "$installer" ] && [ ! -L "$installer" ] || fail "Linux release is missing packaging/install.sh"
    set -- --version "$VERSION" "$EXTRACT_ROOT/our-runner" "$EXTRACT_ROOT/our-runner-updater"
    if [ "${OUR_RUNNER_SETUP_ROOTLESS_DOCKER:-0}" = 1 ]; then
        set -- --setup-rootless-docker "$@"
    fi
    if [ "$(id -u)" -eq 0 ]; then
        sh "$installer" "$@"
    else
        need_command sudo
        sudo sh "$installer" "$@"
    fi
    say "our-runner $TAG installed in /opt/our-runner/versions/$VERSION"
    say "Next: enroll the runner using the one-time code from your control-plane dashboard."
}

install_darwin() {
    destination=/usr/local/bin/our-runner
    if [ "$(id -u)" -eq 0 ]; then
        mkdir -p /usr/local/bin
        install -m 0755 "$EXTRACT_ROOT/our-runner" "$destination"
    else
        need_command sudo
        sudo mkdir -p /usr/local/bin
        sudo install -m 0755 "$EXTRACT_ROOT/our-runner" "$destination"
    fi
    say "our-runner $TAG installed at $destination"
    say "macOS is supported for development only; production runners require Linux."
}

need_command curl
need_command tar
need_command awk
need_command grep
need_command wc
need_command tr
need_command mktemp

RELEASE_REPOSITORY=${OUR_RUNNER_RELEASE_REPOSITORY:-$DEFAULT_RELEASE_REPOSITORY}
[ "$RELEASE_REPOSITORY" != __OUR_RUNNER_RELEASE_REPOSITORY__ ] || \
    fail "installer template has no release repository; set OUR_RUNNER_RELEASE_REPOSITORY"
validate_repository "$RELEASE_REPOSITORY"

platform
TAG=${OUR_RUNNER_VERSION:-latest}
if [ "$TAG" = latest ]; then
    TAG=$(resolve_latest_tag "$RELEASE_REPOSITORY")
fi
validate_tag "$TAG"
VERSION=${TAG#v}
ARTIFACT="our-runner_${VERSION}_${OS}_${ARCH}.tar.gz"
RELEASE_URL="https://github.com/$RELEASE_REPOSITORY/releases/download/$TAG"

TEMP_PARENT=${TMPDIR:-/tmp}
case "$TEMP_PARENT" in
    /*) ;;
    *) fail "TMPDIR must be absolute" ;;
esac
TEMP_ROOT=$(mktemp -d "$TEMP_PARENT/our-runner-install.XXXXXX")
case "$TEMP_ROOT" in
    /*) ;;
    *) fail "mktemp did not return an absolute directory" ;;
esac
[ "$TEMP_ROOT" != / ] && [ "${#TEMP_ROOT}" -gt 8 ] && [ ! -L "$TEMP_ROOT" ] || \
    fail "mktemp returned an unsafe directory"
cleanup() {
    rm -rf "$TEMP_ROOT"
}
trap cleanup EXIT HUP INT TERM

ARCHIVE="$TEMP_ROOT/$ARTIFACT"
CHECKSUMS="$TEMP_ROOT/checksums.txt"
CHECKSUM_BUNDLE="$TEMP_ROOT/checksums.txt.sigstore.json"
EXTRACT_ROOT="$TEMP_ROOT/release"

say "Downloading our-runner $TAG for $OS/$ARCH"
download "$RELEASE_URL/$ARTIFACT" "$ARCHIVE" 536870912
download "$RELEASE_URL/checksums.txt" "$CHECKSUMS" 10485760
download "$RELEASE_URL/checksums.txt.sigstore.json" "$CHECKSUM_BUNDLE" 1048576
prepare_cosign
verify_release
say "Release signature and SHA-256 checksum verified"
extract_release

case "$OS" in
    linux) install_linux ;;
    darwin) install_darwin ;;
esac
