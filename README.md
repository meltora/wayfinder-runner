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

Provider review-feedback content retrieval in protocol v1 is intentionally
limited to exact native GitHub change-request review bodies. GitLab and
Bitbucket reviewer-state events are derived, have no native content identifier,
and carry zero content bytes, so the control plane must never dispatch them for
content continuation. Those providers still support reviewer-state and comment
metadata observation; the runner does not guess a causal comment or fetch
content by matching an actor.

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
runner verifies that network identity and labels match its configured policy
ID. Prompt-only consultation additionally requires a fresh effective-policy
attestation from the root-owned verifier described below.

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

When a build command with `push_branch` carries a control-plane GitHub
installation credential and the repository URL is HTTPS on the runner's single
GitHub Git host, the runner pushes over HTTPS with that credential. The host is
`github.com` unless the GitHub installations set `git_host` (a bare hostname,
for GitHub Enterprise Server). Jobs do not name their installation, so a runner
with mixed github.com and GitHub Enterprise Server installations falls back to
the existing push, with no credential, for every repository. The token reaches only the `git push` process
environment. Git's askpass program is the runner binary itself in a hidden mode,
so nothing is written to disk and a `noexec` state volume is fine; it answers
only the exact username and password prompts for the expected host and stays
silent for any other host. The push resets host credential helpers
(`-c credential.helper=`), pins `core.askPass`, sets
`http.followRedirects=false`, disables terminal prompts, and never puts the
token in argv, the remote URL, `.git/config` or any file. Before pushing, the
runner lists the mirror's effective Git config (local, worktree and included
files, with system and global config disabled exactly as for the push) and
checks every key against an allow-list of the keys `git clone --mirror` writes:
`core.repositoryformatversion`, `core.filemode`, `core.bare`,
`core.logallrefupdates`, `core.ignorecase`, `core.precomposeunicode`,
`core.symlinks`, `remote.origin.url`, `remote.origin.fetch`,
`remote.origin.mirror`, `remote.origin.tagopt`, `extensions.objectformat`,
`extensions.refstorage`, `branch.*.remote` and `branch.*.merge`. Any other key,
such as `url.*.insteadOf`, `http.*`, `include.*`, `includeIf.*`,
`extensions.worktreeConfig` or a `remote.*.proxy`, and any `config.worktree`
file in the mirror, fails the push closed as a build finalization failure that
names the key but not its value.
Trust for a private CA must therefore come from the environment
(`SSL_CERT_FILE`/`SSL_CERT_DIR`, where the host Git build honors them) or the
system trust store, not from mirror config. Git
output is scrubbed before it is surfaced. SSH origins keep the SSH push; the
runner does not rewrite remotes, so HTTPS push requires an HTTPS repository
URL. A push that needs the credential while it has less than one minute left
finishes as a `build_finalization_failed` job failure whose message says the
provider credential was unavailable. The agent process never receives the token.

Known limitations: in direct (unsafe) isolation mode the token is present in the
`git push` process environment, so other processes running as the same user can
read it for the duration of the push. The credential is issued with the
`start_job` command and lives about an hour, so a build whose agent runs past
roughly 55 minutes reaches the push with an unusable credential and finishes
with `build_finalization_failed`; re-issuing the credential at push time is
planned.

## Runner-local source publication

Post-build pull-request publication is a separate fenced runner operation; it
does not run inside the coding agent. For GitHub, the preferred credential is the
ephemeral installation token the control plane attaches to the command
(`provider_credential`): it is held in memory for that command only, is never
written to the journal, logs or events, and must have at least one minute left
when the runner starts the GitHub API calls. The runner checks the numeric repository
id GitHub returns for the repository the command names against the
credential's `repository_ids`; a mismatch is `permission_denied` and a missing
id is `invalid_provider_response`, both before any other call. A GitHub installation may
therefore omit `token_file`; such an entry needs a command credential, and a
command without a usable one reports the retryable `provider_unavailable`
outcome, which the control plane retries under a new command id. A new command whose attached
credential is invalid or expired on arrival is durably rejected
(`invalid_command`); redelivering the same command id replays that rejection,
so it can only be retried under a new command id with a fresh credential. `token_file` remains supported for
GitLab, Bitbucket and as a GitHub operator override (used when no command
credential is present). Bind the control plane's exact installation ID to its
provider API and, where needed, a private credential file on the runner:

```yaml
source_publication_installations:
  installation-1:
    provider: github
    api_base_url: https://api.github.com
  installation-4:
    provider: github
    api_base_url: https://api.github.com
    token_file: /run/secrets/our-runner/github-installation-4
  # GitHub Enterprise Server: set git_host to the Git host for HTTPS pushes.
  # installation-5:
  #   provider: github
  #   api_base_url: https://ghe.example.com/api/v3
  #   git_host: ghe.example.com
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
Bitbucket repository identity uses the canonical provider UUID together with a
separate lowercase `workspace/repo_slug` locator.
The daemon fails startup for an explicitly configured unsafe credential and
rechecks credential readiness before each capability refresh and provider
operation.

The runner advertises source-publication transport v1 and each ready provider
only while a matching local installation is usable. Before creating or reusing
a pull or merge request, it verifies that the configured repository exists and
that the remote head branch still resolves to the exact full commit SHA
authorized by the control plane. Requests use bounded responses and timeouts,
refuse redirects, and normalize provider failures before emitting a durable
result event. A crash after the provider accepts a request is recovered through
create-or-find idempotency; duplicate command delivery cannot create a second
logical result.

The same authenticated installation boundary powers verified reviewer identity
checks and provider reviewer-assignment delivery. Assignment commands carry
stable provider repository and account IDs, immutable identity revisions and
digests, an exact head SHA, and a short fenced lease. Immediately before a
mutation, the runner re-reads the open pull or merge request, head, every stable
account, and effective repository membership. GitLab and Bitbucket replacement
calls union the live reviewer set so unrelated reviewers are preserved; GitHub
uses its additive review-request endpoint. Every attempted mutation is followed
by a live reconciliation read. Response loss, partial application, reviewer
loss, or post-mutation head drift produces terminal `delivery_uncertain`
evidence and is never blindly retried. For GitHub, reviewer identity
verification, reviewer assignment and its reconciliation, review observation and
review feedback content use the command's `provider_credential` exactly as source
publication does, so `token_file` is optional for GitHub for all of these
operations when the control plane supplies credentials, and a GitHub
installation without one advertises them. The credential must have at least one
minute left before every GitHub request; otherwise the operation reports its
retryable `provider_unavailable` outcome without a network call, as does a
token-less GitHub installation whose command carries no credential. Every
provider repository id the command names must be in the credential's
`repository_ids`, or the operation reports `installation_permission_denied`
before any call; the runner then fetches that repository by id, checks the id
GitHub returns, and confines later calls to that repository. `token_file` is
still used for GitLab and Bitbucket, and for GitHub when no credential is
attached. A credential-only GitHub installation therefore reports the retryable
`provider_unavailable` outcome for every review operation until the control
plane's GitHub App is configured and attaches credentials.

## Runner-local preview observation

Preview health is observed by the runner daemon after the exact build completes;
it is not accepted from the coding agent or fetched by the cloud control plane.
Each locally configured profile binds one environment-blueprint ID, version,
and digest to a complete health-check inventory and to customer-owned network
policy:

```yaml
preview_observation_profiles:
  node-web-preview:
    blueprint_id: node-web-app
    version: "2026.08.20"
    digest: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    allowed_hostname_suffixes: [preview.example.com]
    allowed_private_cidrs: [10.40.0.0/16]
    trusted_ca_file: /etc/our-runner/preview-root-ca.pem
    authorization_file: /run/secrets/our-runner/preview-authorization
    max_response_bytes: 65536
    health_checks:
      http-smoke:
        path: /healthz
        expected_status: [200]
        timeout_seconds: 10
```

The authorization file contains the complete fixed `Authorization` header value
and must be a regular, non-symlink file with no group or world permissions. A
custom CA bundle may be group-readable but cannot be group- or world-writable.
Neither value, allowed network ranges, host rules, nor check paths are advertised
to the control plane.

The observer accepts only canonical HTTPS hostnames under an allowed suffix. It
rejects credentials, queries, fragments, explicit ports, IP literals, localhost,
unqualified names, redirects, and suffix-boundary confusion. DNS is resolved
once for the operation; every answer must be allowed, the selected address is
pinned for dialing, and the original hostname remains the HTTP authority and TLS
server name. RFC1918 or ULA targets require an exact configured CIDR, while
loopback and link-local addresses remain prohibited. Connections, TLS, headers,
bodies, and total check time are bounded, and bodies and headers are never
stored.

Only a fully usable profile is advertised as preview-observation transport v1.
The command carries the exact profile identity, reviewer URL, commit set, and
sorted check IDs but no method, path, header, credential, DNS result, or script.
The result is durably journaled before command acknowledgement and contains only
normalized status, HTTP status, and duration measurements or a controlled error
code. An observed failed check is evidence of unhealthy reality; it never
satisfies readiness. Preview observation cannot approve, merge, transition QA,
or mark Done.

## Runner-local preview lifecycle controller

Preview extension and destruction use a separate mutation capability backed by
a customer-operated HTTPS controller. A runner administrator binds each local
controller to one exact environment-blueprint ID, version, and digest:

```yaml
preview_lifecycle_controllers:
  node-web-preview:
    blueprint_id: node-web-app
    version: "2026.08.20"
    digest: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    controller_url: https://preview-controller.example.com/v1/preview-lifecycle
    trusted_ca_file: /etc/our-runner/preview-controller-root-ca.pem
    authorization_file: /run/secrets/our-runner/preview-controller-authorization
    actions: [destroy, extend]
    max_extension_ms: 2592000000
    timeout_seconds: 20
    max_response_bytes: 65536
```

The controller URL, CA root, complete Authorization value, actions, timeout,
and response bound stay local. A control-plane command contains no endpoint,
method, header, credential, or script. Only profiles with currently readable,
permission-safe credential and CA files are advertised. `max_extension_ms` is
mandatory whenever `extend` is advertised, forbidden for destroy-only
profiles, and bounded to 30 days.

The runner sends one bounded `POST` with controller protocol v1, the exact
validated operation, its stable idempotency key, and lifecycle fence. Redirects,
environment proxies, compression, and connection reuse are disabled. TLS,
headers, response bodies, and total time are bounded. The controller must apply
an absolute desired expiry for `extend`, or desired absence for `destroy`, and
return an exact normalized resource state with binding and receipt digests.
Destroying an already absent resource returns successful `already_absent`; it
must not return a generic 404 after mutation.

The controller contract reserves 401/403/404/409/429 for definite pre-mutation
rejections. Every ambiguous post-send condition—including connection loss,
timeout, 5xx, malformed or truncated success, mismatched returned state, and a
crossed operation lease—is recorded as `uncertain`, never as success and never
blindly retried under a new identity. Crash redelivery repeats the same absolute
desired state under the same idempotency identity and fence. The durable result
event is journaled before command acknowledgement. A paused or emergency-stopped
runner rejects both lifecycle actions.

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

The recommended installation path is the release installer. The release build
generates `install.sh` from `packaging/bootstrap.sh` with the repository
identity baked in (`packaging/scripts/prepare-release.sh`, run by GoReleaser)
and includes it in the signed checksum manifest. The dashboard's enrollment
wizard shows the exact command for its configured release: it downloads
`install.sh`, checks it against the SHA-256 recorded in the signed checksum
manifest, and runs it only if the check passes, so no unverified code runs:

```sh
curl --proto '=https' --tlsv1.2 -fsSLo our-runner-install.sh 'https://github.com/OWNER/REPOSITORY/releases/download/v1.2.3/install.sh' \
  && echo '<install.sh SHA-256 from checksums.txt>  our-runner-install.sh' | sha256sum -c - \
  && OUR_RUNNER_VERSION='v1.2.3' sh our-runner-install.sh
```

On macOS (development only) use `shasum -a 256 -c -` instead of
`sha256sum -c -`. The short form below trusts the HTTPS installation domain for
the first script; the release payload is still verified before it runs:

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
network access, create the egress-enforced network and apply the discovery
labels checked by the runner:

```text
com.ourplatform.egress-enforced=true
com.ourplatform.egress-policy=<configured egress_policy_id>
```

These mutable Docker labels are not proof of effective egress enforcement.
Prompt-only work-intake consultation also requires a root-authenticated policy
agent at `egress_policy_verifier_socket` (default
`/run/our-runner-egress/policy.sock`). Immediately before a provider start, the
runner acquires a nonce-bound enforcement lease over that authenticated Unix
connection. The root agent must atomically pin the immutable Docker network ID,
configured policy ID, enforcement/rules identity, and blocked unauthorized
reachability for the full lease lifetime; it must close the connection if that
guarantee is lost. The runner holds the lease until the provider exits and kills
the provider/container if the verifier disconnects, revokes, or expires it.
Provider output is accepted only after the agent acknowledges that enforcement
was held through the runner's nonce-bound release request. Missing, stale,
mismatched, or ineffective leases fail closed.

This requires a separately deployed root policy agent that is the exclusive
authority for the applicable host firewall/proxy rules. No such agent is bundled
with the runner repository; consultation cannot be enabled until one implements
this lease contract and is provisioned at the configured socket.
Kernel-authenticated verifier IPC is Linux-only; consultation must remain
disabled on other operating systems.

Start from [`config.example.yaml`](config.example.yaml). Configuration parsing
rejects unknown fields, insecure service URLs, mutable image tags, invalid
resource limits, unregistered checks/repositories, and unsafe file permissions.
Production configuration must include every Agent Pack signing key that the
control plane may select for this runner. A missing key intentionally prevents
the job from starting.

Signed runtime capabilities probe `provider_binaries` overrides using the same
executable selection as job execution. An empty override uses the adapter name
on `PATH`; a missing explicit executable reports unavailable without falling back.
Authentication readiness is checked separately. Overrides are local configuration
and are not included in the manifest; version output containing the configured or
resolved executable path is omitted. These host checks do not prove executable or
credential availability inside a production job image.

The Claude probe is advertised as both `claude` and the signed Pack/runtime name
`claude-code`, with identical availability, authentication and hook facts; these
compatibility aliases represent one adapter, not additional runner capacity.

### Provider environment

`provider_environment` sets extra environment variables for the agent process of
one provider, for example to run Claude Code under a specific account:

```yaml
provider: claude
provider_environment:
  claude:
    CLAUDE_CONFIG_DIR: ~/.claude-xogito
```

Keys are `codex`, `claude` or `opencode`. Variable names must match
`^[A-Z][A-Z0-9_]{0,63}$`. The runner rejects `PATH`, `HOME`, `USER`, `LOGNAME`,
`SHELL`, `TMPDIR`, any `GIT_*`, `SSH_*` or `PLATFORM_*` name, the source-host
tokens `GH_TOKEN`, `GITHUB_TOKEN`, `GITLAB_TOKEN` and `BITBUCKET_TOKEN`, and the
variables it sets itself (`CLAUDE_CODE_SUBPROCESS_ENV_SCRUB`,
`OPENCODE_CONFIG_CONTENT`, `OPENCODE_CONFIG_DIR`, `OPENCODE_DISABLE_*`). Values
must be non-empty and must not contain NUL or line breaks. A leading `~/` is
expanded to the runner user's home directory when the configuration is loaded.
Values are visible to the agent and every tool it runs, so do not put secrets
in `provider_environment`.

The variables are applied only to agent sessions (planning, build and review
continuation) started with `isolation: direct`, under the direct, herdr or tmux
supervisor. They are never applied to Git, required checks, preview observation,
source publication or other runner processes. They are not applied under
`isolation: docker`, where job homes receive copies of the default provider
credentials, or to prompt-only work-intake consultation, which always runs in
Docker. The tmux supervisor passes them on its command line and the herdr
supervisor writes them to its private launch script, as it already does for the
runner's own session variables.

When `CLAUDE_CONFIG_DIR` is set, the Claude session settings also deny the agent
read and edit access to that directory, as they do for `~/.claude`. Capability
detection runs the provider `--version` probe with the configured variables, and
checks sign-in in `CLAUDE_CONFIG_DIR` (Claude) or `CODEX_HOME` (Codex) when set.
Variable names and values are not included in the capability manifest, and
version output containing a configured value that is path-like or at least 8
characters long is omitted. `our-runner doctor
--config` lists the configured names for each provider and never shows values.

The app's Project runner setup guide can prepare a local `runner-config.json`;
the same strict loader accepts JSON as well as YAML. Supply the enrolled identity's
existing state directory, an approved digest-pinned image and each exact repository
clone URL. Before the first Project configuration, the guide supports repository
discovery without adding Pack trust. Once configured, it binds the recorded runtime
and asks you to acknowledge the exact current signed Pack key.

Save the file with owner-only permissions (`chmod 600 ./runner-config.json`) and
run the displayed diagnostics under the runner service account. The starter keeps
job networking disabled. Live provider execution needs separately provisioned,
operator-approved restricted egress and local credentials. Review and merge these
settings into an existing production configuration; the starter is not a complete
replacement for configured checks, publication credentials or preview controllers.

Full configuration diagnostics fail when the enrolled identity or selected runtime
executable is missing. They report repository registrations and trusted Pack-key
counts without running the provider or inspecting its credentials. These counts
do not prove repository access, provider sign-in, Pack execution, current app
authorization or runtime availability inside the job image. Zero registrations
or trust keys produce warnings, allowing initial discovery setup to be diagnosed.

## Configuration fetched from the dashboard

Instead of writing `config.yaml` by hand, an enrolled runner can fetch its
configuration from the control plane over its mTLS identity:

```sh
our-runner enroll --url https://control-plane.example.com
our-runner configure
our-runner start
```

After a successful exchange, `enroll` asks the control plane for its gateway
address (`GET <url>/v1/runners/discovery`, public and secret-free, with the same
HTTPS, no-redirect and size rules as the exchange) and saves it in
`<state-dir>/gateway-url` (mode 0600). `configure` uses that address unless
`--gateway URL` is given; the flag wins and is not written back. If the control
plane does not offer discovery (an older app, or an invalid answer), enrollment
still succeeds and prints "The gateway address was not provided; pass --gateway
URL to our-runner configure." A state directory without `gateway-url` (enrolled
before discovery, or completed by enrollment recovery, which never contacts the
server) needs `configure --gateway URL`. Relative `--state-dir` values are
resolved to absolute paths by every command.

Without `--code-file`, `enroll` asks for the enrollment code on the terminal:
paste or type it and press Enter. The terminal is switched to raw mode while
you type, each accepted character shows as `•`, Backspace removes the last
one, and non-ASCII input and escape sequences such as arrow keys are ignored.
After Enter the runner prints `Enrollment code received: ••••…` followed by the
last 4 characters so you can check it is the right code; those 4 characters
stay in the terminal scrollback, but the code never appears on the command
line, in shell history or in the process list. The runner checks that the input
looks like an enrollment code (it starts with `ren1.`) before sending it and
allows 3 attempts. Ctrl-C cancels (exit status 130), and an empty entry or
Ctrl-D on an empty line stops with "No enrollment code was entered."; the
terminal is restored on every exit and nothing is written to the state
directory until a code has been accepted. Without a terminal, pass
`--code-file FILE` or `--code-file -` (stdin), which read the code exactly as
before and apply no format check.

The dashboard owns only `isolation`, `allow_unsafe_direct`, `provider`,
`job_image`, `job_network`, `job_memory_mb`, `job_cpus`, `job_pids`, `labels`,
`repositories`, `agent_pack_public_keys` and `source_publication_installations`
(GitHub entries without `token_file`). A delivered document with any other key
is rejected as a whole. The host keeps `control_plane_url` (the gateway
address), `state_dir`, `provider_environment`, `provider_binaries`, the accepted
Agent Pack key fingerprints and the direct-isolation acknowledgement in
`<state-dir>/runner.local.yaml`. The last valid delivered document is cached in
`<state-dir>/runner.remote.json`. Both files are written with mode 0600 only after
the merged configuration passes the same validation as `config.yaml`.

`configure` shows the runner ID, isolation, provider, job image, repository
counts and each Agent Pack key fingerprint, then asks only for local values:
the provider environment variable for the selected provider (`CLAUDE_CONFIG_DIR`
for Claude Code, `CODEX_HOME` for Codex), an optional provider binary path, whether
to trust each new Agent Pack key, and, when the dashboard selected direct
isolation, a typed `direct` confirmation. Keys the host has not accepted are
removed before validation and reported as awaiting acceptance; the runner never
trusts control-plane keys on its own. Without a terminal, pass
`--non-interactive` with `--provider-env NAME=VALUE`, `--provider-binary PATH`,
`--accept-pack-keys` and `--acknowledge-direct` as needed. If the dashboard has
not saved the runner's configuration yet, `configure` waits up to two minutes.
The direct-isolation acknowledgement is stored only while the dashboard delivers
direct isolation: as soon as `configure`, `start` or `doctor` receives a Docker
document it is withdrawn, so a later switch back to direct needs a new
confirmation.

When a value was saved earlier, Enter keeps it and a single `-` clears it: a
cleared binary path means the provider is found on `PATH`, and a cleared
environment entry is removed. Any other answer replaces the value. The binary
prompt shows the saved path and says when it is not an executable file; the
environment prompt only says whether a value is set and never prints it.
`--provider-binary` and `--provider-env` still override without a prompt.

`start` and `doctor --state-dir DIR` fetch the latest document every time. Only
after a network failure or a 5xx answer does `start` fall back to the cached
document, with a warning; an authentication, revocation, not-ready or conflict
answer always stops it. A runner certificate that has expired or is not yet
valid also stops it before any request, because the gateway closes such a
connection without saying why. `start --config FILE` keeps working unchanged and
cannot be combined with `--state-dir`.

## Certificate renewal

Runner certificates are valid for 30 days. The daemon renews its certificate
over its existing mTLS connection (`POST /v1/runners/rotate` on the gateway)
without a new enrollment code. It checks at start and every 15 minutes.

Files in the state directory, all private (`0600`) and written by atomic rename:

- `identity.json`: the key, certificate and CA bundle the runner uses now.
- `pending-identity.json`: the key for the next certificate. It is written
  before the renewal request is sent, so a lost response or a crash resends the
  same key and the control plane returns the same certificate. The certificate
  and CA are added once a valid response arrives.
- `previous-identity.json`: the certificate the runner used before its last
  switch. It is deleted after the gateway first accepts a session with the new
  certificate.

Switch policy: once less than half of the certificate lifetime remains, the
runner obtains the new certificate and switches to it when it holds no active
job lease. With less than 72 hours left it switches even while a lease is
active; that lease ends when the new certificate is activated. A `rotate_cert`
command from the dashboard is acknowledged first, then renews and switches
immediately.

The control plane activates the new certificate when the runner opens its
stream with it; from then on the old certificate is refused. If the gateway
answers that stream upgrade with 401 and the previous certificate is still
valid, the runner logs `certificate rotation was not accepted; continuing with
the previous certificate`, goes back to the previous certificate once, discards
the pending one, and tries again at the next check. A 401 on the previous
certificate is an identity failure as before.

A failed renewal at start is logged, and the daemon keeps running with its
current certificate while that certificate is valid. If the current certificate
has already expired and no renewed certificate is pending, the daemon does not
start (`startup certificate rotation: ...`), and the runner must be enrolled
again.

## Commands

```text
our-runner enroll [--url URL] [--state-dir DIR]                # masked code prompt
our-runner enroll --code-file FILE [--url URL] [--state-dir DIR]
our-runner configure [--gateway URL] [--state-dir DIR] [--non-interactive] [--provider-env NAME=VALUE]
    [--provider-binary PATH] [--accept-pack-keys] [--acknowledge-direct]
our-runner doctor --state-dir DIR               # diagnostics with the fetched configuration
our-runner doctor --config FILE
our-runner doctor --url URL [--state-dir DIR]  # limited enrollment diagnostics
our-runner start [--state-dir DIR]             # fetch the configuration, then start
our-runner start --config FILE
our-runner status [--state-dir DIR]
our-runner pause [--state-dir DIR]
our-runner resume [--state-dir DIR]
our-runner register-repository --url URL [--state-dir DIR]
our-runner scan-agent-config [--config REPOSITORY_PATH]
our-runner support-bundle [--state-dir DIR]
our-runner version                             # version, release channel, default control plane
```

`--url` is optional only for a release build that carries a default control
plane for its channel (see "Release channels"); `--url` always overrides it. A
source build, or a release built without a channel URL, stops with
`enroll requires --url (this build has no default control plane)`.

`mcp-serve` and `hook-event` are internal job commands. Their bearer token is
read from a private file and must never be placed directly on a command line.
Hook telemetry is occurrence-only: event names come from a fixed allowlist and
provider-controlled hook payloads are consumed locally but never forwarded.
Repository registration acquires the runner lock and therefore requires the
daemon to be stopped; normal repository definitions should be managed through
the root-owned configuration file.

### Status and rejected events

`our-runner status` prints the state directory, whether the runner is enrolled
and its runner ID, then reads the local journal without changing it:

```text
unacknowledged events: 3
rejected events: 2
rejected for more than 24 hours (may never be accepted): 1
last rejection: runner_event_rejected at 2026-09-29T11:00:00Z
gateway closed the session: invalid_envelope ×2 (last 2026-09-29T10:30:00Z)
```

- `unacknowledged events` are events the control plane has not yet
  acknowledged.
- `rejected events` are unacknowledged events the gateway answered with an
  error. A rejected event stays in the journal and is sent again on every
  reconnect. The runner cannot tell whether the gateway will ever accept it.
- `rejected for more than 24 hours` counts the rejected events first rejected
  more than a day ago. They are the likely permanent ones. Nothing is deleted
  automatically.
- `last rejection` shows the most recent rejection code, when there is one.
- `gateway closed the session` lines count session-level errors that are not
  tied to one event, such as an envelope the gateway could not parse, for the
  last 7 days.

Before the daemon first creates the journal, `status` prints
`journal: not created yet`. If the journal cannot be read, it prints
`journal: unavailable (<reason>)`. Neither case fails the command.
If `journal.db-wal` exists without `journal.db-shm`, `status` prints
`journal: unavailable (wal index missing)` instead of recreating the index; the
daemon rebuilds it on its next start.

### Standalone repository configuration discovery

The `agent_config_scan` control-plane command scans one existing project's
registered repository without creating a Work, WorkflowRun, job, or agent
process. It accepts a full `refs/heads/...` branch and control-plane identifiers;
it never accepts a repository URL, filesystem path, commit, executable, or
configuration content. The URL comes only from the administrator-managed
`repositories` map. HTTPS and SSH registrations are supported; unsafe local
repository mode does not enable this transport.

A runner advertises `agent_config_scan_transport_version: agent-config-scan-v1`
and a sorted, unique list of at most 256 eligible repository IDs only when Git
is available on Linux or macOS. Other platforms cannot advertise or execute
this command. The control plane must require this capability before dispatch.
The existing protocol version or local `scan-agent-config` CLI is not evidence
that a runner supports remote scans. The CLI remains a local inventory command.

Each attempt creates a disposable shallow bare clone, fetches the exact branch,
resolves its immutable commit, and reads raw Git tree/blob objects. No checkout,
archive attributes, repository hooks, filters, submodules, dependency installers,
or agent configuration are executed. Discovery reports metadata and hashes;
review-required and rejected entries do not grant execution approval.

Scans allow at most 512 catalog entries, 1 MiB per readable metadata file,
16 MiB total metadata reads, 16 MiB of tree output, and 100,000 tree entries.
Oversized metadata files are reported as rejected; other traversal or read
limits fail the whole scan rather than publish a partial inventory. Commands
expire within ten minutes. These are time, output, and parser bounds; they do
not impose a hard disk quota on a remote Git packfile. Operators must retain
adequate disk capacity for their trusted registered repositories.

A scan reserves the runner against jobs and other scans. Pause, emergency stop,
and shutdown cancel its context and Git transport process group. The first
result is durably journaled before command acknowledgment. Exact redelivery
reuses that result; a processing receipt without a result after interruption
finishes as `interrupted` (or `deadline_exceeded` after expiry) without repeating Git. A new explicit request is
required to scan again. Command and result digests bind the request, attempt,
project, repository, branch, lease, fence, and deadline. Repository diagnostics,
credentials, and file contents are excluded from failure results.

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

## Release channels

A release belongs to exactly one channel, chosen by its tag:

| Tag | Channel | GitHub release |
| --- | --- | --- |
| `vX.Y.Z` | `production` | draft, then published by hand; "latest" |
| `vX.Y.Z-staging.N` | `staging` | draft prerelease |
| `vX.Y.Z-develop.N` | `develop` | draft prerelease |

Release builds stamp `internal/daemon.Channel` and
`internal/daemon.DefaultControlPlaneURL` with `-X` from the GoReleaser
environment inputs `OUR_RUNNER_CHANNEL` and `OUR_RUNNER_CONTROL_PLANE_URL`.
`prepare-release.sh` refuses a channel that does not match the tag and a URL
that is not an HTTPS origin. No control-plane URL is committed: the release
workflow reads it from the repository variables
`OUR_RUNNER_CONTROL_PLANE_URL_DEVELOP`, `OUR_RUNNER_CONTROL_PLANE_URL_STAGING`
and `OUR_RUNNER_CONTROL_PLANE_URL_PRODUCTION`. An empty variable builds that
channel without a default, so `enroll` then needs `--url`.

`.github/workflows/release.yml` runs on a `v*` tag or by hand
(`workflow_dispatch`) on a tag ref. It only creates a draft release; publishing
stays a manual step. The repository (`github.repository`) and the GHCR
namespace (`ghcr.io/<repository owner>`) come from the workflow context, so a
repository rename needs no file change. A rename does change the signing identity:
`install.sh` verifies `checksums.txt` against the Cosign certificate identity
`https://github.com/<repository>/.github/workflows/release.yml@refs/tags/<tag>`,
so releases signed under the old repository name fail verification once the
installer points at the new name. This fails closed (nothing is installed);
re-release those versions under the new name instead of weakening the check. With the `publish_job_image` input, a
manual run also builds the job image with `packaging/job-image/build.sh`,
pushes it to `ghcr.io/<owner>/<repository>-job:<tag>`, signs the digest with
Cosign and prints the digest-pinned reference for the dashboard's
`RUNNER_DEFAULT_JOB_IMAGE`.

Build the job image locally (nothing is pushed without `--push`):

```sh
sh packaging/job-image/build.sh our-runner-job:local
```

## Development and verification

```sh
go test ./...
go test -race ./...
go vet ./...
go test -coverpkg=./... -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 ./...
sh -n packaging/bootstrap.sh packaging/install.sh packaging/scripts/preinstall.sh packaging/scripts/postinstall.sh packaging/tests/bootstrap_test.sh packaging/tests/install_test.sh packaging/tests/systemd_test.sh packaging/tests/release_artifacts_test.sh packaging/scripts/prepare-release.sh packaging/tests/prepare_release_test.sh packaging/job-image/build.sh
sh packaging/tests/bootstrap_test.sh
sh packaging/tests/install_test.sh
sh packaging/tests/systemd_test.sh
sh packaging/tests/prepare_release_test.sh
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
