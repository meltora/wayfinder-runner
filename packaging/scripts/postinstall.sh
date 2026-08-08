#!/bin/sh
set -eu

if ! id -u our-runner >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/our-runner --shell /usr/sbin/nologin our-runner || true
fi
mkdir -p /var/lib/our-runner /var/log/our-runner
chown -R our-runner:our-runner /var/lib/our-runner /var/log/our-runner
chmod 700 /var/lib/our-runner
if [ -f /etc/our-runner/config.yaml ]; then
    chown root:our-runner /etc/our-runner/config.yaml
    chmod 640 /etc/our-runner/config.yaml
fi

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    systemctl enable our-runner.service || true
fi

echo "our-runner installed. Enroll with:"
echo "  1. install and verify rootless Docker for the our-runner user"
echo "  2. write the one-time code to /var/lib/our-runner/enrollment-code"
echo "  3. chown our-runner:our-runner /var/lib/our-runner/enrollment-code && chmod 600 /var/lib/our-runner/enrollment-code"
echo "  4. sudo -u our-runner /usr/bin/our-runner enroll --state-dir /var/lib/our-runner --code-file /var/lib/our-runner/enrollment-code --url <https-url>"
echo "  5. remove the code file, run doctor, then: systemctl start our-runner"
