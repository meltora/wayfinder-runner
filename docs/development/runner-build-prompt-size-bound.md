# Runner agent prompt delivery without the single-argument limit

## Control

| Field                  | Value |
| ---------------------- | ----- |
| ID                     | FR-08 `runner-build-prompt-size-bound` |
| Kind                   | bug (release blocker, BJAP follow-up) |
| Status                 | done |
| Risk                   | medium. The change sits on the provider start path for every Plan and Build job, every adapter and every supervisor. A defect stops all jobs from starting or drops prompt text. It does not touch authentication, credentials, authority or tenant data. The prompt is ticket text, not a secret, but the change also stops putting it on host and container command lines. |
| Complexity             | standard. The plan entry estimated easy. It is standard because the fix crosses three adapters (Claude, Codex, OpenCode), three supervisors (direct, tmux, Herdr), the Docker wrapper, file lifecycle and goldens. Each part is small and the design is settled. |
| Parent scope           | `PRODUCT.md` "Implementation and review flow" step 7 (the Build agent receives the approved plan and ticket context). Parent slice: app `docs/development/build-job-approved-plan-delivery.md` (BJAP), open limitation "The ticket description (up to 256 KiB) is inlined in the prompt argument". The parent plan entry is in app `docs/implementation-progress.md`, "First release slice plan (28 October 2026)", FR-08. |
| Product sources        | `PRODUCT.md` "Implementation and review flow" step 7. App protocol limits: `src/server/runner-protocol/v1/shared/protocol-limits.ts` (`MAX_TICKET_SUMMARY_BYTES` 1024, `MAX_TICKET_DESCRIPTION_BYTES` 256 KiB, `MAX_ACCEPTANCE_CRITERIA_BYTES` 128 KiB, `MAX_REVIEW_COMMENT_BYTES` 128 KiB, `MAX_REQUIRED_CHECKS` 64 opaque IDs of 128 bytes each, `MAX_ENVELOPE_BYTES` 2 MiB). Runner limits: `api/runner/v1/review_feedback_continuation.go` (`ReviewFeedbackContinuationMaxContentBytes` 16 KiB). The approved plan (up to 768 KiB) is not inlined in the prompt (BJAP D3), so the plan size does not affect the prompt size. |
| Planner                | Opus 5.5 (`claude-opus-5-5`); develop-planner subagent, FR-08 planning session, 28 October 2026 |
| Implementer            | Opus 5.5 (`claude-opus-5-5`); develop-implementer subagent, FR-08 implementation session, 28–29 October 2026 |
| Reviewer               | Opus 5.5 (`claude-opus-5-5`), fresh develop-reviewer context, 29 October 2026 |
| Repair attempt         | 0 |
| Updated                | 2026-10-29 |
| Baseline               | app-runner `main` at `d07f644` ("feat(runner): receive the approved plan in Build jobs"), worktree `wt/runner-fr-08`, branch `slice/fr-08-runner-build-prompt-size-bound`. The tree was clean before this record was added. This record is the only change, and it is uncommitted. The runner repository had no `docs/development/` directory, so this record creates it. |
| Blocker / resume state | none |

## Outcome and current gap

**Outcome.** A Plan job, a Build job and a Build continuation start their provider process whenever every ticket and review field is within the product maximum. The provider receives the whole prompt byte for byte, and nothing is silently truncated. Input beyond the product maximum is refused with a typed reason before any container starts.

**Current behavior at `d07f644` (code evidence):**

- The prompt is built by `jobRunner.buildPrompt` (`internal/daemon/job.go:1037`), which is called twice: once for the direct spec (`job.go:480`) and again for the Docker spec (`job.go:549`).
  - Plan jobs inline the summary, the description and the acceptance criteria (`job.go:1042`).
  - Build jobs inline the same fields through `buildTicketBackground` (`internal/daemon/approved_plan_prompt.go:10`).
  - Build jobs also inline the review comment (`job.go:1065`), and a review-feedback or human change request continuation as one JSON-escaped line (`job.go:1081`, `human_change_request_continuation.go:19`).
- Every adapter passes the prompt as **one argv element**:
  - Claude: `"-p", p.Prompt` (`internal/provider/provider.go:308`).
  - Codex: the trailing positional (`provider.go:159`).
  - OpenCode: the trailing positional (`internal/provider/opencode.go:84`).
- Docker isolation keeps that element unchanged, as one argument of `docker run --rm -i ... image claude ...` (`internal/isolation/isolation.go:104`, `Docker.Wrap`). Inside the container, `docker` then calls `execve` on the provider with the same element.
- The supervisors treat argv as follows:
  - `DirectProcess` calls `exec.CommandContext("docker", args...)` (`internal/supervisor/supervisor.go:99`). It sets `cmd.Stdin = spec.Stdin`, and job specs never set `Stdin`.
  - `TmuxSupervisor` joins the whole command, prompt included, into **one** shell string passed as one argument to `tmux new-session` (`internal/supervisor/tmux.go:83-90`). That makes the limit even tighter, because quoting adds bytes and the other arguments share the same element.
  - `HerdrSupervisor` writes the quoted argv into a `0700` launch script in the supervisor runtime directory (`internal/supervisor/herdr.go:333-380`). The script then calls `exec docker ... "<prompt>"`.
- Linux limits a single argv or environment string to `MAX_ARG_STRLEN` = 32 pages = 128 KiB. Overall, `ARG_MAX` is 2 MiB on this host (`getconf ARG_MAX` = 2097152). It is measured against argv plus environment.
- Measured on this host on 28 October 2026: `/bin/true "<140000 bytes>"` fails with `argument list too long` (exit 127), and `/bin/true "<120000 bytes>"` succeeds.
- **Gap.** A product-valid ticket with a description of more than about 128 KiB fails to start:
  - Direct and tmux fail at `cmd.Start` with `E2BIG`, reported as `agent_start_failed` / "start agent process".
  - Herdr fails inside the launch script, so the agent appears to exit nonzero (`agent_exit_failed`). The job never runs.
- **Secondary gap.** The prompt is visible in host `ps` output for the `docker` client, in `docker inspect` (`Config.Cmd`), in the Herdr launch script and in the tmux server command. The README notes it in the Claude weaker-sandbox paragraph ("command lines (prompts and paths ...)").
- The runner never validates ticket field sizes. It relies on the app's Zod limits. No runner code checks `len(Ticket.Description)`.
- The work-intake prompt-only path already delivers its prompt on stdin, for Claude, Codex and OpenCode (`provider.go:372`, `provider.go:199`, `opencode.go:139`). Claude uses `-p --input-format text`, so this repository already depends on the CLI's stdin prompt behavior.

**Worst-case prompt size under the product maximums** (Build continuation, which is the largest variant):

| Part | Bytes |
| --- | --- |
| Summary | 1 KiB |
| Description | 256 KiB |
| Acceptance criteria | 128 KiB |
| Review comment | 128 KiB |
| Required checks (64 × 128 + separators) | about 8.3 KiB |
| Continuation content (16 KiB, JSON-escaped; Go escapes `<`, `>`, `&` and control characters as `\uXXXX`, so 6×) | 96 KiB |
| Fixed instruction text and pack path | about 5 KiB |
| **Total** | **about 622 KiB** |

The Plan worst case is about 386 KiB. Both are far above 128 KiB and below 1 MiB.

**Reproduction plan (before changing code, test-first):**

1. Add a unit test `TestDirectProcessStartsWithAPromptOverTheSingleArgumentLimit` in `internal/supervisor`. It uses the current argv delivery: a spec with `Command: "sh"`, `Args: ["-c", "exit 0", "x", <200 KiB string>]`. At baseline, `Start` returns an error that wraps `syscall.E2BIG` (`errors.Is(err, syscall.E2BIG)`). Keep this as the documented baseline failure, then turn the test into the stdin-file variant for REQ-02.
2. Add a daemon test that builds a Build job with a 256 KiB description and runs it through the job start path, with a recording supervisor that executes the spec through `DirectProcess` and a fake provider `sh -c 'wc -c'`. At baseline it fails at "start agent process". After the fix it starts and receives every byte (REQ-01).
3. Behind the Docker gate (see the verification plan), run the wrapped argv for a 256 KiB prompt through the real `docker` CLI. At baseline the host `execve` of `docker` fails with `E2BIG`.

## Scope

**In scope (runner repository only):**

- Build each job prompt once, check its size, and deliver it to the provider on **stdin from a runner-private file**. This applies to Plan, Build and continuation jobs, in direct and Docker isolation, under every supervisor (direct, tmux, Herdr).
- Adapter specs:
  - Claude: `-p --input-format text` with no prompt argument.
  - Codex: `exec ... -` and `exec ... resume <id> -`.
  - OpenCode: `run ...` with no message.
- Runner-side input bounds that mirror the app protocol limits, plus an assembled-prompt ceiling. Oversize input gets a typed refusal before the container starts.
- Prompt file lifecycle and secrecy: `0600`, never mounted, never handed off, removed on every exit path, and swept at startup.
- Update the goldens, the README paragraphs that describe prompt delivery and the weaker-sandbox command-line exposure, and `docs/TESTING.md` for the new gated test.

**Explicit exclusions:**

- Changing prompt wording or content. Plan prompts must stay byte-identical (see BJAP REQ-08).
- Inlining the approved plan. BJAP D3 stands.
- The work-intake prompt-only path. It already uses stdin and is unchanged.
- Any app change.
- Live runner, dev database or stack changes. No live verification is required ("Live: none" in the plan entry).
- New `JobFinishedFailureCode` values. These would cross the protocol and need an app change.

**Dependencies:**

- None blocking.
- The app already enforces the field limits. This slice needs no app change. If a later slice wants a dedicated failure code (for example `agent_prompt_too_large`), that becomes an app dependency for that slice, not this one.

**Parent requirements left open:** none from BJAP beyond this limitation. BJAP L-1 (JSON escaping and a runner digest rule) stays deferred.

## Acceptance contract

| ID | Product source | Required behavior and observable acceptance scenario | Verification method |
| --- | --- | --- | --- |
| REQ-01 | Flow step 7; BJAP limitation | Given product-maximum inputs, the provider process starts and its stdin delivers exactly the bytes `buildPrompt` produced, with no truncation and no `E2BIG`. Each case uses distinct start and end sentinels in every large field. The cases are: **(a)** a Build job with a 256 KiB description; **(b)** a Plan job with a 256 KiB description and 128 KiB criteria; **(c)** a Build human change request continuation with a 256 KiB description, 128 KiB criteria, a 128 KiB review comment, 64 required checks and 16 KiB `<`-filled content (worst case, about 622 KiB); **(d)** a minimal prompt (`Summary` only, empty description), so the one-byte-to-large range is covered. For each case, the received byte count equals `len(prompt)` and the SHA-256 matches. | Daemon test that runs the real start path with `DirectProcess` and a fake provider that writes `wc -c` and `sha256sum` of stdin to a file. Gated Docker test (REQ-03). |
| REQ-02 | Same | No prompt byte appears in any argv element. The prompt goes to the provider only through `supervisor.Spec.StdinFile`, which is new. **(a)** For each adapter (Claude, Codex, OpenCode) × Plan/Build × resume/no-resume × hardened/unhardened, `BuildSpec` returns args with no element that contains the prompt. Its stdin source yields the prompt. **(b)** Claude args contain `-p` and `--input-format text`, with no positional prompt. Codex args end with `-`, also after `resume <id>`. OpenCode args have no trailing message. **(c)** After `Docker.Wrap`, the `docker` argv still contains `-i` and no prompt bytes. **(d)** `DirectProcess` opens `StdinFile` read-only, as a regular file, without following symlinks, and uses it as the child's stdin. Tmux and Herdr add a shell `< '<quoted path>'` redirect to the provider command, inside the existing redirects and before `> log 2>&1`. **(e)** Setting both `Stdin` and `StdinFile` is refused by every supervisor. **(f)** The tmux `new-session` command string stays under 16 KiB for the REQ-01(c) job. | Provider unit tests, an option-collection test, updated goldens (`internal/provider/testdata/baseline-*.golden.json` gain the stdin content and lose the prompt argument), `isolation` Wrap test, and supervisor unit tests for direct, tmux and Herdr script content. |
| REQ-03 | Same; Claude 2.1.299 pin | The job image's Claude Code (2.1.299) accepts the prompt on stdin under the exact Docker job flags. Behind `RUN_DOCKER_SANDBOX_PROBE=1` and `JOB_IMAGE`, a new gated test runs the image's real `claude` offline, reusing the offline Messages API harness in `internal/isolation/claude_weaker_sandbox_integration_test.go`. It passes the argv from `Claude.BuildSpec` for a Docker Build job and feeds a 256 KiB-description prompt through `docker run -i` from a host file. The test passes when: the fake API records that the first request's user text contains both sentinels and equals the prompt (byte count and SHA-256); Claude exits 0; and no probe container remains. A control case keeps argv delivery of the same prompt and shows the host `docker` exec fails with `E2BIG`. The doctor and startup gate keep `--print` and `--input-format` in `claudeRequiredCLIOptions` (`internal/provider/cli_options.go:13`). Both are already declared in `internal/provider/testdata/claude-help-2.1.299.txt` (lines 123 and 170), and `TestClaudeRequiredCLIOptionsMatchEveryOptionTheRunnerPasses` must still pass. | Gated Docker test, run locally by the implementer against the current job image with the result recorded. The existing CLI option tests. |
| REQ-04 | Same | Codex and OpenCode jobs use stdin with no prompt argument. Codex: `codex exec [opts] -` and `codex exec [opts] resume <id> -` read the prompt from stdin. Local `codex-cli 0.153.4` help states this for both forms. OpenCode: `opencode run [opts]` with no message reads stdin. The existing prompt-only path relies on this (`opencode.go:136-139`). Neither adapter declares a `CLIOptionContract`, so the doctor does not probe them. Unit tests cover the spec. A gated smoke test, skipped unless `RUN_PROVIDER_STDIN_SMOKE=1` and the binary is on `PATH`, runs each CLI with a fake provider endpoint where practical. If the implementer cannot prove OpenCode reads stdin offline, they record the evidence and return to the planner. They do not keep argv delivery for OpenCode. | Provider unit tests; optional gated smoke with the result recorded or marked `unverified`. |
| REQ-05 | Protocol limits | Before workspace preparation, the runner refuses a job whose input is over a product limit, and starts no container. The limits are: summary > 1024 bytes, description > 256 KiB, acceptance criteria > 128 KiB, review comment > 128 KiB, more than 64 required checks or any check over 128 bytes, or continuation content over `ReviewFeedbackContinuationMaxContentBytes`. After building, a prompt over `maxAgentPromptBytes` = 1 MiB is also refused before `BuildSpec` and before any `docker` call. Both refusals finish the job `failed` with `FailureCode` `agent_start_failed` and the fixed message `agent prompt exceeds the runner size limit`. No prompt text appears in the message or in logs. Fields exactly at each limit are accepted (boundary). One byte over each limit is refused. A 1 MiB prompt is accepted and 1 MiB + 1 is refused. A test asserts that the supervisor's `Start` and the isolator are never called on refusal, and that no prompt file is created. | Daemon unit tests with a recording supervisor and isolator, table-driven per field, at the limit and one byte over. |
| REQ-06 | Security posture (runner README: credentials, isolation) | **Prompt file location, permissions and lifecycle.** The file lives in a runner-owned `0700` directory `<state_dir>/supervisor/prompts/`. It is created exclusively (`O_EXCL`, mode `0600`) with a random name scoped to the attempt. It is **not** in the job home, the socket or config directory, the workspace or the pack, so it is never bind-mounted into the container. It is never in `OwnershipRequest.Private` or `Workspace`, so it is never chowned to the job user. In Docker, only the host `docker` client reads it, and the container sees the prompt only on stdin. The file is removed after `proc.Wait` returns and on every earlier failure path (BuildSpec error, wrap error, hand-off error, cancellation before start, `Start` error, `finalizeProviderStart` error). Daemon startup removes any leftover files in `prompts/`. This is safe for a re-adopted tmux or Herdr session, because the child keeps an open file descriptor after unlink. Concurrent attempts get distinct files, and one attempt's cleanup never removes another's. | Daemon tests: file mode and directory mode while running; file absent after success, after each failure path and after cancel; startup sweep; two concurrent attempts. The isolation test asserts no `-v` mount contains the prompts directory. The ownership request excludes it. |
| REQ-07 | Same | **Secrecy.** Prompt bytes never appear in: the `docker` argv (so not in `ps` or `docker inspect`), the Herdr launch script, the tmux command string, runner logs (`jr.logger` at any level), job events, `job_finished.failure_message`, sanitized logs or artifacts. A test runs the REQ-01(a) job with a unique 32-byte sentinel in the description. It captures the logger output, all sent job events, the Herdr script content and the tmux command, and asserts the sentinel is absent from all of them. A separate assertion covers the prompt file path: it may appear only in the supervisor redirect, never in events. | Daemon and supervisor tests with captured outputs. |
| REQ-08 | BJAP REQ-08 | **Byte-identical prompt content.** For a fixed job, the bytes delivered after the change equal `buildPrompt()` at baseline. `TestPlanPromptsAreUnchanged` and the approved-plan prompt tests pass unmodified. The prompt is built once per attempt, and both the direct and Docker spec use the same string. No truncation or "see platform_get_job_context" substitution is added, because full delivery makes bounding unnecessary up to the product maximum. The existing pointer sentence to `platform_get_job_context` stays as it is. | Existing prompt tests unchanged; a new test comparing stdin bytes to `buildPrompt()`. |
| REQ-09 | Regression | Jobs without a large prompt behave as before: the plan checkout hand-off, the composition checks (`compositionPluginLoaded(spec.Args)` still sees `--plugin-dir`), the provider CLI gate, resume session IDs, the MCP bridge and capacity classification. The full `go test ./...`, `go test -race ./...`, `go vet ./...`, gosec (no new findings beyond the inherited 11) and govulncheck stay green. | The gates listed in the verification plan. |

**Dimensions marked inapplicable:**

- UI and prototype: no screen is involved. This is runner-internal, and the user-visible result is a job that starts.
- Accessibility: no UI.
- Authorization: no actor-facing endpoint changes. The job authority, the lease and the fencing are untouched.
- Persistence: none beyond the transient prompt file.
- Cardinality: one prompt per attempt. Many concurrent attempts are covered in REQ-06. Many repositories per job do not change the prompt.

## Design and decisions

| ID | Decision | Rationale |
| --- | --- | --- |
| D1 | Deliver the prompt on **stdin** from a runner-private host file. Reject the alternatives: a file in the job home read by the provider, a provider `--prompt-file` option, and chunked arguments. | Stdin has no per-argument or `ARG_MAX` limit. `docker run -i` (already in `Docker.Wrap`) forwards host stdin into the container, so the job user never needs file access. The job home is bind-mounted read-write and owned by the job user after hand-off, so a file there would be readable and modifiable by the agent and its sandboxed commands. Claude Code 2.1.299 has no prompt-file option for the user prompt, and `--system-prompt-file` would change semantics. All three CLIs already accept stdin prompts in this repository's prompt-only path. |
| D2 | Add `Spec.StdinFile string` instead of reusing `Spec.Stdin io.Reader`. | Tmux and Herdr launch the provider through a shell in another process, and an in-memory reader cannot cross that boundary. A path works for every supervisor: direct opens it, and tmux and Herdr add a redirect. `Stdin` stays for the prompt-only path. Setting both is refused. |
| D3 | Remove the file after `Wait`, not right after `Start`. Sweep at startup. | Tmux and Herdr open the redirect asynchronously after `Start` returns, so an early unlink races. Unlinking a file whose descriptor is already open is safe on Linux, so the startup sweep cannot break a re-adopted session. |
| D4 | Mirror the app field limits in the runner, add a 1 MiB assembled ceiling, and reuse `agent_start_failed` with a fixed message. | Validating at the boundary is required by `AGENTS.md`. 1 MiB is above the about 622 KiB worst case and below the 2 MiB envelope. A new failure code would need an app protocol change, which is out of scope (see Dependencies). |
| D5 | Build the prompt once per attempt. | It removes the duplicate `buildPrompt` call (`job.go:480`, `job.go:549`) and guarantees that the checked bytes are the delivered bytes. |
| D6 | Claude passes `--input-format text` explicitly. | It matches the prompt-only path and an option the doctor already requires, and it does not depend on the CLI's default. |

**How the doctor and startup gate verify the CLI options:**

- `isolation.ProbeProviderCLI` (`internal/isolation/provider_cli_probe.go`) runs a probe container from the job image as the job user, with `--network none`, `--read-only` and no logs. It runs `sh -c '"$0" --version && printf "%s\n" "----" && "$0" --help' claude`.
- `provider.DeclaredCLIOptions` (`internal/provider/cli_options.go:26`) parses help lines that start with `  -` into long option names. `MissingCLIOptions` compares them with `Claude.RequiredCLIOptions()`.
- The daemon runs the probe at startup (`verifyDockerProviderCLIs`, `internal/daemon/provider_cli_gate.go`) and refuses Docker jobs whose image was not verified (`providerCLIVerified`, `job.go:528`). `our-runner doctor` runs it too (`internal/doctor/provider_cli_check.go:70`).
- The probe proves an option is declared, not that stdin is read. REQ-03 supplies the behavioral proof.

**Evidence that the CLI supports stdin delivery:**

- `internal/provider/testdata/claude-help-2.1.299.txt` line 1: `Usage: claude [options] [command] [prompt]`, so the prompt is optional.
- Same file, line 123: `--input-format <format>  Input format (only works with --print): "text" (default), or "stream-json"`.
- Same file, line 170: `-p, --print  Print response and exit (useful for pipes)`.
- `--input-format` and `--print` are already in `claudeRequiredCLIOptions`, which the daemon gate enforces against the job image.
- The work-intake path already runs `claude -p --input-format text ...` with the prompt on stdin (`provider.go:365-372`).
- Locally, Claude 2.1.312 shows the same help text. It is not the pinned version and is noted only as corroboration.
- Codex 0.153.4 `exec --help`: "[PROMPT] ... instructions are read from stdin". `exec resume --help`: "[PROMPT] ... If `-` is used, read from stdin".
- OpenCode 1.2.27 `run --help` does not document stdin. Only the existing prompt-only usage supports it, so REQ-04 carries a gated check and a return-to-planner rule.

## Implementation plan

1. [x] **Test first: reproduce the baseline failure.** Add the `E2BIG` test in `internal/supervisor` (reproduction plan step 1) and the 256 KiB daemon test (step 2). Confirm both fail at `d07f644`. Record the output in the verification table.
2. [x] **`internal/supervisor` (REQ-02 d/e, REQ-07).**
   - Add `StdinFile`.
   - `DirectProcess` opens the file with `O_RDONLY|O_NOFOLLOW`, checks it is a regular file and closes it after `Wait`.
   - Tmux and Herdr add the quoted redirect.
   - All three refuse `Stdin` and `StdinFile` together.
   - Tests: tmux string length, Herdr script content without the prompt, the direct child reads all bytes.
3. [x] **`internal/provider` (REQ-02 a/b, REQ-04, REQ-08).**
   - `StartParams.Prompt` stays the prompt. `BuildSpec` stops putting it in args and returns a spec whose prompt is delivered through stdin. Choose one way and document it: either `BuildSpec` sets `Stdin` and the daemon converts it to a file, or the daemon sets `StdinFile` and `BuildSpec` only drops the argument. Prefer the second, so specs stay free of I/O.
   - Change the Claude, Codex and OpenCode arguments as REQ-02(b) says.
   - Regenerate the goldens with `-update-provider-goldens` and review the diff by hand.
   - Update `prompt_only_test.go` and the option-collection test only if they break. Prompt-only behavior must not change.
4. [x] **`internal/daemon` (REQ-01, REQ-05, REQ-06, REQ-07, REQ-08).**
   - Put the input-limit check in a new cohesive file, for example `agent_prompt_bounds.go`, called before `prepareWorkspace`.
   - Build the prompt once, enforce `maxAgentPromptBytes`, write the prompt file in `<state_dir>/supervisor/prompts/` (a new file, for example `agent_prompt_file.go`, using `safeio`), set `spec.StdinFile` after `iso.Wrap`, defer removal and keep the path out of `isoPolicy` and `OwnershipRequest`.
   - Add the startup sweep where the daemon initializes its state directory.
   - Write tests per REQ.
5. [x] **`internal/isolation`.** Add an assertion that `Docker.Wrap` keeps `-i` and never mounts the prompts directory. Add the gated test `TestClaudeReadsALargePromptFromStdinInTheJobImage`, with the `E2BIG` argv control case, by extending the offline Messages API harness to record the first user message (REQ-03).
6. [x] **Docs.**
   - README: update the Claude weaker-sandbox paragraph (prompts are no longer on command lines) and add a short "Prompt delivery" note covering stdin, the private file, the limits and the refusal message.
   - `docs/TESTING.md`: add the new gated test.
   - Update this record's implementation report and verification table.
7. [x] **Run every gate** in the verification plan, set `review_ready` and hand off to a fresh reviewer. Do not commit unless the coordinator asks.

**Owned files:**

- `internal/supervisor/{supervisor.go,tmux.go,herdr.go}` and their tests.
- `internal/provider/{provider.go,opencode.go}`, `internal/provider/testdata/baseline-*.golden.json` and the provider tests.
- `internal/daemon/job.go`, new `internal/daemon/agent_prompt_*.go` files and tests, and the daemon startup sweep site.
- `internal/isolation/isolation_test.go` and the new gated test file.
- `README.md` and `docs/TESTING.md` (relevant paragraphs only).
- This record.

**Repository rules that apply:**

- No code comments.
- One concept per file.
- Validate input at the boundary.
- Do not weaken assertions.
- Preserve the edits of other agents. Other worktrees exist, and this one is owned by this slice.

**Known unrelated failures:** the flaky Go tests `TestAgentProcessNeverReceivesProviderCredential`, `TestNonPushingJobAcceptsRepositoryReadCredential` and `TestScanGitCancellationKillsChildrenAfterLeaderExits` (owned by FR-12), and the 11 inherited gosec findings (FR-12). Report them; do not fix them here.

## Verification plan and results

| Command / scenario | Required for which IDs | Actual result / date | Evidence path | Baseline failure attribution if relevant |
| --- | --- | --- | --- | --- |
| Baseline `E2BIG` reproduction (supervisor and daemon tests at `d07f644`) | REQ-01, REQ-02 | Failed as expected, 2026-10-28. The argv variant of `TestDirectProcessStartsWithAPromptOverTheSingleArgumentLimit` returned an error wrapping `syscall.E2BIG`. `TestProductMaximumPromptsReachTheProviderOnStdinByteForByte` and `TestPromptBytesStayOutOfLogsEventsAndCommandLines`, run against a scratch export of `d07f644`, failed with `agent_start_failed` (large cases) and 0 received stdin bytes (case d). | implementer session; scratch export `/tmp/claude-1000/-home-omar-Code-Automated-Development-app/30c6447b-7fdc-4b3f-8101-5e8a52ec03f5/scratchpad/fr08-baseline` | expected baseline failure |
| Focused suite (`-run 'Prompt\|AgentInput\|Stdin\|TestDockerWrapForwardsStdin\|TestPlanPromptsAreUnchanged\|ApprovedPlan\|Composition'` over daemon, provider, supervisor and isolation) | REQ-01, REQ-02, REQ-04 to REQ-08 | Pass, 2026-10-29: 48 top-level tests pass, 0 fail. `TestPlanPromptsAreUnchanged` and the approved-plan prompt tests are unmodified. | implementer session | |
| `GOTOOLCHAIN=go1.26.10 go test ./... -count=1` | REQ-09 | Pass, 2026-10-29. The first full run caught `TestCompositionAttemptLeavesTheWorktreeUntouchedAndReportsBothPhases`, which read the prompt from the `-p` argument. It now reads the prompt from `spec.StdinFile` while the provider runs, with the same `Contains` assertion. On the rerun, every package passes. The three known flaky tests did not fail. | implementer session | none needed |
| `go test -race -count=1` for supervisor, provider, daemon, isolation and validation | REQ-06, REQ-09 | Pass, 2026-10-29; no data races | implementer session | |
| `gofmt -l .`, `go vet ./...` | REQ-09 | Pass, 2026-10-29. gofmt lists no files. | implementer session | |
| `go test -cover` for touched packages | REQ-09 | 2026-10-29: provider 87.1%, daemon 85.6%, isolation 89.6%, validation 85.9%, **supervisor 75.5%**. Supervisor was 73.5% at `d07f644` (measured on the baseline export), so this slice raised it by 2.0 points. The remaining gap is inherited: `HerdrSupervisor.Start` is 42.9%, because most of it needs a live Herdr server. The new `stdin_file.go` is 100%, 81.8% and 100% per function. | implementer session | inherited below-80% supervisor coverage (not introduced here) |
| `~/go/bin/gosec -fmt json -out .../scratchpad/fr08-gosec.json ./...` | REQ-06, REQ-09 | 2026-10-29: 11 findings, the inherited set (G304×5, G302×2, G115, G122, G204, G404), at unchanged locations, none in files this slice touched. No new `#nosec`. | `/tmp/claude-1000/-home-omar-Code-Automated-Development-app/30c6447b-7fdc-4b3f-8101-5e8a52ec03f5/scratchpad/fr08-gosec.json` | inherited 11 (FR-12) |
| `GOTOOLCHAIN=go1.26.10 ~/go/bin/govulncheck ./...` | REQ-09 | Pass, 2026-10-29: the code is affected by 0 vulnerabilities. One vulnerability is in a required module, but no code path calls it. | implementer session | |
| `DOCKER_HOST=unix:///run/user/1000/docker.sock RUN_DOCKER_SANDBOX_PROBE=1 JOB_IMAGE=our-runner-job:rjic@sha256:a2c8cabef3dbe9d4044bf2bcb2f7031aadbeee24ac0bd0dce8b41b5e900e8382 go test ./internal/isolation/ -run 'StdinInTheJobImage' -v -count=1` | REQ-03, REQ-04 | Pass, 2026-10-29. **Claude 2.1.299:** the argv control failed with `E2BIG`. The first request's user text equals the 262317-byte prompt (bytes and SHA-256), in 1 request with 2 text blocks. Claude exited 0. **OpenCode 1.18.13:** the first Chat Completions request carries the exact 262323-byte prompt (bytes and SHA-256), in 2 requests. OpenCode exited 1 because the fake endpoint refuses every request. No labelled container remains after either test. | implementer session | |
| Same gate, `-run 'ProviderCLIProbeIntegration\|TestJobImageSandboxRuntimeStartsUnderTheNestedSandboxFlags\|TestClaudeBash'` | REQ-03, REQ-09 | Pass, 2026-10-28, including the two `TestClaudeBash` cases, whose prompt now travels on stdin. The `INCOMPATIBLE_JOB_IMAGE` case is skipped because that variable was not set. `TestClaudeRequiredCLIOptionsMatchEveryOptionTheRunnerPasses` passes in the full suite. | implementer session | |
| `RUN_PROVIDER_STDIN_SMOKE=1 go test ./internal/provider/ -run StdinSmoke -v -count=1` | REQ-04 | Pass, 2026-10-29, with local `codex-cli 0.153.4`. A new session sent the full 200 KiB stdin prompt to the endpoint. A second run, `exec ... resume <real thread_id> -`, sent a fresh 200 KiB stdin prompt; the thread ID came from the first run's `thread.started` event in a shared `CODEX_HOME`. OpenCode is covered by the job-image test above: the local `opencode 1.2.27` rejects the runner's `--auto` and `--pure` flags, so it cannot represent the shipped CLI. | implementer session | |

## Implementation report

**Changed paths (runner worktree `wt/runner-fr-08`, uncommitted):**

- Supervisor:
  - `internal/supervisor/supervisor.go`: `Spec.StdinFile`. `DirectProcess.Start` validates the stdin source and opens the file as the child's stdin.
  - `internal/supervisor/stdin_file.go` (new): `validateStdinSource`, `openStdinFile` (`O_RDONLY|O_NOFOLLOW`, regular file only) and `stdinRedirect`.
  - `internal/supervisor/tmux.go` and `internal/supervisor/herdr.go`: refuse a conflicting stdin source and add the quoted `<` redirect.
  - `internal/supervisor/stdin_file_test.go` (new).
- Provider:
  - `internal/provider/provider.go`: Claude uses `-p --input-format text`; Codex uses a trailing `-`, also after `resume <id>`. Both set `Stdin`.
  - `internal/provider/opencode.go`: no message argument; sets `Stdin`.
  - 10 goldens, `internal/provider/testdata/baseline-*.golden.json`, regenerated and reviewed by hand. The prompt argument is replaced by `--input-format text` (Claude), `-` (Codex) or nothing (OpenCode), and `"stdin": "do the ticket"` is added.
  - `internal/provider/job_prompt_stdin_test.go` (new).
  - `internal/provider/stdin_smoke_test.go` (new, gated Codex smoke).
- Validation:
  - `internal/validation/agent_prompt_input_limits.go` (new): exported field-limit constants.
  - `internal/validation/validation.go`: `JobSpec` uses those constants instead of literals. Behavior is unchanged.
- Daemon:
  - `internal/daemon/agent_prompt_bounds.go` (new): field bounds, the 1 MiB ceiling and the fixed refusal message.
  - `internal/daemon/agent_prompt_file.go` (new): the private prompt file and the startup sweep.
  - `internal/daemon/job.go`: the bounds check before workspace work; the prompt is built once and checked against the ceiling; the prompt file is attached before start, removed after `Wait` and by a deferred removal on every other path.
  - `internal/daemon/startup_recovery.go`: the sweep.
  - `internal/daemon/agent_prompt_delivery_test.go`, `agent_prompt_bounds_test.go`, `agent_prompt_file_test.go` (new).
  - `internal/daemon/job_composition_test.go`: reads the prompt from `StdinFile` instead of `-p`, with the assertion unchanged.
- Isolation:
  - `internal/isolation/isolation_test.go`: `TestDockerWrapForwardsStdinAndAddsNoPromptArgument`.
  - `internal/isolation/claude_weaker_sandbox_integration_test.go`: the offline Claude harness feeds its prompt on `docker run -i` stdin.
  - `internal/isolation/claude_stdin_prompt_integration_test.go` and `internal/isolation/opencode_stdin_prompt_integration_test.go` (new, gated).
- Docs:
  - `README.md`: a new "Prompt delivery" section, and the weaker-sandbox paragraph no longer lists prompts among command-line contents.
  - `docs/TESTING.md`: the stdin job-image probes and the Codex smoke.
  - This record.

**Decisions and deviations:**

- **I1 (deviation from the plan's "prefer second").** `BuildSpec` sets `Spec.Stdin` to a reader over the prompt. The daemon drains it, bounded by the limit plus one, into the private file. It then sets `Stdin = nil` and `StdinFile = <path>`. Reasons:
  - REQ-02(a) requires that each adapter's stdin source yields the prompt.
  - The goldens gain the stdin content.
  - It is the same pattern as the prompt-only path.
  - The adapters stay free of file I/O.
- **I2.** `DirectProcess` closes its copy of the stdin file right after `Start`, not after `Wait`. The child holds its own descriptor, so the parent's copy is not needed. The plan's "close after `Wait`" would only keep a descriptor open longer. File removal still happens after `Wait` (D3).
- **I3 (contract correction).** The "Current behavior" claim that the runner never validates ticket field sizes is wrong. `internal/validation.JobSpec` already enforces the same limits at `start_job`, as does the protocol limit for continuation content. The in-run check (REQ-05) is defense in depth on the same constants, so this slice moved them to exported constants shared by both checks. A job that is over a limit is therefore normally refused at `start_job`. The daemon tests prove the in-run refusal by calling the job runner directly.
- **I4.** Both refusals reuse `agent_start_failed` with `agent prompt exceeds the runner size limit`, as the coordinator decided. The field check runs at the top of the run, before the job directory, workspace or any progress event. The ceiling check runs after workspace preparation, because the prompt embeds the pack path, and before `BuildSpec` and any isolator call.
- **I5.** `jobRunner.agentPromptLimit` is a test seam, because product-limited fields cannot reach a 1 MiB prompt. Production uses `maxAgentPromptBytes`.
  - The predicate is tested at exactly 1 MiB (accepted) and 1 MiB + 1 (refused).
  - The run path is tested with the seam set to the prompt length (start) and the prompt length − 1 (refusal).
  - A test asserts that the default limit is `maxAgentPromptBytes` = `1<<20`.
- **I6.** The prompts directory is opened through `os.Root`. A symlinked `prompts` directory is refused, and a wider mode is narrowed to `0700`. Files are `O_EXCL` `0600`, named `prompt-<attempt>-<random hex>`. Cleanup failures are logged with no path or prompt detail.
- **I7.** REQ-02(f): the tmux command-string size is asserted at the supervisor level. That test uses a 200 KiB-plus prompt, keeps the command under 16 KiB with no prompt bytes, and runs the command through `sh` to prove the redirect delivers the prompt. The test is not repeated at daemon level.
- **I8.** REQ-04 OpenCode is proven in the shipped job image (OpenCode 1.18.13), not locally. The local 1.2.27 predates `--auto` and `--pure`. The return-to-planner rule did not trigger.
- **I9.** The existing `TestClaudeBash` offline harness had to move to stdin, because the prompt is no longer an argument. Its assertions are unchanged.

**Candidate coverage for the reviewer:**

- REQ-01: `TestProductMaximumPromptsReachTheProviderOnStdinByteForByte`, cases a–d.
- REQ-02:
  - (a, b) `TestJobSpecsDeliverThePromptOnStdinOnly` and the goldens.
  - (c) `TestDockerWrapForwardsStdinAndAddsNoPromptArgument`.
  - (d) `TestDirectProcessStartsWithAPromptOverTheSingleArgumentLimit`, `TestDirectProcessRefusesUnsafeStdinFiles`, `TestTmuxRedirectsTheStdinFileIntoTheProviderCommand` and `TestHerdrLaunchScriptRedirectsTheStdinFile`.
  - (e) `TestEverySupervisorRefusesStdinAndStdinFileTogether`.
  - (f) the tmux test.
- REQ-03: `TestClaudeReadsALargePromptFromStdinInTheJobImage` (gated).
- REQ-04: `TestOpenCodeReadsALargePromptFromStdinInTheJobImage` (gated) and `TestCodexStdinSmokeReadsTheJobPromptFromStdin` (gated).
- REQ-05: `TestAgentInputLimitsAcceptTheMaximumAndRefuseOneByteMore`, `TestAgentPromptCeilingAcceptsOneMebibyteAndRefusesOneByteMore` and `TestPromptRefusalUnderDockerNeverReachesTheIsolatorOrSupervisor`.
- REQ-06: `TestPromptFileIsPrivateWhileTheProviderRunsAndRemovedAfterwards`, `TestDockerPromptFileIsNeverMountedOrHandedOff`, `TestPromptFileIsRemovedOnEveryExitPath`, `TestConcurrentAttemptsGetDistinctPromptFiles`, `TestPromptFileRefusesAnUnsafePromptsDirectory` and `TestStartupSweepsLeftoverPromptFiles`.
- REQ-07: `TestPromptBytesStayOutOfLogsEventsAndCommandLines`, plus the tmux and Herdr supervisor tests.
- REQ-08: the REQ-01 SHA comparison against `buildPrompt()`, and `TestPlanPromptsAreUnchanged` (unmodified).
- REQ-09: the full gates.

**Open items:**

- Supervisor package coverage is 75.5%. That is below 80%, but up from 73.5% at baseline; the gap is in the Herdr live path.
- The OpenCode local smoke was dropped in favour of the job-image test.

## Reviewer requirement review

The reviewer worked from a fresh context on 29 October 2026. The review covered `git diff d07f644` and the untracked files in `wt/runner-fr-08`, read against this contract, app `PRODUCT.md` (line 3459: command lines and prompts may contain secrets), app `src/server/runner-protocol/v1/shared/protocol-limits.ts` and the BJAP record. There is no UI, prototype screen or app runtime surface, so no browser verification applies.

| ID | Status | Source evidence | Test / runtime evidence |
| --- | --- | --- | --- |
| REQ-01 | implemented | `job.go` builds the prompt once (`prompt := jr.buildPrompt()`) and passes it to both `BuildSpec` calls. `attachAgentPromptFile` (`agent_prompt_file.go:95`) drains the spec reader into the private file and sets `StdinFile`. `DirectProcess.Start` opens that file as the child's stdin (`supervisor.go:111-119`). | `TestProductMaximumPromptsReachTheProviderOnStdinByteForByte` covers cases a–d through the real `DirectProcess`. The fake `claude` runs `cat > received`, and the test checks the byte count, the SHA-256 against `buildPrompt()` and every sentinel. It passed in the full suite. |
| REQ-02 | implemented | Claude `-p --input-format text` (`provider.go:309`). Codex trailing `-` (`provider.go:159`), also after `resume <id>`. OpenCode has no message (`opencode.go`). All three set `Stdin`. `validateStdinSource` is called in all three supervisors. `openStdinFile` uses `O_RDONLY\|O_NOFOLLOW` and accepts only a regular file. `stdinRedirect` places the quoted `<` before `> log 2>&1` (tmux) and inside the subshell before `) >` (Herdr). `shellQuote` uses standard single-quote escaping, and the path is built from the state dir, a validated attempt ID (`^[A-Za-z0-9][A-Za-z0-9._-]*$`) and hex. The work-intake and resume paths go through the same `BuildSpec`; no other job start path exists (grep of `BuildSpec(` and `supervisor.Start(`). | `TestJobSpecsDeliverThePromptOnStdinOnly` covers 3 adapters × Plan/Build × resume × hardened × docker. The 10 goldens were checked by hand: only the prompt argument and the `stdin` field changed. `TestDockerWrapForwardsStdinAndAddsNoPromptArgument`, `TestDirectProcessStartsWithAPromptOverTheSingleArgumentLimit`, `TestDirectProcessRefusesUnsafeStdinFiles` (symlink, directory, missing, relative, both), `TestTmuxRedirectsTheStdinFileIntoTheProviderCommand` (the command is under 16 KiB with no prompt bytes and delivers the prompt when run through `sh`), `TestHerdrLaunchScriptRedirectsTheStdinFile` and `TestEverySupervisorRefusesStdinAndStdinFileTogether`. |
| REQ-03 | implemented | `--print` and `--input-format` remain in `claudeRequiredCLIOptions` (`cli_options.go:13`); that file is unchanged. | The reviewer ran the gate (`DOCKER_HOST=unix:///run/user/1000/docker.sock RUN_DOCKER_SANDBOX_PROBE=1 JOB_IMAGE=our-runner-job:rjic`). `TestClaudeReadsALargePromptFromStdinInTheJobImage` passed. The argv control failed with `E2BIG`. The first request carried the 262317-byte prompt with a matching SHA-256 in one of 2 text blocks, and Claude exited 0. `TestClaudeBash*` (2 cases) passed. No container with the `fr08-stdin-prompt` label remains. |
| REQ-04 | implemented | The Codex and OpenCode argument changes are listed under REQ-02. | The reviewer ran `TestOpenCodeReadsALargePromptFromStdinInTheJobImage` with OpenCode 1.18.13 in the job image: the 262323-byte prompt arrived with a matching SHA-256 and the test passed. The reviewer did not rerun the Codex stdin smoke (`RUN_PROVIDER_STDIN_SMOKE=1`). The implementer's run with local codex-cli 0.153.4, new and resumed sessions, stands alongside `codex exec --help` and the unit tests. |
| REQ-05 | implemented | `agentInputWithinBounds` (`agent_prompt_bounds.go:13`) runs at the top of `run()`, before `MkdirAll` and before any progress event. The continuation is set when the job runner is constructed (`job.go:234`), so it is present at that point. `agentPromptWithinLimit` runs after `prepareWorkspace` and before `BuildSpec` (I4). The refusal message is fixed. The constants in `internal/validation` match the app protocol limits. | `TestAgentInputLimitsAcceptTheMaximumAndRefuseOneByteMore` covers 7 fields at the limit and one byte over. It checks that there is no start, no progress event, no input text in the logs and no prompt file. `TestAgentPromptCeilingAcceptsOneMebibyteAndRefusesOneByteMore` tests the predicate at exactly 1 MiB and 1 MiB + 1, checks the default limit and exercises the run path at the limit and at the limit − 1. `TestPromptRefusalUnderDockerNeverReachesTheIsolatorOrSupervisor` passed. |
| REQ-06 | implemented | The prompts directory is opened through `os.Root` (`agent_prompt_file.go:29-53`). A symlink is refused because `Lstat` plus `OpenRoot` do not follow it out of the root. The directory mode is narrowed to `0700`. Each file is `O_EXCL` `0600` and named `prompt-<attempt>-<128-bit hex>`. The file is attached after the ownership hand-off and kept out of `OwnershipRequest` (`job.go:647-651`), and it is never in `isoPolicy`. A deferred removal covers every exit after creation, and an explicit removal runs after `Wait`. The sweep runs in `recoverStartupJobState` after the daemon lock is held (`daemon.go:307`), for both values of `cleanupJobs`. | `TestPromptFileIsPrivateWhileTheProviderRunsAndRemovedAfterwards`, `TestDockerPromptFileIsNeverMountedOrHandedOff` (no `-v`, `--volume` or `--mount` entry and no argv element references the file or directory; no hand-off or reclaim root covers it), `TestPromptFileIsRemovedOnEveryExitPath` (7 paths), `TestConcurrentAttemptsGetDistinctPromptFiles`, `TestPromptFileRefusesAnUnsafePromptsDirectory` and `TestStartupSweepsLeftoverPromptFiles`. The BuildSpec and wrap failures happen before the file exists, so there is nothing to remove on those paths. |
| REQ-07 | implemented | The supervisor `Start` error, which can contain the path, is not logged (`job.go:689-699`). The cleanup warning carries no path. The refusal message is fixed. | `TestPromptBytesStayOutOfLogsEventsAndCommandLines` covers logs at debug level, events, argv and environment, plus the prompt path in logs and events. The tmux and Herdr tests check that the command string and the launch script contain no prompt bytes. |
| REQ-08 | implemented | The prompt is built once. Wording is unchanged and no truncation was added. | `approved_plan_prompt_test.go`, which contains `TestPlanPromptsAreUnchanged`, is unchanged since `d07f644` (`git diff --stat` is empty). The REQ-01 SHA-256 comparison against `buildPrompt()` passed. |
| REQ-09 | implemented | Composition still reads `spec.Args` for `--plugin-dir`. The one test change in `job_composition_test.go` reads the prompt from `StdinFile`, and its `Contains` assertion is unchanged. | See the reviewer gate results below. |

**Checks the reviewer ran (29 October 2026, `GOTOOLCHAIN=go1.26.10`):**

- `gofmt -l .` listed no files, and `go vet ./...` passed.
- `go test ./... -count=1`: 4648 tests passed in 46 packages.
- `go test -race -count=1` on supervisor, provider, daemon, isolation and validation: 2305 passed and 1 failed. The failure was the known flaky `TestAgentProcessNeverReceivesProviderCredential`: a `job-results/.../repo.git/logs` entry disappeared while the test walked the state tree, which is unrelated to prompt delivery. Rerun alone with `-race -count=5`, it passed.
- Coverage by package:

  | Package | Coverage |
  | --- | --- |
  | supervisor | 75.5% |
  | provider | 87.1% |
  | daemon | 85.5% |
  | isolation | 89.6% |
  | validation | 85.9% |

- gosec (`/tmp/claude-1000/-home-omar-Code-Automated-Development-app/30c6447b-7fdc-4b3f-8101-5e8a52ec03f5/scratchpad/fr08-gosec-review.json`) reported 11 findings. All are the inherited set (G115, G404, G122, G204, G304×5, G302×2), none is in a file this slice touched, and no `#nosec` was added.
- govulncheck: the code is affected by 0 vulnerabilities.
- The gated job-image tests are listed under REQ-03 and REQ-04.

**Assessment of the implementer's deviations:**

- **I1 (sound).** `attachAgentPromptFile` reads at most the limit plus one byte, refuses anything over the limit, and then clears `Stdin` before setting `StdinFile`, so the supervisor conflict check cannot fire on a job spec. The reader is a `strings.Reader` over the same string, so the checked bytes are the delivered bytes.
- **I2 (sound, no race).** `cmd.Stdin` is an `*os.File`, so Go hands the descriptor to the child directly, with no copy goroutine, and `Wait` does not use it. Closing the parent's copy after `Start` is correct, and an early return between open and `Start` is impossible. The file is still removed only after `Wait`, which D3 requires for the asynchronous tmux and Herdr redirects.
- **I3–I9** are accepted as recorded.

## Review findings and repair

No blocking findings. Repair attempts: 0.

Non-blocking follow-ups:

- **N1. Supervisor coverage is 75.5%, below the 80% target.**
  - Evidence: the baseline was 73.5%, and this slice raised it.
  - The new `stdin_file.go` is fully exercised, and the gap is in the inherited Herdr live path (`HerdrSupervisor.Start`, 42.9%).
  - The reviewer judges this non-blocking because the slice did not cause the shortfall.
  - Follow-up: raise Herdr coverage with a fake-server test (FR-12 or a dedicated hardening slice).
- **N2. The startup sweep and `Remove` use path-based `os.ReadDir` and `os.Remove`, not `os.Root`.**
  - If the `prompts` directory were swapped for a symlink, the sweep would delete files whose names start with `prompt-` in the symlink's target.
  - Exploiting this needs write access to the runner-owned `0700` `supervisor` directory, so the impact is low.
  - Suggested hardening: run the sweep through `openAgentPromptDirectory` (`root.ReadDir`/`root.Remove`).
- **N3. Tmux and Herdr apply the shell `<` redirect without the `O_NOFOLLOW` and regular-file checks that `DirectProcess` makes.**
  - The contract requires those checks only for the direct supervisor.
  - Safety relies on the daemon creating the file itself in the private directory.
  - Possible hardening: validate the file with `Lstat` before launching.

## Completion

- Decision: complete (`done`). Every REQ from REQ-01 to REQ-09 is `implemented` with evidence, and no finding blocks.
- Verified by: develop-reviewer (Opus 5.5, fresh context), 29 October 2026.
- Gate: every REQ-01 to REQ-09 `implemented` with evidence, no blocking findings, gates green apart from the attributed flaky and inherited items.
- Remaining parent-feature or excluded work:
  - A dedicated prompt-size failure code would need an app protocol change (future, optional).
  - BJAP L-1 runner digest rule (deferred).
- Product/checkpoint links updated: none yet. On completion the coordinator updates app `docs/implementation-progress.md` FR-08 and the "Known follow-ups" row "Ticket description inlined in argv beyond 128 KiB". This planner does not edit the app repository.

## Subsequent changes

None.
