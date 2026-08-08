#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SYSTEMD_DIR="$SCRIPT_DIR/../systemd"
RUNNER="$SYSTEMD_DIR/our-runner.service"
UPDATER="$SYSTEMD_DIR/our-runner-updater.service"
SOCKET="$SYSTEMD_DIR/our-runner-updater.socket"

fail() {
    printf 'systemd boundary test failed: %s\n' "$*" >&2
    exit 1
}

expect_line() {
    grep -Fqx "$2" "$1" || fail "$(basename "$1") is missing: $2"
}

expect_line "$RUNNER" 'User=our-runner'
expect_line "$RUNNER" 'Group=our-runner'
expect_line "$RUNNER" 'ExecStart=/opt/our-runner/current/our-runner start --config /etc/our-runner/config.yaml'
expect_line "$RUNNER" 'ProtectSystem=strict'
expect_line "$RUNNER" 'ReadOnlyPaths=/opt/our-runner /etc/our-runner'
expect_line "$RUNNER" 'ReadWritePaths=/var/lib/our-runner'
if grep -Eq '^ConfigurationDirectory=' "$RUNNER"; then
    fail 'systemd must not transfer ownership of the root-controlled configuration directory to the runner'
fi
if grep -Eq '^ReadWritePaths=.*(/opt/our-runner|/etc/our-runner|/usr/bin)' "$RUNNER"; then
    fail 'unprivileged runner can write an installation or configuration boundary'
fi

expect_line "$SOCKET" 'ListenStream=/run/our-runner-updater/control.sock'
expect_line "$SOCKET" 'SocketUser=root'
expect_line "$SOCKET" 'SocketGroup=our-runner'
expect_line "$SOCKET" 'SocketMode=0660'
expect_line "$SOCKET" 'FileDescriptorName=control'

expect_line "$UPDATER" 'User=root'
expect_line "$UPDATER" 'Group=root'
expect_line "$UPDATER" 'ExecStart=/opt/our-runner/updater-current serve --config /etc/our-runner/updater.yaml'
if grep -Fq '/current/our-runner-updater' "$UPDATER"; then
    fail 'recovery service executes the updater from the unproven runner slot'
fi
expect_line "$UPDATER" 'Sockets=our-runner-updater.socket'
expect_line "$UPDATER" 'ProtectSystem=strict'
expect_line "$UPDATER" 'ReadWritePaths=/opt/our-runner /var/lib/our-runner-updater'
expect_line "$UPDATER" 'NoNewPrivileges=true'
expect_line "$UPDATER" 'CapabilityBoundingSet=CAP_SETUID CAP_SETGID CAP_KILL'
expect_line "$UPDATER" 'AmbientCapabilities='
if grep -Eq '^CapabilityBoundingSet=.*CAP_SYS_PTRACE' "$UPDATER"; then
    fail 'updater must not receive process-inspection capability'
fi
expect_line "$UPDATER" 'RestrictNamespaces=true'
expect_line "$UPDATER" 'MemoryMax=768M'
expect_line "$UPDATER" 'TasksMax=64'
expect_line "$UPDATER" 'LimitNOFILE=1024'
if grep -Eq '^RuntimeMaxSec=' "$UPDATER"; then
    fail 'updater runtime is capped before health and recovery deadlines can complete'
fi

if grep -Eq '^ReadWritePaths=.*(/etc/our-runner|/var/lib/our-runner([[:space:]]|$)|/usr/bin)' "$UPDATER"; then
    fail 'updater can write runner identity/configuration or the compatibility link'
fi

printf 'systemd boundary tests passed\n'
