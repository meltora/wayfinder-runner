# app-runner

Customer-hosted execution plane for the Autonomous Software Delivery Platform.
The runner connects outbound to the control plane, executes a single leased job
at a time in a constrained container, and reports authoritative progress through
a job-scoped MCP bridge. Customers keep their repositories, agent instructions,
Claude/Codex/OpenCode authentication, and build environment on infrastructure they own.

This repository implements the runner boundary. Jira, GitHub/GitLab/Bitbucket,
Slack, Sentry, workflow design, approvals, preview deployment, organization and
team management, and completion-state policy belong to the control plane and
its integrations.

## Current production boundary

The supported production target is Linux with systemd and rootless Docker (or a
Docker daemon configured with user-namespace remapping). macOS and direct
process isolation are development-only. Firecracker isolation is a future
hardening layer and is not implemented here.

A production deployment also needs:

- an immutable job image pinned by `sha256`;
- a runner-owned Git credential helper or SSH agent, never credentials in URLs;
- a pre-created, externally enforced Docker egress network for cloud-backed
  Claude, Codex, or OpenCode jobs;
- control-plane enrollment, Agent Pack, artifact, and update signing keys;
- release checksum/signature verification and tested backup/recovery procedures.

`job_network: none` is the secure default, but cloud-backed agent CLIs cannot
operate without an administrator-provisioned restricted egress network. The
runner verifies that network labels match its configured policy ID; the network
policy itself must be enforced outside the runner.

## Security and trust model

- The daemon opens no inbound TCP listener. Enrollment uses HTTPS and normal
  operation uses an outbound mTLS WebSocket.
- The runner identity key and certificate stay in its private state directory.
  Enrollment codes and MCP tokens are accepted from private files, not command
  arguments.
- Each job has a durable lease and monotonically checked fencing token. Duplicate
  control-plane commands, lease-renewal requests, and MCP tool calls are
  idempotent. Long-running jobs request renewal during the latter third of the
  current authority window and accept only a manifest-bound, fencing-checked
  extension from the control plane.
- Every job container runs under rootless/user-namespaced Docker with a read-only
  root filesystem, non-root UID/GID, PID/CPU/memory limits, private temporary
  filesystems, and either no network or the verified restricted network.
  Credential-free check and Git containers drop every capability and enforce
  `no-new-privileges`. Provider containers drop all capabilities and add back
  only `SYS_ADMIN`, `SYS_CHROOT`, `SETUID`, and `SETGID`, with an unconfined
  outer seccomp profile, so the mandatory inner Codex/Claude bubblewrap sandbox
  can start. OpenCode jobs do not receive those capabilities: the hardened
  adapter disables shell, LSP, web, custom-tool, and subagent execution and
  delegates tests to runner-owned checks. Both modes deny model tool access to
  provider credentials.
- Agent processes receive only their repository workspace, a job-specific home,
  and a read-only MCP socket/token mount. They do not receive the runner's mTLS
  identity or control-plane credentials.
- Repository instructions such as `AGENTS.md`, `CLAUDE.md`, skills, commands,
  hooks, and MCP declarations are inventoried without execution. The provider
  may then use repository-native instructions as code owned by that repository.
  Platform overlays are delivered as signed, digest-pinned, read-only Agent
  Packs.
- Untrusted job workspaces never push directly. Git finalization occurs in a
  fresh credential-free container, produces a bundle, and the trusted host
  mirror verifies the exact commit before pushing with hooks disabled.
- Events and artifacts are written locally before upload. SQLite replay,
  monotonic keyset cursors, bounded spools, integrity checks, retry,
  acknowledgement, retention, and startup reconciliation protect against
  duplicate delivery, skipped pages, and crashes.
- Support bundles use an allowlist and exclude identities, journals,
  repositories, agent homes, tokens, and job contents.

The customer trusts its own repositories, selected agent instructions, installed
provider credentials, configured Git credentials, signed Agent Packs, job image,
and infrastructure administrators. Repository code and model output remain
untrusted with respect to the runner identity and control plane.

## Execution flow

1. The control plane sends a validated `start_job` command containing the exact
   project ID, lease ID, manifest ID/kind/digest, workflow revision, fencing
   token, ticket context, one to sixteen registered repository IDs, and
   an Agent Pack reference paired with its signed delivery authority. The
   runner rejects an unsigned, mismatched, untrusted, incompatible, or malformed
   pack before acquiring a lease or preparing a workspace, and preserves this
   immutable authority on every accepted progress, checkpoint, plan-publication,
   provider-capacity, terminal, and artifact event.
2. The daemon durably records immutable command content and a receipt state
   before applying any effect. Redelivery replays the original terminal
   acknowledgement; reuse of a command ID with changed content fails closed.
   An interrupted processing receipt is reclaimed after reconnect or restart,
   including exact-authority recovery for a start whose lease was installed
   before its acknowledgement was persisted. The daemon then acquires the
   single active lease. While the job remains active, it emits at most one durable
   `lease_renewal` request per authority window. A successful `renew_lease`
   command must repeat the owning attempt and fencing token and strictly extend
   the current expiration; the renewed window is persisted before execution
   continues.
3. Each repository is cloned from a trusted local mirror into a self-contained
   job workspace. No job receives mirror metadata or host Git credentials.
4. For each repository, the runner scans only the versioned catalog beneath the
   exact checkout and durably publishes one metadata-only
   `agent_config_discovery` event. It never executes discovered material,
   follows catalog symlinks, or publishes raw content. It then verifies the
   Agent Pack Ed25519 publication attestation, downloads the exact archive from
   an approved HTTPS origin, verifies its whole-archive SHA-256 digest, safely
   validates and extracts it, and exposes a read-only job view. Only after that
   succeeds does it prepare an isolated provider home and start a token-scoped
   Unix MCP bridge.
5. Codex, Claude, or OpenCode receives the ticket/approved plan and invokes platform MCP
   tools to publish plan revisions, progress, decisions, checkpoints, preview
   metadata, and artifact declarations. A plan-revision job retrieves the full
   reviewed base plan and structured feedback with `platform_get_job_context`;
   the potentially large context is not copied into process arguments. Provider
   hooks are non-authoritative telemetry only.

Planning tools publish the complete versioned plan content contract, not a
runner-local summary or revision number. Initial plans have no base revision;
revision plans must repeat the exact reviewed `base_revision_id` supplied in the
immutable manifest. The control plane remains the sole authority that assigns
revision numbers. Each planning attempt is durably bound to one stable event and
one idempotency key before publication, allowing crash recovery without emitting
a competing plan.
6. For build jobs, administrator-defined checks run in fresh, credential-free
   containers. Successful changes are committed and bundled in isolation, then
   imported and optionally pushed from the trusted mirror.
7. A strict `job_finished` event records only the manifest-bound authority,
   controlled terminal state/failure evidence, and optional provider session.
   It is durably queued before the local lease is released. The exact envelope
   that is journaled is also used for live delivery, so an acknowledgement loss
   cannot replay the same event ID with different bytes. Durable events replay in
   SQLite insertion order so preceding plan, progress, test, review, and artifact
   evidence cannot be overtaken by completion after reconnect. Each reconnect
   replays a finite sequence snapshot; acknowledgements may remove earlier rows
   without shifting later pages, while events appended during the pass remain
   queued for the next pass.
   Workspaces, job homes, sockets, tokens, and labeled containers are removed.
   Crash recovery reconciles an unfinished lease and queued uploads after restart.
8. Cancellation kills the complete supervised process tree. Emergency stop also
   persists the paused state so no new job starts until an explicit resume.

When a provider reports subscription-capacity exhaustion during a single-
repository build, trusted finalization commits and pushes the partial-work
branch before the runner emits recovery lineage. The capacity event includes
the exact retained lease and manifest authority, a bounded redacted provider
message, and that branch and full commit SHA plus the opaque current-attempt
checkpoint reference when a checkpoint exists. It omits recovery lineage if
finalization or publication fails. The event's resume policy is descriptive;
the control plane applies its stored project policy. Protocol v1 also omits
lineage for multi-repository jobs, because a single branch/commit pair cannot
safely represent their full change set; those attempts remain paused for manual
resolution until a versioned multi-repository lineage contract is available.
Capacity exhaustion is a resumable non-terminal condition and emits no competing
`job_finished` event; the capacity event itself is durably queued before the
runner releases its local lease.

The runner finishing a job or a merge request being merged does **not** mark a
feature Done. Merge may move the control-plane workflow to Ready for QA, but only
an authorized user action marks the work item Done.

## Multiple repositories

A project or job may use multiple repositories. An administrator registers each
URL under an opaque local ID in the runner configuration. A job references only
those IDs and selects exactly one primary repository. Branches, checks, commits,
and output commit SHAs are tracked per repository. Jobs cannot introduce an
arbitrary repository URL.

```yaml
repositories:
  web-app:
    url: ssh://git@example.com/company/web-app.git
  shared-api:
    url: https://example.com/company/shared-api.git

checks:
  web-unit:
    command: npm
    args: [test, --, --runInBand]
    timeout_seconds: 900
    repository_id: web-app
```

Repository credentials must come from the `our-runner` user's SSH agent or Git
credential helper. Passwords, tokens, queries, and fragments in repository URLs
are rejected.

## Runner-local source publication

Post-build pull-request publication is a separate fenced runner operation; it
does not run inside the coding agent and the control plane never receives the
provider token. Bind the control plane's exact installation ID to a private
credential file on the runner:

```yaml
source_publication_installations:
  installation-1:
    provider: github
    api_base_url: https://api.github.com
    token_file: /run/secrets/our-runner/github-installation-1
  installation-2:
    provider: gitlab
    api_base_url: https://gitlab.com/api/v4
    token_file: /run/secrets/our-runner/gitlab-installation-2
  installation-3:
    provider: bitbucket
    api_base_url: https://api.bitbucket.org/2.0
    token_file: /run/secrets/our-runner/bitbucket-installation-3
```

`api_base_url` defaults to `https://api.github.com` for GitHub and
`https://gitlab.com/api/v4` for GitLab, and may point to the provider's canonical
HTTPS self-managed API base. Bitbucket means Bitbucket Cloud in this transport
version and always uses `https://api.bitbucket.org/2.0`; Bitbucket Data Center
is not treated as API-compatible. The token file must be an absolute, regular,
non-symlink file with no group or world permissions; a typical mode is `0600`.
Use a GitLab personal, project, or group access token with the minimum API and
repository permissions required by the installation. For Bitbucket Cloud, use
a repository, project, or workspace access token with `repository`,
`pullrequest`, and `pullrequest:write` scopes; it is sent only as a Bearer token.
Bitbucket repository external IDs use the canonical `workspace/repo_slug` form.
The daemon fails startup for an explicitly configured unsafe credential and
rechecks credential readiness before each capability refresh and publication.

The runner advertises source-publication transport v1 and each ready provider
only while a matching local installation is usable. Before creating or reusing
a pull or merge request, it verifies that the configured repository exists and
that the remote head branch still resolves to the exact full commit SHA
authorized by the control plane. Requests use bounded responses and timeouts,
refuse redirects, and normalize provider failures before emitting a durable
result event. A crash after the provider accepts a request is recovered through
create-or-find idempotency; duplicate command delivery cannot create a second
logical result.

## Signed Agent Packs

Agent Pack trust is administrator-owned runner configuration, never repository
configuration. `agent_pack_public_keys` maps an exact key ID to a canonical
base64-encoded Ed25519 SubjectPublicKeyInfo DER value. Configure
`allowed_artifact_origins` when packs are hosted somewhere other than the
control-plane HTTPS origin. Origins must be canonical HTTPS origins without a
path, query, credentials, fragment, uppercase host, trailing dot, explicit
default port, or raw Unicode hostname.

Before any provider process starts, the runner verifies the authority's exact
pack reference, provider, protocol, minimum runner version, publication digest,
and Ed25519 signature. It downloads without redirects or content transformation
under strict time/header/body limits, hashes while streaming, and publishes only
a complete 0400 archive into the private content-addressed cache. Concurrent
jobs converge through an OS-backed per-digest lock; corrupt and partial entries
are rejected or recovered, and bounded LRU pruning protects the active digest.

Extraction accepts one bounded gzip/tar stream containing only declared regular
ASCII-named files. Traversal, links, devices, FIFOs, sparse entries, set-ID bits,
duplicates, case collisions, file/directory collisions, unknown manifest fields,
digest mismatches, oversized input, and trailing payloads fail closed before the
job view is published. Docker jobs receive the resulting directory as an
explicit read-only bind mount. Direct isolation remains development-only.

## Install on Linux

The recommended installation path is the curl installer. The release workflow
publishes `install.sh` with the repository identity baked in and includes it in
the signed checksum manifest; serve that exact asset from the product's HTTPS
installation domain:

```sh
curl -fsSL https://get.ourplatform.com/install.sh | bash
```

It detects Linux amd64/arm64, resolves the latest release, downloads the archive,
signed checksum file, and Sigstore bundle, verifies the GitHub Actions signing
identity with Cosign, verifies the archive SHA-256, and only then invokes the
packaged service installer through `sudo`. If Cosign is unavailable, the script
downloads the pinned Cosign v3.0.6 binary and verifies its embedded SHA-256
before using it. The downloader itself permits only HTTPS, HTTPS redirects, and
TLS 1.2 or newer; users do not need to repeat those internal policy flags in the
public command. Pin a runner version or optionally configure rootless Docker:

```sh
curl -fsSL https://get.ourplatform.com/install.sh | OUR_RUNNER_VERSION=v1.2.3 bash

curl -fsSL https://get.ourplatform.com/install.sh | OUR_RUNNER_SETUP_ROOTLESS_DOCKER=1 bash
```

The custom domain must serve or redirect to the generated release asset, not
the unreplaced source template. On Linux, the verified archive contains both
the unprivileged runner and the privileged updater. Installation creates an
immutable slot under `/opt/our-runner/versions`, atomically switches
`/opt/our-runner/current`, and keeps `/usr/bin/our-runner` as a compatibility
symlink. The root-owned `/opt/our-runner/updater-current` link is initialized
directly to the versioned updater binary and is independent of `current`.
Existing configuration and state are preserved during migration. The manual
signed-release path remains available:

```sh
sudo packaging/install.sh --setup-rootless-docker --version 1.2.3 \
  ./our-runner ./our-runner-updater
sudoedit /etc/our-runner/config.yaml
```

Enrollment must run as the same user as the service so its identity remains
readable by the daemon and inaccessible to other users:

```sh
sudo install -o our-runner -g our-runner -m 0600 /path/to/code \
  /var/lib/our-runner/enrollment-code
sudo -u our-runner /usr/bin/our-runner enroll \
  --state-dir /var/lib/our-runner \
  --code-file /var/lib/our-runner/enrollment-code \
  --url https://control-plane.example.com
sudo rm /var/lib/our-runner/enrollment-code

sudo -u our-runner /usr/bin/our-runner doctor \
  --config /etc/our-runner/config.yaml
sudo systemctl start our-runner
sudo systemctl status our-runner
```

For a manually configured rootless daemon, confirm the service's `DOCKER_HOST`
matches the socket created for the `our-runner` UID. Before enabling restricted
network access, create the egress-enforced network and apply both labels checked
by the runner:

```text
com.ourplatform.egress-enforced=true
com.ourplatform.egress-policy=<configured egress_policy_id>
```

Start from [`config.example.yaml`](config.example.yaml). Configuration parsing
rejects unknown fields, insecure service URLs, mutable image tags, invalid
resource limits, unregistered checks/repositories, and unsafe file permissions.
Production configuration must include every Agent Pack signing key that the
control plane may select for this runner. A missing key intentionally prevents
the job from starting.

## Commands

```text
our-runner enroll --code-file FILE --url URL [--state-dir DIR]
our-runner doctor --config FILE
our-runner doctor --url URL [--state-dir DIR]  # limited enrollment diagnostics
our-runner start --config FILE
our-runner status [--state-dir DIR]
our-runner pause [--state-dir DIR]
our-runner resume [--state-dir DIR]
our-runner register-repository --url URL [--state-dir DIR]
our-runner scan-agent-config [--config REPOSITORY_PATH]
our-runner support-bundle [--state-dir DIR]
our-runner version
```

`mcp-serve` and `hook-event` are internal job commands. Their bearer token is
read from a private file and must never be placed directly on a command line.
Hook telemetry is occurrence-only: event names come from a fixed allowlist and
provider-controlled hook payloads are consumed locally but never forwarded.
Repository registration acquires the runner lock and therefore requires the
daemon to be stopped; normal repository definitions should be managed through
the root-owned configuration file.

## Updates and release integrity

Automatic updates use a separate root-owned, socket-activated service. The
runner may request an update through
`/run/our-runner-updater/control.sock`, but it cannot write `/opt/our-runner`,
`/etc/our-runner`, `/usr/bin`, or the updater's state. The updater receives the
systemd listener on file descriptor 3, checks the signed release policy, stages
both executables in a new immutable version slot, preflights them, drains active
work, and atomically moves `current`. The new runner must become healthy within
the configured deadline. Only after that proof does it atomically promote
`updater-current` to the same slot. Until then, recovery continues executing the
previously proven updater binary, so a broken candidate cannot replace the code
responsible for rolling it back. On failure the updater restores the previous
runner slot without promoting the candidate updater. Interrupted transactions
are recovered from root-only state in `/var/lib/our-runner-updater`.

Updates are deliberately disabled after installation. Provision the required
trusted release keys in `/etc/our-runner/update-trust.json`, review the allowed
origin and channel in `/etc/our-runner/updater.yaml`, then set `enabled: true`.
Both files are root-owned and mode `0600`. Threshold signatures, key validity
and revocation, manifest lifetime and sequence, target OS/architecture, size
limits, content digests, and downgrade protection against the root-owned active
runner slot are checked before activation. This remains correct when
`current` and the older recovery-safe `updater-current` intentionally differ.
Multiple slots are retained so rollback does not depend on downloading an older
release.

After provisioning trust and enabling the updater, start its persistent recovery
process so an interrupted activation or missed health deadline is handled even
when the runner cannot reconnect:

```sh
sudo systemctl restart our-runner-updater.service
sudo systemctl status our-runner-updater.service our-runner-updater.socket
```

The service is enabled at installation for boot-time transaction recovery. With
`enabled: false` it exits cleanly without changing the installed release.

Protocol upgrades that introduce new immutable lease or manifest fields require
the fleet to be drained before upgrading runners. Upgrade runners first, then
enable the matching control-plane dispatch contract. A pre-upgrade active lease
cannot be assigned trustworthy manifest authority after the fact, so the runner
migrates the local schema but fails closed with an operator-facing drain error
instead of recovering that lease under guessed identity.

The updater service has only `CAP_SETUID`, `CAP_SETGID`, and `CAP_KILL` in its
capability bounding set. The first two run candidate preflight checks as the
unprivileged runner account; `CAP_KILL` is used only to terminate that
different-UID preflight process group if its timeout expires. It has no
`CAP_SYS_PTRACE` or ambient capabilities.

### Update promotion deployment contract

`.github/workflows/promote-update.yml` promotes a published release into the
update catalog; it does not run against a draft release. Protect the
`update-production` GitHub environment with required reviewers and configure
these environment variables:

```text
UPDATE_AWS_ROLE_ARN
UPDATE_AWS_REGION
UPDATE_BUCKET
UPDATE_PUBLIC_BASE_URL
UPDATE_KMS_KEY_ID_PRIMARY
UPDATE_SIGNING_KEY_ID_PRIMARY
UPDATE_KMS_KEY_ID_SECONDARY       # optional during key overlap
UPDATE_SIGNING_KEY_ID_SECONDARY   # optional during key overlap
```

`UPDATE_PUBLIC_BASE_URL` must be the HTTPS origin served by the update bucket or
CDN. Catalogs are published at `updates/<channel>/manifest.json`; the default
runner configuration therefore reads
`https://updates.ourplatform.com/updates/stable/manifest.json`. The GitHub OIDC
role should have `kms:Sign` and `kms:Verify` only for the named signing keys, and
S3 get/head/put only under the update prefixes; it must not have delete access.
The bucket and CDN must enforce HTTPS. Promotion must reject a sequence that is
not strictly greater than the currently published sequence, except for a
byte-identical retry of the same promotion. Immutable artifacts and manifests
also accept only byte-identical retries; conflicting content fails closed.

Use asymmetric KMS signing keys with key spec `ECC_NIST_P256`, usage
`SIGN_VERIFY`, and signing algorithm `ECDSA_SHA_256`. Populate each trust
entry's `public_key_b64` from the matching KMS public key, for example with
`aws kms get-public-key --query PublicKey --output text`.
Operators manage each key's validity window and optional `revoked_at` timestamp
in the root-owned trust file. Keep the primary and secondary configured during
a signing-key overlap, and require the matching signature threshold before
retiring the old key.

Linux release archives contain matching `our-runner` and
`our-runner-updater` binaries. The curl installer uses the full service archive;
automatic updates use a separate minimal
`our-runner-update_<version>_linux_<arch>.tar.gz` containing exactly those two
top-level regular binaries, matching the privileged extractor's closed
allowlist. Release archives include SBOMs and checksums;
GoReleaser signs the checksum with
Cosign and publishes `checksums.txt.sigstore.json`. The checksum also covers the
published `install.sh`. The installer requires the bundle identity to match this
repository's tag-triggered `release.yml` workflow. Like every `curl | sh`
bootstrap, the first script is authenticated by the HTTPS installation domain;
the release payload is then cryptographically verified before privileged code
runs. Security-sensitive operators can download `install.sh`, the checksum, and
its Sigstore bundle separately, verify them, and execute the local script.
Package and self-update signing keys should be separated and protected by the
release system. Operators must test rollback in their environment.

## Development and verification

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
```

`doctor --config` validates the identity, Docker/rootless boundary, restricted
network labels when configured, and the exact immutable job image. Its sandbox
probe creates a synthetic provider credential and proves that a real nested
sandbox can use the workspace but cannot read that credential. CI also builds
the image from the committed lockfile and rejects fixed HIGH or CRITICAL image
vulnerabilities.

The mock control plane and development-only direct isolation are documented in
[`docs/TESTING.md`](docs/TESTING.md). A release is not production-approved until
tests, race detection, vet, dependency vulnerability scanning, static security
analysis, release signing, Linux rootless-Docker smoke tests, restricted-egress
tests, upgrade/rollback tests, and disaster-recovery exercises pass in CI.

## Package map

- `api/runner/v1`: versioned wire protocol and job/tool schemas
- `internal/identity`, `internal/enroll`, `internal/daemon/rotate.go`: identity lifecycle
- `internal/transport`, `journal`, `lease`: durable command/event channel and execution authority
- `internal/repo`, `isolation`, `supervisor`: repository and process boundary
- `internal/scanner`, `agentpack`, `provider`: customer agent configuration
- `internal/sourcepublication`: runner-local provider credentials and idempotent review-request publication
- `internal/mcpbridge`, `hookspool`: authoritative MCP and telemetry hooks
- `internal/collector`, `supportbundle`: bounded artifacts and diagnostics
- `internal/updatecatalog`, `internal/updater`, `internal/updateipc`: signed update policy, privileged A/B activation, and capability-poor daemon IPC
- `internal/daemon`: orchestration, reconciliation, fencing, and job lifecycle
