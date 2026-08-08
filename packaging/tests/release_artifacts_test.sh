#!/bin/sh
set -eu

DIST_DIR=${1:-dist}

fail() {
    printf 'release artifact test failed: %s\n' "$*" >&2
    exit 1
}

find_archive() {
    pattern=$1
    found=
    for candidate in "$DIST_DIR"/$pattern; do
        [ -f "$candidate" ] || continue
        [ -z "$found" ] || fail "multiple archives matched $pattern"
        found=$candidate
    done
    [ -n "$found" ] || fail "no archive matched $pattern"
    printf '%s\n' "$found"
}

archive_contains() {
    tar -tzf "$1" | sed 's#^\./##' | grep -Fqx "$2"
}

linux_archive=$(find_archive 'our-runner_[0-9]*_linux_amd64.tar.gz')
darwin_archive=$(find_archive 'our-runner_*_darwin_amd64.tar.gz')
update_archive=$(find_archive 'our-runner-update_*_linux_amd64.tar.gz')
update_archive_arm64=$(find_archive 'our-runner-update_*_linux_arm64.tar.gz')

for required in \
    our-runner \
    our-runner-updater \
    packaging/install.sh \
    packaging/systemd/our-runner.service \
    packaging/systemd/our-runner-updater.service \
    packaging/systemd/our-runner-updater.socket \
    packaging/tmpfiles/our-runner-updater.conf \
    packaging/updater.yaml \
    packaging/update-trust.json \
    config.example.yaml \
    README.md; do
    archive_contains "$linux_archive" "$required" || fail "Linux archive is missing $required"
done

for required in our-runner README.md; do
    archive_contains "$darwin_archive" "$required" || fail "Darwin archive is missing $required"
done

for forbidden in our-runner-updater packaging/install.sh packaging/systemd/our-runner.service packaging/systemd/our-runner-updater.service packaging/systemd/our-runner-updater.socket packaging/updater.yaml packaging/update-trust.json config.example.yaml; do
    if archive_contains "$darwin_archive" "$forbidden"; then
        fail "Darwin archive unexpectedly contains $forbidden"
    fi
done

expected_update_entries=$(printf '%s\n' our-runner our-runner-updater | sort)
for minimal_archive in "$update_archive" "$update_archive_arm64"; do
    update_entries=$(tar -tzf "$minimal_archive" | sed 's#^\./##' | sed '/^$/d' | sort)
    [ "$update_entries" = "$expected_update_entries" ] || \
        fail "minimal update archive must contain exactly our-runner and our-runner-updater: $minimal_archive"
done

[ -f "$DIST_DIR/checksums.txt" ] || fail "checksums.txt was not produced"
[ "$(awk '$2 == "install.sh" { count++ } END { print count + 0 }' "$DIST_DIR/checksums.txt")" -eq 1 ] || \
    fail "checksums.txt must contain exactly one install.sh entry"

expected=$(awk '$2 == "install.sh" { print $1 }' "$DIST_DIR/checksums.txt")
actual=$(if command -v sha256sum >/dev/null 2>&1; then
    sha256sum .release/install.sh | awk '{print $1}'
else
    shasum -a 256 .release/install.sh | awk '{print $1}'
fi)
[ "$actual" = "$expected" ] || fail "install.sh checksum does not match the signed manifest input"

printf 'release artifact tests passed\n'
