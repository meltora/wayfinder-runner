# Runner Docker Build checks: agent snapshot under hand-off and a host-owned check toolchain

## Control

| Field                  | Value |
| ---------------------- | ----- |
| ID                     | FR-01 follow-up `runner-docker-build-checks` (RDBC) |
| Kind                   | bug (release blocker for Docker-isolated Build jobs) plus one bounded feature (host-declared check image) |
| Status                 | review_ready (code verified; REQ-14 live pending) |
| Risk                   | critical. The slice changes what the runner reads out of the agent's workspace into the trusted result repository, and which image runs required checks. A defect can publish harness state into a change set, hide a real concurrent change, or let a check run in an image that nobody on the host chose. It does not touch credentials, the control-plane protocol or tenant data. |
| Complexity             | complex. Two independent defects meet in one live path (snapshot refusal, then missing toolchain). The work crosses the candidate copy, configuration validation, the check task path, the provenance artifact, the start gate, doctor, a new packaging directory and gated Docker tests. Each piece is small; the live proof depends on rootless Docker behaviour that unit tests cannot show. |
| Parent scope           | `PRODUCT.md` "Implementation and review flow" (Build runs required checks and returns evidence), "Workspace composition" item 2 ("A pinned project toolchain layer"), the isolation baseline ("Separate trusted bootstrap and evidence collection from checked-out repository code"), and MCP enforcement (scoped checks use the same runner-owned sandbox and network policy as final required checks; `platform_run_check` cannot supply or override the executable, isolation or network). Parent records in the app repository: `docs/development/build-job-approved-plan-delivery.md`, section "FR-01 Docker Build check (9 November 2026)" (defects FD-1, FD-2, FD-3); `docs/development/runner-local-checks.md`; `docs/development/runner-hermetic-check-workspace.md` (D49, REQ-12 provenance, "Owner decisions on LC-1 to LC-4"); `docs/development/runner-job-image-claude-cli.md` (RJIC REQ-12, still open). |
| Product sources        | `PRODUCT.md` sections named above. Owner decisions LC-1 and LC-2 (checks stay defined on the runner host; the control plane cannot define check commands), LC-3 (isolation changes are restart-required), LC-4 (`acknowledge_direct`). |
| Planner                | Opus 5.5 (`claude-opus-5-5`); develop-planner subagent, RDBC planning session, 9 November 2026 |
| Implementer            | Opus 5.5 (`claude-opus-5-5`); develop-implementer subagent, worktree `agent-a828b3f7e7edaa3e6`, branch `worktree-agent-a828b3f7e7edaa3e6`, fast-forwarded to main `65e7310`, 9 November 2026 |
| Reviewer               | Opus 5.5 (`claude-opus-5-5`); fresh develop-reviewer, reviewed head `a6208e43` against main `65e7310`, 9 November 2026. Re-review after repair attempt 1 by a second fresh develop-reviewer, head `7035946`, 9 November 2026 |
| Repair attempt         | 1 |
| Updated                | 2026-11-09 |
| Baseline               | app-runner `main` at `fc4ae6a` ("test(daemon): prove a job keeps its start-time checks across a hot reload"), worktree `agent-a6540183dac8397d8`, branch `worktree-agent-a6540183dac8397d8`. This record is the only change. |
| Blocker / resume state | Code verified; REQ-14 live pending. The re-review after repair attempt 1 verified REQ-01 to REQ-13 as implemented (see "Re-review after repair attempt 1"). The live verification (REQ-14) still needs the coordinator to swap the runner binary, edit `runner.local.yaml` and restart; see "Coordinator live steps". The record cannot reach `done` before REQ-14 passes, because the contract does not exempt it. |

## Outcome and current gap

**Outcome.** On a runner with Docker isolation and a registered Go check, a Build job on `OMKE/shortstack-e2e` reaches Ready for review. The agent's iterative `platform_run_check` calls succeed while Claude is running, the terminal required check runs `go test ./...` in a host-chosen, digest-pinned toolchain image and passes, the provenance artifact names that image digest, and Claude's harness state never appears in the change set. The same run is observed running a Bash command under the weaker nested sandbox, which closes RJIC REQ-12.

**What happened on 9 November 2026 (FR-01 Docker check, app record):**

- Runner config revision 5: Docker, Claude, image `our-runner-job:rjic@sha256:a2c8cabef3dbe9d4044bf2bcb2f7031aadbeee24ac0bd0dce8b41b5e900e8382`, network `our-runner-jobs-dev`, egress `local-dev-egress-not-enforced`.
- KAN-24, Build attempt `aaaf3559-752d-488c-8cf4-f17aed432072`: outcome `required_checks_failed`. The `go-test` check (container `9162e1267589`, same job image) failed `exit_nonzero` in 605 ms. Committed tree `d716af79…` contains only `max.go` and `max_test.go`.
- **FD-1.** The agent's `platform_run_check` was refused three times (keys `check-go-test-1` twice, `check-go-test-2`) with "the checkout changed while it was read at .claude/.cc-writes". The terminal snapshot, taken after ownership reclaim, succeeded.
- **FD-2.** The job image has no Go (`go` and `gofmt` not found), so every Docker Go check fails. This was a documented exclusion in `runner-local-checks.md` REQ-05 and `runner-hermetic-check-workspace.md` D49; it is now a release blocker.
- **FD-3.** Hook events carry only `hook_type` (`internal/daemon/hook_event.go`, `hookspool.Event`), so the runner's evidence cannot show that Bash ran under the weaker sandbox. RJIC REQ-12 stays open.

**Current code at `fc4ae6a` (evidence):**

- Candidate copy, `internal/daemon/agent_result_copy.go`. Five distinct failures produce the same text "changed while it was read at X": `Lstat` failure (line 244), `OpenRoot` failure on a subdirectory (284), the `sameDirectory` identity check (288), `Readlink` failure (299) and a change of a copied file (342). The copy already honours a `skip` map (lines 26, 40, 186, 233), filled today only with nested candidate roots (`agent_result_resolution.go:92`, `nestedCandidates`).
- Ownership: during the agent run the workspace is handed to the container user (`internal/daemon/job.go` around 653) by `chmod -R g+rX` then `chown -hR 10001`; it is reclaimed with `chown -hR 0:0` through a helper container only after the provider exits (around 733). Under rootless Docker container uid 10001 is host uid 110000. Anything Claude creates during the run is owned by 110000 with Claude's own modes, so a `0700` directory or `0600` file is unreadable to the runner's host user until reclaim.
- `.cc-writes` (planner inspection of `/opt/agent-tools/node_modules/@anthropic-ai/claude-code/bin/claude.exe` in the job image): Claude 2.1.299 defines `FU=".cc-writes"` and uses it as `stagingDir: join(dirname(settingsFile), FU)` for atomic settings writes, and builds staging paths `join(<dir>, ".claude", FU)` for the project, the home directory and the local settings directory. The directory mode is **not confirmed** (flagged uncertainty); step 0 measures it.
- Check execution: `platform_run_check` → `checkResults` → `resolveAgentResult` (the snapshot) → `runCheckRequest` → `runInCheckWorkspace` → install → `executeConfiguredCheck` → `runWorkspaceCommand` → `runWorkspaceTask` (`internal/daemon/build.go:309`). `runWorkspaceTask` is called only for checks (`build.go:303`) and check installs (`check_install.go:348`). Under Docker it builds `commandPolicy` (`build.go:281`) with `Image: jr.d.cfg.JobImage`, a read-only root, `/tmp` as a `noexec` tmpfs, a fresh `/home/runner` bind, `--cap-drop ALL`, `no-new-privileges`, and network none or the restricted network. The container environment has only explicit `-e` entries, so the image's own `ENV` values apply.
- `CheckConfig{Command, Args, TimeoutSeconds, RepositoryID, DependencyDirectories, Install}` (`internal/config/config.go:76`), validated by `config.ValidateCheck` (`internal/config/check_validation.go:5`). In state-dir mode checks come only from `runner.local.yaml`, are read once at start and need a restart to change (`runner-local-checks.md` REQ-02, REQ-03). `job_image` comes from the remote document and must match `immutableImagePattern` (`config.go:524`, `^[^\s@]+@sha256:[a-fA-F0-9]{64}$`).
- Provenance: `internal/daemon/check_install_provenance.go`, schema `our-runner.check-install.v1`, exact keys `schema, job_id, attempt_id, check_id, repository_id, commit_sha, isolation, hermetic, carried_directories, install, result`. In the app repository the string `check-install` appears only in tests and test support (`src/lib/prisma/project-setup-provider-check.integration.test.ts`, `src/server/runner-artifacts/services/go-artifact.integration.test.ts`, `src/server/planning-manifests/test-support/*.go`); no production code parses the schema (planner grep, 9 November 2026; the implementer re-runs it).
- Job image `packaging/job-image/Dockerfile`: `node:22-bookworm-slim` with git, bubblewrap (setuid plus an unprivileged copy), socat, ripgrep, Claude Code 2.1.299, sandbox-runtime 0.0.69, Codex, OpenCode, npm 12.0.2. No Go.

**What exists versus what is only described.** Exists: the skip map, hand-off and reclaim, the check task path, local checks, the v1 provenance artifact, the provider CLI start gate and doctor rows. Described only: a toolchain layer (PRODUCT composition item 2), any per-check image, any harness-state exclusion, any tool-named hook evidence.

## Scope

**In scope (app-runner only):**

1. FD-1: exclude a fixed, runner-owned list of Claude harness-state paths from every agent snapshot, and give each snapshot refusal an accurate message.
2. FD-2: a host-declared, digest-pinned `image` per local check, used for that check's install and run under Docker; start gate, doctor row, provenance v2; a reviewed reference Go check image under `packaging/check-image-go/`.
3. README and `docs/TESTING.md` updates; gated Docker tests.
4. The live verification plan for the coordinator, including RJIC REQ-12 closure by container inspection.

**Out of scope:**

- FD-3 protocol change (tool names in hook events). See D-4.
- Adding Go or any toolchain to the job image, or giving the agent container a toolchain (FU-2).
- A general read grant for every private file the agent creates during the run (FU-1). Only the named harness paths are excluded; other unreadable entries get an accurate refusal (REQ-02).
- Any app repository change. The coordinator links this record from app `docs/implementation-progress.md` and the FR-01 section.
- Remote-document or dashboard support for check images. The control plane must not choose them (D-2).
- Non-Go reference images. The mechanism is ecosystem-neutral; only Go gets a reference image now.
- KAN-10, KAN-14, KAN-16, existing PRs, ESM Docker and Coolify.

**Repositories touched:** app-runner (all code, tests, packaging, README, TESTING, this record). App: none by the implementer; the coordinator updates its own progress records.

## Acceptance contract

| ID | Source | Requirement (observable) | Evidence |
| -- | ------ | ------------------------ | -------- |
| REQ-01 | FD-1, PRODUCT isolation baseline | **Root cause confirmed before the fix.** Step 0 reproduces the refusal on the rootless daemon and records: the exact error returned by the failing call (which of the five sites), and the owner, mode and lifetime of `.claude/.cc-writes` when Claude 2.1.299 writes a project settings file inside the job image under the job flags. The record states which hypothesis holds: H1, the directory is created `0700` by uid 10001 and `OpenRoot` fails with permission denied during hand-off; or H2, the directory appears and disappears between `list` and `Lstat`/`OpenRoot`. If neither holds, the implementer stops and reports before writing REQ-02/REQ-03 code. | Gated reproduction test output and a step-0 note in the implementation report |
| REQ-02 | FD-1 | **Accurate refusal messages.** Each of the five refusal sites in `agent_result_copy.go` returns a message naming its cause: an entry that cannot be read because of permissions says "the checkout has an entry the runner cannot read at X"; an entry that vanished or changed type or identity keeps "the checkout changed while it was read at X"; a failed `Readlink` and a copied file whose content or size changed keep "changed while it was read". X is the existing `displayEntry` value (workspace-relative, no host path). Messages stay within the current tool-result limits. | Unit tests per site with an injected error (`fs.ErrPermission`, `fs.ErrNotExist`, identity mismatch) |
| REQ-03 | FD-1 | **Harness state excluded.** A runner-owned list in a new file `internal/daemon/agent_harness_state.go` names `.claude/.cc-writes` (plus any other path proven in step 0, each with its evidence). For every candidate root, a listed path is added to the snapshot `skip` set **only when it is absent from the start tree** (the candidate's base commit tree); when it is tracked at the base commit it is copied and compared like any other entry. The exclusion applies to iterative snapshots (during the agent run) and to the terminal snapshot. A listed path never appears in a published change set, a patch artifact or the check workspace. Paths outside the list are never skipped. Cardinality: zero, one and two candidate roots; the path present, absent, tracked, and as a symlink (a symlink at a listed path is skipped when untracked, never followed). | Unit tests in `agent_result_copy_test.go` / `agent_result_resolution_test.go`; terminal change-set test with the directory present |
| REQ-04 | FD-1 | **Rootless hand-off regression.** A gated test (`RUN_DOCKER_SANDBOX_PROBE=1`) hands a workspace to the job user with the production hand-off, creates from inside a job-image container as uid 10001 both `.claude/.cc-writes/` (mode `0700`, one file inside) and `other/` (mode `0700`), then takes an iterative snapshot from the host. With only `.claude/.cc-writes` present the snapshot succeeds and contains no `.claude/.cc-writes` entry. With `other/` present it is refused with the REQ-02 "cannot read" message naming `other`. The workspace is reclaimed and removed afterwards; no containers with the test labels remain. | Gated test output pasted in this record |
| REQ-05 | FD-2, LC-1, LC-2 | **Host-declared check image.** `CheckConfig` gains `Image string \`yaml:"image,omitempty"\``. `ValidateCheck` accepts an empty value or one matching `immutableImagePattern`, and refuses anything else with "check <id>: image must be pinned by digest (name@sha256:<64 hex>)". It is read only from the host's local check sources (`runner.local.yaml`, or `config.yaml` in config-file mode). A remote configuration document that carries a check, or a check image, is still refused or ignored exactly as `runner-local-checks.md` REQ-02 specifies today; no remote field is added. A change to `image` is restart-required like every other check field (existing hot-reload test extended: a reloaded document never changes a running daemon's check images). Under direct isolation a configured `image` is unused, and start logs one warning per check naming the check ID. | `config` unit tests (valid, tag-only, empty, whitespace, uppercase hex accepted by the pattern); `remoteconfig` test; reload test; direct-mode warning test |
| REQ-06 | FD-2, PRODUCT composition item 2, MCP enforcement | **Check image used for that check only.** Under Docker, a check with `image` runs both its install step (if configured) and its command in that image, for iterative `platform_run_check` runs and the terminal required run alike. Every other `isolation.Policy` field is identical to the job-image case (user, read-only root, tmpfs, home bind, caps, `no-new-privileges`, network choice, CPU, memory, PIDs, labels). A check without `image` keeps `jr.d.cfg.JobImage` (current behaviour, byte-identical argv). The provider container, the ownership helper, the probe containers and the proposal adapter keep the job image. `platform_run_check` input is unchanged: an extra field such as `image` is refused by the existing strict schema. | Daemon unit tests asserting the policy per task (image set, unset, install plus run, iterative plus terminal); MCP tool schema test |
| REQ-07 | FD-2 | **Start gate and doctor.** Under Docker, daemon start inspects each distinct configured check image by its pinned reference on the configured Docker host. A missing image stops start with "check <id>: image <ref> is not present on the runner's Docker host; pull or build it before starting" before the daemon connects. `our-runner doctor` adds a `check-images` row: OK listing the check IDs and short digests, Fail naming each missing image, Skip under direct isolation or when no check declares an image. No network pull is attempted by the runner. | Daemon start tests with an inspection seam; doctor tests |
| REQ-08 | FD-2, runner-hermetic-check-workspace REQ-12 | **Provenance v2.** The artifact schema becomes `our-runner.check-install.v2` with one added key `check_image` placed after `hermetic`. Under Docker it is the `sha256:<64 hex>` digest of the image the check ran in (the check image, or the job image when no check image is set); under direct isolation it is `null`. It never contains a registry, name or tag. All other keys and rules from FR-11 REQ-12 are unchanged. The implementer re-runs the app grep and records that no app production code parses v1; if any does, stop and report. | `check_install_provenance_test.go` exact JSON for direct, Docker with check image, Docker without check image |
| REQ-09 | FD-2 | **Reference Go check image.** New `packaging/check-image-go/Dockerfile` and `build.sh`, following `packaging/job-image/build.sh` conventions: `FROM golang:<version>-bookworm@sha256:<digest>`, where the version is the newest Go release published at least two weeks before implementation and not below the `go` directive of `OMKE/shortstack-e2e`'s `go.mod` (both recorded with evidence); user `runner` uid and gid `10001`; `ENV GOTOOLCHAIN=local GOPATH=/home/runner/go GOTMPDIR=/home/runner GOFLAGS=-modcacherw`; no setuid or setgid files (build asserts `find / -xdev -type f -perm /6000` prints nothing); `go version` assertion in the build. `build.sh` prints the local image ID. No secret, no network at check time. | Build output pasted in this record |
| REQ-10 | FD-2 | **Go test passes under the real policy.** A gated test (`RUN_DOCKER_SANDBOX_PROBE=1`, `CHECK_IMAGE=<ref>`) runs `go test ./...` on a small fixture module through the production `runWorkspaceTask` path (or the exact `isolation.Policy` it builds) with network none, read-only root, noexec `/tmp`, uid 10001 and a fresh home, and asserts exit 0 and a `ok` line. A second case with a failing test asserts `exit_nonzero`. A third case with the job image asserts `go: not found` behaviour is unchanged when no `image` is set. | Gated test output pasted in this record |
| REQ-11 | Security review | **Security properties.** The control plane, the ticket and the agent cannot choose or change a check image (REQ-05, REQ-06 tests). Logs and tool results never print a host path or the image's registry name beyond what the operator configured in local logs; control-plane-bound data carries only the digest (REQ-08). The check container keeps every current restriction (REQ-06). Excluded harness paths cannot be used to smuggle content: an untracked listed path is dropped, never copied (REQ-03). | Tests above plus reviewer diff review |
| REQ-12 | AGENTS.md | **Docs.** README documents `checks.<id>.image`, the start gate, the doctor row, the reference Go image and its build command, the harness-state list, provenance v2, and the direct-mode warning. `docs/TESTING.md` lists the new gated tests and `CHECK_IMAGE`. No code comments (repository owner rule). | Diff review |
| REQ-13 | AGENTS.md | **Checks.** `gofmt -l .` prints nothing; `go build ./...`, `go vet ./...`, `go test ./... -count=1` pass; `go test -race ./internal/daemon/ ./internal/config/ ./internal/doctor/ ./internal/isolation/ -count=1` passes; statement coverage of each new or changed Go file is at least 80 %; `gosec` and `govulncheck` at the README/TESTING versions pass or each failure is shown to predate `fc4ae6a`; `staticcheck` recorded as run or "not installed". Gated tests REQ-04 and REQ-10 pass on `DOCKER_HOST=unix:///run/user/1000/docker.sock`, and the existing `TestJobImageSandboxRuntimeStartsUnderTheNestedSandboxFlags` and `TestAgentPackMountRefusesEveryWriteInsideTheContainer` still pass. | Command output pasted in this record |
| REQ-14 | FR-01, RJIC REQ-12 | **Live Docker Build (coordinator).** The steps under "Coordinator live steps" run in order, each with command, output excerpt and UTC time pasted here. Pass: (a) a fresh KAN ticket on `OMKE/shortstack-e2e` with a small Go change reaches **Ready for review**; (b) at least one iterative `platform_run_check` for `go-test` returns `passed` during the agent run (runner log or agent report) and no refusal mentions `.cc-writes`; (c) the terminal `test_results` for `go-test` is `passed` with `commit_sha` equal to the pushed head; (d) `runner-check-install-go-test.json` has schema v2, `isolation: "docker"`, `hermetic: true`, `check_image` equal to the Go check image digest; (e) `docker inspect` of the check container shows the check image digest and user `10001:10001`, and of the provider container shows the job image; (f) the pushed tree contains no `.claude/` entry; (g) a `docker top` sampler on the provider container captures `/usr/local/lib/our-runner/unprivileged-bwrap/bwrap` running the ticket's shell command, and the agent reports that command's output: this closes RJIC REQ-12. Any other failure is recorded as a new finding and REQ-14 stays open. | Manual live run |

**Inapplicable dimensions.** UI, browser screenshots, accessibility and prototype matching: no app or UI change (the dashboard is used as is in REQ-14). Persistence and migrations: none. Tenant permission: the runner is single-tenant; the check image is a host fact. Cardinality is covered in REQ-03 (candidate roots, path states), REQ-06 (install plus run, iterative plus terminal, image set or unset) and REQ-07 (zero, one, several distinct images).

## Design and decisions

### D-1 FD-1 fix: exact harness-state exclusion plus accurate messages

- Options considered: (a) grant the runner read access to everything the agent creates during the run (for example a periodic group re-grant, or a default ACL); (b) take iterative snapshots through a helper container running as the job user; (c) skip a fixed list of harness paths and make other refusals accurate.
- Decision: (c). The refused path is Claude's own settings staging directory, which is not work product and must not be published anyway (the terminal commit already left `.claude/` out). An exact list, applied only when the path is untracked at the base commit, keeps the copy fail-closed for everything else. (a) needs ACL support or a second privileged loop that races the agent; (b) moves the trusted read into a container the agent's user owns, which weakens "separate trusted evidence collection".
- Tradeoff: an agent that writes a private-mode file anywhere else still gets a refusal during the run. The message now says why, and the terminal path after reclaim still works. Recorded as FU-1.
- Uncertainty: H1 versus H2 is not yet proven (REQ-01). Under H2 the exclusion still fixes the symptom; REQ-02 then only clarifies messages.

### D-2 FD-2 decision: a host-declared, digest-pinned image per check

- Options considered: (a) add Go to the job image; (b) a host-declared check image per check, `checks.<id>.image`; (c) mount a host toolchain directory into the check container; (d) a control-plane-selected toolchain image in the remote document.
- Decision: (b).
- Reasons, grounded in the owner decisions:
  - LC-1 and LC-2 keep the whole check definition on the runner host and forbid the control plane from defining check commands. The executable a check runs is part of its definition. `job_image` is chosen by the control plane (remote document, dashboard revision), so (a) would let a control-plane revision decide which `go` binary every required check executes. (d) does the same explicitly. Only (b) and (c) keep the toolchain a host decision.
  - PRODUCT "Workspace composition" item 2 asks for a pinned project toolchain layer, separate from the agent tooling. (b) is that layer, pinned by digest like the job image.
  - The isolation baseline separates trusted evidence collection from the agent's environment. With (b) the image that produces required-check evidence is not the image the agent runs in, so nothing the agent's image contains (or a future job image change) changes the evidence toolchain.
  - (c) bind-mounts host files into an untrusted-code container, depends on the host's libc and layout, and is not reproducible across hosts. (a) also grows the job image per ecosystem and forces a job-image rebuild, a remote revision and a restart for every toolchain bump.
- Tradeoffs: the agent itself still has no Go; it relies on `platform_run_check`, which D-1 unblocks. The operator must build or pull the check image before start (REQ-07 makes this explicit). FU-2 covers toolchains inside the agent container.
- Default: no `image` keeps today's behaviour, so existing direct-mode and Node checks are unchanged.

### D-3 Provenance carries only the digest

- The registry and name are operator-local and can reveal host naming; the digest is what identifies the bytes. A schema bump to v2 is required because FR-11 REQ-12 fixes the key set exactly.

### D-4 FD-3 stays out of this slice

- A minimal FD-3 fix would add a tool name to hook events. The app's runner-protocol schemas are strict, so this needs a protocol version or field change in both repositories, app tests and a coordinated rollout; it is not minimal and does not unblock Build.
- RJIC REQ-12 needs one observation that Bash ran successfully under the weaker sandbox. REQ-14(g) gets it by container inspection on the live run: a sampler catches the unprivileged `bwrap` running the ticket's shell command, and the agent reports the command's output. Tool-named hook evidence is FU-3, a cross-repository protocol slice.

### D-5 Reference image environment

- `/tmp` is `noexec` and `go test` executes test binaries from its temporary directory, so `GOTMPDIR` points at the writable home. The root filesystem is read-only, so `GOPATH` and the module cache live under `/home/runner`; `GOFLAGS=-modcacherw` keeps the cache removable by the reclaim. `GOTOOLCHAIN=local` stops any toolchain download attempt (network is none). These are image `ENV` values because the runner passes only explicit `-e` entries.

## Implementation plan

Owned files (app-runner): `internal/daemon/agent_result_copy.go` and test, `internal/daemon/agent_result_resolution.go` and test, new `internal/daemon/agent_harness_state.go` and test, `internal/config/config.go`, `internal/config/check_validation.go` and tests, `internal/remoteconfig` tests (and code only if the refusal path needs the new field named), `internal/daemon/build.go`, `internal/daemon/check_install.go`, `internal/daemon/check_install_provenance.go` and test, new `internal/daemon/check_image_gate.go` and test, `internal/daemon/daemon.go` (gate call and direct-mode warning), `internal/doctor/doctor.go` and test, new gated test files under `internal/daemon/` or `internal/isolation/`, new `packaging/check-image-go/Dockerfile` and `build.sh`, `README.md`, `docs/TESTING.md`, this record. Do not touch `bin/`, `~/.shortstack-runner`, `runner.local.yaml`, the running runner, the job image, host Docker settings or any app file.

0. [x] REQ-01 first, no production code yet. On the rootless daemon, in the job image under the job flags, run Claude 2.1.299 offline so that it writes a project settings file (for example via a settings-writing command that needs no credentials; if none exists, record that and use `strace`-free evidence such as a wrapper that lists `.claude/` from a second container while Claude starts). Record owner, mode and lifetime of `.claude/.cc-writes`, then reproduce the host-side snapshot refusal and record which call fails. Write the step-0 note. Stop if neither H1 nor H2 holds.
1. [x] REQ-02 tests first (injected errors per site), then the message split.
2. [x] REQ-03 tests first, then `agent_harness_state.go` and the skip-set wiring for iterative and terminal snapshots.
3. [x] REQ-04 gated test.
4. [x] REQ-05 tests first, then the config field, validation, reload and warning.
5. [x] REQ-06 tests first, then carry the check image through `workspaceTask` into `commandPolicy` (one added parameter or field; no other caller changes).
6. [x] REQ-07 tests first with an inspection seam (`var inspectCheckImage = …`, like `probeProviderCLI`), then the gate and the doctor row.
7. [x] REQ-08 tests first, then provenance v2 and the app grep.
8. [x] REQ-09: packaging files; build on the rootless daemon; record version, base digest and image ID.
9. [x] REQ-10 gated test against the built image.
10. [x] REQ-12 docs, REQ-13 checks, implementation report, status `review_ready`.

## Verification plan and results

| Requirement | Planned evidence | Result |
| ----------- | ---------------- | ------ |
| REQ-01 | Step-0 note with reproduction output | implemented, H1 holds; see step-0 note (awaiting review) |
| REQ-02 | Unit tests | implemented; `agent_result_copy_refusal_test.go` (9 tests, one or more per site) pass (awaiting review) |
| REQ-03 | Unit tests | implemented; `agent_harness_state_test.go` (12 tests) pass (awaiting review) |
| REQ-04 | Gated test output | implemented; `TestIterativeSnapshotSkipsHarnessStateDuringARootlessHandOff` PASS 0.94 s on the rootless daemon (awaiting review) |
| REQ-05 | Unit tests | implemented; `check_image_config_test.go`, `local_check_image_test.go`, direct-mode warning test pass (awaiting review) |
| REQ-06 | Unit tests | implemented; `check_image_policy_test.go` (4 tests) pass (awaiting review) |
| REQ-07 | Unit tests | implemented; `check_image_gate_test.go`, `check_image_check_test.go`, `image_inspect_test.go`, `doctor_check_images_test.go` pass (awaiting review) |
| REQ-08 | Unit tests and app grep | implemented; exact JSON tests pass; app grep: no production parser, one test-support driver expects v1 (awaiting review) |
| REQ-09 | Build output | implemented; image ID `sha256:59201f8899824429f7a9eb8621f82f9df946580884df6e49380db89795451813` (awaiting review) |
| REQ-10 | Gated test output | implemented; `TestGoCheckImageRunsGoTestUnderTheProductionPolicy` PASS 11.10 s, 4 subtests (awaiting review) |
| REQ-11 | Tests and diff review | implemented; see security notes (awaiting review) |
| REQ-12 | Diff review | implemented; README and `docs/TESTING.md` (awaiting review) |
| REQ-13 | Command output | implemented; see command evidence (awaiting review) |
| REQ-14 | Coordinator live evidence | pending |

Reviewer verdicts (9 November 2026) supersede the "awaiting review" notes above: REQ-01 to REQ-11 implemented, REQ-12 partial (RV-2), REQ-13 partial (RV-1), REQ-14 unverified (live, coordinator). See "Reviewer requirement review". The re-review after repair attempt 1 supersedes REQ-12 and REQ-13: both implemented. REQ-01 to REQ-13 are implemented; REQ-14 stays unverified (live, coordinator).

## Coordinator live steps (REQ-14)

The coordinator owns every step below; the implementer and reviewer do none of them. All Docker commands use `DOCKER_HOST=unix:///run/user/1000/docker.sock`. Start only after the review verifies REQ-01 to REQ-13.

1. **Build the check image** from the reviewed tree: `sh packaging/check-image-go/build.sh our-runner-check-go:rdbc`. Record `sha256:<CHECK>`; the reference is `our-runner-check-go:rdbc@sha256:<CHECK>`.
2. **Build and swap the runner binary**: `go build -o <scratchpad>/our-runner-rdbc ./cmd/our-runner`, stop the live runner, keep the current `bin/our-runner` as a backup in the scratchpad, replace it.
3. **Config change (owner-approved, coordinator only):** add `image: our-runner-check-go:rdbc@sha256:<CHECK>` to `checks.go-test` in `~/.shortstack-runner/runner.local.yaml`. Nothing else in that file changes. This takes effect only on the restart in step 6.
4. **Isolation revision:** if the current dashboard revision is still Docker (revision 5, `our-runner-job:rjic@sha256:a2c8cabe…`), keep it. If direct isolation was restored after FR-01, save a new Docker revision with the same values as revision 5 and run `bin/our-runner configure --gateway wss://localhost:7443/v1/runners/stream --state-dir ~/.shortstack-runner --non-interactive` (LC-3: isolation is restart-required; `acknowledge_direct` does not apply to Docker).
5. **Doctor:** `bin/our-runner doctor --state-dir ~/.shortstack-runner`; expect `check-images` OK naming `go-test`, `job-image-provider-cli` OK, `docker-user-mapping` OK rootless.
6. **Start:** `bin/our-runner start --state-dir ~/.shortstack-runner`; expect "connected to control plane" and no check-image refusal.
7. **Samplers:** start a loop every 0.5 s recording `docker ps --filter label=com.ourplatform.runner-id=<runner id>` with image and user, and for each provider container `docker top <id> -eo pid,user,args`, grepping for `unprivileged-bwrap/bwrap`. Save output to the scratchpad.
8. **Ticket:** create a fresh KAN ticket for `OMKE/shortstack-e2e` with a small Go change (a new function plus its test). Acceptance criteria include "Before finishing, run the shell command `sleep 20 && git status --short` once and include its output in your report" and "Run the go-test check through the platform before finishing". Do not use KAN-10, KAN-14 or KAN-16.
9. **Plan and Build:** let planning publish a plan, have the owner approve it and authorise Build. Record attempt IDs, container IDs, `docker inspect` `.Config.Image` and `.Config.User` for the provider and each check container, the runner log lines for `platform_run_check`, the terminal `test_results`, the provenance artifact content, the pushed head and its tree listing, the sampler lines showing `bwrap`, and the agent's report of the shell output. Confirm Ready for review in the app.
10. **Afterwards (optional):** restore direct isolation with the steps in the app FR-01 section (new direct revision, `our-runner configure --acknowledge-direct`, restart). The check image stays configured; under direct it is unused and logged as a warning (REQ-05).
11. Update RJIC REQ-12 in `runner-job-image-claude-cli.md` with a link to this evidence, and the FR-01 section and `docs/implementation-progress.md` in the app repository.

### Live evidence (REQ-14)

Not started.

## Implementation report

Implementer: develop-implementer, 9 November 2026. Status set to `review_ready`; REQ-14 is the coordinator's.

### Step-0 note (REQ-01): H1 holds

- Claude 2.1.299 in `our-runner-job:rjic@sha256:a2c8cabe…` names its settings staging directory `.cc-writes` (`var FU=".cc-writes"` in the bundled CLI, scratchpad extract `rdbc/ccwrites.txt`).
- `TestClaudeCreatesAPrivateStagingDirectoryInTheHandedOffWorkspace` (gated, `internal/isolation/claude_staging_dir_integration_test.go`, PASS 4.15 s) runs the image's real `claude` offline under the job flags in a workspace handed off by the production rootless hand-off. Claude creates `<cwd>/.claude/.cc-writes` as a directory with mode 0700, owned by uid 10001 in the container (host uid 110000). It exists for the whole run and stays after the provider exits, until reclaim. From the host, `os.Root.Lstat(".cc-writes")` succeeds and `OpenRoot(".cc-writes")` fails with `fs.ErrPermission`.
- The failing call was the subdirectory `OpenRoot` site in `copyCandidateTree` (`agent_result_copy.go` line 284 at the baseline). It mapped every error to "changed while it was read".
- Pre-fix reproduction: `scratchpad/rdbc/prefix-repro.sh` runs the REQ-04 test body against an export of `main` before this slice. It fails with "agent result refused in repository repo-1: the checkout repositories/repo-1 changed while it was read at .claude/.cc-writes", which is the FR-01 FD-1 symptom.
- No second harness path was observed, so the list is only `.claude/.cc-writes`. H2 (a race between list and open) was not observed: the directory persists.

### Changed paths

- FD-1: `internal/daemon/copy_target.go` (`errCopySourceUnreadable`), `internal/daemon/agent_result_copy.go` (`readRefusal`, the message split at each site), `internal/daemon/agent_harness_state.go` (new: `agentHarnessState`, untracked-at-base detection through `ls-tree`, `snapshotSkips`), `internal/daemon/agent_result_resolution.go` (skip-set wiring for iterative and terminal snapshots).
- FD-2 config: `internal/config/config.go` (`CheckConfig.Image`), `internal/config/check_validation.go` (digest-pin validation).
- FD-2 execution: `internal/daemon/build.go` (`workspaceTask.image` sets `policy.Image` only when non-empty), `internal/daemon/checkexec.go` (the check command and `checkReport.checkImage`), `internal/daemon/check_install.go` (the install step).
- FD-2 gate and doctor: `internal/isolation/image_inspect.go` (new: `InspectImage`, `ErrImageNotPresent`, `ImageDigest`), `internal/daemon/check_image_gate.go` (new), `internal/daemon/daemon.go` (gate call under Docker, warning under direct), `internal/doctor/check_image_check.go` (new), `internal/doctor/doctor.go` (`Skip` status, `Options.CheckImages`, the row), `cmd/our-runner/doctor_command.go` (passes the declared check images to doctor).
- FD-2 provenance: `internal/daemon/check_install_provenance.go` (v2, `check_image`).
- Packaging: `packaging/check-image-go/Dockerfile`, `packaging/check-image-go/build.sh` (new).
- Tests: `internal/daemon/agent_result_copy_refusal_test.go`, `agent_harness_state_test.go`, `agent_harness_state_rootless_integration_test.go` (gated), `check_image_gate_test.go`, `check_image_policy_test.go`, `check_image_go_integration_test.go` (gated), `check_install_provenance_test.go` (changed); `internal/config/check_image_config_test.go`; `internal/remoteconfig/local_check_image_test.go`; `internal/doctor/check_image_check_test.go`, `doctor_test.go` (changed: 13 rows); `internal/isolation/image_inspect_test.go`, `claude_staging_dir_integration_test.go` (gated); `cmd/our-runner/doctor_check_images_test.go`.
- Docs: `README.md`, `docs/TESTING.md`, this record.

### Decisions

- **I-1 Doctor `Skip` status.** The contract asks for a Skip row. Doctor had only OK, Warn and Fail, so `Skip` is new, prints `SKIP`, and does not fail the report (`TestSkipIsReportedWithoutFailingTheReport`).
- **I-2 cmd wiring outside the owned files.** `cmd/our-runner/doctor_command.go` passes the checks that declare an image to `doctor.Options.CheckImages`. Without this the doctor row would always skip. It is a one-function addition with its own test.
- **I-3 File permission failures.** `copyRegular` also maps a permission failure on opening a file to "cannot read", so a private file reports the same cause as a private directory. Vanished or replaced entries keep "changed while it was read".
- **I-4 Provenance digest source.** `check_image` is taken from the configured pinned reference (`ImageDigest`), not from a fresh inspection. The start gate has already proven that this reference resolves on the host, and the container is started by that same reference. A job image that is not digest-pinned yields `null` (direct-mode equivalent), never a name or tag.
- **I-5 Inspection reuses `runDockerProbe`.** No new gosec annotation was needed. `InspectImage` validates the reference before Docker runs, passes `--` before it, and accepts only `sha256:<64 hex>` output. It never pulls.
- **I-6 Direct-mode warning.** It names only the check ID, never the reference, so host naming stays out of logs that may be shared.
- **I-7 REQ-10 `ok` line.** A passed `platform_run_check` result carries no output, so a passing `go test ./...` cannot show its `ok` line through the production path. A separate subtest runs `/bin/sh -c "go test ./... && exit 3"` in the same image and policy and asserts the `ok  \texample.test/fixture` line in the failed output. The plain `go test ./...` subtest asserts `passed`. The fixture test itself asserts uid and gid 10001, a read-only root, a noexec `/tmp` and no network, so `passed` proves those properties.
- **I-8 Job-image behaviour without a check image.** FD-2 says "`go` not found". The real output of `go test ./...` in the job image is `Cannot find module '…/go'` with `exit_nonzero`. The `node` image entrypoint prefixes `node` when the first argument is not a command on `PATH`. The REQ-10 third case asserts that real, unchanged behaviour. The reference Go image has no entrypoint, so its argv runs directly.
- **I-9 Image reference format.** The image built on the rootless daemon (containerd image store) reports an ID equal to its manifest-list digest, and `docker image inspect` and `docker run` both resolve `our-runner-check-go:rdbc@sha256:<ID>` locally without a registry. This was verified with the built image (below). On the classic store without containerd, a locally built and never-pushed image may not resolve by `name@digest`. This is not tested here, so it is flagged as an uncertainty for other hosts.
- **I-10 `--pull=never` for check images (repair 1, RV-2 coordinator decision).** `isolation.Policy.PullNever` adds `--pull=never` immediately before the image in the Docker argv. Only `runWorkspaceTask` sets it, and only when the task carries a configured check image, so the check command and its install step both use it. Providers, consultation and generation containers, the sandbox probe, and the probe, user-mapping and ownership containers (which build their own argv) never set it. A check without an image keeps its exact pre-slice argv. An image removed after start now fails that check instead of letting Docker pull it.

### Go version and base image (REQ-09)

- go.dev/dl lists stable go1.27.3, go1.27.2, go1.27.1, go1.27.0 and go1.26.10 as the newest. The go.dev/doc/devel/release page dates go1.27.3 to 2026-10-19, which is 21 days before implementation (2026-11-09) and so at least two weeks. It is the newest release that qualifies.
- `OMKE/shortstack-e2e` `go.mod` (read with `gh api`): `module github.com/omke/shortstack-e2e`, `go 1.25`, no requirements. 1.27.3 is not below it, and no module download is needed at check time.
- Base image: `golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0`, pulled on the rootless daemon, linux/amd64.

Build output (`DOCKER_HOST=unix:///run/user/1000/docker.sock sh packaging/check-image-go/build.sh our-runner-check-go:rdbc`, tail):

```text
#5 [2/3] RUN groupadd --gid 10001 runner && useradd --create-home --uid 10001 --gid 10001 runner && find / -xdev -type f -perm /6000 -exec chmod a-s {} + && test -z "$(find / -xdev -type f -perm /6000)" && test "$(id -u runner):$(id -g runner)" = "10001:10001" && go version | grep -Eq '^go version go1\.27\.3 linux/(amd64|arm64)$'
#5 DONE 0.7s
#7 exporting manifest list sha256:59201f8899824429f7a9eb8621f82f9df946580884df6e49380db89795451813 done
#7 naming to docker.io/library/our-runner-check-go:rdbc done
image: our-runner-check-go:rdbc
platform: linux/amd64
toolchain: go version go1.27.3 linux/amd64
local image ID: sha256:59201f8899824429f7a9eb8621f82f9df946580884df6e49380db89795451813
pinned reference: our-runner-check-go:rdbc@sha256:59201f8899824429f7a9eb8621f82f9df946580884df6e49380db89795451813
```

`build.sh` asserts after the build, outside the Dockerfile, that `go version` matches as uid 10001 under `--network none --read-only` and that `find / -xdev -type f -perm /6000` as root prints nothing. Checks against the pinned reference:

```text
$ docker image inspect --format '{{.Id}} {{.Config.User}}' -- our-runner-check-go:rdbc@sha256:59201f88…
sha256:59201f8899824429f7a9eb8621f82f9df946580884df6e49380db89795451813 runner
$ docker run --rm --network none --read-only --user 10001:10001 --tmpfs /tmp:rw,noexec,nosuid our-runner-check-go:rdbc@sha256:59201f88… go env GOTOOLCHAIN GOPATH GOTMPDIR GOFLAGS
local
/home/runner/go
/home/runner
-modcacherw
```

The coordinator rebuilds from the reviewed tree in REQ-14 step 1. A rebuild on the same base and Dockerfile may produce a different ID (timestamps), so the coordinator must use the ID it prints.

### REQ-08 app grep

`grep -rn "check-install" <app>/src` (app checkout, read only): no production code parses the provenance schema. One test-support driver does: `src/server/planning-manifests/test-support/go-build-upload-evidence.go:24` checks `document.Schema != "our-runner.check-install.v1"`, and its `go-planning-driver_test.go` uses v1. It runs under `run-go-planning-driver.test.ts`. This is test support, not production, so the slice did not stop. Follow-up FU-5: the app test support must accept v2 before a v2 artifact reaches that driver. The implementer may not change app files.

### Security notes (REQ-11)

- Who chooses the image: only the host's local check source. `ParseDelivery` still refuses a remote `checks` key, and the new tests add a remote check with `image`, a top-level `check_image` and a `check_images` map, all refused. `platform_run_check` refuses an `image` field through the strict schema (`TestIterativeAndTerminalRunsOfACheckUseItsImage`, `TestRunCheckRejectsArbitraryFieldsAndOverrides`). A hot reload keeps the start-time image (`TestReloadNeverChangesTheRunningCheckImages`).
- Pinning: tag-only, name-only, short or long, sha512, non-hex, whitespace and double-digest references are refused at load. The start gate inspects by the pinned reference and never pulls, and a missing image stops start before the daemon connects.
- Container restrictions: `check_image_policy_test.go` proves that with an image set, the normalized argv differs from the job-image argv only in the image, keeping user, read-only root, tmpfs, home bind, dropped capabilities, `no-new-privileges`, network, CPU, memory, PIDs and labels. A check without an image produces the exact pre-slice argv. The provider, ownership, probe and proposal containers do not read `workspaceTask.image`.
- Disclosure: provenance carries only `sha256:<64 hex>`, with exact JSON tests that the registry, name and tag never appear. The direct-mode warning names only the check ID. The gate and doctor messages include the configured reference, and only in local output.
- Harness exclusion: a listed path is skipped only when the base commit does not track it, and it is dropped, never copied. A symlink at the path is not followed. A tracked listed path is read and compared, and an unreadable tracked one is refused. Paths outside the list are never skipped.
- Reference image: no secret, no `COPY` from the host, no setuid or setgid files (asserted twice), non-root user 10001, `GOTOOLCHAIN=local` so no toolchain download is attempted.
- Existing behaviour noted, not changed: a failed check's output can include the check workspace's absolute host path (the job image's `Cannot find module '/tmp/…/job-results/<job>.checks/…/go'`). The check directory is bind-mounted at its host path. This predates the slice and is recorded as FU-6.

### Command evidence (REQ-13)

| Command | Result |
| ------- | ------ |
| `gofmt -l .` | prints nothing |
| `go build ./...` | ok |
| `go vet ./...` | ok |
| `go test ./... -count=1` | 5190 passed in 46 packages, exit 0 |
| `go test -race ./internal/daemon/ ./internal/config/ ./internal/doctor/ ./internal/isolation/ -count=1` | 2161 passed in 4 packages, exit 0 |
| `sh scripts/check.sh vuln` (pinned govulncheck) | "Your code is affected by 0 vulnerabilities" |
| `sh scripts/check.sh sec` (pinned gosec) | no findings, exit 0 |
| `staticcheck` | not installed |
| `sh -n packaging/check-image-go/build.sh` | ok |
| Gated, `DOCKER_HOST=unix:///run/user/1000/docker.sock RUN_DOCKER_SANDBOX_PROBE=1 JOB_IMAGE=our-runner-job:rjic@sha256:a2c8cabe… CHECK_IMAGE=our-runner-check-go:rdbc@sha256:59201f88…` | see below |

```text
--- PASS: TestIterativeSnapshotSkipsHarnessStateDuringARootlessHandOff (0.94s)
--- PASS: TestGoCheckImageRunsGoTestUnderTheProductionPolicy (11.10s)
    --- PASS: .../a_passing_module_passes_in_the_check_image (3.51s)
    --- PASS: .../go_test_prints_its_ok_line_in_the_check_image (3.39s)
    --- PASS: .../a_failing_test_fails_in_the_check_image (3.42s)
    --- PASS: .../the_job_image_still_has_no_Go_toolchain (0.77s)
ok  	github.com/example/app-runner/internal/daemon	12.057s
--- PASS: TestAgentPackMountRefusesEveryWriteInsideTheContainer (0.68s)
--- PASS: TestClaudeCreatesAPrivateStagingDirectoryInTheHandedOffWorkspace (4.15s)
--- PASS: TestJobImageSandboxRuntimeStartsUnderTheNestedSandboxFlags (2.35s)
ok  	github.com/example/app-runner/internal/isolation	7.193s
```

Statement coverage per new or changed Go file. The profile comes from `go test -coverprofile` over `./internal/daemon/ ./internal/config/ ./internal/doctor/ ./internal/isolation/ ./cmd/our-runner/`, aggregated per file with a scratchpad helper (`rdbc/filecov/main.go`):

| File | Coverage |
| ---- | -------- |
| `cmd/our-runner/doctor_command.go` | 100.0 % |
| `internal/config/check_validation.go` | 100.0 % |
| `internal/config/config.go` | 92.9 % |
| `internal/daemon/agent_harness_state.go` | 100.0 % |
| `internal/daemon/agent_result_copy.go` | 88.7 % |
| `internal/daemon/agent_result_resolution.go` | 90.2 % |
| `internal/daemon/build.go` | 92.2 % |
| `internal/daemon/check_image_gate.go` | 100.0 % |
| `internal/daemon/check_install.go` | 95.3 % |
| `internal/daemon/check_install_provenance.go` | 90.9 % |
| `internal/daemon/checkexec.go` | 97.4 % |
| `internal/daemon/copy_target.go` | 80.4 % after repair 1 (78.3 % before; baseline 77.8 %) |
| `internal/daemon/daemon.go` | 80.6 % after repair 1 (77.6 % before; baseline 77.3 %) |
| `internal/isolation/isolation.go` | 96.5 % after repair 1 |
| `internal/doctor/check_image_check.go` | 97.1 % |
| `internal/doctor/doctor.go` | 85.8 % |
| `internal/isolation/image_inspect.go` | 100.0 % |

`copy_target.go` and `daemon.go` were below 80 % before this slice. The baseline was measured on an export of `main` before the slice with the same command. Every block added by this slice in those two files has a non-zero count: `copy_target.go` 106–109, and `daemon.go` 306–313. The gap is in untouched code and is recorded as FU-7, not closed here.

### Candidate coverage

- Cardinality: zero, one and several check images (gate distinct-image test); install plus run and iterative plus terminal (policy tests); zero, one and two candidate roots and the path present, absent, tracked, unreadable and as a symlink (harness-state tests); every refusal site with permission, vanished and replaced errors (refusal tests).
- Not covered by an automated test: the classic (non-containerd) image store resolving a local `name@digest` (I-9), and a live Build (REQ-14).

### Notes for the coordinator (REQ-14)

- Use the pinned reference printed by the step-1 rebuild. The ID recorded above is only valid for the image built in this worktree.
- `doctor` should show `check-images  OK  go-test sha256:<12 hex>`.
- The app's `go-build-upload-evidence.go` test support expects v1 (FU-5). It does not affect the live run, but `run-go-planning-driver.test.ts` breaks once a v2 artifact reaches it.
- `OMKE/shortstack-e2e` has no module requirements, so `go test ./...` needs no network.

## Reviewer requirement review

Reviewer: fresh develop-reviewer (Opus 5.5), 9 November 2026. Reviewed head `a6208e43` (one commit over main `65e7310`) from the contract, `PRODUCT.md` ("Workspace composition" item 2, the isolation baseline, MCP enforcement), the owner decisions LC-1 to LC-4 in the app record `runner-hermetic-check-workspace.md`, the source, the tests and the rootless Docker daemon. No UI is involved, so the prototype and `https://app.shortstack.dev` do not apply (contract "Inapplicable dimensions").

### Commands re-run by the reviewer

| Command | Result |
| ------- | ------ |
| `gofmt -l .` | prints nothing |
| `go vet ./...`, `go build ./...` | ok |
| `go test ./... -count=1` | 5190 passed in 46 packages, exit 0 |
| `go test -race ./internal/daemon/ ./internal/config/ ./internal/doctor/ ./internal/isolation/ ./internal/remoteconfig/ ./cmd/our-runner/ -count=1` | 2433 passed in 6 packages, exit 0 |
| Gated, `DOCKER_HOST=unix:///run/user/1000/docker.sock RUN_DOCKER_SANDBOX_PROBE=1 JOB_IMAGE=our-runner-job:rjic@sha256:a2c8cabe… CHECK_IMAGE=our-runner-check-go:rdbc@sha256:59201f88…` | `TestIterativeSnapshotSkipsHarnessStateDuringARootlessHandOff` PASS 1.07 s; `TestGoCheckImageRunsGoTestUnderTheProductionPolicy` PASS 11.49 s (4 subtests); `TestAgentPackMountRefusesEveryWriteInsideTheContainer` PASS 0.67 s; `TestClaudeCreatesAPrivateStagingDirectoryInTheHandedOffWorkspace` PASS 4.27 s; `TestJobImageSandboxRuntimeStartsUnderTheNestedSandboxFlags` PASS 2.33 s |
| Per-file coverage, `-coverpkg` over daemon, config, doctor, isolation and cmd | matches the implementer's table; `copy_target.go` 78.3 % (72 of 92 statements), `daemon.go` 77.6 % (643 of 829) |
| `docker run --user 0:0 --entrypoint find <check image> / -xdev -type f -perm /6000` | prints nothing |
| go.dev release page and `/dl/?mode=json` | go1.27.3 is the newest stable release, released 2026-10-19 (21 days before review) |
| App `grep -rn check-install src` | only tests and test support; `go-build-upload-evidence.go:24` expects v1 (FU-5); no production parser |

gosec and govulncheck were not re-run by the reviewer; the implementer's results are taken as reported and are not the basis of any verdict below.

### Per-requirement verdicts

| ID | Verdict | Evidence |
| -- | ------- | -------- |
| REQ-01 | implemented | `internal/isolation/claude_staging_dir_integration_test.go` runs the image's real `claude` offline after the production hand-off and asserts a 0700 directory owned by the job user (lines 72–85), host `Lstat` success (92) and `OpenRoot` `fs.ErrPermission` (95–98); re-run PASS. H1 holds; the failing site was the subdirectory `OpenRoot`. |
| REQ-02 | implemented | `agent_result_copy.go:244` (Lstat), `:284` (OpenRoot), `:342` with `copy_target.go:107-108` (file open), `readRefusal` at `:350`; `sameDirectory` and `Readlink` keep "changed while it was read". `agent_result_copy_refusal_test.go` covers each site with permission, vanished and replaced cases, asserts no `/tmp` host path and a size under 4096 bytes. An unreadable entry is always a refusal, never a skip, unless it is the untracked harness path. |
| REQ-03 | implemented | `agent_harness_state.go:9` (list), `:15-27` (`ls-tree` on the start commit, skip only when empty), `:29-33`; wired once per resolution at `agent_result_resolution.go:88,96`, which serves the iterative check snapshot (`check_workspace.go:69`), publication, preview and pause. Skipped names are removed from the trusted copy target (`agent_result_copy.go:186`) and never opened (`:233`). `agent_harness_state_test.go` covers present, absent, tracked, tracked-and-unreadable (refused), symlink (not followed, secret not published), paths outside the list kept, zero, one and two candidate roots, the pushed branch and the iterative snapshot. Security review below. |
| REQ-04 | implemented | `agent_harness_state_rootless_integration_test.go` uses the production hand-off, writes `.claude/.cc-writes` (0700) and `other/` (0700) as uid 10001, asserts success without the harness path, then the "cannot read at other" refusal with no host path, then reclaim, removal and no labelled containers; re-run PASS. |
| REQ-05 | implemented | `config.go:81`, `check_validation.go:15`; `check_image_config_test.go` (empty, pinned, registry, uppercase hex accepted; tag-only, name-only, whitespace, newline, short, long, sha512, non-hex, digest-only, two digests refused with the exact message; config-file load); `local_check_image_test.go` (local load and merge keep it, a remote check, `check_image` or `check_images` is refused, hot reload keeps the start-time image, restart picks up the new one); `check_image_gate_test.go:129` direct-mode warning once per check naming only the ID. |
| REQ-06 | implemented | `build.go:343-345` sets `policy.Image` only when the task carries an image; only `checkexec.go:62` and `check_install.go:351` pass one. Provider (`job.go:535`), ownership (`docker_job_user.go:39`), probe and proposal containers read `cfg.JobImage` and are untouched. `check_image_policy_test.go` proves an argv that differs only in the image, the exact pre-slice argv without an image, install plus run, iterative plus terminal, and refusal of an `image` field in `platform_run_check`. |
| REQ-07 | implemented | `check_image_gate.go:28-48` inspects each distinct image once, with the contract's exact message; called at `daemon.go:306` under Docker before the daemon is returned (so before connect), warning at `:312` otherwise. `image_inspect.go` validates the reference, passes `--`, never pulls, and accepts only a `sha256:<64 hex>` ID. Doctor row `doctor/check_image_check.go` (OK with IDs and short digests, Fail naming missing images, Skip for direct or none) with `check_image_check_test.go`; the cmd wiring is tested by `doctor_check_images_test.go`. |
| REQ-08 | implemented | `check_install_provenance.go:17,32,75-91`; key placed after `hermetic`; `check_install_provenance_test.go:104` (exact direct JSON, `check_image: null`), `:163` (exact Docker JSON with and without a check image, no registry, name or tag), `:199` (direct with a configured image stays `null`); `check_image_policy_test.go:198` end to end through the terminal run. App grep re-run: no production parser. |
| REQ-09 | implemented | `packaging/check-image-go/Dockerfile` and `build.sh`: base pinned by digest, go1.27.3 (newest stable, released 2026-10-19, not below `go 1.25`), user 10001:10001, the four `ENV` values, setuid and setgid stripped and asserted in the build and again by `build.sh`, `go version` asserted, image ID printed. Reviewer inspected the built image: no setuid or setgid files, user `runner`. |
| REQ-10 | implemented | `check_image_go_integration_test.go` through the production `platform_run_check` path with network none: pass, the `ok` line (I-7), `exit_nonzero` on a failing test, and the job image without `image` still failing (I-8); the fixture asserts uid and gid 10001, read-only root, noexec `/tmp` and no network. Re-run PASS. |
| REQ-11 | implemented | See the security review below. |
| REQ-12 | partial | README and `docs/TESTING.md` cover every listed item, but `README.md:1024` says "The runner never pulls a check image", which is not true of the check run itself. See RV-2. |
| REQ-13 | partial | gofmt, vet, build, the full suite, race and every gated test pass on re-run. The contract requires at least 80 % statement coverage for each new or changed Go file; two changed files are below it: `internal/daemon/copy_target.go` 78.3 % and `internal/daemon/daemon.go` 77.6 %. Their baseline was already below 80 % and the added blocks are covered, but the contract does not make that exception. See RV-1. |
| REQ-14 | unverified (live, coordinator) | Not run. The contract says the live steps start only after the review verifies REQ-01 to REQ-13, and it does not exempt REQ-14 from completion, so the record cannot be `done` until REQ-14 passes. |

### Security review

- **The harness skip cannot hide agent changes.**
  - The skip is decided per resolution from `git ls-tree` on the start commit, for the one exact path `.claude/.cc-writes`. A path tracked at the base, as a blob, a tree or a gitlink, gives a non-empty listing and is copied and compared normally. An unreadable tracked path is refused (`TestPublishRefusesATrackedHarnessPathTheRunnerCannotRead`).
  - Because the skipped path is untracked at the base, dropping it can only drop new content. It cannot remove or mask a tracked file.
  - Matching is by the exact workspace-relative name during the walk. The walk never follows symlinks, so a symlinked `.claude` is copied as a symlink and visible in the change set. A `.cc-writes` symlink is dropped without being followed.
  - Siblings are kept: `.claude/settings.local.json`, `src/.cc-writes` and `.claude/.cc-writes-other` are all tested as kept.
  - Replacing a tracked `.claude` file with a directory is still a visible deletion.
  - Nested candidates get the same relative key against the same start commit.
- **Pinning is enforced and cannot be set remotely.**
  - Only `CheckConfig.Image` carries the image, validated by `immutableImagePattern` in both the local and the config-file load paths.
  - `ParseDelivery` refuses a remote `checks` key, `check_image` and `check_images`. Hot reload keeps the start-time checks.
  - The strict `platform_run_check` schema refuses `image`.
  - The container is started by the pinned `name@sha256` reference, so Docker resolves the content by digest.
- **"Cannot read" refusals never pass unreadable files.** `readRefusal` only changes the message. Every permission failure still returns a refusal error, and only the untracked harness path is skipped before it is opened.
- **The start gate holds.** It runs inside the Docker branch after the provider CLI gate and before `NewWithExternalOperationDependencies` returns. A missing image, or any inspection error including an invalid reference, stops start (`TestNewRefusesToStartWhenACheckImageIsMissing`).
- **Provenance v2 carries only the digest.** `ImageDigest` returns only the `sha256:` capture, and the exact JSON tests assert that no registry, name or tag appears.

### Decisions I-1 to I-9

- I-1 accepted: `Skip` is additive and does not change the exit code (`TestSkipIsReportedWithoutFailingTheReport`).
- I-2 accepted: the edit to `cmd/our-runner/doctor_command.go` outside the owned files is a pure data pass-through. REQ-07's doctor row cannot be anything but Skip without it. It is tested and at 100 % coverage.
- I-3 accepted: it is consistent with REQ-02. A private file now gets the same cause as a private directory.
- I-4 accepted: the digest comes from the configured pinned reference, the gate proves that the reference resolves, and the container runs by that same reference. Note that `immutableImagePattern` accepts uppercase hex, which Docker rejects. Such a value stops start at the gate with "verify image", so the behaviour fails closed. The digest would only reach provenance if Docker accepted it.
- I-5 accepted: the reference is validated, `--` precedes it, and the output pattern is strict.
- I-6 accepted.
- I-7 accepted: a passed result carries no output, so the `exit 3` wrapper is a sound way to show the `ok` line under the same image and policy.
- I-8 accepted: the contract's "go: not found" was the planner's expectation. The test asserts the real, unchanged job-image behaviour.
- I-9 accepted as a flagged uncertainty. On the classic image store, `build.sh` prints `name@<image ID>`, which may not resolve. That would stop start at the gate, so the failure is closed, but the README instruction would mislead an operator on such a host. Recorded as FU-9.

## Review findings and repair

The first review set `changes_requested` and asked for one directed `develop-implementer` repair pass for RV-1 and RV-2. That pass is repair attempt 1, recorded below.

- **RV-1 (REQ-13, blocking).**
  - Evidence: reviewer coverage run, `internal/daemon/copy_target.go` 72 of 92 statements (78.3 %) and `internal/daemon/daemon.go` 643 of 829 (77.6 %). Both files are changed by this slice, and REQ-13 requires at least 80 % for each new or changed Go file.
  - Required correction: add tests that bring `copy_target.go` to at least 80 %; at least 2 more covered statements, for example the remaining error branches of `copyRegular` and `clearDifferentKind`. Add tests that bring `daemon.go` to at least 80 %; at least 21 more covered statements.
  - If raising `daemon.go` is judged out of proportion, the implementer must not rescope it alone. Return to the planner, who amends REQ-13 with a written exception (changed blocks fully covered, file below baseline) before re-review.
  - Verification: the same `-coverpkg` profile shows both files at or above 80 %, or the amended contract, and the full suite and race still pass.
- **RV-2 (REQ-12, blocking, small).**
  - Evidence: `README.md:1024` says "The runner never pulls a check image." The gate and doctor never pull (`image_inspect.go`), but the check `docker run` argv carries no `--pull=never`. If the image disappears after start, Docker's default `missing` policy attempts a registry pull by the pinned digest. Integrity still holds because the digest pins the content, but the README claim is wrong.
  - Required correction: state precisely that start and doctor never pull, and that the check run relies on the image staying present. Alternatively add `--pull=never` to check containers only, keeping REQ-06's byte-identical argv for checks without an image, with a test.
  - Verification: diff review, plus a policy argv test if the flag is added.

### Repair attempt 1 (develop-implementer, 9 November 2026)

Fast-forwarded to `c18e075`, then fixed RV-1, RV-2 and FU-8. No production code changed for RV-1; only tests were added.

- **RV-1.** Real behavior tests, no assertion-free tests:
  - `internal/daemon/new_optional_registries_test.go`:
    - `New` wires all source-publication roles from one valid installation, and leaves the preview capabilities unset.
    - `New` refuses an invalid credential before the journal is opened.
    - `New` wires a configured preview observer and lifecycle controller.
    - `New` refuses two preview entries with one capability identity (observation and lifecycle).
    - `New` refuses an invalid configuration before touching state.
  - `internal/daemon/runner_state_files_test.go`:
    - `SetPausedState` refuses a shared or uncreatable state directory.
    - It reports a pause entry it cannot remove or replace, and leaves no temporary file.
    - `ReadPausedState` refuses a shared or oversized file.
    - `ReadRunnerID` refuses oversized, invalid and empty content.
    - `WriteRunnerID` refuses an invalid ID, a state path below a file, and a symlinked state directory, and leaves no temporary file when it cannot commit.
  - `internal/daemon/agent_result_cache_reuse_test.go`:
    - An unchanged symlink is kept across snapshots, and a retargeted symlink is published and cached with the new target (`placeSymlink`).
    - A cached copy that is missing is copied again, with the same commit (`reusable`).
    - A candidate cache rewritten or replaced outside the runner makes the snapshot refuse and drop the cache. The next snapshot rebuilds the same commit. This fails closed at `git read-tree -m` before the copy, so it adds no `copy_target.go` coverage; it is kept as a behavior test.
  - Result, same `-coverpkg` profile as the reviewer: `copy_target.go` 80.4 % (74 of 92), `daemon.go` 80.6 %.
- **RV-2.** Implemented the coordinator decision (I-10):
  - `isolation.Policy.PullNever` puts `--pull=never` immediately before the image, and `runWorkspaceTask` sets it only when the task carries a check image.
  - `internal/isolation/pull_never_test.go` proves, for a check, a provider (nested sandbox, MCP) and a consultation policy, that the flag is the only difference and sits just before the image. It also proves that no `--pull*` argument appears when the flag is off.
  - `check_image_policy_test.go`:
    - The with-image argv now equals the without-image argv plus `--pull=never` before the image.
    - A check without an image has no pull flag and keeps its exact pre-slice argv.
    - Install, iterative and terminal runs carry `--pull=never` exactly when the check has an image.
  - The gated `TestGoCheckImageRunsGoTestUnderTheProductionPolicy` passes with the flag on the rootless daemon.
  - README line 1024 now says that start and doctor only inspect, that a check with an image runs with `--pull=never`, and that every other container keeps its argv.
- **FU-8.** `packaging/check-image-go/build.sh` was added to the `sh -n` lists in README and `docs/TESTING.md`.

Repair evidence:

| Command | Result |
| ------- | ------ |
| `gofmt -l ./internal ./cmd` | prints nothing |
| `go vet ./...` | ok |
| `sh -n packaging/check-image-go/build.sh packaging/job-image/build.sh` | ok |
| `go test ./... -count=1` | every package ok, no failures, exit 0 |
| `go test -race ./internal/daemon/ ./internal/config/ ./internal/doctor/ ./internal/isolation/ ./internal/remoteconfig/ ./cmd/our-runner/ -count=1` | all 6 packages ok, exit 0 |
| `-coverpkg` profile over daemon, config, doctor, isolation and cmd | exit 0; `copy_target.go` 80.4 %, `daemon.go` 80.6 %, `build.go` 92.3 %, `isolation.go` 96.5 % |
| Gated, same environment as the review | `TestIterativeSnapshotSkipsHarnessStateDuringARootlessHandOff` PASS 0.97 s; `TestGoCheckImageRunsGoTestUnderTheProductionPolicy` PASS 11.18 s (4 subtests); `TestAgentPackMountRefusesEveryWriteInsideTheContainer` PASS 0.73 s; `TestClaudeCreatesAPrivateStagingDirectoryInTheHandedOffWorkspace` PASS 4.39 s; `TestJobImageSandboxRuntimeStartsUnderTheNestedSandboxFlags` PASS 2.64 s |

Non-blocking observations, no change required in this slice:

- **FU-8 confirmed.** `build.sh` is not in the README and `docs/TESTING.md` `sh -n` lists.
- **FU-9.** On a classic (non-containerd) image store, `build.sh` should print a resolvable reference, for example a repo digest after a push, or the README should say how to obtain one.
- Uppercase hex in a check image passes config validation but fails closed at the gate. A later config change could normalise it or refuse it with the pinning message.

### Re-review after repair attempt 1

Reviewer: second fresh develop-reviewer (Opus 5.5), 9 November 2026. Reviewed head `7035946` against main `65e7310` and the first review `c18e075`, from the contract, `PRODUCT.md` "Workspace composition" item 2, the source, the tests and the rootless Docker daemon. The verdicts come from the code and from runs the reviewer did, not from the repair summary. There is no UI, so the prototype and the app do not apply.

Commands re-run by the reviewer:

| Command | Result |
| ------- | ------ |
| `gofmt -l .` | prints nothing |
| `go vet ./...`, `go build ./...` | ok |
| `sh -n packaging/check-image-go/build.sh packaging/job-image/build.sh` | ok |
| `go test ./... -count=1` | exit 0, every package ok |
| `go test -race ./internal/daemon/ ./internal/config/ ./internal/doctor/ ./internal/isolation/ ./internal/remoteconfig/ ./cmd/our-runner/ -count=1` | exit 0, 6 packages ok |
| `go test -coverpkg=<daemon,config,doctor,isolation,cmd> -coverprofile` over the same packages, aggregated per file by unique block | `copy_target.go` 74 of 92 (80.4 %), `daemon.go` 668 of 829 (80.6 %), `build.go` 92.3 %, `isolation.go` 96.5 %, `agent_result_copy.go` 89.2 %, `agent_result_resolution.go` 90.2 %, `agent_harness_state.go` 100 %, `check_image_gate.go` 100 %, `check_install.go` 95.3 %, `check_install_provenance.go` 90.9 %, `checkexec.go` 97.4 %, `config/config.go` 93.7 %, `check_validation.go` 100 %, `doctor.go` 85.8 %, `check_image_check.go` 97.1 %, `image_inspect.go` 100 %, `doctor_command.go` 100 % |
| Gated, `DOCKER_HOST=unix:///run/user/1000/docker.sock RUN_DOCKER_SANDBOX_PROBE=1 JOB_IMAGE=our-runner-job:rjic@sha256:a2c8cabe… CHECK_IMAGE=our-runner-check-go:rdbc@sha256:59201f88…` | `TestIterativeSnapshotSkipsHarnessStateDuringARootlessHandOff` PASS 1.03 s; `TestGoCheckImageRunsGoTestUnderTheProductionPolicy` PASS 12.95 s (4 subtests); `TestAgentPackMountRefusesEveryWriteInsideTheContainer` PASS 0.65 s; `TestClaudeCreatesAPrivateStagingDirectoryInTheHandedOffWorkspace` PASS 4.17 s; `TestJobImageSandboxRuntimeStartsUnderTheNestedSandboxFlags` PASS 2.10 s |

RV-1 (REQ-13) is resolved:

- Both files are at or above 80 % in the reviewer's own profile.
- The repair changed no production code for RV-1. The new tests assert real outcomes:
  - `new_optional_registries_test.go` checks which roles are wired or left nil, the exact refusal text, and that a refused start never creates `journal.db`.
  - `runner_state_files_test.go` checks refusal text, that no temporary file is left, and that a bad entry does not read back as valid state.
  - `agent_result_cache_reuse_test.go` checks the snapshot SHA, `os.SameFile` on an unchanged cached symlink, the retargeted symlink content, the rebuilt cached copy, and that a tampered cache is refused and dropped before an identical rebuild.
- No test is assertion-free.

RV-2 (REQ-12) is resolved:

- `isolation.Policy.PullNever` is set in exactly one production place, `build.go:345`, inside `if task.image != ""`. The policy is built fresh for each task by `commandPolicy`, so the flag cannot carry over to another task.
- `isolation.go:213-215` appends `--pull=never` immediately before the image.
- The only callers that pass an image are the check command (`checkexec.go:62`) and its install step (`check_install.go:351`).
- `pull_never_test.go` shows that the flag is the only argv difference, that it sits before the image, and that no `--pull*` appears when the flag is off. This holds for check, provider and consultation policies.
- `check_image_policy_test.go` shows:
  - the with-image argv equals the job-image argv plus `--pull=never` before the image;
  - a check without an image has no pull flag;
  - install, iterative and terminal runs carry the flag exactly when the check has an image.
- The gated Go test runs the real `docker run --pull=never` through `runWorkspaceTask` on the rootless daemon and passes, so the Docker CLI there accepts the flag.
- `README.md:1024-1028` now matches the code: start and doctor only inspect, check-image containers use `--pull=never`, and every other container keeps its argv.
- FU-8: `build.sh` is now in both `sh -n` lists.

Regressions:

- REQ-06's byte-identical argv for a check without an image still holds; it is asserted in the policy tests.
- The repair added no code comments.

Per-requirement verdicts after the re-review:

| ID | Verdict | Evidence |
| -- | ------- | -------- |
| REQ-01 | implemented | Unchanged since the first review; gated `TestClaudeCreatesAPrivateStagingDirectoryInTheHandedOffWorkspace` re-run PASS |
| REQ-02 | implemented | Unchanged; refusal tests pass in the full and race runs |
| REQ-03 | implemented | Unchanged; harness-state tests pass |
| REQ-04 | implemented | Gated rootless test re-run PASS |
| REQ-05 | implemented | Unchanged; config, remoteconfig, reload and warning tests pass |
| REQ-06 | implemented | The only argv change for a check with an image is `--pull=never` before the image (I-10, RV-2). That flag strengthens REQ-07's "no network pull", and every listed restriction is still asserted. Without an image the argv is unchanged |
| REQ-07 | implemented | Unchanged; gate and doctor tests pass |
| REQ-08 | implemented | Unchanged; exact JSON tests pass |
| REQ-09 | implemented | Unchanged; image `sha256:59201f88…` used by the gated run |
| REQ-10 | implemented | Gated test re-run PASS with `--pull=never` in the real argv |
| REQ-11 | implemented | Unchanged; `--pull=never` means an image removed after start fails the check instead of being fetched |
| REQ-12 | implemented | RV-2 resolved; README pull statement accurate; FU-8 done |
| REQ-13 | implemented | RV-1 resolved; every new or changed Go file is at least 80 %; gofmt, vet, build, the full suite, race and gated tests pass. gosec and govulncheck were not re-run by this reviewer, and the repair changed no dependency; the implementer's results stand as recorded |
| REQ-14 | unverified (live, coordinator) | Not run; it is the coordinator's live step |

No new findings. Non-blocking points carried over: FU-9 (classic image store reference) and the uppercase-hex pin that passes config validation but fails closed at the gate.

## Completion

Not complete. Status `review_ready`: code verified; REQ-14 live pending. REQ-01 to REQ-13 are implemented as of the re-review after repair attempt 1. REQ-14 is unverified and is the coordinator's live run under "Coordinator live steps". Only REQ-14 evidence that passes may set `done`.

## Follow-ups

- **FU-1.** Read access during the run to any private-mode file the agent creates (outside the harness list), so iterative checks never refuse legitimate work product.
- **FU-2.** Toolchains inside the agent container, so the agent can run `go` directly; must keep the toolchain a host decision (D-2).
- **FU-3.** Tool names in hook events (cross-repository protocol slice) for durable sandbox evidence.
- **FU-4.** Reference check images for other ecosystems.
- **FU-5.** App test support `src/server/planning-manifests/test-support/go-build-upload-evidence.go` and `go-planning-driver_test.go` expect `our-runner.check-install.v1`; accept v2 (app repository).
- **FU-6.** A failed check's output can contain the check workspace's absolute host path, because the check directory is bind-mounted at its host path (predates this slice).
- **FU-7.** Done in repair 1 (RV-1): `copy_target.go` 80.4 %, `daemon.go` 80.6 %.
- **FU-8.** Done in repair 1: `packaging/check-image-go/build.sh` is in the `sh -n` lists in README and `docs/TESTING.md`.
- **FU-9.** On a classic (non-containerd) image store, `build.sh` prints `name@<image ID>`, which may not resolve locally; print or document a resolvable repo-digest reference (review I-9).

## Estimate

This slice: about 1 slice (range 1 to 1.5; the upper end if step 0 shows H2 or a second harness path). FU-1 to FU-4 are not counted here.

## Subsequent changes

None.
