package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"golang.org/x/sys/unix"
)

func TestScanAssignsAPackClassAndPublicReasonCodeToEveryItem(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "# Portable", 0o644)
	write(t, root, "DEVELOPMENT.md", "# Develop", 0o644)
	write(t, root, "z/CLAUDE.md", "# Claude", 0o644)
	write(t, root, ".agents/skills/review/SKILL.md", "# Review", 0o644)
	write(t, root, ".claude/commands/build.md", "# Build", 0o644)
	write(t, root, ".claude/agents/reviewer.md", "# Reviewer", 0o644)
	write(t, root, ".codex/config.toml", "model = 'gpt-5'", 0o644)
	write(t, root, ".mcp.json", `{"mcpServers":{}}`, 0o644)
	write(t, root, ".claude/hooks/pre-tool.sh", "#!/bin/sh\nexit 0", 0o755)
	write(t, root, ".claude/skills/run/SKILL.md", "#!/bin/sh\nexit 0", 0o755)
	write(t, root, ".yourplatform/manifest.yaml", "version: 1", 0o644)
	write(t, root, "big/AGENTS.md", strings.Repeat("x", MaxFileSize+1), 0o644)
	writeBytes(t, root, ".claude/commands/binary.md", []byte{'x', 0, 'y'}, 0o644)
	write(t, root, "secret/AGENTS.md", fmt.Sprintf("%s%s = %q", "api_", "key", strings.Repeat("x", 24)), 0o644)
	write(t, root, ".env", "A=b", 0o644)
	write(t, root, "auth.json", "{}", 0o644)
	write(t, root, "real.md", "safe", 0o644)
	if err := os.Symlink("real.md", filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, ".opencode", "config.json"), 0o600); err != nil {
		t.Fatal(err)
	}

	inventory, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"AGENTS.md":                      {"portable_guidance", "instructions_portable"},
		"DEVELOPMENT.md":                 {"portable_guidance", "instructions_portable"},
		"z/CLAUDE.md":                    {"provider_overlay", "instructions_provider_specific"},
		".agents/skills/review/SKILL.md": {"portable_guidance", "skill_portable"},
		".claude/commands/build.md":      {"provider_overlay", "command_provider_specific"},
		".claude/agents/reviewer.md":     {"provider_overlay", "agent_definition_provider_specific"},
		".codex/config.toml":             {"provider_overlay", "provider_settings"},
		".mcp.json":                      {"elevated", "mcp_server"},
		".claude/hooks/pre-tool.sh":      {"elevated", "hook_program"},
		".claude/skills/run/SKILL.md":    {"elevated", "executable_file"},
		".yourplatform/manifest.yaml":    {"manual_review", "platform_manifest"},
		"big/AGENTS.md":                  {"unsupported", "exceeds_scan_size_limit"},
		".claude/commands/binary.md":     {"rejected", "binary_content"},
		"secret/AGENTS.md":               {"rejected", "secret_content"},
		".env":                           {"rejected", "credential_file"},
		"auth.json":                      {"rejected", "auth_cache"},
		"CLAUDE.md":                      {"rejected", "symlink"},
		".opencode/config.json":          {"rejected", "special_file"},
	}
	for path, expected := range want {
		item := byPath(inventory, path)
		if item == nil {
			t.Fatalf("%s missing from inventory", path)
		}
		if string(item.PackClass) != expected[0] || string(item.PackReasonCode) != expected[1] {
			t.Fatalf("%s pack = %s/%s, want %s/%s", path, item.PackClass, item.PackReasonCode, expected[0], expected[1])
		}
		rule, known := v1.AgentConfigPackReasonRules[item.PackReasonCode]
		if !known || rule.Class != item.PackClass {
			t.Fatalf("%s reason code %s is not in the public catalog for %s", path, item.PackReasonCode, item.PackClass)
		}
		if rule.Rejected != (item.Classification == v1.AgentConfigClassRejected) {
			t.Fatalf("%s reason code %s disagrees with classification %s", path, item.PackReasonCode, item.Classification)
		}
		allowed := false
		for _, sourceType := range rule.SourceTypes {
			allowed = allowed || sourceType == item.SourceType
		}
		if !allowed {
			t.Fatalf("%s reason code %s does not apply to %s", path, item.PackReasonCode, item.SourceType)
		}
	}
	for _, item := range inventory.Items {
		if item.PackClass == "" || item.PackReasonCode == "" {
			t.Fatalf("%s has no pack class", item.Path)
		}
	}
}

func TestGitTreeScanAssignsPackClasses(t *testing.T) {
	item := classifiedItem(".mcp.json", catalogMatch{sourceType: v1.AgentConfigMCPDeclaration}, 10)
	if item.PackClass != v1.AgentConfigPackElevated || item.PackReasonCode != v1.AgentConfigPackReasonMCPServer {
		t.Fatalf("classified item pack = %s/%s", item.PackClass, item.PackReasonCode)
	}
	rejected := rejectedItem("x.md", v1.AgentConfigSkill, 10, "", v1.AgentConfigPackReasonExceedsScanSizeLimit)
	if rejected.PackClass != v1.AgentConfigPackUnsupported || rejected.Reason == "" {
		t.Fatalf("rejected item pack = %+v", rejected)
	}
}
