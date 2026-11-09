# Runner tool names in sanitized logs (FD-3)

## Control

| Field                  | Value |
| ---------------------- | ----- |
| ID                     | FR-01 follow-up FD-3 `runner-hook-tool-names` (RHTN) |
| Kind                   | bug (missing evidence: platform logs cannot say which tool ran or failed) |
| Status                 | review_ready (REQ-01 to REQ-13 implemented with evidence; REQ-14 unverified, coordinator after merge) |
| Risk                   | critical. The slice widens what agent-derived data leaves the runner for the control plane. Today a sanitized log carries only the provider event type and a fixed status word. After the slice it also carries tool names. A defect could forward tool arguments, tool output, file paths or credentials that the sanitizer removes today. No authentication, tenant, persistence or execution-authority change. |
| Complexity             | standard. One new pure package, one changed emitter, one log line, a gated test that reuses the existing offline Claude probe, and docs. No protocol, app or migration change. |
| Parent scope           | App `docs/development/build-job-approved-plan-delivery.md`, section "FR-01 Docker Build check (9 November 2026)", finding FD-3 ("Hook events and sanitized logs carry no tool names, so a reviewer cannot tell from platform evidence which tool ran or failed"), and "Follow-up: RDBC live Docker Build on KAN-25" ("FD-3: still a note"). App `docs/implementation-progress.md`, entries "FR-01 Docker-isolated Build check" and "RDBC runner Docker Build checks" (follow-up list). This repository: `docs/development/runner-docker-build-checks.md` D-4 (FD-3 deferred as FU-3). |
| Product sources        | `PRODUCT.md` agent run evidence (sanitized logs are runner-redacted, bounded and non-authoritative) and the isolation baseline (provider output and repository code are untrusted). README "Hook telemetry is occurrence-only" (line 1125). |
| Planner                | Opus 5.5 (`claude-opus-5-5`); develop-planner subagent, FD-3 planning session, 9 November 2026 |
| Implementer            | develop-implementer, Opus 5.5 (`claude-opus-5-5`), 9 November 2026 |
| Reviewer               | not assigned |
| Repair attempt         | 0 |
| Updated                | 2026-11-09 |
| Baseline               | app-runner `main` at `67eeac7`, worktree `.claude/worktrees/fd3-hook-tool-names`, branch `fd3-hook-tool-names`. This record is the only change. App `main` at `abf145dd`, unchanged. |

## Outcome and current gap

**Outcome.** A reviewer opening a Build's Activity panel, or the stored `sanitized_log` rows, can tell which tool the agent called and whether each call succeeded or failed: for example "Tool call: Bash" followed by "Tool result: Bash succeeded". The entries never carry a tool's arguments, output, file paths or credentials.

**Current code at `67eeac7` (evidence):**

- Hook events. The managed hook script runs `our-runner hook-event --type <event>` with stdin from `/dev/null` (`internal/provider/hooks.go:102-107`). `cmdHookEvent` reads stdin but sends only `event_type` and an idempotency key (`cmd/our-runner/main.go:402-440`). The daemon refuses any other field ("hook telemetry field %q is not allowed", `internal/daemon/tools.go:212-235`). The spooled `hookspool.Event` and the wire payload (`internal/daemon/hook_event.go`) contain only `hook_type` and authority fields. `mcpbridge.ValidateHookEventType` documents why: "Hook payloads are deliberately not accepted because repository code and provider output are untrusted and may contain source or credentials" (`internal/mcpbridge/protocol.go:337-342`). The README says the same (line 1125).
- App acceptance of hook events. `hookEventSchema` is a `z.strictObject` with `kind`, the manifest-bound fields and `hook_type` only (app `src/server/runner-protocol/v1/events/hook-event.schema.ts`). Any new runner field is rejected. The Activity UI shows hook events only as the kind `HOOK_EVENT`; it never renders `hook_type`.
- Sanitized logs. `consumeOutput` decodes each provider stdout JSON line and calls `publishSanitizedAgentStatus` (`internal/daemon/job.go:1131-1173`). That function emits source `agent_status`, summary "Agent status: <type>" with `type` from a fixed allowlist, and optional detail "Status: <status>". Every other top-level field is dropped and counted as a redaction (`internal/daemon/sanitized_log.go:19-104`). For Claude's `stream-json`, tool calls arrive as `type: "assistant"` with `message.content[]` blocks `{type: "tool_use", id, name, input}`, and results as `type: "user"` with blocks `{type: "tool_result", tool_use_id, content, is_error}`. The repository already parses that result shape in `internal/isolation/claude_weaker_sandbox_integration_test.go:240-274`. So a Bash call today shows as "Agent status: assistant" and its result as "Agent status: user".
- The local runner log line is `agent event type=<type> status=<status>` (`job.go:1158`). It has no tool name either.
- The protocol already has an unused source `tool_summary` (`api/runner/v1/agent_run.go:195`). The app accepts it in the protocol schema, the DB check (`20260904002000_agent_run_sanitized_log_secondary_redaction`) and the UI label (`provider-review-workflow-agent-run-sanitized-logs.tsx`, `sanitizedLogSources.toolSummary`). The app's secondary redaction (`secondary-redact-agent-run-log.ts`) keeps plain identifiers like `Bash` or `mcp__platform__platform_run_check`.
- Caps: at most 512 sanitized logs per job plus one suppression marker (`maxSanitizedLogsPerJob`). Summary at most 1024 bytes.

**What exists versus what is only described.** Exists: the `tool_summary` source end to end, the sanitizer, the cap, the offline Claude Bash probe. Missing: any code that reads `tool_use` or `tool_result` blocks, any tool name in a platform-bound record.

## Scope

**In scope (app-runner only):**

1. A new pure package `internal/toolsummary` that turns one decoded Claude `stream-json` event into a fixed-vocabulary tool summary, and a bounded per-job tracker from `tool_use` IDs to names.
2. `publishSanitizedAgentStatus` emits source `tool_summary` for events that carry tool blocks, and keeps `agent_status` for all other events.
3. The local `agent event` log line gains a `tools` attribute with the same sanitized names.
4. A gated test that runs the existing offline Claude Bash probe and feeds its real stdout through the new code.
5. README and `docs/TESTING.md` updates.

**Out of scope:**

- **Hook events stay unchanged** (D-1). No new field in `hookspool.Event`, the hook-event payload, the MCP internal tool, the managed hook script or the app's `hookEventSchema`.
- Codex and OpenCode output (D-3). Their events keep today's `agent_status` entries.
- Any app repository change. The app already accepts and shows `tool_summary` entries.
- Tool arguments, commands, file paths, exit codes or output in any form.
- Raising the 512-entry cap.

**Dependencies.** None. The app at `abf145dd` already accepts the events this slice emits.

## Acceptance contract

| ID | Source | Requirement (observable) | Evidence |
| -- | ------ | ------------------------ | -------- |
| REQ-01 | FD-3 | **Step 0: confirm the stream shape.** Before writing code, the implementer runs the gated offline probe (`RUN_DOCKER_SANDBOX_PROBE=1 JOB_IMAGE=<reviewed RJIC digest> go test ./internal/isolation -run TestClaudeBashRunsInTheWeakerNestedSandbox -count=1 -v`) with a temporary local print of the raw stdout lines, and pastes into this record (with secrets and host paths removed) the `assistant` line with the `tool_use` block and the `user` line with the `tool_result` block. The record states whether `is_error` is present when the call succeeds, and whether `tool_use_id` equals the `tool_use` block `id`. If the shape differs from "Current code" above, the implementer stops and reports before REQ-02. The temporary print is removed before commit. | Pasted lines in this record |
| REQ-02 | FD-3 | **Tool call summary.** For a decoded event with `type == "assistant"` and at least one `message.content[]` block with `type == "tool_use"`, the runner emits one sanitized log with source `tool_summary` and summary `Tool call: <N1>` for one block, or `Tool calls: <N1>, <N2>, …` for several, in block order. Duplicates are kept (two Bash calls show `Bash, Bash`). Each name is shown as given by D-2, else replaced with `unlisted tool`. When there are more than 8 blocks, the first 8 are named, the summary ends with ` (+<k> more)`, and `truncated` is true. No `detail`. Cardinality: 1, 2, 8 and 9 blocks; an assistant event that has text blocks and one `tool_use` block; an assistant event with no `tool_use` block keeps today's `agent_status` entry byte for byte. | `internal/toolsummary` and `internal/daemon/sanitized_log_test.go` unit tests |
| REQ-03 | FD-3 | **Tool result summary.** For a decoded event with `type == "user"` and at least one block with `type == "tool_result"`, the runner emits one `tool_summary` log: `Tool result: <name> <outcome>` for one block, `Tool results: <name> <outcome>, …` for several, with the same cap of 8 and the same ` (+<k> more)` rule. `<name>` is the name the tracker recorded for that `tool_use_id` in an earlier `tool_use` block of the same job (already reduced by D-2), else `unknown tool`. `<outcome>` is `failed` when `is_error` is the boolean `true`, `succeeded` when it is the boolean `false`, else `returned`. Cardinality: one result; two results with mixed outcomes; a result whose ID was never seen; a result whose ID was seen in another job (still `unknown tool`, since each job has its own tracker); `is_error` missing, `true`, `false` and a string. A user event with no `tool_result` block keeps today's `agent_status` entry. | Unit tests |
| REQ-04 | FD-3, isolation baseline | **Bounded tracker.** The tracker is per job (a field of `jobRunner`), guarded by the existing `procMu`, and holds at most 512 IDs. A new ID beyond the limit is not stored, so its result later shows `unknown tool`. An ID is used only as a map key: it must match `^[A-Za-z0-9_-]{1,128}$` to be stored or looked up, and it never appears in any summary, detail, log line or event. A resumed provider session starts with an empty tracker. | Unit tests: 513th ID not stored; invalid ID ignored; IDs absent from every emitted JSON |
| REQ-05 | Security review | **Nothing else leaves.** For a `tool_summary` entry the summary contains only the fixed words of REQ-02 and REQ-03, names that passed D-2, digits, commas, spaces, parentheses and `+`. A test drives events whose `tool_use` `input` holds a command, a host path, a `ghp_` token and a private-key header; whose `tool_result` `content` holds the same as a string and as text parts; and whose `name` is `Bash; cat /etc/passwd`, `ghp_<40 chars>`, a 200-byte name, a name with a newline and an `mcp__other__tool` name. None of those values appears anywhere in the emitted event JSON or the local log line; the bad names show as `unlisted tool`. `content_digest_sha256` validates through `validation.SanitizedLogEvent`. | Unit test over the whole emitted event |
| REQ-06 | Redaction metadata | **Counts and categories.** A `tool_summary` entry keeps today's counting of dropped top-level fields (`classifySuppressedProviderField`), and adds one redaction with category `command_argument` for each `tool_use` block that has an `input`, one with `tool_output` for each `tool_result` block that has `content`, and one with `tool_output` for each name replaced with `unlisted tool`. Blocks beyond the cap of 8 still count. Categories stay sorted and unique, and the count is zero exactly when there are no categories. `runnerRedactionPolicyVersion` becomes `runner-redaction-2` for every sanitized log the runner emits. | Unit tests; `validation.SanitizedLogEvent` passes |
| REQ-07 | Caps | **Same budget.** Each provider stdout line still produces at most one sanitized log. A `tool_summary` entry counts against the same 512-entry limit, and the suppression marker is unchanged. The lease and start-evidence checks run unchanged before any `tool_summary` entry is built. | Existing cap test extended with a `tool_use` event at the limit |
| REQ-08 | FD-3 | **Local log line.** The `agent event` log line keeps `type` and `status` and adds `tools`: the same comma-separated names as the summary (after D-2 and the cap), or no `tools` attribute when the event has no tool block. Tool-result lines show `name:outcome`. No input, output or ID is logged. | Unit test with a captured `slog` handler |
| REQ-09 | D-1 | **Hook events unchanged.** `hookspool.Event`, `hookEventPayload`, `cmdHookEvent`, the managed hook script and the `platform_record_hook_event` field check are byte-identical to `67eeac7`. The existing tests `TestHookEventPayloadContainsOnlyDaemonDerivedAuthority` and the hook field refusal tests pass unchanged. | `git diff 67eeac7 -- internal/hookspool internal/daemon/hook_event.go internal/provider/hooks.go cmd/our-runner/main.go internal/mcpbridge` is empty |
| REQ-10 | D-3 | **Other providers unchanged.** With the configured provider `codex` or `opencode`, events of every shape, including one that looks like a Claude `tool_use` event, produce today's `agent_status` entry byte for byte (apart from the policy version in REQ-06). | Unit test per provider |
| REQ-11 | FD-3, RJIC REQ-12 | **Real Claude output (gated).** A gated test in `internal/isolation` (`RUN_DOCKER_SANDBOX_PROBE=1`, `JOB_IMAGE`) reuses `runOfflineClaudeBash` (extended to keep the raw output lines in `offlineClaudeBashRun`; existing assertions unchanged), passes each stdout JSON line of that run through `toolsummary`, and asserts the sequence contains `Tool call: Bash` and then `Tool result: Bash succeeded`. A second case uses the existing environment-scrub run (`TestClaudeBashCannotStartUnderTheEnvironmentScrubInDocker`'s setup) and asserts `Tool result: Bash failed`. | Gated test output pasted in this record |
| REQ-12 | AGENTS.md | **Docs.** README: next to "Hook telemetry is occurrence-only", state that hook events still carry no tool names and that Claude tool names appear only in sanitized logs with source `tool_summary`, with the exact vocabulary, the allowlist rule and what is never forwarded. `docs/TESTING.md` lists the REQ-11 gated test. No code comments (repository owner rule). | Diff review |
| REQ-13 | AGENTS.md | **Checks.** `gofmt -l .` prints nothing; `go build ./...`, `go vet ./...`, `go test ./... -count=1` pass; `go test -race ./internal/daemon/ ./internal/toolsummary/ -count=1` passes; statement coverage of `internal/toolsummary` and of the changed functions in `internal/daemon/sanitized_log.go` is at least 80 %; `gosec` and `govulncheck` at the README/TESTING versions pass or each failure is shown to predate `67eeac7`; `staticcheck` recorded as run or "not installed". | Command output pasted in this record |
| REQ-14 | FD-3 | **Live check (coordinator, after merge).** With the owner's permission to rebuild and restart the local runner, run one Claude Build on a fresh KAN ticket on `OMKE/shortstack-e2e` whose plan runs at least one Bash command. Pass: (a) the Build's Activity panel at `https://app.shortstack.dev` shows entries labelled with the tool summary source whose text includes `Tool call: Bash` and a following `Tool result: Bash succeeded` or `Tool result: Bash failed`; (b) a read-only query of the stored inbox payloads for that attempt finds `source = 'tool_summary'` rows and none of them contains the ticket's command text, a host path or `toolu_`; (c) the run otherwise behaves as before (no rejected events in `our-runner status`). Isolation mode is whatever the runner is configured with at the time; record it. | Screenshot at 1440×900, query output, UTC times pasted here |

**Inapplicable dimensions.** New UI, prototype matching and accessibility: no app change; the existing sanitized log list renders the new entries (REQ-14 checks it in the browser). Persistence and migrations: none; the app's DB check already allows `tool_summary`. Permissions and tenancy: unchanged; the entries go through the existing authority checks. Failure and recovery: a malformed or unexpected event falls back to today's `agent_status` entry (REQ-02, REQ-03, REQ-10); the tracker is in memory and resets with the job.

## Design and decisions

### D-1 Tool names go in sanitized logs, not in hook events

- Hook events are occurrence-only by design (`mcpbridge.ValidateHookEventType`, README line 1125). Adding a tool name means reading provider-controlled hook stdin in `cmdHookEvent`, a new field in the spool format, the daemon's field check and the wire payload, and a coordinated change to the app's strict `hookEventSchema`. An app that has not been updated rejects the event, and rejected events have caused runner backlogs before (app ERRORS note of 12 October 2026). The Activity UI does not render hook event details anyway.
- The sanitized log path already decodes the provider's structured stream, already has a source made for this (`tool_summary`), and the app already stores and shows it. Tool results there also carry success or failure, which is what FD-3 asked for ("which tool ran or failed").
- Both sources are equally non-authoritative: hook stdin and stdout both come from the provider process. Neither proves a call; both are evidence for a reviewer.
- Consequence: hook events keep their timing role and gain nothing. If the owner wants tool names on hook events too, it is a separate cross-repository protocol slice (the FU-3 described in `runner-docker-build-checks.md` D-4).

### D-2 Names come from a fixed allowlist

- Allowed names: Claude built-in tools `Agent`, `Bash`, `BashOutput`, `Edit`, `ExitPlanMode`, `Glob`, `Grep`, `KillBash`, `KillShell`, `LS`, `MultiEdit`, `NotebookEdit`, `NotebookRead`, `Read`, `Skill`, `SlashCommand`, `Task`, `TaskOutput`, `TodoWrite`, `ToolSearch`, `WebFetch`, `WebSearch`, `Write`; and `mcp__platform__<name>` for every public tool in `mcpbridge.ToolDefinitions()` (not the internal hook and list tools). The match is exact and case-sensitive.
- Uncertain: the planner wrote the built-in list from knowledge of Claude Code, not from the 2.1.299 binary. In step 0 the implementer greps the job image's `claude.exe` for each listed name, drops none that the runner already allows or denies by name (`provider.go:320`, `plan_permissions.go`), and records any extra built-in name found next to a `tool_use` in the REQ-01 output. A name missing from the list only shows as `unlisted tool`, so the list errs toward hiding.
- Why not a character pattern: a model can put any text in a `tool_use` name, including a short secret or a path that no secret regex catches. The existing sanitizer already uses allowlists for `type` and `status` (`allowedAgentEventType`, `allowedAgentStatus`). An allowlist keeps the same rule.
- MCP names from other servers (for example from an Agent Pack plugin) show as `unlisted tool`. Listing them would need a pack-reviewed name source; that is out of scope.

### D-3 Claude only

- Live Builds use Claude. The planner has not verified the JSON shapes of `codex exec --json` or `opencode run --format json` tool events in this repository, and no fixture exists. Guessing them would risk forwarding fields that were never reviewed. The new code runs only when the configured provider is `claude`.

### D-4 Package boundary

- `internal/toolsummary` is pure (no I/O, no daemon types): `Summarize(event map[string]any, tracker *Tracker) (Summary, bool)` where `Summary` holds the summary text, the log `tools` value, `Truncated`, and the extra redaction counts by category; `bool` is false when the event has no tool block. The daemon builds the `v1.SanitizedLogEvent` around it. The gated isolation test (REQ-11) imports the package directly, so it checks the real Claude output against the same code the daemon runs. Exact names are the implementer's choice if these roles hold.

## Implementation plan

Owned files (app-runner): new `internal/toolsummary/toolsummary.go` and `toolsummary_test.go`; `internal/daemon/sanitized_log.go` and `sanitized_log_test.go`; `internal/daemon/job.go` (tracker field, `consumeOutput` log line only); `internal/isolation/claude_weaker_sandbox_integration_test.go` (REQ-11 cases); `README.md`; `docs/TESTING.md`; this record. Do not touch `internal/hookspool`, `internal/daemon/hook_event.go`, `internal/provider/hooks.go`, `cmd/our-runner/main.go`, `internal/mcpbridge`, `bin/`, `~/.shortstack-runner`, the running runner, the job image or any app file.

1. Step 0 (REQ-01, D-2): run the gated probe, record the stream lines and the binary name check. Stop on a shape mismatch.
2. Test first: `toolsummary` unit tests for REQ-02 to REQ-06 cardinality and hostile inputs; then the package.
3. Test first: daemon tests for REQ-02, REQ-03, REQ-06, REQ-07, REQ-08, REQ-10; then wire `publishSanitizedAgentStatus` and the log line. Keep the `agent_status` path unchanged apart from the policy version.
4. REQ-11 gated test on `DOCKER_HOST=unix:///run/user/1000/docker.sock` with the reviewed RJIC job image.
5. Docs (REQ-12), checks (REQ-13), fill in "Verification plan and results" and "Implementation report", set `review_ready`.
6. After review and merge, the coordinator runs REQ-14.

## Verification plan and results

| Command / scenario | Required for | Actual result / date | Evidence | Baseline attribution |
| ------------------ | ------------ | -------------------- | -------- | -------------------- |
| Gated probe with raw stdout (step 0) | REQ-01, D-2 | Pass, 2026-11-09 21:15 UTC. Shape matches "Current code". | "Step 0 evidence" below | |
| `go test ./internal/toolsummary/ ./internal/daemon/ -count=1` | REQ-02 to REQ-10 | Pass, 2026-11-09. `toolsummary`: 13 tests (incl. subtests). Daemon: the 7 new tests and the 5 existing sanitized log and `consumeOutput` tests pass. | `internal/toolsummary/toolsummary_test.go`, `internal/daemon/sanitized_log_test.go` | |
| `git diff 67eeac7 -- internal/hookspool internal/daemon/hook_event.go internal/provider/hooks.go cmd/our-runner/main.go internal/mcpbridge` | REQ-09 | Empty (0 lines), 2026-11-09 | | |
| `DOCKER_HOST=unix:///run/user/1000/docker.sock RUN_DOCKER_SANDBOX_PROBE=1 JOB_IMAGE=our-runner-job:rjic@sha256:a2c8cabef3dbe9d4044bf2bcb2f7031aadbeee24ac0bd0dce8b41b5e900e8382 go test ./internal/isolation -run 'ToolSummary\|TestClaudeBashRunsInTheWeakerNestedSandbox\|TestClaudeBashCannotStartUnderTheEnvironmentScrubInDocker' -count=1 -v` | REQ-11 | Pass, 2026-11-09. All 4 tests pass in 22.8 s: the 2 new ones and the 2 existing ones, whose assertions are unchanged. | "REQ-11 output" below | |
| `gofmt -l .`, `go build ./...`, `go vet ./...` (linux and darwin/arm64), `go test ./... -count=1`, race, coverage, `gosec`, `govulncheck`, `staticcheck` | REQ-13 | Pass, 2026-11-09 | "REQ-13 output" below | |
| Live Claude Build, Activity panel and inbox query | REQ-14 | not run (coordinator, after merge) | | |

### Step 0 evidence (REQ-01, D-2)

The probe ran on 2026-11-09 with a temporary `t.Logf` of each `==claude-out` line in `parseOfflineClaudeBashRun`. The print was removed before commit. Below are the lines with `usage`, `uuid`, `session_id`, timestamps and `tool_result_meta` trimmed (`…`). Claude Code 2.1.299.

Success run (`TestClaudeBashRunsInTheWeakerNestedSandboxWithoutTheEnvironmentScrub`):

```json
{"type":"assistant","message":{"id":"msg_offline","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_offline_1","name":"Bash","input":{"command":"echo sandbox-bash-ok; grep NSpid /proc/self/status; cat /home/runner/.claude/canary.txt 2>/dev/null; touch /home/runner/outside 2>/dev/null; echo \"scrub=${CLAUDE_CODE_SUBPROCESS_ENV_SCRUB:-unset}\"","description":"sandbox probe"}}],"stop_reason":null,…},"parent_tool_use_id":null,"session_id":"…","uuid":"…","timestamp":"…","request_id":"req_offline"}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_offline_1","type":"tool_result","content":"sandbox-bash-ok\nNSpid:\t131\t8\t3\nscrub=unset","is_error":false}]},"parent_tool_use_id":null,"session_id":"…","uuid":"…","timestamp":"…","tool_use_result":{"stdout":"sandbox-bash-ok\nNSpid:\t131\t8\t3\nscrub=unset","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false},"tool_result_meta":[…]}
```

Scrub run (`TestClaudeBashCannotStartUnderTheEnvironmentScrubInDocker`), user line:

```json
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"Exit code 1\nbwrap: Can't mount proc on /newroot/proc: Operation not permitted","is_error":true,"tool_use_id":"toolu_offline_1"}]},"parent_tool_use_id":null,"session_id":"…","uuid":"…","timestamp":"…","tool_use_result":"Error: Exit code 1\nbwrap: Can't mount proc on /newroot/proc: Operation not permitted","tool_result_meta":[…]}
```

Findings:

- `is_error` is present as boolean `false` when the call succeeds, and `true` when it fails.
- `tool_use_id` equals the `tool_use` block's `id` (`toolu_offline_1`).
- The shape matches "Current code". There is one addition: the user event carries a top-level `tool_use_result` (stdout and stderr) and `tool_result_meta`. Both are top-level fields, so the existing loop drops them and counts them as `tool_output` redactions. They are never forwarded.
- Only `Bash` appeared next to a `tool_use` block.
- Binary name check. In the job image, `grep -a -o -F -c -w <name>` on `/opt/agent-tools/node_modules/@anthropic-ai/claude-code/bin/claude.exe` finds every D-2 built-in name except `SlashCommand` (0 matches). `SlashCommand` stays on the list: it is harmless and no runner allow or deny list refers to it. Every name that `provider.go` (`Bash`, `mcp__platform__*`) and `plan_permissions.go` (`Read`, `Grep`, `Glob`, `Skill`, `Edit`, `Write`, `NotebookEdit`, `MultiEdit`, `WebFetch`, `WebSearch`) allow or deny is on the list.
- Extra built-in names. The `system/init` event's `tools` array lists names that are not in D-2: `CronCreate`, `CronDelete`, `CronList`, `EnterWorktree`, `ExitWorktree`, `ListAgents`, `ReportFindings`, `ScheduleWakeup`, `SendMessage`, `TaskStop`, `Workflow`. All of them also appear in the binary. They were **not** added, because D-2 fixes the list and missing names err toward hiding. They show as `unlisted tool`. Adding them is a planner decision (see Follow-up).

### REQ-11 output

```text
=== RUN   TestClaudeBashToolSummaryNamesASucceededCallInDocker
    claude_weaker_sandbox_integration_test.go:367: tool summaries: ["Tool call: Bash" "Tool result: Bash succeeded"]
--- PASS: TestClaudeBashToolSummaryNamesASucceededCallInDocker (7.72s)
=== RUN   TestClaudeBashToolSummaryNamesAFailedCallUnderTheEnvironmentScrubInDocker
    claude_weaker_sandbox_integration_test.go:376: tool summaries: ["Tool call: Bash" "Tool result: Bash failed"]
--- PASS: TestClaudeBashToolSummaryNamesAFailedCallUnderTheEnvironmentScrubInDocker (10.80s)
--- PASS: TestClaudeBashRunsInTheWeakerNestedSandboxWithoutTheEnvironmentScrub (2.43s)
--- PASS: TestClaudeBashCannotStartUnderTheEnvironmentScrubInDocker (1.88s)
ok  	github.com/example/app-runner/internal/isolation	22.840s
```

### REQ-13 output (2026-11-09)

| Check | Result |
| ----- | ------ |
| `gofmt -l .` | no output |
| `go build ./...` | ok |
| `go vet ./...` and `GOOS=darwin GOARCH=arm64 go vet ./...` | ok |
| `go test ./... -count=1` | exit 0. Every package ok or has no test files (47 packages listed). |
| `go test -race ./internal/daemon/ ./internal/toolsummary/ -count=1` | ok: daemon 303.9 s, toolsummary 1.0 s |
| Coverage `internal/toolsummary` | 100.0 % of statements (every function 100 %) |
| Coverage of changed daemon functions, from the sanitized log, tool summary and `consumeOutput` tests | `summarizeProviderTools` 100 %, `publishSanitizedAgentStatus` 88.9 %, `consumeOutput` 80.6 %, `sanitizeSelectedLogValue` 85.7 % (unchanged) |
| `sh scripts/check.sh vuln` (govulncheck v1.8.0) | "Your code is affected by 0 vulnerabilities", exit 0. It also reports 1 vulnerability in a required module that the code does not call; that is unchanged by this slice. |
| `sh scripts/check.sh sec` (gosec v2.29.0) | no findings, exit 0 |
| `staticcheck` | not installed |

## Implementation report

Implementer: develop-implementer (Opus 5.5), 2026-11-09. Branch `fd3-hook-tool-names`, based on contract commit `013cfdd`.

**Changed paths**

- `internal/toolsummary/toolsummary.go` (new). A pure package with `Summarize(event, *Tracker) (Summary, bool)`, `NewTracker`, `AllowedName`, `MaxTrackedIDs = 512` and `MaxNamedBlocks = 8`. `Summary` holds `Text`, `Tools` (the log value), `Truncated`, `CommandArgumentRedactions` and `ToolOutputRedactions`. The allowlist is the D-2 built-ins plus `mcp__platform__` + each `mcpbridge.ToolDefinitions()` name. The internal hook and list tools are not in `ToolDefinitions()`, and a test asserts they are refused.
- `internal/toolsummary/toolsummary_test.go` (new).
- `internal/daemon/sanitized_log.go`:
  - `runnerRedactionPolicyVersion` is now `runner-redaction-2`.
  - New `summarizeProviderTools`. It runs only when `cfg.Provider == "claude"` and works under `procMu`; the tracker is created lazily.
  - `publishSanitizedAgentStatus(providerEvent, tools *toolsummary.Summary)` runs the lease, start-evidence and cap logic and the top-level field counting unchanged. It then replaces the source, summary and detail with the tool summary, and adds `command_argument` and `tool_output` counts.
- `internal/daemon/job.go`:
  - New `toolTracker` field.
  - `consumeOutput` resets the tracker under `procMu` when a provider stream starts, so a resumed session starts empty. It summarizes each decoded line once, adds `tools` to the `agent event` log line when there is a tool block, and passes the summary to the emitter.
- `internal/daemon/sanitized_log_test.go`:
  - The 2 existing emitter calls now pass `nil` or a summary. The lease-refusal case also covers a tool summary.
  - 7 new tests: `TestClaudeToolCallAndResultBecomeToolSummaries`, `TestToolSummaryLogLineAndEventForSeveralAndHostileBlocks`, `TestEventsWithoutToolBlocksKeepTheAgentStatusEntry`, `TestOtherProvidersKeepAgentStatusForToolShapedEvents`, `TestResumedSessionStartsWithAnEmptyToolTracker`, `TestToolSummaryCountsAgainstTheSanitizedLogCap`, `TestToolSummaryWaitsForStartEvidence`.
- `internal/isolation/claude_weaker_sandbox_integration_test.go`: `offlineClaudeBashRun.outputLines` keeps the raw `==claude-out` lines. The 2 new gated tests use them; the existing assertions are unchanged.
- `README.md`: the hook paragraph now says hook events carry no tool names. A new paragraph gives the `tool_summary` vocabulary, the allowlist, what is never forwarded, the redaction policy and the `tools` log attribute.
- `docs/TESTING.md`: the 2 REQ-11 gated tests.
- This record.

**Per-REQ evidence**

| ID | Evidence |
| -- | -------- |
| REQ-01 | "Step 0 evidence" above. The temporary print is not in the commit (`git diff 013cfdd -- internal/isolation` shows only the `outputLines` field, its append and the 2 new tests). |
| REQ-02 | `TestToolCallSummaryCardinality` covers 1, 2, 8 and 9 blocks, including ` (+1 more)` with `truncated`. `TestToolCallSummaryKeepsBlockOrderAndSkipsText` covers order, text and thinking blocks mixed in, and duplicates (`Bash, Bash`). `TestEventsWithoutToolBlocksAreNotSummarized` covers events with no tool block. `TestEventsWithoutToolBlocksKeepTheAgentStatusEntry` shows that, for assistant and user text events, `system` and `result`, the event emitted with the Claude provider equals the legacy one in every field except `observed_at`. `TestClaudeToolCallAndResultBecomeToolSummaries` checks the source `tool_summary` and that there is no `detail`. |
| REQ-03 | `TestToolResultSummary` covers one result, two mixed results, an unseen ID, an ID from another tracker (another job), a nil tracker, and `is_error` missing, `true`, `false` and `"true"`. `TestToolResultCap` covers 10 results, ` (+2 more)`. |
| REQ-04 | `TestTrackerIsBounded`: the 513th ID is not stored and its result shows `unknown tool`; a stored ID can be reused. `TestTrackerRejectsInvalidIDs` covers an empty ID, 129 bytes, `/`, a space, a newline, non-ASCII, a non-string and a missing ID; a 128-byte ID is stored. The tracker is `jobRunner.toolTracker`, guarded by `procMu`. `TestResumedSessionStartsWithAnEmptyToolTracker` shows it is reset per provider stream. No emitted event JSON or log line contains `toolu_` (daemon tests). |
| REQ-05 | `TestHostileValuesNeverLeave` (package) and `TestToolSummaryLogLineAndEventForSeveralAndHostileBlocks` (daemon, whole emitted event and log buffer) cover: a command, a host path, a `ghp_` token and a private-key header in `input`, the same in `content` as a string and as text parts, and the names `Bash; cat /etc/passwd`, `ghp_<40>`, 200 bytes, a newline, `mcp__other__tool`, `bash`, and the internal platform tools. None appears; the bad names show as `unlisted tool`. A summary character-class regex passes. Every emitted event passes `validation.SanitizedLogEvent` and a digest recomputation (`toolSummaryValidatedEvents`). |
| REQ-06 | The daemon tests assert exact counts and sorted categories. Real call line: 4 dropped top-level fields + 1 input = 5, `[command_argument tool_output]`. Real result line: 5 dropped + 1 content = 6, `[tool_output]`. Hostile call: 2 + 9 inputs + 5 unlisted = 16, which includes the block beyond the cap. Every emitted event, including the cap marker, has `runner-redaction-2`. |
| REQ-07 | `TestToolSummaryCountsAgainstTheSanitizedLogCap`. At 511 entries, a `tool_use` line takes slot 512, the next line produces the unchanged suppression marker, and the third produces nothing (sequence 514). `TestSanitizedLogEnforcesLeaseAndEmissionCap`: a tool summary with an invalid lease emits nothing. `TestToolSummaryWaitsForStartEvidence`: nothing is emitted and the counter does not move before start evidence. |
| REQ-08 | `TestClaudeToolCallAndResultBecomeToolSummaries` checks the exact log line endings `type=assistant status=[suppressed] tools=Bash` and `type=user status=[suppressed] tools=Bash:succeeded`, and no `tools` for a text event. The hostile test checks the capped `tools="Bash,unlisted tool,…,Read,Edit"` and `tools="Bash:failed,Read:returned,unlisted tool:succeeded"`. `status=[suppressed]` is today's `safeLogValue` output for a missing status; it is unchanged. |
| REQ-09 | The `git diff 67eeac7` over the hook paths is empty. The full `go test ./...` passes, which includes `TestHookEventPayloadContainsOnlyDaemonDerivedAuthority` and the hook field refusal tests. |
| REQ-10 | `TestOtherProvidersKeepAgentStatusForToolShapedEvents`: for `codex` and `opencode`, the real Claude tool-use and tool-result lines, a text line and a flat `tool_use` event all give `agent_status` `Agent status: …`, no `tools` attribute and no tracker. |
| REQ-11 | "REQ-11 output" above. |
| REQ-12 | `README.md` (after "Hook telemetry is occurrence-only") and `docs/TESTING.md`. No code comments were added. |
| REQ-13 | "REQ-13 output" above. |
| REQ-14 | unverified. This is the coordinator's live check after merge. |

**Decisions**

- I-1: `publishSanitizedAgentStatus` takes the precomputed summary instead of summarizing itself. Summarizing changes the tracker, and the local log line needs the same result, so each line is summarized exactly once in `consumeOutput`. The summary is pure text. The `v1.SanitizedLogEvent` is still built only after the lease, start-evidence and cap checks (REQ-07). The tracker learns IDs even when an entry is suppressed or not yet allowed to be emitted, so later results keep their names.
- I-2: For a `tool_summary` entry, the selected `type` and `status` values are still sanitized for their redaction categories. `detail` is cleared (REQ-02 "No detail").
- I-3: The local `tools` value is joined with `,` and no space. It lists only the shown names; there is no `(+k more)`.
- I-4: An `unlisted tool` name is counted once, at the `tool_use` block. A later result for that ID shows the tracker's stored `unlisted tool` and is not counted again.
- I-5: `toolsummary` imports `mcpbridge` for `ToolDefinitions()` only. There is no import cycle.

**Follow-up (out of scope)**

- The planner could add the 11 extra Claude 2.1.299 built-in names from the `system/init` `tools` array to D-2. Today they show as `unlisted tool`.
- REQ-14 live Build after merge (coordinator).

## Reviewer requirement review

Not started.

## Owner questions (non-blocking)

- Q-1: D-1 keeps hook events free of tool names and puts them only in sanitized logs. Do you also want tool names on hook events? If yes, that becomes a separate app plus runner protocol slice; this slice does not depend on the answer.
  - **Answered (coordinator decision, 9 November 2026):** no tool names on hook events in this slice. D-1 stands. Tool names on hook events would be the separate cross-repository slice FU-3 described in `runner-docker-build-checks.md` D-4.
