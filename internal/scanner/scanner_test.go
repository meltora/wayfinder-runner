package scanner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"golang.org/x/sys/unix"
)

func write(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	writeBytes(t, root, rel, []byte(content), mode)
}

func writeBytes(t *testing.T, root, rel string, content []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func byPath(inventory Inventory, path string) *v1.AgentConfigDiscoveryItem {
	for index := range inventory.Items {
		if inventory.Items[index].Path == path {
			return &inventory.Items[index]
		}
	}
	return nil
}

func TestScanMatchesTheExactCatalogAndSortsCanonicalPaths(t *testing.T) {
	root := t.TempDir()
	write(t, root, "z/CLAUDE.md", "# Claude", 0o644)
	write(t, root, "AGENTS.md", "# Codex", 0o644)
	write(t, root, ".agents/skills/review/SKILL.md", "# Review", 0o644)
	write(t, root, ".claude/commands/build.md", "# Build", 0o644)
	write(t, root, ".claude/agents/reviewer.md", "# Reviewer", 0o644)
	write(t, root, ".codex/config.toml", "model = 'gpt-5'", 0o644)
	write(t, root, ".mcp.json", `{"mcpServers":{}}`, 0o644)
	write(t, root, ".claude/hooks/pre-tool.sh", "#!/bin/sh\nexit 0", 0o755)
	write(t, root, "DEVELOPMENT.md", "# Develop", 0o644)
	write(t, root, ".yourplatform/manifest.yaml", "version: 1", 0o644)
	write(t, root, ".opencode/plugins/tool.ts", "export default {}", 0o644)
	write(t, root, "src/hooks/use-safe.ts", "export const safe = true", 0o644)
	write(t, root, "deploy.sh", "#!/bin/sh\nexit 0", 0o755)
	write(t, root, "src/main.go", "package main", 0o644)
	write(t, root, "node_modules/dependency/AGENTS.md", "# Untrusted dependency", 0o644)

	inventory, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := byPath(inventory, "AGENTS.md"); got == nil || got.SourceType != v1.AgentConfigRepositoryInstructions || got.Decision != v1.AgentConfigDecisionNotRequired {
		t.Fatalf("AGENTS.md = %+v", got)
	}
	if got := byPath(inventory, ".mcp.json"); got == nil || got.SourceType != v1.AgentConfigMCPDeclaration || got.Decision != v1.AgentConfigDecisionPending {
		t.Fatalf(".mcp.json = %+v", got)
	}
	if got := byPath(inventory, ".claude/hooks/pre-tool.sh"); got == nil || got.SourceType != v1.AgentConfigHook || got.Classification != v1.AgentConfigClassReview {
		t.Fatalf("hook = %+v", got)
	}
	for _, ignored := range []string{"src/hooks/use-safe.ts", "deploy.sh", "src/main.go", "node_modules/dependency/AGENTS.md"} {
		if got := byPath(inventory, ignored); got != nil {
			t.Fatalf("non-catalog path %q was inventoried: %+v", ignored, got)
		}
	}
	for index := 1; index < len(inventory.Items); index++ {
		if inventory.Items[index-1].Path >= inventory.Items[index].Path {
			t.Fatalf("inventory is not strictly sorted: %+v", inventory.Items)
		}
	}
}

func TestScanRejectsSecretsBinarySymlinksAndOversizedFilesWithoutDigests(t *testing.T) {
	root := t.TempDir()
	secretValue := strings.Repeat("x", 24)
	write(t, root, "AGENTS.md", fmt.Sprintf("%s%s = %q", "api_", "key", secretValue), 0o644)
	writeBytes(t, root, ".claude/commands/binary.md", []byte{'x', 0, 'y'}, 0o644)
	write(t, root, "nested/CLAUDE.md", strings.Repeat("x", MaxFileSize+1), 0o644)
	write(t, root, "real.md", "safe", 0o644)
	if err := os.Symlink("real.md", filepath.Join(root, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "linked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../real.md", filepath.Join(root, "linked", ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".yourplatform"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, ".yourplatform", "manifest.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	databaseValue := "postgres://" + "not-uploaded"
	write(t, root, ".env", fmt.Sprintf("%s=%s", "DATABASE_URL", databaseValue), 0o644)
	write(t, root, "auth.json", fmt.Sprintf(`{"%s":"%s"}`, "access_token", "not-uploaded"), 0o644)

	inventory, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"AGENTS.md", ".claude/commands/binary.md", "nested/CLAUDE.md", ".mcp.json", "linked/.claude", ".yourplatform/manifest.json", ".env", "auth.json"} {
		item := byPath(inventory, path)
		if item == nil || item.Classification != v1.AgentConfigClassRejected || item.Decision != v1.AgentConfigDecisionRejected {
			t.Fatalf("%s = %+v", path, item)
		}
		if item.DigestSHA256 != "" {
			t.Fatalf("rejected item %s leaked a digest", path)
		}
	}
	if got := byPath(inventory, ".claude/commands/binary.md"); got.SourceType != v1.AgentConfigBinary {
		t.Fatalf("binary source type = %q", got.SourceType)
	}
	if got := byPath(inventory, ".mcp.json"); got.SourceType != v1.AgentConfigSymlink {
		t.Fatalf("symlink source type = %q", got.SourceType)
	}
	if got := byPath(inventory, "linked/.claude"); got.SourceType != v1.AgentConfigSymlink {
		t.Fatalf("catalog directory symlink = %+v", got)
	}
	if got := byPath(inventory, "auth.json"); got.SourceType != v1.AgentConfigAuthCache {
		t.Fatalf("authentication cache = %+v", got)
	}
	raw, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secretValue, databaseValue} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("inventory leaked repository content %q", forbidden)
		}
	}
}

func TestScanFailsClosedWhenInventoryExceedsTheProtocolLimit(t *testing.T) {
	root := t.TempDir()
	for index := 0; index <= MaxItems; index++ {
		write(t, root, fmt.Sprintf("packages/p%03d/AGENTS.md", index), "# Instructions", 0o644)
	}
	if _, err := Scan(root); err == nil || !strings.Contains(err.Error(), "inventory item limit") {
		t.Fatalf("Scan error = %v, want inventory limit", err)
	}
}

func TestScanFailsClosedWhenCatalogPathExceedsTheProtocolLimit(t *testing.T) {
	if err := validateCatalogPath(strings.Repeat("a", MaxPathSize+1)); err == nil || !strings.Contains(err.Error(), "catalog path exceeds") {
		t.Fatalf("path validation error = %v, want path limit", err)
	}
}

func TestBuildDiscoveryEventHasDeterministicSemanticIdentity(t *testing.T) {
	inventory := Inventory{Items: []v1.AgentConfigDiscoveryItem{{
		Path: "AGENTS.md", SourceType: v1.AgentConfigRepositoryInstructions,
		DigestSHA256: strings.Repeat("a", 64), SizeBytes: 10,
		Classification: v1.AgentConfigClassReady, Risk: v1.AgentConfigRiskLow,
		Compatibility: v1.AgentConfigCompatible, Decision: v1.AgentConfigDecisionNotRequired,
		RequestedPermissions: []string{}, Conflicts: []string{},
	}}}
	input := DiscoveryInput{
		ProjectID: "project-1", RepositoryID: "repository-1", BranchRef: "main",
		CommitSHA: strings.Repeat("b", 40), ObservedAt: time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC),
	}
	first, err := BuildDiscoveryEvent(input, inventory)
	if err != nil {
		t.Fatal(err)
	}
	input.ObservedAt = input.ObservedAt.Add(time.Minute)
	repeated, err := BuildDiscoveryEvent(input, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if first.ScanID != repeated.ScanID {
		t.Fatalf("observation time changed semantic scan identity: %s != %s", first.ScanID, repeated.ScanID)
	}
	changed := Inventory{Items: append([]v1.AgentConfigDiscoveryItem(nil), inventory.Items...)}
	changed.Items[0].DigestSHA256 = strings.Repeat("c", 64)
	different, err := BuildDiscoveryEvent(input, changed)
	if err != nil {
		t.Fatal(err)
	}
	if first.ScanID == different.ScanID {
		t.Fatal("different inventory reused a semantic scan identity")
	}
	if first.Kind != v1.EventKindAgentConfigDiscovery || first.CatalogVersion != v1.AgentConfigCatalogVersion || first.ParserVersion != ParserVersion {
		t.Fatalf("event contract = %+v", first)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"requested_permissions":null`) || strings.Contains(string(raw), `"conflicts":null`) {
		t.Fatalf("required arrays encoded as null: %s", raw)
	}
}
