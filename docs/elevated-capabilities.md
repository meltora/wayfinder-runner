# Elevated capabilities on the runner

An organization admin can approve a Claude Code hook declaration or a stdio MCP server declaration from a repository by its SHA-256 digest. The runner enables an approved declaration only when the file still matches the digest and `internal/elevatedcapability/command_reference.go` finds no command that could run repository-controlled code.

## Command checks

- The program must be an absolute path outside the checkout, its parent directory, the runner state and configuration directories, the home, cache and config directories, temporary directories and `/proc` or `/dev/fd` style device paths.
- Shells, interpreters, editors, macro processors, wrappers that run another program, and project runners are refused by name. The name is matched on the path, on its resolved symlink target, and on the part before the first `.` or `-`.
- Every argument that is not an option flag must be an `http` or `https` URL or an absolute path outside those directories. A bare word such as `macros` is refused whether or not that file exists yet, because the agent can create it after the check. Arguments after `--` are treated the same way.
- Option names may not contain `/`, `\` or `.`. An option value after `=` is split on every `=`, `,` and `:`, and each segment must meet the argument rule, so `--checkpoint-action=exec=make` and `--path=/usr/lib:` are refused. A value that is a URL is accepted whole.
- MCP server environment keys must be letters, digits and `_`, not starting with a digit. Keys that change how a program loads code or configuration are refused outright, compared case-insensitively: for example `LD_*`, `DYLD_*`, `BASH_ENV`, `ENV`, `BASH_FUNC_*`, `NODE_*`, `NPM_CONFIG_*`, `PYTHON*`, `PERL*`, `RUBY*`, `GIT_*`, `XDG_*`, `JAVA_*`, `HOME`, `ZDOTDIR`, `SHELL`, `EDITOR`, `TMPDIR` and `CLASSPATH`. Keys ending in `PATH` or `_DIRS`, including `PATH` itself, hold a colon-separated list in which every segment must be a non-empty absolute path outside the refused directories. Keys ending in `_DIR`, `_HOME`, `_FILE` or `_ROOT` must be one such absolute path. Other values may not name a path under the refused directories or a relative path.
- Hook commands may not contain shell metacharacters, control characters, invalid UTF-8 or any whitespace other than space and tab.

## Network hosts

- A `network_access` key is a bare lowercase host or `*.` followed by one. A wildcard directly over a listed public suffix (`wildcardForbiddenSuffixes`, for example `co.uk`, `github.io`) is refused.
- A wildcard anywhere under a shared multi-tenant storage domain is refused (`wildcardForbiddenStorageDomains`): `amazonaws.com` (every S3 endpoint, regional, dash-regional and dualstack forms included), `amazonaws.com.cn`, `core.windows.net` (Azure blob, web, file and other storage endpoints), `r2.dev`, `r2.cloudflarestorage.com`, `storage.googleapis.com`, `digitaloceanspaces.com` and `backblazeb2.com`. Anyone can create a bucket under these domains, so `*.s3.amazonaws.com` or `*.my-bucket.s3.amazonaws.com` would let the agent reach storage it does not own. An exact bucket host such as `my-bucket.s3.amazonaws.com` is still accepted.
- Both lists are pinned by `testdata/elevated-capability-key-contract.json`, which the app keeps as a byte-identical copy.

## Limitation

The program name denylists (`unverifiablePrograms`, `wrapperPrograms`, `interpreterPrefixes` and `projectRunnerPrograms`) are not complete. Many tools can load configuration, plugins or scripts from the working directory, and new ones appear all the time. The checks catch common cases; they do not prove a command is safe. The admin's digest approval of the exact declaration remains the gate, and admins should approve only commands whose behaviour they know.
