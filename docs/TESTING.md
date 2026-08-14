# Testing the runner

## Automated gates

Run these from the repository root:

```sh
go test ./...
go test -race ./...
go vet ./...
go test -coverpkg=./... -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 ./...
sh -n packaging/bootstrap.sh packaging/install.sh packaging/scripts/preinstall.sh packaging/scripts/postinstall.sh packaging/tests/bootstrap_test.sh packaging/tests/install_test.sh packaging/tests/systemd_test.sh packaging/tests/release_artifacts_test.sh
sh packaging/tests/bootstrap_test.sh
sh packaging/tests/install_test.sh
sh packaging/tests/systemd_test.sh
mkdir -p .release
sed 's|__OUR_RUNNER_RELEASE_REPOSITORY__|example/app-runner|g' packaging/bootstrap.sh > .release/install.sh
docker run --rm -v "$PWD:/work" -w /work goreleaser/goreleaser:v2.17.1 check
docker run --rm -v "$PWD:/work" -w /work goreleaser/goreleaser:v2.17.1 release --snapshot --clean --skip=publish,sign,sbom
sh packaging/tests/release_artifacts_test.sh
```

Integration tests use a mock control plane and fake provider process. They cover
enrollment, mTLS transport/replay, plan and build jobs, multi-repository jobs,
checks, trusted Git finalization, artifacts, leases, emergency stop, Agent Pack
verification, and self-update. Production release CI must additionally exercise
Linux rootless Docker, the real immutable job image, restricted egress,
certificate rotation, upgrade/rollback, and recovery after forced termination.
The production update-promotion test must use the protected
`update-production` environment, publish only after a draft release becomes a
published release, reject a non-increasing sequence, and confirm that the OIDC
role cannot delete update objects or use unrelated KMS keys.
The bootstrap test uses a fake signed release boundary to cover pinned and
latest Linux installs plus the macOS development path. It proves that a valid
two-binary Linux archive installs, while a missing updater, checksum mismatch,
or failed Cosign verification stops before the packaged installer runs. The
service-installer test exercises legacy migration, preservation of configuration
and state, immutable slots, idempotency, atomic-link rollback, independent
`current` and proven `updater-current` links, and symlink/path boundary rejection
without modifying the host. The systemd test keeps the privilege boundary
explicit: only the root updater can write the installation tree, its service
executes from `updater-current` rather than the candidate runner slot, and the
runner reaches it through a group-restricted socket. The release
artifact test validates the real GoReleaser archive contents and confirms that
the minimal update archive contains exactly the two executables, while
`install.sh` is present in the checksum manifest signed by the release workflow.

## Local mock-control-plane test

Local testing deliberately opts into unsafe direct execution. Use only disposable
repositories and credentials:

```sh
go run ./cmd/mock-cp
```

In a second terminal, create a private state directory and enroll using the URL
printed by the mock server:

```sh
install -d -m 0700 /tmp/our-runner-dev
printf '%s\n' 'dev-code' > /tmp/our-runner-dev/enrollment-code
chmod 0600 /tmp/our-runner-dev/enrollment-code

go run ./cmd/our-runner enroll \
  --code-file /tmp/our-runner-dev/enrollment-code \
  --url http://127.0.0.1:PORT \
  --state-dir /tmp/our-runner-dev
```

Use a configuration equivalent to the following, substituting the exact
WebSocket URL and a disposable local repository:

```yaml
control_plane_url: ws://127.0.0.1:PORT/v1/runners/stream
control_plane_http_url: http://127.0.0.1:PORT
state_dir: /tmp/our-runner-dev
transport: websocket
isolation: direct
allow_unsafe_direct: true
supervisor: direct
provider: codex
repositories:
  demo:
    url: /absolute/path/to/disposable/repository
```

Then run:

```sh
go run ./cmd/our-runner doctor \
  --url http://127.0.0.1:PORT \
  --state-dir /tmp/our-runner-dev
go run ./cmd/our-runner start --config /tmp/runner-config.yaml
```

Use the mock control plane's test API or the integration-test fixtures for a
valid `start_job` payload. Job specs now reference `repositories` by registered
ID; arbitrary `repository_url` and free-form environment fields are not part of
the protocol. A valid payload also carries the exact `lease_id`, `manifest_id`,
`manifest_digest`, and `workflow_revision` issued by the control plane. Revision
planning fixtures must include the immutable request, reviewed base plan, and
structured feedback. Contract tests assert that passive evidence repeats that
authority exactly and that legacy active-lease databases fail closed after their
schema is migrated.

Planning integration tests publish all required plan sections and assert the
strict `plan_published` shape. They also cover both crash windows around durable
event publication, reject a second idempotency key for the same attempt, and
bind a revision event to the exact reviewed base revision ID.

Provider-capacity tests assert the strict manifest-bound wire shape, local
fail-closed validation, UTF-8 byte limits, exact single-repository recovery
lineage, and omission of unsafe or incomplete lineage. The end-to-end build
fixture proves the capacity event repeats the retained lease, manifest, attempt,
and fencing authority accepted by the control plane, emits no competing terminal
outcome, and releases local authority only after durable recording.

Terminal-outcome tests assert the strict manifest-bound `job_finished` shape,
state-to-failure-code consistency, bounded failure detail, and omission of build
provenance that belongs in dedicated evidence events. Journal tests insert
multiple event IDs with one timestamp and prove reconnect replay follows durable
SQLite insertion order instead of lexical ID order.

Lease-renewal tests prove the runner waits until the latter third of the current
authority window, emits one stable manifest-bound request per window, rejects
non-increasing or incorrectly fenced extensions, and persists the new window.
The integration fixture runs an agent beyond its original expiry, delivers a
strict `renew_lease` command, and proves that the job completes under renewed
authority. Transport tests also assert that live delivery sends the exact
journaled envelope rather than rebuilding mutable metadata such as `sent_at`.

## Troubleshooting

```sh
our-runner status --state-dir /var/lib/our-runner
our-runner support-bundle --state-dir /var/lib/our-runner
our-runner scan-agent-config --config /path/to/repository
journalctl -u our-runner
journalctl -u our-runner-updater
systemctl status our-runner-updater.socket
```

- No commands: confirm outbound DNS/TLS and the mTLS certificate with `doctor`.
- Agent cannot reach its provider: `job_network: none` blocks all network. Check
  the restricted network and its policy labels; do not switch to unrestricted
  bridge networking.
- Agent starts but publishes nothing: inspect MCP socket/token mounting and the
  signed Agent Pack. Hook events are telemetry and cannot replace MCP results.
- Build passes but cannot push: verify the service user's trusted Git credential
  helper or SSH agent and the registered repository URL.
- Runner stays paused: use `resume` only after resolving the emergency-stop cause.
- Repeated restart: inspect the previous systemd logs before changing state; the
  durable journal and lease reconciliation are designed to survive a restart.
