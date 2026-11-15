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
