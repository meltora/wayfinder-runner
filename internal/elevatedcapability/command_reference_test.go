package elevatedcapability

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestHookCommandReason(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, "lint", "#!/bin/sh\n")
	for command, want := range map[string]Reason{
		"/usr/local/bin/format-changed --quiet":        "",
		"/usr/bin/curl -fsS https://hooks.example.com": "",
		"./scripts/format.sh":                          ReasonCommandReferencesRepositoryFile,
		"scripts/format.sh":                            ReasonCommandReferencesRepositoryFile,
		"/usr/bin/shellcheck lint":                     ReasonCommandReferencesRepositoryFile,
		filepath.Join(root, "lint"):                    ReasonCommandReferencesRepositoryFile,
		"/tmp/hook.sh":                                 ReasonCommandReferencesRepositoryFile,
		"/usr/bin/prettier --config=.prettierrc":       ReasonCommandReferencesRepositoryFile,
		"/usr/bin/cat file:///etc/passwd":              ReasonCommandReferencesRepositoryFile,
		"npm run format":                               ReasonCommandReferencesRepositoryFile,
		"/usr/bin/make lint":                           ReasonCommandReferencesRepositoryFile,
		"bash -c true":                                 ReasonReferencedFileUnverifiable,
		"node -e 1":                                    ReasonReferencedFileUnverifiable,
		"cd scripts":                                   ReasonReferencedFileUnverifiable,
		"/usr/bin/true && ./x":                         ReasonReferencedFileUnverifiable,
		"/usr/bin/true $HOME":                          ReasonReferencedFileUnverifiable,
		"/usr/bin/ls *.sh":                             ReasonReferencedFileUnverifiable,
		"/usr/bin/echo 'quoted'":                       ReasonReferencedFileUnverifiable,
		"/usr/bin/true `id`":                           ReasonReferencedFileUnverifiable,
		"FOO=1 /usr/bin/true":                          ReasonReferencedFileUnverifiable,
		"/usr/bin/env ./x":                             ReasonReferencedFileUnverifiable,
	} {
		if got := HookCommandReason(command, root); got != want {
			t.Errorf("HookCommandReason(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestHookCommandReasonRefusesReviewProbes(t *testing.T) {
	root := t.TempDir()
	for _, command := range []string{
		"busybox sh", "python3.12 -m tools.fmt", "perl5.36 x", "node22 x", "java Main", "eslint --fix",
		"prettier --write --no-config", "vite build", "webpack", "docker compose up", "pre-commit run --all-files",
		"lefthook run pre-commit", "terraform apply -auto-approve", "cmake --build build", "ninja", "Rscript script.R",
		"julia run.jl", "npm.cmd test", "format-changed --quiet",
		"/usr/bin/busybox sh", "/usr/bin/python3.12 -m tools.fmt", "/usr/bin/perl5.36 x", "/opt/node/bin/node22 x",
		"/usr/bin/java Main", "/usr/local/bin/eslint --fix", "/usr/local/bin/prettier --write --no-config",
		"/usr/local/bin/vite build", "/usr/local/bin/webpack", "/usr/bin/docker compose up",
		"/usr/local/bin/pre-commit run --all-files", "/usr/local/bin/lefthook run pre-commit",
		"/usr/local/bin/terraform apply -auto-approve", "/usr/bin/cmake --build build", "/usr/bin/ninja",
		"/usr/bin/Rscript script.R", "/usr/local/bin/julia run.jl", "/opt/node/npm.cmd test",
		"/usr/bin/python3-", "/usr/bin/PODMAN run x", "/usr/bin/deno.exe run x", "/usr/bin/tsc", "/usr/bin/gradle",
		"/usr/bin/mvn", "/usr/bin/bazel", "/usr/bin/toybox sh", "/usr/bin/nerdctl run x", "/usr/bin/husky",
		"/usr/bin/rollup", "/usr/bin/esbuild", "/usr/bin/jest", "/usr/bin/vitest", "/usr/bin/php x", "/usr/bin/lua x",
		"/usr/bin/bun x", "/usr/bin/npx x", "/usr/bin/pnpm x", "/usr/bin/yarn x", "/usr/bin/javaw Main",
		"/usr/bin/ruby x",
	} {
		if got := HookCommandReason(command, root); got == "" {
			t.Errorf("HookCommandReason(%q) accepted", command)
		}
	}
}

func TestHookCommandReasonRefusesThirdReviewProbes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", "/home/runner-probe")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	root := filepath.Join(home, "jobs", "job-1", "repo")
	for _, command := range []string{
		"/proc/self/cwd/tools/fmt",
		"/opt/fmt /proc/self/cwd/tools/fmt.py",
		"/usr/bin/run-parts /proc/self/cwd/tools",
		"/dev/fd/3",
		"/opt/fmt /dev/stdin",
		"/usr/bin/strace -o /dev/null python3 -m tools.fmt",
		"/usr/bin/taskset 1 python3 -m tools.fmt",
		"/usr/bin/prlimit python3 -m tools.fmt",
		"/usr/bin/python3.12-dbg -m tools.fmt",
		"/usr/bin/pypy3 x", "/usr/bin/perl-5.36 x", "/usr/bin/ruby3.3 x", "/usr/bin/nodejs22 x", "/usr/bin/php8.3 x",
		"/usr/bin/lua5.4 x", "/usr/lib/jvm/bin/java17 x", "/usr/bin/bash5 x", "/usr/bin/zsh-5.9 x", "/usr/bin/dash.static x",
		"/usr/bin/ksh93 x", "/usr/bin/fish3 x",
		"$HOME/.cache/fmt",
		"/home/runner-probe/.cache/fmt",
		"/home/runner-probe/.local/bin/fmt",
		"/home/runner-probe/.config/fmt",
		filepath.Join(home, "jobs", "job-1", "tools", "fmt"),
		"/opt/fmt " + filepath.Join(home, "jobs", "job-1", "x"),
		"/opt/fmt run python3",
		"/opt/fmt --exec=node",
		"/opt/fmt eslint",
	} {
		if got := HookCommandReason(command, root); got == "" {
			t.Errorf("HookCommandReason(%q) accepted", command)
		}
	}
	for _, wrapper := range []string{
		"strace", "ltrace", "taskset", "prlimit", "chrt", "nice", "ionice", "setpriv", "runuser", "su", "sudo", "doas",
		"sg", "systemd-run", "bwrap", "firejail", "gdb", "valgrind", "faketime", "timeout", "xargs", "run-parts",
		"nohup", "stdbuf", "unshare", "nsenter", "chroot", "flock", "watch", "script",
	} {
		if got := HookCommandReason("/usr/bin/"+wrapper+" /usr/local/bin/format-changed", root); got != ReasonReferencedFileUnverifiable {
			t.Errorf("wrapper %s = %q", wrapper, got)
		}
	}
	if got := HookCommandReason("/usr/local/bin/format-changed --quiet", root); got != "" {
		t.Errorf("trusted absolute tool refused: %q", got)
	}
}

func TestHookCommandReasonFollowsSymlinks(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	writeRepositoryFile(t, root, "tools/fmt", "#!/bin/sh\n")
	link := filepath.Join(t.TempDir(), "fmt")
	if err := os.Symlink(filepath.Join(root, "tools", "fmt"), link); err != nil {
		t.Fatal(err)
	}
	scope := commandScope{workDir: root, roots: []string{filepath.Clean(root)}}
	if !scope.refusedAbsolute(link) {
		t.Fatalf("symlink into the checkout accepted")
	}
	linkedRoot := filepath.Join(t.TempDir(), "linked-repo")
	if err := os.Symlink(root, linkedRoot); err != nil {
		t.Fatal(err)
	}
	resolvedScope := newCommandScope(linkedRoot, nil)
	if !resolvedScope.refusedAbsolute(filepath.Join(root, "tools", "fmt")) {
		t.Fatalf("path under the resolved checkout accepted")
	}
}

func TestNewCommandScopeIncludesRunnerDirectories(t *testing.T) {
	t.Setenv("HOME", "/home/runner-probe")
	scope := newCommandScope("/srv/state/jobs/job-1/repo", []string{"/srv/state", "/etc/runner"})
	for _, path := range []string{
		"/srv/state/identity.json", "/etc/runner/config.yaml", "/srv/state/jobs/job-1/prompt.md",
		"/home/runner-probe/bin/x", "/proc/1/root/x", "/dev/stderr",
	} {
		if !scope.refusedAbsolute(path) {
			t.Errorf("%s accepted", path)
		}
	}
	if scope.refusedAbsolute("/usr/local/bin/format-changed") {
		t.Errorf("trusted tool refused")
	}
	for _, root := range newCommandScope("/repo", nil).roots {
		if root == "/" {
			t.Fatalf("filesystem root became a refused root")
		}
	}
}

func TestHookCommandReasonRequiresAnAbsoluteProgram(t *testing.T) {
	root := t.TempDir()
	for command, want := range map[string]Reason{
		"format-changed --quiet":                                   ReasonProgramNotAbsolute,
		"/usr/local/bin/format-changed --quiet":                    "",
		"/usr/local/bin/../bin/format-changed":                     "",
		filepath.Join(root, "..", filepath.Base(root), "bin", "x"): ReasonCommandReferencesRepositoryFile,
		"/usr/bin/python3.12 x":                                    ReasonReferencedFileUnverifiable,
		"/opt/node/bin/node22 x":                                   ReasonReferencedFileUnverifiable,
		"/opt/node/npm.cmd test":                                   ReasonCommandReferencesRepositoryFile,
		"/usr/bin/eslint --fix":                                    ReasonCommandReferencesRepositoryFile,
		"/usr/bin/docker run img":                                  ReasonReferencedFileUnverifiable,
	} {
		if got := HookCommandReason(command, root); got != want {
			t.Errorf("HookCommandReason(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestNormalizedProgramName(t *testing.T) {
	for program, want := range map[string]string{
		"/usr/bin/python3.12": "python", "node22": "node", "perl5.36": "perl", "npm.cmd": "npm",
		"/x/Deno.EXE": "deno", "run.ps1": "run", "tool.bat": "tool", "python3-": "python", "7": "7",
		"ts-node": "ts-node", "Rscript": "rscript",
	} {
		if got := normalizedProgramName(program); got != want {
			t.Errorf("normalizedProgramName(%q) = %q, want %q", program, got, want)
		}
	}
}

func TestMCPCommandReasonRefusesReviewProbes(t *testing.T) {
	root := t.TempDir()
	for _, server := range []map[string]any{
		{"command": "java", "args": []string{"-cp", "lib", "Main"}},
		{"command": "/usr/bin/java", "args": []string{"-cp", "lib", "Main"}},
		{"command": "python3.12", "args": []string{"-m", "server"}},
		{"command": "/usr/bin/python3.12", "args": []string{"-m", "server"}},
		{"command": "docker", "args": []string{"run", "-i", "mcp/server"}},
		{"command": "/usr/bin/docker", "args": []string{"run", "-i", "mcp/server"}},
		{"command": "docs-mcp", "args": []string{"--stdio"}},
	} {
		if got := MCPCommandReason(server, root); got == "" {
			t.Errorf("MCPCommandReason(%v) accepted", server)
		}
	}
}

func TestMCPCommandReasonRefusesThirdReviewProbes(t *testing.T) {
	t.Setenv("HOME", "/home/runner-probe")
	root := t.TempDir()
	for name, server := range map[string]map[string]any{
		"proc pythonpath": {"command": "/usr/local/bin/mcp", "env": map[string]string{"PYTHONPATH": "/proc/self/cwd"}},
		"proc in value":   {"command": "/usr/local/bin/mcp", "env": map[string]string{"OPTS": "--root=/proc/self/cwd"}},
		"home in value":   {"command": "/usr/local/bin/mcp", "env": map[string]string{"PATH": "/usr/bin:/home/runner-probe/.local/bin"}},
		"fd in value":     {"command": "/usr/local/bin/mcp", "env": map[string]string{"CONFIG": "/dev/fd/3"}},
		"strace":          {"command": "/usr/bin/strace", "args": []string{"-o", "/dev/null", "python3", "-m", "tools.fmt"}},
		"taskset":         {"command": "/usr/bin/taskset", "args": []string{"1", "python3", "-m", "tools.fmt"}},
		"prlimit":         {"command": "/usr/bin/prlimit", "args": []string{"python3", "-m", "tools.fmt"}},
		"interpreter arg": {"command": "/opt/mcp-launcher", "args": []string{"python3", "-m", "server"}},
		"dbg":             {"command": "/usr/bin/python3.12-dbg", "args": []string{"-m", "server"}},
		"proc command":    {"command": "/proc/self/cwd/bin/mcp"},
		"home command":    {"command": "/home/runner-probe/.cache/mcp"},
	} {
		if got := MCPCommandReason(server, root); got == "" {
			t.Errorf("%s: MCPCommandReason accepted", name)
		}
	}
}

func TestHookCommandReasonRefusesControlAndUnicodeWhitespace(t *testing.T) {
	root := t.TempDir()
	for _, command := range []string{
		"/usr/local/bin/format-changed ./x",
		"/usr/local/bin/format-changed --quiet",
		"/usr/local/bin/format-changed​",
		"/usr/local/bin/format-changed\v--quiet",
		"/usr/local/bin/format-changed\f--quiet",
		"/usr/local/bin/format-changed\x00",
		"/usr/local/bin/format-changed\x1b[0m",
		"/usr/local/bin/format-changed\u0085--quiet",
	} {
		if got := HookCommandReason(command, root); got != ReasonReferencedFileUnverifiable {
			t.Errorf("HookCommandReason(%q) = %q", command, got)
		}
	}
	if got := HookCommandReason("/usr/local/bin/format-changed\t--quiet", root); got != "" {
		t.Errorf("tab separated command refused: %q", got)
	}
	for name, server := range map[string]map[string]any{
		"command": {"command": "/usr/local/bin/docs-mcp "},
		"arg":     {"command": "/usr/local/bin/docs-mcp", "args": []string{"--stdio "}},
		"env":     {"command": "/usr/local/bin/docs-mcp", "env": map[string]string{"TOKEN": "abc\x00"}},
		"env key": {"command": "/usr/local/bin/docs-mcp", "env": map[string]string{"TOKEN​": "abc"}},
	} {
		if got := MCPCommandReason(server, root); got != ReasonReferencedFileUnverifiable {
			t.Errorf("%s: MCPCommandReason = %q", name, got)
		}
	}
}

func TestNameReasonFollowsSymlinkTargets(t *testing.T) {
	targets := t.TempDir()
	links := t.TempDir()
	for link, target := range map[string]string{
		"formatter": "bash", "rbash": "bash", "lint": "vim.basic", "view-tool": "python3.12", "build": "make",
	} {
		writeRepositoryFile(t, targets, target, "")
		if err := os.Symlink(filepath.Join(targets, target), filepath.Join(links, link)); err != nil {
			t.Fatal(err)
		}
		if got := nameReason(filepath.Join(links, link)); got == "" {
			t.Errorf("symlink %s -> %s accepted", link, target)
		}
	}
	writeRepositoryFile(t, targets, "format-changed", "")
	if err := os.Symlink(filepath.Join(targets, "format-changed"), filepath.Join(links, "fmt")); err != nil {
		t.Fatal(err)
	}
	if got := nameReason(filepath.Join(links, "fmt")); got != "" {
		t.Errorf("symlink to a trusted tool refused: %q", got)
	}
}

func TestNameReasonRefusesEditorsAndMacroProcessors(t *testing.T) {
	for _, program := range []string{
		"/usr/bin/rbash", "/usr/bin/ex", "/usr/bin/rview", "/usr/bin/view", "/usr/bin/vi", "/usr/bin/vim",
		"/usr/bin/vim.basic", "/usr/bin/vim.tiny", "/usr/bin/rvim", "/usr/bin/gvim", "/usr/bin/nvim",
		"/usr/bin/m4", "/usr/bin/emacs", "/usr/bin/emacs-nox", "/usr/bin/ed", "/usr/bin/red", "/usr/bin/find",
		"/usr/bin/rzsh", "/usr/bin/rksh", "/usr/bin/vimdiff", "/usr/bin/busybox.static",
	} {
		if got := nameReason(program); got != ReasonReferencedFileUnverifiable {
			t.Errorf("nameReason(%q) = %q", program, got)
		}
	}
}

func TestResolveRefusesHooksOnDecisionEvents(t *testing.T) {
	for _, event := range []string{"PreToolUse", "UserPromptSubmit", "PermissionRequest", "PreCompact"} {
		root := t.TempDir()
		declaration := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"/usr/local/bin/format-changed"}]}],"` +
			event + `":[{"hooks":[{"type":"command","command":"/usr/local/bin/format-changed"}]}]}}`
		writeRepositoryFile(t, root, ".claude/hooks/format.json", declaration)
		resolution := resolveFor(root, grant("ecr_hook", v1.ElevatedCapabilityHook, ".claude/hooks/format.json", digestOf(declaration)))
		if got := reasonsByID(resolution)["ecr_hook"]; got != ReasonHookEventNotAllowed || len(resolution.Grants.Hooks) != 0 {
			t.Errorf("%s hook = %s %#v", event, got, resolution.Grants.Hooks)
		}
	}
	for _, event := range []string{"PostToolUse", "Stop", "SubagentStop", "SessionStart", "SessionEnd", "Notification"} {
		root := t.TempDir()
		declaration := `{"hooks":{"` + event + `":[{"hooks":[{"type":"command","command":"/usr/local/bin/format-changed"}]}]}}`
		writeRepositoryFile(t, root, ".claude/hooks/format.json", declaration)
		resolution := resolveFor(root, grant("ecr_hook", v1.ElevatedCapabilityHook, ".claude/hooks/format.json", digestOf(declaration)))
		if got := reasonsByID(resolution)["ecr_hook"]; got != ReasonApplied {
			t.Errorf("%s hook = %s", event, got)
		}
	}
}

func TestMCPCommandReason(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, "src/server.js", "")
	writeRepositoryFile(t, root, "src2", "")
	for name, tc := range map[string]struct {
		server map[string]any
		want   Reason
	}{
		"external binary": {map[string]any{"command": "/usr/local/bin/docs-mcp", "args": []string{"--stdio"}}, ""},
		"remote":          {map[string]any{"url": "https://mcp.example.com"}, ""},
		"script argument": {map[string]any{"command": "/usr/bin/node", "args": []string{"src/server.js"}}, ReasonReferencedFileUnverifiable},
		"repo binary":     {map[string]any{"command": "./bin/mcp"}, ReasonCommandReferencesRepositoryFile},
		"repo argument":   {map[string]any{"command": "/usr/local/bin/mcp", "args": []string{"--root", "src2"}}, ReasonCommandReferencesRepositoryFile},
		"npx":             {map[string]any{"command": "npx", "args": []string{"-y", "pkg"}}, ReasonCommandReferencesRepositoryFile},
		"expansion":       {map[string]any{"command": "/usr/local/bin/mcp", "args": []string{"${HOME}"}}, ReasonReferencedFileUnverifiable},
		"env preload": {map[string]any{"command": "/usr/local/bin/mcp", "env": map[string]string{
			"NODE_OPTIONS": "--require ./hook.js",
		}}, ReasonCommandReferencesRepositoryFile},
		"env path": {map[string]any{"command": "/usr/local/bin/mcp", "env": map[string]string{
			"PYTHONPATH": "/opt/lib:src2",
		}}, ReasonCommandReferencesRepositoryFile},
		"env token": {map[string]any{"command": "/usr/local/bin/mcp", "env": map[string]string{"TOKEN": "abc"}}, ""},
	} {
		if got := MCPCommandReason(tc.server, root); got != tc.want {
			t.Errorf("%s: MCPCommandReason = %q, want %q", name, got, tc.want)
		}
	}
}

func TestResolveRefusesHooksThatRunRepositoryCode(t *testing.T) {
	root := t.TempDir()
	declaration := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"./scripts/format.sh"}]}]}}`
	writeRepositoryFile(t, root, ".claude/hooks/format.json", declaration)
	writeRepositoryFile(t, root, "scripts/format.sh", "#!/bin/sh\nexit 0\n")
	resolution := resolveFor(root, grant("ecr_hook", v1.ElevatedCapabilityHook, ".claude/hooks/format.json", digestOf(declaration)))
	if got := reasonsByID(resolution)["ecr_hook"]; got != ReasonCommandReferencesRepositoryFile {
		t.Fatalf("hook referencing repository script = %s", got)
	}
	if len(resolution.Grants.Hooks) != 0 {
		t.Fatalf("hook enabled: %#v", resolution.Grants.Hooks)
	}
}

func TestResolveIgnoresAgentRewritesAfterApproval(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, ".claude/hooks/format.json", testHook)
	resolution := resolveFor(root, grant("ecr_hook", v1.ElevatedCapabilityHook, ".claude/hooks/format.json", digestOf(testHook)))
	if got := reasonsByID(resolution)["ecr_hook"]; got != ReasonApplied {
		t.Fatalf("approved hook = %s", got)
	}
	writeRepositoryFile(t, root, ".claude/hooks/format.json", `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"/usr/bin/evil"}]}]}}`)
	writeRepositoryFile(t, root, "usr/local/bin/format-changed", "#!/bin/sh\nevil\n")
	commands := resolution.Grants.Hooks["PostToolUse"][0]["hooks"].([]map[string]any)
	if commands[0]["command"] != "/usr/local/bin/format-changed --quiet" {
		t.Fatalf("resolved hook changed after rewrite: %#v", commands)
	}
	again := resolveFor(root, grant("ecr_hook", v1.ElevatedCapabilityHook, ".claude/hooks/format.json", digestOf(testHook)))
	if got := reasonsByID(again)["ecr_hook"]; got != ReasonDigestMismatch || len(again.Grants.Hooks) != 0 {
		t.Fatalf("rewritten declaration = %s %#v", got, again.Grants.Hooks)
	}
}

func TestResolveRefusesInvalidGrantShapesPerGrant(t *testing.T) {
	root := t.TempDir()
	writeRepositoryFile(t, root, ".mcp.json", testMCP)
	good := grant("ecr_good", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	kind := grant("ecr_kind", "plugin", ".mcp.json", digestOf(testMCP))
	dots := grant("ecr_dots", v1.ElevatedCapabilityMCPServer, "a/../.mcp.json", digestOf(testMCP))
	slash := grant("ecr_slash", v1.ElevatedCapabilityHook, `.claude\hooks\x.json`, digestOf(testMCP))
	long := grant("ecr_long", v1.ElevatedCapabilityNetworkAccess, strings.Repeat("a", v1.MaxElevatedCapabilityKeyBytes-2)+".io", digestOf("x"))
	digest := grant("ecr_digest", v1.ElevatedCapabilityMCPServer, ".mcp.json", strings.ToUpper(digestOf(testMCP)))
	expiry := grant("ecr_expiry", v1.ElevatedCapabilityMCPServer, ".mcp.json", digestOf(testMCP))
	expiry.ExpiresAt = time.Time{}
	resolution := resolveFor(root, good, kind, dots, slash, long, digest, expiry)
	got := reasonsByID(resolution)
	want := map[string]Reason{
		"ecr_good": ReasonApplied, "ecr_kind": ReasonDeclarationInvalid, "ecr_dots": ReasonDeclarationInvalid,
		"ecr_slash": ReasonDeclarationInvalid, "ecr_long": ReasonDeclarationInvalid, "ecr_digest": ReasonDeclarationInvalid,
		"ecr_expiry": ReasonDeclarationInvalid,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons = %v", got)
	}
	for _, outcome := range resolution.Outcomes {
		if outcome.Reason == ReasonDeclarationInvalid && (outcome.Key != "" || outcome.Digest != "") {
			t.Fatalf("invalid grant echoed untrusted fields: %+v", outcome)
		}
	}
}
