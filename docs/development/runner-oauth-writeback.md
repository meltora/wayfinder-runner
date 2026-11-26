# Subscription login survives token refresh (RUNNER-OAUTH-WRITEBACK, 0.4.3)

## Control

| Field | Value |
|---|---|
| ID | `runner-oauth-writeback` |
| Kind | slice |
| Status | design approved (approach A, master, 2026-11-26); spikes done; plan written; Tasks 1 to 5 done; Task 6 in progress |
| Target | 0.4.3. Task 1 (`47bf54a`) and Task 3 (`a7a34ba`, job copy narrowed to `claudeAiOauth`) shipped early in v0.4.2. |
| Risk | high. The runner writes the owner's Claude login on the host. A defect could lose the login, write a stale one over a fresh one, swap in another account's login, or leak it. |
| Baseline | app-runner `main` at `105182f` (v0.4.1) |
| Updated | 2026-11-26 |

## Problem

This is the known issue in [release-0.4.1.md](release-0.4.1.md), "Subscription login works for one job, then OAuth session expired".

- `isolation.PrepareHome` (`internal/isolation/home.go`) copies the host's `.credentials.json` (and `.claude.json`) into a fresh job home with mode 0600. `CleanupHome` deletes the home when the job ends (`internal/daemon/job.go` around line 640).
- When the access token is about to expire, Claude Code in the job refreshes it in the job copy. The sign-in server rotates the refresh token, so the host's refresh token is now invalid.
- The refreshed copy is deleted with the job home. Every later job starts from the host's invalid refresh token and fails within seconds with `Failed to authenticate: OAuth session expired and could not be refreshed`.
- Concurrent jobs make it worse. With `max_concurrent` above 1, every running job holds the same access token, with the same expiry, and the same refresh token. Claude Code refreshes 5 minutes before expiry, so running jobs reach the refresh at the same moment. The first one rotates the refresh token, and the other jobs' refreshes fail.
- Interactive Claude Code sessions on the host can use the same config directory. They refresh the host login as well. The 3c record saw this once.

API key sign-in (`provider_api_keys`) is not affected.

## Goals

- A runner signed in with a Claude subscription keeps working across access-token expiry, with any `max_concurrent`, without the owner signing in again.
- The host login is never replaced by an older one, never by another account's, never truncated and never left half-written. This is the anti-swap check required by the master.
- No new place where the login can leak. It stays a file under the sandbox's denied paths (`provider.go` 265 and 273). No credential value enters a Docker job's environment, because Claude Docker jobs run without `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB` (README, "Claude Docker jobs also run without ...").

## Non-goals

- Codex and OpenCode logins. They are a follow-up once the Claude path is proven.
- Cloud mode and remote configuration of credentials. Credential sources stay local (R-05 in [runner-api-key-credentials.md](runner-api-key-credentials.md)).
- The macOS keychain source. It cannot be written back, so it keeps today's behaviour, and `doctor` says so.

## Anti-goals

- The runner calling Claude's OAuth token endpoint itself. The endpoint, client id and rotation rules are Claude Code internals, so the runner's refreshes go through the `claude` binary in the pinned job image.
- Bind-mounting the host's `.credentials.json` read-write into jobs.

## Spike results (Claude Code 2.1.326, pinned job image)

These come from a static reading of the compiled binary, plus `claude --help` in the image. No login was used or refreshed. "Inferred" marks conclusions that were not seen directly in code.

- **Q1. Long-lived token without an environment variable: confirmed.** A `.credentials.json` with `claudeAiOauth.accessToken`, `expiresAt` far ahead, `scopes` containing `user:inference` and no `refreshToken` is used as is. The refresh check returns "not needed", and a `401` has no recovery. `claude setup-token` issues a 365-day token (`TPe=31536000`); inferred, its scope is `user:inference` only. Claude Code never writes such a login itself, so the runner would write it.
  - `CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR` also exists. A regular-file descriptor stays readable through `/proc/<pid>/fd` by any process with the same uid, so it is not used.
  - `CLAUDE_CODE_HOST_CREDS_FILE` is a gateway mechanism that copies values into `process.env`, so it is not used either.
- **Q2. Refresh without a model turn: partly.** `claude auth` has only `login`, `status` and `logout`, and `status` only reads.
  - `claude auth login` with `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and `CLAUDE_CODE_OAUTH_SCOPES` performs a refresh-token grant without a model turn and saves the result. Inferred: the grant rotates the refresh token, and the save happens only when the response carries a refresh token.
  - Claude Code refreshes 5 minutes before `expiresAt`, and on a `401`. Startup requests (remote settings, policy limits) can trigger a refresh before any turn.
- **Q3. Claude Code's own write is safe and cooperative: confirmed.**
  - **Lock:** the refresh takes two proper-lockfile locks, in this order: `<config dir>/.oauth_refresh.lock`, then the legacy `<realpath(config dir)>.lock`, which sits next to the config directory. Both are `mkdir` locks, stale after 60 s, with the mtime refreshed every 5 s. Claude Code tries 5 times with a 1–2 s pause, then gives up on that refresh. It also writes an owner record `.oauth_refresh.lock.owner` (pid, pid namespace). Without a record it falls back to the 60 s staleness rule, and the runner writes none.
  - **Re-read:** before refreshing, and again under the lock, Claude Code re-reads `.credentials.json` when its mtime changed. If the access token changed, it adopts it without a network call.
  - **Compare-and-swap:** the save writes only if the stored `refreshToken` is still the one it sent.
  - **Write:** temp file with `O_EXCL`, fsync, rename, then mode 0600. On a 401, Claude Code first re-reads the file and adopts a changed token.
  - **Consequence:** a login the runner writes into a running job's copy is picked up by that job, and a stale write by that job cannot replace it.
- **Q4. Lifetimes and identity.** `expiresAt = now + expires_in`. A `refreshTokenExpiresAt` field exists, and the host login has one.
  - Claude Code writes `accountUuid` and `organizationUuid` into `claudeAiOauth` at sign-in, but the host login on the 3c machine does not have them, because it is an older login. The account is also in `.claude.json` under `oauthAccount` (`accountUuid`, `organizationUuid`).
  - Claude Code's own same-account test compares `accountUuid` and `organizationUuid`.
- **Constraint found while planning: the runner cannot read a running job's home.** In Docker isolation the job home is handed to the container user, uid 10001, before the agent starts (`job.go`, `handOffOwnership`, `Private: jobHome`). On the host that is a subordinate uid, and the home is 0700. The run 7 credential watch lost sight of the job copy at hand-off. Reclaim at the job's end gives the home back to the runner before `CleanupHome`. So the final sync can read the copy directly, but every read or write while the job runs needs the Docker user namespace.
- **Side finding: other OAuth tokens copied into jobs.** The host `.credentials.json` also holds `mcpOAuth`, the OAuth tokens of the owner's MCP servers and plugins. Today the runner copies the whole file into every job home, so these tokens reach the job without any need. This slice copies only `claudeAiOauth` into jobs, and every write-back keeps the host's other top-level keys untouched.

## Approaches

- **A. Runner-owned sync and scheduled refresh (chosen).** The details follow below.
- **B. Long-lived `setup-token` login, opt-in (later).** Q1 confirms it is possible without an environment variable: the runner writes `{accessToken, expiresAt, scopes}` with no refresh token into the job copy. This is a separate slice after A, opt-in for owners who prefer a fixed token.
- **C. Write-back at job end only.** Rejected: with `max_concurrent` above 1 the jobs still race, as described under Problem.

## Decisions

- **Approach A with the anti-swap check** (master, 2026-11-26).
- **Tasks 1 and 3 ship early in v0.4.2; the write-back is 0.4.3** (master).
- **The exchange helper runs as the job user, not as container root** (master's decision, 2026-11-26, not the user's). The planned helper ran as container root. Without `DAC_OVERRIDE` that root cannot enter the job user's 0700 home, and with it the helper would need more privilege than the job. Running as uid 10001 with every capability dropped is least privilege, and the new file is the job user's own without a `chown`.
- **A write-back does not move other jobs' baselines.** A job's baseline changes only after a write-back from that job or a hand-on to it, so that a job still on its old baseline is recognised and gets the newer host login.
- **`provider_login_refresh_failed` is out of scope** (master): it changes the wire contract.

## Design (A)

The runner keeps one login lineage on the host, and it is the only process that moves a login between the host and job copies.

### Lineage and the anti-swap check

- A job copy is created from a host login whose refresh token is `R0`. The runner remembers `R0` for that job, as a hash in memory only.
- A job copy may be written back only when all of these hold:
  1. its `claudeAiOauth` parses;
  2. its refresh token differs from `R0`, so the job refreshed;
  3. its `expiresAt` is later than the host's;
  4. the host's refresh token is still `R0` (compare-and-swap, the same rule Claude Code uses);
  5. the account matches. When both sides carry `accountUuid` and `organizationUuid` in `claudeAiOauth`, they must be equal. Otherwise the job's `.claude.json` `oauthAccount` must equal the host's on both fields. When neither file has an account id, the lineage check (1–4) alone decides, and `doctor` reports that the account check is unavailable.
- A failed check is never an error for the job. The copy is ignored, and the runner logs `claude login write-back skipped` with the reason (`older`, `host_changed`, `account_mismatch`, `unparsable`) and no values.

### Locks and writes

- Every read-modify-write of a login file, on the host or in a job copy, takes Claude Code's own two locks for that config directory, in Claude Code's order. The runner honours a lock younger than 60 s and retries for at most 10 s. It deletes a lock it did not create only when the lock is stale (mtime older than 60 s), which is proper-lockfile's own rule. It holds a lock for well under Claude Code's 5-attempt window, and refreshes the lock's mtime every 5 s while it holds it.
- Writes use a temp file, fsync, rename and mode 0600, through `safeio`. The runner replaces only the `claudeAiOauth` object and keeps every other top-level key from the target file as it is (`mcpOAuth` on the host).

### Sync

- **Exchange.** A running job's copy is reached through an exchange: a short helper container started by the runner, from the pinned job image, as the job user (uid 10001, the same subordinate uid that owns the home), with every capability dropped. It runs with `--network none`, and only the job home (read-write) and the runner's statically linked binary (read-only) are mounted. It runs `our-runner claude-login-exchange`.
  - The runner sends the host `claudeAiOauth`, the host account ids and the job's baseline hash on stdin, never in arguments or the environment.
  - Under the job copy's locks, the helper reads the copy and prints the copy's `claudeAiOauth` and account ids on stdout. If the copy is still on the baseline and the host login is newer and of the same account, it first replaces the copy's `claudeAiOauth` with the host's. The new file is the job user's own, with mode 0600, so no ownership change is needed.
  - The anti-swap decision is the same Go code (`claudelogin.Decide`) on both sides.
- **When it runs.** Every 2 minutes for each running Claude job; right after a scheduled refresh, for every running job; and on demand when the host file's mtime changes (an interactive sign-in or refresh), checked every 10 s.
- **Write-back.** When the copy an exchange returns passes the anti-swap check, the runner writes it to the host. The next exchange with every other running job then hands it on. Claude Code in those jobs adopts it on its next check (Q3).
- **Final sync.** It runs on every job end (success, failure, cancel), after reclaim and before `CleanupHome`. It reads the reclaimed copy directly, without a helper.

### Scheduled refresh

- So that jobs do not race at Claude Code's 5-minute mark, the runner refreshes first. When a Claude job is running, or one is about to start, and the host access token expires within 15 minutes, the runner refreshes once under the host lock.
- The refresh runs `claude auth login` in a short container from the pinned job image, with `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and `CLAUDE_CODE_OAUTH_SCOPES` in that container's environment and a fresh empty config directory. No agent and no workspace run in that container, so the environment rule for job containers does not apply to it. The network is limited to Claude's sign-in hosts. The refresh token reaches the container through an `--env-file` in a private temporary directory, never through an argument.
- The result is checked like a job copy, written to the host, and fanned out to the running jobs.
- If the refresh fails, the runner logs `claude login refresh failed` with the exit status only. Jobs then fall back to Claude Code's own refresh.
- A job that cannot start because the login is unusable keeps today's failure code in this slice. A dedicated code (`provider_login_refresh_failed`) changes the wire contract and needs the control plane's agreement first.

### Scope and reporting

- All of this applies only to a file login (`CLAUDE_CONFIG_DIR`, or `~/.claude`). The keychain source and API key mode skip it.
- `doctor` reports the login source, whether sync applies, whether the account check is available, and the host token's expiry, without values.

### Tests (TDD, `internal/claudelogin` and the daemon)

- The lock is compatible with proper-lockfile's layout. A held lock blocks, a stale lock (older than 60 s) is taken over, and a fresh foreign lock is never deleted.
- The anti-swap rules:
  - an older copy is skipped;
  - a copy with an unchanged refresh token is skipped;
  - a host already changed by someone else is skipped (`host_changed`);
  - a different `accountUuid` or `organizationUuid` is skipped, in `claudeAiOauth` or in `.claude.json`;
  - a truncated or unparsable copy is skipped.
- Writes: only `claudeAiOauth` is replaced, `mcpOAuth` and unknown keys are kept, mode is 0600, and the write is a rename (no partial file is visible).
- Job copy-in contains only `claudeAiOauth`, never `mcpOAuth`.
- Fan-out: a refresh in job 1 reaches jobs 2 and 3. A job that already has a newer login is not overwritten.
- A final sync runs on success, failure and cancel.
- Scheduled refresh runs once for three jobs and never for an API key or keychain login, and its failure is logged without values.
- No token value appears in any log, event or error (the redaction tests extended).

### 3c for this slice

- A run with `max_concurrent: 3` and three overlapping jobs while the host access token passes its expiry. Every job succeeds.
- The host login's hashes change once per refresh. Afterwards an interactive `claude` on the host still works, without signing in again.
- The live part needs a real refresh of the owner's login. If it goes wrong, the workaround in the known issue applies: sign in again on the host.

## Implementation plan

Execution: native TDD in this worktree, one commit per task, with one review of the whole branch at the end. Every task's tests are table-driven with fake logins only. No real token appears in a test, fixture or log. Each task's commit runs `sh scripts/check.sh format vet test` for the packages it touches.

### Global constraints

- Mode 0600 for every login file, and 0700 for every directory the runner creates.
- No login value in an argument, an environment variable of a job container, a log, an event or an error. Errors and logs name the file and the reason only.
- The `claudeAiOauth` object passes through as raw JSON. The runner reads only `accessToken` (presence), `refreshToken` (hash), `expiresAt`, `scopes`, `accountUuid` and `organizationUuid`, and never rewrites the other fields.
- Fingerprints: SHA-256 of the refresh token, kept in memory only and never logged.

### Review focus

1. Claude Code and the runner refresh at the same moment. Expected: one lineage wins, and the other side adopts it. No 401 loop. Test: Task 2's contention test and Task 5's fan-out test.
2. The owner signs in again on the host, possibly with another account, while jobs run. Expected: running jobs are left alone if the account differs. A same-account login is handed on. Test: Task 1's `account_mismatch` and `host_changed` rows, and Task 5.
3. The runner crashes in the middle of a write. Expected: the old or the new file, never a partial one. A stale lock is taken over after 60 s. Test: Task 2.
4. A host file with keys the runner does not know about (`mcpOAuth`, future fields). Expected: kept byte for byte. Test: Task 1's merge test.
5. A job ends while an exchange with it is running. Expected: the final sync waits for the exchange, and there is no write into a removed home. Test: Task 5.

### Task 1: `internal/claudelogin`, login model and the anti-swap decision

Files: create `internal/claudelogin/login.go` and `login_test.go`.

- `type Login struct { OAuth json.RawMessage; RefreshHash [32]byte; HasRefresh bool; ExpiresAt int64; Account Account }`
- `type Account struct { AccountUUID, OrganizationUUID string }`, with `func (a Account) Known() bool`.
- `func ParseCredentials(data []byte) (Login, error)`, which takes the account from `claudeAiOauth` when present.
- `func ParseAccount(claudeJSON []byte) (Account, error)`, which reads `oauthAccount`.
- `func MergeCredentials(target []byte, oauth json.RawMessage) ([]byte, error)` replaces `claudeAiOauth` and keeps every other top-level key, raw and in the same order.
- `func JobCredentials(host []byte) ([]byte, error)` returns `{"claudeAiOauth": ...}` only.
- `type Reason string`, with the values `ok`, `unchanged`, `older`, `host_changed`, `account_mismatch` and `unparsable`.
- `func Decide(candidate, host Login, baseline [32]byte, candidateAccount, hostAccount Account) Reason` applies rules 1–5 of "Lineage and the anti-swap check".
- Tests:
  - `TestDecide` has one row per reason. It also covers an account known on one side only (that row is `account_mismatch`), an account in `claudeAiOauth` taking precedence over `.claude.json`, and equal `expiresAt` (`older`).
  - `TestMergeCredentialsKeepsOtherKeys` covers `mcpOAuth`, an unknown key and key order.
  - `TestJobCredentialsDropsOtherKeys`.
  - `TestParseCredentialsRejects` covers truncated JSON, a missing `claudeAiOauth` and a non-numeric `expiresAt`.

### Task 2: Lock compatible with Claude Code

Files: create `internal/claudelogin/lock.go` and `lock_test.go`.

- `func LockConfigDir(ctx context.Context, configDir string, options LockOptions) (unlock func() error, err error)`. `LockOptions{Stale, Update, Retry, Timeout, Now}` defaults to 60 s, 5 s, 200 ms, 10 s and `time.Now`. Ownership on release is checked by the same inode and the exact mtime the runner last set, because a recreated lock directory can reuse the inode.
  - It takes `<configDir>/.oauth_refresh.lock`, then `<EvalSymlinks(configDir)>.lock`, by `os.Mkdir`, retrying every 200 ms for at most 10 s.
  - It takes over a lock whose mtime is older than 60 s (`os.Remove`, then mkdir).
  - It updates both locks' mtime every 5 s until unlocked.
  - It removes only directories it created, and releases in reverse order.
  - On failure to take the second lock, it releases the first.
- `func WriteFileAtomic(dir, name string, data []byte, uid int) error`: temp file with `O_EXCL`, fsync, rename, fsync of the directory, mode 0600. It calls `chown` when `uid >= 0`. It reuses `safeio.AtomicWriteFile` if that function allows a chown hook; otherwise it is written next to it.
- Tests:
  - held lock: the call blocks, then fails with `ErrLocked` after the deadline (a fake clock);
  - stale lock: taken over;
  - fresh foreign lock: never removed;
  - the legacy lock path when the config dir is a symlink;
  - release after a failure on the second lock;
  - `WriteFileAtomic` leaves no temp file behind on error, and the mode is 0600.

### Task 3: Copy-in narrowed to `claudeAiOauth`

Files: modify `internal/isolation/home.go` (`copyConfiguredClaudeLogin`, and the `.credentials.json` copy in `PrepareHome`), and `home_test.go` and `claude_config_dir_home_test.go`.

- The copy writes `claudelogin.JobCredentials(host)`. `.claude.json` is still copied whole; Claude Code needs `oauthAccount` and onboarding state from it.
- `PrepareHomeWithLogin(...) (JobHome, error)` returns the baseline as well, with `type JobHome struct { Path string; ClaudeConfigDir string; Baseline [32]byte; SyncsLogin bool }`. `SyncsLogin` is true only for a file login with a refresh token. `PrepareHome` stays as a wrapper that returns `.Path`, so call sites that do not sync keep their signature.
- Tests:
  - the job copy has no `mcpOAuth`;
  - a keychain login has `SyncsLogin` false;
  - an API key provider has no login copy (the current behaviour, kept).

### Task 4: Exchange subcommand and the helper call

Files:

- create `internal/claudelogin/exchange.go` and its test;
- create `cmd/our-runner/claude_login_exchange.go` and its test;
- modify `cmd/our-runner/main.go` (the `claude-login-exchange` case);
- create `internal/isolation/login_exchange.go` and its test.

What each one holds:

- `type ExchangeRequest struct { Host json.RawMessage; HostAccount Account; Baseline [32]byte }` and `type ExchangeResult struct { Job json.RawMessage; JobAccount Account; Replaced bool; Reason Reason }`, as JSON on stdin and stdout.
- `func Exchange(ctx context.Context, home string, request ExchangeRequest, options LockOptions) (ExchangeResult, error)` works under `LockConfigDir(home/.claude)`.
  - It reads `.claude/.credentials.json` and `.claude.json` from the home through `os.OpenRoot`, so a symlink cannot lead out of the home. A missing or unparsable job copy or state is reason `unparsable`, with no write.
  - When `Decide(host, job, baseline, ...)` is `ok`, it writes the merged file with `WriteFileAtomic` (uid -1: the helper already runs as the job user).
  - It returns the job login as it was before any write. When the job has refreshed, the reason is `host_changed` or `older` and the daemon takes `Job` as the write-back candidate.
- The `cmd` side reads at most 1 MiB from stdin, rejects unknown fields and trailing data, runs `Exchange("/home/runner", ...)` within 25 s, and writes the result to stdout. The process exits non-zero with a fixed reason-only message.
- `func (o DockerOwnership) ExchangeLogin(ctx context.Context, runnerID, attemptID, home string, in claudelogin.ExchangeRequest) (claudelogin.ExchangeResult, error)` shares the helper validation with `helperArgs` and only accepts a home under `job-homes/`.
  - Purpose `login-exchange`; `--user 10001:10001`, `--cap-drop ALL`, `--read-only`, `--network none`, `--entrypoint` set to the mounted runner.
  - Mounts: the home rw at `/home/runner`; `os.Executable()` (symlinks resolved) read-only at `/opt/our-runner-helper/our-runner`.
  - It runs with a 30 s timeout. Stdin carries the request; stdout is capped at 1 MiB and never appears in an error.
- Tests:
  - `Exchange` on a temp home covers each reason, the lock, a symlink out of the home, and the write happens only on `ok`;
  - the helper argument test checks the exact argv, no `-e` carrying a login, and that the request goes to stdin only;
  - the `cmd` test rejects oversize input and unknown fields without echoing them;
  - a gated rootless Docker test hands a home off, exchanges, checks from inside a container that the new copy is a 0600 file of the job user, and reclaims.

### Task 5: Daemon sync (registry, exchanges, write-back, final sync)

Files: create `internal/daemon/claude_login_sync.go` and its tests (`claude_login_sync_test.go`, `job_claude_login_sync_test.go`); modify `internal/daemon/job.go` (register after hand-off, stop before reclaim, final sync in the home cleanup defer), `job_ownership.go` (`beforeRelease` hooks) and `daemon.go` (create the sync and run its loop on the spool context).

- `type claudeLoginSync struct` holds a registry of running jobs keyed by attempt id and the last seen mtime of each host config dir, plus the dependencies `exchange`, `now`, `lock` and `logger`. Each `claudeLoginJob` carries its `isolation.JobHome` (with `ClaudeConfigDir` and `ClaudeStateFile`), its baseline and an exchange mutex.
- `register(attemptID string, home isolation.JobHome) *claudeLoginJob` returns nil for a home that does not sync a file login; every method is nil-safe. The exchanger is the docker helper when the ownership supports `ExchangeLogin`, otherwise `claudelogin.Exchange` in process.
- `stop()` takes the job's mutex, so it waits for a running exchange and blocks later ones. `job.go` registers it with `ownership.beforeRelease`, so no exchange runs once the home is being reclaimed.
- `final()` removes the job from the registry, stops it, and once only reads the reclaimed copy directly and runs `writeBack` with a 30 s timeout. It runs first in the home cleanup defer, on every exit path.
- `writeBack(ctx, candidate Login, account Account)`:
  - it takes the host `LockConfigDir`;
  - it re-reads the host;
  - it runs `Decide`;
  - on `ok`, it writes `MergeCredentials(host, candidate)` with uid -1;
  - it sets the writing job's baseline to the new hash and leaves every other job's baseline alone: a job whose copy is still on its baseline gets the newer host login at its next exchange, and only then does its baseline become the handed-on hash;
  - it logs `claude login write-back` with the reason only.
- The loop exchanges with each job every 2 minutes. It checks the host mtime every 10 s, and a change triggers an exchange with all jobs of that host dir; there is no separate wake channel. Exchanges with different jobs run one after another in attempt id order; the daemon waits for no exchange while holding the host lock.
- Tests, against a fake or in-process exchange and a temp host dir:
  - a refresh in job 1 is written back and then handed to jobs 2 and 3;
  - job 3 already newer: not overwritten, `host_changed` logged;
  - a host change by another account: no hand-on;
  - final sync of the reclaimed copy, idempotent, and no exchange after stop;
  - final sync through the job harness on a clean exit, a provider failure and a cancel;
  - exchanges are stopped before the home is reclaimed;
  - stop during an exchange waits for it;
  - a failed exchange is logged without values, and no log capture contains a fake token.

### Task 6: Scheduled refresh

Files: create `internal/daemon/claude_login_refresh.go` and its test; modify `internal/isolation/login_exchange.go` (`RefreshLogin`).

- `func (o DockerOwnership) RefreshLogin(ctx context.Context, refreshToken string, scopes []string) ([]byte, error)`.
  - It runs `claude auth login` in a container from the pinned job image, with an empty temp config dir (0700, removed afterwards).
  - `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and `CLAUDE_CODE_OAUTH_SCOPES` are passed with `--env-file` from a 0600 file in the same temp dir, so the values never appear in an argument list.
  - The network is the job egress network, limited to `api.anthropic.com`, `console.anthropic.com` and `claude.ai`; this is checked in Task 7.
  - It returns the new `.credentials.json` bytes.
- The daemon refreshes when a registered job exists or a Claude job is starting and the host `expiresAt` is within 15 minutes. It does this under the host lock and at most once per lineage, then runs `writeBack` and exchanges with all jobs.
- On failure it logs `claude login refresh failed` with the exit status only, and tries again after 2 minutes.
- In this slice, a job start with an unusable login keeps today's failure code. The new code `provider_login_refresh_failed` changes the wire contract (`api/runner/v1/job_finished.go`), so it waits for agreement with the control plane.
- Tests:
  - one refresh for three jobs;
  - none for an API key or keychain login;
  - none when expiry is more than 15 minutes away;
  - a failure is logged without values and retried;
  - the argument list and the environment never contain the fake refresh token.

### Task 7: Doctor, docs and live check

Files: modify `internal/doctor/doctor.go` and its test, `README.md` (subscription login section) and the known issue in `docs/development/release-0.4.1.md`; add a 0.4.3 release note.

- The doctor line `claude login sync` reports the source (`file`, `keychain`, `api key`), whether sync applies, whether the account check is available, and the time to expiry rounded to minutes.
- Live check, which needs the master's approval because it refreshes the real login:
  1. one `RefreshLogin` against the host login, with hashes before and after, and an interactive `claude` on the host still working afterwards;
  2. then the 3c run with `max_concurrent: 3` described above.
