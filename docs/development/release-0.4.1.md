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

## Known follow-ups (not blocking)

- Fleet-level control of preview hosting from the dashboard would need a new remote key and an app slice. It is planned only if that control is ever needed.
