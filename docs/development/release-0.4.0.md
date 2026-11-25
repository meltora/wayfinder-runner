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

- [ ] `TestRealDockerDeployAndDestroy` passes. It shows that, in allow mode, a `CONNECT example.com:443` from the preview network gets `200`, a host that is not allowlisted gets `403`, and the allowlisted host is unreachable without the proxy.
- [ ] `TestRealDockerJobNetworkReachesOnlyIngress` passes. It shows that:
  - [ ] the gateway gets an IPv4 address on the `inhibit_ipv4` preview network, it is visible through `InterfaceAddrs`, and the egress proxy binds on it (deploy reaches `deployed`);
  - [ ] the job container gets `preview ok` from `http://our-preview:8080/index.html`;
  - [ ] a `CONNECT` to `our-preview:3128` from the job gets no HTTP reply, and a plain TCP connect to port 3128 is refused;
  - [ ] the job container has no default route and cannot reach the public internet, the metadata address, the bridge gateway, the job network gateway or the preview app directly;
  - [ ] after `DetachJob` the job network is gone and the preview is unreachable.
- [ ] Docker's wording for a missing network, container and endpoint matches the matchers in `internal/previewhost/docker_cli.go` (record the daemon version).
- [ ] `docker network inspect --format '{{json .Containers}}'` on a network holding an orphaned endpoint shows the `ep-` key with a `Name`, and `docker network disconnect -f <network> <endpoint name>` removes it so that `docker network rm` succeeds. If an orphan cannot be produced on the test host, record that and the daemon version instead.

### 3a. Local end to end (blocks the tag; requires the user's approval in the runner tab, together with the real-Docker gate)

Run locally against an isolated app at `a352c671f` or later, backed by a disposable database seeded with the e2e seed (never the dev database), and a runner built from `main` under Docker isolation with the restricted job network. This is the only check that shows Claude's inner sandbox proxy allows a private address, so the release depends on it.

- [ ] A Claude build job calls `platform_deploy_preview` and gets `deployed`, and its inner sandbox proxy reaches `our-preview` on the private address at `:8080`.
- [ ] The app accepts `run_network_policy.agent_sandbox_network` = `preview_only` for that job.
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
