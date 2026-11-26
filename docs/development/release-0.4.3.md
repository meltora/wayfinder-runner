# Release 0.4.3: subscription login survives token refresh

Draft release notes for the patch release after v0.4.2. Design and decisions: [runner-oauth-writeback.md](runner-oauth-writeback.md).

## Compatibility

- **Control plane:** no app change and no wire change. A job whose login is unusable keeps today's failure code.
- **Configuration:** no new key. The scheduled refresh needs `isolation: docker` with `job_network: restricted`; without it the runner logs `claude login refresh unavailable` once and keeps the 0.4.2 behaviour.
- **No state migration.**
- **Egress allowlist:** a restricted job network that enforces an allowlist must allow `platform.claude.com:443`, the token endpoint of Claude Code 2.1.326. Without it every scheduled refresh fails and is logged as `claude login refresh failed`.

## Release notes

### The runner keeps the host Claude login fresh

- Fixes the 0.4.1 known issue "Subscription login works for one job, then OAuth session expired" for runners with Docker isolation, the restricted job network and a Claude file login (`CLAUDE_CONFIG_DIR` or `~/.claude`).
- When a Claude job is running, or a subscription job or a Claude work-intake proposal consultation is about to start, and the host access token expires within 15 minutes, the runner refreshes the host login once, before Claude Code's own refresh 5 minutes before expiry. The refresh runs `claude auth login` with the refresh grant in a short container from the pinned job image, as the job user, with every capability dropped and a read-only root. The refresh token reaches it only through a private 0600 `--env-file`, never an argument.
- The result is accepted only with a refresh token, a later expiry, and the host's account when both carry one; an expiry more than 24 hours ahead is logged as a warning and still written. It is written under Claude Code's own lock, atomically with mode 0600, and keeps every other key in the host file, such as `mcpOAuth`. The lock is not held while the container runs; if Claude Code refreshed the host login meanwhile, the runner keeps the host's login.
- A failed refresh is logged as `claude login refresh failed` with the exit status or reason only, and retried after 2 minutes. A result that is refused or cannot be written is logged as `claude login refresh rejected; host Claude login needs a new sign-in` with `event=host_login_needs_sign_in`, because the old refresh token is already revoked.

### Running jobs get the fresh login

- Every 2 minutes, and within 10 seconds of a change to the host login (a scheduled refresh, an interactive refresh or a new sign-in), the runner hands the host login on to each running job through a helper container with no network. A job copy is replaced only when it is still on the login the job started with, the host login is newer, and the account matches.

### A job never writes to the host login

- A job's copy of the login is never written back to the host. The account fields in a job copy are written by the job, so a job could otherwise plant another account's login on the host, and the owner's later interactive Claude Code conversations on the host would go to that account.
- When Claude Code inside a job rotates the login anyway, the runner logs `claude login rotated inside the job and was not written back; the host login may need a new sign-in` with `event=job_login_rotated`, once per rotation. If this happens, the next host refresh fails and the workaround from 0.4.1 applies: sign in again on the host.

### Doctor

- `our-runner doctor --config` reports a `claude-login-sync` check: the login source (file, keychain, none, or API key only), whether sync applies, whether the scheduled refresh and the account check are available, and the time to expiry in minutes. It never shows a value.

## Known issues

- The macOS keychain login and direct isolation are not synced; they keep the 0.4.1 behaviour and the workaround.
