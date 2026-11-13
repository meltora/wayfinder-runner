# Agent activity stream

Runner side of app slice 23 (`docs/development/agent-activity-stream.md` in the app repository holds the contract and owner decision).

## Opt-in

The runner advertises `agent_activity_transport_version: 1` in its signed capability manifest. The app adds `agent_activity` to a job spec only for projects with the stream enabled:

```json
{"schema_version": 1, "max_events": 200, "max_events_per_minute": 30, "max_message_bytes": 280}
```

Bounds: `max_events` 1..500, `max_events_per_minute` 1..60, `max_message_bytes` 64..480. Without the field the runner sends no activity.

## Event

Kind `agent_activity`, manifest bound like `sanitized_log`, with required `agent_run_authority`, then `sequence`, `activity_type`, `message`, `truncated`, `redaction` and `content_digest_sha256`. The runner publishes only while the lease is valid and start evidence is recorded.

- `assistant_message`: assistant text blocks (Claude `assistant` text, Codex `item.completed` `agent_message`, OpenCode `text` parts).
- `tool_step`: a tool name from the runner allowlist (`Using tool: Bash`), otherwise `Using an MCP tool` or `Using a tool`. Codex items map to fixed phrases (`Ran a shell command`, `Changed files`). Tool input and output are never read.
- `status`: session start, turn or step finished, turn error.
- `limit_reached`: one notice when the total cap is reached (it takes the last slot, then the stream stops), and at most one per minute when the rate cap drops messages.

Reasoning and thinking blocks are never forwarded.

## Sanitizing

`internal/agentactivity` replaces private keys, credentialed URLs, bearer tokens, known token prefixes, secret assignments, e-mail addresses and absolute paths with `[redacted]`, records the count and sorted categories, strips control characters, collapses whitespace and truncates on a UTF-8 boundary with `…`. Policy version `runner-activity-redaction-1`.

## Digest

`content_digest_sha256` is `CommandContentDigest` of `{activity_type, sequence, message, truncated, redaction}`: compact JSON with keys sorted at every level, no HTML escaping, SHA-256 hex.
