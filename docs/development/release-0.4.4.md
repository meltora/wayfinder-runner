# Release 0.4.4: concurrent runners no longer break the host Claude login

Patch release after v0.4.3. Ticket: RUNNER-LOGIN-REFRESH-CONCURRENT-WIPE (P0). Design of the refresh itself: [runner-oauth-writeback.md](runner-oauth-writeback.md) and [release-0.4.3.md](release-0.4.3.md).

## Compatibility

- **Control plane:** no app change and no wire change.
- **Configuration:** no new key.
- **No state migration.** The runner creates `.credentials.json.refresh.flock` (mode 0600) next to the host `.credentials.json` the first time it refreshes. Leave it in place; deleting it while a runner refreshes defeats the lock.

## Release notes

### One refresh per host login, however many runners share it

- Fixes a 0.4.3 failure seen with three runner daemons on one host sharing one Claude file login (3c live run, 2026-11-27): when jobs started on all three daemons in the same second inside the 15-minute refresh window, each daemon read the same refresh token and presented it to the token endpoint at the same time. The first rotation revoked the token for the others, those refreshes failed, and the host login ended up without an access or refresh token, so the owner had to sign in again and every job failed with `OAuth session expired and could not be refreshed`.
- Before refreshing, a daemon now takes an exclusive `flock` on `.credentials.json.refresh.flock` and holds it from reading the host login until the result is written. A daemon that waits re-reads the host login after it gets the lock and skips the refresh when another daemon already renewed it. A daemon that cannot get the lock within 2 minutes skips the refresh and logs `claude login refresh skipped` with `reason=refresh_locked`; its job starts with the current host login.
- Claude Code's own lock is still taken only for the read and the write, so interactive Claude Code on the host is not blocked while the refresh container runs.

### The host login is written only when it is complete

- The runner writes the refreshed login only when the result and the merged host file both carry a non-empty access token and refresh token. Anything else is refused, the host file is left untouched, and the refusal is logged as `claude login refresh rejected; host Claude login needs a new sign-in` with `event=host_login_needs_sign_in`.
- The write stays atomic: a temporary file with mode 0600, `fsync`, rename, then `fsync` of the directory.

### Tests

- A new test starts three separate processes that refresh one host login at the same time against a fake token endpoint that rotates the refresh token and revokes it on reuse. It requires exactly one rotation, no concurrent or repeated use of a token, and a host file that holds the rotated login with its other keys intact.

## Operator guidance

- Until every runner on a host that shares a Claude file login runs 0.4.4, keep `max_concurrent: 1` and run one daemon per host login.
- If a host login was already lost, sign in again on the host (`claude`, then `/login`, with the same `CLAUDE_CONFIG_DIR` the runner uses). No runner restart is needed.

## Known issues

- Interactive Claude Code on the host does not take the runner's `flock`. If it refreshes the same login while a runner's refresh container runs, both present the same refresh token. Claude Code refreshes 5 minutes before expiry and the runner from 15 minutes before, and the refresh container has a 90-second deadline, so the overlap needs a runner refresh that starts in the last 90 seconds before Claude Code's own refresh point. If Claude Code's write lands first, the runner keeps the host's login and logs `reason=host_changed`.
- The macOS keychain login and direct isolation are not synced, as in 0.4.3.
