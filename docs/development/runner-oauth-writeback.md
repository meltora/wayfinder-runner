# Subscription login survives token refresh (RUNNER-OAUTH-WRITEBACK, 0.4.3)

## Control

| Field | Value |
|---|---|
| ID | `runner-oauth-writeback` |
| Kind | slice |
| Status | design approved (approach A, master, 2026-11-26), then narrowed to hand-on only (no job write-back, master, 2026-11-26); spikes done; plan written; Tasks 1 to 6 done; Task 7 in progress |
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
- **Constraint found while planning: the runner cannot read a running job's home.** In Docker isolation the job home is handed to the container user, uid 10001, before the agent starts (`job.go`, `handOffOwnership`, `Private: jobHome`). On the host that is a subordinate uid, and the home is 0700. The run 7 credential watch lost sight of the job copy at hand-off. Reclaim at the job's end gives the home back to the runner before `CleanupHome`. So the final check can read the copy directly, but every read or write while the job runs needs the Docker user namespace.
- **Side finding: other OAuth tokens copied into jobs.** The host `.credentials.json` also holds `mcpOAuth`, the OAuth tokens of the owner's MCP servers and plugins. Today the runner copies the whole file into every job home, so these tokens reach the job without any need. This slice copies only `claudeAiOauth` into jobs, and every runner write to the host (a scheduled refresh) keeps the host's other top-level keys untouched.

## Approaches

- **A. Runner-owned sync and scheduled refresh (chosen, without the job write-back).** The details follow below. The first version of A also wrote a job's refreshed login back to the host; that part was removed, see Decisions.
- **B. Long-lived `setup-token` login, opt-in (later).** Q1 confirms it is possible without an environment variable: the runner writes `{accessToken, expiresAt, scopes}` with no refresh token into the job copy. This is a separate slice after A, opt-in for owners who prefer a fixed token.
- **C. Write-back at job end only.** Rejected: with `max_concurrent` above 1 the jobs still race, as described under Problem, and it has the same account problem as any job write-back.

## Decisions

- **Approach A with the anti-swap check** (master, 2026-11-26).
- **Tasks 1 and 3 ship early in v0.4.2; the sync and the scheduled refresh are 0.4.3** (master).
- **A job never writes to the host login** (master's decision, 2026-11-26, not the user's). It replaces the job write-back of the first version of A.
  - Why: every account claim in a job copy (`accountUuid` and `organizationUuid` in `claudeAiOauth`, `oauthAccount` in `.claude.json`) is written by the job, and the agent controls the job home. A job could plant another account's valid login with matching claims. Written to the host, it would send the owner's later interactive Claude Code conversations on the host to someone else's account. That is a confidentiality problem, not only an integrity one. Checking more fields only narrows it, because the job can copy any field.
  - What remains: the host refreshes itself (Task 6) before Claude Code's own 5-minute margin, and hands the fresh login on to running jobs. `Decide` runs only host → job, inside the exchange.
  - A job rotation is reported, not written: when a job's copy has a refresh token other than its baseline, the runner logs `claude login rotated inside the job and was not written back; the host login may need a new sign-in` once per rotated hash, with `attempt_id` and `event=job_login_rotated`, and no value.
  - Residual risk: if a job's Claude Code refreshes before the runner does (the host refresh failed or was unavailable, a `401`, or a refresh at startup), the rotation invalidates the host refresh token. The next host refresh fails, other jobs on the same lineage fail at their next refresh, and the owner signs in again on the host. The warning above explains that failure. Task 7's live check shows whether the host refresh comes reliably before the 5-minute margin.
- **The exchange helper runs as the job user, not as container root** (master's decision, 2026-11-26, not the user's). The planned helper ran as container root. Without `DAC_OVERRIDE` that root cannot enter the job user's 0700 home, and with it the helper would need more privilege than the job. Running as uid 10001 with every capability dropped is least privilege, and the new file is the job user's own without a `chown`.
- **A job's baseline changes only after a hand-on to it**, so that a job still on its old baseline is recognised and gets the newer host login, and a job that rotated is reported.
- **`provider_login_refresh_failed` is out of scope** (master): it changes the wire contract.

## Design (A)

The runner keeps one login lineage on the host, and it is the only process that moves a login from the host into job copies. Nothing moves from a job copy to the host.

### Lineage and the hand-on check

- A job copy is created from a host login whose refresh token is `R0`. The runner remembers `R0` for that job, as a hash in memory only.
- The host login replaces a job copy only when all of these hold (`claudelogin.Decide` with the host as the candidate):
  1. both `claudeAiOauth` objects parse;
  2. the host's refresh token differs from `R0`, so the host refreshed;
  3. the host's `expiresAt` is later than the copy's;
  4. the copy's refresh token is still `R0` (compare-and-swap, the same rule Claude Code uses);
  5. the account matches. When both sides carry `accountUuid` and `organizationUuid` in `claudeAiOauth`, they must be equal. Otherwise the job's `.claude.json` `oauthAccount` must equal the host's on both fields. When neither file has an account id, the lineage check (1–4) alone decides, and `doctor` reports that the account check is unavailable.
- A failed check is never an error for the job. The copy is left as it is.
- A copy whose refresh token is neither `R0` nor the handed-on one means the job rotated the lineage. The runner logs the rotation warning from Decisions once per hash and does not write the host.

### Locks and writes

- Every read-modify-write of a login file, on the host or in a job copy, takes Claude Code's own two locks for that config directory, in Claude Code's order. The runner honours a lock younger than 60 s and retries for at most 10 s. It deletes a lock it did not create only when the lock is stale (mtime older than 60 s), which is proper-lockfile's own rule. It holds a lock for well under Claude Code's 5-attempt window, and refreshes the lock's mtime every 5 s while it holds it.
- Writes use a temp file, fsync, rename and mode 0600, through `safeio`. The runner replaces only the `claudeAiOauth` object and keeps every other top-level key from the target file as it is (`mcpOAuth` on the host).

### Sync

- **Exchange.** A running job's copy is reached through an exchange: a short helper container started by the runner, from the pinned job image, as the job user (uid 10001, the same subordinate uid that owns the home), with every capability dropped. It runs with `--network none`, and only the job home (read-write) and the runner's statically linked binary (read-only) are mounted. It runs `our-runner claude-login-exchange`.
  - The runner sends the host `claudeAiOauth`, the host account ids and the job's baseline hash on stdin, never in arguments or the environment.
  - Under the job copy's locks, the helper reads the copy and prints the copy's `claudeAiOauth` and account ids on stdout. If the copy is still on the baseline and the host login is newer and of the same account, it first replaces the copy's `claudeAiOauth` with the host's. The new file is the job user's own, with mode 0600, so no ownership change is needed.
  - The hand-on decision is `claudelogin.Decide`, run inside the helper with the host login as the candidate.
- **When it runs.** Every 2 minutes for each running Claude job; in the same tick after a scheduled refresh, for every running job; and on demand when the host file's mtime changes (an interactive sign-in or refresh), checked every 10 s. Claude Code in the jobs adopts a handed-on login on its next check (Q3).
- **No helper, no exchange.** Without the Docker helper the runner cannot reach a running job's home, so there is no exchange in process.
- **Final check.** It runs on every job end (success, failure, cancel), after reclaim and before `CleanupHome`. It reads the reclaimed copy directly through `os.OpenRoot` (`claudelogin.ReadJobLogin`) and only reports a rotation. A read error is logged as `claude login final check skipped`.

### Scheduled refresh

- So that jobs do not race at Claude Code's 5-minute mark, the runner refreshes first. When a Claude job is running, or one is about to start, and the host access token expires within 15 minutes, the runner refreshes once. It reads the host login under the host lock and releases the lock before the container starts, so Claude Code on the host is never blocked by the network round trip. It takes the lock again to write; if the host login changed in the meantime (Claude Code refreshed it itself), the runner drops its result with `reason=host_changed` and keeps the host's.
- The refresh runs `claude auth login` in a short container from the pinned job image, with `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and `CLAUDE_CODE_OAUTH_SCOPES` in that container's environment and a fresh empty config directory. No agent and no workspace run in that container, so the environment rule for job containers does not apply to it. The network is limited to Claude's sign-in hosts. The refresh token reaches the container through an `--env-file` in a private temporary directory, never through an argument.
- The result must have a refresh token, an `expiresAt` later than the host's, and the host's account when both carry one. An `expiresAt` more than 24 hours ahead is only warned about (`reason=implausible_expiry`) and still written, because the result comes from the pinned image and not from a job, and the server has already rotated the old refresh token. It is merged into the host file under the host lock and handed on to the running jobs.
- Once the server has answered, the old refresh token is revoked. So when a result is refused (`unparsable`, `older`, `account_mismatch`) or cannot be written (`write_failed`), the runner logs `claude login refresh rejected; host Claude login needs a new sign-in` with `event=host_login_needs_sign_in`, and does not retry that lineage.
- Network: the helper needs `platform.claude.com:443`, the token endpoint of Claude Code 2.1.326 (`https://platform.claude.com/v1/oauth/token`). On 2026-11-26 the local e2e network `our-e2e-egress` (policy label `p5f-e2e-egress-v1`) enforced nothing: from a uid 10001 container with every capability dropped, `platform.claude.com` and also `example.com` answered 200. The live refresh therefore proves the helper and the image, not the allowlist.
- Known limits: the refresh token sits in the helper container's environment, which `docker inspect` shows to anyone with Docker access on the host (already root-equivalent). `WriteFileAtomic` replaces a symlinked host `.credentials.json` with a regular file in the config directory. The sync tick handles jobs one after another.
- If the refresh fails, the runner logs `claude login refresh failed` with the exit status or the reason only, and tries again after 2 minutes. Jobs then fall back to Claude Code's own refresh, with the residual risk from Decisions.
- The refresh needs Docker isolation with the restricted job network. Without it the runner logs `claude login refresh unavailable` once.
- A job that cannot start because the login is unusable keeps today's failure code in this slice. A dedicated code (`provider_login_refresh_failed`) changes the wire contract and needs the control plane's agreement first.

### Scope and reporting

- All of this applies only to a file login (`CLAUDE_CONFIG_DIR`, or `~/.claude`). The keychain source and API key mode skip it.
- `doctor` reports the login source, whether sync applies, whether the account check is available, and the host token's expiry, without values.

### Tests (TDD, `internal/claudelogin` and the daemon)

- The lock is compatible with proper-lockfile's layout. A held lock blocks, a stale lock (older than 60 s) is taken over, and a fresh foreign lock is never deleted.
- The `Decide` rules, one row per reason: `older`, `unchanged`, `host_changed`, `account_mismatch` (in `claudeAiOauth` or in `.claude.json`) and `unparsable`.
- A job rotation never changes the host, and is reported once.
- Writes: only `claudeAiOauth` is replaced, `mcpOAuth` and unknown keys are kept, mode is 0600, and the write is a rename (no partial file is visible).
- Job copy-in contains only `claudeAiOauth`, never `mcpOAuth`.
- Fan-out: a host refresh reaches jobs 1, 2 and 3. A job that already has another login is not overwritten.
- A final check runs on success, failure and cancel.
- Scheduled refresh runs once for three jobs and never for an API key or keychain login, and its failure is logged without values.
- No token value appears in any log, event or error (the redaction tests extended).

### 3c for this slice

- A run with `max_concurrent: 3` and three overlapping jobs while the host access token passes its expiry. Every job succeeds.
- The host login's hashes change once per refresh, and the refresh comes before Claude Code's 5-minute margin, so no `job_login_rotated` event appears. Afterwards an interactive `claude` on the host still works, without signing in again.
- The live part needs a real refresh of the owner's login. If it goes wrong, the workaround in the known issue applies: sign in again on the host.

## Implementation plan

Execution: native TDD in this worktree, one commit per task, with one review of the whole branch at the end. Every task's tests are table-driven with fake logins only. No real token appears in a test, fixture or log. Each task's commit runs `sh scripts/check.sh format vet test` for the packages it touches.

### Global constraints

- Mode 0600 for every login file, and 0700 for every directory the runner creates.
- No login value in an argument, an environment variable of a job container, a log, an event or an error. Errors and logs name the file and the reason only.
- The `claudeAiOauth` object passes through as raw JSON. The runner reads only `accessToken` (presence), `refreshToken` (hash), `expiresAt`, `scopes`, `accountUuid` and `organizationUuid`, and never rewrites the other fields.
- Fingerprints: SHA-256 of the refresh token, kept in memory only and never logged.

### Review focus

1. Claude Code and the runner refresh at the same moment. Expected: one lineage wins. If the host wins, the jobs adopt it. If a job wins, the host is not written and the rotation is reported. No 401 loop. Test: Task 2's contention test, Task 5's rotation tests and Task 6's fan-out test.
2. The owner signs in again on the host, possibly with another account, while jobs run. Expected: running jobs are left alone if the account differs. A same-account login is handed on. Test: Task 1's `account_mismatch` and `host_changed` rows, and Task 5.
3. The runner crashes in the middle of a write. Expected: the old or the new file, never a partial one. A stale lock is taken over after 60 s. Test: Task 2.
4. A host file with keys the runner does not know about (`mcpOAuth`, future fields). Expected: kept byte for byte. Test: Task 1's merge test.
5. A job ends while an exchange with it is running. Expected: the final check waits for the exchange, and there is no write into a removed home. Test: Task 5.

### Task 1: `internal/claudelogin`, login model and the anti-swap decision

Files: create `internal/claudelogin/login.go` and `login_test.go`.

- `type Login struct { OAuth json.RawMessage; RefreshHash [32]byte; HasRefresh bool; ExpiresAt int64; Account Account }`
- `type Account struct { AccountUUID, OrganizationUUID string }`, with `func (a Account) Known() bool`.
- `func ParseCredentials(data []byte) (Login, error)`, which takes the account from `claudeAiOauth` when present.
- `func ParseAccount(claudeJSON []byte) (Account, error)`, which reads `oauthAccount`.
- `func MergeCredentials(target []byte, oauth json.RawMessage) ([]byte, error)` replaces `claudeAiOauth` and keeps every other top-level key, raw and in the same order.
- `func JobCredentials(host []byte) ([]byte, error)` returns `{"claudeAiOauth": ...}` only.
- `type Reason string`, with the values `ok`, `unchanged`, `older`, `host_changed`, `account_mismatch` and `unparsable`.
- `func Decide(candidate, host Login, baseline [32]byte, candidateAccount, hostAccount Account) Reason` applies rules 1–5 of "Lineage and the hand-on check".
- Tests:
  - `TestDecide` has one row per reason. It also covers an account known on one side only (that row is `account_mismatch`), an account in `claudeAiOauth` taking precedence over `.claude.json`, and equal `expiresAt` (`older`).
  - `TestMergeCredentialsKeepsOtherKeys` covers `mcpOAuth`, an unknown key and key order.
  - `TestJobCredentialsDropsOtherKeys`.
  - `TestParseCredentialsRejects` covers truncated JSON, a missing `claudeAiOauth` and a non-numeric `expiresAt`.

### Task 2: Lock compatible with Claude Code

Files: create `internal/claudelogin/lock.go` and `lock_test.go`.

- `func LockConfigDir(ctx context.Context, configDir string, options LockOptions) (unlock func() error, err error)`. `LockOptions{Stale, Update, Retry, Timeout, Now}` defaults to 60 s, 5 s, 200 ms, 10 s and `time.Now`. Ownership on release is checked by the same inode and the exact mtime the runner last set, because a recreated lock directory can reuse the inode.
  - It takes `<configDir>/.oauth_refresh.lock`, then `<EvalSymlinks(configDir)>.lock`, by `os.Mkdir`, retrying every 200 ms for at most 10 s.
  - It takes over a lock whose mtime is older than 60 s: it renames the lock to a unique name, removes it only when the renamed directory is still the stale one (same inode and mtime), puts it back otherwise, then mkdirs.
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
  - It returns the job login as it was before any write. The daemon uses `Job` only to detect a job rotation; it never writes it to the host.
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

### Task 5: Daemon sync (registry, exchanges, rotation report, final check)

Files: create `internal/daemon/claude_login_sync.go` and its tests (`claude_login_sync_test.go`, `job_claude_login_sync_test.go`); modify `internal/daemon/job.go` (register after hand-off, stop before reclaim, final check in the home cleanup defer), `job_ownership.go` (`beforeRelease` hooks) and `daemon.go` (create the sync and run its loop on the spool context). Add `claudelogin.ReadJobLogin(home string) (Login, Account, error)`, which reads the reclaimed copy through `os.OpenRoot`.

- `type claudeLoginSync struct` holds a registry of running jobs keyed by attempt id and the last seen mtime of each host config dir, plus the dependencies `exchange`, `refresh`, `now` and `logger`. Each `claudeLoginJob` carries its `isolation.JobHome`, its baseline, the last reported rotation hash, the job's logger and an exchange mutex.
- `register(attemptID string, home isolation.JobHome, logger *slog.Logger) *claudeLoginJob` returns nil for a home that does not sync a file login; every method is nil-safe. The exchanger is the docker helper when the ownership supports `ExchangeLogin`; without it there is no exchange.
- `stop()` takes the job's mutex, so it waits for a running exchange and blocks later ones. `job.go` registers it with `ownership.beforeRelease`, so no exchange runs once the home is being reclaimed.
- After an exchange: on `Replaced` the job's baseline becomes the host's hash and `claude login handed on` is logged; otherwise the returned copy goes to `noteRotation`.
- `noteRotation` logs the rotation warning from Decisions once per rotated hash. It never writes the host.
- `final()` removes the job from the registry, stops it, and once only reads the reclaimed copy with `ReadJobLogin` and runs `noteRotation`. It runs first in the home cleanup defer, on every exit path.
- The loop exchanges with each job every 2 minutes. It checks the host mtime every 10 s, and a change triggers an exchange with all jobs of that host dir; there is no separate wake channel. Exchanges with different jobs run one after another in attempt id order.
- Tests, against a fake exchange and a temp host dir:
  - a host change is handed to jobs 1, 2 and 3;
  - a job that rotated keeps its copy, the host is unchanged, and the rotation is logged once;
  - a host change by another account: no hand-on;
  - final check of the reclaimed copy, idempotent, and no exchange after stop;
  - final check through the job harness on a clean exit, a provider failure and a cancel, with the host login untouched;
  - exchanges are stopped before the home is reclaimed;
  - stop during an exchange waits for it;
  - a failed exchange is logged without values, and no log capture contains a fake token.

### Task 6: Scheduled refresh

Files: create `internal/claudelogin/refresh.go`, `cmd/our-runner/claude_login_refresh.go` (the `claude-login-refresh` case in `main.go`), `internal/isolation/login_refresh.go` and `internal/daemon/claude_login_refresh.go`, each with its test.

- `claudelogin.RefreshGrant(oauth) (token string, scopes []string, err error)` and `CheckRefreshGrant(token, scopes) error`. The token is printable ASCII without spaces, at most 4096 bytes; there are 1 to 32 scopes, each matching `^[A-Za-z0-9:._-]{1,128}$`.
- `our-runner claude-login-refresh` takes no arguments, reads both variables from its environment, runs `claude auth login` with `CLAUDE_CONFIG_DIR=/tmp/claude-login` and `HOME=/tmp` within 90 s, discards its output, and prints the saved `.credentials.json`. Failures are fixed messages without values.
- `func (o DockerOwnership) RefreshLogin(ctx context.Context, in LoginRefresh) ([]byte, error)`, with `type LoginRefresh struct { RunnerID, Network, RefreshToken string; Scopes []string }`.
  - It runs the helper subcommand in a container from the pinned job image as uid 10001, with every capability dropped, `no-new-privileges`, a read-only root, a 16 MiB tmpfs at `/tmp`, logging off, and the runner binary mounted read-only as entrypoint, within 2 minutes.
  - `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` and `CLAUDE_CODE_OAUTH_SCOPES` (space-separated) are passed with `--env-file` from a 0600 file in a private temp dir under the state dir, removed afterwards, so the values never appear in an argument list.
  - The network is the restricted job network (never `host`, `none` or `bridge`); whether it reaches the sign-in hosts is checked in Task 7.
  - It returns the new `.credentials.json` bytes.
- The daemon refreshes when the host `expiresAt` is within 15 minutes and either a registered job of that host dir exists (checked every 10 s) or a Claude subscription job is about to copy the login (`beforeStart`, synchronous, before `PrepareHomeWithLogin`). Without the second case an idle runner's first job would start with an expired token and Claude Code in it would rotate the lineage. It does this under the host lock, re-reads the host under it, and refreshes at most once per lineage. It checks the result as in "Scheduled refresh", merges it into the host file, and in the same tick hands it on to all jobs.
- On failure it logs `claude login refresh failed` with the exit status or reason (`older`, `unparsable`, `implausible_expiry`, `account_mismatch`) only, and tries again after 2 minutes. Without Docker isolation and the restricted job network it logs `claude login refresh unavailable` once.
- In this slice, a job start with an unusable login keeps today's failure code. The new code `provider_login_refresh_failed` changes the wire contract (`api/runner/v1/job_finished.go`), so it waits for agreement with the control plane.
- Tests:
  - one refresh for three jobs;
  - none for an API key or keychain login;
  - none when expiry is more than 15 minutes away;
  - a failure is logged without values and retried;
  - the argument list and the environment never contain the fake refresh token.

### Task 7: Doctor, docs and live check

Files: modify `internal/doctor/doctor.go` and its test, `README.md` (subscription login section) and the known issue in `docs/development/release-0.4.1.md`; add a 0.4.3 release note.

- The doctor check `claude-login-sync` (`internal/doctor/claude_login_check.go`, reading the host through `isolation.HostClaudeLogin`) reports the source (`file`, `keychain`, none, or API key only), whether sync applies, whether the scheduled refresh and the account check are available, and the time to expiry rounded to minutes.
- Live check, which needs the master's approval because it refreshes the real login:
  1. one `RefreshLogin` against the host login, with hashes before and after, and an interactive `claude` on the host still working afterwards;
  2. then the 3c run with `max_concurrent: 3` described above.

#### Live check 1 (2026-11-26, master-approved single refresh of `~/.claude-personal`)

- Setup: the daemon's `refreshHost` with `DockerOwnership.RefreshLogin`, a statically built runner, the local job image `local/our-runner-job:e2e-611eaa7` (Claude Code 2.1.299), and a temporary user-defined bridge network (removed afterwards). This host has no restricted job network, so the network leg is not verified here.
- A dry run with a fake refresh token reached the sign-in server (`400`, exit 1) in under a second. The read-only root with the `/tmp` tmpfs was enough for `claude auth login`.
- The real refresh, in hashes (SHA-256 prefixes, no values):
  - refresh token `4169bcc58944d301` → `f417393bbbfde216`;
  - file `c7f05e1613b62377` → `945bf892e613820c`;
  - every other top-level key unchanged (`7a0ea5de23c8d82a` before and after); mode 0600;
  - access token lifetime 276 → 479 minutes; the refresh took 13 s.
- Afterwards `claude -p` with the same `CLAUDE_CONFIG_DIR` answered, and the refresh token was still `f417393bbbfde216` (Claude Code only rewrote other keys).
- Timing: the refresh starts within 10 s of entering the 15-minute window and took 13 s, with a 2-minute retry. That leaves about 4 retries before Claude Code's 5-minute margin.
- Open: the token endpoint in this Claude Code is `platform.claude.com`. The restricted job network must allow it. Jobs on that network already refreshed in the 3c runs, so it is reachable there (inferred, not checked). The 3c run with `max_concurrent: 3` is still to do.
- Found: a refresh whose result is refused (`older`, `implausible_expiry`, `account_mismatch`) has already rotated the lineage, so the host login then needs a new sign-in. Refusing is still the safe choice, and the failure is logged.

## Follow-up

- The live 3c run with `max_concurrent: 3` is not done. On 2026-11-26 the isolated 3c stack had already been torn down: its PKI, Agent Pack signing key, pack and test repository were deleted. Rebuilding them would have meant changing that stack's e2e database. 0.4.3 shipped on the unit tests and the live refresh above. Run it the next time 3c is brought up for other work: three overlapping jobs while the host access token passes its expiry, then the checks listed under "3c for this slice".
