# Subscription login survives token refresh (RUNNER-OAUTH-WRITEBACK, 0.4.2)

## Control

| Field | Value |
|---|---|
| ID | `runner-oauth-writeback` |
| Kind | slice |
| Status | design, waiting for the master's choice of approach |
| Target | 0.4.2 |
| Risk | high. The runner writes the owner's Claude login on the host. A defect could lose the login, write a stale one over a fresh one, or leak it. |
| Baseline | app-runner `main` at `105182f` (v0.4.1) |
| Updated | 2026-11-26 |

## Problem

The known issue in [release-0.4.1.md](release-0.4.1.md), "Subscription login works for one job, then OAuth session expired":

- `isolation.PrepareHome` (`internal/isolation/home.go`) copies the host's `.credentials.json` (and `.claude.json`) into a fresh job home with mode 0600. `CleanupHome` deletes the home when the job ends (`internal/daemon/job.go` around line 640).
- When the access token has expired, Claude Code in the job refreshes it in the job copy. The sign-in server rotates the refresh token, so the host's refresh token is now invalid.
- The refreshed copy is deleted with the job home. Every later job starts from the host's invalid refresh token and fails within seconds with `Failed to authenticate: OAuth session expired and could not be refreshed`.
- Concurrent jobs make it worse. With `max_concurrent` above 1, every running job holds the same access token, which expires at the same moment, and the same refresh token. The first job that refreshes rotates it, and every other running job then fails its own refresh, even if the runner wrote the first job's result back.

API key sign-in (`provider_api_keys`) is not affected.

## Goals

- A runner signed in with a Claude subscription keeps working across access-token expiry, with any `max_concurrent`, without the owner signing in again.
- The host login is never replaced by an older one, never truncated, and never left half-written.
- No new place where the login can leak. It stays a file under the sandbox's denied paths (`provider.go` 265 and 273). It never enters a Docker job's environment, because Claude Docker jobs run without `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB` (README, "Claude Docker jobs also run without ...").

## Non-goals

- Codex and OpenCode logins. Codex has the same copy-and-delete pattern for `auth.json`. It is a follow-up, once the Claude path is proven.
- Cloud mode and remote configuration of credentials. Credential sources stay local, as for API keys (R-05 in [runner-api-key-credentials.md](runner-api-key-credentials.md)).
- The macOS keychain source. The runner can read it but cannot write it back. A keychain-sourced login keeps today's behaviour, and `doctor` says so.

## Anti-goals

- The runner implementing Claude's OAuth refresh call itself. The token endpoint, client id and rotation rules are Claude Code internals, not a public contract. A runner that copied them would break silently on a Claude Code release.
- Bind-mounting the host's `.credentials.json` read-write into jobs. A job could then corrupt or replace the host login for every later job, and the login would be shared between attempts of different projects.

## Facts and open questions

What is known on the pinned job image (Claude Code 2.1.326):

- `claude --help` lists `setup-token`, "Set up a long-lived authentication token".
- The runner's README already names `CLAUDE_CODE_OAUTH_TOKEN` as a credential variable that must not enter a Claude Docker job's environment.

Not verified. Each item needs a spike before the plan relies on it:

- **Q1.** Does Claude Code accept a `setup-token` token from a file, for example as `.credentials.json` with a far expiry or through `apiKeyHelper` in managed settings, without an environment variable?
- **Q2.** Is there a command that refreshes an expired access token without running a model turn, such as `claude auth status` or `claude auth refresh`, and does it write the result to `.credentials.json`?
- **Q3.** When Claude Code refreshes, does it write `.credentials.json` by atomic rename or in place? The answer decides whether the runner can read the job copy safely while the job is running.
- **Q4.** How long is the access token valid, and how long is the rotated refresh token valid? The 3c record saw "a few hours" for the access token.

## Approaches

### A. Write-back under a host lock, with a serialized pre-start refresh (recommended)

- **Lock.** All reads and writes of the host login take an exclusive `flock` on a lock file next to it (`<config dir>/.credentials.json.our-runner.lock`). Copy-in at job start takes the same lock.
- **Write-back.** The runner watches each job copy from the host side. It checks at job end, and every few seconds while the job runs, so concurrent jobs see a refresh quickly. When the copy has a different refresh token and a later `expiresAt` than the host login, the runner writes it to the host by temp file, `fsync` and rename, with mode 0600. It writes only the OAuth block it understands and keeps unknown fields from the host file. A copy that is older, unparsable or the same is ignored.
- **Pre-start refresh.** Before a job starts, under the lock: if the host access token expires within a margin (configurable, default 30 minutes), the runner refreshes once using Claude Code's own path (Q2), in a short container from the job image with only the login file. Then it writes the result back the same way. Jobs that start together therefore hold a token that will not expire during the margin. The refresh token is used by one process at a time.
- **Mid-job expiry.** A job that runs past its token's expiry refreshes in its copy, and the write-back takes that result. Two running jobs that both pass expiry at the same moment can still race. The margin makes that rare. A job that loses the race fails with the existing `agent_exit_failed`, and its retry starts from the written-back login.
- **Trade-off.** It fixes the login every owner already has, with no new owner step. Its cost is the lock, the watcher, and the dependency on Q2. If Q2 has no answer, the pre-start step can run a minimal `claude -p` turn instead, which costs one small request per refresh.

### B. Long-lived token from `setup-token`, delivered as a file

- The owner runs `claude setup-token` once and points the runner at the result with a new local key, for example `provider_oauth_token: {file: ...}`, validated like `provider_api_keys` files.
- The runner writes the token into each job home in the form Claude Code reads without an environment variable (Q1). No refresh, no write-back, no race.
- **Trade-off.** It is simple and race-free, but every owner has to take a new step, and it depends entirely on Q1. If Claude Code reads the token only from `CLAUDE_CODE_OAUTH_TOKEN`, B is not possible under Docker isolation while the scrub stays off.

### C. Write-back at job end only

- This is the smallest change: write the copy back, under the lock, when the job ends.
- **Trade-off.** It fixes the one-job-then-fail case only with `max_concurrent: 1`. With the product default of 3 it still fails, as described under Problem. It is a subset of A, so it is not offered on its own.

## Recommendation

Use A for 0.4.2, starting with a spike on Q2, Q3 and Q4 in the pinned job image. Run the spike for Q1 at the same time. If Q1 is confirmed, B becomes an opt-in alternative in a later slice for owners who prefer a fixed token. If Q1 is not confirmed, B is dropped and README says why.

## Design for A

- **New package `internal/claudelogin`:**
  - `Lock(configDir) (unlock func(), err error)`: a `flock` on the lock file, with a bounded wait.
  - `Read(path) (Login, error)`: parses only `claudeAiOauth.accessToken`, `refreshToken` and `expiresAt`, and keeps the raw document.
  - `Newer(a, b Login) bool`: the refresh token differs and `expiresAt` is later.
  - `WriteBack(hostPath string, candidate []byte) (bool, error)`: under the lock, compare and write atomically. Unknown fields come from the host document.
- **`isolation.PrepareHome`:** copy-in takes the lock. It returns the job copy's path, so the daemon can watch it.
- **Daemon:** a write-back watcher per job, from job start until after the provider exits, then a final write-back before `CleanupHome`. The watcher never logs token values. A write-back logs only `claude login written back` with the new `expiresAt`.
- **Pre-start refresh:** in the daemon before `PrepareHome`, gated on the margin, through the same Docker isolation path as a job, with network limited to the provider's sign-in hosts. Its failure fails the job start with a new failure code `provider_login_refresh_failed`, so the owner sees the cause instead of `agent_exit_failed`.
- **Scope:** only for a file login (`CLAUDE_CONFIG_DIR` or `~/.claude`). The keychain source and API key mode skip all of it.
- **`doctor`:** reports the login source, whether write-back applies, and the host token's expiry, without any token value.
- **Tests:**
  - lock contention between two writers;
  - an older copy never replaces a newer host login;
  - a truncated or unparsable copy is ignored;
  - the host keeps unknown fields;
  - mode 0600 after a write;
  - the final write-back runs before cleanup on success, failure and cancel;
  - the pre-start refresh runs once for three jobs that start together;
  - no token value appears in any log or event (the existing redaction tests extended).
- **3c:** a run with `max_concurrent: 3` where the host access token is expired at the start. Every job in the run succeeds, and the host login's hashes change exactly once per refresh.

## Left to the plan

- The watcher's interval and the refresh margin's default, within the bounds above.
- Whether the pre-start refresh container reuses the job network policy or gets its own allowlist.
