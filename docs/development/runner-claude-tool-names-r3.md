# Newer Claude built-in tool names in sanitized logs (RHTN R-3)

## Control

| Field                  | Value |
| ---------------------- | ----- |
| ID                     | RHTN follow-up R-3 `runner-claude-tool-names-r3` |
| Kind                   | bug (missing evidence: newer Claude built-in tool calls show as `unlisted tool`) |
| Status                 | done |
| Risk                   | medium. The slice widens the fixed name allowlist that decides which agent-derived text leaves the runner. Only fixed, reviewed literal names are added; tool input, output and IDs stay dropped. A defect could reveal a name that should stay hidden, but cannot forward a model-chosen string, because the match stays exact against compile-time literals. |
| Complexity             | easy. One map literal, one policy version constant, tests, README and records. No protocol, app or migration change. |
| Parent scope           | `docs/development/runner-hook-tool-names.md` (RHTN), reviewer finding R-3 and the implementer follow-up "add the 11 extra Claude 2.1.299 built-in names". RHTN REQ-14 (live Build) stays with the coordinator and is not part of this slice. |
| Product sources        | `PRODUCT.md` agent run evidence (sanitized logs are runner-redacted, bounded and non-authoritative). README "Hook telemetry is occurrence-only" and the `tool_summary` paragraph after it. |
| Planner                | Opus 5.5 (`claude-opus-5-5`); develop-planner role, swarm slice 03, 13 November 2026 |
| Implementer            | Opus 5.5 (`claude-opus-5-5`); develop-implementer role, same agent, 13 November 2026 |
| Reviewer               | Opus 5.5 (`claude-opus-5-5`), fresh context; develop-reviewer role, 13 November 2026 |
| Repair attempt         | 0 |
| Updated                | 2026-11-13 |
| Baseline               | app-runner `main` at `90243ce`, worktree `.claude/worktrees/slice-runner-claude-tool-names`, branch `slice/runner-claude-tool-names`. |

## Outcome and current gap

A reviewer reading sanitized logs sees the real name of every Claude Code built-in tool the job's Claude version offers. Today `internal/toolsummary/toolsummary.go` `builtinNames` holds the 23 names from RHTN D-2. The RHTN REQ-01 probe of Claude 2.1.299 found 11 more built-in names in the `system/init` `tools` array and in the binary (`runner-hook-tool-names.md`, REQ-01 findings): `CronCreate`, `CronDelete`, `CronList`, `EnterWorktree`, `ExitWorktree`, `ListAgents`, `ReportFindings`, `ScheduleWakeup`, `SendMessage`, `TaskStop`, `Workflow`. Calls to them show as `unlisted tool`, and `TestAllowedNames` asserts that `CronCreate` is refused.

## Scope

In scope: add the 11 names to the fixed allowlist; bump the redaction policy version; update tests, README and the RHTN record.

Excluded:

- Hook events (still occurrence-only, RHTN D-1).
- MCP names from servers other than `mcp__platform__` (still `unlisted tool`, RHTN D-2).
- Codex and OpenCode tool names (RHTN D-3).
- A dynamic allowlist read from `system/init`: that event is provider output, so a model or a hostile binary could choose its contents.
- RHTN R-1 and R-2 docs nits, and RHTN REQ-14 live Build.

## Acceptance contract

| ID     | Product source | Required behavior and observable acceptance scenario | Verification method |
| ------ | -------------- | ---------------------------------------------------- | ------------------- |
| REQ-01 | RHTN R-3 | Given a Claude assistant event with one `tool_use` block named exactly one of the 11 names, `Summarize` returns `Tool call: <name>`, `Tools` `<name>`, and zero `ToolOutputRedactions`; a later `tool_result` for that ID shows `Tool result: <name> succeeded`. `AllowedName` is true for each of the 11 names and for every one of the 23 existing names. | `go test ./internal/toolsummary` |
| REQ-02 | RHTN D-2 | The match stays exact and case-sensitive against literals: lower case, padded, prefixed or suffixed variants of the new names (`cronCreate`, `CRONCREATE`, ` Workflow`, `Workflow `, `mcp__platform__SendMessage`, `SendMessage2`) are refused and show as `unlisted tool`. Input and content are still counted as `command_argument` and `tool_output` redactions and never appear in the summary. | `go test ./internal/toolsummary` |
| REQ-03 | Redaction metadata | Every sanitized log the runner emits carries `policy_version` `runner-redaction-3`. | `go test ./internal/daemon` (existing per-event assertion) |
| REQ-04 | README | The README `tool_summary` paragraph lists all 34 built-in names and the new policy version; the RHTN record links this follow-up. | Review |

Inapplicable dimensions: no persistence, permissions, UI, retry or wire format change. The app treats `policy_version` as bounded opaque text (`sanitized-log-event.schema.ts`, `validation/agent_run.go`), so the bump needs no app change.

## Design and decisions

### D-1 Add the 11 names to the fixed allowlist

- The names are fixed Claude Code identifiers found both in the 2.1.299 binary and in `system/init`, so revealing them reveals nothing a model chose. The RHTN rule "missing names err toward hiding" was a safe default for unverified names; these are now verified.
- The list stays a compile-time literal. Reading names from `system/init` was rejected (see Scope).

### D-2 Bump the policy version to `runner-redaction-3`

- Context: `runner-redaction-2` was introduced with RHTN to mark "tool names from the D-2 allowlist". The version identifies which rules produced a sanitized log.
- After this slice the same provider line produces different output: a `CronCreate` call that showed as `unlisted tool` with one extra `tool_output` redaction now shows `CronCreate` with no such redaction. A reviewer comparing logs needs to know which list applied, and redaction counts differ between versions for the same input.
- Options: keep `runner-redaction-2` (no signal that the disclosed set grew) or bump (one constant). The bump is free: both sides validate the field only as bounded non-empty text.
- Decision: bump to `runner-redaction-3`. Status: accepted.

## Implementation plan

1. Test first: extend `TestAllowedNames` with the 11 names and exact-match negatives; add a `Summarize` test for each new name (REQ-01, REQ-02). Change the daemon policy version assertion to `runner-redaction-3` (REQ-03). Confirm they fail.
2. Add the names to `builtinNames` and bump `runnerRedactionPolicyVersion`.
3. Update README and the RHTN record (REQ-04). Run `go test ./internal/toolsummary ./internal/daemon`, `go vet`, `gofmt -l`.

## Verification plan and results

| Command / scenario | IDs | Actual result / date | Evidence |
| ------------------ | --- | -------------------- | -------- |
| `go test ./internal/toolsummary` with the new tests and the old `toolsummary.go` (stashed) | REQ-01, REQ-02 | FAIL, 2026-11-13 | `built-in "CronCreate" not allowed` … `built-in "Workflow" not allowed` |
| `go test ./internal/daemon -run 'ToolSummary\|Sanitized'` with the old `sanitized_log.go` (stashed) | REQ-03 | FAIL, 2026-11-13 | `policy version="runner-redaction-2"` in `TestToolSummaryLogLineAndEventForSeveralAndHostileBlocks` and `TestToolSummaryCountsAgainstTheSanitizedLogCap` |
| `go test -count=1 ./internal/toolsummary ./internal/daemon ./internal/validation ./api/runner/v1` after | REQ-01 to REQ-03 | PASS, 2026-11-13 | 2539 tests passed in 4 packages; `toolsummary` 11 top-level tests pass, 0 fail |
| `go vet ./internal/toolsummary ./internal/daemon`, `gofmt -l internal api cmd` | all | PASS, 2026-11-13 | vet clean, gofmt lists no files |

## Implementation report

- `internal/toolsummary/toolsummary.go`: `builtinNames` now holds 34 literal names (the 23 from RHTN D-2 plus the 11 R-3 names). Matching is unchanged: an exact map lookup.
- `internal/toolsummary/toolsummary_test.go`: `TestAllowedNames` no longer expects `CronCreate` to be refused. It refuses case, padding, prefix and suffix variants and asserts the map size is 34. `TestNewerClaudeBuiltinsShowTheirNames` checks call and result summaries for each new name, the redaction counts, and that no input or output leaks (REQ-01). `TestNewerClaudeBuiltinVariantsStayUnlisted` covers REQ-02.
- `internal/daemon/sanitized_log.go`: `runnerRedactionPolicyVersion = "runner-redaction-3"` (D-2). `sanitized_log_test.go` asserts it for every emitted event (REQ-03).
- `README.md`: the `tool_summary` paragraph lists the 34 names and `runner-redaction-3`. Rewrapping the paragraph also fixes RHTN R-1 (the unwrapped line) as a side effect (REQ-04).
- `docs/development/runner-hook-tool-names.md`: the Follow-up bullet and the R-3 row link this record (REQ-04). The REQ-01 findings text from the time of the probe (line 147) is left as history.
- No app change: app fixtures still use `runner-redaction-1`, and the app checks `policy_version` only as bounded text.

| ID | Claimed status |
| -- | -------------- |
| REQ-01 | implemented |
| REQ-02 | implemented |
| REQ-03 | implemented |
| REQ-04 | implemented |

## Reviewer requirement review

Reviewer: develop-reviewer, Opus 5.5 (`claude-opus-5-5`), fresh context, 2026-11-13. Reviewed `main..d02cd35`.

| ID | Verdict | Code evidence | Test / runtime evidence | Gap or finding |
| -- | ------- | ------------- | ----------------------- | -------------- |
| REQ-01 | implemented | `internal/toolsummary/toolsummary.go:21` `builtinNames` holds the 23 RHTN D-2 names plus the 11 R-3 names as literals; `AllowedName` (`:84`) is an exact map lookup. | `TestNewerClaudeBuiltinsShowTheirNames` (call text, `Tools`, zero `tool_output` and one `command_argument` redaction, result `<name> succeeded`, no `secret-` input/output leak); `TestAllowedNames` checks all 23 old names and that the map size is 23+11, so no extra or duplicate entry. PASS. | none |
| REQ-02 | implemented | Same exact lookup; no normalization, trimming or prefix handling added. | `TestAllowedNames` negatives (`cronCreate`, `CRONCREATE`, ` Workflow`, `Workflow `, `mcp__platform__SendMessage`, `SendMessage2`, `Task Stop`); `TestNewerClaudeBuiltinVariantsStayUnlisted` shows `unlisted tool` with input and output counted as redactions. PASS. | none |
| REQ-03 | implemented | `internal/daemon/sanitized_log.go:16` `runner-redaction-3`; the only non-test, non-doc use of the version string. | `toolSummaryValidatedEvents` (`sanitized_log_test.go:141`) asserts it per emitted event; `TestToolSummaryLogLineAndEventForSeveralAndHostileBlocks` and `TestToolSummaryCountsAgainstTheSanitizedLogCap` PASS. Full `./internal/daemon` PASS. | none |
| REQ-04 | implemented | `README.md:1170-1184` lists the 34 names and `runner-redaction-3`; `runner-hook-tool-names.md` Follow-up and R-3 row link this record. | Review. | none |

Contract reconciliation: the names-only invariant holds (input, output and IDs are still dropped; the test asserts no input/output substring appears). App compatibility: `app/src/server/runner-protocol/v1/agent-runs/schemas/sanitized-log-event.schema.ts` validates `policy_version` as non-blank `boundedText(128)`; no app source or SQL migration pins a `runner-redaction-*` value (only test fixtures use `runner-redaction-1`). Runner side: `internal/validation/agent_run.go:195` checks it as bounded text; the remaining `runner-redaction-1` strings in `api/runner/v1/agent_run_test.go:110` and `internal/validation/agent_run_test.go:200` are arbitrary valid inputs, not dependencies on the old value. The diff adds no code comments.

## Review findings and repair

| Finding | Severity | Acceptance IDs | Required correction and verification | Owner | State |
| ------- | -------- | -------------- | ------------------------------------ | ----- | ----- |
| REV-01 | nonblocking (environment) | none | Running the four packages together under the default 10-minute `go test` timeout timed out in `./internal/daemon` (panic during the unrelated `TestAwaitControlResultHandlesAbsentAndPendingBarrier`, 1 s into that test) at host load average about 10. A rerun of `./internal/daemon` alone with `-timeout 40m` passed in 836 s. This is suite length under host load, not a slice defect. Optional: use a longer `-timeout` for daemon tests on shared hosts. | coordinator | open |

## Completion

- Decision: verified done.
- Verified by: develop-reviewer, Opus 5.5 (`claude-opus-5-5`), 2026-11-13.
- Gate: REQ-01 to REQ-04 implemented; no blocking findings. Evidence: `go test -count=1 ./internal/toolsummary ./internal/validation ./api/runner/v1` PASS; `go test -count=1 -timeout 40m ./internal/daemon` PASS (836 s); targeted `-run 'TestAllowedNames|TestNewerClaude|ToolSummary|Sanitized'` PASS; `go vet ./...` clean; `gofmt -l internal api cmd` empty.
- Remaining parent-feature or excluded work: RHTN REQ-14 live Build (coordinator); RHTN R-2 docs nit; hook events, non-platform MCP names, Codex and OpenCode names stay excluded.
- Product/checkpoint links updated: `docs/development/runner-hook-tool-names.md` (by the implementer).
