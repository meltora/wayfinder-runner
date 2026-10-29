# Runner quality gates (FR-12)

## Control

| Field | Value |
| --- | --- |
| ID | FR-12 `runner-quality-gates` |
| Kind | slice (hardening and gates) |
| Status | done (verified 29 October 2026, re-review after repair attempt 1) |
| Risk | medium. The fixes touch filesystem traversal of agent-writable trees, process-group cancellation, private-file handling and the CI gate itself. No protocol, persistence or authorization change. |
| Complexity | standard. Eleven small, independent code fixes, two deterministic test repairs, one production cancellation fix, three FR-08 follow-ups, a gate script and CI wiring. Every root cause is already reproduced. |
| Parent scope | App `docs/implementation-progress.md`, "First release slice plan (28 October 2026)", FR-12. Also FR-08 non-blocking follow-ups N1 to N3 (`docs/development/runner-build-prompt-size-bound.md`, "Review findings and repair"). |
| Product sources | App `AGENTS.md` (typecheck, lint, tests, coverage of 80% or more, and a security review before shipping). App `PRODUCT.md` "Supply chain". FR-12 acceptance: no unjustified gosec finding, `go test -race -count=20` passes for the three flaky tests, a harness runs the planning driver stub package, gofmt is clean, and `security.yml` runs gosec and govulncheck. |
| Planner | Opus 5.5 (`claude-opus-5-5`), develop-planner, 29 October 2026 |
| Implementer | Opus 5.5 (`claude-opus-5-5`), develop-implementer, 29 October 2026 |
| Reviewer | Opus 5.5 (`claude-opus-5-5`), fresh develop-reviewer, 29 October 2026; re-review after repair 1 by a second fresh Opus 5.5 develop-reviewer, 29 October 2026 |
| Repair attempt | 1 (F-1, option (a): `internal/updater` added to owned paths) |
| Updated | 29 October 2026 |
| Baseline | Runner worktree `wt/runner-fr-12`, branch `slice/fr-12-runner-quality-gates`, commit `1b31c61` (includes FR-08). Clean apart from this record. App repository read only. |
| Toolchain | `GOTOOLCHAIN=go1.26.10` (`go.mod` pins `go 1.26.10`). gosec v2.29.0 and govulncheck v1.8.0, as already pinned in `.github/workflows/security.yml`. |

### Environment note (29 October 2026)

The root filesystem was 100% full (under 100 MB free) during planning. Go builds failed with `ENOSPC` in `/tmp`. Every measurement below was taken with the build and test scratch space moved to tmpfs:

```sh
mkdir -p /dev/shm/fr12/tmp /dev/shm/fr12/cache
export GOTOOLCHAIN=go1.26.10 GOTMPDIR=/dev/shm/fr12/tmp TMPDIR=/dev/shm/fr12/tmp GOCACHE=/dev/shm/fr12/cache
```

If the disk is still full, the implementer and reviewer use the same exports. Nothing outside the scratchpad and `/dev/shm/fr12` was deleted or changed.

Implementation update (29 October 2026): the disk was freed (61 GB free on `/`), so the implementer ran without the tmpfs exports. At the coordinator's request, `overlay.json`, `load.sh` and the gosec baseline were copied to `scratchpad/fr12/`, and `/dev/shm/fr12` was deleted. Reviewers use `scratchpad/fr12/load.sh` and `scratchpad/fr12/overlay-run.sh`.

## Outcome and current gap

**Outcome:** the runner repository passes one enforced gate without inherited exceptions. That gate covers format, vet, tests under the race detector, govulncheck and gosec. The three known flaky tests are deterministic. The app's planning driver unit tests run against the runner.

What exists at `1b31c61`, measured on 29 October 2026:

- **gosec.**
  - Both v2.28.0 (`~/go/bin/gosec`) and v2.29.0 report the same 11 findings. There are 361 files and 50 existing `#nosec` annotations.
  - Evidence: `/tmp/claude-1000/-home-omar-Code-Automated-Development-app/30c6447b-7fdc-4b3f-8101-5e8a52ec03f5/scratchpad/fr12-gosec-baseline.json` and `fr12-gosec-v2290.json` in the same directory.
  - CI hides all 11 through a path-scoped `-exclude-rules=...` list in `.github/workflows/security.yml`, job `gosec`. That list is the "inherited exception" this slice removes.
- **Flaky tests.** All three were reproduced under CPU load (`/dev/shm/fr12/load.sh N <test binary> ...`, which starts N busy-loop shells):
  - `TestScanGitCancellationKillsChildrenAfterLeaderExits` failed 1 of 30 runs under 24 busy loops with `-race`: `scan_snapshot_test.go:271: transport child survived after Git leader exited`. The failed run took 2.11 s, which shows that `scanGit` returned only after `WaitDelay` (1 s) and not on the 100 ms cancellation.
  - `TestAgentProcessNeverReceivesProviderCredential` and `TestNonPushingJobAcceptsRepositoryReadCredential` each failed once in 15 runs under 16 busy loops with `-race`. Example: `provider_credential_integration_test.go:176: open .../job-results/job-1373290822/repo-1/repo.git/objects/a1/ed24...: no such file or directory`.
- **Gate tooling.**
  - The runner repository has no Makefile, no check script and no Git hooks (`core.hooksPath` is unset, and the common `.git/hooks` holds only samples).
  - Checks are a manual list in `docs/TESTING.md` "Automated gates" and in `README.md` "Development and verification". Those lists pin gosec v2.28.0 and govulncheck v1.6.0, while CI pins v2.29.0 and v1.8.0.
  - `.github/workflows/` contains only `security.yml` (govulncheck and gosec) and `release.yml` (packaging tests on tags). No workflow runs `gofmt`, `go vet` or `go test`.
- **Current state of the other checks.**
  - `gofmt -l .` lists no files in the runner.
  - `go vet ./...` passes.
  - govulncheck v1.6.0 reports 0 vulnerabilities affecting the code, and 1 in a required module that the code does not call.
- **App planning driver.**
  - `app/src/server/planning-manifests/test-support/go-planning-driver_test.go` (`package main`, 12 tests) imports `internal/collector`, `internal/journal` and `api/runner/v1`. It therefore only compiles inside the runner module.
  - `run-go-planning-driver.ts` copies the seven non-test `go-*.go` files into `../app-runner/cmd/planning-interop-<uuid>` and runs `go build`. No harness copies or runs the `_test.go` file.
  - Four of the app files are not gofmt-clean: `go-build-provider.go`, `go-planning-driver.go`, `go-planning-fixture.go` and `go-planning-provider.go`.
  - The planner compiled and ran the 12 tests against `1b31c61` without writing into the runner tree. The method used `go test -vet=off -overlay <json> -c -o <tmp>/driver.test ./cmd/planning-interop-probe/` and then ran the binary from a temporary directory. All 12 passed.
  - Running `go test -overlay` directly fails, because the test binary's working directory (the virtual package directory) does not exist.
- **FR-08 follow-ups.**
  - `internal/supervisor` coverage is 75.5%. The gaps are `HerdrSupervisor.Start` (42.9%), `findPane` (55.6%), `awaitOutput` (50.0%), `createHerdrOutputFIFO` (62.5%), `prepareHerdrOutput` (70.0%) and `NewTmux` (37.5%).
  - `sweepAgentPromptFiles` and `agentPromptFile.Remove` (`internal/daemon/agent_prompt_file.go`) use the path-based `os.ReadDir` and `os.Remove`.
  - The tmux and Herdr supervisors apply `stdinRedirect` (`internal/supervisor/stdin_file.go`) without the `O_NOFOLLOW` and regular-file checks that `openStdinFile` gives `DirectProcess`.

## Scope

**In scope (runner repository only):**

- Fix all 11 gosec findings, with at most one justified `#nosec` (see D1).
- Deterministic fixes for the three flaky tests. One of them needs a production cancellation fix in `internal/repo`.
- A gate script, CI wiring that has no exclusions, and documentation.
- FR-08 N1 to N3.
- Verifying, from the runner side, that the app planning driver tests compile and pass at the FR-12 head.

**Excluded:**

- Any edit in the app repository. That includes the planning driver harness and gofmt of the four app files, which are recorded as the app-side dependency **FR-12-APP** below.
- The live runner (`app-runner`), its containers, the dev database and the stack.
- Per-package coverage of 80% outside `internal/supervisor`.
- The packaging shell tests (already run in `release.yml`).
- Other `exec` call sites that might share the scan cancellation pattern (see follow-ups).
- A Git pre-push hook, because the repository has no hook convention (see D4).

**Parent requirements left open after this slice:**

- FR-12 acceptance "A harness or CI step compiles and runs the stub package" is met only once FR-12-APP lands.
- "gofmt is clean" holds for the runner after this slice, and for the app's Go test-support files only after FR-12-APP.

**Dependencies:** none for the runner work. Other slices may edit `internal/daemon` in parallel, so preserve their edits and rebase if needed.

## Acceptance contract

UI, prototype, browser and accessibility dimensions do not apply: there is no user-facing surface. Permission dimensions apply only as filesystem-ownership invariants (REQ-02 to REQ-04, REQ-11 and REQ-12).

| ID | Product source | Required behavior and observable acceptance scenario | Verification method |
| --- | --- | --- | --- |
| REQ-01 | FR-12 "gosec reports no finding without a recorded justification"; "Supply chain" | gosec v2.29.0 `-quiet -exclude-generated ./...`, with **no** `-exclude-rules` and no `-exclude`, reports **0** issues at the slice head. The `#nosec` count rises by at most 1, from 50 to 51 or fewer. The only allowed new annotation is D1 (`cmd/agentpack/trust_set.go`, G302), with a `-- reason`. No existing annotation is widened. Every one of the 11 baseline findings is handled as the finding table below records. | gosec JSON output (`Stats.found == 0`, `Stats.nosec <= 51`); `git diff` review of each site |
| REQ-02 | G122 and G304 (`worktree_fingerprint.go:107`) and G204 (`worktree_fingerprint.go:57`) | `fingerprintTree` walks through one `os.Root` opened on the worktree. It calls `fs.WalkDir(root.FS(), ".")`, reads type and size with `root.Lstat(name)` (not `entry.Info()`), reads links with `root.Readlink` and reads files with `root.Open`. The digest byte stream is **identical** to the baseline format: `rel\x00mode\x00size\x00`, then `link\x00target\x00`, `file\x00<sha256>\x00` or `other\x00`, with `.git` skipped at the top level and lexical order. `fingerprintGit` builds each command with literal arguments and sets `cmd.Dir = dir` in place of `-C dir`. The environment is unchanged. Four scenarios must hold. (a) A fixture with a nested file, an empty directory, a symlink to a file, a symlink to a directory and a `.git` directory produces exactly the expected byte stream; a recording `hash.Hash` checks this. (b) If a subdirectory is replaced by a symlink to a directory outside the tree, containing a file, between listing and open, nothing outside the tree is read. Either the entry is digested as a link, or the call returns an error. It never digests the outside file's content. (c) Existing `worktree_fingerprint_test.go` and `job_composition_test.go` pass unchanged. (d) `worktreeFingerprint` on the same clean worktree returns the same value twice. | New unit tests in `internal/daemon/worktree_fingerprint_test.go`; existing tests |
| REQ-03 | G304 (`agent_pack_view.go:82`, `agent_pack_composition.go:310`) | `agentPackViewFileDigest` and `compositionFileDigest` open through `safeio.OpenRegular(root, rel)`. That call checks every component for symlinks, keeps containment inside `os.Root` and requires a regular file. `verifyAgentPackView` passes `(root, rel)` and replaces `os.Lstat(current)` with a root-scoped Lstat. `platformSafety` passes `(configDir, name)`. Three scenarios must hold. (a) Existing view and composition tests pass, and the digests are unchanged. (b) A view where an authenticated file is replaced by a symlink to an outside file with the same content is refused. (c) A platform configuration file that is a symlink is skipped, as before, or refused, and its target is never digested. | Existing `agent_pack_view_test.go`, `agent_pack_composition_test.go` and `job_composition_test.go`, plus one new symlink case each |
| REQ-04 | G115, G404, G304 ×2 and G302 (work intake) | **G115 (`capability.go:215`):** when `Statfs` fails, or `Bsize <= 0`, `DiskFreeBytes` stays 0. The product `Bavail*Bsize` saturates at `math.MaxUint64` on overflow (`bits.Mul64`). `GOOS=darwin GOARCH=arm64 go vet ./...` passes, because `Bsize` is `uint32` on darwin. **G404 (`configuration_reload.go:217`):** the default jitter comes from `crypto/rand` (53 random bits divided by 2^53, which falls in [0,1)). `configurationReloadDelay` is unchanged. If `crypto/rand` fails, the jitter is 0.5 (no jitter). **G304 (`identity/pending.go:47`, `cmd/our-runner/enrollment_recovery.go:110`):** both directory fsyncs call the existing `safeio.SyncDirectory`, and the local `syncDirectory` helper and its `os.Open` are deleted. **G302 (`work_intake_proposal_generation.go:301`):** `preparePrivateExecutionRoot` keeps `os.MkdirTemp`. It replaces `os.Chmod(root, 0o700)` with a chmod through `os.OpenRoot(parent)` and `Root.Chmod(filepath.Base(root), 0o700)`, the same pattern as the reviewed `openAgentPromptDirectory`, and removes the temporary directory on failure. The resulting directory mode is exactly `0700`. | `capability_test.go` (helper with Bsize 0, negative, overflow and normal, if extracted); `configuration_reload_test.go` (default jitter in [0,1) over many draws); existing identity and enrollment-recovery tests; a work-intake test asserting `0700` and removal on error; cross-compile vet |
| REQ-05 | FR-12 "`go test -race -count=20` passes" (`TestScanGitCancellationKillsChildrenAfterLeaderExits`, BJAP L-5) | **Production fix (`internal/repo/scan_snapshot.go` `runScanGit`):** when the caller's context is cancelled, the owned process group is killed promptly, even if the Git leader has already exited and a descendant still holds stdout. Implement this with `cmd.Start()`, `stop := context.AfterFunc(commandCtx, func() { _ = cmd.Cancel() })`, `cmd.Wait()` and `stop()`. Keep `stopOwnedGroup` on return, `WaitDelay`, the overflow cancel and the error mapping. **Test:** the marker assertion stays as it is. Add two assertions: `scanGit` returns `context.DeadlineExceeded`, and the elapsed time is below `cmd.WaitDelay` (1 s), which proves the cancellation path rather than the `WaitDelay` path. The other scan tests (`TestScanProcessCancellationAfterLeaderReaped`, `TestScanGitCleansTransportChildrenOnNormalExit` and the overflow and diagnostic tests) still pass. Three runs must pass: `-race -count=20`, the same under 16 busy loops, and once more (`-count=30`) under 24 busy loops. | `go test -race -count=20 -run TestScanGitCancellationKillsChildrenAfterLeaderExits ./internal/repo/`; the load runs; `go test -race ./internal/repo/` |
| REQ-06 | FR-12 flaky tests (`TestAgentProcessNeverReceivesProviderCredential`, `TestNonPushingJobAcceptsRepositoryReadCredential`, RPCR F-1) | **Root cause:** `completeJob` sends `job_finished`, then `retireCompletedJob` removes `jobs/job-*` and only then `job-results/<job>` (`job_completion.go`). The final `treeContains(stateDir)` races that removal. The first test waits only for `jobs/job-*`. The second test waits for nothing after `job_finished`. **Fix (test only):** add a helper that waits, bounded at 30 s, until both `jobs/job-*` and every entry under `job-results/` are absent. Then cancel the daemon context and wait for `d.Run` to return: `startCredentialTestRunner` returns a stop function backed by a done channel. Only after that does the final `treeContains` walk run. `treeContains` keeps failing on **any** walk or read error; it must not skip `ErrNotExist`. The token needle, the environment-dump checks and the event and acknowledgement checks are unchanged. The mid-run walk (line 137) stays strict and unchanged. Three runs must pass: `-race -count=20` for both tests, the same under 16 busy loops, and the full `go test -race ./internal/daemon/`. | Commands in the verification plan; a diff review confirms that no assertion was removed or loosened |
| REQ-07 | FR-12 "gofmt is clean"; AGENTS.md gates | A POSIX `sh` script `scripts/check.sh [format\|vet\|test\|vuln\|sec\|all]` (default `all`, `set -eu`). Each subcommand exits non-zero on its own failure. **`format`:** fails and lists the files if `gofmt -l .` prints anything. **`vet`:** runs `go vet ./...` and `GOOS=darwin GOARCH=arm64 go vet ./...`. **`test`:** runs `go test -race -count=1 ./...`. **`vuln`:** runs `go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...`. **`sec`:** runs `go run github.com/securego/gosec/v2/cmd/gosec@v2.29.0 -quiet -exclude-generated ./...` with no exclusions. The tool versions are defined once in the script. An unknown subcommand prints usage and exits 2. Each failure mode is demonstrated on a throwaway change that is not committed: an unformatted file makes `format` fail, a vet error makes `vet` fail, a reintroduced `os.Open(variable)` makes `sec` fail, and a failing test makes `test` fail. | Run each subcommand at the head (green) and against each injected failure (red), and record the exit codes |
| REQ-08 | FR-12 "`security.yml` runs gosec and govulncheck" | `.github/workflows/security.yml` keeps the `govulncheck` and `gosec` jobs, the same triggers and the same pinned actions. The steps call `sh scripts/check.sh vuln` and `sh scripts/check.sh sec`. The `-exclude-rules` list and the separate `go install` steps are removed. A new workflow `.github/workflows/checks.yml` runs on `pull_request`, on `push` to `main` and on `workflow_dispatch`. It has `permissions: contents: read`, the same pinned `actions/checkout` and `actions/setup-go` SHAs with `persist-credentials: false` and `go-version-file: go.mod`, and a timeout. It runs `sh scripts/check.sh format`, `vet` and `test`. `actionlint` (`~/go/bin/actionlint`) reports no issue for any workflow. | `actionlint .github/workflows/*.yml`; diff review; a CI run is not required locally |
| REQ-09 | AGENTS.md (documented gates) | `docs/TESTING.md` "Automated gates" and `README.md` "Development and verification" name `sh scripts/check.sh` as the gate and list its subcommands. They no longer pin gosec v2.28.0 or govulncheck v1.6.0, and they keep the coverage and packaging commands. | Diff review; `grep -n 'v2.28.0\|v1.6.0' README.md docs/TESTING.md` returns nothing |
| REQ-10 | FR-08 N1; AGENTS.md coverage of 80% or more | `go test -coverprofile` for `./internal/supervisor/` reports **80.0% or more** of statements. New Herdr tests use the existing `startFakeHerdr` server, extended with per-method failure injection or alternate responses as needed. Each `Start` failure branch must close the Herdr workspace (`workspace.close` called) where one was created, and must leave no `.herdr-launch-*`, `.herdr-exit-*` or `.herdr-output-*` entry in `RuntimeDir`. The branches are: `workspace.create` error, `pane.list` error, empty pane list with no `pane` field, missing `RuntimeDir`, `RuntimeDir` not creatable, `pane.send_input` error, and the stdin source conflict before dial. Also cover the `findPane` fallback shape (`{"pane":{"pane_id":...}}`), `awaitOutput` timing out, `defaultSocketPath` without `HERDR_SOCKET_PATH`, and `NewTmux` defaults, without starting a real tmux. No real Herdr or tmux is needed. | `go test -race -coverprofile=... ./internal/supervisor/ && go tool cover -func=...` (total line) |
| REQ-11 | FR-08 N2 | `sweepAgentPromptFiles` and `agentPromptFile.Remove` operate only through root handles. They open `stateDir/supervisor` with `os.OpenRoot`; a missing directory means nothing to do and returns nil. They require `root.Lstat("prompts")` to be a real directory and not a symlink. They open it with `root.OpenRoot("prompts")`, list it through the root (`Open(".")` and `ReadDir`), and remove with `Root.Remove(name)`. `agentPromptFile` keeps the file name, so `Remove` does not need a path join. The public path given to the supervisor is unchanged. Four scenarios must hold. (a) If `prompts` is a symlink to an outside directory holding `prompt-x`, both the sweep and `Remove` return an error, and `prompt-x` still exists. (b) A file named without the prefix survives the sweep. (c) A missing `supervisor` or `prompts` directory makes the sweep return nil without creating it. (d) Existing prompt-file tests (`TestStartupSweepsLeftoverPromptFiles`, `TestPromptFileIsRemovedOnEveryExitPath` and the others) pass. | New tests in the existing prompt-file test file; existing tests |
| REQ-12 | FR-08 N3 | Before any tmux command or Herdr RPC, both `TmuxSupervisor.Start` and `HerdrSupervisor.Start` verify a non-empty `StdinFile` with `os.Lstat`. It must not be a symlink, must be a regular file, and must have no group or other permission bits. A failure returns an error that contains no file content. Extend the shared `validateStdinSource` or add a helper it calls, and keep `DirectProcess` behaviour (`openStdinFile`). Six scenarios are each refused with zero tmux runs and zero Herdr requests recorded: a symlink to a regular file, a directory, a FIFO, a missing file, a `0644` file and a relative path. A `0600` regular file still launches, so the existing redirect tests pass. The remaining race between `Lstat` and the shell's `<` open is recorded as accepted residual risk: the file lives in the runner-owned `0700` directory. | `stdin_file_test.go`, `tmux_test.go` and `herdr_test.go` cases |
| REQ-13 | FR-12 "A harness or CI step compiles and runs the stub package" (runner side only) | At the slice head, the 12 tests in the app's `go-planning-driver_test.go` compile against the runner and pass. Use the overlay method in the verification plan, so nothing is written into the runner tree. Record the result, the app commit used and the runner commit. If a runner change in this slice breaks them, fix the runner change; the app file is read only. The app-side harness itself is FR-12-APP, which this slice does not implement. | The overlay command; `git status --short` stays clean apart from the slice's own files |
| REQ-14 | AGENTS.md "Run typecheck, lint, Fallow, tests, coverage, and the production build" (runner equivalent) | `sh scripts/check.sh all` passes at the slice head: format, vet (linux and darwin), `go test -race ./...`, govulncheck and gosec with 0 findings. `go test ./... -count=1` passes. Package coverage does not fall below baseline for `daemon` (85.5%), `repo`, `identity`, `capability` or `supervisor` (75.5%, and 80% or more under REQ-10). | The full script run; coverage per package |

### gosec finding table (baseline `1b31c61`, v2.28.0 and v2.29.0 identical)

| # | Rule | Site | Real issue? | Resolution (REQ) |
| --- | --- | --- | --- | --- |
| 1 | G115 | `internal/capability/capability.go:215` `fs.Bavail * uint64(fs.Bsize)` | Minor. `Bsize` is `int64` on Linux, so a negative value would wrap. The product can also overflow. | Guard `Bsize <= 0`, saturating multiply (REQ-04). The planner probe confirmed gosec accepts the guard. |
| 2 | G404 | `internal/daemon/configuration_reload.go:217` `rand.Float64()` | No. This is backoff jitter, not security. | Draw the jitter from `crypto/rand` (REQ-04). The probe confirmed this is clean. |
| 3 | G122 | `internal/daemon/worktree_fingerprint.go:107` `os.Open(path)` in `WalkDir` | **Yes (low).** The worktree is agent-writable. A directory swapped for a symlink between listing and open lets the runner read and digest files outside the tree. | Walk through `os.Root` (REQ-02). The probe confirmed this is clean. |
| 4 | G304 | the same line | Same as #3. | Same as #3. |
| 5 | G204 | `internal/daemon/worktree_fingerprint.go:57` `exec.CommandContext("git", append(-C dir, args...))` | No. `git` is fixed, the arguments are internal literals and no shell is used. | Literal arguments per call plus `cmd.Dir` (REQ-02). The probe confirmed gosec accepts fixed literal arguments. No `#nosec`. |
| 6 | G304 | `internal/identity/pending.go:47` `os.Open(dir)` | No. This is the runner-owned identity directory. | `safeio.SyncDirectory`, which already exists and is gosec-clean (REQ-04). |
| 7 | G304 | `internal/daemon/agent_pack_view.go:82` | Low. The view is runner-materialized and read-only, and the walk is path-based. | `safeio.OpenRegular(root, rel)` (REQ-03). |
| 8 | G304 | `internal/daemon/agent_pack_composition.go:310` | Low. The platform configuration directory is runner-owned. | `safeio.OpenRegular(configDir, name)` (REQ-03). |
| 9 | G304 | `cmd/our-runner/enrollment_recovery.go:110` `os.Open(stateDir)` | No. | `safeio.SyncDirectory(stateDir)` (REQ-04). |
| 10 | G302 | `internal/daemon/work_intake_proposal_generation.go:301` `os.Chmod(root, 0o700)` | No. This is a directory, and owner traversal needs `0700`. | Root-scoped `Root.Chmod`, the same pattern as `openAgentPromptDirectory` (REQ-04). |
| 11 | G302 | `cmd/agentpack/trust_set.go:170` `OpenFile(..., 0o644)` | No. The signed trusted key set is public, integrity-protected material. | **D1:** keep `0644` and add one `#nosec G302 -- <reason>`. The alternative is `0600`. |

## Design and decisions

- **D1 (owner may override): trust-set output mode.**
  - `agentpack trust-set sign` writes a signed public document. Services running as other users read it once the operator installs it.
  - Changing the file to `0600` would pass gosec without an annotation. However, an operator who then runs `cp` (rather than `install -m 0644`) would hand the runner service an unreadable file. That is a behaviour regression.
  - Recommended: keep `0644` with one narrowly scoped annotation, `// #nosec G302 -- the signed trusted key set is public integrity-protected material that services running as other users must read`. This matches the repository's 50 existing `-- reason` annotations.
  - Alternative: use `0600` and document the `install -m 0644` step.
- **D2 (owner note): annotations versus "no comments".**
  - The owner's global preference is to write no code comments.
  - A `#nosec` directive is a tool directive that must take comment syntax. The FR-12 acceptance asks for a recorded justification.
  - The plan therefore allows exactly one (D1) and replaces every other finding with a real code fix. The 50 pre-existing annotations are untouched.
- **D3: the scan cancellation fix is a production change, not a test change.**
  - The test asserts the intended contract: cancellation kills the owned group even after the leader has exited.
  - At baseline, `os/exec` stops watching the context once `Process.Wait` returns. So a cancellation that arrives while a descendant holds the stdout pipe has no effect until `WaitDelay`.
  - `context.AfterFunc` restores prompt group termination. A double kill is harmless, because `cmd.Cancel` maps `ESRCH` to `os.ErrProcessDone`.
- **D4: the gate is a script plus CI, with no Git hook.**
  - The repository has no hook convention.
  - CI enforces the gate; contributors run the same script locally.
  - A hook can be added later, if the owner wants one, by setting `core.hooksPath` to a directory that calls `scripts/check.sh format vet`.
- **D5: tool versions.** gosec v2.29.0 and govulncheck v1.8.0 (already in CI, present in the module cache, and verified to build under go1.26.10). They are defined once in `scripts/check.sh` and run with `go run pkg@version`, so CI and local runs cannot drift.
- **D6: worktree fingerprint compatibility.**
  - The fingerprint is compared only within one job (`job.go:953`, `agent_pack_composition.go:370`). It is never persisted.
  - The byte format is still kept identical, and REQ-02 (a) checks it. That avoids an accidental change of semantics.
- **D7: the credential test fix stays inside the test.**
  - Production cleanup order is correct: the terminal event is recorded before local cleanup.
  - The test was observing the state directory before retirement finished.
  - Tolerating vanished files would weaken the "token never reached runner state" assertion, so the test instead waits for an observable quiescent point.

### FR-12-APP: app-side dependency (not implemented by this slice)

The owner of the app repository, or a coordinator-run app slice, applies the following in `app/`. Record it in `docs/implementation-progress.md` under FR-12.

1. Run `gofmt -w` on `src/server/planning-manifests/test-support/go-build-provider.go`, `go-planning-driver.go`, `go-planning-fixture.go` and `go-planning-provider.go`. These are whitespace-only changes; confirm with `gofmt -l` that nothing is listed.
2. Add a test-support function, for example `run-go-planning-driver-unit-tests.ts`, colocated with `run-go-planning-driver.ts` and reusing its `sources` list plus `go-planning-driver_test.go`, `installedPlanningGoToolchain` and `launchPlanningProcess`. The function does five things:
   - It writes an overlay JSON in a private temporary directory, mapping `<runner>/cmd/planning-interop-<uuid>/<name>` to each app source.
   - It runs `go test -vet=off -overlay <json> -c -o <tmp>/driver.test ./cmd/planning-interop-<uuid>` with `cwd` set to the runner and the same environment (`GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`).
   - It runs `<tmp>/driver.test -test.count=1 -test.v` with `cwd` set to the temporary directory.
   - It runs `go vet -overlay <json> ./cmd/planning-interop-<uuid>` and `gofmt -l` on the app Go files.
   - It fails if any of these fail. The overlay writes nothing into the runner tree.
3. Add an integration test, for example `go-planning-driver-unit.integration.test.ts`, that calls it and asserts the 12 test names passed. Include it in `test:integration`. It needs the Go toolchain, as `run-go-planning-driver.test.ts` already does.
4. Optional, and separate: switch `buildGoPlanningDriver` from copying into `../app-runner/cmd/` to the same overlay method. Today it writes into the sibling runner checkout, which is the live runner repository in this workspace.

Until FR-12-APP lands, FR-12 at the parent level stays open on the harness acceptance item. REQ-13 proves runner-side compatibility only.

## Implementation plan

Work test-first: write each failing test or reproduction before the fix. Order steps 1 to 4 first, because they unblock reliable full-suite runs.

- [x] 1. **REQ-05.** Add the elapsed-time and error assertions to `TestScanGitCancellationKillsChildrenAfterLeaderExits`. Show them failing at baseline: elapsed is about 1 s, at or above `WaitDelay`. Then change `runScanGit` (`internal/repo/scan_snapshot.go`) to `Start`, `context.AfterFunc` and `Wait`. Run the REQ-05 commands.
- [x] 2. **REQ-06.** In `internal/daemon/provider_credential_integration_test.go`, make `startCredentialTestRunner` return a stop function that cancels and waits for `Run`. Add a helper that waits for `jobs/job-*` and `job-results/*` to be absent. Use both before the final `treeContains` in both tests. Show the failure under load at baseline (already reproduced), then 0 failures after the change.
- [x] 3. **REQ-02.** Write the byte-stream and symlink-swap tests in `worktree_fingerprint_test.go`, then the `os.Root` walk and the literal-argument `fingerprintGit` (`internal/daemon/worktree_fingerprint.go`).
- [x] 4. **REQ-03.** Add the symlink cases, then switch `agent_pack_view.go` and `agent_pack_composition.go` to `safeio.OpenRegular` and a root-scoped Lstat.
- [x] 5. **REQ-04.** Change `capability.go` (extract a small pure helper for the arithmetic if needed for the test), `configuration_reload.go`, `identity/pending.go`, `cmd/our-runner/enrollment_recovery.go` and `work_intake_proposal_generation.go`, each with its test.
- [x] 6. **D1.** Annotate `cmd/agentpack/trust_set.go:170`, or apply the owner's alternative.
- [x] 7. **REQ-11.** Write the symlinked-prompts-directory tests, then rework the sweep and `Remove` in `internal/daemon/agent_prompt_file.go`.
- [x] 8. **REQ-12.** Write the refusal tests for tmux and Herdr (`internal/supervisor/*_test.go`), then add the pre-launch check in `internal/supervisor/stdin_file.go`, called from both `Start` methods before any external call.
- [x] 9. **REQ-10.** Add the Herdr fake-server failure-path tests and the `NewTmux` and `defaultSocketPath` tests until supervisor coverage is 80% or more.
- [x] 10. **REQ-07.** Add `scripts/check.sh`, then demonstrate a red result per subcommand on throwaway edits and revert them.
- [x] 11. **REQ-08.** Rewire `security.yml`, add `checks.yml` and run `actionlint`.
- [x] 12. **REQ-09.** Update `docs/TESTING.md` and `README.md`.
- [x] 13. **REQ-13 and REQ-14.** Run the overlay check of the app driver tests and the full `sh scripts/check.sh all`. Record the results in this file, set `review_ready` and hand off to a fresh reviewer.

**Owned paths:**

- `internal/repo/scan_snapshot.go` and `scan_snapshot_test.go`
- `internal/daemon/provider_credential_integration_test.go`
- `internal/daemon/worktree_fingerprint.go` and its test
- `internal/daemon/agent_pack_view.go` and `agent_pack_composition.go`, and their tests
- `internal/daemon/configuration_reload.go` and its test
- `internal/daemon/work_intake_proposal_generation.go` and its test
- `internal/daemon/agent_prompt_file.go` and its test
- `internal/capability/capability.go` and its test
- `internal/identity/pending.go`
- `cmd/our-runner/enrollment_recovery.go`
- `cmd/agentpack/trust_set.go`
- `internal/supervisor/stdin_file.go`, `tmux.go`, `herdr.go` (only if a seam is needed) and their tests
- `scripts/check.sh`
- `.github/workflows/security.yml` and `checks.yml`
- `docs/TESTING.md`, `README.md` and this record
- Repair 1: `internal/updater` (F-1, option (a)); changed: `internal/updater/boundary_test.go` only

Do not add code comments other than the D1 annotation.

## Verification plan and results

Every command runs from the runner worktree with `GOTOOLCHAIN=go1.26.10`, plus the tmpfs exports from the environment note while the disk is full. Load runs use `/dev/shm/fr12/load.sh` (N busy-loop shells around a command). The implementer may recreate that script in the scratchpad.

| Command / scenario | IDs | Actual result / date | Evidence | Baseline attribution |
| --- | --- | --- | --- | --- |
| `go run github.com/securego/gosec/v2/cmd/gosec@v2.29.0 -quiet -exclude-generated -fmt json -out <f> ./...` | 01 | Baseline 29 Oct: 11 found, 50 nosec. Head 29 Oct: 362 files, 0 found, 51 nosec (the one added is D1); `check.sh sec` exit 0 in the final `all` run | `scratchpad/fr12-gosec-v2290.json`, `scratchpad/fr12/gosec-mid.json` | All 11 inherited, all fixed |
| `go test -race -count=20 -run '^TestScanGitCancellationKillsChildrenAfterLeaderExits$' ./internal/repo/` and the same through `load.sh 24` with `-count=30` | 05 | Baseline 29 Oct: unloaded 20/20 passed; under load, 1 of 30 failed (2.11 s). The tightened test (`DeadlineExceeded`, under 1 s) was red at baseline (about 1.001 s). Head 29 Oct: unloaded 20/20; `load.sh 16` with `-count=20` PASS; `load.sh 24` with `-count=30` PASS | planner run; implementer run | Inherited flake, fixed |
| `go test -race -count=20 -run '^(TestAgentProcessNeverReceivesProviderCredential\|TestNonPushingJobAcceptsRepositoryReadCredential)$' ./internal/daemon/` and the same through `load.sh 16` | 06 | Baseline 29 Oct: under load, each test failed once in 15 (`...job-results/.../objects/...: no such file or directory`). Head 29 Oct: `-race -count=20` of the three credential tests, 60/60 passed; `load.sh 16` with `-count=20` PASS | planner run; implementer run | Inherited flake, fixed |
| `go test -race ./internal/repo/ ./internal/daemon/ ./internal/supervisor/ ./internal/capability/ ./internal/identity/ ./cmd/...` | 02–06, 10–12 | Head 29 Oct: all pass. Red at baseline: the directory-swap and linked-worktree `.git` fingerprint tests, private-root failure cleanup, the symlinked prompts sweep and `Remove`, and six unsafe stdin cases for tmux and Herdr | implementer run | none |
| `go test -race -coverprofile=<f> ./internal/supervisor/ && go tool cover -func=<f> \| tail -1` | 10 | Baseline: 75.5%. Head 29 Oct: 86.2% | planner run; implementer run | Inherited (FR-08 N1), fixed |
| `GOOS=darwin GOARCH=arm64 go vet ./...` | 04, 07 | Baseline 29 Oct: fail (`syscall.Mkdirat`/`Openat` in a test, D9). Head 29 Oct: pass; `GOOS=linux GOARCH=386 go vet ./internal/capability/` pass | implementer run | Inherited, fixed (D9) |
| `sh scripts/check.sh format`, `vet`, `test`, `vuln`, `sec` and `all`, plus one injected failure per subcommand | 07, 14 | Head 29 Oct, injected failures: `format` exit 1 and lists the file; `vet` exit 1 (Sprintf); `sec` exit 1 (G304); `test` exit 1; an unknown subcommand exits 2. The probes were removed. Final `sh scripts/check.sh all`: exit 0 (format, vet, test, vuln "0 vulnerabilities", sec). An earlier `all` run failed only on the inherited updater flake (see the `internal/updater` row) | `scratchpad/fr12/all2.log` | Updater flake inherited |
| `~/go/bin/actionlint .github/workflows/*.yml` | 08 | Head 29 Oct: actionlint v1.7.7 exit 0 | implementer run | none |
| `grep -n 'v2.28.0\|v1.6.0' README.md docs/TESTING.md` returns nothing | 09 | Head 29 Oct: no output | implementer run | none |
| App driver overlay: write an overlay JSON mapping `<wt>/cmd/planning-interop-probe/<name>` to the 8 app `go-*.go` files (7 sources plus `_test.go`), then `go test -vet=off -overlay <json> -c -o <tmp>/driver.test ./cmd/planning-interop-probe/` and `cd <tmp> && <tmp>/driver.test -test.count=1 -test.v` | 13 | Baseline `1b31c61` (app at `0b643d20`), 29 Oct: 12/12 passed | planner run (`/dev/shm/fr12/overlay.json`); head 29 Oct (`1b31c61` plus changes, app `0b643d20`): 12/12 passed, and `git status` shows no `planning-interop` paths (`scratchpad/fr12/overlay-run.sh`) | none |
| `gofmt -l .` and `go vet ./...` | 07, 14 | Baseline 29 Oct: clean, pass | planner run | none |
| govulncheck | 07, 14 | Baseline 29 Oct (v1.6.0): 0 affecting | planner run | none |
| `git status --short` after the overlay run | 13 | Baseline: clean. Head 29 Oct: no `planning-interop` paths | planner run; implementer run | none |
| `go test -race -count=30 -run '^TestServerHealthDeadlineTimerRollsBackAndExits$' ./internal/updater/` | 14 | 29 Oct: 2 of 30 failed on the baseline export (`deadline timer did not exit`); 0 of 30 failed at the head in isolation. Repair 1, before the fix: unloaded 30/30 passed, `load.sh 16` failed 2 of 30. After the fix: unloaded `-count=30` PASS; `load.sh 16` `-count=30` PASS; `load.sh 24` `-count=30` PASS; prebuilt binary under `load.sh 16` `-count=200` PASS | implementer run; repair 1 run | Inherited, fixed in repair 1 (test-only; root cause under "Review findings and repair") |
| Repair 1: `sh scripts/check.sh all`, three consecutive full runs | 14 | 29 Oct: exit 0, 0, 0 (407 s, 243 s, 268 s). A preceding attempt exited 1 on two `internal/daemon` timing tests while the shared host was saturated (see repair 1) | `scratchpad/fr12r/gate-{1,2,3}.log`, `gate-summary.txt`, `attempt1/gate-1.log` | See repair 1 residual risk |

## Implementation report

Implementer: Opus 5.5 develop-implementer, 29 October 2026. Work is uncommitted in `wt/runner-fr-12` on top of `1b31c61`. The app repository was only read (app commit `0b643d20`, clean).

### Changed paths

Production:

- `internal/repo/scan_snapshot.go`: `runScanGit` uses `Start`, `context.AfterFunc(commandCtx, cmd.Cancel)` and `Wait` (REQ-05).
- `internal/daemon/worktree_fingerprint.go`: `fingerprintTree` walks `fs.WalkDir(root.FS(), ".")` on one `os.Root`, using `root.Lstat`, `root.Readlink` and `root.Open`, plus an `os.SameFile` check between the listed and the opened file. `fingerprintGit` takes a literal `*exec.Cmd` and sets `cmd.Dir` (REQ-02, D8).
- `internal/daemon/agent_pack_view.go`: the walk goes through an `os.Root`; the digest uses `safeio.OpenRegular(root, rel)` (REQ-03).
- `internal/daemon/agent_pack_composition.go`: `compositionFileDigest(dir, name)` uses `safeio.OpenRegular` (REQ-03).
- `internal/capability/disk_free.go` (new) and `capability.go`: generic `diskFreeBytes` with a `<= 0` guard and a saturating `bits.Mul64` (REQ-04).
- `internal/daemon/configuration_reload.go`: `configurationReloadJitterSample(crypto/rand.Reader)`, which takes 53 bits divided by 2^53 and falls back to 0.5 (REQ-04).
- `internal/identity/pending.go` and `cmd/our-runner/enrollment_recovery.go`: `safeio.SyncDirectory`; the local `syncDirectory` helper was deleted (REQ-04).
- `internal/daemon/work_intake_proposal_generation.go`: `restrictDirectoryToOwner(parent, base)` uses `os.OpenRoot(parent).Chmod(name, 0o700)`, and the directory is removed on failure (REQ-04).
- `cmd/agentpack/trust_set.go`: the single D1 annotation `// #nosec G302 -- the signed trusted key set is public integrity-protected material that services running as other users must read`.
- `internal/daemon/agent_prompt_file.go`: `agentPromptFile` keeps `stateDir` and `name`. `openExistingAgentPromptDirectory` opens `supervisor` with `os.OpenRoot`, treats a missing directory as nil, requires `Lstat("prompts")` to be a real directory and opens it with `OpenRoot`. The sweep lists through `Open(".")` and `ReadDir`, and both the sweep and `Remove` use `Root.Remove` (REQ-11).
- `internal/supervisor/stdin_file.go`, `tmux.go` and `herdr.go`: `validateRedirectedStdinSource` runs `validateStdinSource` and then an `Lstat` check (not a symlink, regular, no `0o077` bits). It is the first call in both `Start` methods. `DirectProcess` keeps `validateStdinSource` plus `openStdinFile` unchanged (REQ-12).

Gate, CI and documentation:

- `scripts/check.sh` (new, `chmod +x`, shellcheck-clean): `format`, `vet`, `test`, `vuln`, `sec` and `all`. Several subcommands can be given in order, the default is `all`, an unknown subcommand exits 2, and the versions are defined once (REQ-07).
- `.github/workflows/security.yml`: steps call `sh scripts/check.sh vuln` and `sec`. The `go install` steps and `-exclude-rules` were removed, and `GOTOOLCHAIN: auto` was kept on those steps as before (REQ-08).
- `.github/workflows/checks.yml` (new): `pull_request`, push to `main` and `workflow_dispatch`, `contents: read`, the same pinned checkout and setup-go SHAs, a 30-minute timeout, and the steps `format`, `vet` and `test` (REQ-08).
- `docs/TESTING.md` and `README.md` (REQ-09).
- This record.

Tests:

- `internal/repo/scan_snapshot_test.go` (error and elapsed-time assertions)
- `internal/daemon/provider_credential_integration_test.go`: `startCredentialTestRunner` returns an idempotent stop that cancels and waits up to 30 s for `Run`; it is also registered with `t.Cleanup`. `waitForJobRetirement` is new. `treeContains` and every assertion are unchanged.
- New test files:
  - `internal/daemon/worktree_fingerprint_tree_test.go`
  - `internal/daemon/agent_pack_symlink_test.go`
  - `internal/daemon/configuration_reload_jitter_test.go`
  - `internal/daemon/work_intake_proposal_private_root_test.go`
  - `internal/daemon/agent_prompt_file_root_test.go`
  - `internal/capability/disk_free_test.go`
  - `internal/supervisor/stdin_redirect_safety_test.go`
  - `internal/supervisor/herdr_failure_test.go`
  - `internal/supervisor/tmux_defaults_test.go`
- `internal/supervisor/herdr_test.go`: the fake Herdr gained a `results` map that overrides responses per method.
- `internal/daemon/agent_result_copy_bounds_test.go`: see D9.

No code comments were added apart from the D1 annotation.

### Implementer decisions and deviations (for review)

- **D8: linked-worktree `.git` file (a baseline bug, fixed).**
  - Production workspaces are linked worktrees (`git worktree add` in `internal/repo/repo.go:537`), so their `.git` is a file.
  - At baseline, `fingerprintTree` returned `SkipDir` for that file. On a non-directory entry, `WalkDir` then skips every remaining entry of the top-level directory. Every top-level path that sorts after `.git` (`.github`, `.gitignore`, `README.md`, `src/` and so on) was therefore missing from the tree section. An edit to an already ignored file there did not change the fingerprint.
  - Now `SkipDir` is returned only for a `.git` directory; a `.git` file is skipped alone.
  - The byte stream for a `.git` directory is identical, as REQ-02 (a) checks.
  - `TestWorktreeFingerprintCoversEntriesAfterAGitFileInALinkedWorktree` failed at baseline and passes now.
  - The fingerprint is compared only within one job (D6), so no persisted value changes.
  - The planner should confirm this in-scope correction.
- **D9: darwin vet was red at baseline.**
  - `GOOS=darwin GOARCH=arm64 go vet ./...` failed on `internal/daemon/agent_result_copy_bounds_test.go`: `syscall.Mkdirat` and `syscall.Openat` are undefined on darwin.
  - The contract assumed that vet passes. The test now uses `golang.org/x/sys/unix` (already a direct dependency at v0.47.0) for those five calls. This is a test-only change in a file outside the owned paths, and the behaviour is unchanged (`TestCopyRefusesADeepDirectoryChainQuickly` passes).
  - The darwin production build was already green.
- **D10: separate test files.** The new tests are in new, focused test files rather than appended to the existing ones. The contract allows "or its test".
- **D11: fingerprint open check.** `fingerprintRegularFile` compares the `Lstat` result with the opened file's `Stat` using `os.SameFile`. If a file is swapped inside the tree between listing and open, the call returns an error wrapping `errWorktreeChangedDuringComposition` rather than digesting a different file. This goes beyond the contract and only adds a failure mode.
- **REQ-03 scenarios (b) and (c)** were already refused or skipped at baseline: an `Lstat` symlink lands in the `default` branch, and `entry.Type().IsRegular()` is false for a symlink. The new tests are regression guards, not red-first tests. The red evidence for REQ-03 is gosec (G304 at both sites).
- **REQ-04 directory sync** has no new behavioural test. The existing identity and enrollment-recovery suites pass, and gosec is the red-first evidence.
- **Awaiting-output timeout test** waits the real 5 s drain timeout. No timing seam was added to production code.
- **Inherited load-sensitive test outside scope:** `internal/updater` `TestServerHealthDeadlineTimerRollsBackAndExits` failed once (`deadline timer did not exit` after 10 s). That happened during the injected-failure `test` run, while a full `-cover` run of five packages was going at the same time. The updater package is untouched by this slice. See the verification table for the clean full-gate run.

### Residual risk (accepted, per REQ-12)

A race remains between the supervisor's `Lstat` and the shell's `<` open for tmux and Herdr. The prompt file lives in the runner-owned `0700` `supervisor/prompts` directory.

### Candidate coverage

Measured at the head with `go test -count=1 -cover`; baselines for `repo`, `identity` and `capability` come from a `git archive 1b31c61` export:

| Package | Baseline | Head |
| --- | --- | --- |
| `daemon` | 85.5% | 85.6% |
| `repo` | 87.9% | 88.0% |
| `identity` | 80.2% | 80.3% |
| `capability` | 90.0% | 90.2% |
| `supervisor` | 75.5% | 86.2% (with `-race`) |
| `updater` (repair 1) | 79.2% | 79.3% |

## Reviewer requirement review

Reviewer: fresh Opus 5.5 develop-reviewer, 29 October 2026. Reviewed `git diff 1b31c61` plus the untracked files in `wt/runner-fr-12`, with `GOTOOLCHAIN=go1.26.10`. The reviewer changed no code. Probes ran on a `tar` copy and a `git archive 1b31c61` export in `scratchpad/rev12/`. UI, prototype and browser checks do not apply, because this slice has no user-facing surface.

| ID | Status | Evidence |
| --- | --- | --- |
| REQ-01 | implemented | gosec v2.29.0 `-exclude-generated -fmt json ./...` with no exclusions reports `{'files': 362, 'nosec': 51, 'found': 0}`. `check.sh sec` exits 0. In the diff, the only new `#nosec` is D1 at `cmd/agentpack/trust_set.go:170`, with a reason, and no annotation was removed or widened (56 to 57 grep hits). Each fix is a code change, not a suppression. G115 now has a guard and a saturating `bits.Mul64` (`internal/capability/disk_free.go`). G404 now uses `crypto/rand`. G122 and G304 in the fingerprint are now `os.Root` scoped. G204 now uses literal argument vectors and `cmd.Dir`. Three G304 sites now call `safeio.SyncDirectory` or `safeio.OpenRegular`. The G302 chmod (work intake) now goes through `Root.Chmod` on the parent, with the same `0700` mode plus containment. |
| REQ-02 | implemented | `internal/daemon/worktree_fingerprint.go` `fingerprintTree` and `fingerprintRegularFile`. I ran a side-by-side harness of the baseline and head tree walks (`scratchpad/rev12/fpcmp`). It covers `.git` directories and objects, nested `sub/.git` files, setuid, absolute and escaping links, dangling links, a FIFO and an empty directory. The result is a byte-**identical** stream for both a `.git` directory and no `.git`. The streams differ only for a top-level `.git` *file* (D8): the baseline emitted 112 bytes and dropped every top-level entry sorting after `.git`, and the head emits 417. **D8:** the fingerprint is held in memory only. It is set at `job.go:953` and `:980` and compared at `agent_pack_composition.go:371`, all within one process. It is never persisted or sent to the app. The change is therefore a correct bug fix that invalidates no stored state. **D11:** a `SameFile` mismatch arises only when a file is swapped between `Lstat` and `Open`. It surfaces as `errWorktreeChangedDuringComposition` (job preparation fails, or composition is refused). That is fail-closed and acceptable. Tests: `worktree_fingerprint_tree_test.go`, which covers the byte stream, the directory swap, an escaping link, and linked-worktree stability and sensitivity. The existing fingerprint and composition tests pass. |
| REQ-03 | implemented | `agent_pack_view.go` (an `os.Root` walk, root `Lstat` and `safeio.OpenRegular`) and `agent_pack_composition.go:310`. `agent_pack_symlink_test.go` covers a file symlink, a directory symlink and a symlinked platform configuration file. The existing view and composition suites pass. |
| REQ-04 | implemented | `disk_free.go` and `disk_free_test.go` cover the 0, negative, overflow and `uint32` cases. Linux/386 and darwin vet pass. `configurationReloadJitterSample` with its test covers the [0,1) range, the bounds and the 0.5 fallback. `identity/pending.go` and `enrollment_recovery.go` call `safeio.SyncDirectory`, and the local helper was deleted. `restrictDirectoryToOwner` with `work_intake_proposal_private_root_test.go` gives mode `0700` and removal on failure. |
| REQ-05 | implemented | `scan_snapshot.go:173-178` (`Start`, `context.AfterFunc(commandCtx, cmd.Cancel)`, `Wait`, `stop`). No goroutine leaks: `stop()` runs before the deferred `cancel()`, and the AfterFunc goroutine exists only once the context is done. The kill target is the `Setpgid` group the call owns. A concurrent AfterFunc after `Wait` falls in the same window as the existing deferred `stopOwnedGroup`, so no new class of wrong-process kill is introduced. The test now asserts `DeadlineExceeded` and an elapsed time under 1 s. Reviewer runs: `-race -count=20` PASS; `load.sh 24` with `-count=30` PASS. `go test ./internal/repo/` PASS. |
| REQ-06 | implemented | The diff is test-only. `startCredentialTestRunner` returns an idempotent stop that cancels and waits on `done`. `waitForJobRetirement` is strict and fails on unexpected `ReadDir` errors. `treeContains`, the mid-run walk and every assertion are untouched (no hunk). Reviewer runs (prebuilt `-race` binary) of both tests with `-count=20` all passed: unloaded, `load.sh 16` and `load.sh 24`. |
| REQ-07 | implemented | `scripts/check.sh` is POSIX `sh`, `set -eu`, shellcheck clean, with the versions defined once. It validates arguments before `cd`, and it runs from any directory: I ran it from `/`. Injected probes on a scratch copy: unknown subcommand exits 2; `format` exits 0 when clean, and exits 1 on an unformatted file and lists it; `vet` exits 1 on a bad Sprintf; `sec` exits 1 on G304; `test` exits 1 on a failing test. |
| REQ-08 | implemented | `security.yml` keeps the same jobs, triggers and SHA-pinned actions, and its steps call `check.sh vuln` and `check.sh sec`. The `-exclude-rules` list and the `go install` steps are gone. The new `checks.yml` has `contents: read`, SHA-pinned checkout and setup-go, `persist-credentials: false`, `go-version-file`, a 30-minute timeout, a concurrency group, and the format, vet and test steps. actionlint v1.7.7 exits 0. The `# vX.Y.Z` comments after the pins follow the existing workflow convention; they are not code comments. |
| REQ-09 | implemented | `README.md` and `docs/TESTING.md` name `sh scripts/check.sh` and its subcommands, and they keep the coverage and packaging commands. A grep for `v2.28.0`, `v1.6.0`, `gosec@` and `govulncheck@` outside `scripts/check.sh` and the development records finds nothing. |
| REQ-10 | implemented | `internal/supervisor` total is 86.2% under `-race`. `findPane`, `awaitOutput`, `NewTmux` and `validateRedirectedStdinSource` are at 100%, and `HerdrSupervisor.Start` is at 76.2%. `herdr_failure_test.go` asserts `workspace.close` and that no `.herdr-*` runtime entries remain, for every listed branch. |
| REQ-11 | implemented | `agent_prompt_file.go` `openExistingAgentPromptDirectory`, sweep and `Remove`. `agent_prompt_file_root_test.go` covers (a) to (c), plus a `prompts` entry that is a file. The existing prompt-file tests pass. |
| REQ-12 | implemented | `stdin_file.go` `validateRedirectedStdinSource` is the first call in `TmuxSupervisor.Start` and `HerdrSupervisor.Start`. `stdin_redirect_safety_test.go` covers six cases × two supervisors, with zero tmux calls and zero Herdr requests, checks that refusals contain no file content, and checks that a `0600` file is accepted. `DirectProcess` is unchanged. |
| REQ-13 | implemented | The overlay run at the head, with app `b9e4b775` (the newer app head, not `0b643d20`), passes 12/12 driver tests. `git status` shows 0 `planning-interop` paths. |
| REQ-14 | **partial** | Format, both vet runs, govulncheck (0 affecting) and gosec (0) pass. Coverage is at or above baseline: daemon 85.6, repo 88.0, identity 80.3, capability 90.2, supervisor 86.2, our-runner 87.7 and agentpack 85.5. **However, `sh scripts/check.sh all` exited 1 in both of the reviewer's full runs** (`scratchpad/rev12/all.log`, plus the rerun of `format vet test`). Each time the cause was `internal/updater` `TestServerHealthDeadlineTimerRollsBackAndExits` (`boundary_test.go:222: deadline timer did not exit`). That test is untouched, and its package does not import any changed package. A full `go test -race ./...` on the baseline export also failed on the same test. So the flake is inherited, but it reproduces in 3 of 3 full-suite runs on this host, and the new `checks.yml` test job puts it on every pull request. |

Docker-gated tests were not run, because no change touches the Docker paths.

## Review findings and repair

- **F-1 (blocking, REQ-14): the enforced gate is red.**
  - Evidence: `sh scripts/check.sh all` failed 2 of 2 times at the head and 1 of 1 time on the baseline export. The failing test is the inherited updater test `TestServerHealthDeadlineTimerRollsBackAndExits`. The slice's own outcome is "one enforced gate without inherited exceptions", and REQ-14 requires `all` to pass.
  - Required correction, one of:
    - (a) Recommended: a directed repair makes this test deterministic. The implementer's hypothesis is that, with `HealthTimeout` at 30 ms, a late initial `Recover` rolls back synchronously and arms no timer. Confirm the cause and fix it: in the test if the test is wrong, or in `internal/updater` if `Serve` can hang in production. Add `internal/updater` to the owned paths.
    - (b) The planner, with the owner's explicit acceptance, amends REQ-14 and the outcome to exclude this test, and records that the new `checks.yml` will fail intermittently until a follow-up lands.
  - Verification scenario: `sh scripts/check.sh all` exits 0 in three consecutive full runs. `go test -race -count=30 -run '^TestServerHealthDeadlineTimerRollsBackAndExits$' ./internal/updater/` passes, both unloaded and under `load.sh 16`.
- **Non-blocking notes:**
  - N-1: `openExistingAgentPromptDirectory` opens `stateDir/supervisor` by path, so a symlinked `supervisor` is followed. The contract only requires the `prompts` check, and `stateDir` is runner-owned.
  - N-2: `fingerprintTree` now follows a worktree root that is itself a symlink. The baseline digested such a root as a link. Workspace directories are runner-created, so this has no practical effect.
  - N-3: Both the cancellation AfterFunc and the existing deferred `stopOwnedGroup` send `kill(-pgid)` after the leader is reaped. Once every member of the group has exited, the PGID can in principle be reused. This is inherited, and it is worth noting for the `exec` call-site audit follow-up.

### Repair attempt 1 (F-1, 29 October 2026)

Implementer: Opus 5.5 develop-implementer, directed repair. Coordinator decision: option (a), so `internal/updater` is added to the owned paths. N-1 to N-3 stay follow-ups and are unchanged.

- **Reproduced.** `go test -race -c` of `./internal/updater` at the head, then `-test.run '^TestServerHealthDeadlineTimerRollsBackAndExits$' -test.count=30`. Unloaded: 30/30 passed. Under `scratchpad/fr12/load.sh 16`: 2 of 30 failed (`boundary_test.go:222: deadline timer did not exit`, after 11.4 s and 13.3 s).
- **Root cause (test, not production).**
  - I ran a diagnostic copy of the test in `scratchpad/fr12r/mod`, under `load.sh 16` with `-count=200`. It failed 4 times. In every failure, the persisted `HealthDeadline` was already 20 to 210 ms in the past when `Activate` returned: the 30 ms deadline was used up by the state fsync in `Activate` under load. The state was then `idle` with `FailedReleaseID=rel-6.0.0` and `LastError="health deadline expired"`.
  - So the initial `Coordinator.Recover` in `Server.Serve` had already rolled back synchronously. `armRecoveryTimer` then saw `idle` and armed no timer, and `Serve` kept serving, as designed.
  - The same can happen when the deadline passes between the initial `Recover` and the `Status` read in `armRecoveryTimer`.
  - Production is correct. Startup recovery handles an already missed deadline (README: "persistent recovery process so ... missed health deadline is handled"), and the rollback did complete. Only the deadline timer path ends `Serve`. The test assumed that its 30 ms real-time deadline was still pending when `Serve` started, and that is not guaranteed under load. No production change was made.
- **Fix (`internal/updater/boundary_test.go`, test only).**
  - The test uses `serveStartupClock`, a mutex-guarded scripted clock. During `Prepare` and `Activate`, it reports the activation instant. Then it reports that instant (deadline still pending) for exactly `serveStartupDeadlineReads = 2` further reads: the initial `Recover` and the `armRecoveryTimer` `Status` check. Every later read reports one hour on, so the deadline has expired.
  - The real timer still waits `time.Until(deadline)` (30 ms at most). Its `Recover` is now the only path that can observe expiry and roll back, whatever the host load.
  - Every original assertion stays: `Serve` returns nil within 10 s, the phase is `idle` and `FailedReleaseID` is the release. Two assertions are new and stricter: `LastError == "health deadline expired"`, and the clock was read after the startup reads. The second proves that the timer, not startup recovery, performed the rollback.
  - The 30 ms `HealthTimeout` and the 10 s bound are unchanged.
  - Coupling note: the test depends on `Serve` reading the clock exactly twice before it arms the timer. If production adds a startup read, the test fails loudly (startup rolls back, `Serve` does not exit). It cannot pass silently.
- **Sensitivity (mutation probes on the scratch copy, not kept).**
  - Removing `s.armTimer` from `armRecoveryTimer` makes the test fail (`deadline timer did not exit`).
  - Setting `serveStartupDeadlineReads = 0` makes startup recovery see the deadline as expired. That is the original race path, and it fails deterministically (1 of 1). This is the red evidence for the repair.
- **Changed paths:** `internal/updater/boundary_test.go` (imports `math` and `sync`, the test body, and `serveStartupClock`), and this record. No production file changed.
- **Evidence (`GOTOOLCHAIN=go1.26.10`):**
  - `go test -race -count=30 -run '^TestServerHealthDeadlineTimerRollsBackAndExits$' ./internal/updater/`: PASS unloaded, PASS under `load.sh 16`, and PASS under `load.sh 24`. A prebuilt `-race` binary under `load.sh 16` with `-count=200` also passed.
  - `gofmt -l internal/updater` is empty and `go vet ./internal/updater` passes.
  - Coverage of `internal/updater` (`go test -count=1 -cover`): 79.2% before the fix and 79.3% after. `armTimer` went from 75.0% to 83.3%, and `armRecoveryTimer` stays at 100%.
  - **`sh scripts/check.sh all`:**
    - First attempt: exit 1, failing only in `internal/daemon`, on `TestPlanningJobRenewsBeforeItsOriginalLeaseExpires` (`job_integration_test.go:473`, `lease_lost`) and `TestTerminalCommandRedeliveryWithExpiredCredentialReplaysStoredAck` (`provider_credential_integration_test.go:231`, the 10 s ack wait). The daemon package took 382 s, while other agents' `next build`, phpunit and vitest jobs saturated the host. `internal/updater` passed in that run. Neither test is touched by this repair. The body of the second is unchanged by the slice; only its call to `startCredentialTestRunner` changed. Log: `scratchpad/fr12r/attempt1/gate-1.log`.
    - Rerun: three consecutive full runs, all exit 0 (407 s, 243 s and 268 s). Each run covers format, vet for linux and darwin, `go test -race ./...`, govulncheck (0 affecting) and gosec. Logs: `scratchpad/fr12r/gate-{1,2,3}.log` and `gate-summary.txt`.
- **Residual risk for the reviewer:** the two daemon timing tests above failed once on a saturated shared host. They are not part of F-1, and I did not investigate them under this directed repair. They are a candidate follow-up if they recur.

### Re-review after repair attempt 1 (29 October 2026)

Reviewer: second fresh Opus 5.5 develop-reviewer. Reviewed `git diff main...HEAD` (empty, because `main` and `HEAD` are both `1b31c61`) plus the uncommitted and untracked changes in `wt/runner-fr-12`, with `GOTOOLCHAIN=go1.26.10`. No product code was changed. Mutation probes ran on an `rsync` copy in `scratchpad/rev12r/mut`. The baseline comparison ran on a `git archive 1b31c61` export in `scratchpad/rev12r/base`. Nothing was written into `app-runner` or the app repository.

**F-1 repair verification.**

- **Clock reads.** In `Serve`, the startup `Coordinator.Recover` reads the clock once (`coordinator.go:415`), and the `Status` call in `armRecoveryTimer` reads it once (`coordinator.go:385`). So `serveStartupDeadlineReads = 2` matches production exactly. `armTimer` waits on the real clock (`time.Until`) for the persisted deadline, which is the activation instant plus 30 ms. The third read, in the timer's `Recover`, sees expiry. The test no longer depends on wall time, and a production change that adds a startup read fails it loudly.
- **Assertions.** No original assertion was weakened: `Serve` must still return within 10 s, and the state must be `idle` with `FailedReleaseID`. Two new assertions are stricter: `LastError == "health deadline expired"`, and `servedExpired()` proves the timer performed the rollback.
- **Mutation probes on the scratch copy:**
  - Removing the `s.armTimer(...)` call in `armRecoveryTimer` makes the test FAIL (`boundary_test.go:226: deadline timer did not exit`).
  - Replacing the timer's `Recover` with a no-op makes it FAIL (`boundary_test.go:230`, the phase stays `awaiting_health`).
  - Setting `serveStartupDeadlineReads = 0` makes it FAIL (`:226`).
  - The unmutated copy passes `-count=3`.
- **Repeated runs.** `go test -race -count=30 -run '^TestServerHealthDeadlineTimerRollsBackAndExits$' ./internal/updater/` passed 30/30 unloaded. A prebuilt `-race` binary passed `-count=30` under `load.sh 16` and `-count=100` under `load.sh 24`.

**Gate runs (`sh scripts/check.sh all` from the worktree).**

- **Run 1: exit 1 (236 s).**
  - `internal/daemon`, `internal/repo` and `internal/updater` passed.
  - The only failure was `internal/sourcepublication` `TestReviewOperationsNearExpiryCredentialIsNotReplacedByTokenFile/reviewer_identity_verification`. The test's `httptest` server logged `unexpected request: GET /` twice (`requests=2`).
  - That package and all of its in-module dependencies (`safeio`, `config`, `redact`, `validation`, `api/runner/v1`) are untouched by the slice.
  - The runner never requests the bare path `/`. These were foreign connections to a random loopback port from another process on this shared host.
  - `go test -race -count=50 -run TestReviewOperations ./internal/sourcepublication/` then passed 2850/2850.
  - Verdict: inherited, environmental and not slice-caused.
- **Run 2: exit 0 (224 s).** It ran format, vet (linux and darwin), `go test -race ./...`, govulncheck ("Your code is affected by 0 vulnerabilities") and gosec (exit 0).
- Logs: `scratchpad/rev12r/gate-1.log` and `gate-2.log`.

**Residual daemon timing tests.** Both tests ran with `-count=10` from prebuilt `-race` binaries, on the head and on the `1b31c61` export (`scratchpad/rev12r/daemon-flake.sh`):

| Load | Head | Baseline `1b31c61` |
| --- | --- | --- |
| Unloaded | 20/20 | 20/20 |
| `load.sh 24` | 20/20 | 19/20. `TestPlanningJobRenewsBeforeItsOriginalLeaseExpires` failed with the same `lease_lost` signature at `job_integration_test.go:473`, at a host load average of 41 |
| `load.sh 40` | 20/20 | 20/20 (host load average about 49) |

- **Lease test.** The lease failure reproduces on the baseline with the identical signature, so the slice did not cause it. The test uses real-time 6 s and 8 s leases around an 8 s agent sleep. It is a pre-existing load flake.
- **Ack-wait test.** The ack-wait failure did not reproduce on either side. The slice changed only that test's cleanup, which now cancels and waits for `Run` after the body; its 10 s ack wait is unchanged. It is judged a pre-existing load flake as well.
- **Impact on the gate.** Neither blocks the gate's purpose. They reproduce only on a saturated host. The gate was green in run 2 and in the implementer's three consecutive runs, and CI uses dedicated runners. They are recorded as follow-ups.

**Other checks at the head.**

- `actionlint .github/workflows/*.yml` exits 0.
- `grep 'v2.28.0\|v1.6.0' README.md docs/TESTING.md` finds nothing.
- The only added comment in the diff, including the untracked Go files and `scripts/check.sh`, is the D1 `#nosec G302 -- ...` annotation.
- The app driver overlay (`scratchpad/fr12/overlay-run.sh`, app `44b816b3`) passes 12/12 and leaves 0 `planning-interop` paths.
- Coverage (`go test -count=1 -cover`):

| Package | Head | Baseline |
| --- | --- | --- |
| `updater` | 79.2% | 79.2% |
| `supervisor` | 86.2% | 75.5% |
| `daemon` | 85.7% | 85.5% |
| `repo` | 88.0% | 87.9% |
| `identity` | 80.3% | 80.2% |
| `capability` | 90.2% | 90.0% |

| ID | Status | Re-review evidence |
| --- | --- | --- |
| REQ-01 | implemented | `check.sh sec` exits 0 in gate run 2. There are no exclusions in `scripts/check.sh` or `security.yml`. The diff adds only the D1 annotation (`cmd/agentpack/trust_set.go:170`). |
| REQ-02 | implemented | The `worktree_fingerprint.go` diff matches the contract (an `os.Root` walk with `Lstat`, `Readlink`, `Open` and `SameFile`, plus literal `exec` with `cmd.Dir`). The daemon suite passes in gate run 2. |
| REQ-03 | implemented | The daemon suite passes, including `agent_pack_symlink_test.go`. Unchanged since the first review. |
| REQ-04 | implemented | The capability, daemon and identity suites pass in gate run 2. darwin vet passes. |
| REQ-05 | implemented | The `scan_snapshot.go` `Start`, `AfterFunc` and `Wait` diff was reviewed. `internal/repo` passes in both gate runs. |
| REQ-06 | implemented | The test-only diff was reviewed. The assertions and `treeContains` are unchanged. The daemon suite passes in both gate runs. |
| REQ-07 | implemented | The script was reviewed (POSIX, `set -eu`, versions defined once, usage exits 2). Run 1 shows a failing test turns `test` red, and run 2 exits 0. |
| REQ-08 | implemented | The `security.yml` diff and `checks.yml` were reviewed. `actionlint` exits 0. |
| REQ-09 | implemented | The version grep is empty. |
| REQ-10 | implemented | `supervisor` coverage is 86.2%. |
| REQ-11 | implemented | The `agent_prompt_file.go` diff was reviewed. The daemon suite passes. |
| REQ-12 | implemented | `validateRedirectedStdinSource` is the first call in both `Start` methods. The supervisor suite passes. |
| REQ-13 | implemented | The overlay passes 12/12 against app `44b816b3`, with no `planning-interop` paths left. |
| REQ-14 | implemented | F-1 is resolved: the updater test is deterministic and guarded by mutation probes. `check.sh all` exits 0 in gate run 2 and in the implementer's three runs. Coverage is at or above baseline. The one red run was the inherited, environmental `sourcepublication` flake, in a package the slice does not touch. |

There are no blocking findings. New non-blocking follow-ups:

- **N-4.** The `internal/sourcepublication` review-credential test servers fail on foreign loopback requests (`GET /`) from other processes on a shared host.
- **N-5.** `TestPlanningJobRenewsBeforeItsOriginalLeaseExpires` is load-sensitive on the baseline, because it uses real-time leases. `TestTerminalCommandRedeliveryWithExpiredCredentialReplaysStoredAck` probably is too, because of its real-time 10 s ack wait. Both are candidates for scripted clocks.

## Completion

- Decision: done, 29 October 2026. REQ-01 to REQ-14 are all implemented, and repair attempt 1 resolves F-1.
- Remaining parent-feature or excluded work:
  - FR-12-APP, the app harness and gofmt of 4 app files.
  - Per-package coverage of 80% outside `internal/supervisor`.
  - N-4 and N-5, the inherited load and environment flakes described in the re-review.
  - Planner confirmation of D8 and D9. Both reviewers accepted them as in-scope corrections, so this is not blocking.
  - An audit of other `exec` call sites for the post-leader-exit cancellation pattern fixed in REQ-05. Candidates: `internal/repo/repo.go`, `credential_read.go` and `internal/daemon/trusted_result_repository.go`.
- Product/checkpoint links updated: none. On completion, the coordinator updates the app `docs/implementation-progress.md` FR-12 entry. This planner does not edit the app repository.

## Subsequent changes

None.
