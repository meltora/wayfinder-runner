# Release 0.4.0: preview tunnel and job-requested preview deploys

Draft release notes and tag checklist for the first runner release after v0.3.1 that carries P5e and P5f. The version number is proposed; the release is cut only after every gate below is checked.

## Compatibility

- **Control plane floor:** the Meltora app must be at commit `a352c671f` or later before runners on this release connect to it. That is the app side of the preview tunnel v1 and job deploy v1 contracts, including the replay handling agreed for this release.
- **Host:** git 2.29 or later for preview deploys; `our-runner doctor` reports it.
- **No new configuration keys** and no state migration. `preview_hosting` and the tunnel are advertised only when the runner already hosts previews.

## Release notes

### Preview tunnel v1 (P5e)

- The runner serves its hosted previews to the Meltora edge over a single outbound websocket tunnel, so a preview host needs no inbound port. The frame protocol is frozen in [preview-tunnel-v1.md](preview-tunnel-v1.md).
- The session enforces flow control, stream and buffer limits, and closes with explicit close codes; protocol errors and overflow are logged as warnings.
- The tunnel starts only after the runner manifest is sent, and it is advertised as `preview_tunnel_transport_version: 1`.

### Job-requested preview deploys (P5f)

- Build jobs get the `platform_deploy_preview` tool. The agent asks for a preview of its own commits; the runner resolves and records the commit set, sends `preview_deploy_requested`, and waits for the app's `preview_deploy` command. The contract is frozen in [preview-job-deploy-v1.md](preview-job-deploy-v1.md).
- At most 6 requests per job, one open at a time, a 2-minute command timeout, and local refusal codes when the runner cannot serve the request.
- The agent reaches its own preview at `http://our-preview:8080/` over a per-job internal Docker network. The gateway's egress proxy binds only on the preview network, so a job is refused on port 3128 by the kernel. Codex jobs never get reach (`job_network_unsupported`). See [preview-job-network.md](../preview-job-network.md).
- At the end of a build job that recorded a review-ready intent, the runner publishes the final `preview` evidence for the final commit set, redeploying first when the latest preview is stale or expires within 5 minutes. The final redeploy wait is capped at 5 minutes and never blocks review-ready.

### Reliability fixes

- Journaled events replay byte for byte, including null payloads.
- Lease renewal, lease validity and preview deploy expiry decisions use one injectable clock; production behaviour is unchanged.
- Job network removal force-disconnects containers and orphaned `ep-` endpoints before `network rm`, and refuses an orphan name shadowed by a live container.
- Cancelling a lease recovered after a restart no longer keeps the lease held when its job network cannot be removed; the network is logged and removed by the next startup sweep.
- An invalid preview hosting capability is dropped before the manifest is signed.

## Tag checklist

### 1. Code

- [ ] `origin/main` contains `ceb3ac9` or later, and every P5e/P5f follow-up the master accepted for this release is merged.
- [ ] `sh scripts/check.sh format vet test` exits 0 on the release commit.
- [ ] `GOOS=darwin go vet ./...` passes.
- [ ] `go test -race -count=1 ./internal/daemon/ ./internal/previewhost/ ./internal/lease/ ./internal/previewgateway/` passes.

### 2. Real-Docker gate (requires the user's approval in the runner tab)

Run on a host with Docker, from the release commit:

```sh
RUN_PREVIEW_HOST_DOCKER=1 go test -count=1 -v -run 'TestRealDocker' ./internal/previewhost/
```

- [x] `TestRealDockerDeployAndDestroy` passes. It shows that, in allow mode, a `CONNECT example.com:443` from the preview network gets `200`, a host that is not allowlisted gets `403`, and the allowlisted host is unreachable without the proxy.
- [x] `TestRealDockerJobNetworkReachesOnlyIngress` passes. It shows that:
  - [x] the gateway gets an IPv4 address on the `inhibit_ipv4` preview network, it is visible through `InterfaceAddrs`, and the egress proxy binds on it (deploy reaches `deployed`);
  - [x] the job container gets `preview ok` from `http://our-preview:8080/index.html`;
  - [x] a `CONNECT` to `our-preview:3128` from the job gets no HTTP reply, and a plain TCP connect to port 3128 is refused;
  - [x] the job container has no default route and cannot reach the public internet, the metadata address, the bridge gateway, the job network gateway or the preview app directly;
  - [x] after `DetachJob` the job network is gone and the preview is unreachable.
- [x] Docker's wording for a missing network, container and endpoint matches the matchers in `internal/previewhost/docker_cli.go` (record the daemon version).
- [x] `docker network inspect --format '{{json .Containers}}'` on a network holding an orphaned endpoint shows the `ep-` key with a `Name`, and `docker network disconnect -f <network> <endpoint name>` removes it so that `docker network rm` succeeds. If an orphan cannot be produced on the test host, record that and the daemon version instead.

#### Gate 2 record (2026-11-25)

- **Host:** Docker 29.6.1 (client and server), API 1.55, `overlay2`, rootful default context, AppArmor and seccomp.
- **Commit:** runner `99e3390`. Both tests passed on two consecutive runs, and no `our-preview*` container or network was left behind.
- **First run:** the allowlisted `CONNECT` got `502`. Busybox `nc` half-closed the connection as soon as `printf` ended. The gateway's HTTP server then cancelled the request context, and the DNS lookup failed with "operation was canceled". `99e3390` keeps the probe's input open in both tests, `(printf "$0"; sleep 5) | nc …`. Real HTTPS clients do not half-close before the `200`.
- **Docker wording:**

  | Operation | Daemon message | Matcher |
  |---|---|---|
  | `network inspect`, `network disconnect`, `network rm` on a missing network | `network <name> not found` | `networkMissing` |
  | `network disconnect --force` of a missing endpoint or container name | `endpoint <name> not found` | `endpointMissing` |
  | `network disconnect` without `--force` of a missing container, `rm -f -v` of a missing container | `No such container: <name>` | `containerMissing` |
  | `network disconnect` of a live container that is not on the network, with or without `--force` | `container <id> is not connected to network <name>` | `containerNotConnected` |
  | `volume rm -f` of a missing volume | none; exit status 0 | not needed |

- **Orphan endpoint:** an orphan was not produced on Docker 29.6.1; producing one needs an unclean daemon stop. A normal endpoint in `{{json .Containers}}` has the keys `EndpointID`, `IPv4Address`, `IPv6Address`, `MacAddress` and `Name`, which matches the parser's `map[string]struct{ Name string }`.

### 3a. Local gates (block the tag; approved by the user together with the real-Docker gate)

The app at `a352c671f` does not yet send `preview_deploy` for a job request, attach J6 evidence or offer reach through the tunnel condition, so the app-driven flow moved to 3c. 3a checks the two parts that the release itself depends on.

- [x] Claude's inner sandbox proxy reaches `our-preview` on the private address at `:8080`.
- [x] The app accepts `run_network_policy.agent_sandbox_network` = `preview_only`.

#### 3a record (2026-11-25)

- **Sandbox reach:**
  - **Setup:**
    - Rootless Docker 29.6.1, runner `611eaa7`, job image `local/our-runner-job:e2e-611eaa7`.
    - Claude Code 2.1.299. Its sandbox runtime is bundled inside `claude.exe` and does not report a version; the standalone `@anthropic-ai/sandbox-runtime` in the image is 0.0.69 and was not used.
    - A real preview was deployed with `previewhost`. A job container was started with the runner's nested-sandbox flags on an internal base network, then attached with `AttachJob`.
    - The real `claude` binary ran against an offline Messages API stub. The stub returned one Bash call.
    - The settings came from `provider.Claude.BuildSpec` with `HardenedRuntime`, `DockerIsolation` and `PreviewReach`: `strictAllowlist: true`, `allowedDomains: ["our-preview"]`, `enableWeakerNestedSandbox: true`.
  - **Result**, from inside the Bash sandbox, through the sandbox proxy and with its proxy credentials:
    - `http://our-preview:8080/index.html` returned `200` with `preview ok`. `our-preview` resolved to `172.22.0.2`.
    - `http://example.com/` returned `403` "Connection blocked by network allowlist", reported as a sandbox violation.
    - A direct TCP connect to the preview address failed with `ENETUNREACH`.
  - Passed on two consecutive runs. No `our-preview*` container or network was left behind.
  - On rootful Docker, bubblewrap fails with "Failed to make / slave", which is why the runner requires rootless Docker for nested sandboxes.
- **`preview_only` acceptance:**
  - Run on app `a352c671f` with `pnpm test` on an isolated `vitest_*` database: 4 files, 53 tests passed.
    - `agent-run-guardrails.runtime-login.integration.test.ts`: a `docker`/`restricted` policy with `preview_only` is stored under the runtime login and read back by lease and by agent run. `preview_only` without the restricted job network is rejected by `agent_run_network_policy_preview_only_shape_check`.
    - `get-agent-run-guardrails.tool.integration.test.ts`
    - `accept-run-network-policy.service.test.ts`: the runner event with `preview_only` is accepted.
    - `run-network-policy-event.schema.test.ts`

### 3c. Full P5f app wiring end to end (after the release, once the app P5f slice lands)

Not a tag gate. Until then the app never sends `preview_deploy` for a job request, so the runner's job deploy path stays dormant in production.

- [ ] Run against an isolated app with the J2 policy (`preview_deploy_requested`), the J6 evidence and the tunnel condition in `isPreviewReachOffered` wired, on a disposable database (never the dev database), with a runner built from `main` under rootless Docker isolation.
- [ ] A Claude build job calls `platform_deploy_preview` and gets `deployed`, then reaches `our-preview` from inside its sandbox.
- [ ] The job's final `preview` evidence is accepted and `create_preview` closes through the built-in observation.

### 3b. Edge through the tunnel (after the release, before hosted previews are enabled in production)

There is no staging environment. This needs the preview domain, its DNS, the certificate and the keys.

- [ ] A deployed preview is served at its edge URL through the tunnel.

### 4. Cut

- [ ] Bump `var Version` in `internal/daemon/daemon.go` and `packaging/release-version` to `0.4.0` in one commit.
- [ ] The master tags `v0.4.0` on that commit and pushes the tag; the release workflow's preflight and GoReleaser succeed.
- [ ] The release notes above go into the GitHub release.

## Known follow-ups (not blocking)

- Readiness with the userland proxy can report about 50 ms early.
- Job start and startup recovery still read the wall clock rather than the injected lease clock.
- A job network leaked by a failed recovery detach waits for the next restart, since the sweep is startup-only.
- A client that half-closes its connection before the gateway answers `CONNECT` gets `502`, because the request context is cancelled during the DNS lookup or dial. No known HTTPS client does this.
- The 3a sandbox reach check was a one-off spike and is not in the repository; a gated integration test (rootless Docker, job image, offline Messages API) could keep it from regressing on Claude Code upgrades.
- The orphaned `ep-` endpoint path (`disconnect -f` by endpoint name) is covered by unit tests only, since no orphan could be produced on the gate host.
