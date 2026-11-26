# Release 0.4.1: preview hosting for dashboard-configured runners

Draft release notes and tag checklist for the patch release after v0.4.0.

## Compatibility

- **Control plane:** no app change. The delivered configuration document is unchanged, and `preview_hosting` stays a key the dashboard does not own: a document carrying it is still rejected as a whole.
- **Configuration:** one new optional key, `preview_hosting`, in `<state-dir>/runner.local.yaml`. A missing key means preview hosting is off, which matches what 0.4.0 did. `config.yaml` for `start --config FILE` is unchanged.
- **No state migration.**

## Release notes

### Preview hosting on dashboard-configured runners

- On 0.4.0, a runner set up with `configure` and started with `start --state-dir` never hosted previews, because the merged configuration always left `preview_hosting.enabled` false. Preview hosting now comes from `runner.local.yaml`.
- Preview hosting is a host resource decision (Docker on this host, memory budget, concurrency), so it stays host-owned and the dashboard cannot turn it on.
- It fails closed:
  - a missing block means off;
  - invalid limits or an unknown key make `configure` save nothing and make `start` and `doctor` refuse the configuration;
  - preview hosting together with direct isolation from the dashboard is refused with "preview_hosting requires docker isolation".
- `configure --preview-hosting on|off` turns it on or off. Without the flag, the saved setting is kept. `runner.local.yaml` is written atomically with mode 0600.
- `our-runner status` shows `preview hosting: on (at most N previews at once)`, or `off` or `unknown` with the reason.
- `our-runner doctor` reports a `preview-hosting` check: `OK` when enabled, `SKIP` with the reason when disabled.

### Valid `platform_deploy_preview` schema

- On 0.4.0, the MCP bridge served the `platform_deploy_preview` input schema with `"required": null`, because cloning the tool schema turned an empty string list into nil. JSON Schema requires `required` to be an array, so a client could reject or ignore the tool. The clone now keeps an empty list as `[]`.
- A test checks every served tool schema, from both `ToolDefinitions` and `ToolDefinitionsForNames`: no keyword is null, `required` is an array of unique declared properties, and nested `properties`, `items`, `enum` and `additionalProperties` are well formed. The check is structural, written with the standard library, and is not a full JSON Schema metaschema validation.

### Clear error for a state directory too long for the job socket

- Under Docker isolation each job's MCP socket lives at `<state-dir>/sockets/job-<random>/mcp.sock` and is mounted into the container at the same path. A Unix socket path is limited to 108 bytes on Linux and 104 on macOS, so on 0.4.0 a long `state_dir` made every job fail with `workspace_preparation_failed` ("start MCP bridge"), and the log did not say why.
- `our-runner start` now refuses such a configuration before it connects: `state_dir is too long for docker isolation: the job MCP socket path can reach N bytes, over the 103-byte unix socket limit; use a state_dir of at most M bytes`. The 103-byte limit fits both platforms, and the longest temporary directory name is counted, so any `state_dir` of up to 71 bytes works.
- `our-runner doctor` reports the same as a failing `job-socket-path` check, and `OK` with the longest socket path otherwise. Direct isolation is unchanged: it already places long socket paths in the temporary directory.

### Shell commands work in Docker jobs

- On 0.4.0 and every earlier release, every Bash command of a `claude` job under Docker isolation failed before it ran, with `bwrap: Can't mkdir <job>/pack/.claude: Read-only file system`. A build agent could edit files but could not run tests, commit, start a server or deploy a preview, so 0.4.1 is the first release where Docker builds can actually work.
- **Cause:** the runner passed the Agent Pack directory to Claude Code as `--add-dir`, which makes it a sandbox write root. Claude Code protects `.claude` paths inside every write root by mounting placeholders over them. The pack is mounted read-only and its directories are mode 0555, so Claude Code took the missing `pack/.claude` for a path it had to cover, and bwrap could not create the mount point.
- The pack is now readable through a `Read(//<pack>/**)` permission rule in the job settings and is no longer a write root. Nothing about the pack's own protection changes: it stays a read-only mount.
- Claude Code also covers missing dotfiles in the job's working directory (`.bashrc`, `.gitconfig`, `.mcp.json`, `.vscode`, `.claude/…` and others) with `/dev/null` placeholders while a command runs, and `git add -A` refused them (`can only add regular files, symbolic links or git-directories`). The agent's `~/.gitconfig` now sets `core.excludesFile` to a runner-written list of those root paths, so `git add -A` skips them. Files the repository already tracks are unaffected.

### Claude Code 2.1.326 in the job image

- The job image pins Claude Code 2.1.326, the npm `stable` release on 26 November 2026. On 2.1.299, accounts whose default model is `claude-fable-5-2` logged `unrecognized_model`.
- Build and plan jobs do not pass `--model`, so they use the account's default model.

### Turning preview hosting on

On a runner already enrolled and configured from the dashboard, with Docker isolation and git 2.29 or later:

```sh
our-runner configure --preview-hosting on
our-runner status
our-runner doctor --state-dir <state-dir>
```

Then restart the runner. Limits can be set under the same key in `runner.local.yaml`, with the bounds `config.yaml` uses:

```yaml
preview_hosting:
    enabled: true
    max_concurrent: 2
    memory_budget_mb: 8192
```

The runner offers preview hosting to the app only after the preview tunnel is connected. To turn it off, run `our-runner configure --preview-hosting off` and restart.

## Tag checklist

### 1. Code

- [ ] `origin/main` contains the 0.4.1 fix commit.
- [ ] `sh scripts/check.sh format vet test` exits 0 on the release commit.
- [ ] `GOOS=darwin go vet ./...` passes.
- [ ] `goreleaser check` passes.

### 2. Manual check

- [ ] On a scratch state directory enrolled against an isolated app, `configure --preview-hosting on` followed by `start` advertises `preview_hosting` in the runner manifest, and `status` and `doctor` report it as on.

### 3. Cut

- [ ] Bump `var Version` in `internal/daemon/daemon.go` and `packaging/release-version` to `0.4.1` in one commit.
- [ ] The master tags `v0.4.1` on that commit and pushes the tag; the release workflow's preflight and GoReleaser succeed.
- [ ] The release notes above go into the GitHub release.

### 4. Job image

The MCP bridge that serves the tool schemas runs inside the job container, from the `our-runner` binary that `packaging/job-image/Dockerfile` copies to `/usr/local/bin/our-runner`. The `platform_deploy_preview` schema fix therefore reaches jobs only through a job image built from the release commit; a runner on 0.4.1 with the 0.4.0 job image still serves `"required": null`.

- [ ] The release workflow's "Build, push, sign and verify the job image" step builds the image on the `v0.4.1` tag with `sh packaging/job-image/build.sh ghcr.io/meltora/wayfinder-runner-job:v0.4.1 --push`. It pushes to GHCR with the workflow's `GITHUB_TOKEN`, signs the digest with Cosign and verifies the signature against the release workflow identity. No manual build or push is needed.
- [ ] Take the digest-pinned reference from the release asset `job-image.txt`, which is covered by `checksums.txt` and its Cosign bundle, or from the workflow step summary. It has the form `ghcr.io/meltora/wayfinder-runner-job@sha256:<digest>`. The v0.4.0 reference was `ghcr.io/meltora/wayfinder-runner-job@sha256:51d3a7519af2711febc15360222eff6851d3137c9eb37d897e03578b110a851e`.
- [ ] Verify it before use: `cosign verify <reference> --certificate-identity https://github.com/meltora/wayfinder-runner/.github/workflows/release.yml@refs/tags/v0.4.1 --certificate-oidc-issuer https://token.actions.githubusercontent.com`.
- [ ] Set the app's `RUNNER_DEFAULT_JOB_IMAGE` to that reference. It only prefills the job image field in the runner configuration form and the enrollment wizard; it is not delivered to runners by itself.
- [ ] For every existing dashboard-configured runner, save its configuration profile with the new job image. The delivered `job_image` comes from the stored profile (`profile.jobImage`), so a runner keeps the old image until its profile is saved. Runners started with `start --config FILE` need `job_image` changed in that file and a restart.
- [ ] After the switch, a new job's `tools/list` serves `platform_deploy_preview` with `"required": []`.

### 5. App environment switch

Production last pointed at v0.3.1 and never moved to 0.4.0, so 0.4.1 is the first switch since 0.3.1. Change the four values together, after the release is published and verified:

1. Download `checksums.txt`, `checksums.txt.sigstore.json`, `install.sh` and `job-image.txt` from the v0.4.1 release and verify them:

   ```sh
   cosign verify-blob checksums.txt \
     --bundle checksums.txt.sigstore.json \
     --certificate-identity "https://github.com/meltora/wayfinder-runner/.github/workflows/release.yml@refs/tags/v0.4.1" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   sha256sum --check --ignore-missing checksums.txt
   ```

2. Set:

   | Variable | Value |
   |---|---|
   | `RUNNER_RELEASE_VERSION` | `v0.4.1` |
   | `RUNNER_RELEASE_INSTALL_SCRIPT_URL` | `https://github.com/meltora/wayfinder-runner/releases/download/v0.4.1/install.sh` |
   | `RUNNER_RELEASE_INSTALL_SCRIPT_SHA256` | the `install.sh` line of the verified `checksums.txt`, which equals `sha256sum install.sh` (lowercase hex) |
   | `RUNNER_DEFAULT_JOB_IMAGE` | the content of the verified `job-image.txt`, after `cosign verify` of that reference (section 4) |

3. Leave `RUNNER_RELEASE_DEFAULT_CONTROL_PLANE_URL` as it is, unless it differs from the URL stamped into the release. `our-runner version` on the v0.4.1 binary prints it; on v0.4.0 it was `https://app.meltora.dev`.

Other app-side requirements from 0.3.1 to 0.4.x:

- The production app must be at `a352c671f` or later, the control plane floor from [release-0.4.0.md](release-0.4.0.md) for the preview tunnel v1 and job deploy v1 contracts.
- The active platform Agent Pack's `minimumRunnerVersion` (currently `0.3.1`) is already met by 0.4.1. No pack change is required.
- Runners already installed on 0.3.1 keep running. The dashboard shows them the upgrade notice once `RUNNER_RELEASE_VERSION` is v0.4.1. They get the new job image only after their configuration profile is saved with it (section 4).
- Hosted previews stay off until an operator runs `configure --preview-hosting on`, and they are reachable through the edge only after 3b (preview domain, DNS, certificate and keys).

## Known issues

### Subscription login works for one job, then "OAuth session expired"

- **Affected:** a runner with Docker isolation and the `claude` provider signed in with a Claude subscription (OAuth in `.credentials.json`). The issue is older than 0.4.1 and 0.4.0. API key sign-in is not affected.
- **Symptom:** once the access token in the host's `.credentials.json` has expired, one job still runs, and every later job fails within seconds with `agent_exit_failed` and `provider_error="Failed to authenticate: OAuth session expired and could not be refreshed"`.
- **Cause:** each job gets a copy of the host login in its job home. The first job refreshes the expired access token in that copy, and the sign-in server rotates the refresh token, which invalidates the one on the host. The runner does not write the refreshed copy back and deletes the job home when the job ends. Every later job starts from the host's invalidated refresh token.
- **Workaround:** sign in again on the host (`claude`, then `/login`, with the same `CLAUDE_CONFIG_DIR` the runner uses). No runner restart is needed, because every job copies the host login when it starts. Jobs keep working while the new access token is valid, which is a few hours, because they do not need to refresh it. The fix is tracked as RUNNER-OAUTH-WRITEBACK for 0.4.2.

## Known follow-ups (not blocking)

- RUNNER-OAUTH-WRITEBACK (0.4.2): write the refreshed login back to the host atomically under a lock, or support a long-lived token, so subscription sign-in survives token refresh.
- The runner logs only "start MCP bridge" when the job MCP bridge fails to start. It should log the underlying error.
- Fleet-level control of preview hosting from the dashboard would need a new remote key and an app slice. It is planned only if that control is ever needed.
