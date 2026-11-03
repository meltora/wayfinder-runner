#!/bin/sh
set -eu

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
CHECK="$ROOT/packaging/tests/release_artifacts_test.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT INT TERM

fail() {
    printf 'release artifact fixture test failed: %s\n' "$*" >&2
    exit 1
}

make_archive() {
    archive=$1
    shift
    stage="$WORK/stage"
    rm -rf "$stage"
    mkdir -p "$stage"
    for entry in "$@"; do
        mkdir -p "$stage/$(dirname "$entry")"
        printf '%s\n' "$entry" > "$stage/$entry"
    done
    (cd "$stage" && tar -czf "$archive" "$@")
}

sha() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

build_fixture() {
    fixture=$1
    dist="$fixture/dist"
    rm -rf "$fixture"
    mkdir -p "$dist" "$fixture/.release"
    printf '#!/bin/sh\n' > "$fixture/.release/install.sh"
    for arch in amd64 arm64; do
        make_archive "$dist/our-runner_1.2.3_linux_$arch.tar.gz" \
            our-runner our-runner-updater packaging/install.sh \
            packaging/systemd/our-runner.service packaging/systemd/our-runner-updater.service \
            packaging/systemd/our-runner-updater.socket packaging/tmpfiles/our-runner-updater.conf \
            packaging/updater.yaml packaging/update-trust.json config.example.yaml README.md
        make_archive "$dist/our-runner_1.2.3_darwin_$arch.tar.gz" our-runner README.md
        make_archive "$dist/our-runner-update_1.2.3_linux_$arch.tar.gz" our-runner our-runner-updater
    done
    for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
        make_archive "$dist/agentpack_1.2.3_$target.tar.gz" agentpack
    done
    : > "$dist/checksums.txt"
    for archive in "$dist"/*.tar.gz; do
        printf '%s  %s\n' "$(sha "$archive")" "$(basename "$archive")" >> "$dist/checksums.txt"
    done
    printf '%s  install.sh\n' "$(sha "$fixture/.release/install.sh")" >> "$dist/checksums.txt"
}

run_check() {
    (cd "$1" && sh "$CHECK" dist) > "$WORK/out.log" 2>&1
}

expect_pass() {
    run_check "$1" || fail "$2: expected pass, got: $(cat "$WORK/out.log")"
}

expect_fail() {
    if run_check "$1"; then
        fail "$2: expected the artifact check to fail"
    fi
    grep -Fq "$3" "$WORK/out.log" || fail "$2: expected message '$3', got: $(cat "$WORK/out.log")"
}

fixture="$WORK/fixture"

build_fixture "$fixture"
expect_pass "$fixture" "complete release"

for platform in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
    build_fixture "$fixture"
    rm "$fixture/dist/agentpack_1.2.3_$platform.tar.gz"
    expect_fail "$fixture" "missing agentpack $platform" "no archive matched agentpack_*_$platform.tar.gz"
done

build_fixture "$fixture"
make_archive "$fixture/dist/agentpack_1.2.3_linux_arm64.tar.gz" agentpack README.md
expect_fail "$fixture" "extra file in agentpack archive" "agentpack archive must contain exactly agentpack"

build_fixture "$fixture"
grep -v 'agentpack_1.2.3_darwin_arm64.tar.gz' "$fixture/dist/checksums.txt" > "$WORK/checksums.txt"
cp "$WORK/checksums.txt" "$fixture/dist/checksums.txt"
expect_fail "$fixture" "agentpack archive outside checksums" "checksums.txt does not list agentpack_1.2.3_darwin_arm64.tar.gz"

build_fixture "$fixture"
make_archive "$fixture/dist/our-runner_1.2.3_linux_amd64.tar.gz" \
    our-runner our-runner-updater agentpack packaging/install.sh \
    packaging/systemd/our-runner.service packaging/systemd/our-runner-updater.service \
    packaging/systemd/our-runner-updater.socket packaging/tmpfiles/our-runner-updater.conf \
    packaging/updater.yaml packaging/update-trust.json config.example.yaml README.md
expect_fail "$fixture" "agentpack inside the runner install archive" "Linux archive unexpectedly contains agentpack"

printf 'release artifact fixture tests passed\n'
