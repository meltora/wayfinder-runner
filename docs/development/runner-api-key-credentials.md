# Provider API keys and token budgets on the runner (BYOK)

## Control

| Field                  | Value |
| ---------------------- | ----- |
| ID                     | `runner-api-key-credentials` (slice 22) |
| Kind                   | slice |
| Status                 | review_ready |
| Risk                   | high. The runner now holds customer model API keys. A defect could leak a key into logs, hook events or the control plane, or stop customer runs wrongly. |
| Complexity             | complex. Config, key resolution, redaction, isolation, supervisors, capability report, start_job protocol, budget enforcement. |
| Parent scope           | Owner requirements for slice 22; app contract `docs/development/runner-api-key-credentials.md` on app branch `worktree-agent-a03e7d2938ecbd23c`. |
| Product sources        | `PRODUCT.md` "No model API resale or token billing" (the SaaS never requests, stores, copies, exports or proxies model API keys). |
| Planner                | Opus 5.5 (`claude-opus-5-5`); swarm slice 22 agent, 13 November 2026 |
| Implementer            | Opus 5.5 (`claude-opus-5-5`); same agent, 13 November 2026 |
| Reviewer               | pending |
| Repair attempt         | 1 |
| Updated                | 2026-11-13 |
| Baseline               | app-runner `main` at `7e272f9`, branch `slice/runner-api-key-credentials-r` (fetched by the coordinator from the scratch clone branch `slice/runner-api-key-credentials`). |

## Outcome

A runner owner can point Claude Code or Codex at a model API key held on the runner host. The runner uses API-key mode when a key source is configured and subscription mode otherwise, reports only the mode and readiness, never sends or logs the key, and redacts it from sanitized output. The runner enforces token budgets delivered with each job.

## Scope

In scope: `provider_api_keys` config and validation; `internal/apikey` resolution; process environment delivery; known-secret redaction including the hook spool; Docker forwarding by name; refusal of tmux and herdr supervisors in API-key mode; remote config cannot override key sources; `auth_mode`/`auth_ready` in the capability report; `token_budget` in start_job; enforcement and failure codes `run_token_budget_exceeded` and `project_token_budget_exceeded`.

Excluded: OpenCode API-key mode; work-intake generation with an API key; Cloud mode; cost display.

## Acceptance contract

| ID | Requirement | Verification | Claimed status |
| -- | ----------- | ------------ | -------------- |
| R-01 | Exactly one of `env` (`^[A-Z][A-Z0-9_]{0,63}$`) or `file` (clean absolute path); file must be regular, not a link, owner-only (no group/other bits), owned by the runner user and size-bounded; value length and characters checked; conflict with `provider_environment` rejected. | `internal/apikey/apikey_test.go`, `internal/config/provider_api_keys_test.go` | implemented |
| R-02 | No error, log, event or report contains the key. | `TestResolveErrorsNeverContainTheKey`, `TestProviderAPIKeyErrorsDoNotContainTheKey` | implemented |
| R-03 | Resolved keys are remembered by `redact.KnownSecrets` (plain and common encodings) and removed from sanitized output and hook spool string values. | `internal/redact/known_secrets_test.go`, `TestRedactionRemovesRememberedProviderAPIKeys` | implemented |
| R-04 | Docker passes `-e NAME` only, never the value; tmux and herdr supervisors are refused when a key is configured. | `TestDockerWrapForwardsProviderAPIKeysByNameOnly`, `TestProviderAPIKeyRefusesSupervisorsThatExposeTheEnvironment` | implemented |
| R-05 | Remote configuration cannot set or change key sources. | `TestMergeKeepsProviderAPIKeySourcesLocal` | implemented |
| R-06 | Capability report states `auth_mode` (`subscription`/`api_key`) and `auth_ready` per provider, no key material. | `TestCollectReportsAPIKeyModeWithoutKeyMaterial`, `TestCollectReportsSubscriptionModeByDefault` | implemented |
| R-07 | `token_budget` is strictly decoded (positive integers, unknown fields rejected); new failure codes are valid on job_finished. | `internal/validation/token_budget_test.go` | implemented |
| R-08 | Budgeted tokens = uncached input + output + cache creation, from provider-reported usage including Claude assistant events; malformed usage ignored. Codex `input_tokens` include `cached_input_tokens`, so for Codex the cached input (capped at input) is subtracted; the reported `provider_usage` keeps the raw counters. | `internal/provider/usage_budget_test.go` | implemented |
| R-09 | The provider is stopped when the run or project limit is reached, with the tighter limit's code; a zero project remainder refuses the start; no budget means no stop. | `internal/daemon/job_token_budget_test.go` | implemented |

## Design and decisions

- Key variables passed to the provider: `ANTHROPIC_API_KEY` for Claude, `CODEX_API_KEY` for Codex.
- Open owner verification item. Build and planning run `claude -p --input-format text --output-format stream-json` without `--bare` (`internal/provider/provider.go` 309), because `--bare` skips the hooks the runner's telemetry needs. `claude --help` states `ANTHROPIC_API_KEY` is the only Anthropic auth source only under `--bare`; it does not show whether the key wins over a stored subscription login in non-bare `-p` mode. No verifiable mechanism that forces API-key auth while keeping hooks was found, so none was added. `auth_mode: api_key` therefore means "a key source is configured and readable", not proof of which credential the CLI used. The same applies to `CODEX_API_KEY` versus a stored ChatGPT login for `codex exec`.
- Enforcement follows provider events and can overshoot by one event; unparseable usage fails open; a clean exit over the limit stays successful.
- Agent subprocesses inherit the provider environment and could read the key.

## Implementation plan

Done as listed in scope; new packages `internal/apikey`, files `internal/config/provider_api_keys.go`, `internal/capability/provider_api_keys.go`, `internal/daemon/job_provider_api_key.go`, `internal/daemon/job_token_budget.go`, `internal/provider/usage_budget.go`, `internal/redact/known_secrets.go`, `internal/validation/token_budget.go`, `api/runner/v1/token_budget.go`, plus wiring in config, capability, daemon, hookspool, isolation, remoteconfig, provider usage, validation and the v1 API types.

## Verification

- `gofmt -l .`: clean.
- `go vet ./...`: clean.
- `go test ./...`: 3600 passed in 17 packages (initial run).
- Repair 1 (Codex cached-input rule, new tests `TestBudgetedTokensRemovesCodexCachedInputFromInput` and `TestBudgetedTokensNeverRemovesMoreThanCodexInput`): `gofmt -l .` clean, `go vet ./...` clean, `go test ./...` 5332 passed in 48 packages.

## Implementation report

All R-01..R-09 implemented with tests. Follow-ups: OpenCode API-key mode; work-intake generation with an API key; live check of the Codex and Claude key variables.
