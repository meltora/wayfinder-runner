# Testing the runner

## Automated gates

`scripts/check.sh` is the enforced gate. CI runs it in `.github/workflows/checks.yml`
(`format`, `vet`, `test`) and `.github/workflows/security.yml` (`vuln`, `sec`), and
contributors run the same script locally from any directory:

```sh
sh scripts/check.sh
```

With no argument it runs `all`. Each subcommand can also run on its own, and several
can be given in order (for example `sh scripts/check.sh format vet`):

| Subcommand | What it runs | Fails when |
| --- | --- | --- |
| `format` | `gofmt -l .` | any file is listed (the script prints them) |
| `vet` | `go vet ./...`, then `GOOS=darwin GOARCH=arm64 go vet ./...` | either vet run reports a problem |
| `test` | `go test -race -count=1 ./...` | any test fails |
| `vuln` | govulncheck, pinned in the script | the code reaches a known vulnerability |
| `sec` | gosec `-quiet -exclude-generated ./...`, pinned in the script, with no rule or path exclusions | gosec reports any issue |
| `all` | `format`, `vet`, `test`, `vuln`, `sec` in that order | any step fails |

The govulncheck and gosec versions are defined once, at the top of the script. An
unknown subcommand prints usage and exits with status 2. A gosec finding is fixed in
code; a `#nosec` annotation is allowed only with a `-- reason` that justifies it.

Coverage and the packaging checks are run separately:

```sh
go test -coverpkg=./... -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
sh -n packaging/bootstrap.sh packaging/install.sh packaging/scripts/preinstall.sh packaging/scripts/postinstall.sh packaging/tests/bootstrap_test.sh packaging/tests/install_test.sh packaging/tests/systemd_test.sh packaging/tests/release_artifacts_test.sh packaging/scripts/prepare-release.sh packaging/tests/prepare_release_test.sh packaging/job-image/build.sh packaging/check-image-go/build.sh packaging/scripts/fresh-host-doctor.sh packaging/tests/fresh_host_doctor_test.sh packaging/scripts/refuse-published-tag.sh packaging/tests/published_tag_test.sh packaging/tests/check_image_go_build_test.sh
sh packaging/tests/bootstrap_test.sh
sh packaging/tests/install_test.sh
sh packaging/tests/systemd_test.sh
sh packaging/tests/prepare_release_test.sh
sh packaging/tests/published_tag_test.sh
sh packaging/tests/release_artifacts_fixture_test.sh
sh packaging/tests/fresh_host_doctor_test.sh
sh packaging/tests/check_image_go_build_test.sh
docker run --rm -v "$PWD:/work" -w /work \
  -e OUR_RUNNER_RELEASE_REPOSITORY=example/app-runner -e OUR_RUNNER_CHANNEL=develop -e OUR_RUNNER_CONTROL_PLANE_URL=https://control.example.test \
  goreleaser/goreleaser:v2.17.1 check
docker run --rm -v "$PWD:/work" -w /work \
  -e OUR_RUNNER_RELEASE_REPOSITORY=example/app-runner -e OUR_RUNNER_CHANNEL=develop -e OUR_RUNNER_CONTROL_PLANE_URL=https://control.example.test \
  goreleaser/goreleaser:v2.17.1 release --snapshot --clean --skip=publish,sign,sbom
sh packaging/tests/release_artifacts_test.sh dist snapshot
./dist/our-runner-darwin-development_darwin_arm64_v8.0/our-runner version
```

Integration tests use a mock control plane and fake provider process. They cover
enrollment, mTLS transport/replay, plan and build jobs, multi-repository jobs,
checks, trusted Git finalization, artifacts, leases, emergency stop, Agent Pack
verification, and self-update. Production release CI must additionally exercise
Linux rootless Docker, the real immutable job image, restricted egress,
certificate rotation, upgrade/rollback, and recovery after forced termination.

## Gated rootless Docker tests

These tests start real containers, so they are skipped unless the gate is set.
Point `DOCKER_HOST` at a rootless daemon owned by the user running the tests and
`JOB_IMAGE` at the pinned job image:

```sh
DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock \
RUN_DOCKER_SANDBOX_PROBE=1 \
JOB_IMAGE=<registry>/<image>@sha256:<digest> \
go test ./internal/isolation/ ./internal/daemon/ \
  -run 'Integration|Container|Ownership|TestRecoveryReclaimsJobOwnedLeftoversOnARootlessDaemon|TestPlanCheckoutCarryForwardOnARootlessDaemon' -v -count=1
```

They cover the user-namespace map, ownership hand-off and re-owning of job
trees (including symlinks, a `000` directory, a `0600` file and a FIFO made by
the job user), the job user's security properties inside a nested provider
container, Agent Pack read-only mounts, the nested sandbox probe, recovery of
job-owned leftovers after a simulated crash, and hand-off timing on a
100 000-entry tree.

`TestPlanCheckoutCarryForwardOnARootlessDaemon` skips on a daemon that is not
rootless. It proves that the real hand-off changes ctime but not the plan
checkout identity, that plan checkout verification still passes after the
baseline is carried forward and the job user runs a read-only
`git rev-parse HEAD`, and that a write by the job user is refused.

The provider CLI probe tests run in the same package under the same gate:

```sh
DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock \
RUN_DOCKER_SANDBOX_PROBE=1 \
JOB_IMAGE=<registry>/<image>@sha256:<digest> \
INCOMPATIBLE_JOB_IMAGE=<older image>@sha256:<digest> \
go test ./internal/isolation/ \
  -run 'ProviderCLIProbeIntegration|TestJobImageSandboxRuntimeStartsUnderTheNestedSandboxFlags|TestClaudeBash' -v -count=1
```

The `TestClaudeBash` cases run the image's real `claude` offline: no
credentials, `--network none`, and a local fake Messages API on loopback that
asks for one Bash call. The argv, settings file and container environment come
from `Claude.BuildSpec` for a Docker build job, under the job flags and the
unprivileged bubblewrap `PATH`, with a logging `bwrap` wrapper earlier on
`PATH`. Without `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB`, Bash must run in a nested
PID namespace with `--bind /proc /proc`, unable to read a denied canary or
write outside the allowed paths. The control case sets the scrub and expects
`--proc /proc` and `Can't mount proc`. Claude did not use a wrapper owned by
the job user under its home directory (cause not verified), so the test mounts
the wrapper read-only from a directory that the rootless daemon maps to root.

They probe `claude --version` and `claude --help` inside `JOB_IMAGE` as the
job user and require every runner option to be declared, start `srt` as the
job user with the nested-sandbox flags and expect `sandbox-ok`, check that
`rg --version` runs, and check that `/usr/bin/bwrap` is the only set-ID file.
`INCOMPATIBLE_JOB_IMAGE` is optional; when set it names an image whose Claude
Code release predates `--permission-prompts` and `--restricted`, and the test
requires the probe to report exactly those two options as missing.

The stdin prompt probes run under the same gate:

```sh
DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock \
RUN_DOCKER_SANDBOX_PROBE=1 \
JOB_IMAGE=<registry>/<image>@sha256:<digest> \
go test ./internal/isolation/ -run 'StdinInTheJobImage' -v -count=1
```

`TestClaudeReadsALargePromptFromStdinInTheJobImage` and
`TestOpenCodeReadsALargePromptFromStdinInTheJobImage` build a Docker build-job
spec for a prompt over 256 KiB, first show that passing it as one argument fails
with `E2BIG` (Claude case), then run the image's real CLI offline under
`--network none` with the prompt file as `docker run -i` standard input. A fake
Messages or Chat Completions API on loopback records the size and SHA-256 of each
user text block; the first request must carry the exact prompt. Containers get
unique names and the `fr08-stdin-prompt` runner label, and the tests fail if any
labelled container remains.

The harness-state and check-image tests run under the same gate. `CHECK_IMAGE`
names a digest-pinned Go check image, for example the reference image built by
`sh packaging/check-image-go/build.sh our-runner-check-go:<tag>` on the rootless
daemon (use the printed pinned reference; on a classic image store the script
prints one only with `--push`):

```sh
DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock \
RUN_DOCKER_SANDBOX_PROBE=1 \
JOB_IMAGE=<registry>/<image>@sha256:<digest> \
CHECK_IMAGE=<pinned reference printed by build.sh> \
go test ./internal/isolation/ ./internal/daemon/ \
  -run 'TestClaudeCreatesAPrivateStagingDirectoryInTheHandedOffWorkspace|TestIterativeSnapshotSkipsHarnessStateDuringARootlessHandOff|TestGoCheckImageRunsGoTestUnderTheProductionPolicy' -v -count=1
```

`TestClaudeCreatesAPrivateStagingDirectoryInTheHandedOffWorkspace` runs the
job image's real `claude` offline in a handed-off workspace and records that
`.claude/.cc-writes` is a 0700 directory owned by the job user that the runner
cannot read. `TestIterativeSnapshotSkipsHarnessStateDuringARootlessHandOff`
creates that directory as the job user after a rootless hand-off and requires
the iterative snapshot to publish the work product without it.
`TestGoCheckImageRunsGoTestUnderTheProductionPolicy` runs `platform_run_check`
for `go test ./...` on a fixture module through the production check path:
it passes in `CHECK_IMAGE` (the fixture test itself asserts uid and gid 10001, a
read-only root, a noexec `/tmp` and no network), prints its `ok` line, fails
with `exit_nonzero` when a test fails, and still fails in `JOB_IMAGE` when the
check sets no image. `CHECK_IMAGE` is required only by the last test.

`TestImageStoreResolvesALocallyBuiltImageByDigest` records how each image store
names a locally built image. It builds a throwaway `FROM scratch` image with one
small file and no network, tagged `our-runner-classic-probe:<random>` with a
unique `our-runner-classic-probe` label, removes it in cleanup and fails if any
image with that label remains. Under `RUN_DOCKER_CLASSIC_STORE_PROBE=1` it runs
on the classic-store daemon named by `CLASSIC_DOCKER_HOST` and requires that
`<tag>@<image ID>` does not resolve (`InspectImage` returns
`ErrImageResolvesOnlyByID`) while the bare `sha256:<image ID>` does. Under
`RUN_DOCKER_SANDBOX_PROBE=1` it runs on `DOCKER_HOST`, which must use the
containerd image store, and requires that `<tag>@<image ID>` resolves to the ID:

```sh
CLASSIC_DOCKER_HOST=unix:///var/run/docker.sock RUN_DOCKER_CLASSIC_STORE_PROBE=1 \
go test ./internal/isolation/ -run TestImageStoreResolvesALocallyBuiltImageByDigest -v -count=1
DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock RUN_DOCKER_SANDBOX_PROBE=1 \
go test ./internal/isolation/ -run TestImageStoreResolvesALocallyBuiltImageByDigest -v -count=1
```

`packaging/tests/check_image_go_build_test.sh` runs the real
`packaging/check-image-go/build.sh` against a fake `docker` for the containerd
and classic stores, with and without `--push`: a pinned reference only when it
resolves to the built ID, the classic-store refusal, repository digest
selection (another repository first, a registry with a port), push failure, a
missing, uppercase or mismatched repository digest, usage errors, and never a
pull.

The Codex stdin smoke runs the host's `codex` against a local refusing endpoint,
once for a new session and once resuming that session's real thread ID:

```sh
RUN_PROVIDER_STDIN_SMOKE=1 go test ./internal/provider/ -run StdinSmoke -v -count=1
```

Agent Pack tests cover the cross-language canonical publication digest,
Ed25519-SPKI trust validation, exact authority/reference/provider/version
binding, pre-network rejection, HTTPS origin policy, redirects, response
encoding, size limits, cancellation, streaming archive digests, corrupt-cache
recovery, concurrent cache convergence, LRU pruning, and provider-start
blocking. Malicious archive cases include traversal, absolute paths, links,
devices, FIFOs, sparse metadata, set-ID modes, duplicate and case-colliding
paths, file/directory collisions, unsafe characters, excessive depth/count/
size, unknown or multi-document manifests, internal digest mismatches, multiple
gzip members, and trailing payloads. Materialized job views are checked for
0444 files and 0555 directories; production Docker execution adds a read-only
bind mount.
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
the protocol. A valid payload also carries the exact `project_id`, `lease_id`,
`manifest_id`, `manifest_digest`, and `workflow_revision` issued by the control plane. Revision
planning fixtures must include the immutable request, reviewed base plan, and
structured feedback. Contract tests assert that passive evidence repeats that
authority exactly and that legacy active-lease databases fail closed after their
schema is migrated.

Discovery tests cover the exact catalog, canonical ordering, deterministic scan
identity, one event per repository, durable replay identity, and rejection of
secrets, authentication caches, binaries, devices, oversized files, and catalog
symlinks without content or digest leakage.

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
SQLite insertion order instead of lexical ID order. Keyset tests acknowledge a
full first page while replaying 101 events and prove the last event is not
skipped. They also cover finite replay snapshots, send interruption and
reconnect, corrupt-row advancement, legacy schema migration, and monotonic
sequence allocation after acknowledged rows are pruned.

Lease-renewal tests prove the runner waits until the latter third of the current
authority window, emits one stable manifest-bound request per window, rejects
non-increasing or incorrectly fenced extensions, and persists the new window.
The integration fixture runs an agent beyond its original expiry, delivers a
strict `renew_lease` command, and proves that the job completes under renewed
authority. Transport tests also assert that live delivery sends the exact
journaled envelope rather than rebuilding mutable metadata such as `sent_at`.

Inbound-command recovery tests cover durable receive/claim/terminal receipt
transitions, canonical content-digest conflicts, concurrent duplicate delivery,
legacy pending-command adoption, acknowledgement replay, journal pruning, and
effect-before-outcome crash windows. The integration fixture sends the same
command ID with refreshed delivery metadata, verifies the exact accepted
acknowledgement is replayed, and confirms that changed content under that ID is
rejected without applying its effect.

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
  the restricted network identity, discovery labels, and root egress-policy
  verifier; do not switch to unrestricted bridge networking.
- Agent starts but publishes nothing: inspect MCP socket/token mounting and the
  signed Agent Pack. Hook events are telemetry and cannot replace MCP results.
- Build passes but cannot push: verify the service user's trusted Git credential
  helper or SSH agent and the registered repository URL.
- Runner stays paused: use `resume` only after resolving the emergency-stop cause.
- Repeated restart: inspect the previous systemd logs before changing state; the
  durable journal and lease reconciliation are designed to survive a restart.
