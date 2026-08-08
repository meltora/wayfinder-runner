#!/bin/sh
set -eu

if ! id -u our-runner >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/our-runner --shell /usr/sbin/nologin our-runner
fi
