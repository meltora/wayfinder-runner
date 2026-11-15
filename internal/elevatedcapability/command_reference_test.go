package elevatedcapability

import (
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
