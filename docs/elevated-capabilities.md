# Elevated capabilities on the runner

An organization admin can approve a Claude Code hook declaration or a stdio MCP server declaration from a repository by its SHA-256 digest. The runner enables an approved declaration only when the file still matches the digest and `internal/elevatedcapability/command_reference.go` finds no command that could run repository-controlled code.

## Command checks

- The program must be an absolute path outside the checkout, its parent directory, the runner state and configuration directories, the home, cache and config directories, temporary directories and `/proc` or `/dev/fd` style device paths.
- Shells, interpreters, editors, macro processors, wrappers that run another program, and project runners are refused by name. The name is matched on the path, on its resolved symlink target, and on the part before the first `.` or `-`.
- Every argument that is not an option flag must be an `http` or `https` URL or an absolute path outside those directories. A bare word such as `macros` is refused whether or not that file exists yet, because the agent can create it after the check. Arguments after `--` are treated the same way.
- Option names may not contain `/`, `\` or `.`. An option value after `=` is split on every `=`, `,` and `:`, and each segment must meet the argument rule, so `--checkpoint-action=exec=make` and `--path=/usr/lib:` are refused. A value that is a URL is accepted whole.
- MCP server environment keys must be letters, digits and `_`, not starting with a digit. Keys that change how a program loads code or configuration are refused outright, compared case-insensitively: for example `LD_*`, `DYLD_*`, `BASH_ENV`, `ENV`, `BASH_FUNC_*`, `NODE_*`, `NPM_CONFIG_*`, `PYTHON*`, `PERL*`, `RUBY*`, `GIT_*`, `XDG_*`, `JAVA_*`, `HOME`, `ZDOTDIR`, `SHELL`, `EDITOR`, `TMPDIR` and `CLASSPATH`. Keys ending in `PATH` or `_DIRS`, including `PATH` itself, hold a colon-separated list in which every segment must be a non-empty absolute path outside the refused directories. Keys ending in `_DIR`, `_HOME`, `_FILE` or `_ROOT` must be one such absolute path. Other values may not name a path under the refused directories or a relative path.
- Hook commands may not contain shell metacharacters, control characters, invalid UTF-8 or any whitespace other than space and tab.

## Repository binding

Grants travel in `elevated_capabilities` with `schema_version` 2. A `hook` or `mcp_server` grant carries the `repository_id` of the repository whose declaration the admin approved. The runner reads the declaration only from that repository's checkout, so the same path in another repository of the job is never enabled. A declaration grant without `repository_id` is refused with `repository_not_bound`, and one whose repository is not checked out in this job with `repository_not_in_job`. `network_access` grants are not tied to a repository. Every checkout of the job, the workspace root and the runner state directory count as refused directories for the command checks.

## Agent Pack binding

A `hook` or `mcp_server` grant for a declaration shipped in the job's Agent Pack carries `agent_pack_digest` (64 lowercase hex) instead of `repository_id`. The runner reads the declaration only from the installed pack directory (`<job>/pack`) and only when the job's `agent_pack.digest` equals the grant's digest; otherwise it refuses the grant with `agent_pack_not_in_job`. The key follows the same contract as a repository grant, so a pack hook declaration lives under `.claude/hooks/` and ends in `.json`. A grant that carries both `repository_id` and `agent_pack_digest`, a malformed digest, or a `network_access` grant with a pack digest is refused with `declaration_invalid`. The pack directory is a refused directory for the command checks.

## Ungranted declarations

Claude Code gets only what the runner hands it, so an ungranted hook or MCP server in the pack or the checkout does not run.

- MCP servers: the runner passes `--mcp-config <ConfigDir>/claude-mcp.json --strict-mcp-config`. That file holds the `platform` server and granted servers only. The Claude Code CLI reference says `--strict-mcp-config` skips servers from `.mcp.json`, `~/.claude.json` and plugins. A probe with Claude Code 2.1.325 confirmed that a checkout `.mcp.json` server did not start.
- Checkout settings: the runner passes `--setting-sources user` (plan jobs: `--restricted --setting-sources ""`) and its own `--settings <ConfigDir>/claude-settings.json`. Settings and hooks in the checkout's `.claude/settings.json` and `.claude/settings.local.json` are not loaded. The probe confirmed that checkout `SessionStart` and `UserPromptSubmit` hooks did not run.
- Agent Pack plugin: Claude Code registers a plugin's hooks as soon as it loads the plugin. Before this change the runner passed the installed pack directory as `--plugin-dir`, and the probe showed the pack's `hooks/hooks.json` running without a grant. The runner now builds a plugin view in `<ConfigDir>/claude-plugin` (`internal/provider/claude_plugin_view.go`) and passes that directory instead. The pack itself is readable through a `Read(//<pack>/**)` permission rule and is not passed as `--add-dir`, because a write root on the read-only pack mount broke every sandboxed Bash command. The view is rebuilt every time the spec is built. It contains:
  - `.claude-plugin/plugin.json` with only `name` (required, a non-empty string), `version`, `description` and `author`. A manifest that is not a JSON object, repeats a key or has a mistyped field fails the job start.
  - regular files under `skills/`, `commands/` and `agents/`. Symbolic links are skipped, and executable bits are kept read-only.
  - nothing else. `hooks/hooks.json`, manifest `hooks`, `mcpServers`, `lspServers`, `.lsp.json`, monitors, `bin/`, the plugin `settings.json`, `outputStyles` and custom component paths are left out.
- A skill directory or command file is left out when any of its Markdown files has YAML front matter that declares `hooks` (in any letter case). It is also left out when the front matter cannot be parsed. Claude Code ignores `hooks` in plugin agent front matter, so agents are copied as they are.
- Granted pack hooks still come only from `.claude/hooks/*.json` through `--settings`.

Not covered: the host user's own `~/.claude/settings.json` and plugins still load through the `user` source; they are operator configuration. The Agent SDK documentation says the `project` source also carries the checkout `CLAUDE.md`, skills and subagents, so under `--setting-sources user` they are probably not loaded either. This has not been confirmed for the CLI.

## Rollout

Deploy the control plane before the runner: the control plane's strict capability manifest schema must know `protocol_features` before a runner reports it, or the whole manifest is refused. Before deploying a runner that only reads `schema_version` 2 grants, drain `start_job` commands that were queued with `schema_version` 1 grants; a queued v1 command fails validation on the new runner.

## Docker isolation

Under Docker isolation the agent runs in the job image, not on the host, so the runner cannot check that an absolute program path names the same file inside the container. It refuses every hook grant and every stdio MCP server (one with `command`) with `container_program_unverifiable`. Remote MCP servers (`url` only) and `network_access` grants still apply. The container home `/home/runner` is also a refused directory.

## Outcome reporting

After the job start evidence is recorded, the runner sends one `elevated_capability_outcomes` event (`schema_version` 1) for a job that carried grants. It is manifest bound like `run_network_policy`, carries `agent_run_authority` when the job has one, and lists every grant as `{request_id, applied, reason}`, where `applied` is true exactly when `reason` is `applied`. The reasons are pinned by `v1.ElevatedCapabilityOutcomeReasons`, which a daemon test keeps equal to the resolver's `elevatedcapability.Reasons`. `content_digest_sha256` is the command content digest of `{schema_version, outcomes}`. The event uses a stable id, `elevated-capability-outcomes-` plus the job start suffix, so a resend is deduplicated. Kinds and keys are not sent; the control plane already knows them from the request ids.

## Network hosts

- A `network_access` key is a bare lowercase host or `*.` followed by one. A wildcard directly over a listed public suffix (`wildcardForbiddenSuffixes`, for example `co.uk`, `github.io`) is refused.
- A wildcard anywhere under a shared multi-tenant storage domain is refused (`wildcardForbiddenStorageDomains`): `amazonaws.com` (every S3 endpoint, regional, dash-regional and dualstack forms included), `amazonaws.com.cn`, `core.windows.net` (Azure blob, web, file and other storage endpoints), `r2.dev`, `r2.cloudflarestorage.com`, `storage.googleapis.com`, `digitaloceanspaces.com` and `backblazeb2.com`. Anyone can create a bucket under these domains, so `*.s3.amazonaws.com` or `*.my-bucket.s3.amazonaws.com` would let the agent reach storage it does not own. An exact bucket host such as `my-bucket.s3.amazonaws.com` is still accepted.
- Both lists are pinned by `testdata/elevated-capability-key-contract.json`, which the app keeps as a byte-identical copy.

## Limitation

The program name denylists (`unverifiablePrograms`, `wrapperPrograms`, `interpreterPrefixes` and `projectRunnerPrograms`) are not complete. Many tools can load configuration, plugins or scripts from the working directory, and new ones appear all the time. The checks catch common cases; they do not prove a command is safe. The admin's digest approval of the exact declaration remains the gate, and admins should approve only commands whose behaviour they know.
