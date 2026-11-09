# Runner Docker Build checks: agent snapshot under hand-off and a host-owned check toolchain

## Control

| Field                  | Value |
| ---------------------- | ----- |
| ID                     | FR-01 follow-up `runner-docker-build-checks` (RDBC) |
| Kind                   | bug (release blocker for Docker-isolated Build jobs) plus one bounded feature (host-declared check image) |
| Status                 | ready |
| Risk                   | critical. The slice changes what the runner reads out of the agent's workspace into the trusted result repository, and which image runs required checks. A defect can publish harness state into a change set, hide a real concurrent change, or let a check run in an image that nobody on the host chose. It does not touch credentials, the control-plane protocol or tenant data. |
| Complexity             | complex. Two independent defects meet in one live path (snapshot refusal, then missing toolchain). The work crosses the candidate copy, configuration validation, the check task path, the provenance artifact, the start gate, doctor, a new packaging directory and gated Docker tests. Each piece is small; the live proof depends on rootless Docker behaviour that unit tests cannot show. |
| Parent scope           | `PRODUCT.md` "Implementation and review flow" (Build runs required checks and returns evidence), "Workspace composition" item 2 ("A pinned project toolchain layer"), the isolation baseline ("Separate trusted bootstrap and evidence collection from checked-out repository code"), and MCP enforcement (scoped checks use the same runner-owned sandbox and network policy as final required checks; `platform_run_check` cannot supply or override the executable, isolation or network). Parent records in the app repository: `docs/development/build-job-approved-plan-delivery.md`, section "FR-01 Docker Build check (9 November 2026)" (defects FD-1, FD-2, FD-3); `docs/development/runner-local-checks.md`; `docs/development/runner-hermetic-check-workspace.md` (D49, REQ-12 provenance, "Owner decisions on LC-1 to LC-4"); `docs/development/runner-job-image-claude-cli.md` (RJIC REQ-12, still open). |
| Product sources        | `PRODUCT.md` sections named above. Owner decisions LC-1 and LC-2 (checks stay defined on the runner host; the control plane cannot define check commands), LC-3 (isolation changes are restart-required), LC-4 (`acknowledge_direct`). |
| Planner                | Opus 5.5 (`claude-opus-5-5`); develop-planner subagent, RDBC planning session, 9 November 2026 |
| Implementer            | not assigned |
| Reviewer               | not assigned |
| Repair attempt         | 0 |
| Updated                | 2026-11-09 |
| Baseline               | app-runner `main` at `fc4ae6a` ("test(daemon): prove a job keeps its start-time checks across a hot reload"), worktree `agent-a6540183dac8397d8`, branch `worktree-agent-a6540183dac8397d8`. This record is the only change. |
| Blocker / resume state | none for implementation. The live verification (REQ-14) needs the coordinator to swap the runner binary, edit `runner.local.yaml` and restart; see "Coordinator live steps". |

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

0. [ ] REQ-01 first, no production code yet. On the rootless daemon, in the job image under the job flags, run Claude 2.1.299 offline so that it writes a project settings file (for example via a settings-writing command that needs no credentials; if none exists, record that and use `strace`-free evidence such as a wrapper that lists `.claude/` from a second container while Claude starts). Record owner, mode and lifetime of `.claude/.cc-writes`, then reproduce the host-side snapshot refusal and record which call fails. Write the step-0 note. Stop if neither H1 nor H2 holds.
1. [ ] REQ-02 tests first (injected errors per site), then the message split.
2. [ ] REQ-03 tests first, then `agent_harness_state.go` and the skip-set wiring for iterative and terminal snapshots.
3. [ ] REQ-04 gated test.
4. [ ] REQ-05 tests first, then the config field, validation, reload and warning.
5. [ ] REQ-06 tests first, then carry the check image through `workspaceTask` into `commandPolicy` (one added parameter or field; no other caller changes).
6. [ ] REQ-07 tests first with an inspection seam (`var inspectCheckImage = …`, like `probeProviderCLI`), then the gate and the doctor row.
7. [ ] REQ-08 tests first, then provenance v2 and the app grep.
8. [ ] REQ-09: packaging files; build on the rootless daemon; record version, base digest and image ID.
9. [ ] REQ-10 gated test against the built image.
10. [ ] REQ-12 docs, REQ-13 checks, implementation report, status `review_ready`.

## Verification plan and results

| Requirement | Planned evidence | Result |
| ----------- | ---------------- | ------ |
| REQ-01 | Step-0 note with reproduction output | pending |
| REQ-02 | Unit tests | pending |
| REQ-03 | Unit tests | pending |
| REQ-04 | Gated test output | pending |
| REQ-05 | Unit tests | pending |
| REQ-06 | Unit tests | pending |
| REQ-07 | Unit tests | pending |
| REQ-08 | Unit tests and app grep | pending |
| REQ-09 | Build output | pending |
| REQ-10 | Gated test output | pending |
| REQ-11 | Tests and diff review | pending |
| REQ-12 | Diff review | pending |
| REQ-13 | Command output | pending |
| REQ-14 | Coordinator live evidence | pending |

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

Not started.

## Reviewer requirement review

Not started.

## Review findings and repair

None yet.

## Completion

Not complete. Only a verified review may set `done`.

## Follow-ups

- **FU-1.** Read access during the run to any private-mode file the agent creates (outside the harness list), so iterative checks never refuse legitimate work product.
- **FU-2.** Toolchains inside the agent container, so the agent can run `go` directly; must keep the toolchain a host decision (D-2).
- **FU-3.** Tool names in hook events (cross-repository protocol slice) for durable sandbox evidence.
- **FU-4.** Reference check images for other ecosystems.

## Estimate

This slice: about 1 slice (range 1 to 1.5; the upper end if step 0 shows H2 or a second harness path). FU-1 to FU-4 are not counted here.

## Subsequent changes

None.
